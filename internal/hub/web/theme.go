package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"sync"
	"sync/atomic"
	"time"
)

// ThemeSource 是主题托管的内容来源；store.Store 满足它。
//
// ThemeGeneration 是启用中主题内容的代数：只增不减，内容每变一次至少增一，读它不碰库。EnabledThemePackage 在一次读里
// 取出启用中主题的整包（path → content）与读之前取到的代数：返回的文件同属一个包，内容至少与返回的代数一样新；
// enabled 为假表示没有启用中的主题。
type ThemeSource interface {
	ThemeGeneration() uint64
	EnabledThemePackage(ctx context.Context) (gen uint64, files map[string][]byte, enabled bool, err error)
}

// ThemeHandler 服务主题 origin（§10.1）上 RPC 之外的路径：启用中主题的文件，规则与 --public-dir 相同（serveFiles：
// 只服务包里的文件，assets/ 下未命中 404，其余回落 index.html），头也相同（customHeaders）。没有启用中的主题时交给
// builtin（内置公开页）：公开页是匿名入口，删掉或停用主题不能让它变成 404。
//
// 所有请求共享启用中主题的一份不可变快照（themeSnapshot）。快照标着读它之前的代数；请求时代数没变就直接从快照服务，
// 不碰库。代数变了由一个请求在 mu 下整包重读、原子换上新快照，同时到达的其余请求在 mu 上等它换完，拿到锁后再比一次
// 代数，同一代数只读一次库。快照的文件表建成后不再改动，请求只在它上面开 bytes.Reader，所以每个请求的分配量与文件大小
// 无关。常驻内存是当前快照一份（展开 ≤ 64 MiB），外加仍在向慢客户端写的请求所引用的旧快照；旧快照只因三个写者的提交
// 而产生，匿名请求的数量放大不了它——整份读进内存的是包，不是每个请求各一份。
//
// 读库失败回 500、不换快照，下一个请求重试：回落内置页会把一次故障伪装成"主题被停用了"。
func ThemeHandler(src ThemeSource, builtin http.Handler, log *slog.Logger) http.Handler {
	return &themeHandler{src: src, builtin: builtin, log: log}
}

type themeHandler struct {
	src     ThemeSource
	builtin http.Handler
	log     *slog.Logger
	cur     atomic.Pointer[themeSnapshot]
	mu      sync.Mutex // 串行化重读；cur 的读侧不取它。
}

// themeSnapshot 是某一代启用中主题的整包，建成后只读。
type themeSnapshot struct {
	gen     uint64
	enabled bool
	serve   http.Handler // enabled 为假时为 nil。
}

func (h *themeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	snap, err := h.snapshot(r.Context())
	if err != nil {
		h.log.Error("reading theme files failed", "err", err, "path", r.URL.Path)
		customHeaders(w.Header())
		http.Error(w, "theme unavailable", http.StatusInternalServerError)
		return
	}
	if !snap.enabled {
		h.builtin.ServeHTTP(w, r)
		return
	}
	snap.serve.ServeHTTP(w, r)
}

// snapshot 返回当前代数的快照，必要时重读。比较用相等：代数只增，快照标的是某个已读到的值，不相等就是库里有了更新的提交。
func (h *themeHandler) snapshot(ctx context.Context) (*themeSnapshot, error) {
	if s := h.cur.Load(); s != nil && s.gen == h.src.ThemeGeneration() {
		return s, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if s := h.cur.Load(); s != nil && s.gen == h.src.ThemeGeneration() {
		return s, nil
	}
	gen, files, enabled, err := h.src.EnabledThemePackage(ctx)
	if err != nil {
		return nil, err
	}
	s := &themeSnapshot{gen: gen, enabled: enabled}
	if enabled {
		s.serve = serveTheme(files)
	}
	h.cur.Store(s)
	return s, nil
}

// themeFile 是快照里的一个文件与按其内容算的强 ETag。
type themeFile struct {
	content []byte
	etag    string
}

// serveTheme 在整包上建静态服务，只在建快照时调用一次：ETag 在这里算好，请求时不再碰内容。
func serveTheme(files map[string][]byte) http.Handler {
	table := make(map[string]themeFile, len(files))
	for p, c := range files {
		sum := sha256.Sum256(c)
		table[p] = themeFile{content: c, etag: `"` + hex.EncodeToString(sum[:]) + `"`}
	}
	open := func(rel string) (fs.File, error) {
		f, ok := table[rel]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return &memFile{Reader: bytes.NewReader(f.content), name: path.Base(rel), size: int64(len(f.content)), etag: f.etag}, nil
	}
	return serveFiles("/", customHeaders, func(string) string { return "no-cache" }, open)
}

// memFile 是快照里的一个主题文件。修改时间为零值：http.ServeContent 因此不发 Last-Modified，也不按 If-Modified-Since
// 回 304——整秒的上传时刻分不出同一秒内的两次替换，按它回 304 会让浏览器留着旧包的文件。校验器是按内容算的强 ETag
// （serveRegular 经 entityTagger 发出），同一秒内的两次替换只要内容不同 ETag 就不同。内容类型由 ServeContent 按 name 的
// 扩展名定。
type memFile struct {
	*bytes.Reader
	name string
	size int64
	etag string
}

func (f *memFile) Stat() (fs.FileInfo, error) { return f, nil }
func (f *memFile) Close() error               { return nil }
func (f *memFile) Name() string               { return f.name }
func (f *memFile) Size() int64                { return f.size }
func (f *memFile) Mode() fs.FileMode          { return 0o444 }
func (f *memFile) ModTime() time.Time         { return time.Time{} }
func (f *memFile) IsDir() bool                { return false }
func (f *memFile) Sys() any                   { return nil }
func (f *memFile) EntityTag() string          { return f.etag }
