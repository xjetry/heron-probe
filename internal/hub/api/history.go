package api

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/hub/live"
	"github.com/xjetry/heron-probe/internal/hub/metric"
	"github.com/xjetry/heron-probe/internal/hub/store"
)

const (
	defaultMaxPoints = 720
	maxMaxPoints     = 2000
	maxQuerySpan     = 400 * 24 * time.Hour
)

// history 是两族历史查询的唯一实现，管理端与公开端共用。两端在两处不同，都由调用方决定：
// 哪些节点可查（Service.requireNode / Public.requirePublic 先行裁决，这里不再看节点），
// 以及探测序列怎样标注（taskLabel）。
type history struct {
	store *store.Store
	log   *slog.Logger
}

// taskLabel 给出任务的种类与目标；ok 为 false 时两项留空，客户端退回编号。管理端传 probe.Registry.Target
// （任务当前的配置），公开端传 probe.Registry.TargetFor（只标当前分配给被查节点的任务）。
type taskLabel func(taskID uint64) (kind heronv1.ProbeKind, target string, ok bool)

// checkWindow 为两族查询、两个服务维持同一套窗口与点数约束；只看请求本身，不查库。
func checkWindow(from, to int64, requested uint32) (int, error) {
	if from < 0 {
		return 0, invalid("from must be a nonnegative Unix timestamp; got %d", from)
	}
	if from >= to {
		return 0, invalid("from (%d) must be earlier than to (%d)", from, to)
	}
	// 两族水位从 Unix epoch 开始；非负秒差直接比较，避免转换纳秒时溢出。
	if span := to - from; span > int64(maxQuerySpan/time.Second) {
		return 0, invalid("window spans %d seconds; the maximum is %d (400 days)", to-from, int64(maxQuerySpan/time.Second))
	}
	maxPoints := int(requested)
	if maxPoints == 0 {
		maxPoints = defaultMaxPoints
	}
	if maxPoints > maxMaxPoints {
		return 0, invalid("max_points must be at most %d; got %d", maxMaxPoints, maxPoints)
	}
	return maxPoints, nil
}

func (h history) metrics(ctx context.Context, m *heronv1.QueryMetricsRequest, maxPoints int) (*heronv1.QueryMetricsResponse, error) {
	lv, step := store.ChooseLevel(m.GetFrom(), m.GetTo(), maxPoints)
	rows, err := h.store.QueryMetrics(ctx, m.GetNodeId(), m.GetFrom(), m.GetTo(), lv, step)
	if err != nil {
		h.log.Error("metric query failed", "err", err)
		return nil, internalError("metric query failed")
	}
	resp := &heronv1.QueryMetricsResponse{Level: lv.Name, StepS: uint32(step)}
	series := make([]*heronv1.MetricSeries, len(metric.Columns))
	for i, c := range metric.Columns {
		series[i] = &heronv1.MetricSeries{Name: c.Name, Unit: c.Unit, Samples: make([]*heronv1.MetricSample, 0, len(rows))}
	}
	for _, r := range rows {
		resp.Ts = append(resp.Ts, r.TS)
		for i, c := range metric.Columns {
			sample := &heronv1.MetricSample{N: r.Bucket.N[i]}
			switch {
			case c.Kind == metric.Sum:
				// 可加量下发和，不下发均值：一分钟内的字节数除以入账次数没有意义。
				if r.Bucket.N[i] > 0 {
					sample.Sum = proto.Float64(r.Bucket.Sum[i])
				}
			default:
				if mean, ok := r.Bucket.Mean(i); ok {
					sample.Mean = proto.Float64(mean)
					if c.Kind == metric.MeanMax {
						sample.Max = proto.Float64(r.Bucket.Max[i])
					}
				}
			}
			series[i].Samples = append(series[i].Samples, sample)
		}
	}
	resp.Series = series
	return resp, nil
}

func (h history) probeSeries(ctx context.Context, m *heronv1.QueryProbesRequest, maxPoints int, label taskLabel, order []uint64) (*heronv1.QueryProbesResponse, error) {
	lv, step := store.ChooseLevel(m.GetFrom(), m.GetTo(), maxPoints)
	rows, err := h.store.QueryProbes(ctx, m.GetNodeId(), m.GetFrom(), m.GetTo(), lv, step)
	if err != nil {
		h.log.Error("probe query failed", "err", err)
		return nil, internalError("probe query failed")
	}
	resp := &heronv1.QueryProbesResponse{Level: lv.Name, StepS: uint32(step)}
	var cur *heronv1.ProbeSeries
	for _, r := range rows { // store 已按 TaskID、TS 排序
		if r.Bucket.Sent == 0 {
			continue
		}
		if cur == nil || cur.TaskId != r.TaskID {
			cur = &heronv1.ProbeSeries{TaskId: r.TaskID}
			cur.Kind, cur.Target, _ = label(r.TaskID)
			resp.Series = append(resp.Series, cur)
		}
		sample := &heronv1.ProbeSample{Ts: r.TS, Sent: r.Bucket.Sent, Lost: r.Bucket.Lost, Errors: r.Bucket.Errors}
		if mean, ok := r.Bucket.RttMean(); ok {
			sample.RttMeanUs, sample.RttMinUs, sample.RttMaxUs = proto.Uint32(mean), proto.Uint32(r.Bucket.RttMinUs), proto.Uint32(r.Bucket.RttMaxUs)
		}
		cur.Samples = append(cur.Samples, sample)
	}
	rank := make(map[uint64]int, len(order))
	for i, id := range order {
		rank[id] = i
	}
	sort.Slice(resp.Series, func(i, j int) bool {
		a, b := resp.Series[i].TaskId, resp.Series[j].TaskId
		ra, knownA := rank[a]
		rb, knownB := rank[b]
		if knownA != knownB {
			return knownA
		}
		if knownA {
			return ra < rb
		}
		return a < b
	})
	return resp, nil
}

// liveState 是两端快照共用的在线判定：在线只来自 live；库里的 last_seen_at 只在 live 没有该节点
// （hub 重启后尚未再上报）时用来展示"上次见到"，不参与在线判定。
func liveState(l *live.Live, n store.Node) (online bool, lastSeen *int64, m *heronv1.Metrics) {
	if e, ok := l.Get(n.ID); ok {
		return e.Online, proto.Int64(e.LastSeenWall.Unix()), e.Metrics
	}
	if !n.LastSeenAt.IsZero() {
		return false, proto.Int64(n.LastSeenAt.Unix()), nil
	}
	return false, nil, nil
}
