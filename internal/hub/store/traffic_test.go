package store

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xjetry/heron-probe/internal/clock"
)

func openTraffic(t *testing.T) (*Store, int64) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC))
	st, err := Open(filepath.Join(t.TempDir(), "t.db"), clk, slog.Default(), MigrateSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	id, _, err := st.CreateNode(context.Background(), "n", Billing{}, []byte{1})
	if err != nil {
		t.Fatal(err)
	}
	return st, id
}

func TestTrafficRoundTripUpsertsBaselineAndTotalsTogether(t *testing.T) {
	st, id := openTraffic(t)
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	rec := TrafficRecord{NodeID: id, BootID: "b1", LastRx: 100, LastTx: 200, TotalRx: 1000, TotalTx: 2000, PeriodRx: 10, PeriodTx: 20, PeriodStart: start}
	rec.NetCounterEpoch = strings.Repeat("a", 64)
	if written, err := st.WriteTraffic(t.Context(), []TrafficRecord{rec}); err != nil || len(written) != 1 || written[0] != id {
		t.Fatalf("write: %v written=%v", err, written)
	}
	rec.LastRx, rec.TotalRx, rec.PeriodRx = 150, 1050, 60
	rec.NetCounterEpoch = strings.Repeat("b", 64)
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
	config := filepath.Join(t.TempDir(), "config.db")
	if err := st.SnapshotConfig(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored.db")
	if _, err := Restore(t.Context(), target, config, "", "", time.Now(), slog.Default()); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(target, clock.Real(), slog.Default(), RequireCurrentSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	got, err = restored.LoadTraffic(t.Context())
	if err != nil || len(got) != 1 || got[0].NetCounterEpoch != rec.NetCounterEpoch || got[0].BootID != rec.BootID || got[0].TotalRx != rec.TotalRx {
		t.Fatalf("restored traffic=%+v err=%v want=%+v", got, err, rec)
	}
}

func TestTrafficCounterEpochValidationAndAtomicWrite(t *testing.T) {
	st, id := openTraffic(t)
	rec := TrafficRecord{NodeID: id, BootID: "boot", NetCounterEpoch: strings.Repeat("a", 64), TotalRx: 100, PeriodStart: time.Unix(0, 0).UTC()}
	if _, err := st.WriteTraffic(t.Context(), []TrafficRecord{rec}); err != nil {
		t.Fatal(err)
	}
	for _, epoch := range []string{"short", strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		valid, invalid := rec, rec
		valid.TotalRx = 200
		invalid.NetCounterEpoch = epoch
		if _, err := st.WriteTraffic(t.Context(), []TrafficRecord{valid, invalid}); err == nil || !strings.Contains(err.Error(), "epoch") {
			t.Fatalf("invalid epoch accepted: %v", err)
		}
		got, err := st.LoadTraffic(t.Context())
		if err != nil || len(got) != 1 || got[0] != rec {
			t.Fatalf("failed batch changed baseline or totals: %+v %v", got, err)
		}
	}
}

func TestTrafficReadAndRestoreRejectInvalidEpoch(t *testing.T) {
	st, id := openTraffic(t)
	rec := TrafficRecord{NodeID: id, BootID: "boot", NetCounterEpoch: strings.Repeat("a", 64), TotalRx: 100, PeriodStart: time.Unix(0, 0).UTC()}
	if _, err := st.WriteTraffic(t.Context(), []TrafficRecord{rec}); err != nil {
		t.Fatal(err)
	}
	if err := st.write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE traffic SET net_counter_epoch='invalid'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var epoch string
	if err := st.r.QueryRow("SELECT net_counter_epoch FROM traffic WHERE node_id=?", id).Scan(&epoch); err != nil || epoch != "invalid" {
		t.Fatalf("invalid fixture did not land: %q %v", epoch, err)
	}
	if _, err := st.LoadTraffic(t.Context()); err == nil || !strings.Contains(err.Error(), "net_counter_epoch") {
		t.Fatalf("invalid epoch read: %v", err)
	}
	config := filepath.Join(t.TempDir(), "config.db")
	if err := st.SnapshotConfig(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if _, err := st.WriteTraffic(t.Context(), []TrafficRecord{rec}); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(t.Context(), st.path, config, "", "", time.Now(), slog.Default()); err == nil || !strings.Contains(err.Error(), "net_counter_epoch") {
		t.Fatalf("invalid epoch restored: %v", err)
	}
	got, err := st.LoadTraffic(t.Context())
	if err != nil || len(got) != 1 || got[0] != rec {
		t.Fatalf("rejected restore changed baseline or totals: %+v %v", got, err)
	}
}

func TestTrafficWriteSkipsDeletedNodes(t *testing.T) {
	st, id := openTraffic(t)
	if err := st.DeleteNode(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	written, err := st.WriteTraffic(t.Context(), []TrafficRecord{{NodeID: id, BootID: "b", PeriodStart: time.Unix(0, 0)}})
	if err != nil || len(written) != 0 {
		t.Fatalf("written=%v err=%v, want empty/nil", written, err)
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
