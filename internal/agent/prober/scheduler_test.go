package prober

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/clock"
	"github.com/xjetry/heron-probe/internal/probelimit"
	"github.com/xjetry/heron-probe/internal/testwait"
)

type sleepCall struct {
	ctx     context.Context
	delay   time.Duration
	release chan struct{}
}

func controlledSleep(calls chan<- sleepCall) func(context.Context, time.Duration) error {
	return func(ctx context.Context, delay time.Duration) error {
		call := sleepCall{ctx: ctx, delay: delay, release: make(chan struct{})}
		select {
		case calls <- call:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-call.release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(testwait.Bound):
		t.Fatal("timed out waiting for controlled event")
		var zero T
		return zero
	}
}

func task(id uint64) *heronv1.ProbeTask {
	return &heronv1.ProbeTask{Id: id, Kind: heronv1.ProbeKind_PROBE_KIND_ICMP, Target: "127.0.0.1", IntervalS: 5, TimeoutMs: 1000}
}

func logger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type probeCall struct {
	ctx    context.Context
	id     uint64
	target string
}

func TestApplyStartsNewStopsGoneKeepsUnchanged(t *testing.T) {
	clk := clock.NewFake(time.Unix(0, 0))
	calls := make(chan sleepCall, 32)
	probes := make(chan probeCall, 32)
	s := NewScheduler(engineFunc(func(ctx context.Context, task *heronv1.ProbeTask) Outcome {
		probes <- probeCall{ctx, task.Id, task.Target}
		return Outcome{RttUs: 1}
	}), NewQueue(QueueCap), clk, logger())
	s.Sleep, s.Rand = controlledSleep(calls), func() float64 { return .25 }
	defer s.Stop()
	s.Apply(&heronv1.ProbeTasks{Version: 1, Tasks: []*heronv1.ProbeTask{task(1), task(2), task(4)}})
	var initial []sleepCall
	for range 3 {
		c := receive(t, calls)
		if c.delay != 1250*time.Millisecond {
			t.Fatalf("initial offset=%v", c.delay)
		}
		initial = append(initial, c)
	}
	for _, c := range initial {
		close(c.release)
	}
	byID := map[uint64]probeCall{}
	for range 3 {
		c := receive(t, probes)
		byID[c.id] = c
	}
	asleep := map[context.Context]sleepCall{}
	for range 3 {
		c := receive(t, calls)
		asleep[c.ctx] = c
	}
	changed := task(2)
	changed.Target = "localhost"
	s.Apply(&heronv1.ProbeTasks{Version: 2, Tasks: []*heronv1.ProbeTask{task(1), changed, task(3)}})
	if s.Version() != 2 {
		t.Fatalf("version=%d", s.Version())
	}
	for _, id := range []uint64{2, 4} {
		if byID[id].ctx.Err() == nil {
			t.Fatalf("task %d not canceled", id)
		}
	}
	if byID[1].ctx.Err() != nil {
		t.Fatal("unchanged task canceled")
	}
	first := []sleepCall{receive(t, calls), receive(t, calls)}
	for _, c := range first {
		if c.delay != 1250*time.Millisecond {
			t.Fatalf("new offset=%v", c.delay)
		}
	}
	close(asleep[byID[1].ctx].release)
	again := receive(t, probes)
	if again.id != 1 || again.ctx != byID[1].ctx {
		t.Fatalf("unchanged task restarted: %+v", again)
	}
	c := receive(t, calls)
	if c.delay != 5*time.Second {
		t.Fatalf("unchanged interval=%v", c.delay)
	}
	for _, c := range first {
		close(c.release)
	}
	want := map[uint64]string{2: "localhost", 3: "127.0.0.1"}
	for range 2 {
		c := receive(t, probes)
		if want[c.id] != c.target {
			t.Fatalf("new task=%+v", c)
		}
		delete(want, c.id)
	}
	for range 2 {
		receive(t, calls)
	}
	s.Apply(&heronv1.ProbeTasks{Version: 3})
	s.mu.Lock()
	n := len(s.running)
	s.mu.Unlock()
	if n != 0 || s.Version() != 3 {
		t.Fatalf("empty list: running=%d version=%d", n, s.Version())
	}
}

func TestApplyRejectsInvalidAndOverLimitWithErrorResults(t *testing.T) {
	clk := clock.NewFake(time.Unix(0, 0))
	q := NewQueue(QueueCap)
	calls := make(chan sleepCall, 128)
	probes := make(chan uint64, 128)
	s := NewScheduler(engineFunc(func(_ context.Context, task *heronv1.ProbeTask) Outcome {
		probes <- task.Id
		return Outcome{RttUs: 1}
	}), q, clk, logger())
	s.Sleep, s.Rand = controlledSleep(calls), func() float64 { return 0 }
	defer s.Stop()
	var tasks []*heronv1.ProbeTask
	for id := uint64(65); id > 0; id-- {
		tasks = append(tasks, task(id))
	}
	tasks[64].IntervalS = 1
	s.Apply(&heronv1.ProbeTasks{Version: 7, Tasks: tasks})
	rs := q.Take(clk.Mono(), probelimit.MaxResultAge, probelimit.MaxResultsPerReport)
	if len(rs) != 2 || rs[0].TaskID != 1 || !strings.Contains(rs[0].Outcome.Err, "interval_s must be between 5 and 3600") ||
		rs[1].TaskID != 65 || !strings.Contains(rs[1].Outcome.Err, "more than 64 tasks assigned") ||
		rs[0].At != clk.Mono() || rs[1].At != clk.Mono() {
		t.Fatalf("rejections=%v", rs)
	}
	s.mu.Lock()
	n := len(s.running)
	s.mu.Unlock()
	if n != 63 {
		t.Fatalf("running=%d want 63", n)
	}
	var initial []sleepCall
	for range 63 {
		initial = append(initial, receive(t, calls))
	}
	for _, c := range initial {
		close(c.release)
	}
	seen := map[uint64]bool{}
	for range 63 {
		id := receive(t, probes)
		if id < 2 || id > 64 || seen[id] {
			t.Fatalf("forbidden or repeated engine call: %d", id)
		}
		seen[id] = true
	}
}

func TestRunKeepsPeriodDespiteProbeDuration(t *testing.T) {
	clk := clock.NewFake(time.Unix(0, 0))
	q := NewQueue(QueueCap)
	calls := make(chan sleepCall, 4)
	s := NewScheduler(engineFunc(func(context.Context, *heronv1.ProbeTask) Outcome {
		clk.Advance(2 * time.Second)
		return Outcome{RttUs: 2000000}
	}), q, clk, logger())
	s.Sleep, s.Rand = controlledSleep(calls), func() float64 { return 0 }
	defer s.Stop()
	s.Apply(&heronv1.ProbeTasks{Tasks: []*heronv1.ProbeTask{task(1)}})
	c := receive(t, calls)
	close(c.release)
	for i := range 3 {
		c = receive(t, calls)
		if c.delay != 3*time.Second {
			t.Fatalf("remaining interval=%v want 3s", c.delay)
		}
		rs := q.Take(clk.Mono(), probelimit.MaxResultAge, probelimit.MaxResultsPerReport)
		if len(rs) != 1 || rs[0].At != clk.Mono() || rs[0].Outcome.RttUs != 2000000 {
			t.Fatalf("result=%v", rs)
		}
		if i < 2 {
			clk.Advance(c.delay)
			close(c.release)
		}
	}
}

func TestStopDoesNotEnqueueInFlightResult(t *testing.T) {
	clk := clock.NewFake(time.Unix(0, 0))
	q := NewQueue(QueueCap)
	entered := make(chan struct{})
	calls := make(chan sleepCall, 2)
	s := NewScheduler(engineFunc(func(ctx context.Context, _ *heronv1.ProbeTask) Outcome {
		close(entered)
		<-ctx.Done()
		return Outcome{RttUs: 1}
	}), q, clk, logger())
	s.Sleep, s.Rand = controlledSleep(calls), func() float64 { return 0 }
	s.Apply(&heronv1.ProbeTasks{Tasks: []*heronv1.ProbeTask{task(1)}})
	c := receive(t, calls)
	close(c.release)
	receive(t, entered)
	s.Stop()
	if rs := q.Take(clk.Mono(), probelimit.MaxResultAge, probelimit.MaxResultsPerReport); len(rs) != 0 {
		t.Fatalf("stopped result queued: %v", rs)
	}
}

func TestApplyOwnsTaskSnapshot(t *testing.T) {
	calls := make(chan sleepCall, 2)
	probes := make(chan string, 1)
	s := NewScheduler(engineFunc(func(_ context.Context, t *heronv1.ProbeTask) Outcome {
		probes <- t.Target
		return Outcome{}
	}), NewQueue(3), clock.Real(), logger())
	s.Sleep, s.Rand = controlledSleep(calls), func() float64 { return 0 }
	defer s.Stop()
	input := task(1)
	s.Apply(&heronv1.ProbeTasks{Tasks: []*heronv1.ProbeTask{input}})
	first := receive(t, calls)
	input.Target = "changed.invalid"
	close(first.release)
	if got := receive(t, probes); got != "127.0.0.1" {
		t.Fatalf("input mutation reached engine: %s", got)
	}
}

func TestSleepHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- sleep(ctx, time.Hour) }()
	cancel()
	if err := receive(t, done); err != context.Canceled {
		t.Fatalf("canceled sleep=%v", err)
	}
	if err := sleep(t.Context(), 0); err != nil {
		t.Fatalf("zero sleep=%v", err)
	}
}
