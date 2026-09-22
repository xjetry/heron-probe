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

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/auth"
	"github.com/xjetry/probe/internal/hub/ingest"
	"github.com/xjetry/probe/internal/hub/live"
	"github.com/xjetry/probe/internal/hub/store"
)

func newMux(svc *ingest.Service) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle(svc.Handler())
	return mux
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	db := fs.String("db", "probe.db", "SQLite database path")
	listen := fs.String("listen", "127.0.0.1:8080", "listen address")
	proxies := fs.String("trusted-proxies", "", "comma-separated CIDRs whose X-Forwarded-For is trusted; empty trusts none")
	fs.String("site-url", "", "public URL of this hub, used in generated install commands")
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

	mux := newMux(svc)
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
