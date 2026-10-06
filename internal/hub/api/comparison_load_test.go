package api

// §2.4 成本验收：数据与负载都重（约 56M 行、分钟级耗时、多 GiB 库），默认跳过。跑法：
//
//	HERON_LOAD=1 [HERON_LOAD_DB=<目录>] go test ./internal/hub/api/ -run TestProbeComparisonCostAcceptance -v -timeout 2h
//
// HERON_LOAD_DB 指定的目录里已有 load.db 时跳过生成直接复用（升级计时也用它）。
// 结论（机型、各组 p50/p99、内存峰值、N_max）由跑的人回填 result.md，原始输出存任务 logs/。
//
// 入口是真实的公开处理器（限流 → 缓存头 → connect → Public），外面包一层按过程计数的
// 中间件当读路径计数器：除 GetSnapshot（另有快照字节缓存）外，进到 connect 层就是进了
// 读取路径；负载全部用 POST，客户端与公开页的缓存都不参与。水位回拨用第二个直连连接
// 写 rollup_state，等价于维护进程在推进水位。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	_ "modernc.org/sqlite"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/gen/heron/v1/heronv1connect"
	"github.com/xjetry/heron-probe/internal/clock"
	"github.com/xjetry/heron-probe/internal/hub/live"
	"github.com/xjetry/heron-probe/internal/hub/probe"
	"github.com/xjetry/heron-probe/internal/hub/store"
	"github.com/xjetry/heron-probe/internal/hub/traffic"
)

// loadClock：墙钟固定在数据的"现在"，单调钟走真实时间——限流桶按它补充令牌。
type loadClock struct {
	wall  time.Time
	start time.Time
}

func (c *loadClock) Now() time.Time      { return c.wall }
func (c *loadClock) Mono() time.Duration { return time.Since(c.start) }

const (
	loadNodesFull = 152 // 总节点数（其余 120 个只有节点行）
	loadBusyFull  = 32  // 各有 64 个任务的节点数
	loadTasksFull = 64  // 每个忙节点的任务数（被比任务 + 另外 63 个）
	loadCallsFull = 30  // 每个尺寸点每窗口的调用次数
	hammerSecFull = 30

	hammerSource = "198.51.100.1"
	readerSource = "198.51.100.2"
	burstSource  = "198.51.100.3"
	probeSource  = "198.51.100.4" // ④ 受保护读者专用：与前面几组的桶无关。
)

// smoke：HERON_LOAD_SMOKE=1 时全部缩到分钟级，验证跑法本身；结果不作数。
var smoke = os.Getenv("HERON_LOAD_SMOKE") != ""

func loadSizes() (nodes, busy, tasks, calls, hammerSec int) {
	if smoke {
		return 10, 4, 3, 3, 2
	}
	return loadNodesFull, loadBusyFull, loadTasksFull, loadCallsFull, hammerSecFull
}

