package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeThemeSource 按 ThemeSource 的契约应答：files 为 nil 表示没有启用中的主题。每次整包读取计数，并像库一样返回新分配的
// 字节：逐请求读的实现因此按文件大小分配，分配量用例量得出来。
type fakeThemeSource struct {
	gen   atomic.Uint64
	reads atomic.Int64
	delay time.Duration // 每次整包读取的耗时，让并发请求在读的过程中到达。
	mu    sync.Mutex
	files map[string]string
	err   error
}

func (f *fakeThemeSource) ThemeGeneration() uint64 { return f.gen.Load() }

func (f *fakeThemeSource) EnabledThemePackage(context.Context) (uint64, map[string][]byte, bool, error) {
	f.reads.Add(1)
	gen := f.gen.Load()
	time.Sleep(f.delay)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, nil, false, f.err
	}
	if f.files == nil {
		return gen, nil, false, nil
	}
	out := make(map[string][]byte, len(f.files))
	for p, c := range f.files {
		out[p] = []byte(c)
	}
	return gen, out, true, nil
}

// set 像三个写者之一那样换掉启用中的主题：先提交内容，再递增代数。
func (f *fakeThemeSource) set(files map[string]string) {
	f.mu.Lock()
	f.files = files
	f.mu.Unlock()
	f.gen.Add(1)
}

func etagOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

var builtinSentinel = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("X-Builtin", "1")
	io.WriteString(w, "builtin public page")
})

