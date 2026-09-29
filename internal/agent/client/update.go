package client

import (
	"context"
	"log/slog"
	"sync"
	"time"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/update"
	"google.golang.org/protobuf/proto"
)

type localUpdater interface {
	Status(context.Context) update.Status
	Submit(context.Context, update.Request) (update.Job, error)
	Ready(context.Context, string) error
}

// UpdateCoordinator 将本机 socket 往返移到后台，Report 只交换不可变快照。
type UpdateCoordinator struct {
	client  localUpdater
	version string
	log     *slog.Logger
	mu      sync.Mutex
	status  *heronv1.UpdateStatus
	success chan *heronv1.UpdateTask
}

func NewUpdateCoordinator(version string, log *slog.Logger) *UpdateCoordinator {
	return &UpdateCoordinator{client: update.NewClient("agent"), version: version, log: log, success: make(chan *heronv1.UpdateTask, 1), status: &heronv1.UpdateStatus{Version: version, Reason: "checking local updater"}}
}
func (c *UpdateCoordinator) Snapshot() *heronv1.UpdateStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return proto.Clone(c.status).(*heronv1.UpdateStatus)
}
func (c *UpdateCoordinator) Reported(task *heronv1.UpdateTask) {
	if task != nil {
		task = proto.Clone(task).(*heronv1.UpdateTask)
	}
	select {
	case c.success <- task:
	default:
	}
}
func (c *UpdateCoordinator) refresh(ctx context.Context) update.Status {
	s := c.client.Status(ctx)
	c.mu.Lock()
	c.status = update.StatusProto(s, c.version)
	c.mu.Unlock()
	return s
}
func (c *UpdateCoordinator) Run(ctx context.Context) {
	c.refresh(ctx)
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.refresh(ctx)
		case task := <-c.success:
			s := c.refresh(ctx)
			if s.Job != nil && s.Job.State == "verifying" && s.Job.Version == c.version {
				if err := c.client.Ready(ctx, c.version); err != nil {
					c.log.Warn("confirm agent update", "err", err)
				}
				c.refresh(ctx)
			}
			if task == nil || task.State != "dispatched" {
				continue
			}
			r := update.Request{ID: task.Id, Version: task.Version, ExpiresAt: task.ExpiresAt}
			if err := r.Validate(time.Now()); err != nil {
				c.log.Warn("reject agent update", "err", err)
				continue
			}
			if !update.Newer(task.Version, c.version) {
				continue
			}
			if _, err := c.client.Submit(ctx, r); err != nil {
				c.log.Warn("submit agent update", "err", err)
			}
			c.refresh(ctx)
		}
	}
}
