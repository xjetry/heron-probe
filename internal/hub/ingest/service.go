// Package ingest 实现 AgentService：校验 → live 与流量入账、facts 落盘、分钟刷出。
//
// 上报路径只碰内存：鉴权查 auth 的映射，状态写进 live 与 traffic，facts 投递给写协程
// 即返回。分钟桶由 RunFlusher 落盘，流量由 traffic.Book.Run 定时刷出。
package ingest

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
	"github.com/xjetry/probe/internal/hub/auth"
	"github.com/xjetry/probe/internal/hub/live"
	"github.com/xjetry/probe/internal/hub/metric"
	"github.com/xjetry/probe/internal/hub/sanitize"
	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/hub/traffic"
)

// maxBody 限制 AgentService 的请求体：一条上报远小于此，超出的只可能是滥用。
const maxBody = 64 << 10

// MinTTL 限制服务允许的最短离线判定时长，命令行与直接构造共用同一准入边界。
const MinTTL = 10 * time.Second

type Config struct {
	TTL            time.Duration
	TrustedProxies []netip.Prefix
}

type storeWriter interface {
	WriteMinuteRows(ctx context.Context, rows []metric.Row) (int, error)
	UpsertFactsAsync(nodeID int64, hash uint64, f *probev1.Facts, done func(error))
}

type Service struct {
	cfg           Config
	live          *live.Live
	traffic       *traffic.Book
	store         *store.Store
	writer        storeWriter
	auth          *auth.Auth
	clk           clock.Clock
	log           *slog.Logger
	limit         *buckets[int64]
	registerLimit *buckets[netip.Addr]

	// stateMu 将 Report 的鉴权及内存写入与 Forget 排他，防止已放行的在途请求重建状态。
	// 同持时锁序为 pendingMu → stateMu → mu；上报不取 pendingMu，不等待刷盘。
	stateMu sync.RWMutex
	mu      sync.Mutex
	// factsHash 是 hub 已持久化的各节点 facts 摘要；只在写库成功后更新，
	// 写失败则保持旧值，下一次上报会因不一致再次要求 facts。
	factsHash map[int64]uint64

	pendingMu sync.Mutex
	pending   [][]metric.Row
}

func New(cfg Config, l *live.Live, st *store.Store, a *auth.Auth, book *traffic.Book, clk clock.Clock, log *slog.Logger) (*Service, error) {
	if cfg.TTL < MinTTL {
		return nil, fmt.Errorf("TTL %v is below the minimum %v", cfg.TTL, MinTTL)
	}
	return &Service{cfg: cfg, live: l, traffic: book, store: st, writer: st, auth: a, clk: clk, log: log,
		limit: newBuckets[int64](burst), registerLimit: newBuckets[netip.Addr](30), factsHash: map[int64]uint64{}}, nil
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
type nodeTokenKey struct{}
type registerFromKey struct{}

func unauthenticated() error {
	return connect.NewError(connect.CodeUnauthenticated, errors.New("unauthenticated"))
}

// authInterceptor 在挂载点上裁决每个方法的凭据来源。没有在这里显式列出的
// 方法一律拒绝：新增方法不可能因为忘了加检查而被放行。
// 匿名注册的 handler 体内任何语句都可能触碰写协程，因此来源限速也必须
// 在此完成，由拦截器拒绝分发来保证超限请求先于 handler 的一切操作被挡住。
type authInterceptor struct{ service *Service }

func (s *Service) authInterceptor() connect.Interceptor { return authInterceptor{service: s} }

func (i authInterceptor) WrapStreamingHandler(connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(context.Context, connect.StreamingHandlerConn) error { return unauthenticated() }
}

func (i authInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i authInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	s := i.service
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		switch req.Spec().Procedure {
		case probev1connect.AgentServiceRegisterProcedure:
			from := auth.ClientIP(req.Peer().Addr, req.Header().Get("X-Forwarded-For"), s.cfg.TrustedProxies)
			if !s.registerLimit.allow(from, s.clk.Mono(), time.Second) {
				return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("registration rate limit exceeded for source address"))
			}
			// 凭据是请求体里的窗口 key，由 Register 裁决。
			return next(context.WithValue(ctx, registerFromKey{}, from), req)
		case probev1connect.AgentServiceReportProcedure:
			s.stateMu.RLock()
			defer s.stateMu.RUnlock()
			tok, ok := strings.CutPrefix(req.Header().Get("Authorization"), "Bearer ")
			if !ok {
				return nil, unauthenticated()
			}
			id, ok := s.auth.Authenticate(tok)
			if !ok {
				return nil, unauthenticated()
			}
			ctx = context.WithValue(ctx, nodeTokenKey{}, tok)
			return next(context.WithValue(ctx, nodeKey{}, id), req)
		}
		return nil, unauthenticated()
	}
}

