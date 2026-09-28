// Package api 实现 AdminService。
//
// 鉴权在挂载点的拦截器里裁决：每个方法以 probe.v1.access 声明准入口径，流式调用一律拒绝，
// 方法体只在准入通过后执行。凭据有两条路径：Authorization 的 scheme 为 Bearer 时走 API token，
// 此后不看 cookie；否则走会话 cookie。两条路径互不回退——若互相回退，实际生效的是较弱的那条。
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
	// maxBody 是管理请求的解码预算。connect 先解码再进拦截器，未鉴权的请求也会被读到这个上限，所以它必须有界。
	// 它要装下 UpdateSettings 的满额设置在最坏转义下的 JSON：logo 满额（base64 字符在 JSON 里无需转义）；自定义 CSS
	// 与清洗前的标题满额，且每个字节都转义成 6 字节的 \u00XX（控制字符就是这样）；另留 4 KiB 给明暗、主色、总闸布尔值、
	// 字段名与 JSON 语法。多余的 JSON 空白、对无需转义的字符的转义不在预算内：这样的请求超出预算时得到 resource_exhausted。
	// 各项的上限在 settings.go；每个字段的合法取值都有字节上限（明暗与主色由取值集合与格式限定，总闸只能是 true 或 false）
	// 是这条推导成立的前提。
	maxBody = maxLogoBytes + 6*maxCSSBytes + 6*maxTitleBytes + 4<<10
)

type Config struct {
	// TTL 必须为正；零值会放宽宽限期下限，New 将其视为装配错误并 panic。
	TTL time.Duration
	// ReportInterval 是 agent 的正常上报间隔，客户端据此选择轮询节奏。
	ReportInterval time.Duration
	TrustedProxies []netip.Prefix
	// HubVersion 原样经 GetSnapshotResponse.hub_version 下发。空串（装配时没传）与 dev 一样
	// 被面板当作非正式版本：给出 latest 安装命令、不标落后节点。
	HubVersion string
	// Location 是 hub 的 --timezone，days_left 按它的日历日算；New 要求非 nil。
	Location *time.Location
	// Retention 是 serve 交给维护循环的同一份保留期，存储健康按它判定最老桶是否超期。零值会把最老桶早于
	// 一个桶长加一个维护间隔之前的表都标成超期，New 用 Retention.Validate 把它当作装配错误拒绝。
	Retention store.Retention
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
	history  history

	// access 是 AdminService 每个过程的准入口径，New 时从描述符读出，之后只读。
	access map[string]probev1.Access
}

func New(cfg Config, st *store.Store, a *auth.Auth, l *live.Live, nodes NodeState, book *traffic.Book, probes *probe.Registry, alerts *alert.Engine, notifier *alert.Queue, clk clock.Clock, log *slog.Logger) *Service {
	if cfg.TTL <= 0 {
		panic("api.Config.TTL must be positive")
	}
	if cfg.Location == nil {
		panic("api.Config.Location must be set")
	}
	if err := cfg.Retention.Validate(); err != nil {
		panic("api.Config.Retention: " + err.Error())
	}
	return &Service{
		cfg: cfg, store: st, auth: a, live: l, nodes: nodes, traffic: book, probes: probes, alerts: alerts, notifier: notifier, clk: clk, log: log,
		history: history{store: st, log: log},
		access:  accessTable(probev1.File_probe_v1_admin_proto.Services().ByName("AdminService")),
	}
}

// today 是 hub 时区（--timezone）的今天，days_left 以它为基准。
func (s *Service) today() time.Time { return alert.Today(s.clk.Now(), s.cfg.Location) }

