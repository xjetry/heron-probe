package alert

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/store"
)

const QueueCap = 256

// backoff[n-1] 是第 n 次尝试以可重试的渠道失败告终后，到下一次尝试的最短间隔。
var backoff = [...]time.Duration{time.Second, 4 * time.Second}

// MaxRetryAfter 是 429 应答的 Retry-After 纳入退避时的上限（§9.3）：固定的 1 s 与 4 s 短于 Telegram 常见的几十秒，
// 照它重试多数投递会以失败告终；不设上限则一个接收方就能让批次在内存里等上任意久。
const MaxRetryAfter = 5 * time.Minute

// rateWindow 是渠道节奏上限的计量窗口：rate_per_minute 限的是任意一段 rateWindow 内发出的请求数。
const rateWindow = time.Minute

// maxListedNodes 是一条合并消息逐行列出的节点数上限，其余写"另外 N 台"（§9.3）：IM 渠道对单条消息有长度上限，
// 全部节点同时掉线时消息必须仍能发出。
const maxListedNodes = 20

// DeliveryRetryWait 是渠道失败可重试、应答不带 429 的 Retry-After、渠道节奏未满且存储正常时，一个批次在各次尝试之间
// 至多等待的总和：第 n 次尝试得到可重试的渠道失败且未到 store.MaxDeliveryAttempts 时，retryWait 给出 backoff[n-1]，
// 所以至多依次等过 backoff 的每一项。不在其内的等待有三种：429 的 Retry-After 把单次等待拉长到至多 MaxRetryAfter；
// 渠道节奏已满时批次排队到下一个空位（rateSlot）；存储失败时 Run 经 retryAfterFailure 退避（1s 起翻倍、上限 1 分钟），
// 存储持续失败时没有总量上界。启动行以 delivery_retry_wait 报出，scripts/e2e.sh 据此推出告警等待上限——e2e 的接收器
// 是不限节奏的 Webhook 渠道且不回 429；TestQueueGivesUpAfterMaxAttempts 把它钉到实际等过的总和上。
func DeliveryRetryWait() time.Duration {
	var total time.Duration
	for _, d := range backoff {
		total += d
	}
	return total
}

// Queue 是投递的有界窗口与唯一的 worker。窗口里的单位是发送批次（store.DeliveryBatch）：批次在入库时已经定下，
// 队列只按批次号装填、发送与重试，不再合并或拆分——重启后按原批次重投，结果与崩溃前"发了什么"一致。
//
// 窗口分两部分：ready 是可以立即尝试的批次（先进先出），waiting 是等待重试间隔或渠道节奏空位的批次。等待不占用
// worker：一个接收方要求的几分钟 Retry-After 或一个渠道的节奏上限，不能拖住发往其它渠道的批次，也不能让满队列的
// 最坏投递时长超过告警事件的最短保留期（TestAlertRetentionCoversFullQueueDelivery）。
type Queue struct {
	st           *store.Store
	channels     func() []store.NotifyChannel
	client       *http.Client
	telegramBase string
	clk          clock.Clock
	sleep        func(context.Context, time.Duration) error
	log          *slog.Logger
	limit        int           // ready 与 waiting 合计至多这么多批次；NewQueue 取 QueueCap。
	wake         chan struct{} // 容量 1：enqueue 放入批次后发出，worker 空闲时据此醒来。
	mu           sync.Mutex
	ready        []int64
	waiting      map[int64]time.Duration // 批次 → 最早可尝试的单调时刻（clk.Mono）。
	active       map[int64]struct{}      // ready ∪ waiting ∪ 正在尝试的批次。
	overflow     bool                    // 库中可能仍有窗口外的待投递批次，包括非终态错误出窗的项。
	// sent 只由 worker 读写，不经 mu：渠道 → 最近 rateWindow 内各次请求的单调时刻，升序。进程重启后为空，
	// 所以重启后的第一分钟可能与上一个进程的最后一分钟叠加出至多两倍的请求数。
	sent map[int64][]time.Duration
}

var _ Sender = (*Queue)(nil)

