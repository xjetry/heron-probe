// Package api 实现 AdminService。
//
// 鉴权在挂载点的拦截器里裁决：除 Login 外每个方法都要求有效会话，流式调用一律
// 拒绝，方法体只在准入通过后执行。凭据只走会话 cookie；带 Authorization 头也不看——
// 两条口径若互相回退，实际生效的是较弱的那条。
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/alert"
	"github.com/xjetry/probe/internal/hub/auth"
	"github.com/xjetry/probe/internal/hub/live"
	"github.com/xjetry/probe/internal/hub/probe"
	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/hub/traffic"
)

const (
	SessionCookie = "probe_session"
	// maxBody 限制解码预算，避免未鉴权的管理请求消耗无界内存。
	maxBody = 64 << 10
)

type Config struct {
	// TTL 必须为正；零值会放宽宽限期下限，New 将其视为装配错误并 panic。
	TTL time.Duration
	// ReportInterval 是 agent 的正常上报间隔，客户端据此选择轮询节奏。
	ReportInterval time.Duration
	TrustedProxies []netip.Prefix
}

// NodeState 是节点在进程内的状态持有者；删除节点后由它清理。用接口而不直接依赖
// ingest：清理由状态持有者承载，管理服务不依赖上报服务的内部结构。
type NodeState interface {
	Forget(nodeID int64)
}

type Service struct {
	// 内存里的重置日与库里的 traffic_reset_day 必须一致，Forget 之后不得再为该节点建内存状态；
	// 节点的库写入与内存更新在同一临界区内完成，不同请求按此锁串行。
	nodeMu sync.Mutex

	cfg      Config
	store    *store.Store
	auth     *auth.Auth
	live     *live.Live
	nodes    NodeState
	traffic  *traffic.Book
	probes   *probe.Registry
	alerts   *alert.Engine
	notifier *alert.Queue
	clk      clock.Clock
	log      *slog.Logger
}

func New(cfg Config, st *store.Store, a *auth.Auth, l *live.Live, nodes NodeState, book *traffic.Book, probes *probe.Registry, alerts *alert.Engine, notifier *alert.Queue, clk clock.Clock, log *slog.Logger) *Service {
	if cfg.TTL <= 0 {
		panic("api.Config.TTL must be positive")
	}
	return &Service{cfg: cfg, store: st, auth: a, live: l, nodes: nodes, traffic: book, probes: probes, alerts: alerts, notifier: notifier, clk: clk, log: log}
}

func (s *Service) Handler() (string, http.Handler) {
	return probev1connect.NewAdminServiceHandler(s,
		connect.WithInterceptors(s.sessionInterceptor()),
		connect.WithReadMaxBytes(maxBody))
}

type sessionKey struct{}
type peerKey struct{}

// peerInfo 由拦截器统一计算；转发头只有来自可信代理时才参与来源地址与协议判定。
type peerInfo struct {
	from   netip.Addr
	scheme string
}

func unauthenticated(msg string) error {
	return connect.NewError(connect.CodeUnauthenticated, errors.New(msg))
}

func internalError(msg string) error {
	return connect.NewError(connect.CodeInternal, errors.New(msg))
}

func invalid(format string, args ...any) error {
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(format, args...))
}

func notFound(id int64) error {
	return connect.NewError(connect.CodeNotFound, fmt.Errorf("node %d does not exist", id))
}

type sessionInterceptor struct{ s *Service }

func (s *Service) sessionInterceptor() connect.Interceptor { return sessionInterceptor{s: s} }

func (i sessionInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	s := i.s
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		proc := req.Spec().Procedure
		if !strings.HasPrefix(proc, "/probe.v1.AdminService/") {
			return nil, unauthenticated("unauthenticated")
		}
		peer := peerInfo{
			from:   auth.ClientIP(req.Peer().Addr, req.Header().Get("X-Forwarded-For"), s.cfg.TrustedProxies),
			scheme: auth.RequestScheme(req.Peer().Addr, req.Header().Get("X-Forwarded-Proto"), s.cfg.TrustedProxies),
		}
		ctx = context.WithValue(ctx, peerKey{}, peer)
		if proc == probev1connect.AdminServiceLoginProcedure {
			// 凭据是请求体里的密码，由 Login 裁决；按来源的锁定也在那里。
			return next(ctx, req)
		}
		tok, ok := sessionToken(req.Header())
		if !ok {
			return nil, unauthenticated("no session cookie; call Login first")
		}
		alive, err := s.auth.AuthenticateSession(ctx, tok)
		if err != nil {
			s.log.Error("session lookup failed", "err", err)
			return nil, internalError("session lookup failed")
		}
		if !alive {
			return nil, unauthenticated("session expired or unknown; call Login again")
		}
		return next(context.WithValue(ctx, sessionKey{}, tok), req)
	}
}

func (i sessionInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i sessionInterceptor) WrapStreamingHandler(connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(context.Context, connect.StreamingHandlerConn) error {
		return unauthenticated("unauthenticated")
	}
}

func sessionToken(h http.Header) (string, bool) {
	for _, line := range h.Values("Cookie") {
		cookies, err := http.ParseCookie(line)
		if err != nil {
			continue
		}
		for _, c := range cookies {
			if c.Name == SessionCookie && c.Value != "" {
				return c.Value, true
			}
		}
	}
	return "", false
}

// sessionCookie 不设 Domain，避免会话被发送到其他子域；它不隔离同主机不同端口。
// 跨站发送由 SameSite 限制，跨源读取由不下发 CORS 允许头限制。
// Secure 只由可信代理转发的协议决定。
func sessionCookie(value string, secure bool, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: SessionCookie, Value: value, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secure, MaxAge: maxAge,
	}
}

func (s *Service) Login(ctx context.Context, req *connect.Request[probev1.LoginRequest]) (*connect.Response[probev1.LoginResponse], error) {
	peer := ctx.Value(peerKey{}).(peerInfo)
	tok, err := s.auth.Login(ctx, req.Msg.GetPassword(), peer.from)
	switch {
	case errors.Is(err, auth.ErrLocked):
		return nil, unauthenticated("too many failed logins from this address; retry in 15 minutes")
	case errors.Is(err, auth.ErrNoAdmin), errors.Is(err, auth.ErrBadPassword):
		// 对外同一响应，避免匿名调用方由错误内容判断管理员是否已配置。
		return nil, unauthenticated("wrong password")
	case err != nil:
		s.log.Error("login failed", "err", err)
		return nil, internalError("login failed")
	}
	resp := connect.NewResponse(&probev1.LoginResponse{})
	resp.Header().Add("Set-Cookie", sessionCookie(tok, peer.scheme == "https", int(auth.SessionAbsolute/time.Second)).String())
	return resp, nil
}

func (s *Service) Logout(ctx context.Context, _ *connect.Request[probev1.LogoutRequest]) (*connect.Response[probev1.LogoutResponse], error) {
	tok := ctx.Value(sessionKey{}).(string)
	peer := ctx.Value(peerKey{}).(peerInfo)
	if err := s.auth.Logout(ctx, tok); err != nil {
		s.log.Error("logout failed", "err", err)
		return nil, internalError("logout failed")
	}
	resp := connect.NewResponse(&probev1.LogoutResponse{})
	resp.Header().Add("Set-Cookie", sessionCookie("", peer.scheme == "https", -1).String())
	return resp, nil
}
