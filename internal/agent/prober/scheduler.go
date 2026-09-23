package prober

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/probelimit"
	"google.golang.org/protobuf/proto"
)

type Scheduler struct {
	engine  Engine
	queue   *Queue
	clk     clock.Clock
	log     *slog.Logger
	Sleep   func(context.Context, time.Duration) error
	Rand    func() float64
	mu      sync.Mutex
	version uint64
	running map[uint64]*runningTask
	wg      sync.WaitGroup
}

type runningTask struct {
	task   *probev1.ProbeTask
	cancel context.CancelFunc
}

func NewScheduler(engine Engine, queue *Queue, clk clock.Clock, log *slog.Logger) *Scheduler {
	return &Scheduler{engine: engine, queue: queue, clk: clk, log: log, Rand: rand.Float64, Sleep: sleep, running: map[uint64]*runningTask{}}
}

func sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

func (s *Scheduler) Version() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version
}

// Apply 整份替换任务集；未变的任务不重启计时。字段规则由 CheckTask 保证，数量由本入口限制，
// 应用清单时为每个被拒任务留下一条 error，说明原因。
func (s *Scheduler) Apply(tasks *probev1.ProbeTasks) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version = tasks.GetVersion()
	want := map[uint64]*probev1.ProbeTask{}
	sorted := slices.SortedFunc(slices.Values(tasks.GetTasks()), func(a, b *probev1.ProbeTask) int { return cmp.Compare(a.GetId(), b.GetId()) })
	for i, t := range sorted {
		if i >= probelimit.MaxTasksPerNode {
			s.reject(t, fmt.Sprintf("more than %d tasks assigned; task dropped", probelimit.MaxTasksPerNode))
			continue
		}
		if err := probelimit.CheckTask(t); err != nil {
			s.reject(t, err.Error())
			continue
		}
		// 调度器持有独立快照，调用方复用消息不能绕过 Apply 改变正在执行的任务。
		want[t.GetId()] = proto.Clone(t).(*probev1.ProbeTask)
	}
	for id, r := range s.running {
		if t, ok := want[id]; !ok || !proto.Equal(t, r.task) {
			r.cancel()
			delete(s.running, id)
		}
	}
	for id, t := range want {
		if _, ok := s.running[id]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		s.running[id] = &runningTask{task: t, cancel: cancel}
		s.wg.Add(1)
		go s.run(ctx, t)
	}
}

func (s *Scheduler) reject(t *probev1.ProbeTask, why string) {
	s.queue.Push(Result{TaskID: t.GetId(), Outcome: Outcome{Err: why}, At: s.clk.Mono()})
	s.log.Warn("probe task rejected", "task", t.GetId(), "reason", why)
}

// 首次偏移避免齐发；后续周期从触发时刻计算，不把探测耗时累加到周期。
// CheckTask 保证超时预算不超过间隔；单任务循环串行调用引擎，停止后不发布半途结果。
func (s *Scheduler) run(ctx context.Context, t *probev1.ProbeTask) {
	defer s.wg.Done()
	interval := time.Duration(t.GetIntervalS()) * time.Second
	if err := s.Sleep(ctx, time.Duration(s.Rand()*float64(interval))); err != nil {
		return
	}
	for {
		next := s.clk.Mono() + interval
		out := s.engine.Probe(ctx, t)
		if ctx.Err() != nil {
			return
		}
		s.queue.Push(Result{TaskID: t.GetId(), Outcome: out, At: s.clk.Mono()})
		if err := s.Sleep(ctx, max(next-s.clk.Mono(), 0)); err != nil {
			return
		}
	}
}

// Stop 等待全部任务退出；调用方在此之后不得再 Apply。
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.running {
		r.cancel()
	}
	clear(s.running)
	s.wg.Wait()
}