func NewQueue(st *store.Store, channels func() []store.NotifyChannel, client *http.Client, telegramBase string, clk clock.Clock, sleep func(context.Context, time.Duration) error, log *slog.Logger) *Queue {
	if sleep == nil {
		sleep = sleepContext
	}
	return &Queue{st: st, channels: channels, client: client, telegramBase: telegramBase, clk: clk, sleep: sleep, log: log, limit: QueueCap,
		wake: make(chan struct{}, 1), waiting: make(map[int64]time.Duration), active: make(map[int64]struct{}), sent: make(map[int64][]time.Duration)}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// mu 同时保护窗口和 active；去重让有界窗口只装不同批次，同一批次的多行、重复的 Enqueue 与并发的 Requeue 都只占一格。
// worker 每次尝试前回读批次，保证已终态的旧项不再发送，包括并发 Requeue 读到的旧快照。
func (q *Queue) enqueue(batch int64, evict bool) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, exists := q.active[batch]; exists {
		return true
	}
	if len(q.ready)+len(q.waiting) >= q.limit {
		q.overflow = true
		if !evict {
			return false
		}
		if len(q.ready) == 0 {
			// 窗口全是等待中的批次：它们的等待时刻只在内存里，挤出后由补货装回会提前重试；新批次留在库里，
			// 等窗口有空位时由补货装入。
			q.log.Warn("notification queue full of waiting batches; delivery batch left for refill", "batch_id", batch)
			return false
		}
		old := q.ready[0]
		q.ready = q.ready[1:]
		delete(q.active, old)
		q.log.Warn("notification queue full; oldest delivery batch dropped", "batch_id", old)
	}
	q.ready = append(q.ready, batch)
	q.active[batch] = struct{}{}
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return true
}

// Engine 在一次评估调用结束时、仍持 writeMu 时调用 Enqueue；这里只操作有界内存窗口，不等数据库或网络。
// 同一批次的多行（多个事件）只入队一次。满窗口丢弃的项仍在库中保持 done=0；overflow 让 worker 在窗口取空后补货，
// 在本进程内续投。
func (q *Queue) Enqueue(ev store.AlertEvent) {
	for _, d := range ev.Deliveries {
		if !d.Done {
			q.enqueue(d.BatchID, true)
		}
	}
}

// 库中 done=0 的行是真源；按批次号升序装填窗口，未装下的批次由 worker 取空后继续补货。
func (q *Queue) Requeue(ctx context.Context) error {
	batches, err := q.st.PendingBatches(ctx)
	if err != nil {
		return err
	}
	for _, b := range batches {
		if !q.enqueue(b, false) {
			break
		}
	}
	return nil
}

// outcome 是一次尝试之后批次的去向：retry 为假时批次已终态或已不存在，离开窗口；为真时进入 waiting，到 at 再试。
type outcome struct {
	retry bool
	at    time.Duration
}

// 由装配方启动一个 worker；退出不清空库中待投递行，下一次 Requeue 接续未完成项。
// 每次发送前已持久化尝试计数，发送次数受 MaxDeliveryAttempts 约束。
// 结果未落盘可能来自崩溃、写失败，或关停取消时请求已到对端；后续重发都消耗尝试名额。
func (q *Queue) Run(ctx context.Context) {
	var retryDelay time.Duration
	for ctx.Err() == nil {
		batch, refill, until, timed := q.next()
		switch {
		case refill:
			// 只有 worker 自动补货，数据库往返不持 mu，也不占用 Engine 的写锁。
			if err := q.Requeue(ctx); err != nil {
				if ctx.Err() != nil {
					return
				}
				q.log.Error("notification refill failed", "err", err)
				if err := q.retryAfterFailure(ctx, &retryDelay); err != nil {
					return
				}
			}
		case batch != 0:
			out, err := q.attempt(ctx, batch)
			q.mu.Lock()
			if err == nil && out.retry {
				q.waiting[batch] = out.at
			} else {
				delete(q.active, batch)
			}
			q.mu.Unlock()
			if err != nil && ctx.Err() == nil {
				q.log.Error("notification delivery failed", "batch_id", batch, "err", err)
				if err := q.retryAfterFailure(ctx, &retryDelay); err != nil {
					return
				}
			} else if err == nil {
				retryDelay = 0
			}
		default:
			q.idle(ctx, until, timed)
		}
	}
}

