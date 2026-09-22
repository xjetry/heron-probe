package api

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/metric"
	"github.com/xjetry/probe/internal/hub/store"
)

const (
	defaultMaxPoints = 720
	maxMaxPoints     = 2000
	maxQuerySpan     = 400 * 24 * time.Hour
)

// GetSnapshot 的在线判定只来自 live；库里的 last_seen_at 只在 live 没有该节点
// （hub 重启后尚未再上报）时用来展示"上次见到"，不参与在线判定。
func (s *Service) GetSnapshot(ctx context.Context, _ *connect.Request[probev1.GetSnapshotRequest]) (*connect.Response[probev1.GetSnapshotResponse], error) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		s.log.Error("listing nodes failed", "err", err)
		return nil, internalError("listing nodes failed")
	}
	out := &probev1.GetSnapshotResponse{Now: s.clk.Now().Unix(), ReportIntervalMs: uint32(s.cfg.ReportInterval / time.Millisecond)}
	for _, n := range nodes {
		st := &probev1.NodeStatus{Id: n.ID, Name: n.Name}
		if e, ok := s.live.Get(n.ID); ok {
			st.Online = e.Online
			st.Metrics = e.Metrics
			st.LastSeenAt = proto.Int64(e.LastSeenWall.Unix())
		} else if !n.LastSeenAt.IsZero() {
			st.LastSeenAt = proto.Int64(n.LastSeenAt.Unix())
		}
		out.Nodes = append(out.Nodes, st)
	}
	return connect.NewResponse(out), nil
}

func (s *Service) QueryMetrics(ctx context.Context, req *connect.Request[probev1.QueryMetricsRequest]) (*connect.Response[probev1.QueryMetricsResponse], error) {
	m := req.Msg
	if m.GetFrom() < 0 {
		return nil, invalid("from must be a nonnegative Unix timestamp; got %d", m.GetFrom())
	}
	if m.GetFrom() >= m.GetTo() {
		return nil, invalid("from (%d) must be earlier than to (%d)", m.GetFrom(), m.GetTo())
	}
	// 指标水位从 Unix epoch 开始；非负秒差直接比较，避免转换纳秒时溢出。
	if span := m.GetTo() - m.GetFrom(); span > int64(maxQuerySpan/time.Second) {
		return nil, invalid("window spans %d seconds; the maximum is %d (400 days)", m.GetTo()-m.GetFrom(), int64(maxQuerySpan/time.Second))
	}
	maxPoints := int(m.GetMaxPoints())
	if maxPoints == 0 {
		maxPoints = defaultMaxPoints
	}
	if maxPoints > maxMaxPoints {
		return nil, invalid("max_points must be at most %d; got %d", maxMaxPoints, maxPoints)
	}
	exists, err := s.store.NodeExists(ctx, m.GetNodeId())
	if err != nil {
		return nil, internalError("looking up node failed")
	}
	if !exists {
		return nil, notFound(m.GetNodeId())
	}
	lv, step := store.ChooseLevel(m.GetFrom(), m.GetTo(), maxPoints)
	rows, err := s.store.QueryMetrics(ctx, m.GetNodeId(), m.GetFrom(), m.GetTo(), lv, step)
	if err != nil {
		s.log.Error("metric query failed", "err", err)
		return nil, internalError("metric query failed")
	}
	resp := &probev1.QueryMetricsResponse{Level: lv.Name, StepS: uint32(step)}
	series := make([]*probev1.MetricSeries, len(metric.Columns))
	for i, c := range metric.Columns {
		series[i] = &probev1.MetricSeries{Name: c.Name, Unit: c.Unit, Samples: make([]*probev1.MetricSample, 0, len(rows))}
	}
	for _, r := range rows {
		resp.Ts = append(resp.Ts, r.TS)
		for i, c := range metric.Columns {
			sample := &probev1.MetricSample{N: r.Bucket.N[i]}
			if mean, ok := r.Bucket.Mean(i); ok {
				sample.Mean = proto.Float64(mean)
				if c.Kind == metric.MeanMax {
					sample.Max = proto.Float64(r.Bucket.Max[i])
				}
			}
			series[i].Samples = append(series[i].Samples, sample)
		}
	}
	resp.Series = series
	return connect.NewResponse(resp), nil
}
