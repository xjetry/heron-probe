package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/hub/web"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

func newThemeTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"), clk, slog.Default(), store.MigrateSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := httptest.NewServer(newTestMuxOn(t, st, clk))
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

// 按 Host 分流：去掉端口、不分大小写等于主题 origin 的主机名走主题 origin，其余（包括以它为后缀或前缀的主机名）走主 origin。
// 两边各自一张 404/200 表；任何一边都不下发 CORS 允许头。
func TestHandlerRoutesByHost(t *testing.T) {
	srv, st := newThemeTestServer(t)
	installTheme(t, st, "t", map[string]string{"index.html": "theme index", "assets/app.js": "console.log(1)", "admin/index.html": "shadow panel", "probe.v1.PublicService/GetSite": "shadow rpc"})
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
	}
	mainChecks := []check{
		{"GET", "/", builtin, "the built-in public page"},
		{"GET", "/admin/", panel, "the panel"},
		{"POST", listNodes, status(401), "401 from the admin service"},
		{"POST", getSite, connectJSON, "the public service"},
	}
	srvHost := strings.TrimPrefix(srv.URL, "http://")
	for _, c := range []struct {
		host   string
		checks []check
	}{
		{"theme.test", themeChecks},
		{"theme.test:8080", themeChecks},
		{"THEME.Test:80", themeChecks},
		{srvHost, mainChecks},
		{"panel.test", mainChecks},
		{"theme.test.evil", mainChecks},
		{"xtheme.test", mainChecks},
		{"", mainChecks},
	} {
		for _, ck := range c.checks {
			reqBody := ""
			if ck.method == http.MethodPost {
				reqBody = "{}"
			}
			r := hostDo(t, srv, ck.method, c.host, ck.path, reqBody, nil)
			if !ck.ok(r) {
				t.Errorf("Host %q %s %s: %d %q %.60q, want %s", c.host, ck.method, ck.path, r.status, r.header.Get("Content-Type"), r.body, ck.want)
			}
			for k := range r.header {
				if strings.HasPrefix(k, "Access-Control-Allow-") {
					t.Errorf("Host %q %s %s: CORS header %s: %v", c.host, ck.method, ck.path, k, r.header[k])
				}
			}
		}
	}
}

// 主题 origin 上 AdminService、AgentService（以及 probe.v1 里 PublicService 之外的任何服务）的每个过程都是 404：
// 过程从注册表枚举，不手写。PublicService 的过程由 connect 应答（JSON），而不是落到主题的 index.html。
func TestThemeOriginHidesEveryNonPublicProcedure(t *testing.T) {
	srv, st := newThemeTestServer(t)
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
	srv, st := newThemeTestServer(t)
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
	srv, st := newThemeTestServer(t)
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
