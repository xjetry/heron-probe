package api

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/probe"
	"github.com/xjetry/probe/internal/hub/store"
)

func detailProto(d probe.Detail) *probev1.ProbeTaskDetail {
	return &probev1.ProbeTaskDetail{Task: d.Task, NodeIds: d.NodeIDs}
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
	d, version, err := s.probes.Save(ctx, req.Msg.GetTask(), req.Msg.GetNodeIds())
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
	maxPoints, err := s.queryWindow(ctx, m.GetNodeId(), m.GetFrom(), m.GetTo(), m.GetMaxPoints())
	if err != nil {
		return nil, err
	}
	lv, step := store.ChooseLevel(m.GetFrom(), m.GetTo(), maxPoints)
	rows, err := s.store.QueryProbes(ctx, m.GetNodeId(), m.GetFrom(), m.GetTo(), lv, step)
	if err != nil {
		s.log.Error("probe query failed", "err", err)
		return nil, internalError("probe query failed")
	}
	resp := &probev1.QueryProbesResponse{Level: lv.Name, StepS: uint32(step)}
	var cur *probev1.ProbeSeries
	for _, r := range rows { // store 已按 TaskID、TS 排序
		if r.Bucket.Sent == 0 {
			continue
		}
		if cur == nil || cur.TaskId != r.TaskID {
			cur = &probev1.ProbeSeries{TaskId: r.TaskID}
			// 标签取查询时的任务清单；已删除的任务两项留空，客户端退回编号。
			cur.Kind, cur.Target, _ = s.probes.Target(r.TaskID)
			resp.Series = append(resp.Series, cur)
		}
		sample := &probev1.ProbeSample{Ts: r.TS, Sent: r.Bucket.Sent, Lost: r.Bucket.Lost, Errors: r.Bucket.Errors}
		if mean, ok := r.Bucket.RttMean(); ok {
			sample.RttMeanUs, sample.RttMinUs, sample.RttMaxUs = proto.Uint32(mean), proto.Uint32(r.Bucket.RttMinUs), proto.Uint32(r.Bucket.RttMaxUs)
		}
		cur.Samples = append(cur.Samples, sample)
	}
	return connect.NewResponse(resp), nil
}
