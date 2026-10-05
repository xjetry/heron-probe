package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"connectrpc.com/connect"
	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/hub/store"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"
)

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type changeCall func(context.Context) (proto.Message, error)

func invokeChange[Q any, R any](fn func(context.Context, *connect.Request[Q]) (*connect.Response[R], error), q *Q) changeCall {
	return func(ctx context.Context) (proto.Message, error) {
		r, err := fn(ctx, connect.NewRequest(q))
		if err != nil {
			return nil, err
		}
		return any(r.Msg).(proto.Message), nil
	}
}

func operationProto(o store.Operation) *heronv1.Operation {
	return &heronv1.Operation{Id: o.ID, OwnerId: o.OwnerID, RequestId: o.RequestID, Action: o.Action, ResourceId: o.ResourceID, BeforeJson: o.BeforeJSON, AfterJson: o.AfterJSON, CommittedAt: o.CommittedAt}
}

func changeError(err error) error {
	switch {
	case errors.Is(err, store.ErrPermission):
		return permissionDenied("change exceeds preauthorized permissions or node scope")
	case errors.Is(err, store.ErrConflict):
		return connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, store.ErrRequestID):
		return connect.NewError(connect.CodeAlreadyExists, err)
	default:
		return err
	}
}

func replayResponse(hash string, o store.Operation) (*connect.Response[heronv1.ExecuteChangeResponse], error) {
	if o.RequestHash != hash {
		return nil, changeError(store.ErrRequestID)
	}
	return connect.NewResponse(&heronv1.ExecuteChangeResponse{Operation: operationProto(o), Replayed: true}), nil
}

func (s *Service) ExecuteChange(ctx context.Context, req *connect.Request[heronv1.ExecuteChangeRequest]) (response *connect.Response[heronv1.ExecuteChangeResponse], err error) {
	m := proto.Clone(req.Msg).(*heronv1.ExecuteChangeRequest)
	if !m.Preview && !requestIDPattern.MatchString(m.RequestId) {
		return nil, invalid("request_id: 1-128 ASCII letters, digits, hyphens or underscores required")
	}
	c, call, patch, err := s.prepareChange(ctx, m)
	if err != nil {
		return nil, err
	}
	c.OwnerID, c.RequestID, c.Preview = store.OwnerID(ctx), m.RequestId, m.Preview
	if p, ok := store.Principal(ctx); ok && !p.Allows(c.Permission) {
		return nil, changeError(store.ErrPermission)
	}
	// 幂等比较的是原始类型化意图，而非读取当前值补全后的替换请求。
	fingerprint := proto.Clone(m).(*heronv1.ExecuteChangeRequest)
	fingerprint.Preview = false
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(fingerprint)
	if err != nil {
		return nil, internalError("encode change")
	}
	c.RequestHash = fmt.Sprintf("%x", sha256.Sum256(b))
	// 回执是执行结果的唯一持久依据。并发同键请求可能在版本读取、字段补全或业务准入前
	// 已提交；这些步骤的错误不能掩盖已提交结果，也不能让重试再次返回一次性秘密。
	defer func() {
		if err == nil || m.Preview {
			return
		}
		if o, lookupErr := s.store.FindOperation(ctx, c.OwnerID, c.RequestID); lookupErr == nil {
			response, err = replayResponse(c.RequestHash, o)
		}
	}()
	if !m.Preview {
		o, err := s.store.FindOperation(ctx, c.OwnerID, c.RequestID)
		if err == nil {
			return replayResponse(c.RequestHash, o)
		}
		if !errors.Is(err, store.ErrNotFound) {
			return nil, internalError("read change receipt")
		}
	}
	version, err := s.store.ChangeVersion(ctx, c)
	if err != nil {
		return nil, changeError(err)
	}
	c.ExpectedVersion = version
	if !m.Preview {
		if m.ExpectedVersion == "" {
			return nil, invalid("expected_version: preview the change before executing")
		}
		if m.ExpectedVersion != version {
			return nil, changeError(store.ErrConflict)
		}
	}
	if patch != nil {
		if err := patch(); err != nil {
			return nil, err
		}
	} else if len(m.GetUpdateMask().GetPaths()) != 0 {
		return nil, invalid("update_mask is only valid for configuration updates")
	}
	if q := m.GetSaveProbeTask(); q != nil {
		c.Selector = &store.NodeSelector{AllNodes: q.AllNodes, NodeIDs: q.NodeIds, Tags: q.SelectorTags}
	}
	if q := m.GetSaveAlertRule().GetRule(); q != nil {
		c.Selector = &store.NodeSelector{AllNodes: q.AllNodes, NodeIDs: q.NodeIds, Tags: q.SelectorTags}
		c.ReferenceTask = int64(q.TaskId)
	}
	result, err := call(store.WithChange(ctx, c))
	switch {
	case errors.Is(c.Err, store.ErrPreview):
		return connect.NewResponse(&heronv1.ExecuteChangeResponse{Operation: operationProto(c.Operation), ExpectedVersion: c.Version}), nil
	case errors.Is(c.Err, store.ErrReplay):
		return connect.NewResponse(&heronv1.ExecuteChangeResponse{Operation: operationProto(c.Operation), Replayed: true}), nil
	case c.Err != nil:
		return nil, changeError(c.Err)
	case err != nil:
		// 业务提交后的回读可能失败；已提交回执仍是事实，重试不会重复副作用。
		if c.CommittedAt == 0 {
			return nil, err
		}
		return connect.NewResponse(&heronv1.ExecuteChangeResponse{Operation: operationProto(c.Operation)}), nil
	}
	out := &heronv1.ExecuteChangeResponse{Operation: operationProto(c.Operation)}
	if result != nil {
		out.Result, err = anypb.New(result)
		if err != nil {
			return nil, internalError("encode change result")
		}
	}
	return connect.NewResponse(out), nil
}

