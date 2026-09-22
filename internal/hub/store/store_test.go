package store

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/metric"
	"google.golang.org/protobuf/proto"
)

func open(t *testing.T) (*Store, *clock.Fake) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, err := Open(filepath.Join(t.TempDir(), "t.db"), clk, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, clk
}

func hash(b byte) []byte { h := make([]byte, 32); h[0] = b; return h }

func TestOpenCreatesSchemaAtCurrentVersion(t *testing.T) {
	s, _ := open(t)
	var v int
	if err := s.r.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != schemaVersion {
		t.Fatalf("user_version = %d, want %d", v, schemaVersion)
	}
	var seq int
	if err := s.r.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'sqlite_sequence'").Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if seq != 1 {
		t.Fatal("node.id must be AUTOINCREMENT: SQLite creates sqlite_sequence only when some table uses it")
	}
}

func TestReopenKeepsData(t *testing.T) {
	dir := t.TempDir()
	clk := clock.NewFake(time.Unix(0, 0))
	s, err := Open(filepath.Join(dir, "t.db"), clk, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateNode(context.Background(), "a", hash(1))
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(filepath.Join(dir, "t.db"), clk, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	nodes, err := s.ListNodes(context.Background())
	if err != nil || len(nodes) != 1 || nodes[0].ID != id {
		t.Fatalf("nodes = %+v err = %v", nodes, err)
	}
}

func TestDeletedNodeIDIsNotReused(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	a, _ := s.CreateNode(ctx, "a", hash(1))
	if err := s.DeleteNode(ctx, a); err != nil {
		t.Fatal(err)
	}
	b, _ := s.CreateNode(ctx, "b", hash(2))
	if b == a {
		t.Fatalf("id %d was reused after delete", a)
	}
}

func TestTokenHashesAndRotate(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	id, _ := s.CreateNode(ctx, "a", hash(1))
	if err := s.SetTokenHash(ctx, id, hash(9)); err != nil {
		t.Fatal(err)
	}
	m, err := s.TokenHashes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var k [32]byte
	copy(k[:], hash(9))
	if m[k] != id || len(m) != 1 {
		t.Fatalf("hashes = %v", m)
	}
	if err := s.SetTokenHash(ctx, id+100, hash(3)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rotate on missing node: err = %v, want ErrNotFound", err)
	}
}

func TestRegisterNodeConsumesWindow(t *testing.T) {
	s, clk := open(t)
	ctx := context.Background()
	if _, err := s.RegisterNode(ctx, hash(5), "x", hash(1)); !errors.Is(err, ErrNoWindow) {
		t.Fatalf("no window: err = %v", err)
	}
	if err := s.SetRegisterWindow(ctx, hash(5), clk.Now().Add(time.Hour), 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterNode(ctx, hash(6), "x", hash(1)); !errors.Is(err, ErrBadKey) {
		t.Fatalf("wrong key: err = %v", err)
	}
	if _, err := s.RegisterNode(ctx, hash(5), "x", hash(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterNode(ctx, hash(5), "y", hash(2)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterNode(ctx, hash(5), "z", hash(3)); !errors.Is(err, ErrNoWindow) {
		t.Fatalf("exhausted window must read as closed, err = %v", err)
	}
	w, ok, _ := s.RegisterWindow(ctx)
	if !ok || w.Remaining != 0 {
		t.Fatalf("window = %+v ok=%v", w, ok)
	}
}

func TestExpiredWindowIsClosed(t *testing.T) {
	s, clk := open(t)
	ctx := context.Background()
	_ = s.SetRegisterWindow(ctx, hash(5), clk.Now().Add(time.Minute), 5)
	clk.Advance(2 * time.Minute)
	if _, err := s.RegisterNode(ctx, hash(5), "x", hash(1)); !errors.Is(err, ErrNoWindow) {
		t.Fatalf("expired: err = %v", err)
	}
}

func TestFacts(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	id, _ := s.CreateNode(ctx, "a", hash(1))
	f := &probev1.Facts{Hostname: "h", Os: "o", CpuCores: 4}
	if err := s.UpsertFacts(ctx, id, 77, f); err != nil {
		t.Fatal(err)
	}
	f.Hostname = "h2"
	done := make(chan error, 1)
	s.UpsertFactsAsync(id, 78, f, func(err error) { done <- err })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	m, _ := s.FactsHashes(ctx)
	if m[id] != 78 {
		t.Fatalf("facts hash = %d, want 78", m[id])
	}
}

func bucket(cpu float64) *metric.Bucket {
	b := metric.NewBucket()
	b.Add(&probev1.Metrics{CpuPct: proto.Float64(cpu)})
	return b
}

func TestHalfBucketsMergeAdditively(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	id, _ := s.CreateNode(ctx, "a", hash(1))
	if _, err := s.WriteMinuteRows(ctx, []metric.Row{{NodeID: id, TS: 600, Bucket: bucket(10)}}); err != nil {
		t.Fatal(err)
	}
	b := bucket(30)
	b.Add(&probev1.Metrics{CpuPct: proto.Float64(50)})
	if _, err := s.WriteMinuteRows(ctx, []metric.Row{{NodeID: id, TS: 600, Bucket: b}}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ReadMinuteRows(ctx, id, 0, 1000)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v err = %v", rows, err)
	}
	if rows[0].Bucket.Sum[0] != 90 || rows[0].Bucket.N[0] != 3 || rows[0].Bucket.Max[0] != 50 {
		t.Fatalf("merged = %v/%d/%v, want 90/3/50", rows[0].Bucket.Sum[0], rows[0].Bucket.N[0], rows[0].Bucket.Max[0])
	}
}

func TestMissingMetricReadsBackAsNoData(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	id, _ := s.CreateNode(ctx, "a", hash(1))
	_, _ = s.WriteMinuteRows(ctx, []metric.Row{{NodeID: id, TS: 600, Bucket: bucket(10)}})
	rows, _ := s.ReadMinuteRows(ctx, id, 0, 1000)
	for i, c := range metric.Columns {
		_, ok := rows[0].Bucket.Mean(i)
		if c.Name == "cpu" && !ok {
			t.Fatal("cpu had a reading")
		}
		if c.Name != "cpu" && ok {
			t.Fatalf("%s had no readings but reads back as data", c.Name)
		}
	}
}

func TestWriterRejectsRowsBeforeRollupWatermark(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	id, _ := s.CreateNode(ctx, "a", hash(1))
	if err := s.SetRollupWatermark(ctx, "5m", 900); err != nil {
		t.Fatal(err)
	}
	rejected, err := s.WriteMinuteRows(ctx, []metric.Row{
		{NodeID: id, TS: 600, Bucket: bucket(1)},
		{NodeID: id, TS: 900, Bucket: bucket(2)},
	})
	if err != nil || rejected != 1 {
		t.Fatalf("rejected = %d err = %v, want 1 nil", rejected, err)
	}
	rows, _ := s.ReadMinuteRows(ctx, id, 0, 2000)
	if len(rows) != 1 || rows[0].TS != 900 {
		t.Fatalf("rows = %+v, want only ts 900", rows)
	}
}

func TestWriteMinuteRowsUpdatesLastSeen(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	id, _ := s.CreateNode(ctx, "a", hash(1))
	seen := time.Unix(1234, 0).UTC()
	_, _ = s.WriteMinuteRows(ctx, []metric.Row{{NodeID: id, TS: 1200, Bucket: bucket(1), LastSeen: seen}})
	nodes, _ := s.ListNodes(ctx)
	if !nodes[0].LastSeenAt.Equal(seen) {
		t.Fatalf("last_seen_at = %v, want %v", nodes[0].LastSeenAt, seen)
	}
}

func TestDeleteNodeRemovesDependentRows(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	id, _ := s.CreateNode(ctx, "a", hash(1))
	_ = s.UpsertFacts(ctx, id, 1, &probev1.Facts{})
	_, _ = s.WriteMinuteRows(ctx, []metric.Row{{NodeID: id, TS: 600, Bucket: bucket(1)}})
	if err := s.DeleteNode(ctx, id); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.FactsHashes(ctx); len(m) != 0 {
		t.Fatalf("facts survived delete: %v", m)
	}
	if rows, _ := s.ReadMinuteRows(ctx, id, 0, 1000); len(rows) != 0 {
		t.Fatalf("metric rows survived delete: %v", rows)
	}
}

func TestAsyncCallbackObservesCommittedWrite(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	id, _ := s.CreateNode(ctx, "a", hash(1))
	if err := s.UpsertFacts(ctx, id, 77, &probev1.Facts{}); err != nil {
		t.Fatal(err)
	}
	seen := make(chan uint64, 1)
	s.UpsertFactsAsync(id, 78, &probev1.Facts{}, func(err error) {
		if err != nil {
			t.Error(err)
		}
		m, _ := s.FactsHashes(ctx)
		seen <- m[id]
	})
	if got := <-seen; got != 78 {
		t.Fatalf("callback observed facts hash %d, want 78: it must run only after the transaction committed", got)
	}
}
