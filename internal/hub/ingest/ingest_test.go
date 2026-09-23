package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/auth"
	"github.com/xjetry/probe/internal/hub/live"
	"github.com/xjetry/probe/internal/hub/metric"
	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/hub/traffic"
	"google.golang.org/protobuf/proto"
)

type hub struct {
	svc    *Service
	srv    *httptest.Server
	client probev1connect.AgentServiceClient
	clk    *clock.Fake
	store  *store.Store
	auth   *auth.Auth
	live   *live.Live
	book   *traffic.Book
}

func newHub(t *testing.T) *hub { return newHubAt(t, filepath.Join(t.TempDir(), "t.db")) }

// newHubAt 在给定库文件上起一套 hub；同一路径起两次即模拟 hub 重启。
func newHubAt(t *testing.T, path string) *hub {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := store.Open(path, clk, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a := auth.New(st, clk, slog.Default())
	l := live.New(clk, 30*time.Second)
	book := traffic.New(st, clk, time.UTC, slog.Default())
	svc, err := New(Config{TTL: 30 * time.Second}, l, st, a, book, clk, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := errors.Join(a.Load(ctx), svc.Load(ctx), book.Load(ctx)); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(svc.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &hub{svc: svc, srv: srv, client: probev1connect.NewAgentServiceClient(srv.Client(), srv.URL), clk: clk, store: st, auth: a, live: l, book: book}
}

func (h *hub) node(t *testing.T) (int64, string) {
	t.Helper()
	id, tok, err := h.auth.CreateNode(context.Background(), "n")
	if err != nil {
		t.Fatal(err)
	}
	return id, tok
}

func report(tok string, m *probev1.Metrics) *connect.Request[probev1.ReportRequest] {
	req := connect.NewRequest(&probev1.ReportRequest{Metrics: m})
	if tok != "" {
		req.Header().Set("Authorization", "Bearer "+tok)
	}
	return req
}

func TestNewEnforcesMinimumTTL(t *testing.T) {
	for _, ttl := range []time.Duration{-time.Second, 0, 10*time.Second - time.Nanosecond, 10 * time.Second, 30 * time.Second} {
		svc, err := New(Config{TTL: ttl}, nil, nil, nil, nil, clock.NewFake(time.Now()), slog.Default())
		if ttl < 10*time.Second {
			if err == nil || svc != nil {
				t.Fatalf("TTL %v accepted below minimum", ttl)
			}
		} else if err != nil || svc == nil || svc.Interval() != ttl/3 {
			t.Fatalf("TTL %v rejected or wrong interval: %v %v", ttl, svc, err)
		}
	}
}

func TestReportWithoutTokenIsUnauthenticated(t *testing.T) {
	h := newHub(t)
	_, err := h.client.Report(context.Background(), report("", &probev1.Metrics{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("err = %v", err)
	}
	_, err = h.client.Report(context.Background(), report("bogus", &probev1.Metrics{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("err = %v", err)
	}
}

// 从服务描述符枚举全部 RPC，逐个无凭据调用：新增方法自动入测，不可能漏掉鉴权。
func TestEveryProcedureRejectsAnonymousCalls(t *testing.T) {
	h := newHub(t)
	services := probev1.File_probe_v1_agent_proto.Services()
	count := 0
	for i := 0; i < services.Len(); i++ {
		svc := services.Get(i)
		methods := svc.Methods()
		for j := 0; j < methods.Len(); j++ {
			count++
			path := "/" + string(svc.FullName()) + "/" + string(methods.Get(j).Name())
			resp, err := http.Post(h.srv.URL+path, "application/json", strings.NewReader("{}"))
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				Code string `json:"code"`
			}
			json.NewDecoder(resp.Body).Decode(&body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized || body.Code != "unauthenticated" {
				t.Fatalf("%s: status %d code %q, want 401 unauthenticated", path, resp.StatusCode, body.Code)
			}
		}
	}
	if count == 0 {
		t.Fatal("enumerated no procedures; the descriptor lookup is wrong")
	}
}

func TestReportUpdatesLiveAndReturnsInterval(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	resp, err := h.client.Report(context.Background(), report(tok, &probev1.Metrics{CpuPct: proto.Float64(12)}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.ReportIntervalMs != 10_000 {
		t.Fatalf("interval = %d ms, want TTL/3 = 10000", resp.Msg.ReportIntervalMs)
	}
	e, ok := h.live.Get(id)
	if !ok || !e.Online || e.Metrics.GetCpuPct() != 12 {
		t.Fatalf("live = %+v ok=%v", e, ok)
	}
}

func TestInvalidMetricsRejectedWholeWithoutSideEffect(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	cases := map[string]*probev1.Metrics{
		"nan":          {CpuPct: proto.Float64(math.NaN())},
		"inf":          {Load1: proto.Float64(math.Inf(1)), Load5: proto.Float64(0), Load15: proto.Float64(0)},
		"negative":     {CpuPct: proto.Float64(-1)},
		"pct over 100": {CpuPct: proto.Float64(100.5)},
		"partial load": {Load1: proto.Float64(1)},
		"nil metrics":  nil,
	}
	for name, m := range cases {
		h.clk.Advance(h.svc.Interval())
		_, err := h.client.Report(context.Background(), report(tok, m))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: err = %v, want InvalidArgument", name, err)
		}
	}
	if _, ok := h.live.Get(id); ok {
		t.Fatal("a rejected report must leave live untouched")
	}
}

func TestFactsAreStoredAndReconciledByHash(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	ctx := context.Background()
	req := report(tok, &probev1.Metrics{})
	req.Msg.Facts = &probev1.Facts{Hostname: "box", CpuCores: 2}
	req.Msg.FactsHash = 41
	resp, err := h.client.Report(ctx, req)
	if err != nil || resp.Msg.WantFacts {
		t.Fatalf("resp = %+v err = %v", resp, err)
	}
	waitFor(t, func() bool { m, _ := h.store.FactsHashes(ctx); return m[id] == 41 })

	h.clk.Advance(10 * time.Second)
	req = report(tok, &probev1.Metrics{})
	req.Msg.FactsHash = 41
	resp, _ = h.client.Report(ctx, req)
	if resp.Msg.WantFacts {
		t.Fatal("same hash must not ask for facts")
	}
	h.clk.Advance(10 * time.Second)
	req = report(tok, &probev1.Metrics{})
	req.Msg.FactsHash = 42
	resp, _ = h.client.Report(ctx, req)
	if !resp.Msg.WantFacts {
		t.Fatal("changed hash must ask for facts")
	}
}

func TestFactsStringsAreSanitized(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	ctx := context.Background()
	req := report(tok, &probev1.Metrics{})
	req.Msg.Facts = &probev1.Facts{Hostname: "a\x00b\x1fc\x7fd\u0085\u009b\u202e\u200c", Os: strings.Repeat("x", 1000)}
	req.Msg.FactsHash = 1
	if _, err := h.client.Report(ctx, req); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { m, _ := h.store.FactsHashes(ctx); return m[id] == 1 })
	var hostname, os string
	if err := h.store.QueryFacts(ctx, id, &hostname, &os); err != nil {
		t.Fatal(err)
	}
	if hostname != "abcd\u200c" || len(os) != maxFactString {
		t.Fatalf("hostname %q os len %d", hostname, len(os))
	}
}

func TestRateLimitIsTwiceTheReportRate(t *testing.T) {
	h := newHub(t)
	_, tok := h.node(t)
	ctx := context.Background()
	var last error
	for i := 0; i < burst+1; i++ {
		_, last = h.client.Report(ctx, report(tok, &probev1.Metrics{}))
	}
	if connect.CodeOf(last) != connect.CodeResourceExhausted {
		t.Fatalf("burst+1 immediate reports: err = %v, want ResourceExhausted", last)
	}
	h.clk.Advance(h.svc.Interval()/2 - time.Millisecond)
	if _, err := h.client.Report(ctx, report(tok, &probev1.Metrics{})); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("before half interval: err = %v, want ResourceExhausted", err)
	}
	h.clk.Advance(time.Millisecond) // 2× 速率 = 每半个间隔补一个令牌
	if _, err := h.client.Report(ctx, report(tok, &probev1.Metrics{})); err != nil {
		t.Fatalf("after refill: %v", err)
	}
}

// connect v1.21.0 实测：71690 字节消息超过 65536 字节上限时返回 ResourceExhausted。
func TestOversizedBodyIsRejected(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	req := report(tok, &probev1.Metrics{})
	req.Msg.Facts = &probev1.Facts{Os: strings.Repeat("x", 70*1024)}
	_, err := h.client.Report(context.Background(), req)
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("70 KiB body: err = %v, want ResourceExhausted", err)
	}
	if _, ok := h.live.Get(id); ok {
		t.Fatal("rejected body must leave live untouched")
	}
}

func TestRegisterIsRateLimitedPerSourceAddress(t *testing.T) {
	h := newHub(t)
	h.svc.cfg.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	call := func(ip string) connect.Code {
		req := connect.NewRequest(&probev1.RegisterRequest{Key: "wrong", Name: "n"})
		req.Header().Set("X-Forwarded-For", ip)
		_, err := h.client.Register(context.Background(), req)
		return connect.CodeOf(err)
	}
	for i := 1; i <= 30; i++ {
		if code := call("203.0.113.1"); code != connect.CodeUnauthenticated {
			t.Fatalf("attempt %d: %v, want Unauthenticated", i, code)
		}
	}
	if code := call("203.0.113.1"); code != connect.CodeResourceExhausted {
		t.Fatalf("attempt 31: %v, want ResourceExhausted before window decision", code)
	}
	if code := call("203.0.113.2"); code != connect.CodeUnauthenticated {
		t.Fatalf("second source: %v, want Unauthenticated", code)
	}
	h.clk.Advance(time.Second)
	if code := call("203.0.113.1"); code != connect.CodeUnauthenticated {
		t.Fatalf("after one-second refill: %v, want Unauthenticated", code)
	}
}

func TestBucketsSweepIdleKeys(t *testing.T) {
	b := newBuckets[string](3)
	per := time.Second
	b.allow("a", 0, per)
	b.allow("b", 0, per)
	b.allow("c", 3*per, per)
	if len(b.m) != 1 || b.m["c"] == nil {
		t.Fatalf("idle keys not swept: %+v", b.m)
	}
	b.allow("active", 3*per, per)
	active := b.m["active"]
	now := 6*per - time.Millisecond
	b.allow("active", now, per)
	if b.lastSweep != 3*per || len(b.m) != 2 || b.m["active"] != active {
		t.Fatal("swept before period or replaced active bucket")
	}
	b.allow("d", 6*per, per)
	if len(b.m) != 2 || b.m["active"] != active || b.m["d"] == nil {
		t.Fatalf("sweep removed active key or retained idle key: %+v", b.m)
	}
}

func TestRegisterThenReport(t *testing.T) {
	h := newHub(t)
	ctx := context.Background()
	_, err := h.client.Register(ctx, connect.NewRequest(&probev1.RegisterRequest{Key: "nope", Name: "x"}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("closed window: err = %v", err)
	}
	key, _, _ := h.auth.OpenWindow(ctx, time.Hour, 1)
	resp, err := h.client.Register(ctx, connect.NewRequest(&probev1.RegisterRequest{Key: key, Name: "x"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.Report(ctx, report(resp.Msg.Token, &probev1.Metrics{})); err != nil {
		t.Fatalf("token from Register must work: %v", err)
	}
	if !h.live.Online(resp.Msg.NodeId) {
		t.Fatal("node not online after report")
	}
}

func TestFlushWritesClosedBucketsOnly(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	ctx := context.Background()
	h.client.Report(ctx, report(tok, &probev1.Metrics{CpuPct: proto.Float64(10)}))
	h.svc.Flush(ctx, false)
	if rows, _ := h.store.ReadMinuteRows(ctx, id, 0, math.MaxInt64); len(rows) != 0 {
		t.Fatalf("open bucket was written: %+v", rows)
	}
	h.clk.Advance(61 * time.Second)
	h.svc.Flush(ctx, false)
	rows, _ := h.store.ReadMinuteRows(ctx, id, 0, math.MaxInt64)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want 1", rows)
	}
	if mean, _ := rows[0].Bucket.Mean(0); mean != 10 {
		t.Fatalf("mean = %v", mean)
	}
}

type failingWriter struct {
	*store.Store
	fail      bool
	failFacts bool
}

func (f *failingWriter) UpsertFactsAsync(nodeID int64, hash uint64, facts *probev1.Facts, done func(error)) {
	if f.failFacts {
		done(errors.New("disk on fire"))
		return
	}
	f.Store.UpsertFactsAsync(nodeID, hash, facts, done)
}

func (f *failingWriter) WriteMinuteRows(ctx context.Context, rows []metric.Row) (int, error) {
	if f.fail {
		return 0, errors.New("disk on fire")
	}
	return f.Store.WriteMinuteRows(ctx, rows)
}

func TestFlushRetriesFailedBatchesLater(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	ctx := context.Background()
	fw := &failingWriter{Store: h.store, fail: true}
	h.svc.writer = fw
	h.client.Report(ctx, report(tok, &probev1.Metrics{CpuPct: proto.Float64(10)}))
	h.clk.Advance(61 * time.Second)
	h.svc.Flush(ctx, false)
	if rows, _ := h.store.ReadMinuteRows(ctx, id, 0, math.MaxInt64); len(rows) != 0 {
		t.Fatal("write should have failed")
	}
	fw.fail = false
	h.svc.Flush(ctx, false)
	if rows, _ := h.store.ReadMinuteRows(ctx, id, 0, math.MaxInt64); len(rows) != 1 {
		t.Fatalf("retried batch not written: %+v", rows)
	}
}

func TestDrainOnShutdownWritesOpenBucket(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	ctx := context.Background()
	h.client.Report(ctx, report(tok, &probev1.Metrics{CpuPct: proto.Float64(10)}))
	h.svc.Flush(ctx, true)
	if rows, _ := h.store.ReadMinuteRows(ctx, id, 0, math.MaxInt64); len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// 摘要只在写库成功后记下：写失败时下一次上报必须再次索要 facts，直到落库成功。
func TestFactsHashNotRecordedWhenWriteFails(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	ctx := context.Background()
	fw := &failingWriter{Store: h.store, failFacts: true}
	h.svc.writer = fw
	req := report(tok, &probev1.Metrics{})
	req.Msg.Facts = &probev1.Facts{Hostname: "box"}
	req.Msg.FactsHash = 41
	if _, err := h.client.Report(ctx, req); err != nil {
		t.Fatal(err)
	}
	h.clk.Advance(h.svc.Interval())
	req = report(tok, &probev1.Metrics{})
	req.Msg.FactsHash = 41
	resp, err := h.client.Report(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Msg.WantFacts {
		t.Fatal("facts write failed, yet the hub stopped asking: the hash was recorded before the write succeeded")
	}
	fw.failFacts = false
	h.clk.Advance(h.svc.Interval())
	req = report(tok, &probev1.Metrics{})
	req.Msg.Facts = &probev1.Facts{Hostname: "box"}
	req.Msg.FactsHash = 41
	if _, err := h.client.Report(ctx, req); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { m, _ := h.store.FactsHashes(ctx); return m[id] == 41 })
	h.clk.Advance(h.svc.Interval())
	req = report(tok, &probev1.Metrics{})
	req.Msg.FactsHash = 41
	resp, err = h.client.Report(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.WantFacts {
		t.Fatal("facts are persisted now; the hub must stop asking")
	}
}

// 没有在拦截器里显式列出凭据来源的方法一律拒绝，且不会进入处理函数。
func TestInterceptorDeniesStreamingHandlers(t *testing.T) {
	h := newHub(t)
	called := false
	next := connect.StreamingHandlerFunc(func(context.Context, connect.StreamingHandlerConn) error { called = true; return nil })
	err := h.svc.authInterceptor().WrapStreamingHandler(next)(context.Background(), nil)
	if connect.CodeOf(err) != connect.CodeUnauthenticated || called {
		t.Fatalf("streaming handler: err=%v called=%v, want Unauthenticated without dispatch", err, called)
	}
}

func TestInterceptorPassesStreamingClientsThrough(t *testing.T) {
	h := newHub(t)
	called := false
	next := connect.StreamingClientFunc(func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn { called = true; return nil })
	h.svc.authInterceptor().WrapStreamingClient(next)(context.Background(), connect.Spec{})
	if !called {
		t.Fatal("streaming client was not passed through")
	}
}

// 真实 Connect 挂载负责填入 Procedure 与 Peer；超限请求不能进入处理函数，
// 因为处理函数的任何语句都可能触碰注册裁决的写协程。
func TestInterceptorRateLimitsAnonymousRegisterBeforeDispatch(t *testing.T) {
	h := newHub(t)
	var called atomic.Int32
	handler := connect.NewUnaryHandler(probev1connect.AgentServiceRegisterProcedure,
		func(ctx context.Context, req *connect.Request[probev1.RegisterRequest]) (*connect.Response[probev1.RegisterResponse], error) {
			called.Add(1)
			return connect.NewResponse(&probev1.RegisterResponse{NodeId: 77, Token: "accepted"}), nil
		}, connect.WithInterceptors(h.svc.authInterceptor()))
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := probev1connect.NewAgentServiceClient(srv.Client(), srv.URL)
	for attempt := 1; attempt <= 30; attempt++ {
		resp, err := client.Register(context.Background(), connect.NewRequest(&probev1.RegisterRequest{}))
		if got := called.Load(); got != int32(attempt) {
			t.Fatalf("attempt %d: called = %d, want %d", attempt, got, attempt)
		}
		if err != nil {
			t.Fatal(err)
		}
		if resp.Msg.GetNodeId() != 77 || resp.Msg.GetToken() != "accepted" {
			t.Fatalf("attempt %d: handler result changed: %v", attempt, resp.Msg)
		}
	}
	_, err := client.Register(context.Background(), connect.NewRequest(&probev1.RegisterRequest{}))
	if got := called.Load(); got != 30 {
		t.Fatalf("attempt 31: called = %d, want 30: rate-limited request must not enter handler", got)
	}
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("attempt 31: err = %v, want ResourceExhausted", err)
	}
}

func TestInterceptorDeniesUnlistedProcedures(t *testing.T) {
	h := newHub(t)
	called := false
	next := connect.UnaryFunc(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		called = true
		return nil, nil
	})
	req := connect.NewRequest(&probev1.ReportRequest{}) // 未经客户端发送，Spec().Procedure 为空
	req.Header().Set("Authorization", "Bearer whatever")
	_, err := h.svc.authInterceptor().WrapUnary(next)(context.Background(), req)
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("err = %v, want Unauthenticated", err)
	}
	if called {
		t.Fatal("handler must not run for an unlisted procedure")
	}
}

func TestForgetClearsNodeState(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	req := report(tok, &probev1.Metrics{CpuPct: proto.Float64(1)})
	req.Msg.Facts = &probev1.Facts{Hostname: "h"}
	req.Msg.FactsHash = 5
	if _, err := h.client.Report(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { h.svc.mu.Lock(); defer h.svc.mu.Unlock(); return h.svc.factsHash[id] == 5 })
	h.svc.Forget(id)
	if _, ok := h.live.Get(id); ok {
		t.Fatal("live entry survived Forget")
	}
	h.svc.limit.mu.Lock()
	_, limited := h.svc.limit.m[id]
	h.svc.limit.mu.Unlock()
	if limited {
		t.Fatal("rate limit bucket survived Forget")
	}
	h.svc.mu.Lock()
	_, known := h.svc.factsHash[id]
	h.svc.mu.Unlock()
	if known {
		t.Fatal("facts hash survived Forget")
	}
}

type delayedFactsCallback struct {
	*store.Store
	callbacks chan func()
}

func (w *delayedFactsCallback) UpsertFactsAsync(id int64, hash uint64, f *probev1.Facts, done func(error)) {
	w.Store.UpsertFactsAsync(id, hash, f, func(err error) { w.callbacks <- func() { done(err) } })
}

func TestForgetDiscardsDelayedFactsCallback(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	w := &delayedFactsCallback{Store: h.store, callbacks: make(chan func(), 1)}
	h.svc.writer = w
	req := report(tok, &probev1.Metrics{})
	req.Msg.Facts = &probev1.Facts{Hostname: "h"}
	req.Msg.FactsHash = 9
	if _, err := h.client.Report(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	callback := <-w.callbacks
	if err := h.auth.DeleteNode(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	h.svc.Forget(id)
	callback()
	h.svc.mu.Lock()
	_, known := h.svc.factsHash[id]
	h.svc.mu.Unlock()
	if known {
		t.Fatal("late callback rebuilt deleted facts hash")
	}
}

func TestForgetDropsPendingRowsWithoutLosingOtherNodes(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	keep, other := h.node(t)
	fw := &failingWriter{Store: h.store, fail: true}
	h.svc.writer = fw
	for _, token := range []string{tok, other} {
		if _, err := h.client.Report(context.Background(), report(token, &probev1.Metrics{CpuPct: proto.Float64(1)})); err != nil {
			t.Fatal(err)
		}
	}
	h.svc.Flush(context.Background(), true)
	if err := h.auth.DeleteNode(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	h.svc.Forget(id)
	h.svc.pendingMu.Lock()
	forgotten, retained := 0, 0
	for _, batch := range h.svc.pending {
		for _, row := range batch {
			if row.NodeID == id {
				forgotten++
			}
			if row.NodeID == keep {
				retained++
			}
		}
	}
	h.svc.pendingMu.Unlock()
	if forgotten != 0 || retained != 1 {
		t.Fatalf("pending rows after Forget: deleted=%d other=%d", forgotten, retained)
	}
}

type reportGateClock struct {
	clock.Clock
	entered chan struct{}
	release chan struct{}
	armed   atomic.Bool
}

func (c *reportGateClock) Mono() time.Duration {
	if c.armed.Swap(false) {
		close(c.entered)
		<-c.release
	}
	return c.Clock.Mono()
}

func TestForgetWaitsForAdmittedReport(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	gate := &reportGateClock{Clock: h.clk, entered: make(chan struct{}), release: make(chan struct{})}
	h.svc.clk = gate
	gate.armed.Store(true)
	reported := make(chan error, 1)
	go func() {
		_, err := h.client.Report(context.Background(), report(tok, &probev1.Metrics{}))
		reported <- err
	}()
	select {
	case <-gate.entered:
	case <-time.After(2 * time.Second):
		close(gate.release)
		t.Fatal("report did not reach admission gate")
	}
	if err := h.auth.DeleteNode(context.Background(), id); err != nil {
		close(gate.release)
		t.Fatal(err)
	}
	forgotten := make(chan struct{})
	go func() { h.svc.Forget(id); close(forgotten) }()
	select {
	case <-forgotten:
		t.Error("Forget returned before in-flight report completed")
	case <-time.After(50 * time.Millisecond):
	}
	close(gate.release)
	if err := <-reported; err != nil {
		t.Fatal(err)
	}
	<-forgotten
	if _, ok := h.live.Get(id); ok {
		t.Error("in-flight report rebuilt live state")
	}
	h.svc.limit.mu.Lock()
	_, limited := h.svc.limit.m[id]
	h.svc.limit.mu.Unlock()
	if limited {
		t.Error("in-flight report rebuilt rate limit")
	}
}

func TestRegisterTrimsAfterRemovingControls(t *testing.T) {
	h := newHub(t)
	ctx := context.Background()
	key, _, err := h.auth.OpenWindow(ctx, time.Hour, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ raw, want string }{{"  host \x00 ", "host"}, {"\x00 \u202e", "node"}} {
		out, err := h.client.Register(ctx, connect.NewRequest(&probev1.RegisterRequest{Key: key, Name: tc.raw}))
		if err != nil {
			t.Fatal(err)
		}
		n, err := h.store.GetNode(ctx, out.Msg.NodeId)
		if err != nil {
			t.Fatal(err)
		}
		if n.Name != tc.want {
			t.Errorf("registered name=%q want=%q", n.Name, tc.want)
		}
	}
}

func netCounters(boot string, rx, tx uint64) *probev1.Metrics {
	return &probev1.Metrics{BootId: boot, NetRxTotal: proto.Uint64(rx), NetTxTotal: proto.Uint64(tx)}
}

func (h *hub) mustReport(t *testing.T, tok string, m *probev1.Metrics) {
	t.Helper()
	if _, err := h.client.Report(context.Background(), report(tok, m)); err != nil {
		t.Fatal(err)
	}
}

func rxAccounted(rows []metric.Row) (sum float64, n uint32) {
	for _, r := range rows {
		sum += r.Bucket.Sum[metric.RxBytes]
		n += r.Bucket.N[metric.RxBytes]
	}
	return sum, n
}

func TestReportAccountsTrafficIntoTotalsAndMinuteBucket(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	h.mustReport(t, tok, netCounters("b", 1000, 1000))
	h.clk.Advance(10 * time.Second)
	h.mustReport(t, tok, netCounters("b", 1100, 1300))
	e, ok := h.book.Get(id)
	if !ok || e.TotalRx != 100 || e.TotalTx != 300 || e.PeriodRx != 100 {
		t.Fatalf("book after two reports: %+v %v", e, ok)
	}
	rows := h.live.Drain()
	if sum, n := rxAccounted(rows); sum != 100 || n != 1 {
		t.Fatalf("minute bucket rx = %v/%d, want 100/1", sum, n)
	}
	if rows[0].Bucket.Sum[metric.TxBytes] != 300 {
		t.Fatalf("minute bucket tx = %v, want 300", rows[0].Bucket.Sum[metric.TxBytes])
	}
}

func TestGapBeyondTTLKeepsTotalsButSkipsTheBucket(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	h.mustReport(t, tok, netCounters("b", 1000, 1000))
	h.clk.Advance(31 * time.Second) // TTL 30s：这段增量跨了不止一个上报周期
	h.mustReport(t, tok, netCounters("b", 1500, 1000))
	if e, _ := h.book.Get(id); e.TotalRx != 500 {
		t.Fatalf("totals must still take the increment: %+v", e)
	}
	if sum, n := rxAccounted(h.live.Drain()); n != 0 {
		t.Fatalf("increment across a gap longer than TTL landed in a minute bucket: %v/%d", sum, n)
	}

	h.clk.Advance(29 * time.Second)
	h.mustReport(t, tok, netCounters("b", 1600, 1000))
	if sum, n := rxAccounted(h.live.Drain()); sum != 100 || n != 1 {
		t.Fatalf("increment at TTL-1s must enter the bucket: %v/%d, want 100/1", sum, n)
	}
	h.clk.Advance(30 * time.Second)
	h.mustReport(t, tok, netCounters("b", 1700, 1000))
	if sum, n := rxAccounted(h.live.Drain()); sum != 0 || n != 0 {
		t.Fatalf("increment at exactly TTL landed in a minute bucket: %v/%d", sum, n)
	}
}

func TestFirstReportAfterRestartSkipsTheBucketButKeepsTotals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	h := newHubAt(t, path)
	id, tok := h.node(t)
	h.mustReport(t, tok, netCounters("b", 1000, 1000))
	h.clk.Advance(10 * time.Second)
	h.mustReport(t, tok, netCounters("b", 1200, 1000))
	if err := h.book.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}

	h2 := newHubAt(t, path) // 同一库：token 与基线都从库恢复
	h2.mustReport(t, tok, netCounters("b", 1300, 1000))
	if e, _ := h2.book.Get(id); e.TotalRx != 300 {
		t.Fatalf("restart lost the persisted baseline or totals: %+v", e)
	}
	if sum, n := rxAccounted(h2.live.Drain()); n != 0 {
		t.Fatalf("first report after restart landed in a minute bucket: %v/%d", sum, n)
	}
	h2.clk.Advance(10 * time.Second)
	h2.mustReport(t, tok, netCounters("b", 1310, 1000))
	if sum, n := rxAccounted(h2.live.Drain()); sum != 10 || n != 1 {
		t.Fatalf("second report after restart must resume bucketing: %v/%d", sum, n)
	}
}

func TestForgetDropsTrafficState(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	h.mustReport(t, tok, netCounters("b", 1000, 1000))
	h.svc.Forget(id)
	if _, ok := h.book.Get(id); ok {
		t.Fatal("traffic entry survived Forget")
	}
}
