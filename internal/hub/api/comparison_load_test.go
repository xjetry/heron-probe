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
	"strconv"
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

	hammerSource        = "198.51.100.1"
	hammerProbesSource  = "198.51.100.5"
	hammerMetricsSource = "198.51.100.6"
	readerSource        = "198.51.100.2"
	burstSource         = "198.51.100.3"
	probeSource         = "198.51.100.4" // ④ 受保护读者专用：与前面几组的桶无关。
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

	// ④ 饱和：一个来源按限流满速开环打 30 秒重请求；受保护的读者走另一来源。读者基线
	// 用各自 ① 的空载 p99（指标读者对指标基线、探测读者对探测基线），门槛各 ≤2×。
	fds := startFDSampler(path)
	sat := runEntrySaturation(t, srv.URL, hammer, now, hammerSec, tasks,
		fmt.Sprintf("④ 饱和来源（开环 365d %d 节点分块）", store.MaxComparisonNodes),
		func() (*loadOutcome, error) {
			r, err := hammer.QueryProbeComparison(ctxOf(t), connect.NewRequest(&heronv1.QueryProbeComparisonRequest{
				TaskId: 1, NodeIds: firstN(store.MaxComparisonNodes), From: now.Unix() - 365*day, To: now.Unix(), MaxPoints: 720}))
			return comparisonOutcome(r, store.MaxComparisonNodes), err
		}, g1m.p99(), g1p.p99())
	report.group(sat.hammer)
	report.group(sat.reader1)
	report.group(sat.reader2)
	report.group(sat.burst)
	report.note(sat.verdict())
	report.note("④ 期间连接采样：" + fds.stopAndReport())

	// ④-p / ④-m：现有入口的同型饱和——同一来源与节奏，分别打单节点 QueryProbes 365d
	// 与单节点 QueryMetrics 365d，对照"分块打满"与"现有重查询打满"对读者的不同影响。
	hammerP := newSourcedClient(srv.URL, hammerProbesSource)
	fds2 := startFDSampler(path)
	satP := runEntrySaturation(t, srv.URL, hammerP, now, hammerSec, tasks,
		"④-p 饱和来源（开环单节点 QueryProbes 365d）",
		func() (*loadOutcome, error) {
			r, err := hammerP.QueryProbes(ctxOf(t), connect.NewRequest(&heronv1.QueryProbesRequest{
				NodeId: 1, From: now.Unix() - 365*day, To: now.Unix(), MaxPoints: 720}))
			return seriesOutcome(r, tasks), err
		}, g1m.p99(), g1p.p99())
	report.group(satP.hammer)
	report.note(satP.verdict())
	report.note("④-p 期间连接采样：" + fds2.stopAndReport())

	hammerM := newSourcedClient(srv.URL, hammerMetricsSource)
	fds3 := startFDSampler(path)
	satM := runEntrySaturation(t, srv.URL, hammerM, now, hammerSec, tasks,
		"④-m 饱和来源（开环单节点 QueryMetrics 365d）",
		func() (*loadOutcome, error) {
			r, err := hammerM.QueryMetrics(ctxOf(t), connect.NewRequest(&heronv1.QueryMetricsRequest{
				NodeId: 1, From: now.Unix() - 365*day, To: now.Unix(), MaxPoints: 720}))
			return metricsOutcome(r), err
		}, g1m.p99(), g1p.p99())
	report.group(satM.hammer)
	report.note(satM.verdict())
	report.note("④-m 期间连接采样：" + fds3.stopAndReport())

	// ⑦ 闭环干扰曲线：恰好 k 个重请求常驻在飞 30 秒（重来源轮换，绕开单来源限流对
	// 并发数的钳制），读者仍每秒一个 ① 形状。k=0 是曲线内的空载对照。
	for _, kind := range []struct {
		label string
		fire  func(c *sourcedClient) (*loadOutcome, error)
		ks    []int
	}{{"365d 8 节点分块", func(c *sourcedClient) (*loadOutcome, error) {
		r, err := c.QueryProbeComparison(ctxOf(t), connect.NewRequest(&heronv1.QueryProbeComparisonRequest{
			TaskId: 1, NodeIds: firstN(store.MaxComparisonNodes), From: now.Unix() - 365*day, To: now.Unix(), MaxPoints: 720}))
		return comparisonOutcome(r, store.MaxComparisonNodes), err
	}, []int{0, 1, 2, 4, 8}}, {"单节点 QueryProbes 365d", func(c *sourcedClient) (*loadOutcome, error) {
		r, err := c.QueryProbes(ctxOf(t), connect.NewRequest(&heronv1.QueryProbesRequest{
			NodeId: 1, From: now.Unix() - 365*day, To: now.Unix(), MaxPoints: 720}))
		return seriesOutcome(r, tasks), err
	}, []int{1, 2}}} {
		for _, k := range kind.ks {
			f := startFDSampler(path)
			line := runClosedLoop(t, srv.URL, now, tasks, k, curveDur(), kind.label, kind.fire, g1m.p99(), g1p.p99())
			report.note(line + "；连接采样：" + f.stopAndReport())
		}
	}

	// ⑧ 直连读路径的并发吞吐：k 个 worker 闭环打 365d 分块（与入口同一条读路径与参数，
	// 预热连接），回答"读栈到底能并行多少"。
	for _, line := range runDirectSweep(t, st, now, []int{1, 2, 4, 8, 16, 32}, directDur()) {
		report.note(line)
	}

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
	// 核对只统计拿到成功响应、且响应体可核对的请求；被限流/被拒/未完成的请求不算失败。
	seriesChecked, seriesFailed int
	incomplete                  int // 计划时刻已到、goroutine 没能在窗口内发出的数量
	err, lastQuota              string
}

