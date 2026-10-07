package traffic

import (
	"context"
	"log/slog"
	"math"
	"sync"
	"time"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/clock"
	"github.com/xjetry/heron-probe/internal/hub/store"
)

// FlushPeriod 是脏条目落盘的周期；退出时另刷一次。正常刷出下崩溃最多丢一个周期的内存增量；
// 只要 agent 未换启动周期、网卡集合且计数器未倒退，重启后首次上报相对已落盘基线的差分会把这段补回。
// 刷出持续失败时丢的不止一个周期；Flush 保留脏状态，Run 记录日志并重试。
const FlushPeriod = 10 * time.Second

// DefaultResetDay 与 node.traffic_reset_day 的列默认值一致。
const DefaultResetDay = 1

// NoBaseline 是 LastRx/LastTx 的哨兵：条目由校正建立，还没见过这台机器的计数器。
// 计数器来自 uint64、截断后非负，-1 不会与真实读数混淆。
const NoBaseline int64 = -1

type Storage interface {
	LoadTraffic(ctx context.Context) ([]store.TrafficRecord, error)
	WriteTraffic(ctx context.Context, recs []store.TrafficRecord) ([]int64, error)
	TrafficResetDays(ctx context.Context) (map[int64]int, error)
}

var _ Storage = (*store.Store)(nil)

// State 是落盘的累计状态，与 traffic 表一行对应。计数器与累计值用 int64：与 SQLite
// INTEGER 同宽；计数器在入账时截到 MaxInt64，管理接口拒绝越界的校正用量。
type State struct {
	BootID                           string
	NetCounterEpoch                  string
	LastRx, LastTx, TotalRx, TotalTx int64
	PeriodRx, PeriodTx               int64
	PeriodStart                      time.Time
}

func (s State) HasBaseline() bool { return s.LastRx != NoBaseline }

// Entry 是读侧视图：状态加上由重置日推出的边界，后两项不落盘。
type Entry struct {
	State
	ResetDay  int
	NextReset time.Time
}

// Delta 是一次上报入账的字节增量。
type Delta struct{ Rx, Tx int64 }

type entry struct {
	State
	committed *State
	dirty     bool
}

type Book struct {
	st  Storage
	clk clock.Clock
	tz  *time.Location
	log *slog.Logger

	// 同一节点写库的顺序与内存状态更新的顺序一致，否则 Flush 的旧快照会盖掉已成功的校正。
	// Flush、Adjust 与 Commit 统一按 writeMu → mu 取锁；Flush 等待写库时释放 mu，入账不等待数据库。
	writeMu sync.Mutex

	// mu 保护 entries 与 resetDay。Adjust 与 Commit 在 mu 下同步写库：持锁期间所有入账等待，
	// 换来"返回成功即已持久化、返回失败则内存与库都未变"；校正是罕见的管理操作。
	mu       sync.Mutex
	entries  map[int64]*entry
	resetDay map[int64]int
}

func New(st Storage, clk clock.Clock, tz *time.Location, log *slog.Logger) *Book {
	return &Book{st: st, clk: clk, tz: tz, log: log, entries: map[int64]*entry{}, resetDay: map[int64]int{}}
}

func (b *Book) Zone() *time.Location { return b.tz }

// Load 从库恢复条目与重置日。只要 agent 未换启动周期、网卡集合且计数器未倒退，恢复的基线
// 就让重启后的首次上报通过差分补回最后一次成功刷出之后丢失的内存增量。
func (b *Book) Load(ctx context.Context) error {
	recs, err := b.st.LoadTraffic(ctx)
	if err != nil {
		return err
	}
	days, err := b.st.TrafficResetDays(ctx)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries = map[int64]*entry{}
	// 库值是 int64，类型已限定上界；手工改库可能留下负累计值，恢复时统一收敛到非负范围。
	for _, r := range recs {
		b.entries[r.NodeID] = &entry{State: State{BootID: r.BootID, NetCounterEpoch: r.NetCounterEpoch, LastRx: r.LastRx, LastTx: r.LastTx, TotalRx: max(r.TotalRx, 0), TotalTx: max(r.TotalTx, 0),
			PeriodRx: max(r.PeriodRx, 0), PeriodTx: max(r.PeriodTx, 0), PeriodStart: r.PeriodStart}}
		s := b.entries[r.NodeID].State
		b.entries[r.NodeID].committed = &s
	}
	b.resetDay = days
	return nil
}

func clamp(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}

// satAdd 是非负增量的饱和加法：总量到界后停在 MaxInt64，不回绕成负数。
// 调用方保证 d >= 0（差分时计数器不小于基线，校正值经 clamp 非负）。
func satAdd(a, d int64) int64 {
	if a > math.MaxInt64-d {
		return math.MaxInt64
	}
	return a + d
}

func (b *Book) day(nodeID int64) int {
	if d, ok := b.resetDay[nodeID]; ok && d >= 1 && d <= 28 {
		return d
	}
	return DefaultResetDay
}

