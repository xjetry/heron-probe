package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
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

func TestWriteAfterCloseReturnsErrClosed(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprint(async), func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "closed.db"), clock.Real(), slog.Default())
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			fn := func(*sql.Tx) error { t.Error("closed store executed a write"); return nil }
			if async {
				result := make(chan error, 1)
				s.writeAsync(fn, func(e error) { result <- e })
				select {
				case err = <-result:
				case <-time.After(2 * time.Second):
					t.Fatal("closed async write did not notify")
				}
			} else {
				err = s.write(context.Background(), fn)
			}
			if !errors.Is(err, ErrClosed) {
				t.Fatalf("write after close = %v, want ErrClosed", err)
			}
		})
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	s, _ := open(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
}

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

// 已入队但尚未开始的事务在 ctx 取消后不执行，调用方得到 ctx.Err() 且库无变化。
func TestCancelBeforeStartSkipsTransaction(t *testing.T) {
	s, clk := open(t)
	gate := make(chan struct{})
	started := make(chan struct{})
	go s.write(context.Background(), func(*sql.Tx) error { close(started); <-gate; return nil })
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := make(chan error, 1)
	go func() { _, err := s.CreateNode(ctx, "queued", hash(1)); res <- err }()
	// 第一个事务占住写协程，队列非空只能来自 CreateNode；确认入队后取消，
	// 才能检出写协程遗漏取消预检，而不是只覆盖入队前的取消分支。
	deadline := time.Now().Add(5 * time.Second)
	for len(s.writes) == 0 {
		if time.Now().After(deadline) {
			cancel()
			close(gate)
			<-res
			t.Fatal("CreateNode did not enter the write queue")
		}
		runtime.Gosched()
	}
	cancel()
	close(gate)
	if err := <-res; !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	// 同步写屏障保证即使等待方错误地提前返回，节点事务也已结束。
	if err := s.SetRegisterWindow(context.Background(), hash(3), clk.Now().Add(time.Hour), 1); err != nil {
		t.Fatal(err)
	}
	nodes, err := s.ListNodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Fatalf("node was created despite cancellation before start: %+v", nodes)
	}
}

// 已开始的事务不受取消影响，调用方必须拿到真实结果：nil 且库里有行。
func TestCancelDuringTransactionStillReportsCommit(t *testing.T) {
	s, _ := open(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inside := make(chan struct{})
	res := make(chan error, 1)
	go func() {
		res <- s.write(ctx, func(tx *sql.Tx) error {
			close(inside)
			<-ctx.Done() // 事务进行中 ctx 被取消
			_, err := tx.Exec("INSERT INTO node (name, token_hash, created_at) VALUES ('mid', ?, 0)", hash(2))
			return err
		})
	}()
	<-inside
	cancel()
	if err := <-res; err != nil {
		t.Fatalf("err = %v, want nil: the transaction committed", err)
	}
	nodes, err := s.ListNodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("committed row missing: %+v", nodes)
	}
}

func TestListNodesCarriesFactsAndOrder(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	a, _ := s.CreateNode(ctx, "a", hash(1))
	b, _ := s.CreateNode(ctx, "b", hash(2))
	if err := s.UpsertFacts(ctx, b, 7, &probev1.Facts{Hostname: "hb", CpuCores: 4, IcmpAvailable: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReorderNodes(ctx, []int64{b, a}); err != nil {
		t.Fatal(err)
	}
	nodes, err := s.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 || nodes[0].ID != b || nodes[1].ID != a {
		t.Fatalf("order = %+v, want b before a", nodes)
	}
	if nodes[0].Facts == nil || nodes[0].Facts.Hostname != "hb" || nodes[0].Facts.CpuCores != 4 || !nodes[0].Facts.IcmpAvailable || nodes[0].FactsUpdatedAt.IsZero() {
		t.Fatalf("facts not joined: %+v", nodes[0])
	}
	if nodes[1].Facts != nil {
		t.Fatalf("node without facts must carry nil, got %+v", nodes[1].Facts)
	}
	if nodes[0].SortOrder != 0 || nodes[1].SortOrder != 1 {
		t.Fatalf("sort_order = %d, %d", nodes[0].SortOrder, nodes[1].SortOrder)
	}
}

func TestReorderNodesRejectsAnythingButAFullPermutation(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	a, _ := s.CreateNode(ctx, "a", hash(1))
	b, _ := s.CreateNode(ctx, "b", hash(2))
	for _, ids := range [][]int64{{a}, {a, b, 999}, {a, a}, {b, 999}} {
		if err := s.ReorderNodes(ctx, ids); !errors.Is(err, ErrBadOrder) {
			t.Fatalf("ReorderNodes(%v) = %v, want ErrBadOrder", ids, err)
		}
	}
	nodes, _ := s.ListNodes(ctx)
	if nodes[0].ID != a || nodes[1].ID != b {
		t.Fatalf("rejected reorder must leave order untouched: %+v", nodes)
	}
}

func TestUpdateNodeReplacesEditableFields(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	id, _ := s.CreateNode(ctx, "old", hash(1))
	if err := s.UpdateNode(ctx, id, "new", true, "note"); err != nil {
		t.Fatal(err)
	}
	n, err := s.GetNode(ctx, id)
	if err != nil || n.Name != "new" || !n.Public || n.Note != "note" {
		t.Fatalf("GetNode = %+v, %v", n, err)
	}
	if err := s.UpdateNode(ctx, 999, "x", false, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v, want ErrNotFound", err)
	}
	if _, err := s.GetNode(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetNode(999) = %v, want ErrNotFound", err)
	}
}

func TestDeleteNodeClearsEveryLevel(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	id, _ := s.CreateNode(ctx, "n", hash(1))
	keep, _ := s.CreateNode(ctx, "keep", hash(2))
	for _, tbl := range metricTables {
		for _, node := range []int64{id, keep} {
			args := append([]any{node, int64(600)}, bucketArgs(metric.NewBucket())...)
			if _, err := s.w.ExecContext(ctx, metricUpsert(tbl), args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.DeleteNode(ctx, id); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range metricTables {
		var n int64
		if err := s.r.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+tbl+" WHERE node_id = ?", id).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s still has %d rows for deleted node (%v)", tbl, n, err)
		}
		if err := s.r.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+tbl+" WHERE node_id = ?", keep).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s lost the other node's row: %d (%v)", tbl, n, err)
		}
	}
}