// verifiedLine 区分"没核对"与"核对失败"：前者只是没有可核对的响应，后者是正确性问题。
func (g *loadGroup) verifiedLine() string {
	if g.seriesChecked == 0 {
		return "无成功响应可核对"
	}
	if g.seriesFailed == 0 {
		return fmt.Sprintf("核对 %d 个全过", g.seriesChecked)
	}
	return fmt.Sprintf("核对 %d 个、失败 %d 个", g.seriesChecked, g.seriesFailed)
}

// runLoadGroup 按限流补充速度间隔发起（每 100ms 一个）：计量组只测服务端耗时，
// 不让来源桶被自己打空——桶容量 60、每秒补 10，10/s 的节奏只动用补充量。
func runLoadGroup(name string, calls int, one func() (*loadOutcome, error)) *loadGroup {
	g := &loadGroup{name: name, planned: calls}
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
			if bad := out != nil && (out.series == 0 || out.samples == 0 || (out.wantSeries != 0 && out.series != out.wantSeries)); out != nil {
				g.seriesChecked++
				if bad {
					g.seriesFailed++
				}
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

const day = int64(86400)

// ---- 饱和组 ----

// saturationResult 是一轮开环饱和（一个饱和来源 + 两个受保护读者 + 限流验证）的结果。
type saturationResult struct {
	hammer  *loadGroup
	reader1 *loadGroup
	reader2 *loadGroup
	burst   *loadGroup
	// 每个读者用自己的 ① 空载 p99 做基线；门槛是各自的 2 倍。
	baseMetrics, baseProbes time.Duration
}

// runEntrySaturation 跑一轮开环饱和：饱和来源按限流满速（突发 60 后每 100ms 一个）发 heavy
// 请求 hammerSec 秒；两个受保护读者走独立来源，每秒一个 ① 形状请求。heavy 由调用方给出，
// 便于对比分块与现有入口（单节点查询）打满时的读者劣化。
func runEntrySaturation(t *testing.T, srvURL string, hammerClient *sourcedClient, now time.Time, hammerSec, tasks int,
	name string, heavy func() (*loadOutcome, error), baseMetrics, baseProbes time.Duration) *saturationResult {
	t.Helper()
	res := &saturationResult{baseMetrics: baseMetrics, baseProbes: baseProbes}
	plain := newSourcedClient(srvURL, probeSource)
	fire := func(at time.Time, fn func()) {
		time.Sleep(time.Until(at))
		fn()
	}
	burst := newSourcedClient(srvURL, burstSource)
	stop := time.Now().Add(time.Duration(hammerSec) * time.Second)
	hg := &loadGroup{name: name, planned: 60 + 10*hammerSec}
	r1 := &loadGroup{name: "④ 受保护读者·指标 6h", planned: hammerSec}
	r2 := &loadGroup{name: "④ 受保护读者·单节点 6h", planned: hammerSec}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 60 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			begin := time.Now()
			out, err := heavy()
			mu.Lock()
			defer mu.Unlock()
			hg.latencies = append(hg.latencies, time.Since(begin))
			hg.issued++
			if err == nil {
				hg.success++
				if out != nil {
					hg.seriesChecked++
					if out.series == 0 || out.samples == 0 || (out.wantSeries != 0 && out.series != out.wantSeries) {
						hg.seriesFailed++
					}
				}
			} else if connect.CodeOf(err) == connect.CodeResourceExhausted {
				hg.limited++
			} else {
				hg.other++
				hg.err = err.Error()
			}
		}()
	}
	_ = burst
	for i := range 10 * hammerSec {
		at := time.Now().Add(time.Duration(i+1) * 100 * time.Millisecond)
		wg.Add(1)
		go func() {
			defer wg.Done()
			fire(at, func() {
				begin := time.Now()
				out, err := heavy()
				mu.Lock()
				defer mu.Unlock()
				hg.latencies = append(hg.latencies, time.Since(begin))
				hg.issued++
				if err == nil {
					hg.success++
					if out != nil {
						hg.seriesChecked++
						if out.series == 0 || out.samples == 0 || (out.wantSeries != 0 && out.series != out.wantSeries) {
							hg.seriesFailed++
						}
					}
				} else if connect.CodeOf(err) == connect.CodeResourceExhausted {
					hg.limited++
				} else {
					hg.other++
					hg.err = err.Error()
				}
			})
		}()
	}
	for i := range hammerSec {
		at := time.Now().Add(time.Duration(i) * time.Second)
		wg.Add(1)
		go func() {
			defer wg.Done()
			fire(at, func() {
				begin := time.Now()
				r, err := plain.QueryMetrics(ctxOf(t), connect.NewRequest(&heronv1.QueryMetricsRequest{NodeId: 1, From: now.Unix() - 6*3600, To: now.Unix(), MaxPoints: 720}))
				mu.Lock()
				defer mu.Unlock()
				r1.issued++
				if err != nil {
					r1.other++
					r1.err = err.Error()
					return
				}
				out := metricsOutcome(r)
				r1.latencies = append(r1.latencies, time.Since(begin))
				r1.success++
				r1.seriesChecked++
				if out.series == 0 {
					r1.seriesFailed++
				}
			})
		}()
	}
	for i := range hammerSec {
		at := time.Now().Add(time.Duration(i)*time.Second + 500*time.Millisecond)
		wg.Add(1)
		go func() {
			defer wg.Done()
			fire(at, func() {
				begin := time.Now()
				r, err := plain.QueryProbes(ctxOf(t), connect.NewRequest(&heronv1.QueryProbesRequest{NodeId: 1, From: now.Unix() - 6*3600, To: now.Unix(), MaxPoints: 720}))
				mu.Lock()
				defer mu.Unlock()
				r2.issued++
				if err != nil {
					r2.other++
					r2.err = err.Error()
					return
				}
				out := seriesOutcome(r, tasks)
				r2.latencies = append(r2.latencies, time.Since(begin))
				r2.success++
				r2.seriesChecked++
				if out.series != tasks {
					r2.seriesFailed++
				}
			})
		}()
	}
	wg.Wait()
	// 未完成 = 按计划时刻已到、但调度 starvation 使 goroutine 没能在窗口内发出。
	hg.incomplete = hg.planned - hg.issued
	// 第三来源 80 连发验证限流确实在拒；成功的 List 响应核对候选数。
	b := &loadGroup{name: "④ 限流验证（第三来源 80 连发）", planned: 80}
	var bwg sync.WaitGroup
	for range 80 {
		bwg.Add(1)
		go func() {
			defer bwg.Done()
			begin := time.Now()
			r, err := burst.ListProbeComparisonNodes(ctxOf(t), connect.NewRequest(&heronv1.ListProbeComparisonNodesRequest{TaskId: 1}))
			mu.Lock()
			defer mu.Unlock()
			b.issued++
			b.latencies = append(b.latencies, time.Since(begin))
			if err == nil {
				b.success++
				if r.Msg.GetMaxNodesPerQuery() == 0 || len(r.Msg.GetNodeIds()) == 0 {
					b.seriesChecked++
					b.seriesFailed++
				}
			} else if connect.CodeOf(err) == connect.CodeResourceExhausted {
				b.limited++
			} else {
				b.other++
				b.err = err.Error()
			}
		}()
	}
	bwg.Wait()
	b.incomplete = b.planned - b.issued
	res.hammer, res.reader1, res.reader2, res.burst = hg, r1, r2, b
	_ = stop
	return res
}

