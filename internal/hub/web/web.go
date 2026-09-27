// Package web 服务 hub 的静态页面：嵌入的管理面板（/admin/）、嵌入的内置公开页（/），以及运维用
// --public-dir 指定的替换目录。三者共用 serveFiles。
//
// 两份嵌入产物由 Vite 构建到本包的 dist 与 dist-public 目录，不入库；目录里只保证有一个占位文件，
// 所以 embed 永远成立，而"有没有真的构建过"由 index.html 是否存在判定。
package web

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var adminDist embed.FS

//go:embed all:dist-public
var publicDist embed.FS

// Prefix 是面板的挂载路径；Vite 的 base 与之一致，产物里的资源引用都以它开头。
const Prefix = "/admin/"

// csp 是面板与内置公开页共用的策略（§10）。style-src 带 'unsafe-inline'，只因为公开页把站点设置的自定义 CSS
// 写进一个 <style> 元素；元素的 style 经 CSSOM 写入（React 的 style 属性、uPlot），不受 style-src 约束。
// img-src 带 data:：logo 是 data: URL。
const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

const notBuiltAdmin = `<!doctype html><meta charset="utf-8"><title>probe</title>
<p>The admin panel has not been built into this binary. Run <code>make web</code> before <code>go build</code>, or use a release build.</p>`

const notBuiltPublic = `<!doctype html><meta charset="utf-8"><title>probe</title>
<p>The public page has not been built into this binary. Run <code>make web</code> before <code>go build</code>, or use a release build.</p>`

// Handler 服务 /admin/ 下的管理面板。
func Handler() http.Handler { return embedded(adminDist, "dist", Prefix, notBuiltAdmin) }

// PublicHandler 服务挂在 / 的内置公开页。
func PublicHandler() http.Handler { return embedded(publicDist, "dist-public", "/", notBuiltPublic) }

// closedPage 是总闸关闭时 assets/ 之外的公开路径得到的页面。
const closedPage = `<!doctype html><meta charset="utf-8"><title>probe</title><p>公开页已关闭</p>`

// PublicGate 统一包住内置页与自定义目录；只挂在公开根路径，管理面板和 RPC 由 mux 的更具体路由承载。
// 关闭时不调用文件服务，任何脚本或自定义资源都拿不到：文件路径（如 /theme.js）得到的是说明页而不是文件内容。
//
// 关闭时的分流沿用 serveFiles 的回落规则，同一个 relPath 与 underAssets：assets/ 下 404，其余路径回说明页。
// 开闸时 serveFiles 把 assets/ 之外缺失的路径交给客户端路由，分享出去的 /nodes/3 是一个页面；两处规则一旦分叉，
// 同一个前端路由关闸后就成了 404，访客分不清是站点关了还是链接失效。说明页与 404 都带内置页的安全头，
// 不随来源（--public-dir 的 dirHeaders）放宽。
func PublicGate(next http.Handler, enabled func() bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if enabled() {
			next.ServeHTTP(w, r)
			return
		}
		builtinHeaders(w.Header())
		w.Header().Set("Cache-Control", "no-store")
		if underAssets(relPath(r.URL.Path, "/")) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, closedPage)
	})
}

func builtinHeaders(h http.Header) {
	h.Set("Content-Security-Policy", csp)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
}

// embedded 服务一份嵌入产物：assets/ 下的文件名带内容哈希，可以永久缓存，其余 no-cache。
// 没构建过（没有 index.html）时每个路径都回答一页说明。
func embedded(root fs.FS, dir, prefix, notBuilt string) http.Handler {
	sub, err := fs.Sub(root, dir)
	if err != nil {
		panic(err)
	}
	if info, err := fs.Stat(sub, "index.html"); err != nil || !info.Mode().IsRegular() {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			h := w.Header()
			builtinHeaders(h)
			h.Set("Cache-Control", "no-cache")
			h.Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, notBuilt)
		})
	}
	cacheFor := func(rel string) string {
		if underAssets(rel) {
			return "public, max-age=31536000, immutable"
		}
		return "no-cache"
	}
	return serveFiles(prefix, builtinHeaders, cacheFor, sub.Open)
}

