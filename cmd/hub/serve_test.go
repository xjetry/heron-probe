package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/auth"
	"github.com/xjetry/probe/internal/hub/metric"
	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/testwait"
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
	t.Setenv("PROBE_OFFLINE_AFTER", ttl)
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

func TestServeMountsAdminAndPasswdRevokesWithoutRestart(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hub.db")
	old, newPassword := "initial sufficiently long password", "replacement sufficiently long password"
	if err := runPasswdWith([]string{"--db", db}, pipeWith(t, old+"\n"), io.Discard); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	url, _, stop := startTestHub(t, db, clk, "--trusted-proxies", "127.0.0.1/32")
	client := probev1connect.NewAdminServiceClient(http.DefaultClient, url)
	ctx := context.Background()
	login := connect.NewRequest(&probev1.LoginRequest{Password: old})
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
	create := connect.NewRequest(&probev1.CreateNodeRequest{Name: "n"})
	create.Header().Set("Cookie", cookie)
	node, err := client.CreateNode(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	report := connect.NewRequest(&probev1.ReportRequest{Metrics: &probev1.Metrics{CpuPct: proto.Float64(42)}})
	report.Header().Set("Authorization", "Bearer "+node.Msg.Token)
	agent := probev1connect.NewAgentServiceClient(http.DefaultClient, url)
	if _, err := agent.Report(ctx, report); err != nil {
		t.Fatal(err)
	}
	snapReq := connect.NewRequest(&probev1.GetSnapshotRequest{})
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
	if _, err := client.Login(ctx, connect.NewRequest(&probev1.LoginRequest{Password: old})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("old password still logs in: %v", err)
	}
	if _, err := client.Login(ctx, connect.NewRequest(&probev1.LoginRequest{Password: newPassword})); err != nil {
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

func TestServeRunsMaintenanceWithConfiguredRetention(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hub.db")
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 59, 999000000, time.UTC))
	st, err := store.Open(db, clk, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	_, hash := auth.NewToken()
	id, err := st.CreateNode(ctx, "n", hash[:])
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
	b.Add(&probev1.Metrics{CpuPct: proto.Float64(1)})
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
	_, events, stop := startTestHub(t, db, clk, "--retention-1m", "6h", "--retention-5m", "168h", "--retention-1h", "168h")
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
	client := probev1connect.NewAdminServiceClient(http.DefaultClient, url)
	ctx := context.Background()
	logged, err := client.Login(ctx, connect.NewRequest(&probev1.LoginRequest{Password: password}))
	if err != nil {
		t.Fatal(err)
	}
	cookies := (&http.Response{Header: logged.Header()}).Cookies()
	create := connect.NewRequest(&probev1.CreateNodeRequest{Name: "n"})
	create.Header().Set("Cookie", cookies[0].Name+"="+cookies[0].Value)
	node, err := client.CreateNode(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	agent := probev1connect.NewAgentServiceClient(http.DefaultClient, url)
	for _, m := range []*probev1.Metrics{
		{BootId: "b", NetRxTotal: proto.Uint64(1000), NetTxTotal: proto.Uint64(5000)},
		{BootId: "b", NetRxTotal: proto.Uint64(1200), NetTxTotal: proto.Uint64(5001)},
	} {
		report := connect.NewRequest(&probev1.ReportRequest{Metrics: m})
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
func TestServeMountsPublicService(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hub.db")
	url, _, _ := startTestHub(t, db, clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	resp, err := http.Get(url + "/probe.v1.PublicService/GetSite?connect=v1&encoding=json&message=%7B%7D")
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
}