func TestThemeHandlerServesTheEnabledPackageWithCustomHeaders(t *testing.T) {
	h := ThemeHandler(&fakeThemeSource{files: map[string]string{
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
		// 没有 Last-Modified：整秒的上传时刻分不出同一秒内的两次替换，不能作为 304 的依据。校验器是按所服务内容算的
		// 强 ETag（回落的是 index.html 的），404 不带。
		if hdr.Get("Last-Modified") != "" {
			t.Errorf("GET %s: Last-Modified %q", c.path, hdr.Get("Last-Modified"))
		}
		wantETag := ""
		if c.status == 200 {
			wantETag = etagOf(c.want)
		}
		if got := hdr.Get("ETag"); got != wantETag {
			t.Errorf("GET %s: ETag %q, want %q", c.path, got, wantETag)
		}
	}
}

// 没有启用中的主题时主题 origin 服务内置公开页，任何路径都一样；不是 404。
func TestThemeHandlerFallsBackToBuiltinWithoutEnabledTheme(t *testing.T) {
	h := ThemeHandler(&fakeThemeSource{}, builtinSentinel, slog.Default())
	for _, p := range []string{"/", "/nodes/3", "/assets/app.js"} {
		code, hdr, body := serveWithin(t, h, p)
		if code != 200 || body != "builtin public page" || hdr.Get("X-Builtin") != "1" {
			t.Errorf("GET %s: %d %q, want the built-in public page", p, code, body)
		}
	}
}

// 读库失败是 500，而不是回落内置页：回落会把一次故障伪装成"主题被停用了"。失败不留下快照，库恢复后下一个请求就读到。
func TestThemeHandlerReportsStoreFailure(t *testing.T) {
	src := &fakeThemeSource{err: errors.New("disk I/O error"), files: map[string]string{"index.html": "theme index"}}
	h := ThemeHandler(src, builtinSentinel, slog.Default())
	code, hdr, body := serveWithin(t, h, "/")
	if code != http.StatusInternalServerError || hdr.Get("X-Builtin") != "" || body == "builtin public page" {
		t.Fatalf("GET / on store failure: %d %q", code, body)
	}
	if hdr.Get("X-Content-Type-Options") != "nosniff" || hdr.Get("Cache-Control") != "no-cache" {
		t.Fatalf("GET / on store failure: headers %v", hdr)
	}
	src.mu.Lock()
	src.err = nil
	src.mu.Unlock()
	if code, _, body := serveWithin(t, h, "/"); code != http.StatusOK || body != "theme index" {
		t.Fatalf("GET / after the store recovered: %d %q, want the theme", code, body)
	}
}

// 代数不变时所有请求共用一次整包读取；代数一变，下一个请求就读到新内容，停用后回落内置页。
func TestThemeHandlerReadsThePackageOncePerGeneration(t *testing.T) {
	src := &fakeThemeSource{files: map[string]string{"index.html": "v1 index", "assets/app.js": "v1 app"}}
	h := ThemeHandler(src, builtinSentinel, slog.Default())
	expect := func(when, path, want string, reads int64) {
		t.Helper()
		if code, _, body := serveWithin(t, h, path); code != http.StatusOK || body != want {
			t.Fatalf("%s: GET %s: %d %q, want %q", when, path, code, body, want)
		}
		if got := src.reads.Load(); got != reads {
			t.Fatalf("%s: %d package reads, want %d", when, got, reads)
		}
	}
	for i := range 50 {
		expect("first generation", []string{"/", "/assets/app.js", "/nodes/3"}[i%3], map[bool]string{true: "v1 app", false: "v1 index"}[i%3 == 1], 1)
	}
	src.set(map[string]string{"index.html": "v2 index", "assets/app.js": "v2 app"})
	expect("after a replacement", "/assets/app.js", "v2 app", 2)
	for range 50 {
		expect("second generation", "/", "v2 index", 2)
	}
	src.set(nil)
	expect("after disabling", "/", "builtin public page", 3)
	expect("still disabled", "/nodes/3", "builtin public page", 3)
}

// 代数变了之后同时到达的请求只触发一次整包读取：拿到重读锁的请求读库，其余在锁上等，拿到锁后见代数已对上就直接用新快照。
func TestThemeHandlerConcurrentRequestsAfterAChangeReadOnce(t *testing.T) {
	src := &fakeThemeSource{files: map[string]string{"index.html": "v1"}, delay: 50 * time.Millisecond}
	h := ThemeHandler(src, builtinSentinel, slog.Default())
	if code, _, body := serveWithin(t, h, "/"); code != http.StatusOK || body != "v1" {
		t.Fatalf("GET /: %d %q", code, body)
	}
	src.set(map[string]string{"index.html": "v2"})
	const n = 32
	start := make(chan struct{})
	bodies := make(chan string, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			<-start
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			bodies <- rec.Body.String()
		})
	}
	close(start)
	wg.Wait()
	close(bodies)
	for b := range bodies {
		if b != "v2" {
			t.Errorf("concurrent GET / after the change: %q, want v2", b)
		}
	}
	if got := src.reads.Load(); got != 2 {
		t.Fatalf("%d package reads for %d concurrent requests after one change, want 2 in total (one per generation)", got, n)
	}
}

