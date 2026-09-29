package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/gen/heron/v1/heronv1connect"
	"github.com/xjetry/heron-probe/internal/clock"
	"github.com/xjetry/heron-probe/internal/hub/alert"
	"github.com/xjetry/heron-probe/internal/hub/auth"
	"github.com/xjetry/heron-probe/internal/hub/metric"
	"github.com/xjetry/heron-probe/internal/hub/store"
	"github.com/xjetry/heron-probe/internal/hub/web"
	"github.com/xjetry/heron-probe/internal/testlog"
	"github.com/xjetry/heron-probe/internal/testwait"
	"google.golang.org/protobuf/proto"
)

type serveEvents chan map[string]json.RawMessage

func (events serveEvents) Write(p []byte) (int, error) {
	var event map[string]json.RawMessage
	if err := json.Unmarshal(p, &event); err != nil {
		return 0, err
	}
	select {
	case events <- event:
	default:
	}
	return len(p), nil
}

func startTestHub(t *testing.T, db string, clk clock.Clock, flags ...string) (string, serveEvents, func()) {
	t.Helper()
	return startTestHubWithTTL(t, db, clk, "45s", flags...)
}

func startTestHubWithTTL(t *testing.T, db string, clk clock.Clock, ttl string, flags ...string) (string, serveEvents, func()) {
	t.Helper()
	t.Setenv("HERON_OFFLINE_AFTER", ttl)
	ctx, cancel := context.WithCancel(context.Background())
	events := make(serveEvents, 128)
	done := make(chan struct{})
	var result error
	args := append([]string{"--db", db, "--listen", "127.0.0.1:0"}, flags...)
	go func() { result = runServeWith(ctx, args, clk, slog.New(slog.NewJSONHandler(events, nil))); close(done) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			// 从 cancel 起算。空闲关停若总是等满排空超时，会在 drainTimeout 时返回且 result 仍可能为 nil。
			canceled := time.Now()
			cancel()
			select {
			case <-done:
				if result != nil {
					t.Errorf("serve exit: %v", result)
				}
				if elapsed := time.Since(canceled); elapsed >= drainTimeout {
					t.Errorf("idle shutdown took %v since cancel, want < %v", elapsed, drainTimeout)
				}
			case <-time.After(testwait.Bound):
				t.Error("serve did not join background loops")
			}
		})
	}
	t.Cleanup(stop)
	timer := time.NewTimer(testwait.Bound)
	defer timer.Stop()
	for {
		select {
		case e := <-events:
			if string(e["msg"]) == `"hub listening"` {
				var addr string
				if err := json.Unmarshal(e["listen"], &addr); err != nil {
					t.Fatal(err)
				}
				return "http://" + addr, events, stop
			}
		case <-done:
			t.Fatalf("serve stopped before listening: %v", result)
		case <-timer.C:
			t.Fatal("serve did not bind a listener")
		}
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// scripts/e2e.sh 从启动行按整秒读出 ttl、offline_sweep 与 delivery_retry_wait 推出告警等待上限。这里用
// runServe 同一个 newServeLogger 装配日志，按脚本同形的 testlog.WholeSeconds 取值再与常量比较：字段缺失、
// 改名、不再是整秒写法或不跟常量走时 make ci 先红，而不是等到 e2e 才停下。
func TestServeStartupLineStatesAlertTiming(t *testing.T) {
	t.Setenv("HERON_OFFLINE_AFTER", "12s")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &lockedBuffer{}
	done := make(chan error, 1)
	args := []string{"--db", filepath.Join(t.TempDir(), "hub.db"), "--listen", "127.0.0.1:0"}
	go func() {
		done <- runServeWith(ctx, args, clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)), newServeLogger(out))
	}()
	testwait.Until(t, 10*time.Millisecond, func() bool { return strings.Contains(out.String(), `msg="hub listening"`) }, "no startup line in %v", testwait.When(out.String))
	for _, f := range []struct {
		key  string
		want time.Duration
	}{
		{"ttl", 12 * time.Second},
		{"offline_sweep", alert.OfflineSweepEvery},
		{"delivery_retry_wait", alert.DeliveryRetryWait()},
	} {
		got, ok := testlog.WholeSeconds(out.String(), "hub listening", f.key)
		if !ok || got != f.want {
			t.Errorf("startup line: %s read as %v (ok=%v), want whole seconds equal to %v; log:\n%s", f.key, got, ok, f.want, out.String())
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve exit: %v", err)
		}
	case <-time.After(testwait.Bound):
		t.Fatal("serve did not stop")
	}
}

