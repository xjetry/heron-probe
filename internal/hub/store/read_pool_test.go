package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xjetry/heron-probe/internal/hub/metric"
	"github.com/xjetry/heron-probe/internal/testwait"
)

// bigScanTo 让从 0 起、1m 级的窗口的预计扫描量刚好超过 lightScanRows（单序列），这样的查询走历史池。
const bigScanTo = (lightScanRows + 1) * 60

// readPoolCase 是一个读池与一条经它取连接的生产读。
type readPoolCase struct {
	name  string
	db    func(*Store) *sql.DB
	limit int
	read  func(context.Context, *Store) error
}

func readPoolCases() []readPoolCase {
	return []readPoolCase{
		{"light", func(s *Store) *sql.DB { return s.r }, readPoolSize(), func(ctx context.Context, s *Store) error {
			_, err := s.Settings(ctx)
			return err
		}},
		{"history", func(s *Store) *sql.DB { return s.hr }, historyPoolSize(), func(ctx context.Context, s *Store) error {
			lv, _ := LevelByName("1m")
			_, err := s.QueryMetrics(ctx, 1, 0, bigScanTo, lv, 3600)
			return err
		}},
	}
}

// reopenReadPools 把两个读池换成经 driverName 打开的池（同一个库文件、同样的 DSN 与池设置），测试借此在读路径上挂钩子。
// 读按预计扫描量分池（readPoolFor），钩子只挂一个池会漏掉分到另一个池的查询，所以两个一起换。
func reopenReadPools(t *testing.T, s *Store, driverName string) {
	t.Helper()
	var seq int
	var name, path string
	if err := s.r.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct {
		db   **sql.DB
		size int
	}{{&s.r, readPoolSize()}, {&s.hr, historyPoolSize()}} {
		if err := (*p.db).Close(); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open(driverName, dsn(path, "&_pragma=query_only(1)"))
		if err != nil {
			t.Fatal(err)
		}
		configureReadPool(db, p.size)
		*p.db = db
	}
}

