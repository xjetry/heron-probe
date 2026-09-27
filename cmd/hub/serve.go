package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/alert"
	"github.com/xjetry/probe/internal/hub/api"
	"github.com/xjetry/probe/internal/hub/auth"
	"github.com/xjetry/probe/internal/hub/geo"
	"github.com/xjetry/probe/internal/hub/ingest"
	"github.com/xjetry/probe/internal/hub/live"
	"github.com/xjetry/probe/internal/hub/probe"
	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/hub/traffic"
	"github.com/xjetry/probe/internal/hub/web"
)

type mount struct {
	path string
	h    http.Handler
}

func mountOf(path string, h http.Handler) mount { return mount{path: path, h: h} }

// newMux 是所有服务唯一的挂载点；挂载点级测试（TestMuxRejectsAnonymousProcedures）从注册表枚举方法逐个匿名调用，
// 所以任何进了描述符的服务都必须在这里出现。除 PublicService 外，每个服务都带着它的鉴权拦截器；PublicService
// 按 §3.2 不鉴权，它的四个过程是那个测试里唯一的匿名白名单（publicProcedures），其余过程匿名调用必须得到 401。
// 往 PublicService 加方法等于把它公开给任何人，没有拦截器兜底。
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
	return runServeWith(shutdown, args, clock.Real(), newServeLogger(os.Stderr))
}

// newServeLogger 是 serve 的日志装配。启动行的文本格式有外部读者（scripts/e2e.sh 按整秒读字段），
// 测试经同一个函数装配日志，才钉得住读者实际看到的格式。
func newServeLogger(w io.Writer) *slog.Logger { return slog.New(slog.NewTextHandler(w, nil)) }