func (s *saturationResult) ratios() (float64, float64) {
	m, p := 0.0, 0.0
	if s.baseMetrics > 0 {
		m = float64(s.reader1.p99()) / float64(s.baseMetrics)
	}
	if s.baseProbes > 0 {
		p = float64(s.reader2.p99()) / float64(s.baseProbes)
	}
	return m, p
}

func (s *saturationResult) verdict() string {
	rm, rp := s.ratios()
	ok := rm <= 2 && rp <= 2
	return fmt.Sprintf("④：饱和来源 %s 发起 %d（成功 %d、限流 %d、其他 %d、未完成 %d、%s）；"+
		"读者指标 p99=%v（基线 %v，%.2f×）、读者单节点 p99=%v（基线 %v，%.2f×），门槛各 ≤2×：%v；突发验证被限流 %d 个",
		s.hammer.name, s.hammer.issued, s.hammer.success, s.hammer.limited, s.hammer.other, s.hammer.incomplete, s.hammer.verifiedLine(),
		s.reader1.p99(), s.baseMetrics, rm, s.reader2.p99(), s.baseProbes, rp, ok, s.burst.limited)
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
		g.name, g.planned, g.issued, g.success, g.limited, g.other, g.quotaRejected, g.p50(), g.p99(), g.verifiedLine()))
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