func TestProbeComparisonCostAcceptance(t *testing.T) {
	if os.Getenv("HERON_LOAD") == "" {
		t.Skip("set HERON_LOAD=1 to run the §2.4 cost acceptance (heavy: minutes, multi-GB database)")
	}
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	day := int64(86400)
	dir := os.Getenv("HERON_LOAD_DB")
	if dir == "" {
		dir = t.TempDir()
	}
	path := filepath.Join(dir, "load.db")
	clk := &loadClock{wall: now, start: time.Now()}
	if _, err := os.Stat(path); err != nil {
		genLoadDatabase(t, path, now.Unix())
	} else {
		t.Logf("reusing existing database %s", path)
	}
	tuner, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tuner.Close() })

	// 内存峰值采样：50ms 一次，读的是服务进程（即本测试进程）的运行时。
	var peakHeap atomic.Int64
	stopPeak := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopPeak:
				return
			default:
			}
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			if int64(m.HeapAlloc) > peakHeap.Load() {
				peakHeap.Store(int64(m.HeapAlloc))
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	defer close(stopPeak)

	st, err := store.Open(path, clk, slog.Default(), store.MigrateSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	l := live.New(clk, 30*time.Second)
	book := traffic.New(st, clk, time.UTC, slog.Default())
	reg := probe.New(st, slog.Default())
	if err := errors.Join(book.Load(t.Context()), reg.Load(t.Context())); err != nil {
		t.Fatal(err)
	}
	pub := NewPublic(PublicConfig{ReportInterval: 10 * time.Second, Location: time.UTC,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}},
		st, l, book, reg, clk, slog.Default())
	pubPath, pubHandler := pub.Handler()
	counter := &countingServer{inner: pubHandler}
	_ = pubPath
	srv := httptest.NewServer(counter)
	t.Cleanup(srv.Close)

	nodes, busy, tasks, calls, hammerSec := loadSizes()
	plain := newSourcedClient(srv.URL, readerSource)
	hammer := newSourcedClient(srv.URL, hammerSource)

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	baseHeap := mem.HeapAlloc
	report := newLoadReport(path)

	// ① 空载基线：单节点指标与满配任务的探测各 30 次。
	g1m := runLoadGroup("① QueryMetrics 6h", calls, func() (*loadOutcome, error) {
		r, err := plain.QueryMetrics(ctxOf(t), connect.NewRequest(&heronv1.QueryMetricsRequest{NodeId: 1, From: now.Unix() - 6*3600, To: now.Unix(), MaxPoints: 720}))
		return metricsOutcome(r), err
	})
	g1p := runLoadGroup("① QueryProbes 6h（64 任务）", calls, func() (*loadOutcome, error) {
		r, err := plain.QueryProbes(ctxOf(t), connect.NewRequest(&heronv1.QueryProbesRequest{NodeId: 1, From: now.Unix() - 6*3600, To: now.Unix(), MaxPoints: 720}))
		return seriesOutcome(r, tasks), err
	})
	report.group(g1m)
	report.group(g1p)

	// ② 对比分块与 ③ 单节点对照。尺寸阶梯到 N_max 为止（16 是 32 时代的尺寸，已随降档移除）。
	sizes := []int{1, 2, 4, 8, store.MaxComparisonNodes}
	sizes = slices.DeleteFunc(sizes, func(n int) bool { return n > store.MaxComparisonNodes || n > nodes })
	var g2, g3 []*loadGroup
	for _, w := range []struct {
		name string
		span int64
	}{{"6h", 6 * 3600}, {"7d", 7 * day}, {"30d", 30 * day}, {"365d", 365 * day}} {
		for _, n := range sizes {
			ids := firstN(n)
			g2 = append(g2, runLoadGroup(fmt.Sprintf("② 对比 %2d 节点 %s", n, w.name), calls, func() (*loadOutcome, error) {
				r, err := plain.QueryProbeComparison(ctxOf(t), connect.NewRequest(&heronv1.QueryProbeComparisonRequest{
					TaskId: 1, NodeIds: ids, From: now.Unix() - w.span, To: now.Unix(), MaxPoints: 720}))
				return comparisonOutcome(r, n), err
			}))
		}
		g3 = append(g3, runLoadGroup(fmt.Sprintf("③ 单节点 %s", w.name), calls, func() (*loadOutcome, error) {
			r, err := plain.QueryProbes(ctxOf(t), connect.NewRequest(&heronv1.QueryProbesRequest{NodeId: 1, From: now.Unix() - w.span, To: now.Unix(), MaxPoints: 720}))
			return seriesOutcome(r, tasks), err
		}))
	}
	for _, g := range g2 {
		report.group(g)
	}
	for _, g := range g3 {
		report.group(g)
	}
	pass23 := true
	for i := range 4 {
		var worst time.Duration
		for j := range len(sizes) {
			if d := g2[len(sizes)*i+j].p99(); d > worst {
				worst = d
			}
			if g2[len(sizes)*i+j].success != calls {
				pass23 = false
			}
		}
		if worst > g3[i].p99() || g3[i].success != calls {
			pass23 = false
		}
	}
	report.note(fmt.Sprintf("② 每窗口 p99 ≤ ③ 同窗口 p99 且全部成功：%v", pass23))

	// ④ 饱和：一个来源按限流满速开环打 30 秒 365d 分块；受保护的读者走另一来源。
	sat := runSaturation(t, srv.URL, hammer, now, hammerSec, tasks)
	report.group(sat.hammer)
	report.group(sat.reader1)
	report.group(sat.reader2)
	report.group(sat.burst)
	report.note(sat.verdict())

	// ⑤ 维护积压：回拨到额度内重复②③的 365d 形状，再回拨到超出额度，请求被拒。
	setWatermarks := func(w5m, w1h int64) {
		for level, upto := range map[string]int64{"probe_5m": w5m, "probe_1h": w1h} {
			if _, err := tuner.Exec("UPDATE rollup_state SET upto_ts = ? WHERE level = ?", upto, level); err != nil {
				t.Fatal(err)
			}
		}
	}
	// ⑤ 的两档按 N_max=8 校准（R = 12000×8 = 96k；数据范围：1m 表 7 天、5m 表 30 天、1h 表 365 天）：
	// 额度内 = 1h 0 + 5m 6d（13824）+ 1m 7d（80640）= 94464 ≤ R（单节点 755712 ≤ 768000）；
	// 超额 = 1h 335d（64320）+ 5m 23d（52992）+ 1m 7d（80640）= 197952 > R。
	setWatermarks(now.Unix()-24*day, now.Unix()-365*day)
	chunk := min(busy, store.MaxComparisonNodes)
	within := runLoadGroup(fmt.Sprintf("⑤ 额度内 365d（%d 节点）", chunk), calls, func() (*loadOutcome, error) {
		r, err := plain.QueryProbeComparison(ctxOf(t), connect.NewRequest(&heronv1.QueryProbeComparisonRequest{
			TaskId: 1, NodeIds: firstN(chunk), From: now.Unix() - 365*day, To: now.Unix(), MaxPoints: 720}))
		return comparisonOutcome(r, chunk), err
	})
	withinSingle := runLoadGroup("⑤ 额度内 单节点 365d", calls, func() (*loadOutcome, error) {
		r, err := plain.QueryProbes(ctxOf(t), connect.NewRequest(&heronv1.QueryProbesRequest{NodeId: 1, From: now.Unix() - 365*day, To: now.Unix(), MaxPoints: 720}))
		return seriesOutcome(r, tasks), err
	})
	report.group(within)
	report.group(withinSingle)
	report.note(fmt.Sprintf("⑤ 额度内全部成功：%v", within.success == calls && withinSingle.success == calls))

	setWatermarks(now.Unix()-7*day, now.Unix()-30*day)
	over := runLoadGroup(fmt.Sprintf("⑤ 超额 365d（%d 节点）", chunk), calls, func() (*loadOutcome, error) {
		_, err := plain.QueryProbeComparison(ctxOf(t), connect.NewRequest(&heronv1.QueryProbeComparisonRequest{
			TaskId: 1, NodeIds: firstN(chunk), From: now.Unix() - 365*day, To: now.Unix(), MaxPoints: 720}))
		return nil, err
	})
	report.group(over)
	report.note(fmt.Sprintf("⑤ 超额全部被拒（FailedPrecondition）：%v（样例：%v）",
		over.quotaRejected == calls && over.other == 0, oneLine(over.lastQuota)))
	setWatermarks(now.Unix(), now.Unix())

	report.note(fmt.Sprintf("读路径计数（进 connect 层的请求数）：对比 %d、单节点 %d、指标 %d、List %d",
		counter.count(heronv1connect.PublicServiceQueryProbeComparisonProcedure),
		counter.count(heronv1connect.PublicServiceQueryProbesProcedure),
		counter.count(heronv1connect.PublicServiceQueryMetricsProcedure),
		counter.count(heronv1connect.PublicServiceListProbeComparisonNodesProcedure)))
	runtime.ReadMemStats(&mem)
	report.note(fmt.Sprintf("服务进程内存（含生成后残留）：HeapAlloc=%d MiB Sys=%d MiB；生成结束时的 HeapAlloc=%d MiB；读路径期间峰值 HeapAlloc=%d MiB",
		mem.HeapAlloc>>20, mem.Sys>>20, baseHeap>>20, peakHeap.Load()>>20))
	report.write(t)
}

