package alert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/live"
	"github.com/xjetry/probe/internal/hub/store"
)

// flush 在一次评估调用结束时、仍持 writeMu 时逐个事件调用 Enqueue：不得阻塞，也不得回调 Engine 的写方法，否则会
// 阻塞全部写入或自锁。读方法只取 mu，flush 调用时不持 mu，因此可以读取 Channels 等快照。
type Sender interface{ Enqueue(ev store.AlertEvent) }

// cycle 是一次评估调用（一次 SweepOffline、一次 EvaluateProbes、一次 sweepExpiry）：同一次调用产生的事件属于同一个
// 评估周期，按调用而不是时间窗划分，合并窗口因此有上界——至多一次离线巡检或一次探测评估，不为等更多事件推迟发送。
//
// 合并只发生在投递层，事件层不变：alert_event 仍每个规则×节点一行，事件是状态机的事实记录，合并是发送策略。
// 合并的键是（渠道，评估周期，规则，转换方向）；firing 与 recovered 不混，一条消息里既有掉线又有恢复读不出结论。
// batches 记下本周期里每个键已开的批次，后续同键的转换请求加入它（store.DeliveryTarget.Batch）。events 在调用结束时
// 才交给 Sender：经这条路径入队的批次，worker 第一次读到它时已经收齐。worker 从库里补货（窗口曾满、启动积压或存储
// 故障之后）不经过这里，可能在调用中途读到尚未收齐的批次；那时一次发送覆盖哪些行由 store.BeginBatchAttempt 保证与
// 拼消息时读到的相同，此后加入的行要么让它拒绝、worker 重读后一并发出，要么因批次已开始尝试而新开一批。
type cycle struct {
	batches map[batchKey]int64
	events  []store.AlertEvent
}

type batchKey struct {
	channel, rule int64
	transition    store.Transition
}

func newCycle() *cycle { return &cycle{batches: map[batchKey]int64{}} }

// flush 把本周期落库的事件交给 Sender。调用方持 writeMu，在评估调用返回前调用（含出错返回），已提交的转换都会入队；
// 未装配 Sender 时转换仍完整落库，投递行可供后续续投。
func (e *Engine) flush(cy *cycle) {
	e.mu.RLock()
	sender := e.sender
	e.mu.RUnlock()
	if sender == nil {
		return
	}
	for _, ev := range cy.events {
		sender.Enqueue(ev)
	}
}

type Config struct {
	TTL time.Duration
	// Location 是 hub 的 --timezone：到期日按它的日历日计（§9.4），New 要求非 nil。
	Location *time.Location
}
type stateKey struct{ rule, node int64 }
type stateEntry struct {
	state   store.AlertState
	sinceAt time.Time
	// firedExpiresOn 与库里的 alert_state.fired_expires_on 同值（含义见 store.StateRow.FiredExpiresOn）：Load 从库读入，
	// apply 在写库成功后随状态一起发布。
	firedExpiresOn string
	// recoveredAt 与库里的 alert_state.recovered_at 同值（含义见 store.StateRow.RecoveredAt），Load 读入，apply 写库成功后发布。
	recoveredAt time.Time
	// flapping 是离线巡检最近一次对这对规则与节点的判定：pending 且只因抖动抑制而未进入 firing（FlapDeferred）。
	// 它是由当前观测派生的展示量，不落库：重启后第一轮巡检即重新算出。
	flapping bool
}

// StateView 是对外的一条状态：落库的状态行，加上引擎在离线巡检里算出、不落库的 Flapping。
type StateView struct {
	store.StateRow
	Flapping bool
}

// writeMu 串行化读库、写库到内存发布；mu 只保护内存快照，不跨存储往返持有。
// 只有存储成功才在 mu 下发布，失败写入不会改变缓存。
type Engine struct {
	cfg      Config
	st       *store.Store
	live     *live.Live
	clk      clock.Clock
	log      *slog.Logger
	writeMu  sync.Mutex
	mu       sync.RWMutex
	rules    map[int64]store.AlertRule
	channels map[int64]store.NotifyChannel
	states   map[stateKey]stateEntry
	started  time.Duration
	sender   Sender
}