// 强 ETag 让未变的文件回 304；内容换了（哪怕同一秒内）ETag 随之改变，带旧 ETag 的条件请求拿到新内容。
func TestThemeHandlerRevalidatesByContentETag(t *testing.T) {
	src := &fakeThemeSource{files: map[string]string{"index.html": "v1 index", "assets/app.js": "v1 app"}}
	h := ThemeHandler(src, builtinSentinel, slog.Default())
	get := func(path, ifNoneMatch string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	first := get("/assets/app.js", "")
	e1 := first.Header().Get("ETag")
	if first.Code != http.StatusOK || e1 != etagOf("v1 app") {
		t.Fatalf("GET /assets/app.js: %d ETag %q, want 200 with the content's ETag", first.Code, e1)
	}
	if r := get("/assets/app.js", e1); r.Code != http.StatusNotModified || r.Body.Len() != 0 {
		t.Fatalf("GET /assets/app.js with its ETag: %d %q, want 304 without a body", r.Code, r.Body.String())
	}
	// 回落到 index.html 的路径按 index.html 的内容校验。
	if r := get("/nodes/3", etagOf("v1 index")); r.Code != http.StatusNotModified {
		t.Fatalf("GET /nodes/3 with index.html's ETag: %d, want 304", r.Code)
	}
	src.set(map[string]string{"index.html": "v1 index", "assets/app.js": "v2 app"})
	r := get("/assets/app.js", e1)
	if r.Code != http.StatusOK || r.Body.String() != "v2 app" || r.Header().Get("ETag") != etagOf("v2 app") || r.Header().Get("ETag") == e1 {
		t.Fatalf("GET /assets/app.js with the old ETag after a replacement: %d %q ETag %q, want 200 \"v2 app\" with a new ETag", r.Code, r.Body.String(), r.Header().Get("ETag"))
	}
}

// discardWriter 丢弃响应体且不按响应大小分配：ReadFrom 交给 io.Discard（它的缓冲取自池）。httptest.ResponseRecorder
// 会把整个响应体缓冲下来，用它量分配，量到的是它自己。
type discardWriter struct {
	header http.Header
	code   int
	n      int64
}

func (d *discardWriter) Header() http.Header { return d.header }
func (d *discardWriter) WriteHeader(code int) {
	if d.code == 0 {
		d.code = code
	}
}
func (d *discardWriter) Write(p []byte) (int, error) {
	d.WriteHeader(http.StatusOK)
	d.n += int64(len(p))
	return len(p), nil
}
func (d *discardWriter) ReadFrom(r io.Reader) (int64, error) {
	d.WriteHeader(http.StatusOK)
	n, err := io.Copy(io.Discard, r)
	d.n += n
	return n, err
}

// allocBytesPerRequest 是 GET path 每个请求在堆上新分配的平均字节数（runtime.MemStats.TotalAlloc 之差），不含第一次请求
// 建快照的那一次。
func allocBytesPerRequest(t *testing.T, h http.Handler, path string, size int64) float64 {
	t.Helper()
	serve := func() *discardWriter {
		w := &discardWriter{header: http.Header{}}
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}
	if w := serve(); w.code != http.StatusOK || w.n != size {
		t.Fatalf("GET %s: %d, %d bytes, want 200 with %d bytes", path, w.code, w.n, size)
	}
	const runs = 20
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range runs {
		serve()
	}
	runtime.ReadMemStats(&after)
	return float64(after.TotalAlloc-before.TotalAlloc) / runs
}

// 每个请求的分配量与所服务文件的大小无关：一个包里是 16 MiB 的文件、另一个包里是 1 KiB 的文件，各自每请求分配同一
// 量级，都远小于文件本身。匿名慢连接因此放大不了 hub 的内存——每个请求只在共享快照上开一个 reader，不各自持有一份
// 文件内容。两个包分开装：放在同一个包里时，逐请求读整包的实现对两个文件的分配一样大，比不出与文件大小的关系。
func TestThemeHandlerAllocationPerRequestIsIndependentOfFileSize(t *testing.T) {
	const small, big = 1 << 10, 16 << 20
	perRequest := func(size int) float64 {
		t.Helper()
		src := &fakeThemeSource{files: map[string]string{"index.html": "theme index", "assets/file.bin": strings.Repeat("x", size)}}
		return allocBytesPerRequest(t, ThemeHandler(src, builtinSentinel, slog.Default()), "/assets/file.bin", int64(size))
	}
	perSmall, perBig := perRequest(small), perRequest(big)
	t.Logf("bytes allocated per request: %.0f for a %d-byte file, %.0f for a %d-byte file", perSmall, small, perBig, big)
	if perBig > 2*perSmall+4096 || perBig > big/256 {
		t.Fatalf("serving a %d-byte file allocates %.0f bytes per request (%.0f for a %d-byte file); per-request allocation must not grow with file size", big, perBig, perSmall, small)
	}
}
