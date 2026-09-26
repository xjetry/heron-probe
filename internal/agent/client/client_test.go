package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"connectrpc.com/connect"
	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/agent/collect"
	"github.com/xjetry/probe/internal/agent/prober"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/testwait"
)

func TestConfigRoundTripAndPermissions(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.json")
	if err := SaveConfig(p, Config{Hub: "http://h", Token: "t", Name: "n"}); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %o, want 600", st.Mode().Perm())
	}
	c, err := LoadConfig(p)
	if err != nil || c.Hub != "http://h" || c.Token != "t" || c.Name != "n" {
		t.Fatalf("%+v %v", c, err)
	}
}

// 落盘后的配置对其他用户不可读，即使残留的临时文件或旧配置本身权限更宽。
func TestSaveConfigEnforcesModeOverStaleFiles(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p+".tmp", []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfig(p, Config{Hub: "h", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %o, want 600: a stale world-readable temp file must not carry its mode onto the config", st.Mode().Perm())
	}
	c, err := LoadConfig(p)
	if err != nil || c.Token != "t" {
		t.Fatalf("config not replaced: %+v %v", c, err)
	}
}

func TestFactsHashIsStableAndSensitive(t *testing.T) {
	a := &probev1.Facts{Hostname: "h", CpuCores: 2}
	b := &probev1.Facts{Hostname: "h", CpuCores: 2}
	if FactsHash(a) != FactsHash(b) {
		t.Fatal("equal facts must hash equal")
	}
	b.CpuCores = 3
	if FactsHash(a) == FactsHash(b) {
		t.Fatal("different facts must hash differently")
	}
}

func TestBackoffGrowsAndCapsAtThreeIntervals(t *testing.T) {
	interval := 10 * time.Second
	one := func() float64 { return 1 }
	zero := func() float64 { return 0 }
	if d := Backoff(1, interval, one); d != interval {
		t.Fatalf("attempt 1 max = %v, want %v", d, interval)
	}
	if d := Backoff(2, interval, one); d != 2*interval {
		t.Fatalf("attempt 2 max = %v, want %v", d, 2*interval)
	}
	for attempt := 3; attempt < 40; attempt++ {
		if d := Backoff(attempt, interval, one); d != 3*interval {
			t.Fatalf("attempt %d max = %v, want cap %v", attempt, d, 3*interval)
		}
		if d := Backoff(attempt, interval, zero); d < 3*interval/2 {
			t.Fatalf("attempt %d min = %v, jitter must keep at least half the base", attempt, d)
		}
	}
}

// fakeHub 记录收到的上报并按脚本应答。
type fakeHub struct {
	reconcile  bool
	factsHash  uint64
	mu         sync.Mutex
	reports    []*probev1.ReportRequest
	wantNext   bool
	fail       bool
	interval   uint32
	tasks      *probev1.ProbeTasks
	probeError connect.Code
}

func (f *fakeHub) Register(context.Context, *connect.Request[probev1.RegisterRequest]) (*connect.Response[probev1.RegisterResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (f *fakeHub) Report(_ context.Context, req *connect.Request[probev1.ReportRequest]) (*connect.Response[probev1.ReportResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.Header().Get("Authorization") != "Bearer tok" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("no"))
	}
	if f.fail {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("down"))
	}
	f.reports = append(f.reports, req.Msg)
	if f.probeError != 0 {
		return nil, connect.NewError(f.probeError, errors.New("rejected report"))
	}
	want := f.wantNext
	if f.reconcile {
		if req.Msg.Facts != nil {
			f.factsHash = req.Msg.FactsHash
		}
		want = req.Msg.FactsHash != f.factsHash
	}
	f.wantNext = false
	return connect.NewResponse(&probev1.ReportResponse{ReportIntervalMs: f.interval, WantFacts: want, Tasks: f.tasks}), nil
}

func (f *fakeHub) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.reports) }

// Report 只追加记录，不修改已收到的请求；取消 runner 不保证服务端已处理完
// 在途请求，因此读者必须在 mu 下复制切片，不能直接读取仍可能被追加的 reports。
func (f *fakeHub) received() []*probev1.ReportRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*probev1.ReportRequest(nil), f.reports...)
}

