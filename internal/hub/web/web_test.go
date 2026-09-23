package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func builtFS() fstest.MapFS {
	return fstest.MapFS{
		"dist/index.html":         {Data: []byte("<!doctype html><div id=root></div>")},
		"dist/assets/app-abc.js":  {Data: []byte("console.log(1)")},
		"dist/assets/app-abc.css": {Data: []byte("body{}")},
		"dist/robots.txt":         {Data: []byte("User-agent: *")},
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

func TestServesFilesAndFallsBackToIndex(t *testing.T) {
	h := handlerFor(builtFS())
	for _, c := range []struct {
		path, wantBody, wantCache string
		wantStatus                int
	}{
		{"/admin/", "<div id=root>", "no-cache", 200},
		{"/admin/index.html", "<div id=root>", "no-cache", 200},
		{"/admin/nodes/7", "<div id=root>", "no-cache", 200},
		{"/admin/assets/app-abc.js", "console.log", "public, max-age=31536000, immutable", 200},
		{"/admin/assets/app-abc.css", "body{}", "public, max-age=31536000, immutable", 200},
		{"/admin/robots.txt", "User-agent: *", "no-cache", 200},
		{"/admin/assets/missing.js", "404 page not found", "", 404},
	} {
		t.Run(c.path, func(t *testing.T) {
			resp := get(t, h, c.path)
			body := responseBody(t, resp)
			if resp.StatusCode != c.wantStatus || !strings.Contains(body, c.wantBody) {
				t.Fatalf("%s: status %d body %q", c.path, resp.StatusCode, body)
			}
			if c.wantCache != "" && resp.Header.Get("Cache-Control") != c.wantCache {
				t.Fatalf("%s: Cache-Control %q, want %q", c.path, resp.Header.Get("Cache-Control"), c.wantCache)
			}
			checkSecurityHeaders(t, resp)
		})
	}
}

func TestUnbuiltPanelExplainsItself(t *testing.T) {
	h := handlerFor(fstest.MapFS{"dist/.gitkeep": {}})
	resp := get(t, h, "/admin/")
	body := responseBody(t, resp)
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "has not been built") {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	if resp.Header.Get("Cache-Control") != "no-cache" || resp.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("unbuilt response headers: %v", resp.Header)
	}
	checkSecurityHeaders(t, resp)
}

func TestRootRedirectsOnlyExactRoot(t *testing.T) {
	h := RootRedirect()
	if resp := get(t, h, "/"); resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/admin/" {
		t.Fatalf("/: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp := get(t, h, "/nothing"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/nothing: %d", resp.StatusCode)
	}
}

// all:dist 必须能匹配文件；源码检出靠入库的 .gitkeep 满足，构建后还会包含产物。
func TestEmbeddedDistExists(t *testing.T) {
	if _, err := dist.ReadDir("dist"); err != nil {
		t.Fatal(err)
	}
}
