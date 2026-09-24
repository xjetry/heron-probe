package api

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/sanitize"
	"github.com/xjetry/probe/internal/hub/store"
)

const (
	maxNameRunes   = 64
	maxNoteRunes   = 1024
	minWindowTTL   = 60 * time.Second
	maxWindowTTL   = 7 * 24 * time.Hour
	maxWindowNodes = 1000
	minResetDay    = 1
	// 28 是每个月都有的最大日；更大的日子在短月里没有零点可对齐。
	maxResetDay = 28
)

func nodeProto(n store.Node) *probev1.Node {
	out := &probev1.Node{Id: n.ID, Name: n.Name, Public: n.Public, Note: n.Note, SortOrder: n.SortOrder, CreatedAt: n.CreatedAt.Unix(), Facts: n.Facts, TrafficResetDay: uint32(n.TrafficResetDay)}
	if !n.LastSeenAt.IsZero() {
		out.LastSeenAt = proto.Int64(n.LastSeenAt.Unix())
	}
	if n.Facts != nil {
		out.FactsUpdatedAt = proto.Int64(n.FactsUpdatedAt.Unix())
	}
	return out
}

// cleanName 先去控制字符再裁剪空白，避免清洗后暴露新的首尾空白。
// 按完整清洗结果计字符数；先截字节会把超长输入变成合法名称。
func cleanName(raw string) (string, error) {
	name := strings.TrimSpace(sanitize.String(raw, len(raw)))
	if n := utf8.RuneCountInString(name); n == 0 || n > maxNameRunes {
		return "", invalid("name must be 1–%d characters after trimming whitespace and control characters; got %d", maxNameRunes, n)
	}
	return name, nil
}

func cleanNote(raw string) (string, error) {
	note := sanitize.String(raw, len(raw))
	if n := utf8.RuneCountInString(note); n > maxNoteRunes {
		return "", invalid("note must be at most %d characters; got %d", maxNoteRunes, n)
	}
	return note, nil
}

func (s *Service) ListNodes(ctx context.Context, _ *connect.Request[probev1.ListNodesRequest]) (*connect.Response[probev1.ListNodesResponse], error) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		s.log.Error("listing nodes failed", "err", err)
		return nil, internalError("listing nodes failed")
	}
	out := make([]*probev1.Node, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, nodeProto(n))
	}
	return connect.NewResponse(&probev1.ListNodesResponse{Nodes: out}), nil
}

func (s *Service) CreateNode(ctx context.Context, req *connect.Request[probev1.CreateNodeRequest]) (*connect.Response[probev1.CreateNodeResponse], error) {
	name, err := cleanName(req.Msg.GetName())
	if err != nil {
		return nil, err
	}
	id, tok, err := s.auth.CreateNode(ctx, name)
	if err != nil {
		s.log.Error("creating node failed", "err", err)
		return nil, internalError("creating node failed")
	}
	n, err := s.store.GetNode(ctx, id)
	if err != nil {
		s.log.Error("reading created node failed", "err", err)
		return nil, internalError("reading created node failed")
	}
	s.log.Info("node created", "node", id, "name", name)
	return connect.NewResponse(&probev1.CreateNodeResponse{Node: nodeProto(n), Token: tok}), nil
}

func (s *Service) UpdateNode(ctx context.Context, req *connect.Request[probev1.UpdateNodeRequest]) (*connect.Response[probev1.UpdateNodeResponse], error) {
	name, err := cleanName(req.Msg.GetName())
	if err != nil {
		return nil, err
	}
	note, err := cleanNote(req.Msg.GetNote())
	if err != nil {
		return nil, err
	}
	day := int(req.Msg.GetTrafficResetDay())
	if day < minResetDay || day > maxResetDay {
		return nil, invalid("traffic_reset_day must be between %d and %d; got %d", minResetDay, maxResetDay, day)
	}
	s.nodeMu.Lock()
	defer s.nodeMu.Unlock()
	// 请求不携带宽限期；nodeMu 串行化节点编辑，保留已存值而不是把缺省解释为清零。
	current, err := s.store.GetNode(ctx, req.Msg.GetId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound(req.Msg.GetId())
	}
	if err != nil {
		s.log.Error("reading node before update failed", "err", err)
		return nil, internalError("reading node before update failed")
	}
	err = s.store.UpdateNode(ctx, req.Msg.GetId(), name, req.Msg.GetPublic(), note, day, current.OfflineGraceS)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound(req.Msg.GetId())
	}
	if err != nil {
		s.log.Error("updating node failed", "err", err)
		return nil, internalError("updating node failed")
	}
	// 只有库提交成功才改内存；nodeMu 跨越两次写入并与删除共用，失败或并发请求都不能使两者分叉。
	s.traffic.SetResetDay(req.Msg.GetId(), day)
	n, err := s.store.GetNode(ctx, req.Msg.GetId())
	if err != nil {
		s.log.Error("reading updated node failed", "err", err)
		return nil, internalError("reading updated node failed")
	}
	return connect.NewResponse(&probev1.UpdateNodeResponse{Node: nodeProto(n)}), nil
}

