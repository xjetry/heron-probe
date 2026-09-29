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
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/xjetry/heron-probe/internal/clock"
	"github.com/xjetry/heron-probe/internal/hub/alert"
	"github.com/xjetry/heron-probe/internal/hub/api"
	"github.com/xjetry/heron-probe/internal/hub/auth"
	"github.com/xjetry/heron-probe/internal/hub/backup"
	"github.com/xjetry/heron-probe/internal/hub/geo"
	"github.com/xjetry/heron-probe/internal/hub/ingest"
	"github.com/xjetry/heron-probe/internal/hub/live"
	"github.com/xjetry/heron-probe/internal/hub/outbound"
	"github.com/xjetry/heron-probe/internal/hub/probe"
	"github.com/xjetry/heron-probe/internal/hub/store"
	"github.com/xjetry/heron-probe/internal/hub/traffic"
	"github.com/xjetry/heron-probe/internal/hub/web"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

type mount struct {
	path string
	h    http.Handler
}

func mountOf(path string, h http.Handler) mount { return mount{path: path, h: h} }

// newMux 是面板所在 origin（主 origin）的挂载点；挂载点级测试（TestMuxRejectsAnonymousProcedures）从注册表枚举方法逐个
// 匿名调用，所以任何进了描述符的服务都必须在这里出现。除 PublicService 外，每个服务都带着它的鉴权拦截器；PublicService
// 按 §3.2 不鉴权，它的四个过程是那个测试里唯一的匿名白名单（publicProcedures），其余过程匿名调用必须得到 401。
// 往 PublicService 加方法等于把它公开给任何人，总闸只决定整站是否开放，不提供身份鉴权。
func newMux(mounts ...mount) *http.ServeMux {
	mux := http.NewServeMux()
	for _, m := range mounts {
		mux.Handle(m.path, m.h)
	}
	return mux
}

// routes 是 serve 的全部 HTTP 路由；serve 与挂载点测试经同一个 newHandler 装配，测试钉住的就是 serve 实际的分流。
type routes struct {
	agent, admin, public mount
	// page 是主 origin 的根路径：内置公开页，或 --public-dir 的目录。
	page http.Handler
	// themeOrigin 是 parseThemeOrigin 的结果，空串表示没有主题 origin；themePage 是主题 origin 的根路径。
	themeOrigin string
	themePage   http.Handler
	// publicEnabled 是公开页总闸（store.Store.PublicEnabled，读内存副本）。page 与 themePage 都不自带总闸，由 newHandler
	// 统一包上。必填：缺了它不能当作"总开"，newHandler 在装配时拒绝。
	publicEnabled func() bool
}

// newHandler 按请求的 Host 在两个 origin 之间分流（§10.1）：Host 的主机名（hostname）等于主题 origin 的主机名走
// newThemeMux，其余走主 origin。只比主机名，端口不参与：浏览器的 cookie 不按端口区分，与面板同一主机名、只差端口的主题
// origin 上"会话 cookie 是 host-only"这一条不成立，所以主题 origin 必须换主机名；这样配置时按主机名分流还会把面板那个
// 主机名的请求一起分到主题 origin（那里没有面板）——hub 不知道面板用哪个主机名，这一条在启动时查不出来，写在 flag 帮助
// 与 docs/theme-guide.md。
// 公开服务的挂载点在两个 origin 上是同一个处理器：限流的令牌桶与快照缓存只有一份。
//
// 总闸约束 RPC 之外的整个公开静态面（§10）：主 origin 的根路径与主题 origin 的根路径（主题文件与回落的内置页）在这里包进
// 同一个 web.PublicGate，serve 与测试装配走的是同一处，不会一边包了一边没包。关闸时 PublicGate 不调用下游，主题 origin
// 因此不读库、不服务任何主题文件。PublicService 由它自己的拦截器读同一个开关回 NotFound，两个 origin 挂的是同一个处理器。
func newHandler(r routes) http.Handler {
	if r.publicEnabled == nil {
		panic("routes.publicEnabled is required: a missing public switch must not mean the public pages are always open")
	}
	// 面板挂在 web.Prefix 上的只有主 origin，公开服务在这里带上同一个路径，公开页据它放登录入口；主题 origin 不带。
	main := newMux(r.agent, r.admin, mountOf(r.public.path, api.WithAdminPath(r.public.h, web.Prefix)), mountOf(web.Prefix, web.Handler()), mountOf("/", web.PublicGate(r.page, r.publicEnabled)))
	if r.themeOrigin == "" {
		return main
	}
	u, err := url.Parse(r.themeOrigin)
	if err != nil {
		panic("theme origin was not parsed by parseThemeOrigin: " + err.Error())
	}
	themeHost := hostname(u.Host)
	theme := newThemeMux(r.public, web.PublicGate(r.themePage, r.publicEnabled))
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if hostname(req.Host) == themeHost {
			theme.ServeHTTP(w, req)
			return
		}
		main.ServeHTTP(w, req)
	})
}