func TestServeMountsAdminAndPasswdRevokesWithoutRestart(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hub.db")
	old, newPassword := "initial sufficiently long password", "replacement sufficiently long password"
	if err := runPasswdWith([]string{"--db", db}, pipeWith(t, old+"\n"), io.Discard); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	url, _, stop := startTestHub(t, db, clk, "--trusted-proxies", "127.0.0.1/32")
	client := heronv1connect.NewAdminServiceClient(http.DefaultClient, url)
	ctx := context.Background()
	login := connect.NewRequest(&heronv1.LoginRequest{Password: old})
	login.Header().Set("X-Forwarded-Proto", "https")
	logged, err := client.Login(ctx, login)
	if err != nil {
		t.Fatalf("real serve login: %v", err)
	}
	cookies := (&http.Response{Header: logged.Header()}).Cookies()
	if len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("serve did not pass trusted proxies: %v", cookies)
	}
	cookie := cookies[0].Name + "=" + cookies[0].Value
	create := connect.NewRequest(&heronv1.CreateNodeRequest{Name: "n"})
	create.Header().Set("Cookie", cookie)
	node, err := client.CreateNode(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	report := connect.NewRequest(&heronv1.ReportRequest{Metrics: &heronv1.Metrics{CpuPct: proto.Float64(42)}})
	report.Header().Set("Authorization", "Bearer "+node.Msg.Token)
	agent := heronv1connect.NewAgentServiceClient(http.DefaultClient, url)
	if _, err := agent.Report(ctx, report); err != nil {
		t.Fatal(err)
	}
	snapReq := connect.NewRequest(&heronv1.GetSnapshotRequest{})
	snapReq.Header().Set("Cookie", cookie)
	snap, err := client.GetSnapshot(ctx, snapReq)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Msg.ReportIntervalMs != 15000 || len(snap.Msg.Nodes) != 1 || !snap.Msg.Nodes[0].Online {
		t.Fatalf("serve did not share interval/live state: %v", snap.Msg)
	}
	if snap.Msg.HubVersion != version || version == "" {
		t.Fatalf("serve reports hub_version %q, want the build version %q", snap.Msg.HubVersion, version)
	}
	var notice bytes.Buffer
	if err := runPasswdWith([]string{"--db", db}, pipeWith(t, newPassword+"\n"), &notice); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(notice.String(), newPassword) || !strings.Contains(notice.String(), "every existing session has been revoked") {
		t.Fatalf("passwd notice leaked secret or omitted revocation: %q", notice.String())
	}
	if _, err := client.GetSnapshot(ctx, snapReq); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("session survived live passwd: %v", err)
	}
	if _, err := client.Login(ctx, connect.NewRequest(&heronv1.LoginRequest{Password: old})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("old password still logs in: %v", err)
	}
	if _, err := client.Login(ctx, connect.NewRequest(&heronv1.LoginRequest{Password: newPassword})); err != nil {
		t.Fatalf("new password requires restart: %v", err)
	}
	stop()
	st, _, err := openOffline(db, false)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ts := clk.Now().Truncate(time.Minute).Unix()
	rows, err := st.ReadMinuteRows(ctx, node.Msg.Node.Id, ts, ts+60)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Bucket.N[0] != 1 || rows[0].Bucket.Sum[0] != 42 {
		t.Fatalf("shutdown lost final minute: %v", rows)
	}
}

