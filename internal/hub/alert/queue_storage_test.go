package alert

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/hub/store"
)

// 外部连接只在故障用例中安装/移除 SQLite 故障；不向 Store 或 Queue 暴露生产钩子。
func deliveryDB(t *testing.T, f *fixture) *sql.DB {
	t.Helper()
	u := url.URL{Path: f.path}
	db, err := sql.Open("sqlite", "file:"+u.EscapedPath()+"?_pragma=busy_timeout(5000)")
	must(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { must(t, db.Close()) })
	return db
}
func deliverySQL(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	_, err := db.Exec(query)
	must(t, err)
}
func TestAttemptWriteFailureSendsNothing(t *testing.T) {
	f := newFixture(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	ev := queueEvent(t, f, queueChannel(t, f, srv.URL))
	db := deliveryDB(t, f)
	deliverySQL(t, db, "CREATE TRIGGER fail_attempt BEFORE UPDATE ON alert_delivery BEGIN SELECT RAISE(ABORT, 'attempt write blocked'); END")
	q := NewQueue(f.st, f.e.Channels, NewHTTPClient(), "", f.clk, nil, f.log)
	err := q.deliver(t.Context(), deliveryItem{ev.Deliveries[0], ev})
	if err == nil || !strings.Contains(err.Error(), "attempt write blocked") {
		t.Fatalf("write failure=%v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("HTTP sent despite failed attempt persistence: %d", calls.Load())
	}
	saved, err := f.st.GetAlertEvent(t.Context(), ev.ID)
	must(t, err)
	if saved.Deliveries[0].Attempts != 0 {
		t.Fatalf("failed transaction consumed attempt: %+v", saved.Deliveries)
	}
}

func TestExhaustedUnrecordedDeliveryBecomesTerminal(t *testing.T) {
	for _, last := range []string{"", "prior error"} {
		t.Run(last, func(t *testing.T) {
			f := newFixture(t)
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
			defer srv.Close()
			ev := queueEvent(t, f, queueChannel(t, f, srv.URL))
			for range store.MaxDeliveryAttempts {
				_, err := f.st.BeginDeliveryAttempt(t.Context(), ev.Deliveries[0].ID)
				must(t, err)
			}
			must(t, f.st.UpdateDelivery(t.Context(), ev.Deliveries[0].ID, false, false, last, time.Time{}))
			q := NewQueue(f.st, f.e.Channels, NewHTTPClient(), "", f.clk, nil, f.log)
			must(t, q.deliver(t.Context(), deliveryItem{ev.Deliveries[0], ev}))
			saved, err := f.st.GetAlertEvent(t.Context(), ev.ID)
			must(t, err)
			want := last
			if want == "" {
				want = store.DeliveryErrResultUnrecorded
			}
			d := saved.Deliveries[0]
			if calls.Load() != 0 || !d.Done || d.OK || d.Attempts != store.MaxDeliveryAttempts || d.LastError != want {
				t.Fatalf("exhausted delivery sent=%d row=%+v want error=%q", calls.Load(), d, want)
			}
		})
	}
}

func TestResultWriteFailuresCannotExceedSendBudget(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var mu sync.Mutex
	counts := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg receivedMessage
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		counts[msg.Summary]++
		excessive := counts[msg.Summary] > store.MaxDeliveryAttempts
		mu.Unlock()
		if excessive {
			cancel()
		}
	}))
	defer srv.Close()
	c := queueChannel(t, f, srv.URL)
	rule := f.rule(t, offline())
	for i := range QueueCap + 1 {
		_, err := f.st.RecordTransition(t.Context(), rule.ID, f.ids[0], store.StateFiring, store.AlertEvent{
			At: f.clk.Now(), Transition: store.TransitionFiring, Summary: fmt.Sprint(i),
		}, []int64{c.ID})
		must(t, err)
	}
	db := deliveryDB(t, f)
	deliverySQL(t, db, "CREATE TRIGGER fail_result BEFORE UPDATE ON alert_delivery WHEN NEW.done = 1 OR NEW.ok = 1 BEGIN SELECT RAISE(ABORT, 'result write blocked'); END")
	sleeps := 0
	q := NewQueue(f.st, f.e.Channels, NewHTTPClient(), "", f.clk, func(ctx context.Context, _ time.Duration) error {
		sleeps++
		if sleeps > (QueueCap+1)*(store.MaxDeliveryAttempts+1) {
			cancel()
			return ctx.Err()
		}
		return nil
	}, f.log)
	must(t, q.Requeue(t.Context()))
	q.Run(ctx)
	mu.Lock()
	defer mu.Unlock()
	total := 0
	for id, n := range counts {
		total += n
		if n > store.MaxDeliveryAttempts {
			t.Errorf("delivery %s HTTP=%d exceeds maximum %d", id, n, store.MaxDeliveryAttempts)
		}
	}
	if total == 0 || total > (QueueCap+1)*store.MaxDeliveryAttempts {
		t.Fatalf("total HTTP=%d outside send budget %d", total, (QueueCap+1)*store.MaxDeliveryAttempts)
	}
}
