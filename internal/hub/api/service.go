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
	"net/textproto"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/alert"
	"github.com/xjetry/probe/internal/hub/auth"
	"github.com/xjetry/probe/internal/hub/backup"
	"github.com/xjetry/probe/internal/hub/geo"
	"github.com/xjetry/probe/internal/hub/live"
	"github.com/xjetry/probe/internal/hub/probe"
	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/hub/theme"
	"github.com/xjetry/probe/internal/hub/traffic"
)

const (
	SessionCookie = "probe_session"

	// AdminService 的解码预算按过程分两类，由 Handler 分派：UploadTheme 用 maxThemeBody，其余过程用 maxSettingsBody。
	// connect 先解码再进拦截器，未鉴权的请求也会被读到所在过程的预算，所以两类都必须有界，而主题包那么大的预算只给
	// 需要它的那一个过程——全部过程共用取大者，匿名请求在每个过程上都会被读到约 10.7 MiB。每个字段的合法取值都有
	// 字节上限是两条推导成立的前提。多余的 JSON 空白、对无需转义的字符的转义不在预算内：这样的请求超出预算时得到
	// resource_exhausted。

	// Settings 的预算由 settings_budget.go 的 settingsBudget 按字段登记、由 descriptor 汇总成 maxSettingsBody，
	// 不留隐含语法余量。encoding/json 默认写法下，每个码点的编码不超过其 UTF-8 字节数的六倍，逐码点用例核对这条上界。
	// U+0000–U+001F 中除 \b、\f、\n、\r、\t 外的码点，以及 <、>、&，会写成六字节转义；这五个控制字符只占两字节，
	// U+2028/2029 从三字节变成六字节。只限制清洗后的字符数不足以限制解码前的请求，字符串须有原始字节上限。

	// maxChannelIDJSONBytes 是渠道列表里一条合法 ID 在 JSON 里的最大份额：proto3 JSON 把 int64 写成带引号的十进制串，
	// 合法 ID 为正（渠道 id 从 1 起）、至多 19 位，连引号与分隔的逗号共 22 字节。负数与冗余写法（protojson 也收 1.000
	// 与 1e000）不是合法请求的最坏情况，不在预算内。方括号及末项没有逗号的差额由 budgetIDList 的类型规则计算。
	maxChannelIDJSONBytes = 22

	// maxThemeBody 是 UploadTheme 的解码预算，装下满额主题包的 JSON：bytes 在 JSON 里是带填充的标准 base64，8 MiB
	// 编码成 4 × ⌈8388608 / 3⌉ = 11184812 字节（base64 字母表无需转义；二进制编码是原样 8 MiB，更小）；另留 4 KiB 给
	// expect_id（合法值至多 32 个 ASCII 字符，每个最坏转义成 6 字节）、字段名与 JSON 语法。合计 11188908 字节，约
	// 10.7 MiB：这也是匿名请求在 UploadTheme 上被读到的上限。包本身的 8 MiB 由 theme.Parse 另行核对——二进制编码的
	// 请求在这个预算内能带更大的包。
	maxThemeBody = 4*((theme.MaxPackageBytes+2)/3) + 4<<10
)

type Config struct {
	Backups *backup.Manager
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
	// ThemeOrigin 是 serve 的 --theme-origin（规范形态，经 ListThemes 回显给面板）。空串时主题的五个方法一律
	// FailedPrecondition（requireThemeOrigin）：零值是关闭，与"未配置独立 origin 即不开启上传与托管"同一方向。
	ThemeOrigin string
	// PublicDir 为真表示 serve 给了 --public-dir，面板所在 origin 的公开页由目录接管；经 ListThemes 回显，面板据此
	// 标明启用主题不影响那一页。
	PublicDir bool
	// Geo 是 serve 选定并交给国家查询器的同一个后端对象，New 要求非 nil。面板回显的后端与本地库路径取自它
	// （Settings.geo_backend、geo_mmdb_path），不另由启动参数推导，回显因此不会与查询器实际用的后端分叉；仅回显，
	// 不落入运行设置。
	Geo geo.Backend
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

	// uploading 是容量 1 的信号量，UploadTheme 从校验到入库一直持有它：同一时刻至多一个请求在展开与入库，被引用着的
	// 展开内容至多一份（≤ theme.MaxTotalBytes），Parse 的解压也至多一路。占用时直接拒绝而不排队：到了方法体的请求
	// 已各自持有解码后的包，排队只会把它们攒在内存里。请求体的解码在方法体之前，不归它管，由 maxThemeBody 按请求设界。
	uploading chan struct{}
}

func New(cfg Config, st *store.Store, a *auth.Auth, l *live.Live, nodes NodeState, book *traffic.Book, probes *probe.Registry, alerts *alert.Engine, notifier *alert.Queue, clk clock.Clock, log *slog.Logger) *Service {
	if cfg.TTL <= 0 {
		panic("api.Config.TTL must be positive")
	}
	if cfg.Location == nil {
		panic("api.Config.Location must be set")
	}
	if cfg.Backups == nil {
		panic("api.Config.Backups must be set")
	}
	if err := cfg.Retention.Validate(); err != nil {
		panic("api.Config.Retention: " + err.Error())
	}
	if cfg.Geo == nil {
		panic("api.Config.Geo must be set")
	}
	return &Service{
		cfg: cfg, store: st, auth: a, live: l, nodes: nodes, traffic: book, probes: probes, alerts: alerts, notifier: notifier, clk: clk, log: log,
		history:   history{store: st, log: log},
		access:    accessTable(probev1.File_probe_v1_admin_proto.Services().ByName("AdminService")),
		uploading: make(chan struct{}, 1),
	}
}