// serve 把 --theme-origin 与"是否给了 --public-dir"交给管理服务：没配 origin 时主题方法 FailedPrecondition；配了就能调，
// ListThemes 回显规范形态的 origin 与 public_dir，面板据此给出主题的地址与"主 origin 被目录接管"的提示。
func TestServePassesThemeOriginToAdmin(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("site"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		flags     []string
		want      connect.Code
		origin    string
		publicDir bool
	}{
		{nil, connect.CodeFailedPrecondition, "", false},
		{[]string{"--theme-origin", "https://Status.Example.com/"}, 0, "https://status.example.com", false},
		{[]string{"--theme-origin", "http://status.example.com:8081", "--public-dir", dir}, 0, "http://status.example.com:8081", true},
	} {
		t.Run(fmt.Sprint(tc.flags), func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "hub.db")
			const pw = "initial sufficiently long password"
			if err := runPasswdWith([]string{"--db", db}, pipeWith(t, pw+"\n"), io.Discard); err != nil {
				t.Fatal(err)
			}
			url, _, _ := startTestHub(t, db, clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)), tc.flags...)
			client := heronv1connect.NewAdminServiceClient(http.DefaultClient, url)
			logged, err := client.Login(t.Context(), connect.NewRequest(&heronv1.LoginRequest{Password: pw}))
			if err != nil {
				t.Fatal(err)
			}
			req := connect.NewRequest(&heronv1.ListThemesRequest{})
			req.Header().Set("Cookie", strings.Split(logged.Header().Get("Set-Cookie"), ";")[0])
			resp, err := client.ListThemes(t.Context(), req)
			if got := connect.CodeOf(err); (err == nil && tc.want != 0) || (err != nil && got != tc.want) {
				t.Fatalf("ListThemes with flags %v: %v, want code %v", tc.flags, err, tc.want)
			}
			if err == nil && (resp.Msg.GetThemeOrigin() != tc.origin || resp.Msg.GetPublicDir() != tc.publicDir) {
				t.Fatalf("ListThemes with flags %v: theme_origin %q public_dir %v, want %q %v", tc.flags, resp.Msg.GetThemeOrigin(), resp.Msg.GetPublicDir(), tc.origin, tc.publicDir)
			}
		})
	}
}

func TestServeRunsMaintenanceWithConfiguredRetention(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hub.db")
	password := "storage health retention check password"
	if err := runPasswdWith([]string{"--db", db}, pipeWith(t, password+"\n"), io.Discard); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 59, 999000000, time.UTC))
	st, err := store.Open(db, clk, slog.New(slog.NewTextHandler(io.Discard, nil)), store.MigrateSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	_, hash := auth.NewToken()
	id, _, err := st.CreateNode(ctx, "n", hash[:])
	if err != nil {
		t.Fatal(err)
	}
	expired := map[string]int64{
		"1m": clk.Now().Add(-7 * time.Hour).Truncate(time.Hour).Unix(),
		"5m": clk.Now().Add(-8 * 24 * time.Hour).Truncate(time.Hour).Unix(),
		"1h": clk.Now().Add(-14 * 24 * time.Hour).Truncate(time.Hour).Unix(),
	}
	recent := clk.Now().Add(-20 * time.Minute).Truncate(time.Minute).Unix()
	b := metric.NewBucket()
	b.Add(&heronv1.Metrics{CpuPct: proto.Float64(1)})
	rowsToWrite := []metric.Row{{NodeID: id, TS: recent, Bucket: b}}
	for _, ts := range expired {
		rowsToWrite = append(rowsToWrite, metric.Row{NodeID: id, TS: ts, Bucket: b})
	}
	if _, err := st.WriteMinuteBatch(ctx, metric.Batch{Rows: rowsToWrite}); err != nil {
		t.Fatal(err)
	}
	if err := st.Rollup(ctx); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"1m", "5m", "1h"} {
		lv, _ := store.LevelByName(name)
		rows, err := st.QueryMetrics(ctx, id, expired[name], expired[name]+60, lv, lv.Bucket)
		if err != nil || len(rows) != 1 {
			t.Fatalf("missing %s control row before maintenance: rows=%v err=%v", name, rows, err)
		}
	}
	url, events, stop := startTestHub(t, db, clk, "--retention-1m", "6h", "--retention-5m", "168h", "--retention-1h", "168h")
	deadline := time.NewTimer(testwait.Bound)
	defer deadline.Stop()
	for {
		select {
		case e := <-events:
			if string(e["msg"]) == `"pruned expired rows"` {
				goto pruned
			}
		case <-deadline.C:
			t.Fatal("maintenance never pruned expired rows")
		}
	}
