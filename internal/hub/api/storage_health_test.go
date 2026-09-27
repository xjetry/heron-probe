package api

import (
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/hub/metric"
	"github.com/xjetry/probe/internal/hub/store"
)

func (h *harness) storageStatsWithToken(t *testing.T, tok string) *probev1.GetStorageStatsResponse {
	t.Helper()
	client := probev1connect.NewAdminServiceClient(h.srv.Client(), h.srv.URL)
	req := connect.NewRequest(&probev1.GetStorageStatsRequest{})
	req.Header().Set("Authorization", "Bearer "+tok)
	resp, err := client.GetStorageStats(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	return resp.Msg
}

// GetStorageStats 按表给最老桶、水位、保留期与两项判定，经 API token 可读。1m 指标表的最老桶比阈值早 60 秒，
// 1m 探测表恰在阈值上：前者标红、后者不标。上卷之前水位是种子值 0，四张有水位的表都标红，也没有上卷完成时刻；
// 上卷之后水位跟上、不再标红，完成时刻就是上卷时的时钟。
func TestGetStorageStatsReportsHealthWithStaleness(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	_, tok := createToken(t, h, "reader")
	node, _ := h.createNode(t, "n")
	now := h.clk.Now().Unix()
	day := int64(24 * 3600)
	// 阈值 = 保留期 7 天 + 桶长 60 秒 + 维护间隔 60 秒（spec §6.5 的字面值）。
	edge := now - 7*day - 60 - 60
	b := metric.NewBucket()
	b.Add(&probev1.Metrics{CpuPct: proto.Float64(5)})
	batch := metric.Batch{
		Rows:   []metric.Row{{NodeID: node, TS: edge - 60, Bucket: b}},
		Probes: []metric.ProbeRow{{NodeID: node, TS: edge, TaskID: 1, Bucket: &metric.ProbeBucket{Sent: 1}}},
	}
	if _, err := h.store.WriteMinuteBatch(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	type row struct {
		table                   string
		bucket                  uint32
		retention               uint64
		oldest, watermark       *int64
		oldestStale, waterStale bool
	}
	got := func(msg *probev1.GetStorageStatsResponse) []row {
		var out []row
		for _, s := range msg.GetSeries() {
			out = append(out, row{s.GetTable(), s.GetBucketS(), s.GetRetentionS(), s.OldestTs, s.WatermarkTs, s.GetOldestStale(), s.GetWatermarkStale()})
		}
		return out
	}
	check := func(label string, msg *probev1.GetStorageStatsResponse, want []row) {
		t.Helper()
		rows := got(msg)
		if len(rows) != len(want) {
			t.Fatalf("%s: %d series, want %d: %+v", label, len(rows), len(want), rows)
		}
		for i := range want {
			g, w := rows[i], want[i]
			if g.table != w.table || g.bucket != w.bucket || g.retention != w.retention || !sameInt(g.oldest, w.oldest) ||
				!sameInt(g.watermark, w.watermark) || g.oldestStale != w.oldestStale || g.waterStale != w.waterStale {
				t.Errorf("%s: series[%d] = %s, want %s", label, i, describeRow(g.table, g.oldest, g.watermark, g.oldestStale, g.waterStale), describeRow(w.table, w.oldest, w.watermark, w.oldestStale, w.waterStale))
			}
		}
	}
	r := store.DefaultRetention
	secs := func(d time.Duration) uint64 { return uint64(d / time.Second) }
	zero := int64(0)
	before := h.storageStatsWithToken(t, tok)
	check("before rollup", before, []row{
		{"metric_1m", 60, secs(r.M1), ptrTo(edge - 60), nil, true, false},
		{"metric_5m", 300, secs(r.M5), nil, &zero, false, true},
		{"metric_1h", 3600, secs(r.H1), nil, &zero, false, true},
		{"probe_1m", 60, secs(r.M1), ptrTo(edge), nil, false, false},
		{"probe_5m", 300, secs(r.M5), nil, &zero, false, true},
		{"probe_1h", 3600, secs(r.H1), nil, &zero, false, true},
	})
	if before.LastRollupAt != nil || before.LastPruneAt != nil {
		t.Fatalf("maintenance before any run: prune=%v rollup=%v", before.LastPruneAt, before.LastRollupAt)
	}

	if err := h.store.Rollup(t.Context()); err != nil {
		t.Fatal(err)
	}
	after := h.storageStatsWithToken(t, tok)
	for _, s := range after.GetSeries() {
		if s.GetWatermarkStale() || (s.WatermarkTs != nil && now-s.GetWatermarkTs() > 3*int64(s.GetBucketS())) {
			t.Errorf("%s watermark %d still behind after rollup (stale=%v)", s.GetTable(), s.GetWatermarkTs(), s.GetWatermarkStale())
		}
	}
	if after.LastRollupAt == nil || after.GetLastRollupAt() != now || after.LastPruneAt != nil {
		t.Fatalf("maintenance after rollup: prune=%v rollup=%v, want rollup %d", after.LastPruneAt, after.LastRollupAt, now)
	}
}

func ptrTo(v int64) *int64 { return &v }

func sameInt(a, b *int64) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

func describeRow(table string, oldest, watermark *int64, oldestStale, waterStale bool) string {
	show := func(v *int64) any {
		if v == nil {
			return "absent"
		}
		return *v
	}
	return fmt.Sprintf("%s oldest=%v watermark=%v oldest_stale=%v watermark_stale=%v", table, show(oldest), show(watermark), oldestStale, waterStale)
}