// rolled 只计算状态副本，校正写库失败时不能把周期滚动留在内存里。
// 周期起点按旧时区落盘；换时区后若该起点之后的首个新边界已过，下一次读取、入账或刷出会清零周期量。
// 连续错过多个周期时直接跳到 now 所在的周期——中间周期的用量没有载体，本来就丢了。
func (b *Book) rolled(s State, day int, now time.Time) (State, bool) {
	if now.Before(NextResetAfter(s.PeriodStart, day, b.tz)) {
		return s, false
	}
	s.PeriodStart = PeriodStart(now, day, b.tz)
	s.PeriodRx, s.PeriodTx = 0, 0
	return s, true
}

func (b *Book) roll(nodeID int64, e *entry, now time.Time) {
	if next, changed := b.rolled(e.State, b.day(nodeID), now); changed {
		e.State, e.dirty = next, true
	}
}

func (b *Book) view(nodeID int64, s State) Entry {
	day := b.day(nodeID)
	return Entry{State: s, ResetDay: day, NextReset: NextResetAfter(s.PeriodStart, day, b.tz)}
}

func (b *Book) fresh(nodeID int64, now time.Time) State {
	return State{LastRx: NoBaseline, LastTx: NoBaseline, PeriodStart: PeriodStart(now, b.day(nodeID), b.tz)}
}

// Account 入账一次上报：返回应进分钟桶的增量；false 表示这次没有增量（首次、重启、
// 回绕、缺读数）。增量是否真的进桶由调用方按上报间隔判定。
func (b *Book) Account(nodeID int64, m *heronv1.Metrics) (Delta, bool) {
	if m.NetRxTotal == nil || m.NetTxTotal == nil {
		// 无读数不是 0：基线不动，下一次仍相对旧基线做差分。
		return Delta{}, false
	}
	rx, tx := clamp(m.GetNetRxTotal()), clamp(m.GetNetTxTotal())
	now := b.clk.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.entries[nodeID]
	if e == nil {
		e = &entry{State: b.fresh(nodeID, now)}
		b.entries[nodeID] = e
	} else {
		b.roll(nodeID, e, now)
	}
	if !e.HasBaseline() || e.BootID != m.GetBootId() || e.NetCounterEpoch != m.GetNetCounterEpoch() || rx < e.LastRx || tx < e.LastTx {
		// 启动周期、网卡集合或计数方向不一致时，聚合计数无法确定相对旧基线的有效增量；只重建基线，
		// 舍弃整个跨基线区间，避免把另一台机器或新增网卡已有的计数记为本区间流量。
		e.BootID, e.LastRx, e.LastTx, e.dirty = m.GetBootId(), rx, tx, true
		e.NetCounterEpoch = m.GetNetCounterEpoch()
		return Delta{}, false
	}
	d := Delta{Rx: rx - e.LastRx, Tx: tx - e.LastTx}
	e.LastRx, e.LastTx = rx, tx
	e.TotalRx, e.TotalTx = satAdd(e.TotalRx, d.Rx), satAdd(e.TotalTx, d.Tx)
	e.PeriodRx, e.PeriodTx = satAdd(e.PeriodRx, d.Rx), satAdd(e.PeriodTx, d.Tx)
	e.dirty = true
	return d, true
}

// Get 返回有条目的节点的视图；读也会推进周期，所以周期量在边界之后读到的一定是清零的。
func (b *Book) Get(nodeID int64) (Entry, bool) {
	now := b.clk.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.entries[nodeID]
	if !ok {
		return Entry{}, false
	}
	b.roll(nodeID, e, now)
	return b.view(nodeID, e.State), true
}

// View 是每个节点都有的视图：没有条目的节点得到零用量与按重置日算出的当前周期边界，
// 不创建条目。hub 对从未上报的节点确实累计了 0 字节，这与"无读数"不同。
func (b *Book) View(nodeID int64) Entry {
	if e, ok := b.Get(nodeID); ok {
		return e
	}
	now := b.clk.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.view(nodeID, b.fresh(nodeID, now))
}

// Committed 原样复制已提交观测，不按当前重置日滚动。Load、成功的 Flush、Adjust、Commit
// 是唯一发布点；告警引擎另检查周期有效性，未提交的清零不能造成恢复事件。
func (b *Book) Committed() map[int64]State {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[int64]State, len(b.entries))
	for id, e := range b.entries {
		if e.committed != nil {
			out[id] = *e.committed
		}
	}
	return out
}

// Commit 与 Adjust、Flush 共用写锁顺序，周期滚动与写库成功后才发布；锁内不调用告警引擎。
func (b *Book) Commit(ctx context.Context, nodeID int64) (Entry, error) {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.entries[nodeID]
	if e == nil {
		e = &entry{State: b.fresh(nodeID, b.clk.Now())}
	}
	next, _ := b.rolled(e.State, b.day(nodeID), b.clk.Now())
	written, err := b.st.WriteTraffic(ctx, []store.TrafficRecord{record(nodeID, next)})
	if err != nil {
		return Entry{}, err
	}
	if len(written) == 0 {
		return Entry{}, store.ErrNotFound
	}
	e.State, e.committed, e.dirty = next, &next, false
	b.entries[nodeID] = e
	return b.view(nodeID, next), nil
}

