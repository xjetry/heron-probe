package api

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/probe"
	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/probelimit"
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

// SaveProbeTask 的校验全部在注册表里（字段规则与 agent 共用一份）；这里只把错误翻译成响应码：
// 字段非法 → InvalidArgument，节点或任务不存在 → NotFound，每节点上限 → ResourceExhausted。
func (s *Service) SaveProbeTask(ctx context.Context, req *connect.Request[probev1.SaveProbeTaskRequest]) (*connect.Response[probev1.SaveProbeTaskResponse], error) {
	d, version, err := s.probes.Save(ctx, req.Msg.GetTask(), req.Msg.GetNodeIds())
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, store.ErrNodeLimit):
		return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("%w (maximum %d)", err, probelimit.MaxTasksPerNode))
	case errors.Is(err, probe.ErrInvalid):
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	case err != nil:
		s.log.Error("saving probe task failed", "err", err)
		return nil, internalError("saving probe task failed")
	}
	return connect.NewResponse(&probev1.SaveProbeTaskResponse{Task: detailProto(d), Version: version}), nil
}

func (s *Service) DeleteProbeTask(ctx context.Context, req *connect.Request[probev1.DeleteProbeTaskRequest]) (*connect.Response[probev1.DeleteProbeTaskResponse], error) {
	version, err := s.probes.Delete(ctx, req.Msg.GetId())
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, err)
	case err != nil:
		s.log.Error("deleting probe task failed", "err", err)
		return nil, internalError("deleting probe task failed")
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