// holdReadConns 占住 db 里 n 个连接，返回归还它们的函数（幂等）。占住的方式与一条长读查询相同：
// 连接从池里取出、直到归还前不被别人用。
func holdReadConns(t *testing.T, db *sql.DB, n int) (release func()) {
	t.Helper()
	held := make([]*sql.Conn, 0, n)
	for range n {
		c, err := db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	return sync.OnceFunc(func() {
		for _, c := range held {
			c.Close()
		}
	})
}

// 池满时再来一条读：它必须排队（WaitCount 增长）而不是另开第 limit+1 个连接；连接归还后全部
// 留作空闲（MaxIdleClosed 不变），下一条读复用它们而不是关掉重开。两个读池各验一遍。
func TestReadPoolQueuesAtLimitAndKeepsReturnedConnections(t *testing.T) {
	for _, pc := range readPoolCases() {
		t.Run(pc.name, func(t *testing.T) {
			s, _ := open(t)
			ctx := context.Background()
			db, limit := pc.db(s), pc.limit
			before := db.Stats()
			release := holdReadConns(t, db, limit)
			defer release()

			done := make(chan error, 1)
			go func() { done <- pc.read(ctx, s) }()
			var finished bool
			var readErr error
			testwait.Until(t, time.Millisecond, func() bool {
				select {
				case readErr = <-done:
					finished = true
					return true
				default:
				}
				return db.Stats().WaitCount > before.WaitCount
			}, "read beyond the %s pool limit neither queued nor finished", pc.name)
			queued := db.Stats()
			if queued.OpenConnections > limit {
				t.Errorf("open %s connections = %d with %d held and one more reader, want at most %d", pc.name, queued.OpenConnections, limit, limit)
			}
			if queued.WaitCount == before.WaitCount {
				t.Errorf("read beyond the %s pool limit of %d was admitted without queueing (finished=%v)", pc.name, limit, finished)
			}

			release()
			if !finished {
				select {
				case readErr = <-done:
				case <-time.After(testwait.Bound):
					t.Fatal("queued read never got a connection after the holders returned theirs")
				}
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
			after := db.Stats()
			if d := after.MaxIdleClosed - before.MaxIdleClosed; d != 0 {
				t.Errorf("returning %d %s connections closed %d of them, want all kept idle for reuse", limit, pc.name, d)
			}
			if after.Idle != after.OpenConnections || after.OpenConnections > limit {
				t.Errorf("after the burst %s open = %d idle = %d, want every open connection idle and at most %d", pc.name, after.OpenConnections, after.Idle, limit)
			}
		})
	}
}

// 真实读方法、并发数是较大上限的四倍：两个池的连接数始终不超过各自上限，归还的连接被复用而不是关掉重开。
func TestReadPoolReusesConnectionsUnderConcurrentReads(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	cases := readPoolCases()
	id, _, err := s.CreateNode(ctx, "a", Billing{}, hash(1))
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]metric.Row, 0, 60)
	for i := range int64(60) {
		rows = append(rows, metric.Row{NodeID: id, TS: 600 + i*60, CoverageStart: 600, Bucket: bucket(float64(i))})
	}
	if rejected, err := s.WriteMinuteBatch(ctx, metric.Batch{Rows: rows}); err != nil || rejected != 0 {
		t.Fatalf("seed: rejected %d, err %v", rejected, err)
	}
	lv, _ := LevelByName("1m")
	before := make([]sql.DBStats, len(cases))
	for i, pc := range cases {
		before[i] = pc.db(s).Stats()
	}

	peaks := make([]atomic.Int64, len(cases))
	stop := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		for {
			for i, pc := range cases {
				if n := int64(pc.db(s).Stats().OpenConnections); n > peaks[i].Load() {
					peaks[i].Store(n)
				}
			}
			select {
			case <-stop:
				return
			default:
				runtime.Gosched()
			}
		}
	}()
	readers := 4 * max(readPoolSize(), historyPoolSize())
	var wg sync.WaitGroup
	errs := make(chan error, readers)
	for range readers {
		wg.Go(func() {
			for range 3 {
				if _, err := s.ListNodes(ctx); err != nil {
					errs <- err
					return
				}
				if _, err := s.Settings(ctx); err != nil {
					errs <- err
					return
				}
				if _, err := s.QueryMetrics(ctx, id, 0, 4800, lv, 60); err != nil {
					errs <- err
					return
				}
				if _, err := s.QueryMetrics(ctx, id, 0, bigScanTo, lv, 3600); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	wg.Wait()
	close(stop)
	<-sampled
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for i, pc := range cases {
		after := pc.db(s).Stats()
		if got := peaks[i].Load(); got > int64(pc.limit) {
			t.Errorf("peak open %s connections = %d, want at most %d", pc.name, got, pc.limit)
		}
		if d := after.MaxIdleClosed - before[i].MaxIdleClosed; d != 0 {
			t.Errorf("%d %s connections were closed on return and reopened, want 0", d, pc.name)
		}
		if d := after.MaxIdleTimeClosed - before[i].MaxIdleTimeClosed; d != 0 {
			t.Errorf("%d %s connections were reclaimed as idle during a continuous burst, want 0", d, pc.name)
		}
	}
}

// 面板每 POLL_MS（2 秒）拉一次；一次轮询用过、归还的连接，到下一次轮询时必须还在池里。等 3 秒：
// 一个轮询间隔，加上 database/sql 回收协程至少 1 秒一轮的扫描粒度。两个池同时验，共用这一次等待。
func TestReadPoolKeepsIdleConnectionsAcrossPolls(t *testing.T) {
	s, _ := open(t)
	cases := readPoolCases()
	before := make([]sql.DBStats, len(cases))
	for i, pc := range cases {
		holdReadConns(t, pc.db(s), pc.limit)()
		before[i] = pc.db(s).Stats()
		if before[i].Idle != pc.limit {
			t.Fatalf("idle %s connections after returning %d = %d", pc.name, pc.limit, before[i].Idle)
		}
	}
	time.Sleep(3 * time.Second)
	for i, pc := range cases {
		after := pc.db(s).Stats()
		if d := after.MaxIdleTimeClosed - before[i].MaxIdleTimeClosed; d != 0 || after.Idle != pc.limit {
			t.Errorf("after one poll interval: %d idle %s connections reclaimed, %d left, want 0 reclaimed and %d left", d, pc.name, after.Idle, pc.limit)
		}
	}
}

// 两个池互不占用：历史池被占满时（占住的连接在池看来与在飞的大扫描相同），轻读（含历史请求的准入读与
// 对比候选读）照常拿到连接、哪个池都不排队；轻池被占满时，经 queryFamily 的四个历史入口以超过
// lightScanRows 的窗口照常执行、哪个池都不排队。任何一条读走错了池，就会在被占满的那个池上排队到超时。
func TestReadPoolsIsolateHistoryScansFromLightReads(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	id, _, err := s.CreateNode(ctx, "a", Billing{}, hash(1))
	if err != nil {
		t.Fatal(err)
	}
	lv, _ := LevelByName("1m")
	type namedRead struct {
		name string
		fn   func(context.Context) error
	}
	lightReads := []namedRead{
		{"Settings", func(ctx context.Context) error { _, err := s.Settings(ctx); return err }},
		{"ListNodes", func(ctx context.Context) error { _, err := s.ListNodes(ctx); return err }},
		{"NodeExists", func(ctx context.Context) error { _, err := s.NodeExists(ctx, id); return err }},
		{"NodeIsPublic", func(ctx context.Context) error { _, err := s.NodeIsPublic(ctx, id); return err }},
		{"ListComparisonNodes", func(ctx context.Context) error {
			_, _, _, err := s.ListComparisonNodes(ctx, 1, ComparisonPublic)
			return err
		}},
	}
	historyReads := []namedRead{
		{"QueryMetrics", func(ctx context.Context) error { _, err := s.QueryMetrics(ctx, id, 0, bigScanTo, lv, 3600); return err }},
		{"QueryProbes", func(ctx context.Context) error { _, err := s.QueryProbes(ctx, id, 0, bigScanTo, lv, 3600); return err }},
		{"QueryMetricsCoverage", func(ctx context.Context) error {
			_, _, err := s.QueryMetricsCoverage(ctx, id, 0, bigScanTo, lv, 3600)
			return err
		}},
		{"QueryProbeComparison", func(ctx context.Context) error {
			_, err := s.QueryProbeComparison(ctx, 1, []int64{id}, 0, bigScanTo, lv, 3600)
			return err
		}},
	}
	for _, tc := range []struct {
		name  string
		full  *sql.DB
		limit int
		reads []namedRead
	}{
		{"history pool full, light reads", s.hr, historyPoolSize(), lightReads},
		{"light pool full, history reads", s.r, readPoolSize(), historyReads},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := holdReadConns(t, tc.full, tc.limit)
			defer release()
			for _, rd := range tc.reads {
				lightBefore, historyBefore := s.r.Stats().WaitCount, s.hr.Stats().WaitCount
				rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				err := rd.fn(rctx)
				cancel()
				lightWaits, historyWaits := s.r.Stats().WaitCount-lightBefore, s.hr.Stats().WaitCount-historyBefore
				if err != nil || lightWaits != 0 || historyWaits != 0 {
					t.Errorf("%s with the other pool full: err=%v, queued %d times on the light pool and %d on the history pool; want no error and no queueing",
						rd.name, err, lightWaits, historyWaits)
				}
			}
		})
	}
}

// 告警评估读的形状：1m 级、ForMinutes（至多 60，alert/rule.go 校验）分钟窗口，探测族按每节点任务上限
// 计序列。它的预计扫描量在 lightScanRows 之内，所以历史池被大扫描占满时它不排队——告警判定的时效
// 不随公开端的历史负载变化。
func TestReadPoolRoutesAlertShapedReadsToLightPool(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	id, _, err := s.CreateNode(ctx, "a", Billing{}, hash(1))
	if err != nil {
		t.Fatal(err)
	}
	lv, _ := LevelByName("1m")
	const minuteTS = 7200
	from, to := int64(minuteTS-59*60), int64(minuteTS+60)
	release := holdReadConns(t, s.hr, historyPoolSize())
	defer release()
	for _, rd := range []struct {
		name string
		fn   func(context.Context) error
	}{
		{"QueryMetrics", func(ctx context.Context) error { _, err := s.QueryMetrics(ctx, id, from, to, lv, 60); return err }},
		{"QueryProbes", func(ctx context.Context) error { _, err := s.QueryProbes(ctx, id, from, to, lv, 60); return err }},
	} {
		lightBefore, historyBefore := s.r.Stats().WaitCount, s.hr.Stats().WaitCount
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := rd.fn(rctx)
		cancel()
		lightWaits, historyWaits := s.r.Stats().WaitCount-lightBefore, s.hr.Stats().WaitCount-historyBefore
		if err != nil || lightWaits != 0 || historyWaits != 0 {
			t.Errorf("alert-shaped %s (1m, 60-minute window) with the history pool full: err=%v, queued %d times on the light pool and %d on the history pool; want no error and no queueing",
				rd.name, err, lightWaits, historyWaits)
		}
	}
}

// 一次扫描的耗时随预计扫描量怎样变化，给 lightScanRows 的取值作依据。默认跳过；跑法：
//
//	HERON_POOL_BENCH=1 go test ./internal/hub/store/ -run TestReadPoolScanCost -v -count=1
//
// 单序列指标、1m 级，每档窗口串行查 scanCostRuns 次（无并发、无排队），步长取到不超过 720 点（面板默认点数）。
func TestReadPoolScanCost(t *testing.T) {
	if os.Getenv("HERON_POOL_BENCH") == "" {
		t.Skip("set HERON_POOL_BENCH=1 to run the scan cost measurement")
	}
	const (
		scanCostRows = 12000 // quotaRowsPerSeries：单序列一次至多读这么多行
		scanCostRuns = 200
		scanCostFrom = 86400
	)
	s, _ := open(t)
	ctx := context.Background()
	id, _, err := s.CreateNode(ctx, "a", Billing{}, hash(1))
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]metric.Row, 0, scanCostRows)
	for m := range int64(scanCostRows) {
		rows = append(rows, metric.Row{NodeID: id, TS: scanCostFrom + m*60, CoverageStart: scanCostFrom, Bucket: bucket(float64(m % 100))})
	}
	if rejected, err := s.WriteMinuteBatch(ctx, metric.Batch{Rows: rows}); err != nil || rejected != 0 {
		t.Fatalf("seed: rejected %d, err %v", rejected, err)
	}
	lv, _ := LevelByName("1m")
	var b strings.Builder
	b.WriteString("| rows scanned | step | p50 | p99 |\n|---|---|---|---|\n")
	for _, n := range []int64{60, 360, 1440, 2016, 3840, 4096, 8192, scanCostRows} {
		step := max(60, ceilDiv(ceilDiv(n*60, 720), 60)*60)
		var d []time.Duration
		for range scanCostRuns {
			start := time.Now()
			got, err := s.QueryMetrics(ctx, id, scanCostFrom, scanCostFrom+n*60, lv, step)
			if err != nil || len(got) == 0 {
				t.Fatalf("%d rows: %d points, err %v", n, len(got), err)
			}
			d = append(d, time.Since(start))
		}
		fmt.Fprintf(&b, "| %d | %ds | %.2fms | %.2fms |\n", n, step,
			float64(percentile(d, 0.50))/float64(time.Millisecond), float64(percentile(d, 0.99))/float64(time.Millisecond))
	}
	t.Log("\n" + b.String())
}

