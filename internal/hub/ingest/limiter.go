package ingest

import (
	"sync"
	"time"
)

// burst 是令牌桶容量：允许上报间隔的抖动与一次立即重试，再多就是异常。
const burst = 3

// limiter 按节点限速：补充速率是下发间隔对应速率的 2 倍。用单调钟计时。
type limiter struct {
	mu    sync.Mutex
	nodes map[int64]*tokenBucket
}

type tokenBucket struct {
	tokens float64
	last   time.Duration
}

func newLimiter() *limiter { return &limiter{nodes: map[int64]*tokenBucket{}} }

func (l *limiter) allow(id int64, now, interval time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.nodes[id]
	if b == nil {
		b = &tokenBucket{tokens: burst, last: now}
		l.nodes[id] = b
	}
	b.tokens = min(burst, b.tokens+float64(now-b.last)/float64(interval)*2)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