// opener 打开 rel：rel 已按 URL 路径语义清理，相对挂载根，不以 / 开头，不含 ..。
// 打开不得阻塞：FIFO 在没有写端时挂住的是 open 本身，serveFiles 在打开之后才看文件类型，兜不住这一步。
// DirHandler 以 O_NONBLOCK 打开；embed 里没有特殊文件。新增来源要自己满足这一条。
type opener func(rel string) (fs.File, error)

// serveFiles 是三处静态服务共用的核心。命中普通文件就返回它；rel 在 assets 之下（underAssets）而未命中时返回 404——
// 用 HTML 回应 script 标签会被浏览器按 MIME 拒绝，404 才能让缺失可见；其余路径回落到 index.html，交给客户端路由。
// 不在特殊文件上挂住靠两条各自的事实：打开本身不阻塞由 opener 保证；打开之后不是普通文件（目录、FIFO、设备）
// 就当作不存在、不读，所以任何来源都不列目录，也不从 FIFO 与设备读。
// 点文件同样当作不存在：路径任一段以 . 开头就不打开（hidden），.git/config、.env 这类运维放目录时顺手带进来的
// 文件因此不经任何来源服务。判定只看请求路径里各段的名字，符号链接按链接自己的名字算。.well-known/ 也在其列，
// 需要它的（ACME 校验、security.txt）由反向代理提供。
func serveFiles(prefix string, headers func(http.Header), cacheFor func(rel string) string, open opener) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers(w.Header())
		rel := relPath(r.URL.Path, prefix)
		if rel == "" {
			rel = "index.html"
		}
		if !hidden(rel) && serveRegular(w, r, open, rel, cacheFor(rel)) {
			return
		}
		if underAssets(rel) {
			http.NotFound(w, r)
			return
		}
		if !serveRegular(w, r, open, "index.html", "no-cache") {
			http.Error(w, "index.html unreadable", http.StatusInternalServerError)
		}
	})
}

// underAssets 报告 rel 是否是 assets 目录本身或在它之下，按整段比较：assetsx/ 不算。缺失时 404、嵌入产物的
// 永久缓存与总闸关闭时的 404（PublicGate）都按它判定，同一个判定只有这一种写法。
func underAssets(rel string) bool {
	return rel == "assets" || strings.HasPrefix(rel, "assets/")
}

// hidden 报告 rel 是否有一段以 . 开头。rel 来自 relPath：经 path.Clean 清理，没有 . 与 .. 段，也不以 / 开头，
// 所以以 . 开头的段只能是点文件或点目录的名字。
func hidden(rel string) bool {
	return strings.HasPrefix(rel, ".") || strings.Contains(rel, "/.")
}

// relPath 先按 URL 路径语义清理再去掉挂载前缀。三种来源对 . 与 .. 的处理不同（embed 经 fs.ValidPath 一律拒绝，
// os.Root 接受不越界的 ..），先清理，同一个 URL 在三处才落到同一个文件名，assets/ 的 404 判定也看清理后的路径。
// 越界由来源自己拒绝（fs.ValidPath、os.Root），不靠这里。清理后不在前缀之下的（如 /admin 本身）按挂载根处理。
func relPath(urlPath, prefix string) string {
	if rest, ok := strings.CutPrefix(path.Clean("/"+urlPath), prefix); ok {
		return rest
	}
	return ""
}

// serveRegular 在 rel 是普通文件时写出它并返回 true；打不开或不是普通文件时返回 false，什么都不写。
func serveRegular(w http.ResponseWriter, r *http.Request, open opener, rel, cacheControl string) bool {
	f, err := open(rel)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	content, ok := f.(io.ReadSeeker)
	if !ok {
		return false
	}
	w.Header().Set("Cache-Control", cacheControl)
	http.ServeContent(w, r, info.Name(), info.ModTime(), content)
	return true
}