// 读池取值的对照台。默认跳过；跑法（GOMAXPROCS 可用环境变量改，模拟小机器）：
//
//	HERON_POOL_BENCH=1 [GOMAXPROCS=n] go test ./internal/hub/store/ -run TestReadPoolLoadComparison -v -count=1 -timeout 30m
//
// 两种负载，P = GOMAXPROCS：
//   - steady：闭环，2P 个重读者循环查一个节点 poolBenchMinutes 分钟的 1m 历史（预计扫描量超过 lightScanRows，
//     拆池时走历史池；按 15 分钟分桶），P 个轻读者
//     循环 ListNodes + Settings（面板轮询读的那一类）。看轻读者排在重读者后面多久。
//   - heavy-only：同 steady 但没有轻读者。CPU 全给重读者，比较各组重读吞吐只看池的上限，不受轻读者
//     抢走多少 CPU 影响（steady 下轻读者不再排队后会多做一个数量级的读，重读吞吐随之下降）。
//   - burst：每 poolBenchTick 同时起 3P 个轻读者各读一次，之间池子闲着。看归还时关掉、下一波重开
//     （MaxIdleClosed）的代价。
//
// 单池各组让历史读也走轻池（hr 临时指向 r），拆池组即生产的两个池与取值。每种负载下各组交错重复
// poolBenchReps 次取中位数；每次开跑前关掉全部空闲连接，各组都从冷池开始。延迟只作同机对照。
func TestReadPoolLoadComparison(t *testing.T) {
	if os.Getenv("HERON_POOL_BENCH") == "" {
		t.Skip("set HERON_POOL_BENCH=1 to run the read pool comparison")
	}
	const (
		poolBenchRun   = 5 * time.Second
		poolBenchTick  = 100 * time.Millisecond
		poolBenchNodes = 16
		poolBenchReps  = 3
		poolBenchFrom  = 86400
		// poolBenchMinutes 刚过 lightScanRows 的两倍，在单序列读量额度（quotaRowsPerSeries）之内。
		poolBenchMinutes = 2*lightScanRows + 1
	)
	s, _ := open(t)
	ctx := context.Background()
	ids := make([]int64, 0, poolBenchNodes)
	for i := range poolBenchNodes {
		id, _, err := s.CreateNode(ctx, fmt.Sprintf("n%d", i), Billing{}, hash(byte(i+1)))
		if err != nil {
			t.Fatal(err)
		}
		rows := make([]metric.Row, 0, poolBenchMinutes)
		for m := range int64(poolBenchMinutes) {
			rows = append(rows, metric.Row{NodeID: id, TS: poolBenchFrom + m*60, CoverageStart: poolBenchFrom, Bucket: bucket(float64(m % 100))})
		}
		if rejected, err := s.WriteMinuteBatch(ctx, metric.Batch{Rows: rows}); err != nil || rejected != 0 {
			t.Fatalf("seed node %d: rejected %d, err %v", id, rejected, err)
		}
		if got, err := s.ReadMinuteRows(ctx, id, poolBenchFrom, poolBenchFrom+poolBenchMinutes*60); err != nil || len(got) != len(rows) {
			t.Fatalf("seed node %d: read back %d rows (err %v), want %d", id, len(got), err, len(rows))
		}
		ids = append(ids, id)
	}
	lv, _ := LevelByName("1m")
	p := runtime.GOMAXPROCS(0)
	history := s.hr
	type poolConfig struct {
		name       string
		open, idle int
		// historyOpen 为 0 表示单池：历史读与轻读共用 r。
		historyOpen int
	}
	configs := []poolConfig{
		{"single default (open unlimited, idle 2)", 0, 2, 0},
		{fmt.Sprintf("single open=P=%d idle=P", p), p, p, 0},
		{fmt.Sprintf("single open=P=%d idle=P/2", p), p, max(1, p/2), 0},
		{fmt.Sprintf("single open=2P=%d idle=2P", 2*p), 2 * p, 2 * p, 0},
		{fmt.Sprintf("split light=%d history=%d", readPoolSize(), historyPoolSize()), readPoolSize(), readPoolSize(), historyPoolSize()},
	}
	type poolMeasurement struct {
		peakOpen                          int64
		idleClosed, idleTimeClosed, waits int64
		waitDur                           time.Duration
	}
	type measurement struct {
		light, history                       poolMeasurement
		fastP50, fastP99, heavyP50, heavyP99 time.Duration
		fastOps, heavyOps                    int
	}
	fastRead := func() error {
		if _, err := s.ListNodes(ctx); err != nil {
			return err
		}
		_, err := s.Settings(ctx)
		return err
	}
	closedLoop := func(fastReaders int) func(deadline time.Time, record func(heavy bool, d time.Duration)) {
		return func(deadline time.Time, record func(heavy bool, d time.Duration)) {
			var wg sync.WaitGroup
			for g := range 2 * p {
				wg.Go(func() {
					for i := 0; time.Now().Before(deadline); i++ {
						start := time.Now()
						if _, err := s.QueryMetrics(ctx, ids[(g+i)%len(ids)], poolBenchFrom, poolBenchFrom+poolBenchMinutes*60, lv, 900); err != nil {
							t.Error(err)
							return
						}
						record(true, time.Since(start))
					}
				})
			}
			for range fastReaders {
				wg.Go(func() {
					for time.Now().Before(deadline) {
						start := time.Now()
						if err := fastRead(); err != nil {
							t.Error(err)
							return
						}
						record(false, time.Since(start))
					}
				})
			}
			wg.Wait()
		}
	}
	burst := func(deadline time.Time, record func(heavy bool, d time.Duration)) {
		var wg sync.WaitGroup
		for time.Now().Before(deadline) {
			for range 3 * p {
				wg.Go(func() {
					start := time.Now()
					if err := fastRead(); err != nil {
						t.Error(err)
						return
					}
					record(false, time.Since(start))
				})
			}
			time.Sleep(poolBenchTick)
		}
		wg.Wait()
	}
	reset := func(db *sql.DB, open, idle int) {
		db.SetMaxIdleConns(-1) // 关掉全部空闲连接：每组从冷池开始。
		db.SetMaxOpenConns(open)
		db.SetMaxIdleConns(idle)
		db.SetConnMaxIdleTime(0)
	}
	for _, scenario := range []struct {
		name string
		run  func(deadline time.Time, record func(heavy bool, d time.Duration))
	}{{"steady", closedLoop(p)}, {"heavy-only", closedLoop(0)}, {"burst", burst}} {
		t.Run(scenario.name, func(t *testing.T) {
			results := make([][]measurement, len(configs))
			for rep := range poolBenchReps {
				for ci, c := range configs {
					reset(s.r, c.open, c.idle)
					s.hr = history
					if c.historyOpen == 0 {
						s.hr = s.r
					} else {
						reset(s.hr, c.historyOpen, c.historyOpen)
					}
					runtime.GC()
					lightBefore, historyBefore := s.r.Stats(), s.hr.Stats()
					var lightPeak, historyPeak atomic.Int64
					deadline := time.Now().Add(poolBenchRun)
					sampled := make(chan struct{})
					go func() {
						defer close(sampled)
						for time.Now().Before(deadline) {
							if n := int64(s.r.Stats().OpenConnections); n > lightPeak.Load() {
								lightPeak.Store(n)
							}
							if n := int64(s.hr.Stats().OpenConnections); n > historyPeak.Load() {
								historyPeak.Store(n)
							}
							time.Sleep(time.Millisecond)
						}
					}()
					var mu sync.Mutex
					var fast, heavy []time.Duration
					scenario.run(deadline, func(isHeavy bool, d time.Duration) {
						mu.Lock()
						defer mu.Unlock()
						if isHeavy {
							heavy = append(heavy, d)
						} else {
							fast = append(fast, d)
						}
					})
					<-sampled
					delta := func(before, after sql.DBStats, peak int64) poolMeasurement {
						return poolMeasurement{
							peakOpen:       peak,
							idleClosed:     after.MaxIdleClosed - before.MaxIdleClosed,
							idleTimeClosed: after.MaxIdleTimeClosed - before.MaxIdleTimeClosed,
							waits:          after.WaitCount - before.WaitCount,
							waitDur:        after.WaitDuration - before.WaitDuration,
						}
					}
					m := measurement{
						light:    delta(lightBefore, s.r.Stats(), lightPeak.Load()),
						fastOps:  len(fast),
						heavyOps: len(heavy),
					}
					if c.historyOpen != 0 {
						m.history = delta(historyBefore, s.hr.Stats(), historyPeak.Load())
					}
					m.fastP50, m.fastP99 = percentile(fast, 0.50), percentile(fast, 0.99)
					m.heavyP50, m.heavyP99 = percentile(heavy, 0.50), percentile(heavy, 0.99)
					t.Logf("%s rep %d %-40s light{peak=%d idleClosed=%d idleTimeClosed=%d waits=%d waitDur=%v} history{peak=%d idleClosed=%d idleTimeClosed=%d waits=%d waitDur=%v} fast p50/p99=%v/%v (%d ops) heavy p50/p99=%v/%v (%d ops)",
						scenario.name, rep+1, c.name,
						m.light.peakOpen, m.light.idleClosed, m.light.idleTimeClosed, m.light.waits, m.light.waitDur.Round(time.Millisecond),
						m.history.peakOpen, m.history.idleClosed, m.history.idleTimeClosed, m.history.waits, m.history.waitDur.Round(time.Millisecond),
						m.fastP50.Round(time.Microsecond), m.fastP99.Round(time.Microsecond), m.fastOps,
						m.heavyP50.Round(time.Microsecond), m.heavyP99.Round(time.Microsecond), m.heavyOps)
					results[ci] = append(results[ci], m)
				}
			}
			s.hr = history
			var b strings.Builder
			fmt.Fprintf(&b, "%s, GOMAXPROCS=%d, %v per run, median of %d; history columns only for the split config\n", scenario.name, p, poolBenchRun, poolBenchReps)
			b.WriteString("| config | peak open light/history | MaxIdleClosed light/history | MaxIdleTimeClosed light/history | WaitCount light/history | WaitDuration light/history | fast p50 | fast p99 | fast ops | heavy p50 | heavy p99 | heavy ops |\n")
			b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|\n")
			for ci, c := range configs {
				rs := results[ci]
				med := func(f func(measurement) float64) float64 {
					v := make([]float64, len(rs))
					for i, r := range rs {
						v[i] = f(r)
					}
					slices.Sort(v)
					return v[len(v)/2]
				}
				pair := func(f func(poolMeasurement) float64, format string) string {
					l := fmt.Sprintf(format, med(func(r measurement) float64 { return f(r.light) }))
					if c.historyOpen == 0 {
						return l
					}
					return l + " / " + fmt.Sprintf(format, med(func(r measurement) float64 { return f(r.history) }))
				}
				ms := func(f func(measurement) time.Duration) string {
					return fmt.Sprintf("%.2fms", med(func(r measurement) float64 { return float64(f(r)) / float64(time.Millisecond) }))
				}
				fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %.0f | %s | %s | %.0f |\n", c.name,
					pair(func(m poolMeasurement) float64 { return float64(m.peakOpen) }, "%.0f"),
					pair(func(m poolMeasurement) float64 { return float64(m.idleClosed) }, "%.0f"),
					pair(func(m poolMeasurement) float64 { return float64(m.idleTimeClosed) }, "%.0f"),
					pair(func(m poolMeasurement) float64 { return float64(m.waits) }, "%.0f"),
					pair(func(m poolMeasurement) float64 { return float64(m.waitDur) / float64(time.Second) }, "%.1fs"),
					ms(func(r measurement) time.Duration { return r.fastP50 }),
					ms(func(r measurement) time.Duration { return r.fastP99 }),
					med(func(r measurement) float64 { return float64(r.fastOps) }),
					ms(func(r measurement) time.Duration { return r.heavyP50 }),
					ms(func(r measurement) time.Duration { return r.heavyP99 }),
					med(func(r measurement) float64 { return float64(r.heavyOps) }))
			}
			t.Log("\n" + b.String())
		})
	}
}

func percentile(d []time.Duration, q float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := slices.Clone(d)
	slices.Sort(s)
	return s[int(math.Ceil(q*float64(len(s))))-1]
}
