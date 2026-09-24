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
}

var _ Sender = (*Queue)(nil)

func NewQueue(st *store.Store, channels func() []store.NotifyChannel, client *http.Client, telegramBase string, clk clock.Clock, sleep func(context.Context, time.Duration) error, log *slog.Logger) *Queue {
	if sleep == nil {
		sleep = sleepContext
	}
	return &Queue{st: st, channels: channels, client: client, telegramBase: telegramBase, clk: clk, sleep: sleep, log: log, items: make(chan deliveryItem, QueueCap)}
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

func (q *Queue) enqueue(item deliveryItem) {
	q.mu.Lock()
	defer q.mu.Unlock()
	select {
	case q.items <- item:
		return
	default:
	}
	// 生产者在 mu 下串行；worker 只取出，所以腾出一格后发送不会等待。
	select {
	case old := <-q.items:
		q.log.Warn("notification queue full; oldest delivery dropped", "delivery_id", old.delivery.ID)
	default:
	}
	q.items <- item
}

// Engine.apply 在 writeMu 下调用 Enqueue；这里只操作有界内存队列，不等数据库或网络。
// 满队列丢弃的项仍在库中保持 done=0，要等下次启动在 Load 后调用 Requeue 才续投。
func (q *Queue) Enqueue(ev store.AlertEvent) {
	ds := ev.Deliveries
	ev.Deliveries = nil
	for _, d := range ds {
		if !d.Done {
			q.enqueue(deliveryItem{d, ev})
		}
	}
}
func (q *Queue) Requeue(ctx context.Context) error {
	ds, err := q.st.PendingDeliveries(ctx)
	if err != nil {
		return err
	}
	for _, d := range ds {
		ev, err := q.st.GetAlertEvent(ctx, d.EventID)
		if err != nil {
			return err
		}
		ev.Deliveries = nil
		q.enqueue(deliveryItem{d, ev})
	}
	return nil
}

// 由装配方启动一个 worker；退出不清空库中待投递行，下一次 Requeue 接续未完成项。
// HTTP 成功与落盘之间崩溃仍可能重复发送，外部 HTTP 与 SQLite 不共享事务。
func (q *Queue) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case item := <-q.items:
			if ctx.Err() != nil {
				return
			}
			if err := q.deliver(ctx, item); err != nil && ctx.Err() == nil {
				q.log.Error("notification delivery failed", "delivery_id", item.delivery.ID, "err", err)
			}
		}
	}
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
	return store.Delivery{}, store.NotFoundError{Kind: "alert delivery", ID: item.delivery.ID}
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

func (q *Queue) deliver(ctx context.Context, item deliveryItem) error {
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
		var c *store.NotifyChannel
		for _, row := range q.channels() {
			if row.ID == d.ChannelID {
				c = &row
				break
			}
		}
		if c == nil {
			return q.st.UpdateDelivery(ctx, d.ID, d.Attempts, false, true, store.DeliveryErrChannelDeleted, time.Time{})
		}
		channel, err := ParseChannel(*c, q.client, q.telegramBase)
		if err != nil {
			return q.st.UpdateDelivery(ctx, d.ID, d.Attempts, false, true, err.Error(), time.Time{})
		}
		err = channel.Send(ctx, m)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		d.Attempts++
		ok := err == nil
		last := ""
		var at time.Time
		retry := false
		if ok {
			at = q.clk.Now()
		} else {
			last = err.Error()
			var classified Retryable
			retry = errors.As(err, &classified) && classified.Retryable()
		}
		done := ok || !retry || d.Attempts >= store.MaxDeliveryAttempts
		if err := q.st.UpdateDelivery(ctx, d.ID, d.Attempts, ok, done, last, at); err != nil {
			return err
		}
		if done {
			return nil
		}
		if err := q.sleep(ctx, backoff[d.Attempts-1]); err != nil {
			return err
		}
	}
}

func (q *Queue) SendTest(ctx context.Context, c store.NotifyChannel) error {
	channel, err := ParseChannel(c, q.client, q.telegramBase)
	if err != nil {
		return err
	}
	return channel.Send(ctx, Message{Rule: "测试规则", Node: "测试节点", Kind: "test", Transition: "test", Summary: "这是一条测试通知", At: q.clk.Now()})
}
