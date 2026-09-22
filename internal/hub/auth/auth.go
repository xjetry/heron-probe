// Package auth 持有节点 token 的内存映射与注册窗口的裁决。
//
// mutMu 串行化变更者与 Load，保证写库并等待成功后才持 mu 更新映射；绕开
// mutMu 会让提交顺序与映射更新顺序分叉。mu 只保护 byHash 与 failures，临界区
// 不含 I/O，否则只读内存的 Authenticate 也会排在事务之后。绕开 mu 访问内存
// 会产生数据竞争。写库失败不改映射；提交与更新之间崩溃时，Load 在启动时重建。
// 离线子命令直接改表，未更新运行中进程的映射，因此要求 hub 重启。
package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/store"
)

var ErrDenied = errors.New("registration denied")

const (
	// 同一来源 IP 在窗口开启期间连错 failLimit 次 key，failWindow 内拒绝其注册。
	failLimit  = 5
	failWindow = 15 * time.Minute
)

type Auth struct {
	mutMu    sync.Mutex
	mu       sync.RWMutex
	store    *store.Store
	clk      clock.Clock
	log      *slog.Logger
	byHash   map[[32]byte]int64
	failures map[netip.Addr]*failure
}

type failure struct {
	count int
	since time.Duration
}

func New(st *store.Store, clk clock.Clock, log *slog.Logger) *Auth {
	return &Auth{store: st, clk: clk, log: log, byHash: map[[32]byte]int64{}, failures: map[netip.Addr]*failure{}}
}

func (a *Auth) Load(ctx context.Context) error {
	a.mutMu.Lock()
	defer a.mutMu.Unlock()
	m, err := a.store.TokenHashes(ctx)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.byHash = m
	a.mu.Unlock()
	return nil
}

// Authenticate 只查内存映射，不读库：这是上报路径上唯一的鉴权动作。
func (a *Auth) Authenticate(token string) (int64, bool) {
	h := HashToken(token)
	a.mu.RLock()
	defer a.mu.RUnlock()
	id, ok := a.byHash[h]
	return id, ok
}

func (a *Auth) CreateNode(ctx context.Context, name string) (int64, string, error) {
	a.mutMu.Lock()
	defer a.mutMu.Unlock()
	plain, h := NewToken()
	id, err := a.store.CreateNode(ctx, name, h[:])
	if err != nil {
		return 0, "", err
	}
	a.mu.Lock()
	a.byHash[h] = id
	a.mu.Unlock()
	return id, plain, nil
}

// RotateToken 让旧 hash 立即失效：库写成功后先删旧再加新，中间没有两者都有效的窗口。
func (a *Auth) RotateToken(ctx context.Context, id int64) (string, error) {
	a.mutMu.Lock()
	defer a.mutMu.Unlock()
	plain, h := NewToken()
	if err := a.store.SetTokenHash(ctx, id, h[:]); err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dropLocked(id)
	a.byHash[h] = id
	return plain, nil
}

func (a *Auth) DeleteNode(ctx context.Context, id int64) error {
	a.mutMu.Lock()
	defer a.mutMu.Unlock()
	if err := a.store.DeleteNode(ctx, id); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dropLocked(id)
	return nil
}

func (a *Auth) dropLocked(id int64) {
	for k, v := range a.byHash {
		if v == id {
			delete(a.byHash, k)
		}
	}
}

func (a *Auth) OpenWindow(ctx context.Context, ttl time.Duration, max int) (key string, until time.Time, err error) {
	plain, h := NewToken()
	until = a.clk.Now().Add(ttl)
	if err := a.store.SetRegisterWindow(ctx, h[:], until, max); err != nil {
		return "", time.Time{}, err
	}
	return plain, until, nil
}

func (a *Auth) CloseWindow(ctx context.Context) error { return a.store.ClearRegisterWindow(ctx) }

func (a *Auth) Window(ctx context.Context) (store.Window, bool, error) {
	return a.store.RegisterWindow(ctx)
}

// Register 用窗口 key 换取一个新节点的 token。
//
// 窗口关闭与 key 错误对外都是 ErrDenied；失败计数只在窗口开启且 key 错误时累加：
// 窗口关闭时没有可猜的秘密，计数只会误伤与他人共用出口地址的运维者。
// 计数按来源 IP、独立于任何登录失败计数：批量安装时用了过期 key 是配置失误，
// 不是对面板的攻击。
func (a *Auth) Register(ctx context.Context, key, name string, from netip.Addr) (int64, string, error) {
	a.mutMu.Lock()
	defer a.mutMu.Unlock()
	now := a.clk.Mono()
	a.mu.Lock()
	for ip, f := range a.failures {
		if now-f.since >= failWindow {
			delete(a.failures, ip)
		}
	}
	if f := a.failures[from]; f != nil && f.count >= failLimit {
		a.mu.Unlock()
		return 0, "", ErrDenied
	}
	a.mu.Unlock()
	keyHash := HashToken(key)
	plain, tokHash := NewToken()
	id, err := a.store.RegisterNode(ctx, keyHash[:], name, tokHash[:])
	switch {
	case errors.Is(err, store.ErrBadKey):
		a.mu.Lock()
		f := a.failures[from]
		if f == nil {
			f = &failure{since: now}
			a.failures[from] = f
		}
		f.count++
		count := f.count
		a.mu.Unlock()
		a.log.Warn("register key mismatch", "from", from, "failures", count)
		return 0, "", ErrDenied
	case errors.Is(err, store.ErrNoWindow):
		return 0, "", ErrDenied
	case err != nil:
		return 0, "", err
	}
	a.mu.Lock()
	a.byHash[tokHash] = id
	delete(a.failures, from)
	a.mu.Unlock()
	return id, plain, nil
}