// today 是 hub 时区（--timezone）的今天，days_left 以它为基准。
func (s *Service) today() time.Time { return alert.Today(s.clk.Now(), s.cfg.Location) }

// Handler 是 AdminService 的挂载点。两个 connect 处理器挂同一个 Service 与同一个鉴权拦截器，只差解码预算；请求按
// 路径是否等于 UploadTheme 的过程名分给它们。生成的处理器分派过程用的也是 r.URL.Path 的精确相等，所以大预算的
// 处理器只会执行 UploadTheme，其余过程都经小预算的处理器。
func (s *Service) Handler() (string, http.Handler) {
	access := connect.WithInterceptors(s.accessInterceptor())
	path, rest := probev1connect.NewAdminServiceHandler(s, access, connect.WithReadMaxBytes(maxSettingsBody))
	_, upload := probev1connect.NewAdminServiceHandler(s, access, connect.WithReadMaxBytes(maxThemeBody))
	return path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == probev1connect.AdminServiceUploadThemeProcedure {
			upload.ServeHTTP(w, r)
			return
		}
		rest.ServeHTTP(w, r)
	})
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
		candidates := sessionCandidates(req.Header())
		if len(candidates) == 0 {
			return nil, unauthenticated("no session cookie; call Login first")
		}
		tok, alive, err := s.auth.AuthenticateSession(ctx, candidates)
		if err != nil {
			s.log.Error("session lookup failed", "err", err)
			return nil, internalError("session lookup failed")
		}
		if !alive {
			return nil, unauthenticated("session expired or unknown; call Login again")
		}
		// ctx 里放通过校验的那个 token，而不是第一个候选：Logout、撤销当前会话与会话列表的"当前"都读它。
		// 放第一个候选时，排在前面的无效值会让 Logout 吊销一个不存在的会话，真正在用的会话照旧有效。
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

// sessionCandidates 按出现顺序返回请求里全部非空的 probe_session 值，跨所有 Cookie 字段行。
//
// 请求里的 cookie 不全由 hub 签发：与面板同属一个父域的主机能写 Domain 为父域的 cookie，浏览器把它们与管理员的
// host-only 会话放进同一个 Cookie 行，同名时路径更长的排在前面（RFC 6265 §5.4）。会话读取的不变式是多出来的
// cookie 不能让有效会话失效，否则任何能写父域 cookie 的主机都能把管理员锁在面板外。由此：
//   - 返回全部同名值，由 auth.AuthenticateSession 逐个校验、任一有效即通过。
//   - 逐对切分、只认名字，不校验其他 cookie。http.ParseCookie 在同一行里任一 cookie 不合它的语法时整行报错：
//     没有 "="、名字不是 token、去掉两端成对的双引号之后值里仍有双引号、值里有非 ASCII 字节都算，而这些 cookie
//     由别的主机写，形状不归 hub 管。
//   - 不设个数上限，也不用 Request.Cookies：上限让写 cookie 的一方能用更多的值把有效值挤出去；
//     Request.Cookies 遇到超过 3000 个 cookie 时整体返回空，http.ParseCookie 则报错。
//     候选数与校验成本由请求头的大小上限约束，推导见 auth.AuthenticateSession。
//
// 每一对的切分与 Request.Cookies 相同：去掉两端空白，按第一个 "=" 分成名字与值，名字去空白后比较，值两端成对的
// 双引号去掉。值的字节不在这里校验：不是 token 形状的值由 auth.AuthenticateSession 在哈希前丢弃。
func sessionCandidates(h http.Header) []string {
	var out []string
	for _, line := range h.Values("Cookie") {
		for part := range strings.SplitSeq(line, ";") {
			name, value, ok := strings.Cut(textproto.TrimString(part), "=")
			if !ok || textproto.TrimString(name) != SessionCookie {
				continue
			}
			if len(value) > 1 && value[0] == '"' && value[len(value)-1] == '"' {
				value = value[1 : len(value)-1]
			}
			if value != "" {
				out = append(out, value)
			}
		}
	}
	return out
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
	tok, err := s.auth.LoginFactors(ctx, req.Msg.GetPassword(), req.Msg.GetOtp(), req.Msg.GetRecoveryCode(), peer.from)
	switch {
	case errors.Is(err, auth.ErrLoginBusy):
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("password verification is busy; please try again later"))
	case errors.Is(err, auth.ErrLocked):
		return nil, unauthenticated("too many failed logins from this source (one IPv4 address, or one IPv6 /64); retry in 15 minutes")
	case errors.Is(err, auth.ErrNoAdmin), errors.Is(err, auth.ErrBadPassword), errors.Is(err, auth.ErrSecurity), errors.Is(err, store.ErrAdminChanged):
		// 对外同一响应，避免匿名调用方由错误内容判断管理员是否已配置。
		return nil, unauthenticated("密码或第二认证因素无效")
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
