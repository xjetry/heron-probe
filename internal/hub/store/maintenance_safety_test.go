package store

import (
	"context"
	"database/sql"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"
)

type logWriterFunc func([]byte) (int, error)

func (f logWriterFunc) Write(b []byte) (int, error) { return f(b) }

func TestMaintenanceSkipsPruneAfterRollupError(t *testing.T) {
	s, clk := open(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk.SetWall(clk.Now().Add(59*time.Second + 999*time.Millisecond))
	id, err := s.CreateNode(ctx, "n", hash(1))
	if err != nil {
		t.Fatal(err)
	}
	ts := clk.Now().Add(-400 * 24 * time.Hour).Truncate(time.Hour).Unix()
	if err := s.write(ctx, func(tx *sql.Tx) error {
		args := append([]any{id, ts}, bucketArgs(bucket(3))...)
		if _, err := tx.Exec(metricUpsert("metric_1h"), args...); err != nil {
			return err
		}
		_, err := tx.Exec("CREATE TRIGGER block_rollup BEFORE UPDATE ON rollup_state BEGIN SELECT RAISE(ABORT, 'rollup blocked'); END")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	s.log = slog.New(slog.NewTextHandler(logWriterFunc(func(b []byte) (int, error) {
		if strings.Contains(string(b), "rollup failed") {
			cancel()
		}
		return len(b), nil
	}), nil))
	done := make(chan struct{})
	go func() { s.RunMaintenance(ctx, DefaultRetention); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatal("maintenance did not report rollup failure")
	}
	if len(readLevel(t, s, levels[2], id)) != 1 {
		t.Fatal("maintenance pruned after rollup failure")
	}
}

func TestPruneWaitsForConsumer(t *testing.T) {
	s, clk := open(t)
	ctx := context.Background()
	id, err := s.CreateNode(ctx, "n", hash(1))
	if err != nil {
		t.Fatal(err)
	}
	ts := clk.Now().Add(-8 * 24 * time.Hour).Truncate(time.Hour).Unix()
	if _, err := s.WriteMinuteRows(ctx, minuteRows(id, ts, ts+60)); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Prune(ctx, DefaultRetention); err != nil || n != 0 || len(readLevel(t, s, levels[0], id)) != 1 {
		t.Fatalf("unconsumed 1m row deleted: n=%d err=%v", n, err)
	}
	if err := s.Rollup(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Prune(ctx, DefaultRetention); err != nil || n != 1 || len(readLevel(t, s, levels[0], id)) != 0 {
		t.Fatalf("consumed 1m row retained: n=%d err=%v", n, err)
	}
	// 5m 也必须等待自己的消费者；不把 5m 水位误用作 1h 消费凭据。
	if err := s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE rollup_state SET upto_ts = 0 WHERE level = '1h'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r := Retention{6 * time.Hour, 7 * 24 * time.Hour, 365 * 24 * time.Hour}
	if n, err := s.Prune(ctx, r); err != nil || n != 0 || len(readLevel(t, s, levels[1], id)) != 1 {
		t.Fatalf("unconsumed 5m row deleted: n=%d err=%v", n, err)
	}
	if err := s.Rollup(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Prune(ctx, r); err != nil || n != 1 || len(readLevel(t, s, levels[1], id)) != 0 {
		t.Fatalf("consumed 5m row retained: n=%d err=%v", n, err)
	}
}

func TestRollupUsesPrimaryKeyRanges(t *testing.T) {
	s, _ := open(t)
	for _, lv := range levels[1:] {
		rows, err := s.r.Query("EXPLAIN QUERY PLAN "+rollupSQL(lv), 0, 86400)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		// 这是当前 modernc.org/sqlite 版本的实测约束，升级驱动时必须重新执行。
		t.Logf("%s query plan:\n%s", lv.Name, strings.Join(plan, "\n"))
		if p := strings.Join(plan, "\n"); strings.Contains(p, "SCAN "+lv.Source) || !strings.Contains(p, "SEARCH "+lv.Source) {
			t.Fatalf("rollup must seek source primary key ranges:\n%s", p)
		}
	}
}

func TestRollupCatchupCommitsBoundedSlices(t *testing.T) {
	for _, lv := range levels[1:] {
		t.Run(lv.Name, func(t *testing.T) {
			s, _ := open(t)
			ctx := context.Background()
			id, err := s.CreateNode(ctx, "n", hash(1))
			if err != nil {
				t.Fatal(err)
			}
			width := int64(86400)
			if lv.Name == "1h" {
				width *= 7
			}
			base := int64(1_767_225_600)
			limit := base + 2*width + lv.Bucket
			if err := s.write(ctx, func(tx *sql.Tx) error {
				for ts := base; ts < limit; ts += lv.Bucket {
					args := append([]any{id, ts}, bucketArgs(bucket(3))...)
					if _, err := tx.Exec(metricUpsert(lv.Source), args...); err != nil {
						return err
					}
				}
				_, err := tx.Exec(rollupSQL(lv), base, limit)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			want := readLevel(t, s, lv, id)
			if err := s.write(ctx, func(tx *sql.Tx) error { _, err := tx.Exec("DELETE FROM " + lv.Table); return err }); err != nil {
				t.Fatal(err)
			}
			trace := traceWrites(t, s)
			upto, err := s.rollupLevel(ctx, lv, limit)
			if err != nil {
				t.Fatal(err)
			}
			commits := trace.snapshot()
			t.Logf("%s catchup committed %d transactions, watermark=%d", lv.Name, len(commits), upto)
			if len(commits) < 2 {
				t.Fatalf("catchup used %d transactions, want multiple", len(commits))
			}
			for _, statements := range commits {
				for _, stmt := range statements {
					if strings.HasPrefix(stmt.query, "INSERT OR REPLACE INTO "+lv.Table) {
						span := stmt.args[1].Value.(int64) - stmt.args[0].Value.(int64)
						if span > width {
							t.Fatalf("rollup slice span=%d exceeds %d", span, width)
						}
					}
				}
			}
			if upto != limit || watermark(t, s, lv.Name) != limit {
				t.Fatalf("catchup watermark=%d, want %d", upto, limit)
			}
			got := readLevel(t, s, lv, id)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("chunked result differs from single range: got %d want %d", len(got), len(want))
			}
			if _, err := s.rollupLevel(ctx, lv, limit); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(readLevel(t, s, lv, id), got) {
				t.Fatal("catchup result is not idempotent")
			}
		})
	}
}

func TestNodeCreationAppendsAfterReorder(t *testing.T) {
	for _, registered := range []bool{false, true} {
		t.Run(map[bool]string{false: "CreateNode", true: "RegisterNode"}[registered], func(t *testing.T) {
			s, clk := open(t)
			ctx := context.Background()
			a, err := s.CreateNode(ctx, "a", hash(1))
			if err != nil {
				t.Fatal(err)
			}
			b, err := s.CreateNode(ctx, "b", hash(2))
			if err != nil {
				t.Fatal(err)
			}
			if err := s.ReorderNodes(ctx, []int64{b, a}); err != nil {
				t.Fatal(err)
			}
			var c int64
			if registered {
				if err := s.SetRegisterWindow(ctx, hash(9), clk.Now().Add(time.Hour), 1); err != nil {
					t.Fatal(err)
				}
				c, err = s.RegisterNode(ctx, hash(9), "c", hash(3))
			} else {
				c, err = s.CreateNode(ctx, "c", hash(3))
			}
			if err != nil {
				t.Fatal(err)
			}
			nodes, err := s.ListNodes(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var ids []int64
			for _, n := range nodes {
				ids = append(ids, n.ID)
			}
			if !reflect.DeepEqual(ids, []int64{b, a, c}) {
				t.Fatalf("new node displaced reordered nodes: got %v want %v", ids, []int64{b, a, c})
			}
		})
	}
}
