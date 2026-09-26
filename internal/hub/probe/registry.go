// Package probe 是探测任务、分配与版本号的管理写入口和内存缓存。
//
// mu 使本包每次发布的版本、任务与分配一起对读侧可见；它不把库侧删除节点与 Forget 合成一个事务。
// writeMu 串行化存储访问到内存发布，防止保存、删除与重载反序发布；提交与发布之间读侧仍可看到旧数据。
// 管理写入由 store 在变更事务内递增版本；DeleteNode 对分配表只删除该节点的行，不递增，其他节点的清单未变。
// 进程内删除由 auth.DeleteNode 撤销 token，再由 ingest.Forget 等待在途上报退出；离线 CLI 删除需重启运行中的 hub 才刷新缓存。
// Forget 与 Save 互斥且 Save 从提交到发布全程持 writeMu；DeleteNode 提交后调用 Forget，才能清掉较早保存发布的分配。
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

// Load 的读取与发布和其他写入口互斥，避免旧的重载快照覆盖刚发布的保存结果。
func (r *Registry) Load(ctx context.Context) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
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

// List 包含未分配的任务；版本与任务在同一个读锁下取得，不能混合两次内存发布的状态。
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

// Target 返回任务当前的种类与目标，是管理端标注历史序列的口径。任务不在清单里时 ok 为 false。
// 历史行只带 task_id，查不到即已删除，这靠两个前提：ingest 的 foldResults 只收 Assigned 为真的结果，
// 所以历史里的每个 task_id 都曾是已发布的任务；任务表的 AUTOINCREMENT 保证编号不复用，所以不会标成别的任务。
func (r *Registry) Target(id uint64) (kind probev1.ProbeKind, target string, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tasks[id]
	if !ok {
		return probev1.ProbeKind_PROBE_KIND_UNSPECIFIED, "", false
	}
	return t.Kind, t.Target, true
}

// TargetFor 是公开端标注历史序列的口径：只对当前分配给 nodeID 的任务给出种类与目标，其余 ok 为 false。
// 节点公开即公开它正在探测的目标；历史里出现、但现在不分配给该节点的任务，当前目标可能从未被该节点探测过
// （撤下后改成了内网地址、只分配给私有节点），不在公开范围内。分配与目标在同一个读锁下读出：分开两次加锁，
// 中间的 Save 可能让"已分配"与"新目标"拼在一起。
func (r *Registry) TargetFor(nodeID int64, id uint64) (kind probev1.ProbeKind, target string, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, known := r.tasks[id]
	if _, assigned := r.byNode[nodeID][id]; !known || !assigned {
		return probev1.ProbeKind_PROBE_KIND_UNSPECIFIED, "", false
	}
	return t.Kind, t.Target, true
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

// Forget 只能在 store.DeleteNode 提交成功后调用，删除调用链必须先持久化删除再清内存分配。
// 它与 Save 互斥且 Save 从提交到发布全程持 writeMu；已持锁的 Save/Delete 完成存储往返与发布前，此调用会阻塞。
// 调用方不得持有写协程回调会获取的锁：回调经 Authenticate 取 auth.mu 读锁，调用方持写锁调 Forget 即阻塞回调形成环；ingest.mu 同理。
// 若在 ingest.stateMu 下调用且正在等待存储，全体上报也会排队等待。
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
