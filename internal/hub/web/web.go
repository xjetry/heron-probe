// Package web 嵌入并服务管理面板的静态产物。
//
// 产物由 Vite 构建到本包的 dist 目录，不入库；目录里只保证有一个占位文件，
// 所以 embed 永远成立，而“有没有真的构建过”由 index.html 是否存在判定。
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Prefix 是面板的挂载路径；Vite 的 base 与之一致，产物里的资源引用都以它开头。
const Prefix = "/admin/"

const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

const notBuilt = `<!doctype html><meta charset="utf-8"><title>probe</title>
<p>The admin panel has not been built into this binary. Run <code>make web</code> before <code>go build</code>, or use a release build.</p>`

// Handler 服务 /admin/ 下的文件：命中直接返回；assets/ 下未命中返回 404 而不是
// index.html——用 HTML 回应 script 标签会被浏览器按 MIME 拒绝，404 才能让缺失可见；
// 其余路径回落到 index.html 交给客户端路由。
func Handler() http.Handler { return handlerFor(dist) }

func handlerFor(root fs.FS) http.Handler {
	sub, err := fs.Sub(root, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FS(sub)
	server := http.FileServer(files)
	built := true
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		built = false
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if !built {
			h.Set("Cache-Control", "no-cache")
			h.Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(notBuilt))
			return
		}
		rel := strings.TrimPrefix(r.URL.Path, Prefix)
		if rel == "" {
			rel = "index.html"
		}
		if rel == "index.html" {
			serveIndex(w, sub)
			return
		}
		if _, err := fs.Stat(sub, rel); err == nil {
			if strings.HasPrefix(rel, "assets/") {
				// 文件名带内容哈希，可以永久缓存。
				h.Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				h.Set("Cache-Control", "no-cache")
			}
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/" + rel
			server.ServeHTTP(w, r2)
			return
		}
		if strings.HasPrefix(rel, "assets/") {
			http.NotFound(w, r)
			return
		}
		serveIndex(w, sub)
	})
}

func serveIndex(w http.ResponseWriter, sub fs.FS) {
	b, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "index.html unreadable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(b)
}

// RootRedirect 把 / 送到面板：公开页尚不存在，根路径没有别的内容可以给。
func RootRedirect() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, Prefix, http.StatusFound)
	})
}
