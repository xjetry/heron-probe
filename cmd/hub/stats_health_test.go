package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/clock"
	"github.com/xjetry/heron-probe/internal/hub/auth"
	"github.com/xjetry/heron-probe/internal/hub/metric"
	"github.com/xjetry/heron-probe/internal/hub/store"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// CLI 的健康行与 GetStorageStats 的响应出自同一份 store.StorageStats：同一个库上，行数与顺序和响应一一对应、
// 数值相等。响应经 API token 取得，也钉住只读口径可以调用它。
func TestStatsHealthLinesMatchGetStorageStats(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hub.db")
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := store.Open(db, clk, slog.Default(), store.MigrateSchema)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	node, _, err := st.CreateNode(ctx, "n", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	ts := clk.Now().Add(-time.Hour).Unix()
	b := metric.NewBucket()
	b.Add(&heronv1.Metrics{CpuPct: proto.Float64(5)})
	batch := metric.Batch{
		Rows:   []metric.Row{{NodeID: node, TS: ts, Bucket: b}},
		Probes: []metric.ProbeRow{{NodeID: node, TS: ts, TaskID: 1, Bucket: &metric.ProbeBucket{Sent: 1}}},
	}
	if _, err := st.WriteMinuteBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	// 只上卷、不清理：CLI 与响应里 rollup 有值、prune 缺失，两种形态都要对上。
	if err := st.Rollup(ctx); err != nil {
		t.Fatal(err)
	}
	plain, hash := auth.NewAPIToken()
	if _, err := st.CreateAPIToken(ctx, "reader", hash, clk.Now(), 100); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := runStatsWith([]string{"--db", db}, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	start := slices.IndexFunc(lines, func(l string) bool { k, _, _ := strings.Cut(l, ":"); return strings.Contains(k, ".") })
	if start < 0 {
		t.Fatalf("no health lines in %q", out.String())
	}
	cli := lines[start:]

	st, err = store.Open(db, clk, slog.Default(), store.MigrateSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := httptest.NewServer(newTestMuxOn(t, st, clk, "http://"+testThemeHost))
	t.Cleanup(srv.Close)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/heron.v1.AdminService/GetStorageStats", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+plain)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GetStorageStats with an API token: %d %s %v", resp.StatusCode, body, err)
	}
	var msg heronv1.GetStorageStatsResponse
	if err := protojson.Unmarshal(body, &msg); err != nil {
		t.Fatal(err)
	}
	orNoneOf := func(has bool, v int64) string {
		if !has {
			return "none"
		}
		return fmt.Sprint(v)
	}
	var want []string
	for _, h := range msg.GetSeries() {
		want = append(want, h.GetTable()+".oldest: "+orNoneOf(h.OldestTs != nil, h.GetOldestTs()))
		if h.WatermarkTs != nil {
			want = append(want, fmt.Sprintf("%s.watermark: %d", h.GetTable(), h.GetWatermarkTs()))
		}
	}
	want = append(want, "prune.finished_at: "+orNoneOf(msg.LastPruneAt != nil, msg.GetLastPruneAt()),
		"rollup.finished_at: "+orNoneOf(msg.LastRollupAt != nil, msg.GetLastRollupAt()))
	if !slices.Equal(cli, want) {
		t.Fatalf("CLI health lines:\n%s\nAPI response as lines:\n%s", strings.Join(cli, "\n"), strings.Join(want, "\n"))
	}
	// 两边一致之外，内容本身也要是这次造的数据：否则两边一起为空也会相等。
	for _, line := range []string{fmt.Sprintf("metric_1m.oldest: %d", ts-ts%60), fmt.Sprintf("probe_1m.oldest: %d", ts-ts%60),
		fmt.Sprintf("rollup.finished_at: %d", clk.Now().Unix()), "prune.finished_at: none", "metric_1h.oldest: none"} {
		if !slices.Contains(cli, line) {
			t.Errorf("CLI health lines lack %q:\n%s", line, strings.Join(cli, "\n"))
		}
	}
}