// ---- 连接采样与闭环曲线 ----

func ctxOf(t *testing.T) context.Context { return t.Context() }

// curveDur / directDur：闭环每档与直连每档的时长；smoke 缩到 1 秒。
func curveDur() time.Duration {
	if smoke {
		return time.Second
	}
	return 30 * time.Second
}

func directDur() time.Duration {
	if smoke {
		return time.Second
	}
	return 10 * time.Second
}

// startFDSampler 周期性用 lsof 数进程打开的数据库文件句柄（db / -wal / -shm 后缀归类）。
// -wal 与 -shm 的每个连接各持一个，是读池 OpenConnections 的外部观测；InUse 与
// WaitCount 需要暴露 db.Stats 才可得，本轮不改产品代码，以句柄峰值代之。启动先记空载基线。
type fdSampler struct {
	stop    chan struct{}
	done    chan struct{}
	path    string
	baseDB  int
	peakDB  int
	peakWal int
	peakShm int
	peakCon int // 同刻打开连接数估计（取 -wal/-shm 的较大者）
	failed  bool
	mu      sync.Mutex
}

func startFDSampler(dbPath string) *fdSampler {
	s := &fdSampler{stop: make(chan struct{}), done: make(chan struct{}), path: dbPath}
	// lsof 报的是解析后的路径（macOS 的 /var → /private/var 一类），两边先归一。
	if resolved, err := filepath.EvalSymlinks(dbPath); err == nil {
		s.path = resolved
	}
	c := s.countOnce()
	s.mu.Lock()
	s.baseDB = c.db
	s.peakDB, s.peakWal, s.peakShm, s.peakCon = c.db, c.wal, c.shm, max(c.wal, c.shm)
	s.mu.Unlock()
	go func() {
		defer close(s.done)
		for {
			select {
			case <-s.stop:
				return
			case <-time.After(500 * time.Millisecond):
				s.sample()
			}
		}
	}()
	return s
}

