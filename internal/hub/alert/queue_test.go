package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/testwait"
)

type receivedMessage struct {
	Rule, Node, Kind, Transition, Summary string
	Value                                 float64
	At                                    int64
}

func queueChannel(t *testing.T, f *fixture, url string) store.NotifyChannel {
	t.Helper()
	cfg, err := json.Marshal(WebhookConfig{URL: url, Method: "POST"})
	must(t, err)
	c, err := f.e.SaveChannel(t.Context(), store.NotifyChannel{Name: "hook", Kind: store.ChannelWebhook, Config: string(cfg)})
	must(t, err)
	return c
}
func queueEvent(t *testing.T, f *fixture, cs ...store.NotifyChannel) store.AlertEvent {
	t.Helper()
	r := f.rule(t, offline())
	var ids []int64
	for _, c := range cs {
		ids = append(ids, c.ID)
	}
	ev, err := f.st.RecordTransition(t.Context(), r.ID, f.ids[0], store.StateFiring, store.AlertEvent{At: f.clk.Now(), Summary: "persisted summary", Value: 5, Transition: store.TransitionFiring}, ids)
	must(t, err)
	return ev
}
func startQueue(t *testing.T, q *Queue) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); q.Run(ctx) }()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(testwait.Bound):
			t.Fatal("queue did not stop")
		}
	}
	t.Cleanup(stop)
	return stop
}
func awaitDeliveries(t *testing.T, f *fixture, id int64, ready func([]store.Delivery) bool) []store.Delivery {
	t.Helper()
	var got []store.Delivery
	testwait.Until(t, time.Millisecond, func() bool {
		ev, err := f.st.GetAlertEvent(t.Context(), id)
		must(t, err)
		got = ev.Deliveries
		return ready(got)
	}, "delivery state did not arrive: %s", testwait.When(func() string { return fmt.Sprintf("%+v", got) }))
	return got
}
func allDone(ds []store.Delivery) bool {
	for _, d := range ds {
		if !d.Done {
			return false
		}
	}
	return true
}

func TestQueueDeliversAndRecords(t *testing.T) {
	f := newFixture(t)
	var one, two atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/one" {
			one.Add(1)
			return
		}
		if two.Add(1) == 1 {
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	a, b := queueChannel(t, f, srv.URL+"/one"), queueChannel(t, f, srv.URL+"/two")
	ev := queueEvent(t, f, a, b)
	var mu sync.Mutex
	var sleeps []time.Duration
	q := NewQueue(f.st, f.e.Channels, NewHTTPClient(), "", f.clk, func(_ context.Context, d time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		sleeps = append(sleeps, d)
		return nil
	}, f.log)
	q.Enqueue(ev)
	stop := startQueue(t, q)
	ds := awaitDeliveries(t, f, ev.ID, allDone)
	stop()
	if one.Load() != 1 || two.Load() != 2 || ds[0].Attempts != 1 || ds[1].Attempts != 2 {
		t.Fatalf("calls=%d/%d deliveries=%+v", one.Load(), two.Load(), ds)
	}
	for _, d := range ds {
		if !d.OK || !d.Done || d.Failure != store.FailureNone || d.HTTPStatus != 0 || d.LastError != "" || !d.DeliveredAt.Equal(f.clk.Now()) {
			t.Fatalf("delivery=%+v", d)
		}
	}
	if !reflect.DeepEqual(sleeps, []time.Duration{time.Second}) {
		t.Fatalf("backoff=%v", sleeps)
	}
}

func TestQueueGivesUpAfterMaxAttempts(t *testing.T) {
	for _, status := range []int{400, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newFixture(t)
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(status) }))
			defer srv.Close()
			ev := queueEvent(t, f, queueChannel(t, f, srv.URL))
			var sleeps []time.Duration
			q := NewQueue(f.st, f.e.Channels, NewHTTPClient(), "", f.clk, func(_ context.Context, d time.Duration) error { sleeps = append(sleeps, d); return nil }, f.log)
			q.Enqueue(ev)
			stop := startQueue(t, q)
			ds := awaitDeliveries(t, f, ev.ID, allDone)
			stop()
			want := 1
			var backoffs []time.Duration
			if status == 500 {
				want = store.MaxDeliveryAttempts
				backoffs = []time.Duration{time.Second, 4 * time.Second}
			}
			d := ds[0]
			if d.Attempts != want || calls.Load() != int32(want) || d.OK || !d.Done || d.Failure != store.FailureHTTPStatus || d.HTTPStatus != status || !d.DeliveredAt.IsZero() {
				t.Fatalf("calls=%d delivery=%+v", calls.Load(), d)
			}
			if !reflect.DeepEqual(sleeps, backoffs) {
				t.Fatalf("backoff=%v want %v", sleeps, backoffs)
			}
			fresh := NewQueue(f.st, f.e.Channels, NewHTTPClient(), "", f.clk, nil, f.log)
			must(t, fresh.Requeue(t.Context()))
			if len(fresh.items) != 0 {
				t.Fatalf("terminal delivery requeued: %d", len(fresh.items))
			}
		})
	}
}