// New 对缺时区的 Config panic：到期扫描对 nil 时区调用 time.Time.In 会在运行中 panic，装配错误应当在启动时暴露。
func New(cfg Config, st *store.Store, l *live.Live, clk clock.Clock, log *slog.Logger) *Engine {
	if cfg.Location == nil {
		panic("alert.Config.Location must be set")
	}
	return &Engine{cfg: cfg, st: st, live: l, clk: clk, log: log, rules: map[int64]store.AlertRule{}, channels: map[int64]store.NotifyChannel{}, states: map[stateKey]stateEntry{}}
}
func (e *Engine) SetSender(s Sender) { e.mu.Lock(); defer e.mu.Unlock(); e.sender = s }

func (e *Engine) Load(ctx context.Context) error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	rules, err := e.st.ListAlertRules(ctx)
	if err != nil {
		return err
	}
	channels, err := e.st.ListNotifyChannels(ctx)
	if err != nil {
		return err
	}
	states, err := e.st.ListAlertStates(ctx)
	if err != nil {
		return err
	}
	validRules := map[int64]store.AlertRule{}
	for _, r := range rules {
		if err := CheckRule(r); err != nil {
			e.log.Warn("invalid alert rule skipped", "rule_id", r.ID, "err", err)
			continue
		}
		validRules[r.ID] = cloneRule(r)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.started = e.clk.Mono()
	e.rules = validRules
	e.channels = map[int64]store.NotifyChannel{}
	e.states = map[stateKey]stateEntry{}
	for _, c := range channels {
		e.channels[c.ID] = c
	}
	for _, s := range states {
		if _, ok := validRules[s.RuleID]; ok {
			e.states[stateKey{s.RuleID, s.NodeID}] = stateEntry{state: s.State, sinceAt: s.SinceAt, firedExpiresOn: s.FiredExpiresOn, recoveredAt: s.RecoveredAt}
		}
	}
	return nil
}
func cloneRule(r store.AlertRule) store.AlertRule {
	r.NodeIDs = slices.Clone(r.NodeIDs)
	r.ChannelIDs = slices.Clone(r.ChannelIDs)
	return r
}
func (e *Engine) Rules() []store.AlertRule {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []store.AlertRule
	for _, r := range e.rules {
		out = append(out, cloneRule(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (e *Engine) Channels() []store.NotifyChannel {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []store.NotifyChannel
	for _, c := range e.channels {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (e *Engine) States() []StateView {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []StateView
	for k, s := range e.states {
		out = append(out, StateView{store.StateRow{RuleID: k.rule, NodeID: k.node, State: s.state, SinceAt: s.sinceAt, FiredExpiresOn: s.firedExpiresOn, RecoveredAt: s.recoveredAt}, s.flapping})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RuleID != out[j].RuleID {
			return out[i].RuleID < out[j].RuleID
		}
		return out[i].NodeID < out[j].NodeID
	})
	return out
}

// AllNodes 是唯一的放宽开关；SweepOffline 每轮从 store 枚举节点，因此新建节点自动纳入。
// 显式空集可能由删除节点产生，不能解释为全部节点。
func inScope(r store.AlertRule, id int64) bool { return r.AllNodes || slices.Contains(r.NodeIDs, id) }

func (e *Engine) SaveRule(ctx context.Context, r store.AlertRule) (store.AlertRule, error) {
	// 保存请求必须明确指定非空作用域；这不限制 DeleteNode 留下的持久化空集。
	if !r.AllNodes && len(r.NodeIDs) == 0 {
		return store.AlertRule{}, invalid("node_ids", "must not be empty unless all_nodes is true")
	}
	if err := CheckRule(r); err != nil {
		return store.AlertRule{}, err
	}
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	saved, err := e.st.SaveAlertRule(ctx, r)
	if err != nil {
		return store.AlertRule{}, err
	}
	e.publishRule(saved)
	// 除此之外，到期规则只在启动、日界与计费变化时评估：若不在这里评估一次，新建、启用或改了提前天数的规则要等到
	// 下一个日界才有状态。规则已提交，扫描失败只记日志：保存本身成功了，下一次扫描会再评估。
	if saved.Enabled && saved.Kind == store.KindExpiry {
		if err := e.sweepExpiry(context.WithoutCancel(ctx)); err != nil {
			e.log.Error("expiry sweep after saving rule failed", "rule_id", saved.ID, "err", err)
		}
	}
	return saved, nil
}

// publishRule 在 SaveAlertRule 提交之后把规则与状态裁剪同步到内存；调用方持 writeMu。
func (e *Engine) publishRule(saved store.AlertRule) {
	e.mu.Lock()
	defer e.mu.Unlock()
	previous := e.rules[saved.ID]
	// 种类、任务或指标变化后，旧观测不再描述当前规则，与 SaveAlertRule 的状态裁剪一致。
	// threshold、for_minutes 与 days_before 不改变身份，状态沿用，下一轮按新值判断是否恢复。
	identityChanged := previous.Kind != saved.Kind || previous.TaskID != saved.TaskID || previous.Metric != saved.Metric
	e.rules[saved.ID] = cloneRule(saved)
	// SaveAlertRule 已在同一事务裁剪状态，缓存只在提交后同步到相同集合。
	for k := range e.states {
		if k.rule == saved.ID && (identityChanged || !saved.Enabled || !inScope(saved, k.node)) {
			delete(e.states, k)
		}
	}
}
func (e *Engine) DeleteRule(ctx context.Context, id int64) error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	if err := e.st.DeleteAlertRule(ctx, id); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.rules, id)
	for k := range e.states {
		if k.rule == id {
			delete(e.states, k)
		}
	}
	return nil
}
func (e *Engine) SaveChannel(ctx context.Context, c store.NotifyChannel) (store.NotifyChannel, error) {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	var previous store.NotifyChannel
	if c.ID != 0 {
		channels, err := e.st.ListNotifyChannels(ctx)
		if err != nil {
			return store.NotifyChannel{}, err
		}
		for _, old := range channels {
			if old.ID == c.ID {
				previous = old
				break
			}
		}
		if previous.ID == 0 {
			return store.NotifyChannel{}, store.NotFoundError{Kind: store.ObjectNotifyChannel, ID: c.ID}
		}
	}
	// api.channelProto 不回显凭据；writeMu 跨越读旧配置、合并、提交，两个保存不能互相覆盖所保留的值。
	switch c.Kind {
	case store.ChannelTelegram:
		cfg, err := decodeTelegram(c.Config)
		if err != nil {
			return store.NotifyChannel{}, err
		}
		if cfg.BotToken == "" && previous.Kind == store.ChannelTelegram {
			prev, err := decodeTelegram(previous.Config)
			if err != nil {
				return store.NotifyChannel{}, err
			}
			cfg.BotToken = prev.BotToken
		}
		b, err := json.Marshal(cfg)
		if err != nil {
			return store.NotifyChannel{}, err
		}
		c.Config = string(b)
	case store.ChannelWebhook:
		cfg, err := decodeWebhook(c.Config)
		if err != nil {
			return store.NotifyChannel{}, err
		}
		var prev WebhookConfig
		if previous.Kind == store.ChannelWebhook {
			prev, err = decodeWebhook(previous.Config)
			if err != nil {
				return store.NotifyChannel{}, err
			}
		}
		if cfg.URL == "" {
			cfg.URL = prev.URL
		}
		headers := map[string]string{}
		maps.Copy(headers, prev.Headers)
		for _, k := range cfg.RemoveHeaders {
			delete(headers, http.CanonicalHeaderKey(k))
		}
		maps.Copy(headers, cfg.Headers)
		cfg.Headers = headers
		if cfg.Method == "" {
			cfg.Method = "POST"
		}
		b, err := json.Marshal(cfg)
		if err != nil {
			return store.NotifyChannel{}, err
		}
		c.Config = string(b)
	}
	if err := CheckChannel(c); err != nil {
		return store.NotifyChannel{}, err
	}
	if c.Kind == store.ChannelWebhook {
		cfg, err := decodeWebhook(c.Config)
		if err != nil {
			return store.NotifyChannel{}, err
		}
		// remove_headers 只描述这次写入；持久化配置只保留合并结果，不混入已执行的指令。
		cfg.RemoveHeaders = nil
		b, err := json.Marshal(cfg)
		if err != nil {
			return store.NotifyChannel{}, err
		}
		c.Config = string(b)
	}
	saved, err := e.st.SaveNotifyChannel(ctx, c)
	if err != nil {
		return store.NotifyChannel{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.channels[saved.ID] = saved
	return saved, nil
}
func (e *Engine) DeleteChannel(ctx context.Context, id int64) error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	if err := e.st.DeleteNotifyChannel(ctx, id); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.channels, id)
	return nil
}

// 调用方须在 store.DeleteNode 成功后且不持有存储回调需要的锁时调用。
// writeMu 等待在途评估发布完毕，防止其随后重建已删除节点的内存状态。
func (e *Engine) Forget(nodeID int64) {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	for k := range e.states {
		if k.node == nodeID {
			delete(e.states, k)
		}
	}
	for id, r := range e.rules {
		r.NodeIDs = slices.DeleteFunc(r.NodeIDs, func(id int64) bool { return id == nodeID })
		e.rules[id] = r
	}
}
func (e *Engine) current(k stateKey) store.AlertState { return e.entry(k).state }

// entry 是 k 的内存状态；没有状态的一对规则与节点按 ok 处理。
func (e *Engine) entry(k stateKey) stateEntry {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if s, ok := e.states[k]; ok {
		return s
	}
	return stateEntry{state: store.StateOK}
}

// setFlapping 在 apply 成功之后更新展示用的抖动标记；状态不变时 apply 不写库，标记仍要跟着这一轮的观测走。
// 没有状态项（ok 且从未写过）的一对不会是 pending，无需记录。调用方持 writeMu。
func (e *Engine) setFlapping(k stateKey, flapping bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if s, ok := e.states[k]; ok {
		s.flapping = flapping
		e.states[k] = s
	}
}

// 调用方持 writeMu；状态、事件与投递先由 store 原子提交，再发布内存，事件记入 cy，在调用结束时由 flush 交给 Sender。
// firedExpiresOn 只由到期扫描在进入 firing 时给出，其余调用传空。状态不变时 apply 直接返回、不写库，所以一个 firing
// 状态记的始终是它进入 firing 那一刻的到期日。
func (e *Engine) apply(ctx context.Context, cy *cycle, r store.AlertRule, nodeID int64, next store.AlertState, firedExpiresOn string, tr *store.Transition, summary string, value float64) error {
	k := stateKey{r.ID, nodeID}
	cur := e.entry(k)
	if cur.state == next {
		return nil
	}
	now := e.clk.Now()
	since := time.Unix(now.Unix(), 0).UTC()
	// 状态行整行写入，上次恢复时刻必须显式带上：离线规则的恢复转换记下当下，其余写入沿用当前值（恢复之后的再次离线
	// 先写成 pending，那一次若不带上，窗口恰在要用时丢失）；其余种类不做抖动抑制，恒为零值。
	var recoveredAt time.Time
	if r.Kind == store.KindOffline {
		recoveredAt = cur.recoveredAt
		if tr != nil && *tr == store.TransitionRecovered {
			recoveredAt = since
		}
	}
	var ev store.AlertEvent
	var err error
	var merged map[int64]bool
	if tr != nil {
		targets := make([]store.DeliveryTarget, len(r.ChannelIDs))
		merged = e.mergedChannels(r.ChannelIDs)
		for i, id := range r.ChannelIDs {
			targets[i] = store.DeliveryTarget{ChannelID: id}
			if merged[id] {
				targets[i].Batch = cy.batches[batchKey{id, r.ID, *tr}]
			}
		}
		ev, err = e.st.RecordTransition(ctx, r.ID, nodeID, next, firedExpiresOn, recoveredAt, store.AlertEvent{Transition: *tr, At: now, Summary: summary, Value: value}, targets)
	} else {
		// SetAlertState 不写触发日期，内存与库记同一个值。
		firedExpiresOn = ""
		err = e.st.SetAlertState(ctx, r.ID, nodeID, next, now, recoveredAt)
	}
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.states[k] = stateEntry{state: next, sinceAt: since, firedExpiresOn: firedExpiresOn, recoveredAt: recoveredAt}
	e.mu.Unlock()
	if tr != nil {
		// 记下实际所在的批次而不是请求的：请求加入的批次已开始尝试时，store 新开了一批，后续同键的行加入新批。
		for _, d := range ev.Deliveries {
			if merged[d.ChannelID] {
				cy.batches[batchKey{d.ChannelID, r.ID, *tr}] = d.BatchID
			}
		}
		cy.events = append(cy.events, ev)
	}
	return nil
}

// mergedChannels 按当前渠道快照给出 ids 中合并发送的渠道（mergesBatches）。已删除的渠道不在快照里，按不合并处理，
// 随后 RecordTransition 的引用检查会拒绝它。
func (e *Engine) mergedChannels(ids []int64) map[int64]bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if c, ok := e.channels[id]; ok && mergesBatches(c.Kind) {
			out[id] = true
		}
	}
	return out
}

// 调用方持 writeMu 且已成功读取本规则候选集；读库失败时不能用空集代替候选集。
// 单行删除先持久化再发布，规则不再适用不是恢复观测，因此不经过 apply。
func (e *Engine) pruneCandidates(ctx context.Context, ruleID int64, keep map[int64]bool) error {
	var errs []error
	for _, s := range e.States() {
		if s.RuleID != ruleID || keep[s.NodeID] {
			continue
		}
		if err := e.st.DeleteAlertState(ctx, ruleID, s.NodeID); err != nil {
			errs = append(errs, err)
			continue
		}
		e.mu.Lock()
		delete(e.states, stateKey{ruleID, s.NodeID})
		e.mu.Unlock()
		e.log.Info("alert state no longer applicable", "rule_id", ruleID, "node_id", s.NodeID)
	}
	return errors.Join(errs...)
}

func (e *Engine) SweepOffline(ctx context.Context) error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	cy := newCycle()
	defer e.flush(cy)
	nodes, err := e.st.ListNodes(ctx)
	if err != nil {
		return err
	}
	now, wall := e.clk.Mono(), e.clk.Now()
	var errs []error
	for _, r := range e.Rules() {
		if !r.Enabled || r.Kind != store.KindOffline {
			continue
		}
		candidates := map[int64]bool{}
		for _, node := range nodes {
			if !inScope(r, node.ID) {
				continue
			}
			candidates[node.ID] = true
			k := stateKey{r.ID, node.ID}
			cur := e.entry(k)
			// LastSeenAt 是跨进程的墙钟，只供文案；无 live 条目时从本次 Load 的单调起点量时长。
			lastSeen := e.started
			entry, seen := e.live.Get(node.ID)
			if seen {
				lastSeen = entry.LastSeen
			}
			unseen := now - lastSeen
			o := Observation{Reported: seen, Unseen: unseen, Grace: time.Duration(node.OfflineGraceS) * time.Second, TTL: e.cfg.TTL}
			if !cur.recoveredAt.IsZero() {
				// 离线开始的墙钟 = 现在 − 已离线时长；已离线时长按单调钟量，只有上次恢复时刻需要跨重启的墙钟。
				o.Recovered, o.SinceRecovery = true, wall.Add(-unseen).Sub(cur.recoveredAt)
			}
			next, tr := NextOffline(cur.state, o)
			summary := fmt.Sprintf("节点 %s 离线 %s（规则 %s）", node.Name, unseen.Round(time.Second), r.Name)
			if !seen {
				last := "从未上报"
				if !node.LastSeenAt.IsZero() {
					last = "最后在线于 " + node.LastSeenAt.Format(time.RFC3339)
				}
				summary = fmt.Sprintf("节点 %s 离线，%s（规则 %s）", node.Name, last, r.Name)
			}
			if tr != nil && *tr == store.TransitionRecovered {
				summary = fmt.Sprintf("节点 %s 已恢复上报（规则 %s）", node.Name, r.Name)
			}
			if err := e.apply(ctx, cy, r, node.ID, next, "", tr, summary, unseen.Seconds()); err != nil {
				errs = append(errs, err)
				continue
			}
			e.setFlapping(k, FlapDeferred(next, o))
		}
		if err := e.pruneCandidates(ctx, r.ID, candidates); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (e *Engine) EvaluateProbes(ctx context.Context, minuteTS int64) error {
	if minuteTS%60 != 0 {
		return invalid("minuteTS", "must be aligned to 60 seconds")
	}
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	cy := newCycle()
	defer e.flush(cy)
	nodes, err := e.st.ListNodes(ctx)
	if err != nil {
		return err
	}
	lv, _ := store.LevelByName("1m")
	var errs []error
	for _, r := range e.Rules() {
		if !r.Enabled || r.Kind != store.KindProbe {
			continue
		}
		ids, err := e.st.ProbeTaskNodeIDs(ctx, r.TaskID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		candidates := map[int64]bool{}
		for _, node := range nodes {
			if !inScope(r, node.ID) || !slices.Contains(ids, node.ID) {
				continue
			}
			candidates[node.ID] = true
			from := minuteTS - int64(r.ForMinutes-1)*60
			rows, err := e.st.QueryProbes(ctx, node.ID, from, minuteTS+60, lv, 60)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			samples := make([]MinuteSample, r.ForMinutes)
			for _, row := range rows {
				if row.TaskID != r.TaskID {
					continue
				}
				b := row.Bucket
				var value float64
				switch r.Metric {
				case store.MetricLossPct:
					if b.Sent == 0 {
						continue
					}
					value = float64(b.Lost) / float64(b.Sent) * 100
				case store.MetricRttMs:
					if b.RttN == 0 {
						continue
					}
					value = float64(b.RttSumUs) / float64(b.RttN) / 1000
				}
				// 触发含阈值等号，恢复则必须低于阈值。
				samples[(row.TS-from)/60] = MinuteSample{Present: true, Exceeds: value >= r.Threshold, Value: value}
			}
			next, tr := NextProbe(e.current(stateKey{r.ID, node.ID}), samples, r.ForMinutes)
			value := samples[len(samples)-1].Value
			summary := fmt.Sprintf("节点 %s 规则 %s：%s %.1f", node.Name, r.Name, r.Metric, value)
			if err := e.apply(ctx, cy, r, node.ID, next, "", tr, summary, value); err != nil {
				errs = append(errs, err)
			}
		}
		if err := e.pruneCandidates(ctx, r.ID, candidates); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// OfflineSweepEvery 是离线巡检周期。RunOfflineSweep 用它做 ticker。
// 取消后必须在下一次 tick 前返回；节点过了离线宽限之后，投递还要再赶上这一周期。
const OfflineSweepEvery = 10 * time.Second

func (e *Engine) RunOfflineSweep(ctx context.Context) {
	ticker := time.NewTicker(OfflineSweepEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := e.SweepOffline(ctx); err != nil {
				e.log.Error("offline sweep failed", "err", err)
			}
		}
	}
}

func nextProbeAt(now time.Time) time.Time {
	return now.Truncate(time.Minute).Add(time.Minute + 3*time.Second)
}

func probeMinuteAt(trigger time.Time) int64 {
	return trigger.Truncate(time.Minute).Add(-time.Minute).Unix()
}

// 分钟边界后 3s 晚于刷出调度的 0.5s 与维护调度的 2s，给通常的写入留出时间并错开竞争；
// 这些偏移不是提交屏障，存储阻塞时仍可能读到缺失分钟，NextProbe 不把缺失当作恢复。
func (e *Engine) RunProbeEvaluation(ctx context.Context) {
	for {
		now := e.clk.Now()
		timer := time.NewTimer(nextProbeAt(now).Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		minuteTS := probeMinuteAt(e.clk.Now())
		if err := e.EvaluateProbes(ctx, minuteTS); err != nil {
			e.log.Error("probe evaluation failed", "err", err)
		}
	}
}
