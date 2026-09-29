package client

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"connectrpc.com/connect"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/gen/heron/v1/heronv1connect"
	"github.com/xjetry/heron-probe/internal/agent/collect"
	"github.com/xjetry/heron-probe/internal/agent/prober"
	"github.com/xjetry/heron-probe/internal/agentwire"
	"github.com/xjetry/heron-probe/internal/clock"
	"github.com/xjetry/heron-probe/internal/probelimit"
)

type Runner struct {
	Collector *collect.Collector
	Client    heronv1connect.AgentServiceClient
	Token     string
	Clock     clock.Clock
	Sleep     func(context.Context, time.Duration) error
	Rand      func() float64
	Log       *slog.Logger
	// Prober 必填，持有与 hub 对账的完整清单及版本。
	Prober *prober.Scheduler
	// Results 必填，与 Prober 共用同一个结果队列。
	Results *prober.Queue
	// Interval 是收到第一个响应之前使用的间隔；之后用 hub 下发的，经 agentwire.ClampReportInterval 限定。
	Interval time.Duration
	// Network 只读后台检测结果；为空时不做出口探测，保持采集循环无额外网络依赖。
	Network interface{ Snapshot() *heronv1.NetworkInfo }
}

func sleepReal(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Run 每次携带 facts 摘要与任务版本供 hub 对账，间隔以响应为准。
// 失败退避、成功即回到下发间隔；实时指标不缓存，因为过期的实时数据没有意义。
// 探测结果在迟到预算内重试，InvalidArgument 例外：本批丢弃而不回队。
func (r *Runner) Run(ctx context.Context) error {
	if r.Prober == nil {
		return errors.New("Runner.Prober: required")
	}
	if r.Results == nil {
		return errors.New("Runner.Results: required")
	}
	if r.Sleep == nil {
		r.Sleep = sleepReal
	}
	if r.Rand == nil {
		r.Rand = rand.Float64
	}
	interval := r.Interval
	sendFacts := true
	attempt := 0
	var dropped uint64
	var clamped bool
	var clampedMs uint32
	for {
		m, err := r.Collector.Metrics()
		if err != nil {
			r.Log.Warn("partial collection", "err", err)
		}
		req := connect.NewRequest(&heronv1.ReportRequest{Metrics: m})
		// 超龄过滤与 age_ms 必须取同一时刻，否则刚通过过滤的结果可能以大于 MaxResultAge 的年龄发出并被 hub 丢弃。
		now := r.Clock.Mono()
		taken := r.Results.Take(now, probelimit.MaxResultAge, probelimit.MaxResultsPerReport)
		req.Msg.ProbeResults = prober.ToProto(taken, now)
		req.Msg.TasksVersion = r.Prober.Version()
		req.Header().Set("Authorization", "Bearer "+r.Token)
		// Facts 只读几个小文件；每轮重算才能让 hub 从摘要变化发现运行期间的变更。
		f := r.Collector.Facts()
		if r.Network != nil {
			f.Network = r.Network.Snapshot()
		}
		hash := FactsHash(f)
		if sendFacts {
			req.Msg.Facts = f
		}
		req.Msg.FactsHash = hash

		resp, err := r.Client.Report(ctx, req)
		if err != nil {
			// InvalidArgument 可来自 hub 对结构非法探测结果的拒绝、validateMetrics/validateFacts 对非法主机数据的拒绝，
			// 或 Connect 客户端解码/解压响应失败。前两种对同一内容的拒绝是确定性的：
			// 坏结果重发仍失败，非法主机数据持续时本批也只会在迟到预算内反复被拒。
			// hub 正常完成 Report 后的响应若解码/解压失败，本批已折叠，回队会重复入账。
			// 其他失败保留本批结果，下次 Take 丢弃超过迟到预算的部分。
			if connect.CodeOf(err) == connect.CodeInvalidArgument {
				if len(taken) > 0 {
					r.Log.Warn("discarding rejected probe results", "count", len(taken), "err", err)
				}
			} else {
				r.Results.Requeue(taken)
			}
		}
		if err == nil && resp.Msg.Tasks != nil {
			r.Prober.Apply(resp.Msg.Tasks)
		}
		if total := r.Results.Dropped(); total > dropped {
			r.Log.Warn("probe results dropped", "dropped", total-dropped)
			dropped = total
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			attempt++
			d := Backoff(attempt, interval, r.Rand)
			r.Log.Warn("report failed", "err", err, "attempt", attempt, "retry_in", d)
			if err := r.Sleep(ctx, d); err != nil {
				return err
			}
			continue
		}
		attempt = 0
		sendFacts = resp.Msg.WantFacts
		// 间隔来自 hub，而 hub 可能失守（§5.7）：越界值取边界，同一个越界值只告警一次，不随每次上报刷屏。
		ms := resp.Msg.ReportIntervalMs
		d, ok := agentwire.ClampReportInterval(ms)
		if !ok && (!clamped || ms != clampedMs) {
			r.Log.Warn("hub assigned a report interval outside the protocol bounds; using the nearest bound", "assigned_ms", ms, "using", d)
		}
		clamped, clampedMs = !ok, ms
		interval = d
		if err := r.Sleep(ctx, interval); err != nil {
			return err
		}
	}
}
