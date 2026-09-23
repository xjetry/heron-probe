package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/agent/prober"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/probelimit"
	"google.golang.org/protobuf/proto"
)

type quietEngine struct{}

func (quietEngine) Probe(context.Context, *probev1.ProbeTask) prober.Outcome {
	return prober.Outcome{RttUs: 10}
}

func TestRunnerReportsResultsAndReconcilesVersion(t *testing.T) {
	hub := &fakeHub{tasks: &probev1.ProbeTasks{Version: 8}}
	r, _ := newRunner(t, hub)
	q := prober.NewQueue(prober.QueueCap)
	s := prober.NewScheduler(quietEngine{}, q, r.Clock, r.Log)
	defer s.Stop()
	s.Apply(&probev1.ProbeTasks{Version: 7})
	r.Prober, r.Results = s, q
	r.Collector.IcmpAvailable = true
	q.Push(prober.Result{TaskID: 42, Outcome: prober.Outcome{RttUs: 321}, At: r.Clock.Mono() - time.Second})
	q.Push(prober.Result{TaskID: 99, At: r.Clock.Mono() - probelimit.MaxResultAge - time.Second})
	round := 0
	r.Sleep = func(context.Context, time.Duration) error {
		round++
		if round == 2 {
			return context.Canceled
		}
		hub.mu.Lock()
		hub.tasks = nil
		hub.mu.Unlock()
		return nil
	}
	if err := r.Run(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	reports := hub.received()
	if len(reports) != 2 {
		t.Fatalf("reports = %d, want 2", len(reports))
	}
	want := &probev1.ProbeResult{TaskId: 42, AgeMs: 1000, Outcome: &probev1.ProbeResult_RttUs{RttUs: 321}}
	if len(reports[0].ProbeResults) != 1 || !proto.Equal(reports[0].ProbeResults[0], want) {
		t.Fatalf("first results = %v, want %v", reports[0].ProbeResults, want)
	}
	if reports[0].TasksVersion != 7 || reports[1].TasksVersion != 8 || s.Version() != 8 {
		t.Fatalf("versions = %d, %d, scheduler %d; want 7, 8, 8", reports[0].TasksVersion, reports[1].TasksVersion, s.Version())
	}
	if len(reports[1].ProbeResults) != 0 {
		t.Fatalf("successful results repeated: %v", reports[1].ProbeResults)
	}
	if !reports[0].GetFacts().GetIcmpAvailable() {
		t.Fatal("ICMP availability missing from reported facts")
	}
}

func TestRunnerFailureRetainsOnlyRetryableResults(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeUnavailable, connect.CodeInvalidArgument} {
		t.Run(code.String(), func(t *testing.T) {
			hub := &fakeHub{probeError: code}
			r, _ := newRunner(t, hub)
			var logs bytes.Buffer
			r.Log = slog.New(slog.NewTextHandler(&logs, nil))
			q := prober.NewQueue(prober.QueueCap)
			r.Results = q
			q.Push(prober.Result{TaskID: 42, Outcome: prober.Outcome{Timeout: true}, At: r.Clock.Mono()})
			round := 0
			r.Sleep = func(context.Context, time.Duration) error {
				round++
				if round == 2 {
					return context.Canceled
				}
				remaining := q.Take(r.Clock.Mono(), probelimit.MaxResultAge, probelimit.MaxResultsPerReport)
				want := 1
				if code == connect.CodeInvalidArgument {
					want = 0
				}
				if len(remaining) != want {
					t.Errorf("queue after %s = %v, want %d results", code, remaining, want)
				}
				q.Requeue(remaining)
				r.Clock.(*clock.Fake).Advance(2 * time.Second)
				hub.mu.Lock()
				hub.probeError = 0
				hub.mu.Unlock()
				return nil
			}
			if err := r.Run(context.Background()); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			reports := hub.received()
			if len(reports) != 2 {
				t.Fatalf("reports = %d, want 2", len(reports))
			}
			if len(reports[0].ProbeResults) != 1 {
				t.Fatalf("first results = %v", reports[0].ProbeResults)
			}
			if code == connect.CodeInvalidArgument {
				if len(reports[1].ProbeResults) != 0 {
					t.Fatalf("invalid results retried: %v", reports[1].ProbeResults)
				}
				if !strings.Contains(logs.String(), "discarding rejected probe results") {
					t.Fatalf("discard warning missing: %s", logs.String())
				}
			} else {
				want := &probev1.ProbeResult{TaskId: 42, AgeMs: 2000, Outcome: &probev1.ProbeResult_Timeout{Timeout: &probev1.Timeout{}}}
				if len(reports[1].ProbeResults) != 1 || !proto.Equal(reports[1].ProbeResults[0], want) {
					t.Fatalf("retry results = %v, want %v", reports[1].ProbeResults, want)
				}
			}
		})
	}
}

