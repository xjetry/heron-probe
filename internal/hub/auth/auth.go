// Package auth 持有节点 token 的内存映射与注册窗口的裁决。
//
// 不变式：byHash 与 node.token_hash 列始终一致。修改顺序固定为：持 mu → 写库并
// 等待成功 → 改映射 → 放锁。写库失败则映射不动；进程在两步之间崩溃则映射在
// 下次启动时经 Load 自库重建。任何绕开 mu 直接改表或改映射的写入都会让两者
// 分叉——probe-hub 的离线子命令直接改表，所以它们要求 hub 重启。
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
	plain, h := NewToken()
	a.mu.Lock()
	defer a.mu.Unlock()
	id, err := a.store.CreateNode(ctx, name, h[:])
	if err != nil {
		return 0, "", err
	}
	a.byHash[h] = id
	return id, plain, nil
}

// RotateToken 让旧 hash 立即失效：库写成功后先删旧再加新，中间没有两者都有效的窗口。
func (a *Auth) RotateToken(ctx context.Context, id int64) (string, error) {
	plain, h := NewToken()
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.store.SetTokenHash(ctx, id, h[:]); err != nil {
		return "", err
	}
	a.dropLocked(id)
	a.byHash[h] = id
	return plain, nil
}

func (a *Auth) DeleteNode(ctx context.Context, id int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.store.DeleteNode(ctx, id); err != nil {
		return err
	}
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

func (a *Auth) OpenWindow(ctx context.Context, ttl time.Duration, max int) (string, error) {
	plain, h := NewToken()
	if err := a.store.SetRegisterWindow(ctx, h[:], a.clk.Now().Add(ttl), max); err != nil {
		return "", err
	}
	return plain, nil
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
	now := a.clk.Mono()
	a.mu.Lock()
	defer a.mu.Unlock()
	if f := a.failures[from]; f != nil {
		if now-f.since >= failWindow {
			delete(a.failures, from)
		} else if f.count >= failLimit {
			return 0, "", ErrDenied
		}
	}
	keyHash := HashToken(key)
	plain, tokHash := NewToken()
	id, err := a.store.RegisterNode(ctx, keyHash[:], name, tokHash[:])
	switch {
	case errors.Is(err, store.ErrBadKey):
		f := a.failures[from]
		if f == nil {
			f = &failure{since: now}
			a.failures[from] = f
		}
		f.count++
		a.log.Warn("register key mismatch", "from", from, "failures", f.count)
		return 0, "", ErrDenied
	case errors.Is(err, store.ErrNoWindow):
		return 0, "", ErrDenied
	case err != nil:
		return 0, "", err
	}
	a.byHash[tokHash] = id
	delete(a.failures, from)
	return id, plain, nil
}
