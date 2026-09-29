package client

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"connectrpc.com/connect"

	"github.com/xjetry/heron-probe/gen/heron/v1/heronv1connect"
	"github.com/xjetry/heron-probe/internal/agentwire"
)

// NewServiceClient 是 agent 连 hub 的唯一构造，register 与 run 都经过它，下面几条对 hub 的约束因此对两条路径同时成立（§5.7）：
//   - 响应正文在 HTTP 层限读 agentwire.MaxResponseBytes，成功与错误响应都经过这一层。connect-go 的 ReadMaxBytes
//     只管成功响应的消息，错误响应的正文照读不误（TestClientBoundsErrorBody 在去掉这一层时红）。
//   - 不接受压缩：connect 不声明 gzip，Transport 也不自行声明与解压。于是 HTTP 层限的原始字节数就是解码前的全部大小，
//     hub 没法用一个小的压缩正文换出大块内存；hub 不顾声明仍回 gzip 时 connect 按不认识的编码报错。响应本来不超过
//     MaxResponseBytes，压缩没有收益。
//   - 不跟随重定向。http.Client 跟随同主机重定向时保留 Authorization 而不看协议，https 到同主机 http 的重定向
//     会把节点 token 明文发出；AgentService 没有需要重定向的场景。
func NewServiceClient(hub string, timeout time.Duration) heronv1connect.AgentServiceClient {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DisableCompression = true
	tr.MaxResponseHeaderBytes = maxResponseHeaderBytes
	hc := &http.Client{
		Timeout:       timeout,
		Transport:     limitedTransport{base: tr, max: agentwire.MaxResponseBytes},
		CheckRedirect: refuseRedirect,
	}
	return heronv1connect.NewAgentServiceClient(hc, hub, connect.WithAcceptCompression("gzip", nil, nil))
}

// maxResponseHeaderBytes 是响应头的上限。Go 的默认值是 10 MiB，远大于正文的 64 KiB；AgentService 的响应头只有
// Content-Type 与反代加的几行，32 KiB 容得下常见 CDN 附加的头。
const maxResponseHeaderBytes = 32 << 10

var errRedirect = errors.New("hub answered with a redirect; the agent does not follow redirects, so configure the final hub URL")

func refuseRedirect(*http.Request, []*http.Request) error { return errRedirect }

var errResponseTooLarge = fmt.Errorf("hub response body exceeds %d bytes", agentwire.MaxResponseBytes)

type limitedTransport struct {
	base http.RoundTripper
	max  int64
}

func (t limitedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	resp.Body = &limitedBody{rc: resp.Body, left: t.max}
	return resp, nil
}

// limitedBody 读满 left 字节后再读到任何数据即报错，而不是静默截断：截断的正文可能恰好仍能解码成一条更短的合法消息。
type limitedBody struct {
	rc   io.ReadCloser
	left int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.left <= 0 {
		var one [1]byte
		if n, err := b.rc.Read(one[:]); n > 0 {
			return 0, errResponseTooLarge
		} else {
			return 0, err
		}
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.rc.Read(p)
	b.left -= int64(n)
	return n, err
}

func (b *limitedBody) Close() error { return b.rc.Close() }