pruned:
	for _, name := range []string{"1m", "5m", "1h"} {
		lv, _ := store.LevelByName(name)
		rows, err := st.QueryMetrics(ctx, id, expired[name], expired[name]+60, lv, lv.Bucket)
		if err != nil || len(rows) != 0 {
			t.Errorf("configured retention left %s expired rows: %v %v", name, rows, err)
		}
	}
	rows, err := st.ReadMinuteRows(ctx, id, recent, recent+60)
	if err != nil || len(rows) != 1 {
		t.Errorf("maintenance lost retained control row: %v %v", rows, err)
	}
	// retention_s 只取决于装配的配置，与数据和时序无关，不必抢在维护之前查：这条只钉住 serve
	// 把 --retention-* 传给了 api.Config.Retention 的那份值。标红结论是否也用这份配置由
	// internal/hub/api 的 TestGetStorageStatsUsesTheConfiguredRetention 钉住——那边的夹具不跑
	// RunMaintenance，判定不依赖任何真实时间窗。
	client := heronv1connect.NewAdminServiceClient(http.DefaultClient, url)
	logged, err := client.Login(ctx, connect.NewRequest(&heronv1.LoginRequest{Password: password}))
	if err != nil {
		t.Fatalf("login for storage stats check: %v", err)
	}
	cookies := (&http.Response{Header: logged.Header()}).Cookies()
	statsReq := connect.NewRequest(&heronv1.GetStorageStatsRequest{})
	statsReq.Header().Set("Cookie", cookies[0].Name+"="+cookies[0].Value)
	stats, err := client.GetStorageStats(ctx, statsReq)
	if err != nil {
		t.Fatalf("GetStorageStats: %v", err)
	}
	wantRetentionS := map[string]uint64{"1m": uint64(6 * time.Hour / time.Second), "5m": uint64(168 * time.Hour / time.Second), "1h": uint64(168 * time.Hour / time.Second)}
	var sawSeries int
	for _, s := range stats.Msg.GetSeries() {
		level, ok := strings.CutPrefix(s.GetTable(), "metric_")
		if !ok {
			level, ok = strings.CutPrefix(s.GetTable(), "probe_")
		}
		if !ok {
			t.Fatalf("series table %q matches neither the metric_ nor the probe_ prefix", s.GetTable())
		}
		sawSeries++
		if want := wantRetentionS[level]; s.GetRetentionS() != want {
			t.Errorf("%s retention_s = %d, want the configured %d", s.GetTable(), s.GetRetentionS(), want)
		}
	}
	if sawSeries != 6 {
		t.Fatalf("GetStorageStats reported %d series, want 6", sawSeries)
	}
	stop()
}

func TestShutdownHTTPWaitsForHandlersAfterClosingConnections(t *testing.T) {
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var requests atomic.Int32
	drain := &drainingHandler{next: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) > 1 {
			return
		}
		close(entered)
		<-r.Context().Done()
		close(canceled)
		<-release
	})}
	srv := httptest.NewUnstartedServer(drain)
	srv.Start()
	defer func() { unblock(); srv.Close() }()
	go func() {
		resp, err := srv.Client().Get(srv.URL)
		if err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(testwait.Bound):
		t.Fatal("request did not enter handler")
	}
	done := make(chan error, 1)
	// 20ms 是交给 shutdownHTTP 的超时，用来证明到点会断开连接，不是等退出的上界。
	go func() { done <- shutdownHTTP(srv.Config, drain, 20*time.Millisecond) }()
	select {
	case <-canceled:
	case <-time.After(testwait.Bound):
		unblock()
		srv.CloseClientConnections()
		t.Fatal("shutdown timeout did not close active connections")
	}
	select {
	case err := <-done:
		t.Fatalf("shutdown returned before handler finished: %v", err)
	// 负向窗口：排空未完成时 shutdown 不应返回。窗口短只会漏掉稍晚才提前返回的缺陷，不会把仍在等待的调用判失败。
	case <-time.After(50 * time.Millisecond):
	}
	unblock()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shutdown hid timeout: %v", err)
		}
	case <-time.After(testwait.Bound):
		t.Fatal("shutdown did not finish after handler returned")
	}
	recorder := httptest.NewRecorder()
	drain.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("drained handler admitted new request: %d", recorder.Code)
	}
}