func (s *Service) prepareChange(ctx context.Context, m *heronv1.ExecuteChangeRequest) (*store.Change, changeCall, func() error, error) {
	c := &store.Change{}
	var call changeCall
	var patch func() error
	switch q := m.Change.(type) {
	case *heronv1.ExecuteChangeRequest_CreateNode:
		c.Action, c.Kind, c.Permission = "create_node", "node", store.PermissionCreate
		call = invokeChange(s.CreateNode, q.CreateNode)
	case *heronv1.ExecuteChangeRequest_UpdateNode:
		c.Action, c.Kind, c.Permission, c.ResourceID = "update_node", "node", store.PermissionConfigure, q.UpdateNode.GetId()
		call = invokeChange(s.UpdateNode, q.UpdateNode)
		patch = func() error {
			n, err := s.store.GetNode(ctx, c.ResourceID)
			if err != nil {
				return notFound(c.ResourceID)
			}
			base := &heronv1.UpdateNodeRequest{Id: n.ID, Name: n.Name, Public: n.Public, Note: n.Note, PublicRemark: n.PublicRemark, TrafficResetDay: uint32(n.TrafficResetDay), OfflineGraceS: proto.Uint32(uint32(n.OfflineGraceS)), Billing: billingProto(n.Billing, s.today()), CountryPin: n.CountryPin, Tags: n.Tags, Maintenance: n.Maintenance}
			return mergeChange(q.UpdateNode, base, m.GetUpdateMask().GetPaths(), "id", "billing.days_left")
		}
	case *heronv1.ExecuteChangeRequest_DeleteNode:
		c.Action, c.Kind, c.Permission, c.ResourceID = "delete_node", "node", store.PermissionDelete, q.DeleteNode.GetId()
		call = invokeChange(s.DeleteNode, q.DeleteNode)
	case *heronv1.ExecuteChangeRequest_RotateNodeToken:
		c.Action, c.Kind, c.Permission, c.ResourceID = "rotate_node_token", "node", store.PermissionRotate, q.RotateNodeToken.GetId()
		call = invokeChange(s.RotateNodeToken, q.RotateNodeToken)
	case *heronv1.ExecuteChangeRequest_OpenRegisterWindow:
		c.Action, c.Kind, c.Permission, c.ResourceID = "open_register_window", "window", store.PermissionRegister, store.OwnerID(ctx)
		call = invokeChange(s.OpenRegisterWindow, q.OpenRegisterWindow)
	case *heronv1.ExecuteChangeRequest_CloseRegisterWindow:
		c.Action, c.Kind, c.Permission, c.ResourceID = "close_register_window", "window", store.PermissionRegister, store.OwnerID(ctx)
		call = invokeChange(s.CloseRegisterWindow, q.CloseRegisterWindow)
	case *heronv1.ExecuteChangeRequest_SaveProbeTask:
		c.Action, c.Kind, c.Permission, c.ResourceID = "save_probe_task", "probe", store.PermissionConfigure, int64(q.SaveProbeTask.GetTask().GetId())
		call = invokeChange(s.SaveProbeTask, q.SaveProbeTask)
		if c.ResourceID != 0 {
			patch = func() error {
				// 版本和补全值都读持久状态；缓存发布晚于提交，不能作为并发修改的基线。
				_, list, err := s.store.LoadProbeTasks(ctx)
				if err != nil {
					return internalError("read probe task")
				}
				for _, d := range list {
					if int64(d.Task.Id) == c.ResourceID {
						if d.AllNodes || len(d.SelectorTags) != 0 {
							d.NodeIDs = nil
						}
						return mergeChange(q.SaveProbeTask, &heronv1.SaveProbeTaskRequest{Task: proto.Clone(d.Task).(*heronv1.ProbeTask), AllNodes: d.AllNodes, NodeIds: d.NodeIDs, SelectorTags: d.SelectorTags}, m.GetUpdateMask().GetPaths(), "task.id", "task")
					}
				}
				return notFound(c.ResourceID)
			}
		}
	case *heronv1.ExecuteChangeRequest_DeleteProbeTask:
		c.Action, c.Kind, c.Permission, c.ResourceID = "delete_probe_task", "probe", store.PermissionConfigure, int64(q.DeleteProbeTask.GetId())
		call = invokeChange(s.DeleteProbeTask, q.DeleteProbeTask)
	case *heronv1.ExecuteChangeRequest_SaveAlertRule:
		c.Action, c.Kind, c.Permission, c.ResourceID = "save_alert_rule", "alert", store.PermissionConfigure, q.SaveAlertRule.GetRule().GetId()
		call = invokeChange(s.SaveAlertRule, q.SaveAlertRule)
		if c.ResourceID != 0 {
			patch = func() error {
				list, err := s.store.ListAlertRules(ctx)
				if err != nil {
					return internalError("read alert rule")
				}
				for _, r := range list {
					if r.ID == c.ResourceID {
						if r.AllNodes || len(r.SelectorTags) != 0 {
							r.NodeIDs = nil
						}
						return mergeChange(q.SaveAlertRule, &heronv1.SaveAlertRuleRequest{Rule: ruleProto(r)}, m.GetUpdateMask().GetPaths(), "rule.id", "rule.created_at", "rule")
					}
				}
				return notFound(c.ResourceID)
			}
		}
	case *heronv1.ExecuteChangeRequest_DeleteAlertRule:
		c.Action, c.Kind, c.Permission, c.ResourceID = "delete_alert_rule", "alert", store.PermissionConfigure, q.DeleteAlertRule.GetId()
		call = invokeChange(s.DeleteAlertRule, q.DeleteAlertRule)
	case *heronv1.ExecuteChangeRequest_StartUpdate:
		c.Action, c.Kind, c.Permission, c.ResourceID = "start_update", "update", store.PermissionUpdate, q.StartUpdate.GetNodeId()
		if c.ResourceID <= 0 {
			return nil, nil, nil, permissionDenied("ExecuteChange cannot update the hub")
		}
		call = invokeChange(s.StartUpdate, q.StartUpdate)
	case *heronv1.ExecuteChangeRequest_CancelUpdate:
		c.Action, c.Kind, c.Permission, c.ResourceID = "cancel_update", "update", store.PermissionUpdate, q.CancelUpdate.GetNodeId()
		if c.ResourceID <= 0 {
			return nil, nil, nil, permissionDenied("ExecuteChange cannot update the hub")
		}
		call = invokeChange(s.CancelUpdate, q.CancelUpdate)
	case *heronv1.ExecuteChangeRequest_DeleteTag:
		c.Action, c.Kind, c.Permission = "delete_tag", "tag", store.PermissionConfigure
		name, err := cleanTag("name", q.DeleteTag.GetName())
		if err != nil {
			return nil, nil, nil, err
		}
		id, err := s.store.TagID(ctx, name)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, nil, nil, internalError("read tag")
		}
		c.ResourceID = id
		call = invokeChange(s.DeleteTag, q.DeleteTag)
	default:
		return nil, nil, nil, invalid("change: select a supported operation")
	}
	return c, call, patch, nil
}

