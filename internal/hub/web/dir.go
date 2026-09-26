package web

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
)

// DirHandler 服务运维用 --public-dir 放置的替换公开页。
//
// 文件经 os.Root 打开：路径与符号链接都越不出 dir，越界即打开失败，按不存在处理。每个请求重新打开 dir：
// 运维原子替换目录（rename）之后，下一个请求就读到新内容。打开带 O_NONBLOCK：目录里的 FIFO 在没有写端时
// 不会让请求挂住，随后的普通文件检查把它当作不存在；对普通文件的读取它不起作用。
// index.html 在构造时核对，必须是目录内的普通文件：它回答每一个不是文件的路径，缺了它 hub 不启动。
//
// 这个目录与面板同源，是信任边界之内的内容：目录里的脚本能读 /admin/，也能带着来访管理员的会话 cookie 调
// AdminService 的任意方法——同源请求照常带 cookie，SameSite=Strict 不拦同源。只放与 hub 二进制同等可信的内容；
// 不受信任的主题要放在另一个 origin，经 PublicService 取数。
func DirHandler(dir string) (http.Handler, error) {
	open := func(rel string) (fs.File, error) {
		root, err := os.OpenRoot(dir)
		if err != nil {
			return nil, err
		}
		defer root.Close()
		return root.OpenFile(filepath.FromSlash(rel), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	}
	f, err := open("index.html")
	if err != nil {
		return nil, fmt.Errorf("--public-dir %s: %w", dir, err)
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("--public-dir %s: index.html must be a regular file inside the directory; it answers every path that is not a file", dir)
	}
	return serveFiles("/", dirHeaders, func(string) string { return "no-cache" }, open), nil
}

// dirHeaders 只加 nosniff 与 frame-ancestors 'none'（§10）：目录由运维放置，严格 CSP 会让第三方主题的
// 字体与图片失效。不限制脚本的后果是目录里的脚本以面板的 origin 运行（DirHandler 的注释写了能做到什么）。
// 文件名不保证带内容哈希，所以一律 no-cache，404 也一样。
func dirHeaders(h http.Header) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "frame-ancestors 'none'")
	h.Set("Cache-Control", "no-cache")
}
