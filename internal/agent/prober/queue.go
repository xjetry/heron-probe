package prober

import (
	"math"
	"sync"
	"time"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
)

// QueueCap 覆盖最坏情况：64 个任务 × 每 5 秒一次 × 120 秒迟到预算 = 1536 条，留出余量。
const QueueCap = 4096

type Result struct {
	TaskID  uint64
	Outcome Outcome
	At      time.Duration
}

// Queue 满时丢最旧并计数，不等待上报腾出空间；mu 只保护有界内存操作。
type Queue struct {
	mu      sync.Mutex
	items   []Result
	cap     int
	dropped uint64
}

func NewQueue(capacity int) *Queue {
	if capacity < 0 {
		panic("queue capacity must be nonnegative")
	}
	return &Queue{cap: capacity}
}

func (q *Queue) Push(r Result) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(q.items, r)
	q.trim()
}

func (q *Queue) trim() {
	if n := len(q.items) - q.cap; n > 0 {
		q.dropped += uint64(n)
		copy(q.items, q.items[n:])
		clear(q.items[len(q.items)-n:])
		q.items = q.items[:len(q.items)-n]
	}
}

// Take 丢弃超龄结果：hub 会拒收它们，继续保留只会占用上报体积。
func (q *Queue) Take(now, maxAge time.Duration) []Result {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []Result
	for _, r := range q.items {
		if now-r.At > maxAge {
			q.dropped++
		} else {
			out = append(out, r)
		}
	}
	clear(q.items)
	q.items = q.items[:0]
	return out
}

// Requeue 把失败批次放回队头；容量不足仍从最旧的结果开始丢弃。
func (q *Queue) Requeue(rs []Result) {
	q.mu.Lock()
	defer q.mu.Unlock()
	items := make([]Result, 0, len(rs)+len(q.items))
	items = append(items, rs...)
	q.items = append(items, q.items...)
	q.trim()
}

func (q *Queue) Dropped() uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.dropped
}

func ToProto(rs []Result, now time.Duration) []*probev1.ProbeResult {
	out := make([]*probev1.ProbeResult, 0, len(rs))
	for _, r := range rs {
		// 取 now 与取队列不是原子操作；较晚入队的结果不能转成溢出的无符号 age。
		p := &probev1.ProbeResult{TaskId: r.TaskID, AgeMs: uint32(min(max((now-r.At)/time.Millisecond, 0), math.MaxUint32))}
		switch {
		case r.Outcome.Err != "":
			p.Outcome = &probev1.ProbeResult_Error{Error: &probev1.ProbeError{Message: r.Outcome.Err}}
		case r.Outcome.Timeout:
			p.Outcome = &probev1.ProbeResult_Timeout{Timeout: &probev1.Timeout{}}
		default:
			p.Outcome = &probev1.ProbeResult_RttUs{RttUs: r.Outcome.RttUs}
		}
		out = append(out, p)
	}
	return out
}