func TestServeRejectsUnknownTimezoneBeforeListening(t *testing.T) {
	err := runServeWith(context.Background(), []string{"--db", filepath.Join(t.TempDir(), "hub.db"), "--listen", "127.0.0.1:0", "--timezone", "Mars/Olympus"},
		clock.NewFake(time.Now()), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "--timezone") {
		t.Fatalf("err = %v, want a --timezone error", err)
	}
}

// 退出时流量必须先于关库落盘：最后一次上报之后立刻停机，重启后总量仍在。
func TestServeFlushesTrafficOnShutdown(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hub.db")
	password := "initial sufficiently long password"
	if err := runPasswdWith([]string{"--db", db}, pipeWith(t, password+"\n"), io.Discard); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	url, _, stop := startTestHub(t, db, clk, "--timezone", "UTC")
	client := heronv1connect.NewAdminServiceClient(http.DefaultClient, url)
	ctx := context.Background()
	logged, err := client.Login(ctx, connect.NewRequest(&heronv1.LoginRequest{Password: password}))
	if err != nil {
		t.Fatal(err)
	}
	cookies := (&http.Response{Header: logged.Header()}).Cookies()
	create := connect.NewRequest(&heronv1.CreateNodeRequest{Name: "n"})
	create.Header().Set("Cookie", cookies[0].Name+"="+cookies[0].Value)
	node, err := client.CreateNode(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	agent := heronv1connect.NewAgentServiceClient(http.DefaultClient, url)
	for _, m := range []*heronv1.Metrics{
		{BootId: "b", NetRxTotal: proto.Uint64(1000), NetTxTotal: proto.Uint64(5000)},
		{BootId: "b", NetRxTotal: proto.Uint64(1200), NetTxTotal: proto.Uint64(5001)},
	} {
		report := connect.NewRequest(&heronv1.ReportRequest{Metrics: m})
		report.Header().Set("Authorization", "Bearer "+node.Msg.Token)
		if _, err := agent.Report(ctx, report); err != nil {
			t.Fatal(err)
		}
	}
	stop() // 10 秒的刷出周期尚未到：能落盘的只有退出时的那一次
	st, _, err := openOffline(db, false)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	recs, err := st.LoadTraffic(ctx)
	if err != nil || len(recs) != 1 {
		t.Fatalf("traffic rows after shutdown: %v %v", recs, err)
	}
	if r := recs[0]; r.NodeID != node.Msg.Node.Id || r.TotalRx != 200 || r.TotalTx != 1 || r.LastRx != 1200 || r.BootID != "b" {
		t.Fatalf("shutdown lost the traffic state: %+v", r)
	}
}

func TestServeWarnsWhenLocalTimezoneCannotBeResolved(t *testing.T) {
	t.Setenv("TZ", "")
	old := localtimePath
	localtimePath = filepath.Join(t.TempDir(), "missing-localtime")
	t.Cleanup(func() { localtimePath = old })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var logs bytes.Buffer
	err := runServeWith(ctx, []string{"--db", filepath.Join(t.TempDir(), "hub.db"), "--listen", "127.0.0.1:0"},
		clock.NewFake(time.Now()), slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "set --timezone") {
		t.Fatalf("missing UTC fallback warning: %s", &logs)
	}
}

// serve 的装配与 newTestMux 各写一份：这里经真实 serve 调一次公开服务，挂载遗漏不会只在 mux 测试里被掩盖。
// max-age 只有 Public.Handler 的挂载点中间件会写，而它与限流同在一条链上（链内顺序由 api 包的测试钉住）；
// Public 实现了生成的 handler 接口，挂成裸 connect 处理器也能编译，拿到这个头才证明 serve 挂的是 Handler()。
func TestServeMountsPublicService(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hub.db")
	url, _, _ := startTestHub(t, db, clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	resp, err := http.Get(url + "/heron.v1.PublicService/GetSite?connect=v1&encoding=json&message=%7B%7D")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var site struct {
		Theme string `json:"theme"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&site); err != nil || resp.StatusCode != http.StatusOK || site.Theme != "auto" {
		t.Fatalf("GetSite via serve: %d %+v %v", resp.StatusCode, site, err)
	}
	if got := resp.Header.Get("Cache-Control"); got != "max-age=300" {
		t.Fatalf("GetSite via serve: Cache-Control %q, want max-age=300 from Public.Handler", got)
	}
}

// 不带 --public-dir 时根路径是内置公开页。serve 的装配与 newTestMux 各写一份，这里经真实 serve 核对：应答与直接调用
// web.PublicHandler 逐字节相同。面板与公开页的应答总是不同（构建过是各自的 index.html，没构建是各自的说明页），
// 所以无论是否构建过，把 / 挂成面板或别的处理器都会在这里现形。
func TestServeMountsBuiltinPublicPageAtRoot(t *testing.T) {
	url, _, _ := startTestHub(t, filepath.Join(t.TempDir(), "hub.db"), clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	resp, err := http.Get(url + "/nodes/3")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	want := httptest.NewRecorder()
	web.PublicHandler().ServeHTTP(want, httptest.NewRequest(http.MethodGet, "/nodes/3", nil))
	if resp.StatusCode != want.Code || string(body) != want.Body.String() {
		t.Fatalf("/nodes/3 via serve: %d %q, want the built-in public page: %d %q", resp.StatusCode, body, want.Code, want.Body.String())
	}
}

// 替换目录在打开数据库之前核对：配置有误时 hub 不留下任何副作用。
func TestServeRejectsPublicDirWithoutIndexBeforeOpeningTheDatabase(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hub.db")
	dir := t.TempDir()
	err := runServeWith(context.Background(), []string{"--db", db, "--listen", "127.0.0.1:0", "--public-dir", dir},
		clock.NewFake(time.Now()), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "--public-dir "+dir) {
		t.Fatalf("err = %v, want a --public-dir error naming the directory", err)
	}
	if _, statErr := os.Stat(db); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("database touched before --public-dir was checked: %v", statErr)
	}
}

// 替换目录只接管 /：面板与 RPC 路径的路由优先级更高，目录里同名的文件遮蔽不了它们。
func TestServePublicDirReplacesRootButNotPanelOrRPC(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"index.html":                     "custom site",
		"admin/index.html":               "shadow panel",
		"heron.v1.PublicService/GetSite": "shadow rpc",
	} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	url, _, _ := startTestHub(t, filepath.Join(t.TempDir(), "hub.db"), clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)), "--public-dir", dir)
	fetch := func(path string) (*http.Response, string) {
		t.Helper()
		resp, err := http.Get(url + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp, string(b)
	}
	if resp, body := fetch("/nodes/3"); resp.StatusCode != http.StatusOK || body != "custom site" || resp.Header.Get("Content-Security-Policy") != "frame-ancestors 'none'" {
		t.Fatalf("/nodes/3: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	if resp, body := fetch("/admin/"); strings.Contains(body, "shadow") || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatalf("/admin/ was shadowed: %d %q", resp.StatusCode, body)
	}
	if resp, body := fetch("/heron.v1.PublicService/GetSite?connect=v1&encoding=json&message=%7B%7D"); resp.StatusCode != http.StatusOK || strings.Contains(body, "shadow") {
		t.Fatalf("RPC path was shadowed: %d %q", resp.StatusCode, body)
	}
}

// --public-dir 只接管主 origin：主题 origin 上没有启用中的主题时服务的是内置公开页，而不是目录（§10.1）。经真实 serve
// 核对 serve 交给主题 origin 的回落处理器；主 origin 同时仍是目录。
func TestServeThemeOriginIgnoresPublicDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("custom site"), 0o644); err != nil {
		t.Fatal(err)
	}
	url, _, _ := startTestHub(t, filepath.Join(t.TempDir(), "hub.db"), clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		"--public-dir", dir, "--theme-origin", "https://status.example.com")
	fetch := func(host string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, url+"/nodes/3", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(b)
	}
	want := httptest.NewRecorder()
	web.PublicHandler().ServeHTTP(want, httptest.NewRequest(http.MethodGet, "/nodes/3", nil))
	if code, body := fetch("status.example.com"); code != want.Code || body != want.Body.String() {
		t.Fatalf("theme origin /nodes/3: %d %q, want the built-in public page %d %q", code, body, want.Code, want.Body.String())
	}
	if code, body := fetch(strings.TrimPrefix(url, "http://")); code != http.StatusOK || body != "custom site" {
		t.Fatalf("main origin /nodes/3: %d %q, want the --public-dir page", code, body)
	}
}
