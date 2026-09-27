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
	"github.com/xjetry/probe/internal/hub/ratelimit"
	"github.com/xjetry/probe/internal/hub/sanitize"
	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/hub/traffic"
	"github.com/xjetry/probe/internal/probelimit"
)

// Report 的 protobuf 请求由 Metrics 数值标量、boot_id、Facts、有界版本号/摘要和探测结果组成。
// 数值由 proto 类型定界；validateMetrics/validateFacts 将主机字符串各限在 maxHostString 字节。
// validateResults 限条数与错误长度；合法 agent 由 Runner 限批、ToProto 截断错误来遵守这些约束。
// connect 在拦截器前整条读取，超出 maxBody 返回 ResourceExhausted；Runner 会回队，
// 因而合法 agent 的编码上界必须从常量推出，不能因读上限不足而永久重发同一超限批次。
// 这些预算针对已知字段的 protobuf 编码；非法输入仍可能先撞读上限，再也到不了字段校验。
const (
	// Metrics/Facts 的字段类型及字符串校验共同定界；TestHostPayloadFitsMetricsBudget 覆盖满值编码。
	metricsBudget = 32 << 10
	// maxResultWire 含结果及外层 repeated 字段开销，上界由 TestMaxProbeResultWire 钉住。
	maxResultWire = 160
	maxBody       = 256 << 10
)

// 非探测部分加上整批最坏编码不能超过读上限；单结果由 TestMaxProbeResultWire 钉住。
const _ = uint(maxBody - metricsBudget - probelimit.MaxResultsPerReport*maxResultWire)

// MinTTL 与 MaxTTL 限制离线判定时长，命令行与直接构造共用同一准入边界。
const (
	MinTTL        = 10 * time.Second
	MaxTTL        = 180 * time.Second
	reportsPerTTL = 3
)

const (
	// burst 是上报的令牌桶容量：允许上报间隔的抖动与一次立即重试，再多就是异常。
	burst = 3
	// registerBurst 与 registerRefillPer 是 Register 按来源的限速（§5.2；来源的口径见 ratelimit.BySource）：
	// 桶容量与每补充一个令牌的周期。
	registerBurst     = 30
	registerRefillPer = time.Second
)

type Config struct {
	TTL            time.Duration
	TrustedProxies []netip.Prefix
}

type storeWriter interface {
	WriteMinuteBatch(ctx context.Context, batch metric.Batch) (int, error)
	UpsertFactsAsync(nodeID int64, hash uint64, f *probev1.Facts, done func(error))
}

type TaskSource interface {
	Version() uint64
	TasksFor(nodeID int64) *probev1.ProbeTasks
	Assigned(nodeID int64, taskID uint64) bool
	Forget(nodeID int64)
}

type Service struct {
	cfg           Config
	live          *live.Live
	traffic       *traffic.Book
	tasks         TaskSource
	store         *store.Store
	writer        storeWriter
	auth          *auth.Auth
	clk           clock.Clock
	log           *slog.Logger
	limit         *ratelimit.Buckets[int64]
	registerLimit *ratelimit.Buckets[netip.Addr]

	// stateMu 将 Report 的鉴权及内存写入与 Forget 排他，防止已放行的在途请求重建状态。
	// 同持时锁序为 pendingMu → stateMu → mu；上报不取 pendingMu，不等待刷盘。
	stateMu sync.RWMutex
	mu      sync.Mutex
	// factsHash 是 hub 已持久化的各节点 facts 摘要；只在写库成功后更新，
	// 写失败则保持旧值，下一次上报会因不一致再次要求 facts。
	factsHash map[int64]uint64

	pendingMu sync.Mutex
	pending   []metric.Batch
}

