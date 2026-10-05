// Package updates 管理已持久化的节点更新授权；上报路径只读写内存。
package updates

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/clock"
	"github.com/xjetry/heron-probe/internal/hub/store"
	"github.com/xjetry/heron-probe/internal/update"
	"google.golang.org/protobuf/proto"
)

type Manager struct {
	st       *store.Store
	clk      clock.Clock
	log      *slog.Logger
	op       sync.Mutex
	mu       sync.Mutex
	states   map[int64]*heronv1.UpdateStatus
	observed map[int64]*heronv1.UpdateStatus
	wake     chan struct{}
	// boundAgent 是 hub 绑定的 agent 版本（spec §14.1），节点在线更新唯一可用的目标；New 之后不变。
	// API 下发的 bound_agent_version 也读它（BoundAgent），绑定只有这一个持有者。
	boundAgent string
}

// New 装配 Manager。boundAgent 是 hub 绑定的 agent 版本（spec §14.1）：hub 的 release 不携带 agent 产物，
// 节点只能更新到构建时从仓库根 AGENT_VERSION 注入的这个版本。
func New(st *store.Store, clk clock.Clock, log *slog.Logger, boundAgent string) *Manager {
	return &Manager{st: st, clk: clk, log: log, states: make(map[int64]*heronv1.UpdateStatus), observed: make(map[int64]*heronv1.UpdateStatus), wake: make(chan struct{}, 1), boundAgent: boundAgent}
}

// BoundAgent 返回 hub 绑定的 agent 版本，空串表示没有绑定。
func (m *Manager) BoundAgent() string {
	return m.boundAgent
}
func (m *Manager) Load(ctx context.Context) error {
	states, err := m.st.NodeUpdates(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.states = states
	m.mu.Unlock()
	return nil
}
func clone(s *heronv1.UpdateStatus) *heronv1.UpdateStatus {
	if s == nil {
		return &heronv1.UpdateStatus{Reason: "agent has not reported online update support"}
	}
	return proto.Clone(s).(*heronv1.UpdateStatus)
}
func (m *Manager) Snapshot(id int64) *heronv1.UpdateStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return clone(m.states[id])
}

// ActiveTasks 返回仍在进行中的节点任务（ID → 版本）。中转缓存据此按引用释放：版本不再被任何进行中的任务
// 引用即可丢弃；任务的取用计数也只在任务进行中才有意义。
func (m *Manager) ActiveTasks() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]string)
	for _, s := range m.states {
		if t := s.GetTask(); t != nil && update.ActiveState(t.State) {
			out[t.Id] = t.Version
		}
	}
	return out
}
func (m *Manager) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Observe 只在 Report 鉴权后调用。任务必须先以 dispatched 落盘，才能进入下行响应。
func (m *Manager) Observe(id int64, s *heronv1.UpdateStatus) *heronv1.UpdateTask {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.observed[id] = clone(s)
	m.signal()
	t := m.states[id].GetTask()
	if t != nil && t.State == "dispatched" && t.ExpiresAt > m.clk.Now().Unix() {
		return proto.Clone(t).(*heronv1.UpdateTask)
	}
	return nil
}

func NewRequest(version string, now time.Time) update.Request {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return update.Request{ID: hex.EncodeToString(b[:]), Version: version, ExpiresAt: now.Add(24 * time.Hour).Unix()}
}

// Start 校验目标后把节点排队进入更新。绑定检查先于一切：hub 只发与自己一起构建的 agent 版本（spec §14.1），
// 没有稳定绑定的 hub 拒绝所有节点在线更新。
func (m *Manager) Start(ctx context.Context, id int64, version string) (*heronv1.UpdateTask, error) {
	// 节点只能更新到 hub 绑定的 agent 版本（spec §14.1）：只发 hub 的 release 不带 agent 产物，别的版本号在官方
	// release 里不一定有 agent 包，也没有与这个 hub 一起跑过端到端。绑定为空（没有注入的构建）或不是正式版时，
	// 节点在线更新一律不可用——更新器只接受正式版（update.ValidVersion），空值在这里是收紧。
	if !update.ValidVersion(m.boundAgent) {
		return nil, errors.New("this hub has no stable bound agent version; node online updates are unavailable")
	}
	if version != m.boundAgent {
		return nil, fmt.Errorf("node updates must target this hub's bound agent version %s, got %s", m.boundAgent, version)
	}
	m.op.Lock()
	defer m.op.Unlock()
	s := m.Snapshot(id)
	if !s.Supported {
		return nil, errors.New("node does not support online updates: " + s.Reason)
	}
	if !update.Newer(version, s.Version) {
		return nil, errors.New("target must be a newer stable version")
	}
	if t := s.Task; t != nil && update.ActiveState(t.State) {
		return nil, errors.New("node already has an active update")
	}
	r := NewRequest(version, m.clk.Now())
	s.Task = update.TaskProto(&update.Job{Request: r, State: "queued", UpdatedAt: m.clk.Now().Unix()})
	if err := m.st.SaveNodeUpdate(ctx, id, s); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.states[id] = s
	m.mu.Unlock()
	return proto.Clone(s.Task).(*heronv1.UpdateTask), nil
}

