package prober

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/clock"
)

// HTTP 测量一次 GET 从拨号到收到响应头的耗时；解析与整个请求（连接、TLS、等响应头）共用预算。
// URL 非法、解析失败与本地策略拒绝是 error；状态 ≥400 与 TLS 握手失败（含证书错误）计入丢包，
// 连接失败经 classify 区分。不跟随重定向（3xx 本身就是答案）、不复用连接、不读正文：
// 探的是"这个 URL 能不能给出 <400 的响应头"，不是内容。
type HTTP struct {
	Clock   clock.Clock
	Targets Targets
	Version string // User-Agent 用 heron-agent/<Version>，与 heron-agent version 命令同源。
}

func (p HTTP) Probe(ctx context.Context, t *heronv1.ProbeTask) Outcome {
	timeout := time.Duration(t.GetTimeoutMs()) * time.Millisecond
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	u, err := url.Parse(t.GetTarget())
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return Outcome{Err: fmt.Sprintf("target %q is not an absolute http or https URL", t.GetTarget())}
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	ip, err := p.Targets.Resolve(ctx, u.Hostname(), true, true)
	if err != nil {
		return Outcome{Err: err.Error()}
	}
	start := p.Clock.Mono()
	// 只连解析出的那个地址：DialContext 忽略传入地址，SNI 与 Host 仍是 URL 里的名字。
	transport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Outcome{Err: err.Error()}
	}
	req.Header.Set("User-Agent", "heron-agent/"+p.Version)
	resp, err := client.Do(req)
	if err != nil {
		return classify(err)
	}
	elapsed := p.Clock.Mono() - start
	// 拿到响应头即完成测量，不读正文。
	resp.Body.Close()
	// CheckTask 把任务超时限制在 hub 接受的 RTT 上限内；测得耗时超过任务预算时只回报超时。
	if elapsed > timeout {
		return Outcome{Timeout: true}
	}
	if resp.StatusCode >= 400 {
		return Outcome{Timeout: true}
	}
	return Outcome{RttUs: uint32(elapsed / time.Microsecond)}
}