// hostname 是 host[:port] 里规范过的主机名：Host 头与 --theme-origin 都经它比较，IPv6 字面量的方括号与端口按同一规则去掉，
// 再经 canonicalHost。Host 一侧不做 IDNA：浏览器发的 Host 总是 ASCII，--theme-origin 的非 ASCII 写法由 parseThemeOrigin
// 转成 punycode；非 ASCII 的 Host 到不了这里，net/http 以 400 拒绝（TestParseThemeOriginAcceptsOnlyHostsNetHTTPServes 钉住）。
func hostname(hostport string) string {
	return canonicalHost((&url.URL{Host: hostport}).Hostname())
}

// canonicalHost 是比较主机名用的形态：小写；去掉一个 FQDN 尾点（theme.test. 与 theme.test 是同一个 DNS 名字的两种写法，
// URL 里写了尾点，Host 头里就带着它）；IP 字面量经 netip 规范（0:0::1 与 ::1 是同一个地址，IPv4 映射地址的点分与十六进制
// 两种写法也归一）。
func canonicalHost(h string) string {
	h = strings.TrimSuffix(strings.ToLower(h), ".")
	if a, err := netip.ParseAddr(h); err == nil {
		return a.String()
	}
	return h
}

// newThemeMux 是主题 origin 的挂载点：只挂 PublicService 与主题静态文件，"主题脚本只能调 PublicService"由挂载承载而非
// 约定（§3.2、§10.1）。RPC 路径优先于静态文件：heron.v1 包里其余每个服务的路径前缀都挂 404，服务列表从描述符枚举——
// 不挂的话这些路径会落到根路径、由主题回落 index.html 答 200，以后加的服务也照此自动 404。/admin 与 /admin/ 同样 404：
// 面板不在这个 origin 上，主题也不能在这个路径下伪装出一个面板。
func newThemeMux(public mount, page http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle(public.path, public.h)
	for _, svc := range probeServices() {
		if p := "/" + string(svc.FullName()) + "/"; p != public.path {
			mux.Handle(p, http.NotFoundHandler())
		}
	}
	mux.Handle("/admin", http.NotFoundHandler())
	mux.Handle(web.Prefix, http.NotFoundHandler())
	mux.Handle("/", page)
	return mux
}

