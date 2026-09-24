package alert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/live"
	"github.com/xjetry/probe/internal/hub/store"
)

// apply 在 writeMu 下调用 Enqueue：不得阻塞，也不得回调 Engine 的写方法，否则会阻塞全部写入或自锁。
// 读方法只取 mu，apply 调用前已释放 mu，因此可以读取 Channels 等快照。
type Sender interface{ Enqueue(ev store.AlertEvent) }
type Config struct{ TTL time.Duration }
type stateKey struct{ rule, node int64 }
type stateEntry struct {
	state   store.AlertState
	sinceAt time.Time
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

func New(cfg Config, st *store.Store, l *live.Live, clk clock.Clock, log *slog.Logger) *Engine {
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
			e.states[stateKey{s.RuleID, s.NodeID}] = stateEntry{s.State, s.SinceAt}
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
func (e *Engine) States() []store.StateRow {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []store.StateRow
	for k, s := range e.states {
		out = append(out, store.StateRow{RuleID: k.rule, NodeID: k.node, State: s.state, SinceAt: s.sinceAt})
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
	if err := CheckRule(r); err != nil {
		return store.AlertRule{}, err
	}
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	saved, err := e.st.SaveAlertRule(ctx, r)
	if err != nil {
		return store.AlertRule{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	previous := e.rules[saved.ID]
	identityChanged := previous.Kind != saved.Kind || previous.TaskID != saved.TaskID
	e.rules[saved.ID] = cloneRule(saved)
	// SaveAlertRule 已在同一事务裁剪状态，缓存只在提交后同步到相同集合。
	for k := range e.states {
		if k.rule == saved.ID && (identityChanged || !saved.Enabled || !inScope(saved, k.node)) {
			delete(e.states, k)
		}
	}
	return saved, nil
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
	if c.Kind == store.ChannelTelegram && c.ID != 0 {
		cfg, err := decodeTelegram(c.Config)
		if err != nil {
			return store.NotifyChannel{}, err
		}
		if cfg.BotToken == "" {
			channels, err := e.st.ListNotifyChannels(ctx)
			if err != nil {
				return store.NotifyChannel{}, err
			}
			var found bool
			for _, old := range channels {
				if old.ID == c.ID {
					found = true
					if old.Kind == store.ChannelTelegram {
						prev, err := decodeTelegram(old.Config)
						if err != nil {
							return store.NotifyChannel{}, err
						}
						cfg.BotToken = prev.BotToken
					}
					break
				}
			}
			if !found {
				return store.NotifyChannel{}, store.NotFoundError{Kind: "notify channel", ID: c.ID}
			}
			b, err := json.Marshal(cfg)
			if err != nil {
				return store.NotifyChannel{}, err
			}
			c.Config = string(b)
		}
	}
	if err := CheckChannel(c); err != nil {
		return store.NotifyChannel{}, err
	}
	if c.Kind == store.ChannelWebhook {
		cfg, err := decodeWebhook(c.Config)
		if err != nil {
			return store.NotifyChannel{}, err
		}
		if cfg.Method == "" {
			cfg.Method = "POST"
			b, err := json.Marshal(cfg)
			if err != nil {
				return store.NotifyChannel{}, err
			}
			c.Config = string(b)
		}
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
func (e *Engine) current(k stateKey) store.AlertState {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if s, ok := e.states[k]; ok {
		return s.state
	}
	return store.StateOK
}

// 调用方持 writeMu；状态、事件与投递先由 store 原子提交，再发布内存并通知 Sender。
func (e *Engine) apply(ctx context.Context, r store.AlertRule, nodeID int64, next store.AlertState, tr *store.Transition, summary string, value float64) error {
	k := stateKey{r.ID, nodeID}
	if e.current(k) == next {
		return nil
	}
	now := e.clk.Now()
	var ev store.AlertEvent
	var err error
	if tr != nil {
		ev, err = e.st.RecordTransition(ctx, r.ID, nodeID, next, store.AlertEvent{Transition: *tr, At: now, Summary: summary, Value: value}, r.ChannelIDs)
	} else {
		err = e.st.SetAlertState(ctx, r.ID, nodeID, next, now)
	}
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.states[k] = stateEntry{next, time.Unix(now.Unix(), 0).UTC()}
	sender := e.sender
	e.mu.Unlock()
	// 未装配 Sender 时转换仍完整落库，投递行可供后续续投，不能因此跳过持久化。
	if tr != nil && sender != nil {
		sender.Enqueue(ev)
	}
	return nil
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
	nodes, err := e.st.ListNodes(ctx)
	if err != nil {
		return err
	}
	now := e.clk.Mono()
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
			cur := e.current(stateKey{r.ID, node.ID})
			// LastSeenAt 是跨进程的墙钟，只供文案；无 live 条目时从本次 Load 的单调起点量时长。
			lastSeen := e.started
			entry, seen := e.live.Get(node.ID)
			if seen {
				lastSeen = entry.LastSeen
			}
			unseen := now - lastSeen
			next, tr := NextOffline(cur, Observation{Reported: seen, Unseen: unseen, Grace: time.Duration(node.OfflineGraceS) * time.Second, TTL: e.cfg.TTL})
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
			if err := e.apply(ctx, r, node.ID, next, tr, summary, unseen.Seconds()); err != nil {
				errs = append(errs, err)
			}
		}
		if err := e.pruneCandidates(ctx, r.ID, candidates); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (e *Engine) EvaluateProbes(ctx context.Context, minuteTS int64) error {
	if minuteTS%60 != 0 {
		return invalid("minuteTS must be aligned to 60 seconds")
	}
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
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
			if err := e.apply(ctx, r, node.ID, next, tr, summary, value); err != nil {
				errs = append(errs, err)
			}
		}
		if err := e.pruneCandidates(ctx, r.ID, candidates); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (e *Engine) RunOfflineSweep(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
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
