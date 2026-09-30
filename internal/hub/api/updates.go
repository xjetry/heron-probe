package api

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/hub/store"
	"github.com/xjetry/heron-probe/internal/hub/updates"
	"github.com/xjetry/heron-probe/internal/update"
)

type localUpdateClient interface {
	Status(context.Context) update.Status
	Submit(context.Context, update.Request) (update.Job, error)
}
type releaseSource interface {
	Latest(context.Context) (string, error)
}

func (s *Service) GetUpdates(ctx context.Context, req *connect.Request[heronv1.GetUpdatesRequest]) (*connect.Response[heronv1.GetUpdatesResponse], error) {
	out := &heronv1.GetUpdatesResponse{}
	if p, ok := store.Principal(ctx); !ok || p.AllNodes {
		local := s.updateLocal.Status(ctx)
		out.Targets = append(out.Targets, &heronv1.UpdateTarget{NodeId: 0, Status: update.StatusProto(local, s.cfg.HubVersion)})
	}
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, internalError("list update targets")
	}
	for _, n := range nodes {
		status := &heronv1.UpdateStatus{Reason: "agent has not reported online update support"}
		if s.cfg.Updates != nil {
			status = s.cfg.Updates.Snapshot(n.ID)
		}
		out.Targets = append(out.Targets, &heronv1.UpdateTarget{NodeId: n.ID, Status: status})
	}
	if req.Msg.CheckLatest {
		check, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		out.LatestVersion, err = s.updateSource.Latest(check)
		if err != nil {
			out.CheckError = err.Error()
		}
	}
	return connect.NewResponse(out), nil
}

func (s *Service) StartUpdate(ctx context.Context, req *connect.Request[heronv1.StartUpdateRequest]) (*connect.Response[heronv1.StartUpdateResponse], error) {
	if req.Msg.NodeId < 0 || !update.ValidVersion(req.Msg.Version) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("positive node ID or zero for hub and canonical stable version required"))
	}
	var task *heronv1.UpdateTask
	if req.Msg.NodeId == 0 {
		if !update.Newer(req.Msg.Version, s.cfg.HubVersion) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("target must be newer than the running stable hub version"))
		}
		job, err := s.updateLocal.Submit(ctx, updates.NewRequest(req.Msg.Version, s.clk.Now()))
		if err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		task = update.TaskProto(&job)
	} else {
		if s.cfg.Updates == nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("node updates unavailable"))
		}
		var err error
		task, err = s.cfg.Updates.Start(ctx, req.Msg.NodeId, req.Msg.Version)
		if err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
	}
	return connect.NewResponse(&heronv1.StartUpdateResponse{Task: task}), nil
}

func (s *Service) CancelUpdate(ctx context.Context, req *connect.Request[heronv1.CancelUpdateRequest]) (*connect.Response[heronv1.CancelUpdateResponse], error) {
	if req.Msg.NodeId <= 0 || len(req.Msg.Id) > 64 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("node ID and task ID required; hub updates cannot be cancelled"))
	}
	if s.cfg.Updates == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("node updates unavailable"))
	}
	if err := s.cfg.Updates.Cancel(ctx, req.Msg.NodeId, req.Msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&heronv1.CancelUpdateResponse{}), nil
}
