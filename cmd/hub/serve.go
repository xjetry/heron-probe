package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"connectrpc.com/connect"

	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/auth"
	"github.com/xjetry/probe/internal/hub/ingest"
	"github.com/xjetry/probe/internal/hub/live"
	"github.com/xjetry/probe/internal/hub/store"
)

const maxAdminBody = 64 << 10

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

// denyAll 在挂载处阻止请求进入尚无实现的服务；不设方法白名单，
// 因而描述符新增的方法也会被拒绝，不依赖零实现自身返回什么错误。
func denyAll() connect.Interceptor { return denyAllInterceptor{} }

type denyAllInterceptor struct{}

func (denyAllInterceptor) WrapUnary(connect.UnaryFunc) connect.UnaryFunc {
	return func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("unauthenticated"))
	}
}

func (denyAllInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (denyAllInterceptor) WrapStreamingHandler(connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(context.Context, connect.StreamingHandlerConn) error {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("unauthenticated"))
	}
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	db := fs.String("db", "probe.db", "SQLite database path")
	listen := fs.String("listen", "127.0.0.1:8080", "listen address")
	proxies := fs.String("trusted-proxies", "", "comma-separated CIDRs whose X-Forwarded-For is trusted; empty trusts none")
	if err := fs.Parse(args); err != nil {
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
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if !isLoopback(*listen) {
		log.Warn("listening on a non-loopback address: anyone reaching it directly can forge forwarded headers", "listen", *listen)
	}

	clk := clock.Real()
	st, err := store.Open(*db, clk, log)
	if err != nil {
		return err
	}
	a := auth.New(st, clk, log)
	l := live.New(clk, ttl)
	svc, err := ingest.New(ingest.Config{TTL: ttl, TrustedProxies: trusted}, l, st, a, clk, log)
	if err != nil {
		st.Close()
		return err
	}
	ctx := context.Background()
	if err := errors.Join(a.Load(ctx), svc.Load(ctx)); err != nil {
		st.Close()
		return err
	}

	adminPath, adminHandler := probev1connect.NewAdminServiceHandler(probev1connect.UnimplementedAdminServiceHandler{},
		connect.WithInterceptors(denyAll()), connect.WithReadMaxBytes(maxAdminBody))
	mux := newMux(mountOf(svc.Handler()), mountOf(adminPath, adminHandler))
	srv := &http.Server{
		Addr: *listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, // ReadMaxBytes 限量不限时。
	}

	flushCtx, stopFlusher := context.WithCancel(ctx)
	flusherDone := make(chan struct{})
	go func() {
		defer close(flusherDone)
		svc.RunFlusher(flushCtx)
	}()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	log.Info("hub listening", "listen", *listen, "ttl", ttl, "interval", svc.Interval(), "version", version)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		log.Info("shutting down", "signal", s)
	case err := <-errCh:
		stopFlusher()
		<-flusherDone
		st.Close()
		return err
	}
	// 关闭顺序：先停接收新上报，再把内存里的桶全部刷出，最后关库。
	// 反过来会把退出前最后一分钟的数据丢在内存里。
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("HTTP shutdown failed", "err", err)
	}
	stopFlusher()
	<-flusherDone
	return st.Close()
}