// probeServices 是 heron.v1 包里的全部服务，取自注册表。
func probeServices() []protoreflect.ServiceDescriptor {
	var out []protoreflect.ServiceDescriptor
	protoregistry.GlobalFiles.RangeFilesByPackage("heron.v1", func(file protoreflect.FileDescriptor) bool {
		for i := 0; i < file.Services().Len(); i++ {
			out = append(out, file.Services().Get(i))
		}
		return true
	})
	if len(out) == 0 {
		panic("no heron.v1 services registered")
	}
	return out
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
	db := fs.String("db", "heron.db", "SQLite database path")
	geoMMDB := fs.String("geo-mmdb", "", fmt.Sprintf("local MaxMind country database (at most %d MiB); overrides the HTTP lookup service and makes no network requests; read fully into memory and structurally verified at startup (the file carries no checksum, so a value replaced by another valid value is not detected, only structural damage such as invalid UTF-8 is) and not read again while running, so a replaced file takes effect on restart; geo.enabled still controls lookup", geo.MaxMMDBBytes>>20))
	tz := fs.String("timezone", "", "IANA time zone for traffic period boundaries and node expiry days (default: the host's zone, resolved from TZ or /etc/localtime; UTC if neither resolves); already-persisted period starts are interpreted in the new zone; usage of the current period may be reset at the next read, report or flush")
	listen := fs.String("listen", "127.0.0.1:8080", "listen address")
	adminOrigin := fs.String("admin-origin", "", "trusted HTTPS origin of the admin panel for Passkey, e.g. https://panel.example.com; empty disables Passkey; HTTP is permitted only on localhost")
	proxies := fs.String("trusted-proxies", "", "comma-separated CIDRs whose X-Forwarded-For / X-Forwarded-Proto are trusted; empty trusts none. Behind a reverse proxy, list the proxy here: the public page and agent registration are rate-limited per source (one IPv4 address, or one IPv6 /64), and failed logins are locked out per source, so without it every visitor shares the proxy address's single bucket and lockout; a node's recorded source address is also the proxy address")
	publicDir := fs.String("public-dir", "", "serve this directory at / instead of the built-in public page; files are opened through os.Root, so paths cannot leave the directory and symbolic links are followed only if they are relative and never step outside it (absolute links are refused even when they point inside); a path that is not a file, or that has a segment starting with a dot (.git, .env, .well-known), gets the directory's index.html (404 under assets/); every response is no-cache. The directory shares the admin panel's origin: its scripts can read the panel and call the admin API with the session of any signed-in administrator who opens the page, so put only content you trust as much as the hub binary there")
	themeOriginFlag := fs.String("theme-origin", "", "origin that serves uploaded public-page themes, e.g. https://status.example.com; point this second hostname at the hub alongside the panel's. It must be a hostname other than the panel's, not a path under it: a theme's scripts on the panel's hostname could call the admin API with a signed-in administrator's session. A sibling subdomain (status.example.com beside panel.example.com) is same-site, so SameSite=Strict does not separate the two; the facts that do are listed in docs/theme-guide.md. The same hostname on another port does not count: cookies do not isolate ports, and requests are routed by hostname with the port ignored, so the panel's own requests would be routed to the theme origin, where there is no panel; the hub cannot detect this at startup. On this hostname only the public API and the enabled theme are served (the built-in public page when no theme is enabled); --public-dir does not apply here. Empty disables theme upload and hosting")
	retention := store.DefaultRetention
	fs.DurationVar(&retention.M1, "retention-1m", retention.M1, fmt.Sprintf("how long to keep 1-minute rows (minimum %s)", store.MinRetentionM1))
	fs.DurationVar(&retention.M5, "retention-5m", retention.M5, fmt.Sprintf("how long to keep 5-minute rows (minimum %s)", store.MinRetentionM5))
	fs.DurationVar(&retention.H1, "retention-1h", retention.H1, fmt.Sprintf("how long to keep hourly rows (minimum %s)", store.MinRetentionH1))
	fs.DurationVar(&retention.AlertEvents, "retention-alert-events", retention.AlertEvents, fmt.Sprintf("how long to keep alert events and their deliveries (minimum %s)", store.MinRetentionAlertEvents))
	if err := fs.Parse(args); err != nil {
		return err
	}
	// 缺席才选择 HTTP；显式空路径也必须打开并报错，不能把部署配置错误变成意外出网。
	geoMMDBSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "geo-mmdb" {
			geoMMDBSet = true
		}
	})
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

	ttl, err := parseTTL(os.Getenv("HERON_OFFLINE_AFTER"))
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
	// 通知渠道与国家查询的 HTTP 后端共用一个出站客户端（§4.9 复用 §9.3 的那一个），两者不跟随重定向、带总时限的行为
	// 因此是同一份。时限取 alert.NotifyTimeout，推导在通知投递一侧（见其注释）；国家查询是不带正文的 GET、应答至多读
	// geo 包的 maxResponseBytes，同属 outbound.NewClient 所说的请求与应答都有小上界的消费方。
	client := outbound.NewClient(alert.NotifyTimeout)
	// 国家查询的后端只在这里选一次，同一个对象交给查询器与 api：面板回显的后端就是查询器实际用的那个。
	// 文件路径是部署配置，由启动参数指定，设置 API 没有可改它的字段。OpenMMDB 在这里把整个文件读进内存并做结构校验，运行期
	// 不再访问文件，所以换文件要重启才生效，原地覆盖或截断也不影响运行中的答案。
	// 显式选择本地库后不能静默退回 HTTP，否则运维以为不出网时会把节点地址送到外部。
	// geoLog 随选定的后端一起写出，追加在启动行末尾：运维据此确认国家查询会不会出网、加载的是哪一版本地库。
	var geoBackend geo.Backend = geo.NewHTTP(client)
	geoLog := []any{"geo_backend", "http"}
	if geoMMDBSet {
		local, err := geo.OpenMMDB(*geoMMDB)
		if err != nil {
			return err
		}
		geoBackend = local
		md := local.Metadata()
		geoLog = []any{"geo_backend", "mmdb", "geo_mmdb", local.MMDBPath(), "geo_mmdb_type", md.DatabaseType,
			"geo_mmdb_built", md.BuildTime().UTC().Format(time.RFC3339)}
	}

	st, err := store.Open(*db, clk, log, store.MigrateSchema)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, st.Close()) }()
	reg := probe.New(st, log)
	l := live.New(clk, ttl)
	book := traffic.New(st, clk, loc, log)
	alerts := alert.New(alert.Config{TTL: ttl, Location: loc}, st, l, clk, log)
	notifier := alert.NewQueue(st, alerts.Channels, client, "", clk, nil, log)
	alerts.SetSender(notifier)
	a := auth.New(st, reg, notifier, clk, loc, log)
	if err := a.ConfigureWebAuthn(*adminOrigin, themeOrigin); err != nil {
		return fmt.Errorf("--admin-origin: %w", err)
	}
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
	backups := backup.New(st, notifier, clk, log)
	admin := api.New(api.Config{Backups: backups, TTL: ttl, ReportInterval: svc.Interval(), TrustedProxies: trusted, HubVersion: version, Location: loc, Retention: retention, ThemeOrigin: themeOrigin, PublicDir: *publicDir != "", Geo: geoBackend}, st, a, l, svc, book, reg, alerts, notifier, clk, log)
	pub := api.NewPublic(api.PublicConfig{ReportInterval: svc.Interval(), TrustedProxies: trusted, Location: loc}, st, l, book, reg, clk, log)

	handler := newHandler(routes{
		agent: mountOf(svc.Handler()), admin: mountOf(admin.Handler()), public: mountOf(pub.Handler()), page: public,
		themeOrigin: themeOrigin, themePage: web.ThemeHandler(st, web.PublicHandler(), log), publicEnabled: st.PublicEnabled,
	})
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	drain := &drainingHandler{next: handler}
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
	defer startLoop(geo.New(st, geoBackend, clk, log).Run)()
	defer startLoop(backups.Run)()

	// 监听在 net.Listen 返回时已建立，连接先进内核队列。runServe 装配的文本 handler 在 Info 返回前
	// 同步写完 stderr，所以先写启动行再开始 Serve，拿到任何响应的调用方都已能在日志里读到它。
	// 离线告警在 ttl 与节点宽限期中较大者之后至多再等一个 offline_sweep 才触发（NextOffline 两者都要满足），
	// 恢复在首个被接受的上报之后至多等一个 offline_sweep；delivery_retry_wait 只给出固定重试间隔的总和，不是投递
	// 等待上界：Retry-After、排队、not_before 的整秒取整与存储退避还会增加等待（见 alert.DeliveryRetryWait）。
	// scripts/e2e.sh 用不限节奏且总回 200 的 Webhook 接收器，从这一行读出其场景的等待预算。
	log.Info("hub listening", append([]any{"listen", listener.Addr().String(), "ttl", ttl, "interval", svc.Interval(), "offline_sweep", alert.OfflineSweepEvery, "delivery_retry_wait", alert.DeliveryRetryWait(), "retention_1m", retention.M1, "retention_5m", retention.M5, "retention_1h", retention.H1, "retention_alert_events", retention.AlertEvents, "timezone", loc.String(), "public_dir", *publicDir, "theme_origin", themeOrigin, "version", version}, geoLog...)...)
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
