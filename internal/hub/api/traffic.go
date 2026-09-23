package api

import (
	"context"
	"errors"
	"math"

	"connectrpc.com/connect"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/hub/traffic"
)

// Book 的入账、校正及 Load 都维持累计值在 [0, MaxInt64]，包括手工改库后的恢复，转 uint64 不会变号。
func trafficProto(e traffic.Entry) *probev1.Traffic {
	return &probev1.Traffic{
		TotalRx: uint64(e.TotalRx), TotalTx: uint64(e.TotalTx), PeriodRx: uint64(e.PeriodRx), PeriodTx: uint64(e.PeriodTx),
		PeriodStart: e.PeriodStart.Unix(), NextResetAt: e.NextReset.Unix(), ResetDay: uint32(e.ResetDay),
	}
}

func (s *Service) GetTraffic(ctx context.Context, _ *connect.Request[probev1.GetTrafficRequest]) (*connect.Response[probev1.GetTrafficResponse], error) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		s.log.Error("listing nodes failed", "err", err)
		return nil, internalError("listing nodes failed")
	}
	out := &probev1.GetTrafficResponse{Now: s.clk.Now().Unix(), Timezone: s.traffic.Zone().String()}
	for _, n := range nodes {
		out.Nodes = append(out.Nodes, &probev1.NodeTraffic{NodeId: n.ID, Name: n.Name, Traffic: trafficProto(s.traffic.View(n.ID))})
	}
	return connect.NewResponse(out), nil
}

// AdjustTraffic 的存在性由写事务裁决：WriteTraffic 跳过不存在的节点、Book 转成 ErrNotFound
// 且不留条目。不在这里先查一次节点——那会与 DeleteNode 竞争，查到存在、写时已删。
func (s *Service) AdjustTraffic(ctx context.Context, req *connect.Request[probev1.AdjustTrafficRequest]) (*connect.Response[probev1.AdjustTrafficResponse], error) {
	if v := req.Msg.GetPeriodRx(); v > math.MaxInt64 {
		return nil, invalid("period_rx must be at most %d; got %d", int64(math.MaxInt64), v)
	}
	if v := req.Msg.GetPeriodTx(); v > math.MaxInt64 {
		return nil, invalid("period_tx must be at most %d; got %d", int64(math.MaxInt64), v)
	}
	id := req.Msg.GetNodeId()
	e, err := s.traffic.Adjust(ctx, id, req.Msg.GetPeriodRx(), req.Msg.GetPeriodTx())
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound(id)
	}
	if err != nil {
		s.log.Error("adjusting traffic failed", "node", id, "err", err)
		return nil, internalError("adjusting traffic failed")
	}
	s.log.Info("traffic adjusted", "node", id, "period_rx", e.PeriodRx, "period_tx", e.PeriodTx)
	return connect.NewResponse(&probev1.AdjustTrafficResponse{Traffic: trafficProto(e)}), nil
}