func ctxOf(t *testing.T) context.Context { return t.Context() }

func oneLine(s string) string { return strings.SplitN(s, ";", 2)[0] }

func firstN(n int) []int64 {
	ids := make([]int64, n)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	return ids
}

// ---- 数据生成 ----

func genLoadDatabase(t *testing.T, path string, now int64) {
	t.Helper()
	nodes, busy, tasks, _, _ := loadSizes()
	t.Logf("generating load database at %s (%d nodes, %d×%d series, retention %v)", path, nodes, busy, tasks, genRetention())
	st, err := store.Open(path, clock.NewFake(time.Unix(now, 0)), slog.Default(), store.MigrateSchema)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	nodeHash := func(i int) []byte { h := make([]byte, 32); h[0] = byte(i + 1); return h }
	for i := range nodes {
		if _, _, err := st.CreateNode(ctx, fmt.Sprintf("node%03d", i+1), store.Billing{}, nodeHash(i)); err != nil {
			t.Fatal(err)
		}
	}
	busyIDs := firstN(busy)
	for k := 1; k <= tasks; k++ {
		if _, _, err := st.SaveProbeTask(ctx, &heronv1.ProbeTask{
			Kind: heronv1.ProbeKind_PROBE_KIND_ICMP, Target: fmt.Sprintf("192.0.2.%d", k), IntervalS: 60, TimeoutMs: 1000,
		}, store.NodeSelector{NodeIDs: busyIDs}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(OFF)&_pragma=synchronous(OFF)&_pragma=cache_size(-262144)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := genRetention()
	begin := time.Now()
	var total int64
	for _, lv := range []struct {
		table string
		step  int64
		span  time.Duration
	}{
		{"probe_1m", 60, d.M1},
		{"probe_5m", 300, d.M5},
		{"probe_1h", 3600, d.H1},
	} {
		span := int64(lv.span.Seconds())
		from := now - span + lv.step
		stmt, err := db.Prepare(fmt.Sprintf(
			"WITH RECURSIVE m(ts) AS (SELECT ? UNION ALL SELECT ts+? FROM m WHERE ts+? < ?) "+
				"INSERT INTO %s SELECT ?, ts, ?, 1, 0, 0, 500, 500, 500 FROM m", lv.table))
		if err != nil {
			t.Fatal(err)
		}
		for _, node := range busyIDs {
			for task := 1; task <= tasks; task++ {
				if _, err := stmt.Exec(from, lv.step, lv.step, now, node, task); err != nil {
					t.Fatal(err)
				}
			}
		}
		stmt.Close()
		n := countRows(t, db, lv.table)
		total += n
		t.Logf("%s: %d rows（累计 %.1fM，%.0fs）", lv.table, n, float64(total)/1e6, time.Since(begin).Seconds())
	}
	// 指标行只填节点 1：① 的读者要有真实读数。reported 只在 1m 表上有意义。
	for _, lv := range []struct {
		table string
		step  int64
		span  time.Duration
	}{
		{"metric_1m", 60, d.M1},
		{"metric_5m", 300, d.M5},
		{"metric_1h", 3600, d.H1},
	} {
		span := int64(lv.span.Seconds())
		from := now - span + lv.step
		tail := "minutes, observed, both) SELECT 1, ts, 500, 1, 500, 1, NULL, NULL FROM m"
		if lv.table == "metric_1m" {
			tail = "reported, observed) SELECT 1, ts, 500, 1, 500, 1, NULL FROM m"
		}
		q := fmt.Sprintf("WITH RECURSIVE m(ts) AS (SELECT %d UNION ALL SELECT ts+%d FROM m WHERE ts+%d < %d) "+
			"INSERT INTO %s (node_id, ts, cpu_sum, cpu_n, cpu_max, "+tail,
			from, lv.step, lv.step, now, lv.table)
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, level := range []string{"probe_5m", "probe_1h", "5m", "1h"} {
		if _, err := db.Exec("UPDATE rollup_state SET upto_ts = ? WHERE level = ?", now, level); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("生成完成 %.0fs，库 %.1f GiB", time.Since(begin).Seconds(), float64(fi.Size())/(1<<30))
}

func genRetention() store.Retention {
	if smoke {
		return store.Retention{M1: 2 * time.Hour, M5: 6 * time.Hour, H1: 24 * time.Hour, AlertEvents: store.DefaultRetention.AlertEvents}
	}
	return store.DefaultRetention
}

func countRows(t *testing.T, db *sql.DB, table string) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// ---- 入口与读路径计数 ----

type countingServer struct {
	inner http.Handler
	mu    sync.Mutex
	hits  map[string]int
}

func (c *countingServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	if c.hits == nil {
		c.hits = map[string]int{}
	}
	c.hits[r.URL.Path]++
	c.mu.Unlock()
	c.inner.ServeHTTP(w, r)
}

func (c *countingServer) count(procedure string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits[procedure]
}

// sourcedClient 把来源地址写进 X-Forwarded-For：限流按来源分桶，不同来源互不挤占。
type sourcedClient struct {
	inner  heronv1connect.PublicServiceClient
	source string
}

func newSourcedClient(url, source string) *sourcedClient {
	return &sourcedClient{inner: heronv1connect.NewPublicServiceClient(http.DefaultClient, url), source: source}
}

func (s *sourcedClient) do[T any](ctx context.Context, req *connect.Request[T]) *connect.Request[T] {
	req.Header().Set("X-Forwarded-For", s.source)
	return req
}

func (s *sourcedClient) GetSnapshot(ctx context.Context, req *connect.Request[heronv1.PublicServiceGetSnapshotRequest]) (*connect.Response[heronv1.PublicSnapshot], error) {
	return s.inner.GetSnapshot(ctx, s.do(ctx, req))
}

func (s *sourcedClient) GetSite(ctx context.Context, req *connect.Request[heronv1.GetSiteRequest]) (*connect.Response[heronv1.PublicSite], error) {
	return s.inner.GetSite(ctx, s.do(ctx, req))
}

func (s *sourcedClient) QueryMetrics(ctx context.Context, req *connect.Request[heronv1.QueryMetricsRequest]) (*connect.Response[heronv1.QueryMetricsResponse], error) {
	return s.inner.QueryMetrics(ctx, s.do(ctx, req))
}

func (s *sourcedClient) QueryProbes(ctx context.Context, req *connect.Request[heronv1.QueryProbesRequest]) (*connect.Response[heronv1.QueryProbesResponse], error) {
	return s.inner.QueryProbes(ctx, s.do(ctx, req))
}

func (s *sourcedClient) QueryProbeComparison(ctx context.Context, req *connect.Request[heronv1.QueryProbeComparisonRequest]) (*connect.Response[heronv1.QueryProbeComparisonResponse], error) {
	return s.inner.QueryProbeComparison(ctx, s.do(ctx, req))
}

func (s *sourcedClient) ListProbeComparisonNodes(ctx context.Context, req *connect.Request[heronv1.ListProbeComparisonNodesRequest]) (*connect.Response[heronv1.ListProbeComparisonNodesResponse], error) {
	return s.inner.ListProbeComparisonNodes(ctx, s.do(ctx, req))
}

// ---- 负载组与记账 ----

type loadOutcome struct {
	series     int
	samples    int
	wantSeries int
}

func metricsOutcome(r *connect.Response[heronv1.QueryMetricsResponse]) *loadOutcome {
	if r == nil {
		return &loadOutcome{}
	}
	return &loadOutcome{series: len(r.Msg.GetSeries()), samples: len(r.Msg.GetTs())}
}

func seriesOutcome(r *connect.Response[heronv1.QueryProbesResponse], wantTasks int) *loadOutcome {
	if r == nil {
		return &loadOutcome{}
	}
	o := &loadOutcome{wantSeries: wantTasks, series: len(r.Msg.GetSeries())}
	for _, s := range r.Msg.GetSeries() {
		o.samples += len(s.GetSamples())
	}
	return o
}

func comparisonOutcome(r *connect.Response[heronv1.QueryProbeComparisonResponse], wantNodes int) *loadOutcome {
	if r == nil {
		return &loadOutcome{}
	}
	o := &loadOutcome{wantSeries: wantNodes, series: len(r.Msg.GetSeries())}
	for _, s := range r.Msg.GetSeries() {
		o.samples += len(s.GetSamples())
	}
	return o
}

type loadGroup struct {
	name                          string
	planned, issued, success      int
	limited, other, quotaRejected int
	latencies                     []time.Duration
	seriesOK                      bool
	err, lastQuota                string
}

// runLoadGroup 按限流补充速度间隔发起（每 100ms 一个）：计量组只测服务端耗时，
// 不让来源桶被自己打空——桶容量 60、每秒补 10，10/s 的节奏只动用补充量。
func runLoadGroup(name string, calls int, one func() (*loadOutcome, error)) *loadGroup {
	g := &loadGroup{name: name, planned: calls, seriesOK: true}
	for i := range calls {
		if i > 0 {
			time.Sleep(100 * time.Millisecond)
		}
		begin := time.Now()
		out, err := one()
		g.latencies = append(g.latencies, time.Since(begin))
		g.issued++
		switch {
		case err == nil:
			g.success++
			if out == nil || out.series == 0 || out.samples == 0 || (out.wantSeries != 0 && out.series != out.wantSeries) {
				g.seriesOK = false
			}
		case connect.CodeOf(err) == connect.CodeResourceExhausted:
			g.limited++
		case connect.CodeOf(err) == connect.CodeFailedPrecondition:
			g.quotaRejected++
			g.lastQuota = err.Error()
		default:
			g.other++
			g.err = err.Error()
		}
	}
	return g
}

func (g *loadGroup) p50() time.Duration { return quantile(g.latencies, 0.50) }
func (g *loadGroup) p99() time.Duration { return quantile(g.latencies, 0.99) }

func quantile(ds []time.Duration, q float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[min(len(s)-1, int(q*float64(len(s))))]
}

// ---- 饱和组 ----

type saturationResult struct {
	hammer, reader1, reader2, burst *loadGroup
	readerBaseline                  time.Duration
	verified                        bool
}

func runSaturation(t *testing.T, srvURL string, hammer *sourcedClient, now time.Time, hammerSec, tasks int) *saturationResult {
	t.Helper()
	res := &saturationResult{}
	plain := newSourcedClient(srvURL, probeSource)
	ids := firstN(store.MaxComparisonNodes)
	req := func() *connect.Request[heronv1.QueryProbeComparisonRequest] {
		return connect.NewRequest(&heronv1.QueryProbeComparisonRequest{
			TaskId: 1, NodeIds: ids, From: now.Unix() - 365*86400, To: now.Unix(), MaxPoints: 720})
	}
	base := runLoadGroup("④ 读者基线（无负载时 ① 形状）", 10, func() (*loadOutcome, error) {
		r, err := plain.QueryMetrics(t.Context(), connect.NewRequest(&heronv1.QueryMetricsRequest{NodeId: 1, From: now.Unix() - 6*3600, To: now.Unix(), MaxPoints: 720}))
		return metricsOutcome(r), err
	})
	res.readerBaseline = base.p99()

	stop := time.Now().Add(time.Duration(hammerSec) * time.Second)
	hg := &loadGroup{name: fmt.Sprintf("④ 饱和来源（开环 365d 分块，%ds）", hammerSec), planned: 60 + 10*hammerSec}
	r1 := &loadGroup{name: "④ 受保护读者·指标 6h", planned: hammerSec, seriesOK: true}
	r2 := &loadGroup{name: "④ 受保护读者·单节点 6h", planned: hammerSec, seriesOK: true}
	var wg sync.WaitGroup
	var mu sync.Mutex
	record := func(g *loadGroup, begin time.Time, err error) {
		mu.Lock()
		defer mu.Unlock()
		g.issued++
		g.latencies = append(g.latencies, time.Since(begin))
		switch {
		case err == nil:
			g.success++
		case connect.CodeOf(err) == connect.CodeResourceExhausted:
			g.limited++
		default:
			g.other++
			g.err = err.Error()
		}
	}
	fire := func(at time.Time, fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if d := time.Until(at); d > 0 {
				time.Sleep(d)
			}
			if time.Now().After(stop) {
				return // 截止后不再发起；planned 与 issued 的差就是未完成。
			}
			fn()
		}()
	}
	// 突发 60：立刻发满桶，之后每 100ms 一个，按计划时刻发出、不等返回。
	for range 60 {
		fire(time.Now(), func() {
			begin := time.Now()
			_, err := hammer.QueryProbeComparison(context.Background(), req())
			record(hg, begin, err)
		})
	}
	for i := range 10 * hammerSec {
		at := time.Now().Add(time.Duration(i+1) * 100 * time.Millisecond)
		fire(at, func() {
			begin := time.Now()
			_, err := hammer.QueryProbeComparison(context.Background(), req())
			record(hg, begin, err)
		})
	}
	// 受保护的两个读者：另一来源，每秒各一次。
	for i := range hammerSec {
		at := time.Now().Add(time.Duration(i) * time.Second)
		fire(at, func() {
			begin := time.Now()
			r, err := plain.QueryMetrics(context.Background(), connect.NewRequest(&heronv1.QueryMetricsRequest{NodeId: 1, From: now.Unix() - 6*3600, To: now.Unix(), MaxPoints: 720}))
			if o := metricsOutcome(r); err == nil && !o.seriesOK() {
				mu.Lock()
				r1.seriesOK = false
				mu.Unlock()
			}
			record(r1, begin, err)
		})
		fire(at, func() {
			begin := time.Now()
			r, err := plain.QueryProbes(context.Background(), connect.NewRequest(&heronv1.QueryProbesRequest{NodeId: 1, From: now.Unix() - 6*3600, To: now.Unix(), MaxPoints: 720}))
			if o := seriesOutcome(r, tasks); err == nil && (!o.seriesOK() || o.series != tasks) {
				mu.Lock()
				r2.seriesOK = false
				mu.Unlock()
			}
			record(r2, begin, err)
		})
	}
	wg.Wait()
	res.hammer, res.reader1, res.reader2 = hg, r1, r2

	// 突发验证：一个新来源连发 80 个 List（轻请求），容量 60 之外必须看到限流。
	b := &loadGroup{name: "④ 限流验证（第三来源 80 连发）", planned: 80, seriesOK: false}
	for range 80 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := connect.NewRequest(&heronv1.ListProbeComparisonNodesRequest{TaskId: 1})
			req.Header().Set("X-Forwarded-For", burstSource)
			begin := time.Now()
			_, err := plain.ListProbeComparisonNodes(context.Background(), req)
			record(b, begin, err)
		}()
	}
	wg.Wait()
	res.burst = b
	res.verified = b.limited > 0 && r1.success == hammerSec && r2.success == hammerSec &&
		r1.p99() <= 2*max(res.readerBaseline, time.Millisecond) && r2.p99() <= 2*max(res.readerBaseline, time.Millisecond)
	return res
}

func (o *loadOutcome) seriesOK() bool { return o != nil && o.series > 0 && o.samples > 0 }

func (g *loadGroup) incompleteOf() int { return g.planned - g.issued }

func (s *saturationResult) verdict() string {
	var b strings.Builder
	fmt.Fprintf(&b, "④：饱和来源发起 %d（成功 %d、限流 %d、其他 %d、未完成 %d）；", s.hammer.issued, s.hammer.success, s.hammer.limited, s.hammer.other, s.hammer.incompleteOf())
	fmt.Fprintf(&b, "读者基线 p99=%v，打满期间两个读者 p99=%v / %v（门槛 ≤ 2× 基线）；", s.readerBaseline, s.reader1.p99(), s.reader2.p99())
	fmt.Fprintf(&b, "突发验证被限流 %d 个；整体判定 %v", s.burst.limited, s.verified)
	return b.String()
}

// ---- 报告 ----

type loadReport struct {
	path string
	rows []string
}

func newLoadReport(path string) *loadReport {
	cpu := ""
	if out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output(); err == nil {
		cpu = strings.TrimSpace(string(out))
	}
	host := "?"
	if h, err := os.Hostname(); err == nil {
		host = h
	}
	return &loadReport{path: path, rows: []string{
		fmt.Sprintf("机型：%s %s/%s CPU=%d 核 %q", host, runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), cpu),
	}}
}

func (r *loadReport) group(g *loadGroup) {
	r.rows = append(r.rows, fmt.Sprintf("%s：计划 %d、发起 %d、成功 %d、限流 %d、其他 %d、超额拒 %d；p50=%v p99=%v；序列核对 %v",
		g.name, g.planned, g.issued, g.success, g.limited, g.other, g.quotaRejected, g.p50(), g.p99(), g.seriesOK))
}

func (r *loadReport) note(s string) {
	r.rows = append(r.rows, s)
}

func (r *loadReport) write(t *testing.T) {
	t.Helper()
	if fi, err := os.Stat(r.path); err == nil {
		r.rows = append(r.rows, fmt.Sprintf("库大小：%.1f GiB", float64(fi.Size())/(1<<30)))
	}
	t.Logf("§2.4 成本验收报告\n%s", strings.Join(r.rows, "\n"))
	if err := os.WriteFile(filepath.Join(filepath.Dir(r.path), "cost-report.txt"), []byte(strings.Join(r.rows, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