func (m *Manager) Cancel(ctx context.Context, id int64, taskID string) error {
	m.op.Lock()
	defer m.op.Unlock()
	s := m.Snapshot(id)
	if s.Task == nil || s.Task.Id != taskID || s.Task.State != "queued" {
		return errors.New("only the matching queued update can be cancelled")
	}
	s.Task.State = "cancelled"
	s.Task.UpdatedAt = m.clk.Now().Unix()
	if err := m.st.SaveNodeUpdate(ctx, id, s); err != nil {
		return err
	}
	m.mu.Lock()
	m.states[id] = s
	m.mu.Unlock()
	return nil
}

func (m *Manager) Forget(id int64) {
	m.op.Lock()
	defer m.op.Unlock()
	m.mu.Lock()
	delete(m.states, id)
	delete(m.observed, id)
	m.mu.Unlock()
}

func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
		case <-t.C:
		}
		m.flush(ctx)
	}
}

func (m *Manager) flush(ctx context.Context) {
	m.op.Lock()
	defer m.op.Unlock()
	m.mu.Lock()
	observed := m.observed
	m.observed = make(map[int64]*heronv1.UpdateStatus)
	states := make(map[int64]*heronv1.UpdateStatus, len(m.states))
	for id, s := range m.states {
		states[id] = clone(s)
	}
	m.mu.Unlock()
	for id := range observed {
		if states[id] == nil {
			states[id] = clone(nil)
		}
	}
	for id, s := range states {
		before := proto.Clone(s)
		o, seen := observed[id]
		if seen {
			s.Supported, s.Reason, s.Version = o.Supported, o.Reason, o.Version
			if task, reported := s.Task, o.Task; task != nil && reported != nil && task.Id == reported.Id && task.Version == reported.Version && (update.ActiveState(task.State) || task.State == "unconfirmed") && task.State != "queued" {
				// 旧进程或不相干任务不能凭结果字符串宣告新程序已上报。
				if reported.State != "succeeded" || o.Version == task.Version {
					if advances(task.State, reported.State) && (task.State != reported.State || task.Error != reported.Error) {
						task.State = reported.State
						task.Error = reported.Error
						task.UpdatedAt = m.clk.Now().Unix()
					}
				}
			}
		}
		if task := s.Task; task != nil {
			if seen && update.ActiveState(task.State) && s.Supported && update.ValidVersion(s.Version) && !update.Newer(task.Version, s.Version) && (o.Task == nil || o.Task.Id != task.Id) {
				// 本机版本已到达目标但没有本任务的记录，不能冒称任务成功，也不能永久占用更新入口。
				task.State = "unconfirmed"
				task.Error = "running version already meets the target, but no matching local task result exists"
				task.UpdatedAt = m.clk.Now().Unix()
			} else if update.ActiveState(task.State) && task.ExpiresAt <= m.clk.Now().Unix() {
				// 下发后失联不能推断执行结果；本机更新器仍负责互斥与恢复，后台只结束等待。
				if task.State == "queued" {
					task.State = "expired"
				} else {
					task.State = "unconfirmed"
				}
				task.UpdatedAt = m.clk.Now().Unix()
			} else if task.State == "queued" && seen && s.Supported {
				task.State = "dispatched"
				task.UpdatedAt = m.clk.Now().Unix()
			}
		}
		if proto.Equal(before, s) {
			continue
		}
		if err := m.st.SaveNodeUpdate(ctx, id, s); err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				m.log.Error("persist node update", "node", id, "err", err)
				if seen {
					m.mu.Lock()
					if _, newer := m.observed[id]; !newer {
						m.observed[id] = o
					}
					m.mu.Unlock()
				}
			}
			continue
		}
		m.mu.Lock()
		m.states[id] = s
		m.mu.Unlock()
	}
}

// 上报可跨断线重试；同一任务的旧进度不能把已确认的后续阶段回退。
func advances(current, next string) bool {
	switch next {
	case "succeeded", "failed", "rolled_back", "expired":
		return true
	}
	if current == "unconfirmed" {
		return false
	}
	stages := map[string]int{"dispatched": 1, "downloading": 2, "stopping": 3, "installing": 4, "verifying": 5, "rolling_back": 6}
	return stages[next] != 0 && stages[next] >= stages[current]
}
