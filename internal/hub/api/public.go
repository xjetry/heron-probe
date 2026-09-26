package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/live"
	"github.com/xjetry/probe/internal/hub/probe"
	"github.com/xjetry/probe/internal/hub/ratelimit"
	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/hub/traffic"
)

// publicMaxBody 是公开请求的解码预算。公开请求的字段只有历史查询的几个数值与节点 id，正常客户端发出的
// JSON 远小于 4 KiB；connect 的 JSON 解码接受任意空白与未知字段，所以没有"最大合法请求"这样的上界，
// 这个数只是对匿名请求体的有界约束。
const publicMaxBody = 4 << 10

const (
	// 每个来源的桶（IPv4 一个地址、IPv6 一个 /64，见 ratelimit.BySource）：容量 60、每秒补充 10（§10）。公开页每 2 秒轮询一次快照，一个访客开几个标签页仍远在其下。
	publicBurst  = 60
	publicRefill = time.Second / 10
)

type PublicConfig struct {
	// ReportInterval 原样经 PublicSnapshot.report_interval_ms 下发。
	ReportInterval time.Duration
	// TrustedProxies 决定限流按哪个来源地址计：只有来自这些对端的 X-Forwarded-For 才被采信；空表示一个都不信。
	TrustedProxies []netip.Prefix
}

// Public 实现 PublicService。它不经会话或 token：挂载点只绑定按来源地址的限流与缓存头（Handler）。
// 可见范围由 store 的 ListPublicNodes 与 NodeIsPublic 承载，节点是否公开在每个请求里现读库；
// 进程里只有 GetSnapshot 的响应字节有 snapshotTTL 的缓存窗口。
type Public struct {
	cfg     PublicConfig
	store   *store.Store
	live    *live.Live
	traffic *traffic.Book
	probes  *probe.Registry
	history history
	clk     clock.Clock
	log     *slog.Logger

	facts   projection
	metrics projection

	// limit 按来源计数，覆盖挂载点收到的每个请求。
	limit *ratelimit.Buckets[netip.Addr]
	// maxAge 是每个接受 GET 的过程的成功响应 max-age，构造时从描述符读出（cachePolicy），之后只读。
	maxAge map[string]uint32
}

func NewPublic(cfg PublicConfig, st *store.Store, l *live.Live, book *traffic.Book, probes *probe.Registry, clk clock.Clock, log *slog.Logger) *Public {
	return &Public{
		cfg: cfg, store: st, live: l, traffic: book, probes: probes, clk: clk, log: log,
		history: history{store: st, log: log},
		facts:   newProjection((&probev1.PublicFacts{}).ProtoReflect().Type(), (&probev1.Facts{}).ProtoReflect().Descriptor()),
		metrics: newProjection((&probev1.PublicMetrics{}).ProtoReflect().Type(), (&probev1.Metrics{}).ProtoReflect().Descriptor()),
		limit:   ratelimit.New[netip.Addr](publicBurst, publicRefill),
		maxAge:  cachePolicy(probeServices()),
	}
}

// connectHandler 是不带挂载点中间件的处理器。
func (p *Public) connectHandler() (string, http.Handler) {
	return probev1connect.NewPublicServiceHandler(p, connect.WithReadMaxBytes(publicMaxBody))
}

// Handler 是公开服务唯一的挂载点：cacheControl(BySource(connect))。限流包在 connect 之外，解码失败的请求同样计数
// （ratelimit.BySource 的注释写了理由）；缓存头包在最外面，限流的 429 也带 no-store。
func (p *Public) Handler() (string, http.Handler) {
	path, h := p.connectHandler()
	return path, p.cacheControl(ratelimit.BySource(p.limit, p.cfg.TrustedProxies, p.clk, h))
}

// cacheControl 只作用于 GET：GET 的 URL 就是缓存键，浏览器与中间缓存可以复用；POST 响应不带缓存头。
// 成功响应按方法声明的 cache_max_age_s；失败响应（含限流的 429 与 NotFound）一律 no-store——节点改为公开后，
// 之前缓存的 NotFound 不能继续挡住访客。
func (p *Public) cacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(&cacheHeaderWriter{ResponseWriter: w, maxAge: p.maxAge[r.URL.Path]}, r)
	})
}

// cacheHeaderWriter 在状态码确定的那一刻写 Cache-Control：之后头已发出，改不了。maxAge 为 0 只出现在
// 注册表之外的路径上，connect 以 404 应答，落到 no-store。
type cacheHeaderWriter struct {
	http.ResponseWriter
	maxAge uint32
	wrote  bool
}