func (s *Service) Register(ctx context.Context, req *connect.Request[probev1.RegisterRequest]) (*connect.Response[probev1.RegisterResponse], error) {
	// 挂载点的拦截器只向通过限速的请求传入来源，窗口裁决复用同一来源地址。
	from := ctx.Value(registerFromKey{}).(netip.Addr)
	name := strings.TrimSpace(sanitize.String(req.Msg.GetName(), maxFactString))
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
	// 补充速率为下发速率的两倍，允许正常上报间隔内的一次重试。
	if !s.limit.allow(id, s.clk.Mono(), s.Interval()/2) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("reporting faster than twice the assigned interval"))
	}
	m := req.Msg.GetMetrics()
	if err := validateMetrics(m); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	ts, gap, first := s.live.Observe(id, m)
	// 每 token 由单个 agent 串行发出 unary 上报、等到响应才发下一次；这保证同节点的
	// Observe、Account、AddBytes 不被另一次上报交错。多端共用 token 不提供此保证。
	// 间隔达到 TTL 意味着按在线判定节点在这段时间里离线过，这段增量跨过一次离线，
	// 不能当作当前这一分钟的速率；hub 重启后的首次上报（live 无条目）同理。
	// 流量入账只碰内存；first 与 gap 都来自 Observe 之前的状态。
	if d, ok := s.traffic.Account(id, m); ok && !first && gap < s.cfg.TTL {
		s.live.AddBytes(id, ts, d.Rx, d.Tx)
	}
	want := s.reconcileFacts(id, ctx.Value(nodeTokenKey{}).(string), req.Msg.GetFactsHash(), req.Msg.GetFacts())
	return connect.NewResponse(&probev1.ReportResponse{
		ReportIntervalMs: uint32(s.Interval() / time.Millisecond),
		WantFacts:        want,
	}), nil
}

// reconcileFacts 是电平触发的对账：agent 每次带摘要，hub 只在不一致时索要。
func (s *Service) reconcileFacts(id int64, token string, hash uint64, f *probev1.Facts) bool {
	if f != nil {
		sanitizeFacts(f)
		s.writer.UpsertFactsAsync(id, hash, f, func(err error) {
			if err != nil {
				s.log.Error("facts write failed", "node", id, "err", err)
				return
			}
			s.mu.Lock()
			// 回调可晚于删除；在 mu 下复查 token 并发布摘要，与 Forget 的摘要清理互斥。
			if current, ok := s.auth.Authenticate(token); ok && current == id {
				s.factsHash[id] = hash
			}
			s.mu.Unlock()
		})
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	known, ok := s.factsHash[id]
	return !ok || known != hash
}

// Forget 在 auth 移除 token 后清理节点状态。stateMu 等待已鉴权上报退出，
// pendingMu 将 live 桶移交与重试队列清理串行化，因此返回后两处都不再持有该节点。
func (s *Service) Forget(nodeID int64) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.live.Forget(nodeID)
	s.traffic.Forget(nodeID)
	s.limit.forget(nodeID)
	s.mu.Lock()
	delete(s.factsHash, nodeID)
	s.mu.Unlock()
	var pending [][]metric.Row
	for _, batch := range s.pending {
		var kept []metric.Row
		for _, row := range batch {
			if row.NodeID != nodeID {
				kept = append(kept, row)
			}
		}
		if len(kept) > 0 {
			pending = append(pending, kept)
		}
	}
	s.pending = pending
}