func (s *Service) Handler() (string, http.Handler) {
	return probev1connect.NewAdminServiceHandler(s,
		connect.WithInterceptors(s.accessInterceptor()),
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

func permissionDenied(format string, args ...any) error {
	return connect.NewError(connect.CodePermissionDenied, fmt.Errorf(format, args...))
}

// bearerCredential 按 scheme 选凭据路径。scheme 为 Bearer（大小写不敏感）即走 token 路径，
// 哪怕 token 为空，此后不再看 cookie。其他 scheme 不是 hub 的凭据，按不存在处理：反代做 Basic
// 认证时浏览器对每个请求自动附带 Authorization: Basic，若因此选了 token 路径，面板的每个请求都会被拒。
// 同一请求带多个 Bearer 无从判定该用哪个，按无效凭据处理。
func bearerCredential(h http.Header) (string, bool, error) {
	var found []string
	for _, v := range h.Values("Authorization") {
		scheme, rest, _ := strings.Cut(strings.TrimSpace(v), " ")
		if strings.EqualFold(scheme, "Bearer") {
			found = append(found, strings.TrimSpace(rest))
		}
	}
	switch {
	case len(found) == 0:
		return "", false, nil
	case len(found) > 1:
		return "", true, errors.New("multiple bearer credentials")
	case found[0] == "":
		return "", true, errors.New("empty bearer token")
	}
	return found[0], true, nil
}

type accessInterceptor struct{ s *Service }

func (s *Service) accessInterceptor() connect.Interceptor { return accessInterceptor{s: s} }

func (i accessInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	s := i.s
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		level, ok := s.access[req.Spec().Procedure]
		if !ok {
			return nil, unauthenticated("unauthenticated")
		}
		peer := peerInfo{
			from:   auth.ClientIP(req.Peer().Addr, req.Header().Values("X-Forwarded-For"), s.cfg.TrustedProxies),
			scheme: auth.RequestScheme(req.Peer().Addr, req.Header().Values("X-Forwarded-Proto"), s.cfg.TrustedProxies),
		}
		ctx = context.WithValue(ctx, peerKey{}, peer)
		// 先鉴别身份再裁决权限：无效 token 调任何方法都是 401，有效 token 调非 READ 方法是 403。
		// Login 带 Bearer 也落在 403，但文案不能写成"去开一个面板会话"：token 代替不了密码。
		if tok, isBearer, err := bearerCredential(req.Header()); isBearer {
			if err != nil {
				return nil, unauthenticated(err.Error() + "; send exactly one Authorization: Bearer <API token>")
			}
			ok, err := s.auth.AuthenticateAPIToken(ctx, tok)
			if err != nil {
				s.log.Error("API token lookup failed", "err", err)
				return nil, internalError("API token lookup failed")
			}
			if !ok {
				return nil, unauthenticated("API token unknown or revoked")
			}
			if level != probev1.Access_ACCESS_READ {
				if level == probev1.Access_ACCESS_LOGIN {
					return nil, permissionDenied("%s: API tokens cannot log in; send the admin password without an Authorization: Bearer header", req.Spec().Procedure)
				}
				return nil, permissionDenied("%s: API tokens are read-only; this method requires a panel session", req.Spec().Procedure)
			}
			return next(ctx, req)
		}
		if level == probev1.Access_ACCESS_LOGIN {
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

func (i accessInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i accessInterceptor) WrapStreamingHandler(connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
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

func clearSessionCookie(ctx context.Context, header http.Header) {
	peer := ctx.Value(peerKey{}).(peerInfo)
	header.Add("Set-Cookie", sessionCookie("", peer.scheme == "https", -1).String())
}

func (s *Service) Login(ctx context.Context, req *connect.Request[probev1.LoginRequest]) (*connect.Response[probev1.LoginResponse], error) {
	peer := ctx.Value(peerKey{}).(peerInfo)
	tok, err := s.auth.Login(ctx, req.Msg.GetPassword(), peer.from)
	switch {
	case errors.Is(err, auth.ErrLoginBusy):
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("password verification is busy; please try again later"))
	case errors.Is(err, auth.ErrLocked):
		return nil, unauthenticated("too many failed logins from this source (one IPv4 address, or one IPv6 /64); retry in 15 minutes")
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
	if err := s.auth.Logout(ctx, tok); err != nil {
		s.log.Error("logout failed", "err", err)
		return nil, internalError("logout failed")
	}
	resp := connect.NewResponse(&probev1.LogoutResponse{})
	clearSessionCookie(ctx, resp.Header())
	return resp, nil
}
