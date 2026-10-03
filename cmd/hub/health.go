package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// healthTimeout 是 health 子命令的请求总时限。它覆盖连接、请求与读响应体：Docker 的 HEALTHCHECK
// 与运维脚本按退出码判活，一次挂住的探测不能比这更久，否则探针自己成了新的故障点。
const healthTimeout = 3 * time.Second

func runHealth(args []string) error { return runHealthWith(args, os.Stdout) }

// runHealthWith 对 <url>/healthz 发 GET：2xx 打印 ok 并以 nil 返回，否则返回带原因（传输错误或状态码）的错误，
// 由 main 打到 stderr 并以非零退出。不跟随重定向——反代把 /healthz 302 到别处时，探针要看到真实的状态码，
// 而不是最终落地页的 200。scratch 镜像里没有 curl，所以这个子命令随二进制一起进镜像供 HEALTHCHECK 调用。
func runHealthWith(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	url := fs.String("url", "http://127.0.0.1:8080", "hub base URL; /healthz is appended; redirects are not followed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	base := strings.TrimSuffix(*url, "/")
	client := &http.Client{
		Timeout: healthTimeout,
		// 不跟随重定向：一个 3xx 必须是探测失败，探测的是"这个候选 hub 自己在不在服务"。
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Get(base + "/healthz")
	if err != nil {
		return fmt.Errorf("GET %s/healthz: %w", base, err)
	}
	defer resp.Body.Close()
	// 读尽并丢弃：连接要能被复用/干净关闭，正文本身没有内容可看。
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("GET %s/healthz: status %d", base, resp.StatusCode)
	}
	fmt.Fprintln(out, "ok")
	return nil
}
