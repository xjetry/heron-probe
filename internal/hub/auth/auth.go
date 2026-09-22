// Package auth 持有节点 token 的内存映射，并裁决注册、管理员密码和会话。
//
// Load 完成且没有外部进程直接改表时，byHash 与 node.token_hash 在每次变更
// 完成后一致。mutMu 保证变更者彼此不交错，Load 也不与变更交错；先写库并
// 等待成功、后改映射则由每个变更者内部的语句顺序保证，写库失败不改映射。
// 绕开 mutMu 会让提交与映射更新顺序分叉；仅持有它不能代替上述语句顺序。
//
// mu 只保护 byHash 与两个失败计数器，临界区不含 I/O。CreateNode、Register、
// DeleteNode 与 RotateToken 在 store 返回成功后、取得 mu.Lock 前存在可见
// 间隙：Authenticate 可能仍接受已删除或轮换的旧 token，或尚不认识新 token。
// 这个间隙跨越一次 mu.Lock 的获取，包含调度与锁竞争等待，并无固定时长上界；
// 这是让 Authenticate 不等待任何事务的代价。映射更新期间由 mu 排除并发读取，
// 绕开 mu 访问内存会产生数据竞争；崩溃若落在提交与更新之间，启动时由 Load 重建。
//
// 运行中的 hub 不调用 DeleteNode 或 RotateToken；这些离线子命令在另一进程
// 直接改表，不会更新运行中进程的映射，因此要求 hub 重启。
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
	// 注册与登录分开计数，避免安装时用错 key 把同一出口的管理员锁在登录页外。
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
	register *failureTracker
	login    *failureTracker
}

func New(st *store.Store, clk clock.Clock, log *slog.Logger) *Auth {
	return &Auth{store: st, clk: clk, log: log, byHash: map[[32]byte]int64{}, register: newFailureTracker(failLimit, failWindow), login: newFailureTracker(failLimit, failWindow)}
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

// RotateToken 返回前在 mu 下以新 hash 替换旧 hash，读者不会观察到两者同时有效。
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

func (a *Auth) OpenWindow(ctx context.Context, ttl time.Duration, maxNodes int) (key string, until time.Time, err error) {
	plain, h := NewToken()
	until = a.clk.Now().Add(ttl)
	if err := a.store.SetRegisterWindow(ctx, h[:], until, maxNodes); err != nil {
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
	locked := a.register.locked(from, now)
	a.mu.Unlock()
	if locked {
		return 0, "", ErrDenied
	}
	keyHash := HashToken(key)
	plain, tokHash := NewToken()
	id, err := a.store.RegisterNode(ctx, keyHash[:], name, tokHash[:])
	switch {
	case errors.Is(err, store.ErrBadKey):
		a.mu.Lock()
		count := a.register.record(from, a.clk.Mono())
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
	a.register.clear(from)
	a.mu.Unlock()
	return id, plain, nil
}
