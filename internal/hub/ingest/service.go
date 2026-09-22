// Package ingest 实现 AgentService：校验 → live、facts 落盘、分钟刷出。
//
// 上报路径只碰内存：鉴权查 auth 的映射，状态写进 live，facts 投递给写协程
// 即返回。落盘由 RunFlusher 的分钟定时器驱动。
package ingest

import (
	"context"
	"errors"
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
	"github.com/xjetry/probe/internal/hub/auth"
	"github.com/xjetry/probe/internal/hub/live"
	"github.com/xjetry/probe/internal/hub/metric"
	"github.com/xjetry/probe/internal/hub/store"
)

// maxBody 限制 AgentService 的请求体：一条上报远小于此，超出的只可能是滥用。
const maxBody = 64 << 10

type Config struct {
	TTL            time.Duration
	TrustedProxies []netip.Prefix
}

type minuteWriter interface {
	WriteMinuteRows(ctx context.Context, rows []metric.Row) (int, error)
}

type Service struct {
	cfg    Config
	live   *live.Live
	store  *store.Store
	writer minuteWriter
	auth   *auth.Auth
	clk    clock.Clock
	log    *slog.Logger
	limit  *limiter

	mu sync.Mutex
	// factsHash 是 hub 已持久化的各节点 facts 摘要；只在写库成功后更新，
	// 写失败则保持旧值，下一次上报会因不一致再次要求 facts。
	factsHash map[int64]uint64

	pendingMu sync.Mutex
	pending   [][]metric.Row
}

func New(cfg Config, l *live.Live, st *store.Store, a *auth.Auth, clk clock.Clock, log *slog.Logger) *Service {
	return &Service{cfg: cfg, live: l, store: st, writer: st, auth: a, clk: clk, log: log, limit: newLimiter(), factsHash: map[int64]uint64{}}
}

func (s *Service) Load(ctx context.Context) error {
	m, err := s.store.FactsHashes(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.factsHash = m
	s.mu.Unlock()
	return nil
}

// Interval 是下发给 agent 的上报间隔：TTL 内三次上报机会，容得下两次连续失败。
func (s *Service) Interval() time.Duration { return s.cfg.TTL / 3 }

func (s *Service) Handler() (string, http.Handler) {
	return probev1connect.NewAgentServiceHandler(s,
		connect.WithInterceptors(s.authInterceptor()),
		connect.WithReadMaxBytes(maxBody))
}

type nodeKey struct{}

func unauthenticated() error {
	return connect.NewError(connect.CodeUnauthenticated, errors.New("unauthenticated"))
}

// authInterceptor 在挂载点上裁决每个方法的凭据来源。没有在这里显式列出的
// 方法一律拒绝：新增方法不可能因为忘了加检查而被放行。
func (s *Service) authInterceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			switch req.Spec().Procedure {
			case probev1connect.AgentServiceRegisterProcedure:
				// 凭据是请求体里的窗口 key，由 Register 裁决。
				return next(ctx, req)
			case probev1connect.AgentServiceReportProcedure:
				tok, ok := strings.CutPrefix(req.Header().Get("Authorization"), "Bearer ")
				if !ok {
					return nil, unauthenticated()
				}
				id, ok := s.auth.Authenticate(tok)
				if !ok {
					return nil, unauthenticated()
				}
				return next(context.WithValue(ctx, nodeKey{}, id), req)
			}
			return nil, unauthenticated()
		}
	})
}

func (s *Service) Register(ctx context.Context, req *connect.Request[probev1.RegisterRequest]) (*connect.Response[probev1.RegisterResponse], error) {
	from := auth.ClientIP(req.Peer().Addr, req.Header().Get("X-Forwarded-For"), s.cfg.TrustedProxies)
	name := sanitizeString(strings.TrimSpace(req.Msg.GetName()))
	if name == "" {
		name = "node"
	}
	id, tok, err := s.auth.Register(ctx, req.Msg.GetKey(), name, from)
	if errors.Is(err, auth.ErrDenied) {
		return nil, unauthenticated()
	}
	if err != nil {
		s.log.Error("register failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("registration failed"))
	}
	s.log.Info("node registered", "node", id, "name", name, "from", from)
	return connect.NewResponse(&probev1.RegisterResponse{NodeId: id, Token: tok}), nil
}

func (s *Service) Report(ctx context.Context, req *connect.Request[probev1.ReportRequest]) (*connect.Response[probev1.ReportResponse], error) {
	id := ctx.Value(nodeKey{}).(int64)
	if !s.limit.allow(id, s.clk.Mono(), s.Interval()) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("reporting faster than twice the assigned interval"))
	}
	m := req.Msg.GetMetrics()
	if err := validateMetrics(m); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	s.live.Observe(id, m)
	want := s.reconcileFacts(id, req.Msg.GetFactsHash(), req.Msg.GetFacts())
	return connect.NewResponse(&probev1.ReportResponse{
		ReportIntervalMs: uint32(s.Interval() / time.Millisecond),
		WantFacts:        want,
	}), nil
}

// reconcileFacts 是电平触发的对账：agent 每次带摘要，hub 只在不一致时索要。
func (s *Service) reconcileFacts(id int64, hash uint64, f *probev1.Facts) bool {
	if f != nil {
		sanitizeFacts(f)
		s.store.UpsertFactsAsync(id, hash, f, func(err error) {
			if err != nil {
				s.log.Error("facts write failed", "node", id, "err", err)
				return
			}
			s.mu.Lock()
			s.factsHash[id] = hash
			s.mu.Unlock()
		})
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	known, ok := s.factsHash[id]
	return !ok || known != hash
}
