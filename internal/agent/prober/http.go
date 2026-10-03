package prober

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/clock"
)

// CertReportInterval 是同一任务两次携带证书到期时刻的最小间隔：证书到期日按天变化，
// 每次探测都带是无意义的重复字节；一小时内能看到更换后的新到期日已经足够。测试引用它。
const CertReportInterval = time.Hour

// HTTP 测量一次 GET 从拨号到收到响应头的耗时；解析与整个请求（连接、TLS、等响应头）共用预算。
// URL 非法、解析失败与本地策略拒绝是 error；状态 ≥400 与 TLS 握手失败（含证书错误）计入丢包，
// 连接失败经 classify 区分。不跟随重定向（3xx 本身就是答案）、不复用连接、不读正文：
// 探的是"这个 URL 能不能给出 <400 的响应头"，不是内容。
//
// Probe 是指针接收者：证书携带的频率上限记录内嵌在 HTTP 里（certMu/certLast），任何构造方式
// （零值 &HTTP{...} 即可用，map 在锁内按需建立）上限都成立，不需要装配侧为字面量构造打补丁。
type HTTP struct {
	Clock   clock.Clock
	Targets Targets
	Version string // User-Agent 用 heron-agent/<Version>，与 heron-agent version 命令同源。
	// TLSClientConfig 只在测试里注入（如限死 TLS 版本构造对端 alert 的握手失败）；生产为 nil，用默认配置。
	TLSClientConfig *tls.Config
	// certLast 是每任务"上次携带证书到期时刻"的单调钟时刻（CertReportInterval）。它只影响是否
	// 重复携带，不影响任何测量值，因此不属于速率基线，不参与休眠检测的 ResetRates/Clear。
	certMu   sync.Mutex
	certLast map[uint64]time.Duration
}

func (p *HTTP) Probe(ctx context.Context, t *heronv1.ProbeTask) Outcome {
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
		TLSClientConfig:   p.TLSClientConfig,
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
	now := p.Clock.Mono()
	elapsed := now - start
	// 拿到响应头即完成测量，不读正文。
	resp.Body.Close()
	// CheckTask 把任务超时限制在 hub 接受的 RTT 上限内；测得耗时超过任务预算时只回报超时。
	if elapsed > timeout {
		return Outcome{Timeout: true}
	}
	if resp.StatusCode >= 400 {
		return Outcome{Timeout: true}
	}
	out := Outcome{RttUs: uint32(elapsed / time.Microsecond)}
	// HTTPS 握手成功（能走到这里即已完成握手）时顺带带回链首枚证书的到期时刻，
	// 每任务至多每小时一次（reportCert 承载频率上限）；状态 ≥400 已在上面折返，不携带。
	if u.Scheme == "https" && resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 && p.reportCert(t.GetId(), now) {
		out.CertNotAfter = resp.TLS.PeerCertificates[0].NotAfter.Unix()
	}
	return out
}

// reportCert 判定本次成功探测是否携带证书到期时刻；判定携带时把 now 记为该任务的上次携带时刻。
// 频率上限由 CertReportInterval 承载：一小时内能看到更换后的新到期日已经足够，每次都带是重复字节。
func (p *HTTP) reportCert(id uint64, now time.Duration) bool {
	p.certMu.Lock()
	defer p.certMu.Unlock()
	if p.certLast == nil {
		p.certLast = map[uint64]time.Duration{}
	}
	if last, ok := p.certLast[id]; ok && now-last < CertReportInterval {
		return false
	}
	p.certLast[id] = now
	return true
}

// pruneTasks 清掉已不在任务集里的任务的携带记录，由 Scheduler.Apply 在任务集更新后调用：
// 任务消失再出现时按首次探测处理。
func (p *HTTP) pruneTasks(alive map[uint64]struct{}) {
	p.certMu.Lock()
	defer p.certMu.Unlock()
	for id := range p.certLast {
		if _, ok := alive[id]; !ok {
			delete(p.certLast, id)
		}
	}
}
