package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
}

func newHub(t *testing.T) *hub {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), clk, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a := auth.New(st, clk, slog.Default())
	l := live.New(clk, 30*time.Second)
	svc := New(Config{TTL: 30 * time.Second}, l, st, a, clk, slog.Default())
	if err := svc.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(svc.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &hub{svc: svc, srv: srv, client: probev1connect.NewAgentServiceClient(srv.Client(), srv.URL), clk: clk, store: st, auth: a, live: l}
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
	req.Msg.Facts = &probev1.Facts{Hostname: "a\x00b\x1fc\x7fd", Os: strings.Repeat("x", 1000)}
	req.Msg.FactsHash = 1
	if _, err := h.client.Report(ctx, req); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { m, _ := h.store.FactsHashes(ctx); return m[id] == 1 })
	var hostname, os string
	if err := h.store.QueryFacts(ctx, id, &hostname, &os); err != nil {
		t.Fatal(err)
	}
	if hostname != "abcd" || len(os) != maxFactString {
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
	h.clk.Advance(h.svc.Interval() / 2) // 2× 速率 = 每半个间隔补一个令牌
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
	if err == nil {
		t.Fatal("70 KiB body must be rejected")
	}
	if _, ok := h.live.Get(id); ok {
		t.Fatal("rejected body must leave live untouched")
	}
}

func TestRegisterThenReport(t *testing.T) {
	h := newHub(t)
	ctx := context.Background()
	_, err := h.client.Register(ctx, connect.NewRequest(&probev1.RegisterRequest{Key: "nope", Name: "x"}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("closed window: err = %v", err)
	}
	key, _ := h.auth.OpenWindow(ctx, time.Hour, 1)
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