// SetResetDay 只更新配置。边界按新日子在下一次入账、读取或刷出时判定：若 now 已越过
// 按新日子算出的下一个边界，周期立即开始新的一段、周期量清零。
func (b *Book) SetResetDay(nodeID int64, day int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resetDay[nodeID] = day
}

func record(nodeID int64, s State) store.TrafficRecord {
	return store.TrafficRecord{NodeID: nodeID, BootID: s.BootID, NetCounterEpoch: s.NetCounterEpoch, LastRx: s.LastRx, LastTx: s.LastTx, TotalRx: s.TotalRx, TotalTx: s.TotalTx,
		PeriodRx: s.PeriodRx, PeriodTx: s.PeriodTx, PeriodStart: s.PeriodStart}
}

// Adjust 把当前周期用量覆盖为给定值，总量按同一差值调整，基线不动。
// 总量始终不小于周期量（入账对两者加同一增量，校正按同一差值改两者），所以
// total - period 非负；max 只是对手工改库的防御。写穿：锁内先写库、成功才改内存。
// 没有条目的节点也可以校正：先建一条没有基线的条目，它见到的首个计数器只取基线。
// 节点不存在时写事务跳过该行，这里转成 ErrNotFound 且不留下条目。
func (b *Book) Adjust(ctx context.Context, nodeID int64, periodRx, periodTx uint64) (Entry, error) {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	// api 已拒绝越界的校正值，这里只对直接调用者兜底。
	rx, tx := clamp(periodRx), clamp(periodTx)
	now := b.clk.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.entries[nodeID]
	if e == nil {
		e = &entry{State: b.fresh(nodeID, now)}
	}
	next, _ := b.rolled(e.State, b.day(nodeID), now)
	next.TotalRx = satAdd(max(next.TotalRx-next.PeriodRx, 0), rx)
	next.TotalTx = satAdd(max(next.TotalTx-next.PeriodTx, 0), tx)
	next.PeriodRx, next.PeriodTx = rx, tx
	written, err := b.st.WriteTraffic(ctx, []store.TrafficRecord{record(nodeID, next)})
	if err != nil {
		return Entry{}, err
	}
	if len(written) == 0 {
		return Entry{}, store.ErrNotFound
	}
	e.State, e.dirty = next, false
	e.committed = &next
	b.entries[nodeID] = e
	return b.view(nodeID, next), nil
}

// Forget 在节点删除后清理；删除节点的 traffic 行由 DeleteNode 的事务负责。
func (b *Book) Forget(nodeID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.entries, nodeID)
	delete(b.resetDay, nodeID)
}

// Flush 判定周期滚动并把脏条目一次写库。写失败时把这些条目重新标脏，下一轮重试；
// 期间新入账的条目本来就是脏的，不会被误清。
func (b *Book) Flush(ctx context.Context) error {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	now := b.clk.Now()
	b.mu.Lock()
	var recs []store.TrafficRecord
	var ids []int64
	written := map[int64]State{}
	for id, e := range b.entries {
		b.roll(id, e, now)
		if e.dirty {
			recs = append(recs, record(id, e.State))
			ids = append(ids, id)
			written[id] = e.State
			e.dirty = false
		}
	}
	b.mu.Unlock()
	if len(recs) == 0 {
		return nil
	}
	committed, err := b.st.WriteTraffic(ctx, recs)
	if err != nil {
		b.mu.Lock()
		for _, id := range ids {
			if e := b.entries[id]; e != nil {
				e.dirty = true
			}
		}
		b.mu.Unlock()
		return err
	}
	if skipped := len(recs) - len(committed); skipped > 0 {
		b.log.Warn("traffic rows for deleted nodes dropped", "skipped", skipped)
	}
	b.mu.Lock()
	for _, id := range committed {
		state := written[id]
		if e := b.entries[id]; e != nil {
			e.committed = &state
		}
	}
	b.mu.Unlock()
	return nil
}

// Run 每 FlushPeriod 刷出一次，ctx 结束时再刷一次后返回。调用方在停止 HTTP 准入之后、
// 关库之前等待它返回，最后一次刷出才不会漏掉最后几次上报。
func (b *Book) Run(ctx context.Context) {
	t := time.NewTicker(FlushPeriod)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			if err := b.Flush(context.Background()); err != nil {
				b.log.Error("traffic flush on shutdown failed", "err", err)
			}
			return
		case <-t.C:
			if err := b.Flush(ctx); err != nil {
				b.log.Error("traffic flush failed", "err", err)
			}
		}
	}
}
