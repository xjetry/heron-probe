package web

import (
	"bytes"
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"time"
)

// ThemeFiles 是主题托管读文件的来源；store.Store 满足它。paths 在一个快照里读出，返回的文件同属一个包；
// enabled 为假表示没有启用中的主题。
type ThemeFiles interface {
	EnabledThemeFiles(ctx context.Context, paths []string) (files map[string][]byte, enabled bool, err error)
}

// ThemeHandler 服务主题 origin（§10.1）上 RPC 之外的路径：启用中主题的文件，规则与 --public-dir 相同（serveFiles：
// 只服务包里的文件，assets/ 下未命中 404，其余回落 index.html），头也相同（customHeaders）。没有启用中的主题时交给
// builtin（内置公开页）：公开页是匿名入口，删掉或停用主题不能让它变成 404。
//
// 每个请求在一次读里取出请求路径与 index.html（EnabledThemeFiles 的同一个快照），opener 只从这两份里找——
// 这依赖 serveFiles 只打开 requestRel 与 index.html 这一条；分两次读，中间换了启用主题或整包替换就会拼出两个包。
// 文件内容整份读进内存再写出：不在向慢客户端写的同时占住库的读连接与快照。
func ThemeHandler(src ThemeFiles, builtin http.Handler, log *slog.Logger) http.Handler {
	serve := func(files map[string][]byte) http.Handler {
		open := func(rel string) (fs.File, error) {
			content, ok := files[rel]
			if !ok {
				return nil, fs.ErrNotExist
			}
			return &memFile{Reader: bytes.NewReader(content), name: path.Base(rel), size: int64(len(content))}, nil
		}
		return serveFiles("/", customHeaders, func(string) string { return "no-cache" }, open)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		files, enabled, err := src.EnabledThemeFiles(r.Context(), []string{requestRel(r.URL.Path, "/"), "index.html"})
		if err != nil {
			log.Error("reading theme files failed", "err", err, "path", r.URL.Path)
			customHeaders(w.Header())
			http.Error(w, "theme unavailable", http.StatusInternalServerError)
			return
		}
		if !enabled {
			builtin.ServeHTTP(w, r)
			return
		}
		serve(files).ServeHTTP(w, r)
	})
}

// memFile 是库里读出的一个主题文件。修改时间为零值：http.ServeContent 因此不发 Last-Modified，也不按 If-Modified-Since
// 回 304——整秒的上传时刻分不出同一秒内的两次替换，按它回 304 会让浏览器留着旧包的文件。内容类型由 ServeContent 按
// name 的扩展名定。
type memFile struct {
	*bytes.Reader
	name string
	size int64
}

func (f *memFile) Stat() (fs.FileInfo, error) { return f, nil }
func (f *memFile) Close() error               { return nil }
func (f *memFile) Name() string               { return f.name }
func (f *memFile) Size() int64                { return f.size }
func (f *memFile) Mode() fs.FileMode          { return 0o444 }
func (f *memFile) ModTime() time.Time         { return time.Time{} }
func (f *memFile) IsDir() bool                { return false }
func (f *memFile) Sys() any                   { return nil }