// next 取下一件事：到期的等待批次先按到期先后回到 ready 队尾；ready 取空且可能有库中余项时补货（窗口全是等待中的
// 批次时不补——装不进任何一项，只会空转读库）；否则取 ready 队首。都没有时给出最早的到期时刻供 idle 等待。
// 在 mu 下清掉 wake：此前的入队都已反映在这次看到的窗口里，之后的入队会重新发出。
func (q *Queue) next() (batch int64, refill bool, until time.Duration, timed bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	select {
	case <-q.wake:
	default:
	}
	now := q.clk.Mono()
	var due []int64
	for b, at := range q.waiting {
		if at <= now {
			due = append(due, b)
		}
	}
	slices.SortFunc(due, func(a, b int64) int { return cmp.Or(cmp.Compare(q.waiting[a], q.waiting[b]), cmp.Compare(a, b)) })
	for _, b := range due {
		delete(q.waiting, b)
		q.ready = append(q.ready, b)
	}
	if len(q.ready) == 0 && q.overflow && len(q.waiting) < q.limit {
		q.overflow = false
		return 0, true, 0, false
	}
	if len(q.ready) > 0 {
		batch, q.ready = q.ready[0], q.ready[1:]
		return batch, false, 0, false
	}
	for _, at := range q.waiting {
		if !timed || at < until {
			until, timed = at, true
		}
	}
	return 0, false, until, timed
}

// idle 在没有可做的事时等待：有新入队即醒；有等待中的批次时最迟醒在最早的到期时刻。等待经 q.sleep，测试据此推进时钟。
func (q *Queue) idle(ctx context.Context, until time.Duration, timed bool) {
	if !timed {
		select {
		case <-ctx.Done():
		case <-q.wake:
		}
		return
	}
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-q.wake:
			cancel()
		case <-wctx.Done():
		}
	}()
	_ = q.sleep(wctx, until-q.clk.Mono())
}

// 非终态出窗与补货读失败都保留续投信号；故障期间不能靠窗口大小决定是否重试。
// 单个批次的发送上限由 BeginBatchAttempt 保证；退避避免存储故障期间空转读库和高频错误日志。
// 同一个 worker 共用退避，attempt 返回 nil（已终态、已不存在或已排入等待）时复位。
func (q *Queue) retryAfterFailure(ctx context.Context, delay *time.Duration) error {
	q.mu.Lock()
	q.overflow = true
	q.mu.Unlock()
	if *delay == 0 {
		*delay = time.Second
	} else {
		*delay = min(*delay*2, time.Minute)
	}
	return q.sleep(ctx, *delay)
}

// message 由批次的事件组成这次发送的消息：规则与节点用当前名称，事件文案用落库的原文。
//
// 单个事件的批次就是那个事件本身，Webhook 的模板字段逐事件含义不变。多个事件的批次合成一条：首行写规则、台数与转换
// 方向，其后逐行列出至多 maxListedNodes 个事件的原文，超出的写"另外 N 台"。批次只按（渠道，评估周期，规则，转换
// 方向）合并（Engine.apply），所以同批事件的规则与方向相同，首行取第一个事件的即可。合并只发生在 Telegram 渠道；
// 渠道在批次待发期间被改成 Webhook 时仍按合并文案发一次请求（Node 为逐个列出的节点名，Value 为 0）——"一个批次
// 一次发送"是尝试次数与结果能整批记账的前提，优先于模板字段的逐事件含义。
func (q *Queue) message(ctx context.Context, b store.DeliveryBatch) (Message, error) {
	first := b.Events[0]
	m := Message{Rule: fmt.Sprintf("规则 #%d", first.RuleID), Transition: string(first.Transition), At: first.At}
	rules, err := q.st.ListAlertRules(ctx)
	if err != nil {
		return m, err
	}
	for _, r := range rules {
		if r.ID == first.RuleID {
			m.Rule, m.Kind = r.Name, string(r.Kind)
			break
		}
	}
	listed := b.Events[:min(len(b.Events), maxListedNodes)]
	names := make([]string, 0, len(listed)+1)
	for _, ev := range listed {
		name := fmt.Sprintf("节点 #%d", ev.NodeID)
		node, err := q.st.GetNode(ctx, ev.NodeID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return m, err
		}
		if err == nil {
			name = node.Name
		}
		names = append(names, name)
	}
	if len(b.Events) == 1 {
		m.Node, m.Summary, m.Value = names[0], first.Summary, first.Value
		return m, nil
	}
	verb := "触发告警"
	if first.Transition == store.TransitionRecovered {
		verb = "已恢复"
	}
	lines := []string{fmt.Sprintf("规则 %s：%d 台节点%s", m.Rule, len(b.Events), verb)}
	for _, ev := range listed {
		lines = append(lines, ev.Summary)
	}
	if rest := len(b.Events) - len(listed); rest > 0 {
		more := fmt.Sprintf("另外 %d 台", rest)
		lines, names = append(lines, more), append(names, more)
	}
	m.Node, m.Summary = strings.Join(names, "、"), strings.Join(lines, "\n")
	return m, nil
}