func New(cfg Config, l *live.Live, st *store.Store, a *auth.Auth, book *traffic.Book, tasks TaskSource, clk clock.Clock, log *slog.Logger) (*Service, error) {
	if cfg.TTL < MinTTL {
		return nil, fmt.Errorf("TTL %v is below the minimum %v", cfg.TTL, MinTTL)
	}
	if cfg.TTL > MaxTTL {
		return nil, fmt.Errorf("TTL %v is above the maximum %v", cfg.TTL, MaxTTL)
	}
	// 上报的补充周期是下发间隔的一半：允许正常间隔内的一次重试。间隔由 TTL 决定，服务存续期间不变；
	// 限速与下发都经 interval 算，下发间隔改了，限速跟着改。
	return &Service{cfg: cfg, live: l, traffic: book, tasks: tasks, store: st, writer: st, auth: a, clk: clk, log: log,
		limit: ratelimit.New[int64](burst, interval(cfg.TTL)/2), registerLimit: ratelimit.New[netip.Addr](registerBurst, registerRefillPer),
		factsHash: map[int64]uint64{}}, nil
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

// Interval 是下发给 agent 的上报间隔。
func (s *Service) Interval() time.Duration { return interval(s.cfg.TTL) }

// interval 是上报间隔的唯一算法：TTL 内三次上报机会，容得下两次连续失败。下发给 agent 的间隔（Interval）
// 与上报限速的补充周期（New）都从它推出。
func interval(ttl time.Duration) time.Duration { return ttl / reportsPerTTL }

// New 将 TTL 限在 MaxTTL 内；间隔为 interval(TTL) = TTL/reportsPerTTL，满速产出
// MaxTasksPerNode×(TTL/reportsPerTTL)/MinIntervalS 条，单批上限必须容纳它。
const _ = uint(probelimit.MaxResultsPerReport*probelimit.MinIntervalS*reportsPerTTL - probelimit.MaxTasksPerNode*int(MaxTTL/time.Second))

// Handler 挂载 AgentService。Register 是唯一的匿名方法，按来源键限速（§5.2；IPv4 一个地址一桶、IPv6 一个 /64 一桶，见 ratelimit.BySource），限流中间件包在 connect 外面，
// 解码失败的请求同样计数（ratelimit.BySource 的注释写了理由）。Report 不进这个桶：同一出口地址后面可以有很多
// agent，上报按节点限速（Report 方法体里的 s.limit）。路径判定与 connect 分派用同一个 r.URL.Path 全等比较，
// 所以到达 Register 方法体的请求都先经过了限流。
func (s *Service) Handler() (string, http.Handler) {
	path, h := probev1connect.NewAgentServiceHandler(s,
		connect.WithInterceptors(s.authInterceptor()),
		connect.WithReadMaxBytes(maxBody))
	register := ratelimit.BySource(s.registerLimit, s.cfg.TrustedProxies, s.clk, h)
	return path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == probev1connect.AgentServiceRegisterProcedure {
			register.ServeHTTP(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}

type nodeKey struct{}
type nodeTokenKey struct{}

func unauthenticated() error {
	return connect.NewError(connect.CodeUnauthenticated, errors.New("unauthenticated"))
}

// authInterceptor 在挂载点上裁决每个方法的凭据来源。没有在这里显式列出的
// 方法一律拒绝：新增方法不可能因为忘了加检查而被放行。
// Register 的来源限速不在这里，在更外层的 Handler：超限请求连解码都不进，更到不了方法体里会触碰写协程的语句。
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
			// 凭据是请求体里的窗口 key，由 Register 裁决。
			return next(ctx, req)
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
	// 窗口裁决的失败计数与限速按同一个来源键（IPv4 按地址、IPv6 按 /64）：由 Handler 里的 ratelimit.BySource 算出并放进 ctx。
	// 取不到只可能是挂载绕过了 Handler，属于装配错误。
	from, ok := ratelimit.SourceOf(ctx)
	if !ok {
		panic("ingest: Register reached without the source-address rate limit; mount Service.Handler")
	}
	name := sanitize.Text(req.Msg.GetName(), maxHostString)
	if name == "" {
		name = "node"
	}
	id, tok, err := s.auth.Register(ctx, req.Msg.GetKey(), name, from)
	if errors.Is(err, auth.ErrDenied) {
		return nil, unauthenticated()
	}
	// 继承的 all_nodes 任务超限只在窗口与 key 都通过之后才会发生（store.RegisterNode 先判窗口与 key，再 insertNode），
	// 调用方已持有效 key，说明原文可以给它：修复要由管理员减少 all_nodes 任务，probe-agent register 把原文打到 stderr。
	if errors.Is(err, store.ErrNodeLimit) {
		return nil, connect.NewError(connect.CodeResourceExhausted, err)
	}
	if err != nil {
		s.log.Error("register failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("registration failed"))
	}
	s.log.Info("node registered", "node", id, "name", name, "source", auth.DescribeSource(from))
	return connect.NewResponse(&probev1.RegisterResponse{NodeId: id, Token: tok}), nil
}

func (s *Service) Report(ctx context.Context, req *connect.Request[probev1.ReportRequest]) (*connect.Response[probev1.ReportResponse], error) {
	id := ctx.Value(nodeKey{}).(int64)
	if !s.limit.Allow(id, s.clk.Mono()) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("reporting faster than twice the assigned interval"))
	}
	m := req.Msg.GetMetrics()
	if err := validateMetrics(m); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := validateResults(req.Msg.GetProbeResults()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := validateFacts(req.Msg.GetFacts()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	// 来源地址只取 hub 在这次请求上看到的对端，经 auth.ClientIP 按 --trusted-proxies 解析，与限流、登录锁定同一口径：
	//   - 不读 CF-Connecting-IP 之类的旁路头：任何客户端都能自己带上它们，读了就等于采信请求方自述；
	//   - 不采信 agent 自报的地址：那是 agent 的自述，与"hub 看到什么"是两个事实，混在一列里无法区分。
	source := auth.SourceText(auth.ClientIP(req.Peer().Addr, req.Header().Values("X-Forwarded-For"), s.cfg.TrustedProxies))
	ts, gap, first := s.live.Observe(id, source, m)
	// 每 token 由单个 agent 串行发出 unary 上报、等到响应才发下一次；这保证同节点的
	// Observe、Account、AddBytes 不被另一次上报交错。多端共用 token 不提供此保证。
	// 间隔达到 TTL 意味着按在线判定节点在这段时间里离线过，这段增量跨过一次离线，
	// 不能当作当前这一分钟的速率；hub 重启后的首次上报（live 无条目）同理。
	// 流量入账只碰内存；first 与 gap 都来自 Observe 之前的状态。
	if d, ok := s.traffic.Account(id, m); ok && !first && gap < s.cfg.TTL {
		s.live.AddBytes(id, ts, d.Rx, d.Tx)
	}
	s.foldResults(id, req.Msg.GetProbeResults())
	want := s.reconcileFacts(id, ctx.Value(nodeTokenKey{}).(string), req.Msg.GetFactsHash(), req.Msg.GetFacts())
	resp := &probev1.ReportResponse{
		ReportIntervalMs: uint32(s.Interval() / time.Millisecond),
		WantFacts:        want,
	}
	// 电平触发：agent 报它持有的版本，hub 只在不一致时下发整份清单；空清单让 agent 停掉已消失的任务。
	if req.Msg.GetTasksVersion() != s.tasks.Version() {
		resp.Tasks = s.tasks.TasksFor(id)
	}
	return connect.NewResponse(resp), nil
}

// foldResults 按归属与迟到预算逐条准入。task_id 未分配给本节点的结果不得写进本节点的历史：
// token 被挪用时它是伪造的，分配撤销后仍在途时它属于已不承担的任务。
// 超龄结果可能落在已冻结的分钟里；测量时刻由收到时刻减 age_ms 得到。
func (s *Service) foldResults(id int64, rs []*probev1.ProbeResult) {
	if len(rs) == 0 {
		return
	}
	now := s.clk.Now()
	var foreign, late int
	for _, r := range rs {
		if !s.tasks.Assigned(id, r.GetTaskId()) {
			foreign++
			continue
		}
		age := time.Duration(r.GetAgeMs()) * time.Millisecond
		if age > MaxProbeAge {
			late++
			continue
		}
		s.live.AddProbe(id, now.Add(-age), r.GetTaskId(), r)
	}
	if foreign > 0 || late > 0 {
		s.log.Warn("probe results dropped", "node", id, "unassigned", foreign, "too_old", late)
	}
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
	// api.DeleteNode 在 auth.DeleteNode 返回 nil 后才清理状态，保证库删除与 token 撤销已经完成。
	// Registry.Forget 会等在途管理写入完成整个 store 往返；写协程 facts 回调会取 mu，
	// 持 mu 等待可能形成等待环，持 stateMu 或 mu 会挡住其他节点的 Report，
	// 持 pendingMu 会挡住全体节点的分钟刷出，因此必须在所有 ingest 锁之外调用。
	s.tasks.Forget(nodeID)
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.live.Forget(nodeID)
	s.traffic.Forget(nodeID)
	s.limit.Forget(nodeID)
	s.mu.Lock()
	delete(s.factsHash, nodeID)
	s.mu.Unlock()
	var pending []metric.Batch
	for _, batch := range s.pending {
		kept := batch.WithoutNode(nodeID)
		if !kept.Empty() {
			pending = append(pending, kept)
		}
	}
	s.pending = pending
}
