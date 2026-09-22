package auth

import (
	"net/netip"
	"time"
)

// failureTracker 保存滑动窗口中的失败时刻，达到上限后从该次失败起锁满 window。
// 调用方持 Auth.mu，临界区无 I/O；过期地址在访问时回收，每个地址最多保存 limit 次失败。
type failureTracker struct {
	limit  int
	window time.Duration
	m      map[netip.Addr]*failure
}

type failure struct {
	attempts []time.Duration
	until    time.Duration
}

func newFailureTracker(limit int, window time.Duration) *failureTracker {
	return &failureTracker{limit: limit, window: window, m: map[netip.Addr]*failure{}}
}

func (t *failureTracker) sweep(now time.Duration) {
	for ip, f := range t.m {
		if now < f.until {
			continue
		}
		for len(f.attempts) > 0 && now-f.attempts[0] >= t.window {
			f.attempts = f.attempts[1:]
		}
		if len(f.attempts) == 0 {
			delete(t.m, ip)
		}
	}
}

func (t *failureTracker) locked(from netip.Addr, now time.Duration) bool {
	t.sweep(now)
	f := t.m[from]
	return f != nil && now < f.until
}

func (t *failureTracker) record(from netip.Addr, now time.Duration) int {
	t.sweep(now)
	f := t.m[from]
	if f == nil {
		f = &failure{}
		t.m[from] = f
	}
	if now < f.until {
		return len(f.attempts)
	}
	f.attempts = append(f.attempts, now)
	if len(f.attempts) >= t.limit {
		f.until = now + t.window
	}
	return len(f.attempts)
}

func (t *failureTracker) clear(from netip.Addr) { delete(t.m, from) }
