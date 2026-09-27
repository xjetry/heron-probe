package api

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/alert"
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

// billingCycles 是协议枚举与库里周期文本的一一对应，未指定对应"没有周期"；TestBillingCyclesMapEveryValue 按两侧全集核对。
var billingCycles = map[probev1.BillingCycle]store.BillingCycle{
	probev1.BillingCycle_BILLING_CYCLE_UNSPECIFIED: store.CycleNone,
	probev1.BillingCycle_BILLING_CYCLE_MONTHLY:     store.CycleMonthly,
	probev1.BillingCycle_BILLING_CYCLE_QUARTERLY:   store.CycleQuarterly,
	probev1.BillingCycle_BILLING_CYCLE_SEMIANNUAL:  store.CycleSemiannual,
	probev1.BillingCycle_BILLING_CYCLE_YEARLY:      store.CycleYearly,
	probev1.BillingCycle_BILLING_CYCLE_BIENNIAL:    store.CycleBiennial,
	probev1.BillingCycle_BILLING_CYCLE_TRIENNIAL:   store.CycleTriennial,
}

// 价格与币种的形状按 §9.4。RE2 的 \d 只匹配 ASCII 数字，全角数字不算。
var (
	pricePattern    = regexp.MustCompile(`^\d{1,9}(\.\d{1,2})?$`)
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
)

// billingOf 是计费字段唯一的校验，与 traffic_reset_day、offline_grace_s 同在 UpdateNode 入口裁决（§9.4）。m 为 nil
// （请求没带 billing）时各项取零值，即五项全清。days_left 由 hub 计算，请求里的值不读。到期日与到期扫描、days_left
// 共用 alert.ParseDate，写进去的日期读侧一定读得懂。自动续期要求周期与到期日都非空：推后需要这两项。扫描对缺周期
// 另有守卫（alert.cycleMonths），不依赖这里。
func billingOf(m *probev1.Billing) (store.Billing, error) {
	b := store.Billing{Price: m.GetPrice(), Currency: m.GetCurrency(), ExpiresOn: m.GetExpiresOn(), AutoRenew: m.GetAutoRenew()}
	if b.Price != "" && !pricePattern.MatchString(b.Price) {
		return store.Billing{}, invalid("billing.price: must match %s, e.g. 12.50; got %q", pricePattern, b.Price)
	}
	if b.Currency != "" && !currencyPattern.MatchString(b.Currency) {
		return store.Billing{}, invalid("billing.currency: must be three uppercase letters (ISO 4217), e.g. USD; got %q", b.Currency)
	}
	if b.Price != "" && b.Currency == "" {
		return store.Billing{}, invalid("billing.currency: required when billing.price is set")
	}
	cycle, ok := billingCycles[m.GetBillingCycle()]
	if !ok {
		values := probev1.BillingCycle(0).Descriptor().Values()
		names := make([]string, values.Len())
		for i := range names {
			names[i] = string(values.Get(i).Name())
		}
		return store.Billing{}, invalid("billing.billing_cycle: must be one of %s; got %s", strings.Join(names, ", "), m.GetBillingCycle())
	}
	b.Cycle = cycle
	if b.ExpiresOn != "" {
		if _, err := alert.ParseDate(b.ExpiresOn); err != nil {
			return store.Billing{}, invalid("billing.expires_on: must be an existing date in YYYY-MM-DD form; got %q", b.ExpiresOn)
		}
	}
	if b.AutoRenew && (b.Cycle == store.CycleNone || b.ExpiresOn == "") {
		return store.Billing{}, invalid("billing.auto_renew: requires billing.billing_cycle and billing.expires_on to be set")
	}
	return b, nil
}

// billingProto 是 Node.billing，也是公开端 PublicBilling 的投影来源。五项都没填时为 nil，字段缺失。today 是 hub 时区的
// 今天（alert.Today）；没有到期日或库里的值读不懂时 days_left 缺失。
func billingProto(b store.Billing, today time.Time) *probev1.Billing {
	if b == (store.Billing{}) {
		return nil
	}
	out := &probev1.Billing{Price: b.Price, Currency: b.Currency, BillingCycle: enumFor(billingCycles, b.Cycle), ExpiresOn: b.ExpiresOn, AutoRenew: b.AutoRenew}
	if d, ok := alert.DaysLeft(b.ExpiresOn, today); ok {
		out.DaysLeft = proto.Int32(int32(d))
	}
	return out
}

// nodeProto 的 today 是 hub 时区的今天（alert.Today）。
func nodeProto(n store.Node, today time.Time) *probev1.Node {
	out := &probev1.Node{Id: n.ID, Name: n.Name, Public: n.Public, Note: n.Note, SortOrder: n.SortOrder, CreatedAt: n.CreatedAt.Unix(), Facts: n.Facts, TrafficResetDay: uint32(n.TrafficResetDay),
		Billing: billingProto(n.Billing, today), LastSource: n.LastSource}
	if !n.LastSeenAt.IsZero() {
		out.LastSeenAt = proto.Int64(n.LastSeenAt.Unix())
	}
	if n.Facts != nil {
		out.FactsUpdatedAt = proto.Int64(n.FactsUpdatedAt.Unix())
	}
	if n.OfflineGraceS != 0 {
		out.OfflineGraceS = proto.Uint32(uint32(n.OfflineGraceS))
	}
	return out
}

