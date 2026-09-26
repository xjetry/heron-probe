package api

import (
	"context"
	"time"

	"connectrpc.com/connect"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
)

// GetSnapshot 的在线判定见 liveState。
func (s *Service) GetSnapshot(ctx context.Context, _ *connect.Request[probev1.GetSnapshotRequest]) (*connect.Response[probev1.GetSnapshotResponse], error) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		s.log.Error("listing nodes failed", "err", err)
		return nil, internalError("listing nodes failed")
	}
	out := &probev1.GetSnapshotResponse{Now: s.clk.Now().Unix(), ReportIntervalMs: uint32(s.cfg.ReportInterval / time.Millisecond), HubVersion: s.cfg.HubVersion}
	for _, n := range nodes {
		st := &probev1.NodeStatus{Id: n.ID, Name: n.Name, Traffic: trafficProto(s.traffic.View(n.ID))}
		st.Online, st.LastSeenAt, st.Metrics = liveState(s.live, n)
		out.Nodes = append(out.Nodes, st)
	}
	return connect.NewResponse(out), nil
}

func (s *Service) QueryMetrics(ctx context.Context, req *connect.Request[probev1.QueryMetricsRequest]) (*connect.Response[probev1.QueryMetricsResponse], error) {
	m := req.Msg
	maxPoints, err := checkWindow(m.GetFrom(), m.GetTo(), m.GetMaxPoints())
	if err != nil {
		return nil, err
	}
	if err := s.requireNode(ctx, m.GetNodeId()); err != nil {
		return nil, err
	}
	resp, err := s.history.metrics(ctx, m, maxPoints)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// requireNode 是管理端历史查询的节点准入：不存在即 NotFound，错误写出 id。
func (s *Service) requireNode(ctx context.Context, id int64) error {
	exists, err := s.store.NodeExists(ctx, id)
	if err != nil {
		s.log.Error("looking up node failed", "err", err)
		return internalError("looking up node failed")
	}
	if !exists {
		return notFound(id)
	}
	return nil
}
