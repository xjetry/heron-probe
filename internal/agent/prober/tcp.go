package prober

import (
	"context"
	"net"
	"time"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/clock"
)

// TCP 只测量连接建立耗时；解析与连接共用预算，解析失败与地址非法是 error。
// 连接或发送失败经 classify 区分可达性与本地故障。
type TCP struct {
	Clock       clock.Clock
	Resolver    *net.Resolver
	DialContext func(context.Context, string, string) (net.Conn, error)
}

func (p TCP) Probe(ctx context.Context, t *probev1.ProbeTask) Outcome {
	timeout := time.Duration(t.GetTimeoutMs()) * time.Millisecond
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	host, port, err := net.SplitHostPort(t.GetTarget())
	if err != nil {
		return Outcome{Err: err.Error()}
	}
	ip, err := resolve(ctx, p.Resolver, host, true, true)
	if err != nil {
		return Outcome{Err: err.Error()}
	}
	start := p.Clock.Mono()
	dial := p.DialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	conn, err := dial(ctx, "tcp", net.JoinHostPort(ip.String(), port))
	if err != nil {
		return classify(err)
	}
	elapsed := p.Clock.Mono() - start
	conn.Close()
	// CheckTask 把任务超时限制在 hub 接受的 RTT 上限内；测得耗时超过任务预算时只回报超时。
	if elapsed > timeout {
		return Outcome{Timeout: true}
	}
	return Outcome{RttUs: uint32(elapsed / time.Microsecond)}
}
