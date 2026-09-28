package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/hub/web"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// newThemeTestServer 起一个按 serve 装配的 hub，themeOriginFlag 是 --theme-origin 的原文。
func newThemeTestServer(t *testing.T, themeOriginFlag string) (*httptest.Server, *store.Store) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"), clk, slog.Default(), store.MigrateSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := httptest.NewServer(newTestMuxOn(t, st, clk, themeOriginFlag))
	t.Cleanup(srv.Close)
	return srv, st
}

// installTheme 直接经存储装一个主题并启用：分流与托管的用例不经 UploadTheme（它的校验由 api 与 theme 的测试钉住）。
func installTheme(t *testing.T, st *store.Store, id string, files map[string]string) {
	t.Helper()
	var list []store.ThemeFile
	for p, c := range files {
		list = append(list, store.ThemeFile{Path: p, Content: []byte(c)})
	}
	if _, err := st.PutTheme(t.Context(), store.Theme{ID: id, Name: id, Version: "1", UploadedAt: time.Unix(100, 0)}, list, false, 20); err != nil {
		t.Fatal(err)
	}
	if err := st.EnableTheme(t.Context(), id); err != nil {
		t.Fatal(err)
	}
}

type hostResponse struct {
	status int
	header http.Header
	body   string
}

// hostDo 以给定的 Host 头发请求，不跟随重定向。每个请求都带一个别处的 Origin，header 里的头在此之上覆盖或追加。
func hostDo(t *testing.T, srv *httptest.Server, method, host, path, body string, header http.Header) hostResponse {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", "https://elsewhere.example")
	for k, v := range header {
		req.Header[k] = v
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return hostResponse{resp.StatusCode, resp.Header, string(b)}
}

// builtinPublic 是内置公开页对 path 的应答，直接调 web.PublicHandler 得到；构建过与没构建过都能逐字节比较。
func builtinPublic(path string) hostResponse {
	rec := httptest.NewRecorder()
	web.PublicHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return hostResponse{rec.Code, rec.Header(), rec.Body.String()}
}

// 按 Host 分流：Host 与 --theme-origin 的主机名规范之后相等（去端口、不分大小写、去一个尾点、IP 字面量按 netip、
// 非 ASCII 的 --theme-origin 按浏览器发的 punycode）走主题 origin，其余（包括以它为后缀或前缀的主机名）走主 origin。
// 两边各自一张 404/200 表，对主机名、IPv6 字面量、国际化域名三种 --theme-origin 各跑一遍。
//
// 表里每一行的应答都不带任何 Access-Control-Allow-* 头，包括两边对 AdminService 过程、PublicService 过程与静态路径的
// OPTIONS 预检（带 Origin 与 Access-Control-Request-Method/-Headers）。承重的是预检：浏览器的跨源 JSON 请求先发预检，
// 预检的应答不许可这个 origin，实际请求就不发出；一旦许可（允许源加 Allow-Credentials），兄弟子域上的主题脚本就能带着
// 管理员的 cookie 把写请求发到面板，副作用在服务端已经发生，读不读得到响应无关紧要。
func TestHandlerRoutesByHost(t *testing.T) {
	const listNodes, report, getSite = "/probe.v1.AdminService/ListNodes", "/probe.v1.AgentService/Report", "/probe.v1.PublicService/GetSite"
	type check struct {
		method, path string
		ok           func(hostResponse) bool
		want         string
	}
	status := func(code int) func(hostResponse) bool { return func(r hostResponse) bool { return r.status == code } }
	body := func(want string) func(hostResponse) bool {
		return func(r hostResponse) bool { return r.status == http.StatusOK && r.body == want }
	}
	// RPC 路径优先于主题文件：包里的 probe.v1.PublicService/GetSite 遮蔽不了公开服务。
	connectJSON := func(r hostResponse) bool {
		return r.status == http.StatusOK && r.header.Get("Content-Type") == "application/json" && r.body != "shadow rpc"
	}
	builtin := func(r hostResponse) bool {
		want := builtinPublic("/")
		return r.status == want.status && r.body == want.body && r.header.Get("Content-Security-Policy") == want.header.Get("Content-Security-Policy")
	}
	// 面板构建过是 200、没构建是 503，两者都带面板的 CSP；主题里的 admin/index.html 不得出现在任何一边。
	panel := func(r hostResponse) bool {
		return (r.status == http.StatusOK || r.status == http.StatusServiceUnavailable) && strings.Contains(r.header.Get("Content-Security-Policy"), "default-src 'self'") && r.body != "shadow panel"
	}
	// 预检行不约束状态码：承重的是循环里对每一行都做的"没有 Access-Control-Allow-* 头"。
	preflight := func(hostResponse) bool { return true }
	preflightHeader := http.Header{
		"Origin":                         {"http://" + testThemeHost},
		"Access-Control-Request-Method":  {"POST"},
		"Access-Control-Request-Headers": {"content-type, connect-protocol-version"},
	}
	themeChecks := []check{
		{"GET", "/", body("theme index"), "the theme's index.html"},
		{"GET", "/nodes/3", body("theme index"), "the theme's index.html"},
		{"GET", "/assets/app.js", body("console.log(1)"), "the theme's file"},
		{"GET", "/admin", status(404), "404"},
		{"GET", "/admin/", status(404), "404"},
		{"GET", "/admin/index.html", status(404), "404"},
		{"POST", listNodes, status(404), "404"},
		{"POST", report, status(404), "404"},
		{"POST", getSite, connectJSON, "the public service"},
		{"GET", getSite + "?connect=v1&encoding=json&message=%7B%7D", connectJSON, "the public service"},
		{"OPTIONS", listNodes, preflight, "a preflight answer"},
		{"OPTIONS", getSite, preflight, "a preflight answer"},
		{"OPTIONS", "/", preflight, "a preflight answer"},
		{"OPTIONS", "/assets/app.js", preflight, "a preflight answer"},
	}
	mainChecks := []check{
		{"GET", "/", builtin, "the built-in public page"},
		{"GET", "/admin/", panel, "the panel"},
		{"POST", listNodes, status(401), "401 from the admin service"},
		{"POST", getSite, connectJSON, "the public service"},
		{"OPTIONS", listNodes, preflight, "a preflight answer"},
		{"OPTIONS", getSite, preflight, "a preflight answer"},
		{"OPTIONS", "/", preflight, "a preflight answer"},
		{"OPTIONS", "/admin/", preflight, "a preflight answer"},
	}
	for _, setup := range []struct {
		flag        string
		theme, main []string // 应分到主题 origin 与主 origin 的 Host 头；主 origin 另加 hub 自己的地址。
	}{
		{"http://" + testThemeHost,
			[]string{"theme.test", "theme.test:8080", "THEME.Test:80", "theme.test.", "Theme.Test.:8443"},
			[]string{"panel.test", "theme.test.evil", "xtheme.test", "theme.test.."}},
		{"http://[0:0::1]",
			[]string{"[::1]", "[::1]:8080", "[0:0::1]", "[0000:0000::0001]:80"},
			[]string{"[::2]", "[::1:0]", "[2001:db8::1]:8080"}},
		{"https://状态.test",
			[]string{"xn--t7t692b.test", "XN--T7T692B.test:443", "xn--t7t692b.test."},
			[]string{"t7t692b.test", "xn--t7t692b.test.evil"}},
	} {
		srv, st := newThemeTestServer(t, setup.flag)
		installTheme(t, st, "t", map[string]string{"index.html": "theme index", "assets/app.js": "console.log(1)", "admin/index.html": "shadow panel", "probe.v1.PublicService/GetSite": "shadow rpc"})
		type route struct {
			host   string
			checks []check
		}
		var routes []route
		for _, h := range setup.theme {
			routes = append(routes, route{h, themeChecks})
		}
		for _, h := range append(setup.main, strings.TrimPrefix(srv.URL, "http://")) {
			routes = append(routes, route{h, mainChecks})
		}
		for _, c := range routes {
			for _, ck := range c.checks {
				reqBody := ""
				if ck.method == http.MethodPost {
					reqBody = "{}"
				}
				var header http.Header
				if ck.method == http.MethodOptions {
					header = preflightHeader
				}
				r := hostDo(t, srv, ck.method, c.host, ck.path, reqBody, header)
				if !ck.ok(r) {
					t.Errorf("--theme-origin %s, Host %q %s %s: %d %q %.60q, want %s", setup.flag, c.host, ck.method, ck.path, r.status, r.header.Get("Content-Type"), r.body, ck.want)
				}
				for k := range r.header {
					if strings.HasPrefix(k, "Access-Control-Allow-") {
						t.Errorf("--theme-origin %s, Host %q %s %s: CORS header %s: %v", setup.flag, c.host, ck.method, ck.path, k, r.header[k])
					}
				}
			}
		}
	}
}

// 主题 origin 上 AdminService、AgentService（以及 probe.v1 里 PublicService 之外的任何服务）的每个过程都是 404：
// 过程从注册表枚举，不手写。PublicService 的过程由 connect 应答（JSON），而不是落到主题的 index.html。
func TestThemeOriginHidesEveryNonPublicProcedure(t *testing.T) {
	srv, st := newThemeTestServer(t, "http://"+testThemeHost)
	installTheme(t, st, "t", map[string]string{"index.html": "theme index"})
	count, public := 0, 0
	protoregistry.GlobalFiles.RangeFilesByPackage("probe.v1", func(file protoreflect.FileDescriptor) bool {
		for i := 0; i < file.Services().Len(); i++ {
			svc := file.Services().Get(i)
			for j := 0; j < svc.Methods().Len(); j++ {
				path := "/" + string(svc.FullName()) + "/" + string(svc.Methods().Get(j).Name())
				count++
				r := hostDo(t, srv, http.MethodPost, testThemeHost, path, "{}", nil)
				if publicProcedures[path] {
					public++
					if r.status == http.StatusNotFound || r.header.Get("Content-Type") != "application/json" {
						t.Errorf("%s on the theme origin: %d %q, want the public service to answer", path, r.status, r.header.Get("Content-Type"))
					}
					continue
				}
				if r.status != http.StatusNotFound || r.body == "theme index" {
					t.Errorf("%s on the theme origin: %d %.60q, want 404", path, r.status, r.body)
				}
			}
		}
		return true
	})
	if count == 0 || public != len(publicProcedures) || count == public {
		t.Fatalf("enumerated %d procedures, %d public; want every probe.v1 procedure including all %d public ones", count, public, len(publicProcedures))
	}
}

// 没有启用中的主题时主题 origin 服务内置公开页：从未装过、装了未启用、停用、启用中的被删掉，四种情形都与内置页逐字节相同。
func TestThemeOriginFallsBackToBuiltinPublicPage(t *testing.T) {
	srv, st := newThemeTestServer(t, "http://"+testThemeHost)
	assertBuiltin := func(when string) {
		t.Helper()
		for _, p := range []string{"/", "/nodes/3"} {
			got, want := hostDo(t, srv, http.MethodGet, testThemeHost, p, "", nil), builtinPublic(p)
			if got.status != want.status || got.body != want.body || got.header.Get("Content-Security-Policy") != want.header.Get("Content-Security-Policy") {
				t.Errorf("%s: GET %s on the theme origin: %d %.60q, want the built-in public page %d %.60q", when, p, got.status, got.body, want.status, want.body)
			}
		}
	}
	assertTheme := func(when string) {
		t.Helper()
		if got := hostDo(t, srv, http.MethodGet, testThemeHost, "/", "", nil); got.status != http.StatusOK || got.body != "theme index" {
			t.Fatalf("%s: GET / on the theme origin: %d %.60q, want the theme", when, got.status, got.body)
		}
	}
	assertBuiltin("no theme installed")
	if _, err := st.PutTheme(t.Context(), store.Theme{ID: "t", Name: "t", Version: "1", UploadedAt: time.Unix(100, 0)}, []store.ThemeFile{{Path: "index.html", Content: []byte("theme index")}}, false, 20); err != nil {
		t.Fatal(err)
	}
	assertBuiltin("theme installed but not enabled")
	if err := st.EnableTheme(t.Context(), "t"); err != nil {
		t.Fatal(err)
	}
	assertTheme("theme enabled")
	if err := st.EnableTheme(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	assertBuiltin("theme disabled")
	if err := st.EnableTheme(t.Context(), "t"); err != nil {
		t.Fatal(err)
	}
	assertTheme("theme re-enabled")
	if err := st.DeleteTheme(t.Context(), "t"); err != nil {
		t.Fatal(err)
	}
	assertBuiltin("enabled theme deleted")
}

// 启用中主题的内容只经 PutTheme、EnableTheme、DeleteTheme 改变：每个写者提交之后，主题 origin 的下一个请求就看到新内容。
// 同一秒内的两次整包替换内容不同、ETag 就不同：带上一次 ETag 的条件请求拿到新内容，而不是 304 让浏览器留着旧包。
func TestThemeOriginServesEveryWriterCommitOnTheNextRequest(t *testing.T) {
	srv, st := newThemeTestServer(t, "http://"+testThemeHost)
	get := func(ifNoneMatch string) hostResponse {
		t.Helper()
		h := http.Header{}
		if ifNoneMatch != "" {
			h.Set("If-None-Match", ifNoneMatch)
		}
		return hostDo(t, srv, http.MethodGet, testThemeHost, "/", "", h)
	}
	// 上传时刻固定为同一秒：ETag 必须由内容区分，不能靠时间。
	put := func(id, index string) {
		t.Helper()
		if _, err := st.PutTheme(t.Context(), store.Theme{ID: id, Name: id, Version: "1", UploadedAt: time.Unix(100, 0)},
			[]store.ThemeFile{{Path: "index.html", Content: []byte(index)}}, false, 20); err != nil {
			t.Fatal(err)
		}
	}
	enable := func(id string) {
		t.Helper()
		if err := st.EnableTheme(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	theme := func(when, ifNoneMatch, want string) string {
		t.Helper()
		r := get(ifNoneMatch)
		etag := r.header.Get("ETag")
		if r.status != http.StatusOK || r.body != want || etag == "" || etag == ifNoneMatch {
			t.Fatalf("%s: GET / on the theme origin with If-None-Match %q: %d %.60q ETag %q, want 200 %q with a new ETag", when, ifNoneMatch, r.status, r.body, etag, want)
		}
		return etag
	}
	builtin := func(when string) {
		t.Helper()
		got, want := get(""), builtinPublic("/")
		if got.status != want.status || got.body != want.body {
			t.Fatalf("%s: GET / on the theme origin: %d %.60q, want the built-in public page", when, got.status, got.body)
		}
	}
	builtin("nothing installed")
	put("a", "a v1")
	builtin("installed, not enabled")
	enable("a")
	e1 := theme("EnableTheme a", "", "a v1")
	if r := get(e1); r.status != http.StatusNotModified {
		t.Fatalf("unchanged theme with its ETag: %d, want 304", r.status)
	}
	put("a", "a v2")
	e2 := theme("PutTheme replacing the enabled theme", e1, "a v2")
	put("a", "a v3")
	e3 := theme("second PutTheme within the same second", e2, "a v3")
	put("b", "b v1")
	if r := get(e3); r.status != http.StatusNotModified {
		t.Fatalf("installing another theme changed the enabled one: %d %.60q, want 304", r.status, r.body)
	}
	enable("b")
	theme("EnableTheme b", e3, "b v1")
	enable("")
	builtin("EnableTheme none")
	enable("b")
	theme("EnableTheme b again", "", "b v1")
	if err := st.DeleteTheme(t.Context(), "b"); err != nil {
		t.Fatal(err)
	}
	builtin("DeleteTheme of the enabled theme")
}

// 总闸约束主题 origin 上 RPC 之外的整个静态面（§10）：关闸后页面路径（包括主题包里的文件路径）是"公开页已关闭"的说明页，
// 带内置页的安全头，assets/ 下 404，主题文件的内容一处都不出现；PublicService 全部 NotFound；/admin 仍是 404。重新打开即
// 恢复，启用中的主题不变。
func TestThemeOriginObeysThePublicSwitch(t *testing.T) {
	srv, st := newThemeTestServer(t, "http://"+testThemeHost)
	installTheme(t, st, "t", map[string]string{"index.html": "theme index", "theme.js": "theme script", "assets/x.js": "theme asset"})
	setPublic := func(on bool) {
		t.Helper()
		if _, err := st.SaveSettings(t.Context(), store.SettingsUpdate{PublicEnabled: &on}); err != nil {
			t.Fatal(err)
		}
	}
	get := func(path string) hostResponse { return hostDo(t, srv, http.MethodGet, testThemeHost, path, "", nil) }
	getSite := func() hostResponse {
		return hostDo(t, srv, http.MethodPost, testThemeHost, "/probe.v1.PublicService/GetSite", "{}", nil)
	}
	builtinCSP := builtinPublic("/").header.Get("Content-Security-Policy")

	setPublic(false)
	for _, p := range []string{"/", "/nodes/3", "/theme.js", "/index.html"} {
		r := get(p)
		if r.status != http.StatusOK || !strings.Contains(r.body, "公开页已关闭") || strings.Contains(r.body, "theme") {
			t.Errorf("closed: GET %s on the theme origin: %d %.80q, want the closed page", p, r.status, r.body)
		}
		if r.header.Get("Content-Security-Policy") != builtinCSP || r.header.Get("Cache-Control") != "no-store" {
			t.Errorf("closed: GET %s on the theme origin: CSP %q, Cache-Control %q; want the built-in CSP and no-store", p, r.header.Get("Content-Security-Policy"), r.header.Get("Cache-Control"))
		}
	}
	for _, p := range []string{"/assets/x.js", "/assets/missing.js"} {
		if r := get(p); r.status != http.StatusNotFound || strings.Contains(r.body, "theme") {
			t.Errorf("closed: GET %s on the theme origin: %d %.80q, want 404", p, r.status, r.body)
		}
	}
	if r := getSite(); r.status != http.StatusNotFound || !strings.Contains(r.body, `"not_found"`) {
		t.Errorf("closed: PublicService/GetSite on the theme origin: %d %.80q, want NotFound", r.status, r.body)
	}
	if r := get("/admin/"); r.status != http.StatusNotFound {
		t.Errorf("closed: GET /admin/ on the theme origin: %d, want 404", r.status)
	}

	setPublic(true)
	if r := get("/nodes/3"); r.status != http.StatusOK || r.body != "theme index" {
		t.Errorf("reopened: GET /nodes/3 on the theme origin: %d %.80q, want the theme's index.html", r.status, r.body)
	}
	if r := get("/assets/x.js"); r.status != http.StatusOK || r.body != "theme asset" {
		t.Errorf("reopened: GET /assets/x.js on the theme origin: %d %.80q, want the theme's file", r.status, r.body)
	}
	if r := getSite(); r.status != http.StatusOK {
		t.Errorf("reopened: PublicService/GetSite on the theme origin: %d %.80q, want 200", r.status, r.body)
	}
	if list, err := st.ListThemes(t.Context()); err != nil || len(list) != 1 || !list[0].Enabled {
		t.Errorf("themes after closing and reopening: %+v %v, want theme t still enabled", list, err)
	}
}

// 关闸时两个 origin 的根路径处理器一次都不被调用：主题 origin 的托管因此不读库（ThemeHandler 只在被调用时比代数、重读），
// 也交不出主题文件。newHandler 是 serve 与测试共用的装配处，这里直接给它计数的处理器。publicEnabled 缺席时装配即拒绝，
// 不能退化成"总开"。
func TestNewHandlerKeepsBothStaticSurfacesBehindThePublicSwitch(t *testing.T) {
	var open atomic.Bool
	var pageCalls, themePageCalls atomic.Int64
	counting := func(n *atomic.Int64) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			n.Add(1)
			io.WriteString(w, "static surface")
		})
	}
	r := routes{
		agent:  mountOf("/probe.v1.AgentService/", http.NotFoundHandler()),
		admin:  mountOf("/probe.v1.AdminService/", http.NotFoundHandler()),
		public: mountOf("/probe.v1.PublicService/", http.NotFoundHandler()),
		page:   counting(&pageCalls), themeOrigin: "http://" + testThemeHost, themePage: counting(&themePageCalls),
		publicEnabled: open.Load,
	}
	h := newHandler(r)
	serveAll := func() {
		for _, host := range []string{testThemeHost, "panel.test"} {
			for _, p := range []string{"/", "/nodes/3", "/theme.js", "/assets/x.js"} {
				req := httptest.NewRequest(http.MethodGet, p, nil)
				req.Host = host
				h.ServeHTTP(httptest.NewRecorder(), req)
			}
		}
	}
	serveAll()
	if pageCalls.Load() != 0 || themePageCalls.Load() != 0 {
		t.Fatalf("closed: the main origin's page was called %d times and the theme origin's %d times, want neither", pageCalls.Load(), themePageCalls.Load())
	}
	open.Store(true)
	serveAll()
	if pageCalls.Load() != 4 || themePageCalls.Load() != 4 {
		t.Fatalf("open: the main origin's page was called %d times and the theme origin's %d times, want 4 each", pageCalls.Load(), themePageCalls.Load())
	}
	r.publicEnabled = nil
	defer func() {
		if recover() == nil {
			t.Fatal("newHandler accepted routes without a public switch")
		}
	}()
	newHandler(r)
}