func TestQueueRequeuesPendingOnLoad(t *testing.T) {
	f := newFixture(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	ev := queueEvent(t, f, queueChannel(t, f, srv.URL))
	if _, err := f.st.BeginDeliveryAttempt(t.Context(), ev.Deliveries[0].ID); err != nil {
		t.Fatal(err)
	}
	must(t, f.st.UpdateDelivery(t.Context(), ev.Deliveries[0].ID, store.DeliveryResult{Failure: store.FailureTransport, Error: "prior failure"}))
	q := NewQueue(f.st, f.e.Channels, NewHTTPClient(), "", f.clk, nil, f.log)
	must(t, q.Requeue(t.Context()))
	stop := startQueue(t, q)
	ds := awaitDeliveries(t, f, ev.ID, allDone)
	stop()
	if calls.Load() != 1 || ds[0].Attempts != 2 || !ds[0].OK {
		t.Fatalf("calls=%d delivery=%+v", calls.Load(), ds[0])
	}
	fresh := NewQueue(f.st, f.e.Channels, NewHTTPClient(), "", f.clk, nil, f.log)
	must(t, fresh.Requeue(t.Context()))
	if len(fresh.items) != 0 {
		t.Fatal("successful delivery requeued")
	}
}

func TestQueueDropsOldestWhenFull(t *testing.T) {
	f := newFixture(t)
	var logs bytes.Buffer
	q := NewQueue(f.st, f.e.Channels, NewHTTPClient(), "", f.clk, nil, slog.New(slog.NewJSONHandler(&logs, nil)))
	if cap(q.items) != QueueCap || QueueCap != 256 {
		t.Fatalf("capacity=%d", cap(q.items))
	}
	q.items = make(chan deliveryItem, 2)
	for id := int64(1); id <= 3; id++ {
		q.Enqueue(store.AlertEvent{ID: id, Deliveries: []store.Delivery{{ID: id, EventID: id}}})
	}
	if len(q.items) != 2 {
		t.Fatalf("len=%d", len(q.items))
	}
	a, b := <-q.items, <-q.items
	if a.delivery.ID != 2 || b.delivery.ID != 3 {
		t.Fatalf("retained=%d,%d", a.delivery.ID, b.delivery.ID)
	}
	if !strings.Contains(logs.String(), `"level":"WARN"`) || !strings.Contains(logs.String(), `"delivery_id":1`) {
		t.Fatalf("missing drop warning: %s", logs.String())
	}
}

func TestSendTestDoesNotRecord(t *testing.T) {
	f := newFixture(t)
	var got receivedMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewDecoder(r.Body).Decode(&got) }))
	defer srv.Close()
	c := queueChannel(t, f, srv.URL)
	q := NewQueue(f.st, f.e.Channels, NewHTTPClient(), "", f.clk, nil, f.log)
	must(t, q.SendTest(t.Context(), c))
	if got.Summary == "" || got.Transition != "test" {
		t.Fatalf("test message=%+v", got)
	}
	if len(f.events(t)) != 0 {
		t.Fatal("test notification persisted event")
	}
	pending, err := f.st.PendingDeliveries(t.Context())
	must(t, err)
	if len(pending) != 0 {
		t.Fatal("test notification persisted delivery")
	}
}

func TestQueueMissingChannelIsTerminal(t *testing.T) {
	f := newFixture(t)
	c := queueChannel(t, f, "http://127.0.0.1:1")
	ev := queueEvent(t, f, c)
	q := NewQueue(f.st, func() []store.NotifyChannel { return nil }, NewHTTPClient(), "", f.clk, nil, f.log)
	q.Enqueue(ev)
	stop := startQueue(t, q)
	ds := awaitDeliveries(t, f, ev.ID, allDone)
	stop()
	if d := ds[0]; d.Attempts != 0 || d.OK || d.Failure != store.FailureChannelDeleted || d.LastError != "" {
		t.Fatalf("missing channel delivery=%+v", d)
	}
}

func TestQueueCancellationLeavesPending(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	ev := queueEvent(t, f, queueChannel(t, f, srv.URL))
	sleeping := make(chan struct{})
	q := NewQueue(f.st, f.e.Channels, NewHTTPClient(), "", f.clk, func(ctx context.Context, d time.Duration) error { close(sleeping); <-ctx.Done(); return ctx.Err() }, f.log)
	q.Enqueue(ev)
	stop := startQueue(t, q)
	select {
	case <-sleeping:
	case <-time.After(testwait.Bound):
		t.Fatal("no retry sleep")
	}
	stop()
	ds := awaitDeliveries(t, f, ev.ID, func(ds []store.Delivery) bool { return ds[0].Attempts == 1 })
	if ds[0].Done || ds[0].OK {
		t.Fatalf("cancelled delivery=%+v", ds[0])
	}
	fresh := NewQueue(f.st, f.e.Channels, NewHTTPClient(), "", f.clk, nil, f.log)
	must(t, fresh.Requeue(t.Context()))
	if len(fresh.items) != 1 {
		t.Fatalf("pending not requeued: %d", len(fresh.items))
	}
}

func TestQueueMessageUsesCurrentNamesAndHistoricalSummary(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		name := "present"
		if deleted {
			name = "deleted"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			received := make(chan receivedMessage, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var m receivedMessage
				_ = json.NewDecoder(r.Body).Decode(&m)
				received <- m
			}))
			defer srv.Close()
			ev := queueEvent(t, f, queueChannel(t, f, srv.URL))
			if deleted {
				must(t, f.e.DeleteRule(t.Context(), ev.RuleID))
				must(t, f.st.DeleteNode(t.Context(), ev.NodeID))
			}
			q := NewQueue(f.st, f.e.Channels, NewHTTPClient(), "", f.clk, nil, f.log)
			q.Enqueue(ev)
			stop := startQueue(t, q)
			awaitDeliveries(t, f, ev.ID, allDone)
			stop()
			m := <-received
			rule, node, kind := "离线", "node1", "offline"
			if deleted {
				rule, node, kind = "规则 #1", "节点 #1", ""
			}
			if m.Rule != rule || m.Node != node || m.Kind != kind || m.Summary != ev.Summary || m.Value != ev.Value {
				t.Fatalf("message=%+v", m)
			}
		})
	}
}

func TestQueueRealSleepCanBeCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := sleepContext(ctx, time.Hour); err != context.Canceled {
		t.Fatalf("sleep error=%v", err)
	}
}