func TestRunnerEmptySchedulerReportsNoResultsOrVersion(t *testing.T) {
	hub := &fakeHub{tasks: &probev1.ProbeTasks{Version: 8}}
	r, _ := newRunner(t, hub)
	r.Sleep = func(context.Context, time.Duration) error { return context.Canceled }
	if err := r.Run(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	reports := hub.received()
	if len(reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(reports))
	}
	got := reports[0]
	if got.TasksVersion != 0 || len(got.ProbeResults) != 0 || got.GetFacts().GetIcmpAvailable() {
		t.Fatalf("unexpected probe state for empty scheduler: %v", got)
	}
}

type advancingClock struct {
	clock.Clock
	mono time.Duration
}

func TestRunnerRequiresProbeComponents(t *testing.T) {
	for _, field := range []string{"Prober", "Results"} {
		t.Run(field, func(t *testing.T) {
			r, _ := newRunner(t, &fakeHub{})
			if field == "Prober" {
				r.Prober = nil
			} else {
				r.Results = nil
			}
			if err := r.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "Runner."+field+": required") {
				t.Fatalf("missing %s: %v", field, err)
			}
		})
	}
}

func (c *advancingClock) Mono() time.Duration {
	c.mono += time.Second
	return c.mono
}

func TestRunnerUsesOneInstantForResultAge(t *testing.T) {
	hub := &fakeHub{}
	r, _ := newRunner(t, hub)
	r.Clock = &advancingClock{Clock: r.Clock, mono: time.Hour}
	r.Results = prober.NewQueue(1)
	r.Results.Push(prober.Result{TaskID: 1, At: time.Hour + time.Second - probelimit.MaxResultAge})
	r.Sleep = func(context.Context, time.Duration) error { return context.Canceled }
	if err := r.Run(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	reports := hub.received()
	if len(reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(reports))
	}
	got := reports[0].ProbeResults
	if len(got) != 1 || got[0].AgeMs != uint32(probelimit.MaxResultAge/time.Millisecond) {
		t.Fatalf("boundary results = %v, want age_ms 120000 at filtering instant", got)
	}
}

func TestRunnerDoesNotWarnAboutEmptyDiscard(t *testing.T) {
	r, _ := newRunner(t, &fakeHub{probeError: connect.CodeInvalidArgument})
	var logs bytes.Buffer
	r.Log = slog.New(slog.NewTextHandler(&logs, nil))
	r.Results = prober.NewQueue(1)
	r.Sleep = func(context.Context, time.Duration) error { return context.Canceled }
	if err := r.Run(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "discarding rejected probe results") {
		t.Fatalf("empty batch must not log a discard: %s", logs.String())
	}
}

func TestRunnerWarnsOnlyWhenQueueDropsIncrease(t *testing.T) {
	for _, code := range []connect.Code{0, connect.CodeUnavailable} {
		t.Run(code.String(), func(t *testing.T) {
			r, _ := newRunner(t, &fakeHub{probeError: code})
			var logs bytes.Buffer
			r.Log = slog.New(slog.NewJSONHandler(&logs, nil))
			q := prober.NewQueue(1)
			r.Results = q
			q.Push(prober.Result{At: r.Clock.Mono()})
			q.Push(prober.Result{At: r.Clock.Mono()})
			round := 0
			r.Sleep = func(context.Context, time.Duration) error {
				round++
				var deltas []uint64
				decoder := json.NewDecoder(bytes.NewReader(logs.Bytes()))
				for decoder.More() {
					var record struct {
						Message string `json:"msg"`
						Level   string `json:"level"`
						Dropped uint64 `json:"dropped"`
					}
					if err := decoder.Decode(&record); err != nil {
						t.Fatal(err)
					}
					if record.Message == "probe results dropped" {
						if record.Level != "WARN" {
							t.Errorf("drop log level = %s, want WARN", record.Level)
						}
						deltas = append(deltas, record.Dropped)
					}
				}
				want := []uint64{1}
				if round >= 3 {
					want = append(want, 2)
				}
				if !slices.Equal(deltas, want) {
					t.Errorf("round %d drop deltas = %v, want %v", round, deltas, want)
				}
				if round == 2 {
					q.Take(r.Clock.Mono(), probelimit.MaxResultAge, probelimit.MaxResultsPerReport)
					q.Push(prober.Result{At: r.Clock.Mono() - probelimit.MaxResultAge - 2*time.Second})
					q.Push(prober.Result{At: r.Clock.Mono() - probelimit.MaxResultAge - time.Second})
				}
				if round == 4 {
					return context.Canceled
				}
				return nil
			}
			if err := r.Run(context.Background()); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}
