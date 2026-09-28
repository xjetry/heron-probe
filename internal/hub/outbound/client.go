// Package outbound 是 hub 主动出站的 HTTP 边界。通知渠道与 S3 备份共用这里的重定向禁令、应答摘要、去掉 URL 的
// 错误文本与状态码合法性判定；时限不在这里定，由各消费方按自己的请求与应答体量给出并写推导。
package outbound

import (
	"net"
	"net/http"
	"time"
)

// 3xx 原样交回，由调用方按非 2xx 处理：默认跳转可能向配置之外的目标泄漏原 URL 的路径与查询串（Referer，
// 如 Telegram 路径里的 token、S3 对象路径或预签名 URL 的签名查询串），307/308 还可能重发可重放的正文。
// Go 1.27.1 实测跨不同主机去掉 Authorization，同主机或原主机的子域保留；不能把去掉此头当作整个请求不泄密。
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// NewClient 是有总时限的出站客户端：timeout 覆盖建连、写请求与读完应答体，适合请求与应答都有小上界的消费方。
// 0 在 http.Client 里表示不限时，是放宽方向，这里拒绝它。
func NewClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		panic("outbound.NewClient: timeout must be positive")
	}
	return &http.Client{Timeout: timeout, CheckRedirect: noRedirect}
}

// NewTransferClient 不设总时限，给体量随对象变化的传输：总时长由调用方按对象大小给 context 截止时间，客户端只限
// 建连（DNS 加 TCP、TLS 握手各自不超过 connect）与首字节（请求连同正文写完之后到收到响应头，不超过 firstByte）。
// 首字节之后没有任何时限：应答体慢速到达时，只有 context 的截止时间能让请求结束，调用方必须给出它。
func NewTransferClient(connect, firstByte time.Duration) *http.Client {
	if connect <= 0 || firstByte <= 0 {
		panic("outbound.NewTransferClient: connect and firstByte must be positive")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	// KeepAlive 与 http.DefaultTransport 的拨号器相同，只换建连时限。
	tr.DialContext = (&net.Dialer{Timeout: connect, KeepAlive: 30 * time.Second}).DialContext
	tr.TLSHandshakeTimeout = connect
	tr.ResponseHeaderTimeout = firstByte
	return &http.Client{Transport: tr, CheckRedirect: noRedirect}
}