// 先克隆 patch，再按掩码替换；零值与空列表都是明确清除，不回退到旧值。
func mergeChange(dst, base proto.Message, paths []string, forbidden ...string) error {
	if len(paths) == 0 {
		return invalid("update_mask: at least one editable field required")
	}
	source := proto.Clone(dst)
	for _, path := range paths {
		for _, f := range forbidden {
			if path == f {
				return invalid("update_mask: %s is not editable", path)
			}
		}
		if err := copyChangeField(base.ProtoReflect(), source.ProtoReflect(), strings.Split(path, ".")); err != nil {
			return err
		}
	}
	proto.Reset(dst)
	proto.Merge(dst, base)
	return nil
}
func copyChangeField(dst, src protoreflect.Message, path []string) error {
	fd := dst.Descriptor().Fields().ByName(protoreflect.Name(path[0]))
	if fd == nil {
		return invalid("update_mask: unknown field %s", path[0])
	}
	if len(path) > 1 {
		if fd.Message() == nil || fd.IsList() || fd.IsMap() {
			return invalid("update_mask: cannot descend into %s", fd.Name())
		}
		return copyChangeField(dst.Mutable(fd).Message(), src.Get(fd).Message(), path[1:])
	}
	if !src.Has(fd) {
		dst.Clear(fd)
	} else {
		dst.Set(fd, src.Get(fd))
	}
	return nil
}