func (s *fdSampler) countOnce() (c struct{ db, wal, shm int }) {
	out, err := exec.Command("lsof", "-p", strconv.Itoa(os.Getpid()), "-F", "n").Output()
	if err != nil {
		s.failed = true
		return
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		name, ok := strings.CutPrefix(line, "n")
		if !ok || !strings.HasPrefix(name, s.path) {
			continue
		}
		switch strings.TrimPrefix(name, s.path) {
		case "":
			c.db++
		case "-wal":
			c.wal++
		case "-shm":
			c.shm++
		}
	}
	return
}

func (s *fdSampler) sample() {
	c := s.countOnce()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peakDB = max(s.peakDB, c.db)
	s.peakWal = max(s.peakWal, c.wal)
	s.peakShm = max(s.peakShm, c.shm)
	s.peakCon = max(s.peakCon, max(c.wal, c.shm))
}

func (s *fdSampler) stopAndReport() string {
	close(s.stop)
	<-s.done
	s.sample()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return "句柄采样不可用（lsof 失败）"
	}
	return fmt.Sprintf("句柄峰值 db=%d（空载 %d）、-wal=%d、-shm=%d → 打开连接峰值估计 %d；InUse/WaitCount 需暴露 db.Stats，本轮未改产品代码",
		s.peakDB, s.baseDB, s.peakWal, s.peakShm, s.peakCon)
}

// runClosedLoop 保持恰好 k 个重请求在飞 dur：worker 完成一个立刻发下一个。重来源
// 每个请求轮换一个 IP（8 个来源的突发与补充合计远超 k 档位的需求），把"恰好 k 个
// 在飞"与单来源限流解耦；读者每秒一个 ① 形状，走独立来源。
func runClosedLoop(t *testing.T, srvURL string, now time.Time, tasks, k int, dur time.Duration, label string,
	fire func(c *sourcedClient) (*loadOutcome, error), baseMetrics, baseProbes time.Duration) string {
	t.Helper()
	sources := make([]*sourcedClient, 8)
	for i := range sources {
		sources[i] = newSourcedClient(srvURL, fmt.Sprintf("203.0.113.%d", i+1))
	}
	var next atomic.Int64
	pick := func() *sourcedClient { return sources[int(next.Add(1))%len(sources)] }
	plain := newSourcedClient(srvURL, probeSource)

	var mu sync.Mutex
	var heavyLat []time.Duration
	var r1Lat, r2Lat []time.Duration
	var done, limited, other, checked, failed int
	stop := time.Now().Add(dur)
	var wg sync.WaitGroup
	for range k {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(stop) {
				begin := time.Now()
				out, err := fire(pick())
				d := time.Since(begin)
				mu.Lock()
				if err == nil {
					done++
					heavyLat = append(heavyLat, d)
					if out != nil {
						checked++
						if out.series == 0 || out.samples == 0 || (out.wantSeries != 0 && out.series != out.wantSeries) {
							failed++
						}
					}
				} else if connect.CodeOf(err) == connect.CodeResourceExhausted {
					limited++
				} else {
					other++
				}
				mu.Unlock()
				if err != nil && connect.CodeOf(err) == connect.CodeResourceExhausted {
					// 被拒的请求没在飞：退避后重试，保持 k 个“被准入”的在飞。
					time.Sleep(100 * time.Millisecond)
				}
			}
		}()
	}
	deadline := time.Now().Add(dur)
	for i := 0; ; i++ {
		at := time.Now().Add(time.Duration(i) * time.Second)
		if !at.Before(deadline) {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(time.Until(at))
			begin := time.Now()
			plain.QueryMetrics(ctxOf(t), connect.NewRequest(&heronv1.QueryMetricsRequest{NodeId: 1, From: now.Unix() - 6*3600, To: now.Unix(), MaxPoints: 720}))
			mu.Lock()
			r1Lat = append(r1Lat, time.Since(begin))
			mu.Unlock()
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(time.Until(at.Add(500 * time.Millisecond)))
			begin := time.Now()
			plain.QueryProbes(ctxOf(t), connect.NewRequest(&heronv1.QueryProbesRequest{NodeId: 1, From: now.Unix() - 6*3600, To: now.Unix(), MaxPoints: 720}))
			mu.Lock()
			r2Lat = append(r2Lat, time.Since(begin))
			mu.Unlock()
		}()
	}
	wg.Wait()
	_ = tasks
	rm, rp := ratioOf(r1Lat, baseMetrics), ratioOf(r2Lat, baseProbes)
	ver := "核对 " + strconv.Itoa(checked) + " 个"
	if failed > 0 {
		ver += "、失败 " + strconv.Itoa(failed) + " 个"
	} else {
		ver += "全过"
	}
	return fmt.Sprintf("⑦ 闭环 %s k=%d（%v）：重请求完成 %d（%.1f/s，p50=%v，限流 %d、其他 %d、%s）；"+
		"读者指标 p50=%v p99=%v（%.2f×）、读者单节点 p50=%v p99=%v（%.2f×）",
		label, k, dur.Round(time.Second), done, float64(done)/dur.Seconds(), quantile(heavyLat, 0.50), limited, other, ver,
		quantile(r1Lat, 0.50), quantile(r1Lat, 0.99), rm, quantile(r2Lat, 0.50), quantile(r2Lat, 0.99), rp)
}

