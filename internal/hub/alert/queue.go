package alert

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/store"
)

const QueueCap = 256

var backoff = [...]time.Duration{time.Second, 4 * time.Second}

type deliveryItem struct {
	delivery store.Delivery
	event    store.AlertEvent
}

type Queue struct {
	st           *store.Store
	channels     func() []store.NotifyChannel
	client       *http.Client
	telegramBase string
	clk          clock.Clock
	sleep        func(context.Context, time.Duration) error
	log          *slog.Logger
	mu           sync.Mutex
	items        chan deliveryItem
	active       map[int64]struct{}
	overflow     bool // 库中可能仍有窗口外的待投递行，包括非终态错误出窗的项。
}

var _ Sender = (*Queue)(nil)

func NewQueue(st *store.Store, channels func() []store.NotifyChannel, client *http.Client, telegramBase string, clk clock.Clock, sleep func(context.Context, time.Duration) error, log *slog.Logger) *Queue {
	if sleep == nil {
		sleep = sleepContext
	}
	return &Queue{st: st, channels: channels, client: client, telegramBase: telegramBase, clk: clk, sleep: sleep, log: log, items: make(chan deliveryItem, QueueCap), active: make(map[int64]struct{})}
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

// mu 同时保护通道写入和 queued/in-flight id；去重让有界窗口只装不同投递，避免重复项挤占窗口。
// worker 的 done 回读另保证已终态的旧项不再发送，包括并发 Requeue 读到的旧快照。
func (q *Queue) enqueue(item deliveryItem, evict bool) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, exists := q.active[item.delivery.ID]; exists {
		return true
	}
	select {
	case q.items <- item:
		q.active[item.delivery.ID] = struct{}{}
		return true
	default:
	}
	q.overflow = true
	if !evict {
		return false
	}
	// 生产者在 mu 下串行；worker 只取出，所以腾出一格后发送不会等待。
	select {
	case old := <-q.items:
		delete(q.active, old.delivery.ID)
		q.log.Warn("notification queue full; oldest delivery dropped", "delivery_id", old.delivery.ID)
	default:
	}
	q.items <- item
	q.active[item.delivery.ID] = struct{}{}
	return true
}

// Engine.apply 在 writeMu 下调用 Enqueue；这里只操作有界内存队列，不等数据库或网络。
// 满队列丢弃的项仍在库中保持 done=0；overflow 让 worker 在窗口取空后补货，在本进程内续投。
func (q *Queue) Enqueue(ev store.AlertEvent) {
	ds := ev.Deliveries
	ev.Deliveries = nil
	for _, d := range ds {
		if !d.Done {
			q.enqueue(deliveryItem{d, ev}, true)
		}
	}
}

// 库中 done=0 的行是真源；按 id 升序装填窗口，未装下的项由 worker 取空后继续补货。
func (q *Queue) Requeue(ctx context.Context) error {
	ds, err := q.st.PendingDeliveries(ctx)
	if err != nil {
		return err
	}
	for _, d := range ds {
		ev, err := q.st.GetAlertEvent(ctx, d.EventID)
		if deliveryRemoved(err) {
			continue
		}
		if err != nil {
			return err
		}
		ev.Deliveries = nil
		if !q.enqueue(deliveryItem{d, ev}, false) {
			break
		}
	}
	return nil
}

// 由装配方启动一个 worker；退出不清空库中待投递行，下一次 Requeue 接续未完成项。
// 每次发送前已持久化尝试计数，发送次数受 MaxDeliveryAttempts 约束。
// 结果未落盘可能来自崩溃、写失败，或关停取消时请求已到对端；后续重发都消耗尝试名额。
func (q *Queue) Run(ctx context.Context) {
	var retryDelay time.Duration
	for {
		if ctx.Err() != nil {
			return
		}
		q.mu.Lock()
		refill := len(q.items) == 0 && q.overflow
		if refill {
			q.overflow = false
		}
		q.mu.Unlock()
		if refill {
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
			continue
		}
		select {
		case <-ctx.Done():
			return
		case item := <-q.items:
			err := q.deliver(ctx, item)
			q.mu.Lock()
			delete(q.active, item.delivery.ID)
			q.mu.Unlock()
			if err != nil && ctx.Err() == nil {
				q.log.Error("notification delivery failed", "delivery_id", item.delivery.ID, "err", err)
				if err := q.retryAfterFailure(ctx, &retryDelay); err != nil {
					return
				}
			} else if err == nil {
				retryDelay = 0
			}
		}
	}
}

// 非终态出窗与补货读失败都保留续投信号；故障期间不能靠窗口大小决定是否重试。
// 单条投递的发送上限由 BeginDeliveryAttempt 保证；退避避免存储故障期间空转读库和高频错误日志。
// 同一个 worker 共用退避，deliver 返回 nil（已终态或已不存在）时复位。
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

