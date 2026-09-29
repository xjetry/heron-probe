package api

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/probe"
	"github.com/xjetry/probe/internal/hub/store"
)

func detailProto(d probe.Detail) *probev1.ProbeTaskDetail {
	return &probev1.ProbeTaskDetail{Task: d.Task, AllNodes: d.AllNodes, NodeIds: d.NodeIDs, SelectorTags: d.SelectorTags}
}

func (s *Service) ListProbeTasks(_ context.Context, _ *connect.Request[probev1.ListProbeTasksRequest]) (*connect.Response[probev1.ListProbeTasksResponse], error) {
	version, details := s.probes.List()
	resp := &probev1.ListProbeTasksResponse{Version: version}
	for _, d := range details {
		resp.Tasks = append(resp.Tasks, detailProto(d))
	}
	return connect.NewResponse(resp), nil
}

// 字段校验在注册表经 probelimit.CheckTask 完成；节点存在与每节点上限由 store 保存事务裁决，
// 因为只有事务内计数与并发保存互斥。这里只把哨兵翻译成响应码，并补充请求字段名。
func (s *Service) SaveProbeTask(ctx context.Context, req *connect.Request[probev1.SaveProbeTaskRequest]) (*connect.Response[probev1.SaveProbeTaskResponse], error) {
	if err := checkTaskID(req.Msg.GetTask().GetId(), "task.id"); err != nil {
		return nil, err
	}
	tags, err := cleanTags("selector_tags", req.Msg.GetSelectorTags())
	if err != nil {
		return nil, err
	}
	d, version, err := s.probes.Save(ctx, req.Msg.GetTask(), store.NodeSelector{AllNodes: req.Msg.GetAllNodes(), NodeIDs: req.Msg.GetNodeIds(), Tags: tags})
	// 全部节点模式禁止携带显式分配，超限来自 all_nodes 开关本身。
	if req.Msg.GetAllNodes() && errors.Is(err, store.ErrNodeLimit) {
		return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("all_nodes: %w", err))
	}
	if err != nil {
		return nil, s.operationError(err, "task", "saving probe task failed")
	}
	return connect.NewResponse(&probev1.SaveProbeTaskResponse{Task: detailProto(d), Version: version}), nil
}

func (s *Service) DeleteProbeTask(ctx context.Context, req *connect.Request[probev1.DeleteProbeTaskRequest]) (*connect.Response[probev1.DeleteProbeTaskResponse], error) {
	if err := checkTaskID(req.Msg.GetId(), "id"); err != nil {
		return nil, err
	}
	version, err := s.probes.Delete(ctx, req.Msg.GetId())
	if err != nil {
		return nil, s.operationError(err, "id", "deleting probe task failed")
	}
	return connect.NewResponse(&probev1.DeleteProbeTaskResponse{Version: version}), nil
}

func (s *Service) QueryProbes(ctx context.Context, req *connect.Request[probev1.QueryProbesRequest]) (*connect.Response[probev1.QueryProbesResponse], error) {
	m := req.Msg
	maxPoints, err := checkWindow(m.GetFrom(), m.GetTo(), m.GetMaxPoints())
	if err != nil {
		return nil, err
	}
	if err := s.requireNode(ctx, m.GetNodeId()); err != nil {
		return nil, err
	}
	resp, err := s.history.probeSeries(ctx, m, maxPoints, s.probes.Target, s.probes.OrderedIDs())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) ReorderProbeTasks(ctx context.Context, req *connect.Request[probev1.ReorderProbeTasksRequest]) (*connect.Response[probev1.ReorderProbeTasksResponse], error) {
	for _, id := range req.Msg.GetIds() {
		if err := checkTaskID(id, "ids"); err != nil {
			return nil, err
		}
	}
	if err := s.probes.Reorder(ctx, req.Msg.GetIds()); err != nil {
		if errors.Is(err, store.ErrBadOrder) {
			return nil, invalid("ids must list every probe task exactly once")
		}
		return nil, s.operationError(err, "ids", "reordering probe tasks failed")
	}
	return connect.NewResponse(&probev1.ReorderProbeTasksResponse{}), nil
}
