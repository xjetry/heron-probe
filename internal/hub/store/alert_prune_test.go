package store

import (
	"bytes"
	"database/sql"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestPruneAlertEventsKeepsBoundaryAndDeletesDeliveries(t *testing.T) {
	s, ids, cs, _ := alertFixture(t)
	var logs bytes.Buffer
	s.log = slog.New(slog.NewTextHandler(&logs, nil))
	r := saveRule(t, s, AlertRule{Name: "offline", Kind: KindOffline, AllNodes: true})
	before := s.clk.Now().Add(-90 * 24 * time.Hour)
	var events []AlertEvent
	for _, at := range []time.Time{before.Add(-24 * time.Hour), before, before.Add(24 * time.Hour)} {
		ev, err := s.RecordTransition(t.Context(), r.ID, ids[0], StateFiring, AlertEvent{At: at, Transition: TransitionFiring}, []int64{cs[0].ID, cs[1].ID})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.UpdateDelivery(t.Context(), ev.Deliveries[0].ID, 1, true, true, "", at); err != nil {
			t.Fatal(err)
		}
		events = append(events, ev)
	}
	n, err := s.PruneAlertEvents(t.Context(), before)
	if err != nil || n != 1 {
		t.Fatalf("pruned=%d err=%v", n, err)
	}
	assertAlertRows(t, s, "alert_event", "id = 1", 0)
	assertAlertRows(t, s, "alert_delivery", "event_id = 1", 0)
	for _, ev := range events[1:] {
		got, err := s.GetAlertEvent(t.Context(), ev.ID)
		if err != nil || len(got.Deliveries) != 2 {
			t.Fatalf("retained=%+v err=%v", got, err)
		}
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "unfinished_deliveries=1") {
		t.Fatalf("missing unfinished warning: %s", &logs)
	}
}

func TestPruneAlertEventsRollsBackDeliveriesOnFailure(t *testing.T) {
	s, ids, cs, _ := alertFixture(t)
	r := saveRule(t, s, AlertRule{Name: "offline", Kind: KindOffline})
	ev := recordEvent(t, s, r.ID, ids[0], []int64{cs[0].ID})
	if err := s.write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec("CREATE TRIGGER stop_event_delete BEFORE DELETE ON alert_event BEGIN SELECT RAISE(ABORT, 'deny deletion'); END")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PruneAlertEvents(t.Context(), s.clk.Now().Add(time.Second)); err == nil {
		t.Fatal("deletion unexpectedly succeeded")
	}
	got, err := s.GetAlertEvent(t.Context(), ev.ID)
	if err != nil || len(got.Deliveries) != 1 {
		t.Fatalf("partial deletion: %+v %v", got, err)
	}
}

func TestAlertRetentionMustBePositive(t *testing.T) {
	if DefaultRetention.AlertEvents != 90*24*time.Hour {
		t.Fatalf("default=%v", DefaultRetention.AlertEvents)
	}
	for _, d := range []time.Duration{0, -time.Second} {
		r := DefaultRetention
		r.AlertEvents = d
		if err := r.Validate(); err == nil {
			t.Fatalf("retention %v accepted", d)
		}
	}
}
