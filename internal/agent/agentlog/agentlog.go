// Package agentlog 给 agent 的日志设总量边界（§5.7）。
//
// agent 的不少日志行由 hub 的应答触发（上报失败、被拒的任务、越界的间隔），错误文本里还带着 hub 给的字符串。
// hub 失守时它能决定这些行多久出现一次、有多长；OpenRC 与 launchd 把 stderr 写进不轮转的普通文件。
// 所以边界放在出口：每一行都经过这里，不依赖每个调用点各自节制。
package agentlog

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const (
	// Burst 与 RefillEvery 是输出速率的令牌桶：启动时的几行与短时的故障都在突发额度内，持续输出至多每 RefillEvery 一行。
	// 被压掉的行不丢计数：下一次放行时先输出一行汇总。汇总与被放行的那一行共用一个令牌，所以每个令牌至多两行。
	Burst       = 20
	RefillEvery = 30 * time.Second
	// MaxValueLen 是消息与每个字符串值（含 error 与其他经 fmt 格式化的值）的字节上限。
	MaxValueLen = 256
)

type bucket struct {
	mu         sync.Mutex
	now        func() time.Time
	tokens     float64
	last       time.Time
	suppressed int
}

// Handler 包一层 slog.Handler。WithAttrs、WithGroup 派生的 Handler 共用同一个桶：边界是整个进程的，不是每个 logger 各一份。
type Handler struct {
	inner slog.Handler
	b     *bucket
}

// New 以 now 为时钟（nil 取 time.Now）。
func New(inner slog.Handler, now func() time.Time) *Handler {
	if now == nil {
		now = time.Now
	}
	return &Handler{inner: inner, b: &bucket{now: now, tokens: Burst, last: now()}}
}

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool { return h.inner.Enabled(ctx, l) }

// take 取一个令牌；取到时返回自上次放行以来被压掉的行数。
func (b *bucket) take() (ok bool, suppressed int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens = min(Burst, b.tokens+float64(elapsed)/float64(RefillEvery))
		b.last = now
	}
	if b.tokens < 1 {
		b.suppressed++
		return false, 0
	}
	b.tokens--
	suppressed, b.suppressed = b.suppressed, 0
	return true, suppressed
}

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	ok, suppressed := h.b.take()
	if !ok {
		return nil
	}
	if suppressed > 0 {
		s := slog.NewRecord(r.Time, slog.LevelWarn, "log lines suppressed by the output rate limit", 0)
		s.AddAttrs(slog.Int("count", suppressed))
		if err := h.inner.Handle(ctx, s); err != nil {
			return err
		}
	}
	out := slog.NewRecord(r.Time, r.Level, truncate(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(bound(a))
		return true
	})
	return h.inner.Handle(ctx, out)
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	bounded := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		bounded[i] = bound(a)
	}
	return &Handler{inner: h.inner.WithAttrs(bounded), b: h.b}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{inner: h.inner.WithGroup(truncate(name)), b: h.b}
}

// bound 把属性里的字符串限长。数值、时长、时刻本身有界，原样保留；其余类型（error、Stringer、任意值）先格式化再限长，
// 否则一个带着 hub 应答全文的 error 会绕过字符串的上限。
func bound(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(truncate(a.Key), truncate(v.String()))
	case slog.KindGroup:
		group := v.Group()
		out := make([]any, len(group))
		for i, g := range group {
			out[i] = bound(g)
		}
		return slog.Group(truncate(a.Key), out...)
	case slog.KindAny:
		return slog.String(truncate(a.Key), truncate(fmt.Sprint(v.Any())))
	}
	return slog.Attr{Key: truncate(a.Key), Value: v}
}

func truncate(s string) string {
	if len(s) <= MaxValueLen {
		return s
	}
	// 截断点不落在多字节字符中间，截出的仍是合法 UTF-8。
	end := MaxValueLen
	for end > 0 && s[end]&0xC0 == 0x80 {
		end--
	}
	return s[:end] + "…(truncated)"
}
