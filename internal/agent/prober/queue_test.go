package prober

import (
	"context"
	"math"
	"reflect"
	"testing"
	"time"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"google.golang.org/protobuf/proto"
)

func TestQueueDropsOldestAndExpired(t *testing.T) {
	q := NewQueue(3)
	for i := uint64(1); i <= 4; i++ {
		q.Push(Result{TaskID: i, At: time.Duration(i) * time.Second})
	}
	want := []Result{{TaskID: 2, At: 2 * time.Second}, {TaskID: 3, At: 3 * time.Second}, {TaskID: 4, At: 4 * time.Second}}
	if got := q.Take(4*time.Second, 120*time.Second, 1024); !reflect.DeepEqual(got, want) || q.Dropped() != 1 {
		t.Fatalf("results=%v dropped=%d want=%v/1", got, q.Dropped(), want)
	}
	if got := q.Take(4*time.Second, 120*time.Second, 1024); len(got) != 0 {
		t.Fatalf("taken results remained: %v", got)
	}
	q.Push(Result{TaskID: 1, At: 0})
	q.Push(Result{TaskID: 2, At: time.Second})
	if got := q.Take(121*time.Second, 120*time.Second, 1024); !reflect.DeepEqual(got, []Result{{TaskID: 2, At: time.Second}}) || q.Dropped() != 2 {
		t.Fatalf("age boundary results=%v dropped=%d", got, q.Dropped())
	}
}

func TestQueueRequeuesBeforeNewResultsWithinCapacity(t *testing.T) {
	q := NewQueue(3)
	q.Push(Result{TaskID: 1})
	q.Push(Result{TaskID: 2})
	old := q.Take(0, time.Minute, 1024)
	q.Push(Result{TaskID: 3})
	q.Requeue(old)
	want := []Result{{TaskID: 1}, {TaskID: 2}, {TaskID: 3}}
	if got := q.Take(0, time.Minute, 1024); !reflect.DeepEqual(got, want) {
		t.Fatalf("requeue order=%v want=%v", got, want)
	}
	q.Push(Result{TaskID: 3})
	q.Push(Result{TaskID: 4})
	q.Requeue(old)
	want = []Result{{TaskID: 2}, {TaskID: 3}, {TaskID: 4}}
	if got := q.Take(0, time.Minute, 1024); !reflect.DeepEqual(got, want) || q.Dropped() != 1 {
		t.Fatalf("requeue overflow=%v dropped=%d", got, q.Dropped())
	}
}

func TestQueueCapacityBoundaries(t *testing.T) {
	q := NewQueue(0)
	q.Push(Result{TaskID: 1})
	if got := q.Take(0, time.Minute, 1024); len(got) != 0 || q.Dropped() != 1 {
		t.Fatalf("zero capacity results=%v dropped=%d", got, q.Dropped())
	}
	defer func() {
		if recover() == nil {
			t.Error("negative capacity accepted")
		}
	}()
	NewQueue(-1)
}

func TestToProtoMapsAgeAndOutcomes(t *testing.T) {
	rs := []Result{
		{TaskID: 1, At: time.Second, Outcome: Outcome{RttUs: 0}},
		{TaskID: 2, At: 1500 * time.Millisecond, Outcome: Outcome{Timeout: true}},
		{TaskID: 3, At: 1999 * time.Millisecond, Outcome: Outcome{Err: "socket denied"}},
	}
	got := ToProto(rs, 2*time.Second)
	want := []*probev1.ProbeResult{
		{TaskId: 1, AgeMs: 1000, Outcome: &probev1.ProbeResult_RttUs{RttUs: 0}},
		{TaskId: 2, AgeMs: 500, Outcome: &probev1.ProbeResult_Timeout{Timeout: &probev1.Timeout{}}},
		{TaskId: 3, AgeMs: 1, Outcome: &probev1.ProbeResult_Error{Error: &probev1.ProbeError{Message: "socket denied"}}},
	}
	if len(got) != len(want) {
		t.Fatalf("results=%v", got)
	}
	for i := range want {
		if !proto.Equal(got[i], want[i]) {
			t.Errorf("result=%v want=%v", got[i], want[i])
		}
	}
}

type engineFunc func(context.Context, *probev1.ProbeTask) Outcome

func TestToProtoClampsAge(t *testing.T) {
	got := ToProto([]Result{{At: time.Second}, {At: -(time.Duration(math.MaxUint32) + 1) * time.Millisecond}}, 0)
	if got[0].AgeMs != 0 || got[1].AgeMs != math.MaxUint32 {
		t.Fatalf("clamped ages=%d/%d", got[0].AgeMs, got[1].AgeMs)
	}
}

func (f engineFunc) Probe(ctx context.Context, task *probev1.ProbeTask) Outcome { return f(ctx, task) }

func TestMultiDispatchesByKind(t *testing.T) {
	m := Multi{
		ICMP: engineFunc(func(context.Context, *probev1.ProbeTask) Outcome { return Outcome{RttUs: 11} }),
		TCP:  engineFunc(func(context.Context, *probev1.ProbeTask) Outcome { return Outcome{RttUs: 22} }),
	}
	for _, tc := range []struct {
		kind probev1.ProbeKind
		want Outcome
	}{
		{probev1.ProbeKind_PROBE_KIND_ICMP, Outcome{RttUs: 11}},
		{probev1.ProbeKind_PROBE_KIND_TCP, Outcome{RttUs: 22}},
		{probev1.ProbeKind_PROBE_KIND_UNSPECIFIED, Outcome{Err: "unsupported probe kind PROBE_KIND_UNSPECIFIED"}},
	} {
		if got := m.Probe(t.Context(), &probev1.ProbeTask{Kind: tc.kind}); got != tc.want {
			t.Errorf("kind=%v got=%v want=%v", tc.kind, got, tc.want)
		}
	}
}