// DeleteNode 先由 auth 删除库记录和 token，再由状态持有者等待在途上报并清理。
// 返回成功必须同时意味着持久化删除完成与进程内状态清除。
func (s *Service) DeleteNode(ctx context.Context, req *connect.Request[probev1.DeleteNodeRequest]) (*connect.Response[probev1.DeleteNodeResponse], error) {
	s.nodeMu.Lock()
	defer s.nodeMu.Unlock()
	err := s.auth.DeleteNode(ctx, req.Msg.GetId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound(req.Msg.GetId())
	}
	if err != nil {
		s.log.Error("deleting node failed", "err", err)
		return nil, internalError("deleting node failed")
	}
	s.nodes.Forget(req.Msg.GetId())
	s.log.Info("node deleted", "node", req.Msg.GetId())
	return connect.NewResponse(&probev1.DeleteNodeResponse{}), nil
}

func (s *Service) RotateNodeToken(ctx context.Context, req *connect.Request[probev1.RotateNodeTokenRequest]) (*connect.Response[probev1.RotateNodeTokenResponse], error) {
	tok, err := s.auth.RotateToken(ctx, req.Msg.GetId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound(req.Msg.GetId())
	}
	if err != nil {
		s.log.Error("rotating token failed", "err", err)
		return nil, internalError("rotating token failed")
	}
	s.log.Info("node token rotated", "node", req.Msg.GetId())
	return connect.NewResponse(&probev1.RotateNodeTokenResponse{Token: tok}), nil
}

func (s *Service) ReorderNodes(ctx context.Context, req *connect.Request[probev1.ReorderNodesRequest]) (*connect.Response[probev1.ReorderNodesResponse], error) {
	err := s.store.ReorderNodes(ctx, req.Msg.GetIds())
	if errors.Is(err, store.ErrBadOrder) {
		return nil, invalid("ids must list every existing node exactly once; got %d ids", len(req.Msg.GetIds()))
	}
	if err != nil {
		s.log.Error("reordering nodes failed", "err", err)
		return nil, internalError("reordering nodes failed")
	}
	return connect.NewResponse(&probev1.ReorderNodesResponse{}), nil
}

func (s *Service) OpenRegisterWindow(ctx context.Context, req *connect.Request[probev1.OpenRegisterWindowRequest]) (*connect.Response[probev1.OpenRegisterWindowResponse], error) {
	ttl := time.Duration(req.Msg.GetTtlS()) * time.Second
	if ttl < minWindowTTL || ttl > maxWindowTTL {
		return nil, invalid("ttl_s must be between %d and %d seconds; got %d", int(minWindowTTL/time.Second), int(maxWindowTTL/time.Second), req.Msg.GetTtlS())
	}
	maxNodes := req.Msg.GetMaxNodes()
	if maxNodes < 1 || maxNodes > maxWindowNodes {
		return nil, invalid("max_nodes must be between 1 and %d; got %d", maxWindowNodes, maxNodes)
	}
	key, until, err := s.auth.OpenWindow(ctx, ttl, int(maxNodes))
	if err != nil {
		s.log.Error("opening register window failed", "err", err)
		return nil, internalError("opening register window failed")
	}
	s.log.Info("register window opened", "expires_at", until, "max_nodes", maxNodes)
	return connect.NewResponse(&probev1.OpenRegisterWindowResponse{Key: key, ExpiresAt: until.Unix(), MaxNodes: maxNodes}), nil
}

func (s *Service) CloseRegisterWindow(ctx context.Context, _ *connect.Request[probev1.CloseRegisterWindowRequest]) (*connect.Response[probev1.CloseRegisterWindowResponse], error) {
	if err := s.auth.CloseWindow(ctx); err != nil {
		s.log.Error("closing register window failed", "err", err)
		return nil, internalError("closing register window failed")
	}
	return connect.NewResponse(&probev1.CloseRegisterWindowResponse{}), nil
}

// GetRegisterWindow 的 open 与 RegisterNode 事务里的判定同口径：存在、未到期、有名额。
func (s *Service) GetRegisterWindow(ctx context.Context, _ *connect.Request[probev1.GetRegisterWindowRequest]) (*connect.Response[probev1.GetRegisterWindowResponse], error) {
	w, ok, err := s.auth.Window(ctx)
	if err != nil {
		s.log.Error("reading register window failed", "err", err)
		return nil, internalError("reading register window failed")
	}
	if !ok || !s.clk.Now().Before(w.ExpiresAt) || w.Remaining <= 0 {
		return connect.NewResponse(&probev1.GetRegisterWindowResponse{Open: false}), nil
	}
	return connect.NewResponse(&probev1.GetRegisterWindowResponse{Open: true, ExpiresAt: w.ExpiresAt.Unix(), Remaining: uint32(w.Remaining)}), nil
}
