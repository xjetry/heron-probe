// Package prober 在 agent 侧执行 hub 下发的延迟探测任务并暂存结果。
//
// 结果的时间基准是单调钟：入队时记 At，上报时折算成 age_ms，hub 用它反推测量时刻。
package prober

import (
	"context"
	"fmt"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
)

type Outcome struct {
	RttUs   uint32
	Timeout bool
	Err     string
}

// Engine 的 ctx 被调用方取消后返回的 Outcome 无意义，调用方必须丢弃。
type Engine interface {
	Probe(ctx context.Context, t *heronv1.ProbeTask) Outcome
}

// Multi 按任务种类分派；未知种类是 hub 与 agent 版本偏斜的信号，按 error 回报而不是静默跳过。
type Multi struct{ ICMP, TCP, HTTP, DNS Engine }

func (m Multi) Probe(ctx context.Context, t *heronv1.ProbeTask) Outcome {
	switch t.GetKind() {
	case heronv1.ProbeKind_PROBE_KIND_ICMP:
		return m.ICMP.Probe(ctx, t)
	case heronv1.ProbeKind_PROBE_KIND_TCP:
		return m.TCP.Probe(ctx, t)
	case heronv1.ProbeKind_PROBE_KIND_HTTP:
		return m.HTTP.Probe(ctx, t)
	case heronv1.ProbeKind_PROBE_KIND_DNS:
		return m.DNS.Probe(ctx, t)
	}
	return Outcome{Err: fmt.Sprintf("unsupported probe kind %s", t.GetKind())}
}