func ratioOf(ds []time.Duration, base time.Duration) float64 {
	if base <= 0 || len(ds) == 0 {
		return 0
	}
	return float64(quantile(ds, 0.99)) / float64(base)
}

// runDirectSweep 回答读栈的并行度：k 个 worker 闭环直调 store（与入口同一条读路径、
// 同参数的 365d 分块），先各档预热连接，再数完成量。
func runDirectSweep(t *testing.T, st *store.Store, now time.Time, ks []int, dur time.Duration) []string {
	t.Helper()
	lv, step := store.ChooseLevel(now.Unix()-365*day, now.Unix(), 720)
	ids := firstN(store.MaxComparisonNodes)
	var out []string
	for _, k := range ks {
		// 预热：k 个并发轻查询先把读池的连接撑起来。
		var warm sync.WaitGroup
		for range k {
			warm.Add(1)
			go func() {
				defer warm.Done()
				_, _ = st.QueryProbeComparison(ctxOf(t), 1, ids[:1], now.Unix()-3600, now.Unix(), lv, step)
			}()
		}
		warm.Wait()
		var mu sync.Mutex
		var lat []time.Duration
		var done, errs int
		stop := time.Now().Add(dur)
		var wg sync.WaitGroup
		for range k {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for time.Now().Before(stop) {
					begin := time.Now()
					_, err := st.QueryProbeComparison(ctxOf(t), 1, ids, now.Unix()-365*day, now.Unix(), lv, step)
					d := time.Since(begin)
					mu.Lock()
					if err == nil {
						done++
						lat = append(lat, d)
					} else {
						errs++
					}
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		out = append(out, fmt.Sprintf("⑧ 直连读路径 k=%d（%v，365d %d 节点分块）：吞吐 %.1f/s，p50=%v，p99=%v，错误 %d",
			k, dur.Round(time.Second), store.MaxComparisonNodes, float64(done)/dur.Seconds(), quantile(lat, 0.50), quantile(lat, 0.99), errs))
	}
	return out
}