func (q *Queue) currentDelivery(ctx context.Context, item deliveryItem) (store.Delivery, error) {
	ev, err := q.st.GetAlertEvent(ctx, item.event.ID)
	if err != nil {
		return store.Delivery{}, err
	}
	for _, d := range ev.Deliveries {
		if d.ID == item.delivery.ID {
			return d, nil
		}
	}
	return store.Delivery{}, store.NotFoundError{Kind: store.ObjectAlertDelivery, ID: item.delivery.ID}
}

func (q *Queue) message(ctx context.Context, ev store.AlertEvent) (Message, error) {
	m := Message{Rule: fmt.Sprintf("规则 #%d", ev.RuleID), Node: fmt.Sprintf("节点 #%d", ev.NodeID), Transition: string(ev.Transition), Summary: ev.Summary, Value: ev.Value, At: ev.At}
	rules, err := q.st.ListAlertRules(ctx)
	if err != nil {
		return m, err
	}
	for _, r := range rules {
		if r.ID == ev.RuleID {
			m.Rule, m.Kind = r.Name, string(r.Kind)
			break
		}
	}
	node, err := q.st.GetNode(ctx, ev.NodeID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return m, err
	}
	if err == nil {
		m.Node = node.Name
	}
	return m, nil
}

// PruneAlertEvents 与投递并发，且在同一事务删除事件和投递；行消失等价于终态，
// 不是存储故障，不能通过失败退避拖住后续投递。仅归一事件和投递行的不存在错误。
func deliveryRemoved(err error) bool {
	var missing store.NotFoundError
	return errors.As(err, &missing) && (missing.Kind == store.ObjectAlertEvent || missing.Kind == store.ObjectAlertDelivery)
}

func (q *Queue) deliver(ctx context.Context, item deliveryItem) (err error) {
	defer func() {
		if deliveryRemoved(err) {
			err = nil
		}
	}()
	m, err := q.message(ctx, item.event)
	if err != nil {
		return err
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// 渠道删除会把库中投递置为终态；每次尝试前回读，已读到终态的旧队列项不再发送。
		d, err := q.currentDelivery(ctx, item)
		if err != nil {
			return err
		}
		if d.Done {
			return nil
		}
		if d.Attempts >= store.MaxDeliveryAttempts {
			// 名额已耗尽而行仍未终态：最后一次尝试的结果没落盘。已记下的更早失败原样留作终态；
			// 一次失败都没记下才是 result_unrecorded。
			r := store.DeliveryResult{Done: true, Failure: d.Failure, HTTPStatus: d.HTTPStatus, Error: d.LastError}
			if r.Failure == store.FailureNone {
				r = store.DeliveryResult{Done: true, Failure: store.FailureResultUnrecorded}
			}
			return q.st.UpdateDelivery(ctx, d.ID, r)
		}
		var c *store.NotifyChannel
		for _, row := range q.channels() {
			if row.ID == d.ChannelID {
				c = &row
				break
			}
		}
		if c == nil {
			return q.st.UpdateDelivery(ctx, d.ID, store.DeliveryResult{Done: true, Failure: store.FailureChannelDeleted})
		}
		channel, err := ParseChannel(*c, q.client, q.telegramBase)
		if err != nil {
			r, _ := failureResult(err)
			r.Done = true
			return q.st.UpdateDelivery(ctx, d.ID, r)
		}
		d, err = q.st.BeginDeliveryAttempt(ctx, d.ID)
		if errors.Is(err, store.ErrDeliveryDone) {
			return nil
		}
		if err != nil {
			return err
		}
		err = channel.Send(ctx, m)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r := store.DeliveryResult{OK: true, DeliveredAt: q.clk.Now()}
		retry := false
		if err != nil {
			r, retry = failureResult(err)
		}
		r.Done = r.OK || !retry || d.Attempts >= store.MaxDeliveryAttempts
		if err := q.st.UpdateDelivery(ctx, d.ID, r); err != nil {
			return err
		}
		if r.Done {
			return nil
		}
		if err := q.sleep(ctx, backoff[d.Attempts-1]); err != nil {
			return err
		}
	}
}

// 真实渠道的每条失败路径都在 notify.go 带上类别，由 notify 的表驱动测试逐条钉住；
// 取不到类别说明某条路径漏了，记为 unclassified 让遗漏可见，而不是标成某个具体类别。
// 它不可重试，与"不是 Retryable 就不重试"同向。
func failureResult(err error) (store.DeliveryResult, bool) {
	var f *sendFailure
	if !errors.As(err, &f) {
		return store.DeliveryResult{Failure: store.FailureUnclassified, Error: err.Error()}, false
	}
	return store.DeliveryResult{Failure: f.failure, HTTPStatus: f.status, Error: f.detail}, f.Retryable()
}

func (q *Queue) SendTest(ctx context.Context, c store.NotifyChannel) error {
	channel, err := ParseChannel(c, q.client, q.telegramBase)
	if err != nil {
		return err
	}
	return channel.Send(ctx, Message{Rule: "测试规则", Node: "测试节点", Kind: "test", Transition: "test", Summary: "这是一条测试通知", At: q.clk.Now()})
}
