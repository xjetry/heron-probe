package store

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/clock"
)

func openTraffic(t *testing.T) (*Store, int64) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC))
	st, err := Open(filepath.Join(t.TempDir(), "t.db"), clk, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	id, err := st.CreateNode(context.Background(), "n", []byte{1})
	if err != nil {
		t.Fatal(err)
	}
	return st, id
}

func TestTrafficRoundTripUpsertsBaselineAndTotalsTogether(t *testing.T) {
	st, id := openTraffic(t)
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	rec := TrafficRecord{NodeID: id, BootID: "b1", LastRx: 100, LastTx: 200, TotalRx: 1000, TotalTx: 2000, PeriodRx: 10, PeriodTx: 20, PeriodStart: start}
	if skipped, err := st.WriteTraffic(t.Context(), []TrafficRecord{rec}); err != nil || skipped != 0 {
		t.Fatalf("write: %v skipped=%d", err, skipped)
	}
	rec.LastRx, rec.TotalRx, rec.PeriodRx = 150, 1050, 60
	if _, err := st.WriteTraffic(t.Context(), []TrafficRecord{rec}); err != nil {
		t.Fatal(err)
	}
	got, err := st.LoadTraffic(t.Context())
	if err != nil || len(got) != 1 {
		t.Fatalf("load: %v %v", got, err)
	}
	if !got[0].PeriodStart.Equal(rec.PeriodStart) {
		t.Fatalf("period start %v, want %v", got[0].PeriodStart, rec.PeriodStart)
	}
	got[0].PeriodStart, rec.PeriodStart = time.Time{}, time.Time{}
	if got[0] != rec {
		t.Fatalf("got %+v, want %+v", got[0], rec)
	}
}

func TestTrafficWriteSkipsDeletedNodes(t *testing.T) {
	st, id := openTraffic(t)
	if err := st.DeleteNode(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	skipped, err := st.WriteTraffic(t.Context(), []TrafficRecord{{NodeID: id, BootID: "b", PeriodStart: time.Unix(0, 0)}})
	if err != nil || skipped != 1 {
		t.Fatalf("skipped=%d err=%v, want 1/nil", skipped, err)
	}
	got, _ := st.LoadTraffic(t.Context())
	if len(got) != 0 {
		t.Fatalf("row written for a deleted node: %+v", got)
	}
}

func TestTrafficResetDaysComeFromNodeTable(t *testing.T) {
	st, id := openTraffic(t)
	err := st.write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE node SET traffic_reset_day = 15 WHERE id = ?", id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	days, err := st.TrafficResetDays(t.Context())
	if err != nil || days[id] != 15 {
		t.Fatalf("days = %v err=%v, want {%d: 15}", days, err, id)
	}
}