func newRunner(t *testing.T, hub *fakeHub) (*Runner, chan time.Duration) {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(probev1connect.NewAgentServiceHandler(hub))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	sleeps := make(chan time.Duration, 100)
	r := &Runner{
		Collector: &collect.Collector{Host: &collect.ProcFS{FS: fstest.MapFS{"proc/loadavg": {Data: []byte("0 0 0 1/2 3\n")}}, DiskUsage: func(string) (uint64, uint64, error) { return 1, 1, nil }}, Clock: clock.NewFake(time.Unix(0, 0)), Version: "t"},
		Client:    probev1connect.NewAgentServiceClient(srv.Client(), srv.URL),
		Token:     "tok",
		Clock:     clock.NewFake(time.Unix(0, 0)),
		Sleep: func(ctx context.Context, d time.Duration) error {
			select {
			case sleeps <- d:
			default:
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				return nil
			}
		},
		Rand:     func() float64 { return 1 },
		Log:      slog.Default(),
		Interval: 10 * time.Second,
	}
	r.Results = prober.NewQueue(prober.QueueCap)
	r.Prober = prober.NewScheduler(quietEngine{}, r.Results, r.Clock, r.Log)
	t.Cleanup(r.Prober.Stop)
	return r, sleeps
}

// runFor 以 hub 侧计数为就绪信号，只适合断言 hub 已收到的内容。
// 断言 runner 侧事件（sleep）的测试必须直接在该事件上等待：hub 计数并不
// 保证 runner 已处理响应，取消可能让 Runner.Run 直接返回而不再产生该事件。
func runFor(t *testing.T, r *Runner, hub *fakeHub, reports int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	testwait.Until(t, time.Millisecond, func() bool { return hub.count() >= reports }, "got %s reports, want at least %d", testwait.When(func() string { return fmt.Sprint(hub.count()) }), reports)
	cancel()
	<-done
	if hub.count() < reports {
		t.Fatalf("got %d reports, want at least %d", hub.count(), reports)
	}
}

func TestFirstReportCarriesFactsThenOnlyOnRequest(t *testing.T) {
	hub := &fakeHub{interval: 5000}
	r, _ := newRunner(t, hub)
	hub.wantNext = false
	runFor(t, r, hub, 2)
	reports := hub.received()
	if reports[0].Facts == nil || reports[0].FactsHash == 0 {
		t.Fatal("first report must carry facts and their hash")
	}
	if reports[1].Facts != nil || reports[1].FactsHash != reports[0].FactsHash {
		t.Fatal("second report must carry only the hash")
	}
}

func TestFactsChangeIsReportedWithoutRestart(t *testing.T) {
	hub := &fakeHub{interval: 5000, reconcile: true}
	r, _ := newRunner(t, hub)
	fs := r.Collector.Host.(*collect.ProcFS).FS.(fstest.MapFS)
	fs["proc/sys/kernel/hostname"] = &fstest.MapFile{Data: []byte("old\n")}
	round := 0
	r.Sleep = func(context.Context, time.Duration) error {
		round++
		if round == 1 {
			fs["proc/sys/kernel/hostname"] = &fstest.MapFile{Data: []byte("new\n")}
		}
		if round == 3 {
			return context.Canceled
		}
		return nil
	}
	if err := r.Run(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	reports := hub.received()
	if reports[0].Facts == nil {
		t.Fatal("first report missing facts")
	}
	if reports[1].FactsHash == reports[0].FactsHash {
		t.Fatal("facts hash did not change after hostname changed")
	}
	if f := reports[2].Facts; f == nil || f.Hostname != "new" {
		t.Fatalf("requested facts = %+v, want new hostname", f)
	}
}

func TestWantFactsTriggersResend(t *testing.T) {
	hub := &fakeHub{interval: 5000}
	r, _ := newRunner(t, hub)
	hub.mu.Lock()
	hub.wantNext = true // 首次响应就要求 facts → 第二次上报再次携带
	hub.mu.Unlock()
	runFor(t, r, hub, 2)
	reports := hub.received()
	if reports[1].Facts == nil {
		t.Fatal("want_facts must make the next report carry facts")
	}
}

func TestAdoptsIntervalFromResponse(t *testing.T) {
	hub := &fakeHub{interval: 7000}
	r, sleeps := newRunner(t, hub)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	select {
	case d := <-sleeps:
		if d != 7*time.Second {
			t.Fatalf("slept %v after first response, want the assigned 7s", d)
		}
	case <-time.After(testwait.Bound):
		t.Fatal("runner never slept after the first response")
	}
}

func TestFailureBacksOffWithinThreeIntervals(t *testing.T) {
	hub := &fakeHub{interval: 5000, fail: true}
	r, sleeps := newRunner(t, hub)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	var seen []time.Duration
	for len(seen) < 6 {
		select {
		case d := <-sleeps:
			seen = append(seen, d)
		case <-time.After(testwait.Bound):
			t.Fatal("timed out waiting for backoff sleep")
		}
	}
	cancel()
	<-done
	if seen[0] != 10*time.Second || seen[1] != 20*time.Second {
		t.Fatalf("first two backoffs = %v, want 10s then 20s", seen[:2])
	}
	for _, d := range seen[2:] {
		if d != 30*time.Second {
			t.Fatalf("backoff %v exceeds cap 3×interval", d)
		}
	}
}
