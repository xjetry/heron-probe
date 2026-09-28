package api

import (
	"context"
	"encoding/hex"

	"connectrpc.com/connect"
	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/auth"
)

func (s *Service) ListSessions(ctx context.Context, _ *connect.Request[probev1.ListSessionsRequest]) (*connect.Response[probev1.ListSessionsResponse], error) {
	rows, err := s.auth.ListSessions(ctx)
	if err != nil {
		s.log.Error("listing sessions failed", "err", err)
		return nil, internalError("listing sessions failed")
	}
	current := auth.HashToken(ctx.Value(sessionKey{}).(string))
	out := &probev1.ListSessionsResponse{}
	for _, sess := range rows {
		// 库与响应只持有 hash；鉴权仍须对 cookie 明文哈希，列表 id 不能成为登录凭据。
		out.Sessions = append(out.Sessions, &probev1.Session{
			Id: hex.EncodeToString(sess.TokenHash[:]), CreatedAt: sess.CreatedAt.Unix(),
			LastUsedAt: sess.LastUsedAt.Unix(), Current: sess.TokenHash == current,
		})
	}
	return connect.NewResponse(out), nil
}

func (s *Service) RevokeSession(ctx context.Context, req *connect.Request[probev1.RevokeSessionRequest]) (*connect.Response[probev1.RevokeSessionResponse], error) {
	raw, err := hex.DecodeString(req.Msg.Id)
	if err != nil || len(raw) != 32 {
		return nil, invalid("id must be 64 hexadecimal characters from ListSessions")
	}
	hash := [32]byte(raw)
	// DELETE 不要求命中行：超时重试或另一设备已经撤销时，目标均已达成。
	if err := s.store.DeleteSession(ctx, hash); err != nil {
		s.log.Error("revoking session failed", "err", err)
		return nil, internalError("revoking session failed")
	}
	resp := connect.NewResponse(&probev1.RevokeSessionResponse{})
	if hash == auth.HashToken(ctx.Value(sessionKey{}).(string)) {
		clearSessionCookie(ctx, resp.Header())
	}
	return resp, nil
}