// runServeWith 由调用方拥有停止信号；后台循环与请求排空完成后才能关闭它们共用的库。
func runServeWith(stopCtx context.Context, args []string, clk clock.Clock, log *slog.Logger) (result error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	db := fs.String("db", "probe.db", "SQLite database path")
	tz := fs.String("timezone", "", "IANA time zone for traffic period boundaries and node expiry days (default: the host's zone, resolved from TZ or /etc/localtime; UTC if neither resolves); already-persisted period starts are interpreted in the new zone; usage of the current period may be reset at the next read, report or flush")
	listen := fs.String("listen", "127.0.0.1:8080", "listen address")
	proxies := fs.String("trusted-proxies", "", "comma-separated CIDRs whose X-Forwarded-For / X-Forwarded-Proto are trusted; empty trusts none. Behind a reverse proxy, list the proxy here: the public page and agent registration are rate-limited per source (one IPv4 address, or one IPv6 /64), and failed logins are locked out per source, so without it every visitor shares the proxy address's single bucket and lockout")
	publicDir := fs.String("public-dir", "", "serve this directory at / instead of the built-in public page; files are opened through os.Root, so paths cannot leave the directory and symbolic links are followed only if they are relative and never step outside it (absolute links are refused even when they point inside); a path that is not a file, or that has a segment starting with a dot (.git, .env, .well-known), gets the directory's index.html (404 under assets/); every response is no-cache. The directory shares the admin panel's origin: its scripts can read the panel and call the admin API with the session of any signed-in administrator who opens the page, so put only content you trust as much as the hub binary there")
	themeOriginFlag := fs.String("theme-origin", "", "origin that serves uploaded public-page themes, e.g. https://status.example.com; point this second hostname at the hub alongside the panel's. It must be a hostname other than the panel's (a sibling subdomain works: the session cookie is host-only), not a path under it: a theme's scripts on the panel's hostname could call the admin API with a signed-in administrator's session. Empty disables theme upload and hosting")
	retention := store.DefaultRetention
	fs.DurationVar(&retention.M1, "retention-1m", retention.M1, fmt.Sprintf("how long to keep 1-minute rows (minimum %s)", store.MinRetentionM1))
	fs.DurationVar(&retention.M5, "retention-5m", retention.M5, fmt.Sprintf("how long to keep 5-minute rows (minimum %s)", store.MinRetentionM5))
	fs.DurationVar(&retention.H1, "retention-1h", retention.H1, fmt.Sprintf("how long to keep hourly rows (minimum %s)", store.MinRetentionH1))
	fs.DurationVar(&retention.AlertEvents, "retention-alert-events", retention.AlertEvents, fmt.Sprintf("how long to keep alert events and their deliveries (minimum %s)", store.MinRetentionAlertEvents))
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := retention.Validate(); err != nil {
		return err
	}
	loc, fallback, err := loadZone(*tz)
	if err != nil {
		return err
	}
	if fallback {
		log.Warn("host time zone could not be resolved; using UTC; set --timezone explicitly")
	}

	ttl, err := parseTTL(os.Getenv("PROBE_OFFLINE_AFTER"))
	if err != nil {
		return err
	}
	trusted, err := auth.ParsePrefixes(*proxies)
	if err != nil {
		return err
	}
	themeOrigin, err := parseThemeOrigin(*themeOriginFlag)
	if err != nil {
		return err
	}
	// 替换目录在打开数据库之前核对：配置有误时 hub 不留下任何副作用就退出。
	public := web.PublicHandler()
	if *publicDir != "" {
		if public, err = web.DirHandler(*publicDir); err != nil {
			return err
		}
	}
	if !isLoopback(*listen) {
		log.Warn("listening on a non-loopback address: direct access bypasses the proxy; forwarded headers are trusted only from configured peers", "listen", *listen)
	}

	st, err := store.Open(*db, clk, log, store.MigrateSchema)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, st.Close()) }()
	reg := probe.New(st, log)
	a := auth.New(st, reg, clk, log)
	l := live.New(clk, ttl)
	book := traffic.New(st, clk, loc, log)
	alerts := alert.New(alert.Config{TTL: ttl, Location: loc}, st, l, clk, log)
	// 通知渠道与国家查询共用一个出站客户端（§4.9 复用 §9.3 的那一个）：不跟随重定向、带总超时的出站行为只有一份。
	outbound := alert.NewHTTPClient()
	notifier := alert.NewQueue(st, alerts.Channels, outbound, "", clk, nil, log)
	alerts.SetSender(notifier)
	svc, err := ingest.New(ingest.Config{TTL: ttl, TrustedProxies: trusted}, l, st, a, book, reg, clk, log)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if err := errors.Join(a.Load(ctx), svc.Load(ctx), book.Load(ctx), reg.Load(ctx), alerts.Load(ctx)); err != nil {
		return err
	}
	// 续投读取已加载的渠道快照；所有 Load 成功后才入队，后台 worker 尚未启动。
	if err := notifier.Requeue(ctx); err != nil {
		return err
	}
	admin := api.New(api.Config{TTL: ttl, ReportInterval: svc.Interval(), TrustedProxies: trusted, HubVersion: version, Location: loc, Retention: retention, ThemeOrigin: themeOrigin, PublicDir: *publicDir != ""}, st, a, l, svc, book, reg, alerts, notifier, clk, log)
	pub := api.NewPublic(api.PublicConfig{ReportInterval: svc.Interval(), TrustedProxies: trusted, Location: loc}, st, l, book, reg, clk, log)

	mux := newMux(mountOf(svc.Handler()), mountOf(admin.Handler()), mountOf(pub.Handler()), mountOf(web.Prefix, web.Handler()), mountOf("/", public))
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	drain := &drainingHandler{next: mux}
	srv := &http.Server{
		Addr: *listen, Handler: drain, ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, // ReadMaxBytes 限量不限时。
	}

	defer startLoop(svc.RunFlusher)()
	defer startLoop(func(ctx context.Context) { st.RunMaintenance(ctx, retention) })()
	defer startLoop(book.Run)()
	stopSweep := startLoop(alerts.RunOfflineSweep)
	defer startLoop(alerts.RunProbeEvaluation)()
	defer startLoop(alerts.RunExpirySweep)()
	defer startLoop(notifier.Run)()
	defer startLoop(geo.New(st, outbound, clk, log).Run)()

	// 监听在 net.Listen 返回时已建立，连接先进内核队列。runServe 装配的文本 handler 在 Info 返回前
	// 同步写完 stderr，所以先写启动行再开始 Serve，拿到任何响应的调用方都已能在日志里读到它。
	// 离线告警在 ttl 与节点宽限期中较大者之后至多再等一个 offline_sweep 才触发（NextOffline 两者都要满足），
	// 恢复在首个被接受的上报之后至多等一个 offline_sweep；渠道失败可重试且存储正常时，投递另有至多
	// delivery_retry_wait 的重试等待，存储失败时的 worker 级退避不在其内（见 alert.DeliveryRetryWait）；
	// scripts/e2e.sh 从这一行读这些量推出告警等待上限。
	log.Info("hub listening", "listen", listener.Addr().String(), "ttl", ttl, "interval", svc.Interval(), "offline_sweep", alert.OfflineSweepEvery, "delivery_retry_wait", alert.DeliveryRetryWait(), "retention_1m", retention.M1, "retention_5m", retention.M5, "retention_1h", retention.H1, "retention_alert_events", retention.AlertEvents, "timezone", loc.String(), "public_dir", *publicDir, "theme_origin", themeOrigin, "version", version)
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(listener) }()

	select {
	case <-stopCtx.Done():
		log.Info("shutting down")
	case err = <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	// 离线推断以上报准入开放为前提；drainingHandler 关闭准入后新上报一律 503，
	// unseen 的增长不再代表节点沉默。先停止巡检，避免把健康节点判为 firing 并投递，
	// 随后重启又因上报补发 recovered。
	stopSweep()
	// HTTP 先停止准入并排空；超时则断开连接以中止慢请求体，仍等待已经进入的处理器。
	// 最后由 defer 依次停止后台循环、关库，避免晚到的上报落在最后一次刷出之后。
	return errors.Join(err, shutdownHTTP(srv, drain, drainTimeout))
}

// drainTimeout 是关停时排空在途请求的上限。到点后断开连接以中止慢请求体，仍等待已经进入的处理器。
// 空闲关停和请求完成后的退出从发信号或 cancel 起算，必须早于它；否则是在等这个超时，而不是在等请求结束。
const drainTimeout = 10 * time.Second

// startLoop 返回的 stop 取消并等待循环退出；调用方用 defer 的后进先出表达停止顺序。
// 离线巡检是唯一在 HTTP 排空前显式停止的循环，原因见调用处；其余均在排空后、st.Close 前停止。
func startLoop(run func(context.Context)) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(ctx)
	}()
	return func() {
		cancel()
		<-done
	}
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
