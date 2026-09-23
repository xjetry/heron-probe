// Package probe 是探测任务、分配与版本号的管理写入口和内存缓存。
//
// mu 保证每次内存读取都对应完整的已提交状态；管理写入先提交 store 事务，再发布内存快照。
// writeMu 串行化“提交到发布”，防止两次写入以相反顺序更新缓存；提交与发布之间读侧仍可看到旧快照。
// 管理接口修改任务或分配会在事务内递增版本；删除节点顺带清分配不递增，因为被删节点不能再上报，其他节点清单未变。
// Forget 也取 writeMu，一个稍早开始的 Save 不得在内存里复活已删节点的分配。
package probe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"sync"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/probelimit"
	"google.golang.org/protobuf/proto"
)

var ErrInvalid = errors.New("invalid probe task")

type Detail struct {
	Task    *probev1.ProbeTask
	NodeIDs []int64
}

type Registry struct {
	store *store.Store
	log   *slog.Logger

	writeMu sync.Mutex // 锁序 writeMu → mu
	mu      sync.RWMutex
	version uint64
	tasks   map[uint64]*probev1.ProbeTask
	byNode  map[int64]map[uint64]struct{}
	nodesOf map[uint64][]int64
}

func New(st *store.Store, log *slog.Logger) *Registry {
	return &Registry{store: st, log: log, tasks: map[uint64]*probev1.ProbeTask{}, byNode: map[int64]map[uint64]struct{}{}, nodesOf: map[uint64][]int64{}}
}

// Load 从库重建缓存；只在启动时调用一次。
func (r *Registry) Load(ctx context.Context) error {
	version, recs, err := r.store.LoadProbeTasks(ctx)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.version = version
	r.tasks, r.byNode, r.nodesOf = map[uint64]*probev1.ProbeTask{}, map[int64]map[uint64]struct{}{}, map[uint64][]int64{}
	for _, rec := range recs {
		r.put(rec.Task, rec.NodeIDs)
	}
	return nil
}

// 缓存拥有独立对象；保存结果和查询结果都不能让调用方绕过版本事务修改缓存。
func (r *Registry) put(t *probev1.ProbeTask, nodeIDs []int64) {
	r.tasks[t.Id] = proto.Clone(t).(*probev1.ProbeTask)
	r.nodesOf[t.Id] = slices.Clone(nodeIDs)
	for _, node := range nodeIDs {
		if r.byNode[node] == nil {
			r.byNode[node] = map[uint64]struct{}{}
		}
		r.byNode[node][t.Id] = struct{}{}
	}
}

func (r *Registry) remove(id uint64) {
	for _, node := range r.nodesOf[id] {
		delete(r.byNode[node], id)
		if len(r.byNode[node]) == 0 {
			delete(r.byNode, node)
		}
	}
	delete(r.tasks, id)
	delete(r.nodesOf, id)
}

func (r *Registry) Version() uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.version
}

// TasksFor 返回按 id 升序的任务与版本；空清单也必须下发，agent 才能停止已撤销的任务。
func (r *Registry) TasksFor(nodeID int64) *probev1.ProbeTasks {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := &probev1.ProbeTasks{Version: r.version}
	for id := range r.byNode[nodeID] {
		out.Tasks = append(out.Tasks, proto.Clone(r.tasks[id]).(*probev1.ProbeTask))
	}
	sort.Slice(out.Tasks, func(i, j int) bool { return out.Tasks[i].Id < out.Tasks[j].Id })
	return out
}

func (r *Registry) Assigned(nodeID int64, taskID uint64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.byNode[nodeID][taskID]
	return ok
}

// List 包含未分配的任务；版本与任务在同一个读锁下取得，不能混合两次提交的状态。
func (r *Registry) List() (uint64, []Detail) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Detail
	for id, task := range r.tasks {
		out = append(out, Detail{Task: proto.Clone(task).(*probev1.ProbeTask), NodeIDs: slices.Clone(r.nodesOf[id])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Task.Id < out[j].Task.Id })
	return r.version, out
}

func dedupSorted(ids []int64) []int64 {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}

// Save 的内存发布只发生在事务成功后，拒绝的保存不能改变任务、分配或版本。
func (r *Registry) Save(ctx context.Context, t *probev1.ProbeTask, nodeIDs []int64) (Detail, uint64, error) {
	if err := probelimit.CheckTask(t); err != nil {
		return Detail{}, 0, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	ids := dedupSorted(nodeIDs)
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	saved, version, err := r.store.SaveProbeTask(ctx, t, ids)
	if err != nil {
		return Detail{}, 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.remove(saved.Id)
	r.put(saved, ids)
	r.version = version
	return Detail{Task: saved, NodeIDs: ids}, version, nil
}

func (r *Registry) Delete(ctx context.Context, id uint64) (uint64, error) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	version, err := r.store.DeleteProbeTask(ctx, id)
	if err != nil {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.remove(id)
	r.version = version
	return version, nil
}

// Forget 只清分配；持久化清理由 DeleteNode 的事务负责，锁序保证较早的保存已完成发布。
func (r *Registry) Forget(nodeID int64) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	for id := range r.byNode[nodeID] {
		r.nodesOf[id] = slices.DeleteFunc(r.nodesOf[id], func(node int64) bool { return node == nodeID })
	}
	delete(r.byNode, nodeID)
}