// cleanName 按 sanitize.Text 清洗，页面标题（cleanSettings）用同一个口径。
// 按完整清洗结果计字符数；先截字节会把超长输入变成合法名称。
func cleanName(raw string) (string, error) {
	name := sanitize.Text(raw, len(raw))
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
	today := s.today()
	for _, n := range nodes {
		out = append(out, nodeProto(n, today))
	}
	return connect.NewResponse(&probev1.ListNodesResponse{Nodes: out}), nil
}

func (s *Service) CreateNode(ctx context.Context, req *connect.Request[probev1.CreateNodeRequest]) (*connect.Response[probev1.CreateNodeResponse], error) {
	name, err := cleanName(req.Msg.GetName())
	if err != nil {
		return nil, err
	}
	id, tok, err := s.auth.CreateNode(ctx, name)
	if errors.Is(err, store.ErrNodeLimit) {
		return nil, connect.NewError(connect.CodeResourceExhausted, err)
	}
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
	return connect.NewResponse(&probev1.CreateNodeResponse{Node: nodeProto(n, s.today()), Token: tok}), nil
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
	if req.Msg.OfflineGraceS == nil {
		return nil, invalid("offline_grace_s: required; 0 clears it")
	}
	grace := req.Msg.GetOfflineGraceS()
	if grace != 0 && time.Duration(grace)*time.Second < s.cfg.TTL {
		return nil, invalid("offline_grace_s: must be 0 or at least %d seconds (PROBE_OFFLINE_AFTER); got %d", (s.cfg.TTL+time.Second-1)/time.Second, grace)
	}
	billing, err := billingOf(req.Msg.GetBilling())
	if err != nil {
		return nil, err
	}
	edit := store.NodeEdit{Name: name, Public: req.Msg.GetPublic(), Note: note, TrafficResetDay: day, OfflineGraceS: int(grace), Billing: billing}
	s.nodeMu.Lock()
	billingChanged, err := s.store.UpdateNode(ctx, req.Msg.GetId(), edit)
	if err == nil {
		// 只有库提交成功才改内存；nodeMu 跨越两次写入并与删除共用，失败或并发请求都不能使两者分叉。
		s.traffic.SetResetDay(req.Msg.GetId(), day)
	}
	s.nodeMu.Unlock()
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound(req.Msg.GetId())
	}
	if err != nil {
		s.log.Error("updating node failed", "err", err)
		return nil, internalError("updating node failed")
	}
	// 计费字段变了就立刻按新值扫描一次（§9.2）：续费之后不等到零点才恢复。修改已提交，扫描失败只记日志，下一次扫描
	// 会再评估。扫描放在 nodeMu 之外：它要等 writeMu（离线巡检、探测评估、日界扫描都可能正持有），再做一整轮续期
	// 写回与状态写，持 nodeMu 等它只会挡住其它节点的编辑与删除，与 DeleteNode 把清理放在锁外同一个理由。持锁调用
	// 也不会成环：alert 包不 import api，且引擎经 SetSender 注入的实现（当前是 alert.Queue）也不在 api 里，
	// 任何持 writeMu 的路径都取不到 nodeMu。
	if billingChanged {
		if err := s.alerts.SweepExpiry(context.WithoutCancel(ctx)); err != nil {
			s.log.Error("expiry sweep after node update failed", "node", req.Msg.GetId(), "err", err)
		}
	}
	// 扫描之后才回读，响应里的到期日与 days_left 已是推后之后的值。放锁之后节点可能已被并发的 DeleteNode 删掉，
	// 这时按不存在应答。
	n, err := s.store.GetNode(ctx, req.Msg.GetId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound(req.Msg.GetId())
	}
	if err != nil {
		s.log.Error("reading updated node failed", "err", err)
		return nil, internalError("reading updated node failed")
	}
	return connect.NewResponse(&probev1.UpdateNodeResponse{Node: nodeProto(n, s.today())}), nil
}

// DeleteNode 先由 auth 删除库记录和 token，再由状态持有者等待在途上报并清理。
// 返回成功必须同时意味着持久化删除完成与进程内状态清除。
func (s *Service) DeleteNode(ctx context.Context, req *connect.Request[probev1.DeleteNodeRequest]) (*connect.Response[probev1.DeleteNodeResponse], error) {
	s.nodeMu.Lock()
	err := s.auth.DeleteNode(ctx, req.Msg.GetId())
	s.nodeMu.Unlock()
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound(req.Msg.GetId())
	}
	if err != nil {
		s.log.Error("deleting node failed", "err", err)
		return nil, internalError("deleting node failed")
	}
	// 删除已提交，UpdateNode 在库层得到不存在后不会再调 SetResetDay，锁外 Forget 不会与编辑交错重建流量状态。
	// 持 nodeMu 等待在途上报与评估只会阻塞其它节点的编辑，因此清理放在锁外。
	s.nodes.Forget(req.Msg.GetId())
	// auth.DeleteNode 已提交且释放鉴权锁；同步清掉告警缓存，列表不能残留已删除节点的作用域与状态。
	s.alerts.Forget(req.Msg.GetId())
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