// PruneAlertEvents 与投递并发，且在同一事务删除事件和投递；行消失等价于终态，
// 不是存储故障，不能通过失败退避拖住后续投递。仅归一事件和投递行的不存在错误。
func deliveryRemoved(err error) bool {
	var missing store.NotFoundError
	return errors.As(err, &missing) && (missing.Kind == store.ObjectAlertEvent || missing.Kind == store.ObjectAlertDelivery)
}

// attempt 对批次做至多一次尝试。批次在库里的行是真源，每次都回读：渠道删除会把行置为终态，已读到终态的旧队列项
// 不再发送。重试按批次进行——同批各行共享尝试次数与结果（store.BeginBatchAttempt、store.UpdateBatch），一次尝试
// 发送整批合成的那一条消息，不会把批次拆成多条重发。
func (q *Queue) attempt(ctx context.Context, batch int64) (out outcome, err error) {
	defer func() {
		if deliveryRemoved(err) {
			out, err = outcome{}, nil
		}
	}()
	b, err := q.st.GetDeliveryBatch(ctx, batch)
	if err != nil {
		return out, err
	}
	head := b.Deliveries[0]
	if head.Done {
		return out, nil
	}
	if head.Attempts >= store.MaxDeliveryAttempts {
		// 名额已耗尽而行仍未终态：最后一次尝试已发出，结果没落盘。接收方可能已经收到，
		// 所以不沿用更早一次的失败，也不留下它的原文与状态码。
		return out, q.st.UpdateBatch(ctx, batch, store.DeliveryResult{Done: true, Failure: store.FailureResultUnrecorded})
	}
	var c *store.NotifyChannel
	for _, row := range q.channels() {
		if row.ID == head.ChannelID {
			c = &row
			break
		}
	}
	if c == nil {
		return out, q.st.UpdateBatch(ctx, batch, store.DeliveryResult{Done: true, Failure: store.FailureChannelDeleted})
	}
	channel, err := ParseChannel(*c, q.client, q.telegramBase)
	if err != nil {
		r, _ := Classify(err)
		r.Done = true
		return out, q.st.UpdateBatch(ctx, batch, r)
	}
	// 节奏已满时排队到下一个空位，不消耗尝试名额：超出上限的批次排队而不是丢弃。
	if at, full := q.rateSlot(*c, q.clk.Mono()); full {
		return outcome{retry: true, at: at}, nil
	}
	m, err := q.message(ctx, b)
	if err != nil {
		return out, err
	}
	ds, err := q.st.BeginBatchAttempt(ctx, batch)
	if errors.Is(err, store.ErrDeliveryDone) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	// 尝试已落盘即计入节奏：请求可能已到达接收方，无论结果如何都占用它的额度。
	q.sent[c.ID] = append(q.sent[c.ID], q.clk.Mono())
	sendErr := channel.Send(ctx, m)
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	r := store.DeliveryResult{OK: true, DeliveredAt: q.clk.Now()}
	retry := false
	if sendErr != nil {
		r, retry = Classify(sendErr)
	}
	attempts := ds[0].Attempts
	r.Done = r.OK || !retry || attempts >= store.MaxDeliveryAttempts
	if err := q.st.UpdateBatch(ctx, batch, r); err != nil {
		return out, err
	}
	if r.Done {
		return out, nil
	}
	return outcome{retry: true, at: q.clk.Mono() + q.retryWait(sendErr, attempts)}, nil
}