func (c *cacheHeaderWriter) WriteHeader(code int) {
	if !c.wrote {
		c.wrote = true
		v := "no-store"
		if code == http.StatusOK && c.maxAge > 0 {
			v = "max-age=" + strconv.FormatUint(uint64(c.maxAge), 10)
		}
		c.Header().Set("Cache-Control", v)
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *cacheHeaderWriter) Write(b []byte) (int, error) {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	return c.ResponseWriter.Write(b)
}

// Unwrap 让 http.ResponseController 找到底层 ResponseWriter 的能力。
func (c *cacheHeaderWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// cachePolicy 读出接受 GET 的方法的缓存上界，键为 Connect 过程路径，并在装配时核对不变式：一个方法接受 GET
// （idempotency_level = NO_SIDE_EFFECTS，protoc-gen-connect-go 据它生成 GET 支持）当且仅当它声明了正的
// cache_max_age_s。前者缺后者，GET 响应没有上界可写；后者缺前者，声明的上界永远用不上，多半是标错了方法。
// 任一方向不符即 panic，与准入表同一口径：不合的 proto 无法随 hub 启动（NewPublic 在 serve 装配时调用它）。
func cachePolicy(services []protoreflect.ServiceDescriptor) map[string]uint32 {
	table := map[string]uint32{}
	for _, svc := range services {
		methods := svc.Methods()
		for i := 0; i < methods.Len(); i++ {
			m := methods.Get(i)
			opts, _ := m.Options().(*descriptorpb.MethodOptions)
			get := opts.GetIdempotencyLevel() == descriptorpb.MethodOptions_NO_SIDE_EFFECTS
			v, _ := proto.GetExtension(m.Options(), probev1.E_CacheMaxAgeS).(uint32)
			switch {
			case get && v == 0:
				panic(fmt.Sprintf("%s accepts GET (idempotency_level = NO_SIDE_EFFECTS) but does not declare a positive probe.v1.cache_max_age_s", m.FullName()))
			case !get && v != 0:
				panic(fmt.Sprintf("%s declares probe.v1.cache_max_age_s but does not accept GET (idempotency_level is not NO_SIDE_EFFECTS)", m.FullName()))
			case get:
				table["/"+string(svc.FullName())+"/"+string(m.Name())] = v
			}
		}
	}
	return table
}

// probeServices 是 probe.v1 包里的全部服务。包里每个 proto 文件的生成代码都在 gen/probe/v1 这一个 Go 包里，
// 本包 import 了它，所以它们都已登记进 protoregistry.GlobalFiles。
func probeServices() []protoreflect.ServiceDescriptor {
	var out []protoreflect.ServiceDescriptor
	protoregistry.GlobalFiles.RangeFilesByPackage("probe.v1", func(fd protoreflect.FileDescriptor) bool {
		for i := 0; i < fd.Services().Len(); i++ {
			out = append(out, fd.Services().Get(i))
		}
		return true
	})
	return out
}

// noPublicNode 对未公开与不存在的节点是同一个错误：文案不带 id，两种情形的响应逐字节相同，
// 匿名调用方无从由错误区分"存在但未公开"与"不存在"。
func noPublicNode() error {
	return connect.NewError(connect.CodeNotFound, errors.New("node_id: no public node has this id"))
}

// requirePublic 是公开端历史查询的节点准入。
func (p *Public) requirePublic(ctx context.Context, id int64) error {
	public, err := p.store.NodeIsPublic(ctx, id)
	if err != nil {
		p.log.Error("looking up node failed", "err", err)
		return internalError("looking up node failed")
	}
	if !public {
		return noPublicNode()
	}
	return nil
}

func (p *Public) GetSite(ctx context.Context, _ *connect.Request[probev1.GetSiteRequest]) (*connect.Response[probev1.PublicSite], error) {
	st, err := p.store.SiteSettings(ctx)
	if err != nil {
		p.log.Error("reading settings failed", "err", err)
		return nil, internalError("reading settings failed")
	}
	return connect.NewResponse(&probev1.PublicSite{Title: st.Title, Theme: st.Theme, AccentColor: st.AccentColor, Logo: st.Logo, CustomCss: st.CustomCSS}), nil
}

func (p *Public) GetSnapshot(ctx context.Context, _ *connect.Request[probev1.PublicServiceGetSnapshotRequest]) (*connect.Response[probev1.PublicSnapshot], error) {
	nodes, err := p.store.ListPublicNodes(ctx)
	if err != nil {
		p.log.Error("listing public nodes failed", "err", err)
		return nil, internalError("listing nodes failed")
	}
	out := &probev1.PublicSnapshot{Now: p.clk.Now().Unix(), ReportIntervalMs: uint32(p.cfg.ReportInterval / time.Millisecond)}
	for _, n := range nodes {
		online, seen, m := liveState(p.live, n)
		pn := &probev1.PublicNode{Id: n.ID, Name: n.Name, Online: online, LastSeenAt: seen, SortOrder: n.SortOrder, Traffic: trafficProto(p.traffic.View(n.ID))}
		if n.Facts != nil {
			pn.Facts = p.facts.apply(n.Facts).(*probev1.PublicFacts)
		}
		if m != nil {
			pn.Metrics = p.metrics.apply(m).(*probev1.PublicMetrics)
		}
		out.Nodes = append(out.Nodes, pn)
	}
	return connect.NewResponse(out), nil
}

func (p *Public) QueryMetrics(ctx context.Context, req *connect.Request[probev1.QueryMetricsRequest]) (*connect.Response[probev1.QueryMetricsResponse], error) {
	m := req.Msg
	maxPoints, err := checkWindow(m.GetFrom(), m.GetTo(), m.GetMaxPoints())
	if err != nil {
		return nil, err
	}
	if err := p.requirePublic(ctx, m.GetNodeId()); err != nil {
		return nil, err
	}
	resp, err := p.history.metrics(ctx, m, maxPoints)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (p *Public) QueryProbes(ctx context.Context, req *connect.Request[probev1.QueryProbesRequest]) (*connect.Response[probev1.QueryProbesResponse], error) {
	m := req.Msg
	maxPoints, err := checkWindow(m.GetFrom(), m.GetTo(), m.GetMaxPoints())
	if err != nil {
		return nil, err
	}
	if err := p.requirePublic(ctx, m.GetNodeId()); err != nil {
		return nil, err
	}
	node := m.GetNodeId()
	resp, err := p.history.probeSeries(ctx, m, maxPoints, func(id uint64) (probev1.ProbeKind, string, bool) {
		return p.probes.TargetFor(node, id)
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
