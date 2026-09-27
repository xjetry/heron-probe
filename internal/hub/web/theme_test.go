package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
)

// fakeThemeFiles 按 EnabledThemeFiles 的契约应答：只返回 files 里有的路径；files 为 nil 表示没有启用中的主题。
type fakeThemeFiles struct {
	files map[string]string
	err   error
}

func (f fakeThemeFiles) EnabledThemeFiles(_ context.Context, paths []string) (map[string][]byte, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	if f.files == nil {
		return nil, false, nil
	}
	out := map[string][]byte{}
	for _, p := range paths {
		if c, ok := f.files[p]; ok {
			out[p] = []byte(c)
		}
	}
	return out, true, nil
}

var builtinSentinel = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("X-Builtin", "1")
	io.WriteString(w, "builtin public page")
})

func TestThemeHandlerServesTheEnabledPackageWithCustomHeaders(t *testing.T) {
	h := ThemeHandler(fakeThemeFiles{files: map[string]string{
		"index.html":    "theme index",
		"assets/app.js": "console.log(1)",
		"style.css":     "body{}",
		"sub/page.txt":  "sub page",
	}}, builtinSentinel, slog.Default())
	for _, c := range []struct {
		path, want, contentType string
		status                  int
	}{
		{"/", "theme index", "text/html; charset=utf-8", 200},
		{"/index.html", "theme index", "text/html; charset=utf-8", 200},
		{"/assets/app.js", "console.log(1)", "text/javascript; charset=utf-8", 200},
		{"/style.css", "body{}", "text/css; charset=utf-8", 200},
		{"/sub/page.txt", "sub page", "text/plain; charset=utf-8", 200},
		// 客户端路由：包里没有的路径回落 index.html。
		{"/nodes/3", "theme index", "text/html; charset=utf-8", 200},
		{"/sub/../style.css", "body{}", "text/css; charset=utf-8", 200},
		// assets/ 下未命中 404：用 HTML 回应 script 标签会被浏览器按 MIME 拒绝，缺失要可见。
		{"/assets/missing.js", "", "", 404},
		{"/assets", "", "", 404},
	} {
		code, hdr, body := serveWithin(t, h, c.path)
		if code != c.status || (c.status == 200 && (body != c.want || hdr.Get("Content-Type") != c.contentType)) {
			t.Errorf("GET %s: %d %q %q, want %d %q %q", c.path, code, hdr.Get("Content-Type"), body, c.status, c.contentType, c.want)
		}
		if hdr.Get("X-Builtin") != "" {
			t.Errorf("GET %s: answered by the built-in page while a theme is enabled", c.path)
		}
		// 三条头对命中、回落与 404 一样。
		if hdr.Get("X-Content-Type-Options") != "nosniff" || hdr.Get("Content-Security-Policy") != "frame-ancestors 'none'" || hdr.Get("Cache-Control") != "no-cache" {
			t.Errorf("GET %s: headers nosniff=%q csp=%q cache=%q", c.path, hdr.Get("X-Content-Type-Options"), hdr.Get("Content-Security-Policy"), hdr.Get("Cache-Control"))
		}
		// 没有 Last-Modified：整秒的上传时刻分不出同一秒内的两次替换，不能作为 304 的依据。
		if hdr.Get("Last-Modified") != "" {
			t.Errorf("GET %s: Last-Modified %q", c.path, hdr.Get("Last-Modified"))
		}
	}
}

// 没有启用中的主题时主题 origin 服务内置公开页，任何路径都一样；不是 404。
func TestThemeHandlerFallsBackToBuiltinWithoutEnabledTheme(t *testing.T) {
	h := ThemeHandler(fakeThemeFiles{}, builtinSentinel, slog.Default())
	for _, p := range []string{"/", "/nodes/3", "/assets/app.js"} {
		code, hdr, body := serveWithin(t, h, p)
		if code != 200 || body != "builtin public page" || hdr.Get("X-Builtin") != "1" {
			t.Errorf("GET %s: %d %q, want the built-in public page", p, code, body)
		}
	}
}

// 读库失败是 500，而不是回落内置页：回落会把一次故障伪装成"主题被停用了"。
func TestThemeHandlerReportsStoreFailure(t *testing.T) {
	h := ThemeHandler(fakeThemeFiles{err: errors.New("disk I/O error")}, builtinSentinel, slog.Default())
	code, hdr, body := serveWithin(t, h, "/")
	if code != http.StatusInternalServerError || hdr.Get("X-Builtin") != "" || body == "builtin public page" {
		t.Fatalf("GET / on store failure: %d %q", code, body)
	}
	if hdr.Get("X-Content-Type-Options") != "nosniff" || hdr.Get("Cache-Control") != "no-cache" {
		t.Fatalf("GET / on store failure: headers %v", hdr)
	}
}
