package ingest

import (
	"sync"
	"time"
)

// burst 是令牌桶容量：允许上报间隔的抖动与一次立即重试，再多就是异常。
const burst = 3

// buckets 用单调钟按键限速，mu 保护桶状态与回收进度。
// 调用方为每个实例提供固定的正容量和补充周期；空闲足够久的桶必已补满，
// 与新桶不可区分，因此可以回收。按补满周期最多扫描一次，避免每次请求遍历全部键。
type buckets[K comparable] struct {
	mu        sync.Mutex
	m         map[K]*tokenBucket
	capacity  float64
	lastSweep time.Duration
}

type tokenBucket struct {
	tokens float64
	last   time.Duration
}

func newBuckets[K comparable](capacity float64) *buckets[K] {
	return &buckets[K]{m: map[K]*tokenBucket{}, capacity: capacity}
}

func (l *buckets[K]) allow(key K, now, refillPer time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	fullAfter := time.Duration(l.capacity * float64(refillPer))
	if now-l.lastSweep >= fullAfter {
		for k, b := range l.m {
			if now-b.last >= fullAfter {
				delete(l.m, k)
			}
		}
		l.lastSweep = now
	}
	b := l.m[key]
	if b == nil {
		b = &tokenBucket{tokens: l.capacity, last: now}
		l.m[key] = b
	}
	b.tokens = min(l.capacity, b.tokens+float64(now-b.last)/float64(refillPer))
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
