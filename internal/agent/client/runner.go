package client

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"time"

	"connectrpc.com/connect"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/agent/collect"
	"github.com/xjetry/probe/internal/agent/prober"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/probelimit"
)

type Runner struct {
	Collector *collect.Collector
	Client    probev1connect.AgentServiceClient
	Token     string
	Clock     clock.Clock
	Sleep     func(context.Context, time.Duration) error
	Rand      func() float64
	Log       *slog.Logger
	Prober    *prober.Scheduler
	Results   *prober.Queue
	// Interval 是收到第一个响应之前使用的间隔；之后一律用 hub 下发的。
	Interval time.Duration
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
// 失败退避、成功即回到下发间隔；实时指标不缓存，探测结果在迟到预算内重试。
func (r *Runner) Run(ctx context.Context) error {
	if r.Sleep == nil {
		r.Sleep = sleepReal
	}
	if r.Rand == nil {
		r.Rand = rand.Float64
	}
	interval := r.Interval
	sendFacts := true
	attempt := 0
	for {
		m, err := r.Collector.Metrics()
		if err != nil {
			r.Log.Warn("partial collection", "err", err)
		}
		req := connect.NewRequest(&probev1.ReportRequest{Metrics: m})
		var taken []prober.Result
		if r.Results != nil {
			now := r.Clock.Mono()
			taken = r.Results.Take(now, probelimit.MaxResultAge)
			req.Msg.ProbeResults = prober.ToProto(taken, now)
		}
		if r.Prober != nil {
			req.Msg.TasksVersion = r.Prober.Version()
		}
		req.Header().Set("Authorization", "Bearer "+r.Token)
		// Facts 只读几个小文件；每轮重算才能让 hub 从摘要变化发现运行期间的变更。
		f := r.Collector.Facts()
		hash := FactsHash(f)
		if sendFacts {
			req.Msg.Facts = f
		}
		req.Msg.FactsHash = hash

		resp, err := r.Client.Report(ctx, req)
		if err != nil {
			if r.Results != nil {
				// hub 对结构非法结果整条拒绝；回队会让后续上报反复携带同一批坏结果。
				// 其他失败保留本批结果，下次 Take 丢弃超过迟到预算的部分。
				if connect.CodeOf(err) == connect.CodeInvalidArgument {
					r.Log.Warn("discarding rejected probe results", "count", len(taken), "err", err)
				} else {
					r.Results.Requeue(taken)
				}
			}
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
		if resp.Msg.Tasks != nil && r.Prober != nil {
			r.Prober.Apply(resp.Msg.Tasks)
		}
		sendFacts = resp.Msg.WantFacts
		if ms := resp.Msg.ReportIntervalMs; ms > 0 {
			interval = time.Duration(ms) * time.Millisecond
		}
		if err := r.Sleep(ctx, interval); err != nil {
			return err
		}
	}
}