// rateSlot 在渠道最近 rateWindow 内的请求数已达 RatePerMinute 时给出下一个空位的单调时刻：第 RatePerMinute 近的
// 那次请求滑出窗口之时。顺带丢掉已滑出窗口的记录。RatePerMinute 为 0 表示不限。只由 worker 调用（见 Queue.sent）。
func (q *Queue) rateSlot(c store.NotifyChannel, now time.Duration) (time.Duration, bool) {
	log := q.sent[c.ID]
	i := 0
	for i < len(log) && log[i] <= now-rateWindow {
		i++
	}
	log = log[i:]
	if len(log) == 0 {
		delete(q.sent, c.ID)
	} else {
		q.sent[c.ID] = log
	}
	if c.RatePerMinute <= 0 || len(log) < c.RatePerMinute {
		return 0, false
	}
	return log[len(log)-c.RatePerMinute] + rateWindow, true
}

// retryWait 是第 attempts 次尝试以可重试失败告终后到下一次尝试的间隔：固定退避 backoff[attempts-1]；429 应答带
// Retry-After 时取两者较大者，Retry-After 至多计 MaxRetryAfter。固定退避是下限：Retry-After 为 0 或已过去的日期
// 不让重试比没有它时更密。
func (q *Queue) retryWait(err error, attempts int) time.Duration {
	wait := backoff[attempts-1]
	var f *sendFailure
	if errors.As(err, &f) && f.status == http.StatusTooManyRequests {
		if d, ok := parseRetryAfter(f.retryAfter, q.clk.Now()); ok {
			wait = max(wait, min(d, MaxRetryAfter))
		}
	}
	return wait
}

// parseRetryAfter 解析 Retry-After 的两种写法（RFC 9110 §10.2.3）：非负整数秒，或相对 now 的 HTTP 日期。
// 超过 MaxRetryAfter 的秒数在换算前就截到上限，巨大的数值不会溢出成负的时长。读不懂的值返回 false，按固定退避。
func parseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if strings.Trim(v, "0123456789") == "" {
		secs, err := strconv.ParseInt(v, 10, 64)
		if err != nil || secs > int64(MaxRetryAfter/time.Second) {
			return MaxRetryAfter, true
		}
		return time.Duration(secs) * time.Second, true
	}
	at, err := http.ParseTime(v)
	if err != nil {
		return 0, false
	}
	return at.Sub(now), true
}

// Classify 是渠道失败的唯一分类：队列据它落库并决定是否重试，TestNotifyChannel 据它选错误码。
// 真实渠道每条可达的失败路径都在 notify.go 带上类别，由 notify 的表驱动测试逐条钉住；
// 取不到类别说明某条路径漏了，记为 unclassified 让遗漏可见，而不是标成某个具体类别，且不重试。
func Classify(err error) (store.DeliveryResult, bool) {
	var f *sendFailure
	if !errors.As(err, &f) {
		return store.DeliveryResult{Failure: store.FailureUnclassified, Error: err.Error()}, false
	}
	return store.DeliveryResult{Failure: f.failure, HTTPStatus: f.status, Error: f.detail}, f.retryable()
}

func (q *Queue) SendTest(ctx context.Context, c store.NotifyChannel) error {
	channel, err := ParseChannel(c, q.client, q.telegramBase)
	if err != nil {
		return err
	}
	return channel.Send(ctx, Message{Rule: "测试规则", Node: "测试节点", Kind: "test", Transition: "test", Summary: "这是一条测试通知", At: q.clk.Now()})
}
