package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"connectrpc.com/connect"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/live"
	"github.com/xjetry/probe/internal/hub/probe"
	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/hub/traffic"
)

// publicMaxBody 是公开请求的解码预算。公开请求的字段只有历史查询的几个数值与节点 id，正常客户端发出的
// JSON 远小于 4 KiB；connect 的 JSON 解码接受任意空白与未知字段，所以没有"最大合法请求"这样的上界，
// 这个数只是对匿名请求体的有界约束。
const publicMaxBody = 4 << 10

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
}

func NewPublic(cfg PublicConfig, st *store.Store, l *live.Live, book *traffic.Book, probes *probe.Registry, clk clock.Clock, log *slog.Logger) *Public {
	return &Public{
		cfg: cfg, store: st, live: l, traffic: book, probes: probes, clk: clk, log: log,
		history: history{store: st, log: log},
		facts:   newProjection((&probev1.PublicFacts{}).ProtoReflect().Type(), (&probev1.Facts{}).ProtoReflect().Descriptor()),
		metrics: newProjection((&probev1.PublicMetrics{}).ProtoReflect().Type(), (&probev1.Metrics{}).ProtoReflect().Descriptor()),
	}
}

// connectHandler 是不带挂载点中间件的处理器。
func (p *Public) connectHandler() (string, http.Handler) {
	return probev1connect.NewPublicServiceHandler(p, connect.WithReadMaxBytes(publicMaxBody))
}

// Handler 是公开服务唯一的挂载点。
func (p *Public) Handler() (string, http.Handler) { return p.connectHandler() }

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
