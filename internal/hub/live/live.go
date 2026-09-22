// Package live 持有每个节点的实时状态：最新指标、last_seen 与未落盘的分钟桶。
//
// 不变式：在线 ⇔ now − last_seen < TTL，且这是在线的唯一来源——不存在第二张
// 在线表，也没有连接状态可以与它分叉。last_seen 用单调钟，墙钟回拨不影响。
package live

import (
	"sync"
	"time"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/metric"
)

type Live struct {
	mu    sync.Mutex
	clk   clock.Clock
	ttl   time.Duration
	nodes map[int64]*entry
}

type entry struct {
	metrics      *probev1.Metrics
	lastSeen     time.Duration
	lastSeenWall time.Time
	// buckets 按桶起始（墙钟 Unix 秒，60 对齐）索引。同一节点可以同时有多个
	// 未刷出的桶：墙钟回拨时新样本会落进更早的分钟，它们各自独立、刷出后
	// 由写库时的加法合并并入已有的行。
	buckets map[int64]*metric.Bucket
}

type Entry struct {
	Metrics      *probev1.Metrics
	LastSeen     time.Duration
	LastSeenWall time.Time
	Online       bool
}

func New(clk clock.Clock, ttl time.Duration) *Live {
	return &Live{clk: clk, ttl: ttl, nodes: map[int64]*entry{}}
}

func minuteOf(t time.Time) int64 {
	s := t.Unix()
	return s - s%60
}

// Observe 记录一次已通过校验的上报。调用方保证 m 不再被修改。
func (l *Live) Observe(nodeID int64, m *probev1.Metrics) {
	now, wall := l.clk.Mono(), l.clk.Now()
	ts := minuteOf(wall)
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.nodes[nodeID]
	if e == nil {
		e = &entry{buckets: map[int64]*metric.Bucket{}}
		l.nodes[nodeID] = e
	}
	e.metrics, e.lastSeen, e.lastSeenWall = m, now, wall
	b := e.buckets[ts]
	if b == nil {
		b = metric.NewBucket()
		e.buckets[ts] = b
	}
	b.Add(m)
}

func (l *Live) online(e *entry, now time.Duration) bool {
	return now-e.lastSeen < l.ttl
}

func (l *Live) Online(nodeID int64) bool {
	now := l.clk.Mono()
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.nodes[nodeID]
	return ok && l.online(e, now)
}

func (l *Live) Get(nodeID int64) (Entry, bool) {
	now := l.clk.Mono()
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.nodes[nodeID]
	if !ok {
		return Entry{}, false
	}
	return Entry{Metrics: e.metrics, LastSeen: e.lastSeen, LastSeenWall: e.lastSeenWall, Online: l.online(e, now)}, true
}

// Flush 取走所有已闭合的桶：起始早于当前分钟的。取走即从 live 删除——每个
// 桶至多被交给写协程一次，写库的加法合并才不会重复计入。
func (l *Live) Flush() []metric.Row {
	return l.take(minuteOf(l.clk.Now()))
}

// Drain 取走全部桶，包括当前分钟仍开着的；退出时用。
func (l *Live) Drain() []metric.Row {
	return l.take(1<<62 - 1)
}

func (l *Live) take(before int64) []metric.Row {
	l.mu.Lock()
	defer l.mu.Unlock()
	var rows []metric.Row
	for id, e := range l.nodes {
		for ts, b := range e.buckets {
			if ts >= before {
				continue
			}
			rows = append(rows, metric.Row{NodeID: id, TS: ts, Bucket: b, LastSeen: e.lastSeenWall})
			delete(e.buckets, ts)
		}
	}
	return rows
}

// Forget 删除节点时调用；未刷出的桶随之丢弃，被删节点的历史无处可挂。
func (l *Live) Forget(nodeID int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.nodes, nodeID)
}
