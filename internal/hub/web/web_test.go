package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func builtFS(dir string) fstest.MapFS {
	return fstest.MapFS{
		dir + "/index.html":         {Data: []byte("<!doctype html><div id=root></div>")},
		dir + "/assets/app-abc.js":  {Data: []byte("console.log(1)")},
		dir + "/assets/app-abc.css": {Data: []byte("body{}")},
		dir + "/robots.txt":         {Data: []byte("User-agent: *")},
		dir + "/sub/page.txt":       {Data: []byte("sub page")},
		dir + "/.gitkeep":           {},
		dir + "/.env":               {Data: []byte("SECRET=embedded")},
		dir + "/.git/config":        {Data: []byte("[core] embedded")},
		dir + "/sub/.hidden.txt":    {Data: []byte("hidden page")},
		dir + "/assets/.hidden.js":  {Data: []byte("hidden asset")},
	}
}

func get(t *testing.T, h http.Handler, path string) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	resp := rec.Result()
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func responseBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func checkSecurityHeaders(t *testing.T, resp *http.Response) {
	t.Helper()
	wantCSP := "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"
	if resp.Header.Get("Content-Security-Policy") != wantCSP || resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("security headers missing or incorrect: %v", resp.Header)
	}
}

// 面板与内置公开页是同一个核心、两个挂载点：同一组路径在两处得到同样的应答与同一套 CSP。
func TestEmbeddedServesFilesAndFallsBackToIndex(t *testing.T) {
	for _, mount := range []struct {
		name, dir, prefix string
	}{{"panel", "dist", Prefix}, {"public", "dist-public", "/"}} {
		h := embedded(builtFS(mount.dir), mount.dir, mount.prefix, notBuiltAdmin)
		for _, c := range []struct {
			path, wantBody, wantCache string
			wantStatus                int
		}{
			{"", "<div id=root>", "no-cache", 200},
			{"index.html", "<div id=root>", "no-cache", 200},
			{"nodes/7", "<div id=root>", "no-cache", 200},
			{"assets/app-abc.js", "console.log", "public, max-age=31536000, immutable", 200},
			{"assets/app-abc.css", "body{}", "public, max-age=31536000, immutable", 200},
			{"robots.txt", "User-agent: *", "no-cache", 200},
			{"sub/", "<div id=root>", "no-cache", 200},
			{"sub", "<div id=root>", "no-cache", 200},
			{"assets/../robots.txt", "User-agent: *", "no-cache", 200},
			{"sub/../robots.txt", "User-agent: *", "no-cache", 200},
			{"assets/missing.js", "404 page not found", "", 404},
			{"assets/", "404 page not found", "", 404},
			{"assets", "404 page not found", "", 404},
			// 点文件当作不存在：assets/ 下 404，其余回落 index.html；产物里的 .gitkeep 也不例外。
			{".gitkeep", "<div id=root>", "no-cache", 200},
			{".env", "<div id=root>", "no-cache", 200},
			{".git/config", "<div id=root>", "no-cache", 200},
			{"sub/.hidden.txt", "<div id=root>", "no-cache", 200},
			{"assets/.hidden.js", "404 page not found", "", 404},
		} {
			path := mount.prefix + c.path
			t.Run(mount.name+" "+path, func(t *testing.T) {
				resp := get(t, h, path)
				body := responseBody(t, resp)
				if resp.StatusCode != c.wantStatus || !strings.Contains(body, c.wantBody) {
					t.Fatalf("%s: status %d body %q", path, resp.StatusCode, body)
				}
				if c.wantCache != "" && resp.Header.Get("Cache-Control") != c.wantCache {
					t.Fatalf("%s: Cache-Control %q, want %q", path, resp.Header.Get("Cache-Control"), c.wantCache)
				}
				checkSecurityHeaders(t, resp)
			})
		}
	}
}

func TestUnbuiltEmbeddedPagesExplainThemselves(t *testing.T) {
	for _, c := range []struct {
		dir, prefix, notBuilt, want string
	}{
		{"dist", Prefix, notBuiltAdmin, "The admin panel has not been built"},
		{"dist-public", "/", notBuiltPublic, "The public page has not been built"},
	} {
		h := embedded(fstest.MapFS{c.dir + "/.gitkeep": {}}, c.dir, c.prefix, c.notBuilt)
		resp := get(t, h, c.prefix+"nodes/7")
		body := responseBody(t, resp)
		if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, c.want) {
			t.Fatalf("%s: status %d body %q", c.dir, resp.StatusCode, body)
		}
		if resp.Header.Get("Cache-Control") != "no-cache" || resp.Header.Get("Content-Type") != "text/html; charset=utf-8" {
			t.Fatalf("%s: unbuilt response headers: %v", c.dir, resp.Header)
		}
		checkSecurityHeaders(t, resp)
	}
}

// all:dist 与 all:dist-public 必须能匹配文件；源码检出靠入库的 .gitkeep 满足，构建后还会包含产物。
func TestEmbeddedDistExists(t *testing.T) {
	if _, err := adminDist.ReadDir("dist"); err != nil {
		t.Fatal(err)
	}
	if _, err := publicDist.ReadDir("dist-public"); err != nil {
		t.Fatal(err)
	}
}
