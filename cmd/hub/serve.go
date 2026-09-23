package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/api"
	"github.com/xjetry/probe/internal/hub/auth"
	"github.com/xjetry/probe/internal/hub/ingest"
	"github.com/xjetry/probe/internal/hub/live"
	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/hub/traffic"
	"github.com/xjetry/probe/internal/hub/web"
)

type mount struct {
	path string
	h    http.Handler
}

func mountOf(path string, h http.Handler) mount { return mount{path: path, h: h} }

// newMux 是所有服务唯一的挂载点；挂载点级测试从注册表枚举方法逐个匿名调用，
// 所以任何进了描述符的服务都必须在这里出现，且带着它的鉴权拦截器。
func newMux(mounts ...mount) *http.ServeMux {
	mux := http.NewServeMux()
	for _, m := range mounts {
		mux.Handle(m.path, m.h)
	}
	return mux
}

func runServe(args []string) error {
	interrupt, terminate := make(chan os.Signal, 1), make(chan os.Signal, 1)
	signal.Notify(interrupt, syscall.SIGINT)
	signal.Notify(terminate, syscall.SIGTERM)
	defer signal.Stop(interrupt)
	defer signal.Stop(terminate)
	// Ctrl-C 保留人工强退入口；监管方重复 SIGTERM 仍须排空，不能绕过退出前落盘。
	// Stop 按通道恢复默认处置，所以两类信号必须分开订阅。
	shutdown, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case <-interrupt:
				signal.Stop(interrupt)
				cancel()
			case <-terminate:
				cancel()
			case <-done:
				return
			}
		}
	}()
	return runServeWith(shutdown, args, clock.Real(), slog.New(slog.NewTextHandler(os.Stderr, nil)))
}

// runServeWith 由调用方拥有停止信号；后台循环与请求排空完成后才能关闭它们共用的库。
func runServeWith(stopCtx context.Context, args []string, clk clock.Clock, log *slog.Logger) (result error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	db := fs.String("db", "probe.db", "SQLite database path")
	tz := fs.String("timezone", "", "IANA time zone for traffic period boundaries (default: the host's local zone)")
	listen := fs.String("listen", "127.0.0.1:8080", "listen address")
	proxies := fs.String("trusted-proxies", "", "comma-separated CIDRs whose X-Forwarded-For / X-Forwarded-Proto are trusted; empty trusts none")
	retention := store.DefaultRetention
	fs.DurationVar(&retention.M1, "retention-1m", retention.M1, fmt.Sprintf("how long to keep 1-minute rows (minimum %s)", store.MinRetentionM1))
	fs.DurationVar(&retention.M5, "retention-5m", retention.M5, fmt.Sprintf("how long to keep 5-minute rows (minimum %s)", store.MinRetentionM5))
	fs.DurationVar(&retention.H1, "retention-1h", retention.H1, fmt.Sprintf("how long to keep hourly rows (minimum %s)", store.MinRetentionH1))
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := retention.Validate(); err != nil {
		return err
	}
	loc, err := loadZone(*tz)
	if err != nil {
		return err
	}
	ttl, err := parseTTL(os.Getenv("PROBE_OFFLINE_AFTER"))
	if err != nil {
		return err
	}
	trusted, err := auth.ParsePrefixes(*proxies)
	if err != nil {
		return err
	}
	if !isLoopback(*listen) {
		log.Warn("listening on a non-loopback address: direct access bypasses the proxy; forwarded headers are trusted only from configured peers", "listen", *listen)
	}

	st, err := store.Open(*db, clk, log)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, st.Close()) }()
	a := auth.New(st, clk, log)
	l := live.New(clk, ttl)
	book := traffic.New(st, clk, loc, log)
	svc, err := ingest.New(ingest.Config{TTL: ttl, TrustedProxies: trusted}, l, st, a, book, clk, log)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if err := errors.Join(a.Load(ctx), svc.Load(ctx), book.Load(ctx)); err != nil {
		return err
	}
	admin := api.New(api.Config{ReportInterval: svc.Interval(), TrustedProxies: trusted}, st, a, l, svc, book, clk, log)

	mux := newMux(mountOf(svc.Handler()), mountOf(admin.Handler()), mountOf(web.Prefix, web.Handler()), mountOf("/", web.RootRedirect()))
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	drain := &drainingHandler{next: mux}
	srv := &http.Server{
		Addr: *listen, Handler: drain, ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, // ReadMaxBytes 限量不限时。
	}

	flushCtx, stopFlusher := context.WithCancel(ctx)
	flusherDone := make(chan struct{})
	go func() {
		defer close(flusherDone)
		svc.RunFlusher(flushCtx)
	}()
	maintCtx, stopMaintenance := context.WithCancel(ctx)
	maintenanceDone := make(chan struct{})
	go func() {
		defer close(maintenanceDone)
		st.RunMaintenance(maintCtx, retention)
	}()

	trafficCtx, stopTraffic := context.WithCancel(ctx)
	trafficDone := make(chan struct{})
	go func() {
		defer close(trafficDone)
		book.Run(trafficCtx)
	}()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(listener) }()
	log.Info("hub listening", "listen", listener.Addr().String(), "ttl", ttl, "interval", svc.Interval(), "retention_1m", retention.M1, "retention_5m", retention.M5, "retention_1h", retention.H1, "timezone", loc.String(), "version", version)

	stopBackground := func() {
		stopFlusher()
		stopMaintenance()
		stopTraffic()
		<-flusherDone
		<-maintenanceDone
		<-trafficDone
	}
	defer stopBackground()
	select {
	case <-stopCtx.Done():
		log.Info("shutting down")
	case err = <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	// HTTP 先停止准入并排空；超时则断开连接以中止慢请求体，仍等待已经进入的处理器。
	// 最后由 defer 依次停止后台循环、关库，避免晚到的上报落在最后一次刷出之后。
	return errors.Join(err, shutdownHTTP(srv, drain, 10*time.Second))
}

// drainingHandler 将请求准入与关闭裁决串行化，Wait 前封住 Add，
// 即使 http.Server 关闭连接后不再跟踪处理器，也不会提前刷出或关库。
type drainingHandler struct {
	next     http.Handler
	mu       sync.Mutex
	stopping bool
	active   sync.WaitGroup
}

func (d *drainingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	if d.stopping {
		d.mu.Unlock()
		http.Error(w, "server shutting down", http.StatusServiceUnavailable)
		return
	}
	d.active.Add(1)
	d.mu.Unlock()
	defer d.active.Done()
	d.next.ServeHTTP(w, r)
}

func shutdownHTTP(srv *http.Server, drain *drainingHandler, timeout time.Duration) error {
	drain.mu.Lock()
	drain.stopping = true
	drain.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := srv.Shutdown(ctx)
	if err != nil {
		err = errors.Join(err, srv.Close())
	}
	drain.active.Wait()
	return err
}
