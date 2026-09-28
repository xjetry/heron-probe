package api

import (
	"context"

	"connectrpc.com/connect"
	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/backup"
)

func (s *Service) GetBackupStatus(ctx context.Context, _ *connect.Request[probev1.GetBackupStatusRequest]) (*connect.Response[probev1.GetBackupStatusResponse], error) {
	status, err := s.cfg.Backups.Status(ctx)
	if err != nil {
		s.log.Error("backup status failed", "err", err)
		return nil, internalError("backup status failed")
	}
	return connect.NewResponse(&probev1.GetBackupStatusResponse{
		Enabled: status.Enabled, Config: backupLayerProto(status.Config), Metrics: backupLayerProto(status.Metrics),
	}), nil
}

func backupLayerProto(s backup.LayerStatus) *probev1.BackupLayerStatus {
	out := &probev1.BackupLayerStatus{}
	if !s.LastSuccess.IsZero() {
		at := s.LastSuccess.Unix()
		out.LastSuccessAt = &at
	}
	if s.Failure != "" {
		out.Failure = &probev1.BackupFailure{Category: s.Failure, SinceAt: s.Since.Unix(), StatusCode: int32(s.StatusCode)}
	}
	return out
}