func (s *Service) ListOperations(ctx context.Context, req *connect.Request[heronv1.ListOperationsRequest]) (*connect.Response[heronv1.ListOperationsResponse], error) {
	owner := req.Msg.OwnerId
	if p, ok := store.Principal(ctx); ok {
		if owner != 0 && owner != p.ID {
			return nil, permissionDenied("only your own operations are visible")
		}
		owner = p.ID
	}
	limit := int(req.Msg.Limit)
	if limit == 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	list, err := s.store.ListOperations(ctx, owner, req.Msg.RequestId, limit)
	if err != nil {
		return nil, internalError("list operations")
	}
	out := &heronv1.ListOperationsResponse{}
	for _, o := range list {
		out.Operations = append(out.Operations, operationProto(o))
	}
	return connect.NewResponse(out), nil
}

func (s *Service) ListNotifyChannelRefs(context.Context, *connect.Request[heronv1.ListNotifyChannelRefsRequest]) (*connect.Response[heronv1.ListNotifyChannelRefsResponse], error) {
	out := &heronv1.ListNotifyChannelRefsResponse{}
	for _, c := range s.alerts.Channels() {
		out.Channels = append(out.Channels, &heronv1.NotifyChannelRef{Id: c.ID, Name: c.Name, Kind: enumFor(channelKinds, c.Kind)})
	}
	return connect.NewResponse(out), nil
}
