// Package auth 持有节点 token 的内存映射，并裁决注册、管理员密码和会话。
//
// Load 完成且没有外部进程直接改表时，byHash 与 node.token_hash 在每次变更
// 完成后一致。mutMu 保证变更者彼此不交错，Load 也不与变更交错；先写库并
// 等待成功、后改映射则由每个变更者内部的语句顺序保证，写库失败不改映射。
// 绕开 mutMu 会让提交与映射更新顺序分叉；仅持有它不能代替上述语句顺序。
//
// loginGate 只准入一个密码或匿名 Passkey 校验，争用者不排队；mu 保护锁定检查与 TryLock 的
// 同一次裁决，密码错误或 Passkey 验证失败在放门前记账；密码正确后的第二因素仍须另外消费。
// 登录不占用 mutMu，密码校验和会话写入不让节点变更与 Load 等待。
// 持 mu 时只 TryLock 门、不等待门；持门时可以取 mu 记账，因此不会形成等待环。
//
// mu 保护 byHash 与两个失败计数器，临界区不含 I/O；通知发送者在构造时给定、之后只读，不在 mu 之下。CreateNode、Register、
// DeleteNode 与 RotateToken 在 store 返回成功后、取得 mu.Lock 前存在可见
// 间隙：Authenticate 可能仍接受已删除或轮换的旧 token，或尚不认识新 token。
// 这个间隙跨越一次 mu.Lock 的获取，包含调度与锁竞争等待，并无固定时长上界；
// 这是让 Authenticate 不等待任何事务的代价。映射更新期间由 mu 排除并发读取，
// 绕开 mu 访问内存会产生数据竞争；崩溃若落在提交与更新之间，启动时由 Load 重建。
//
// 管理服务的节点变更必须经当前进程的 Auth 更新映射；另一进程执行节点离线子命令
// 只会改表，无法通知这里的映射，因此仍要求 hub 重启。
//
// 建节点（CreateNode、Register）不直接写 store，而经 NodeCreator：新节点会继承全部 all_nodes 探测任务，
// 落库与探测任务缓存的发布必须由任务注册表串行化（见 probe 包）。锁序 mutMu → 注册表的写锁。
package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/xjetry/heron-probe/internal/clock"
	"github.com/xjetry/heron-probe/internal/hub/store"
)

var ErrDenied = errors.New("registration denied")

const (
	// 注册与登录分开计数，避免安装时用错 key 把同一出口的管理员锁在登录页外。
	failLimit  = 5
	failWindow = 15 * time.Minute
)

// NodeCreator 是建节点的落库入口，实现是 probe.Registry。两个方法返回新节点的 id；建节点失败时节点不存在。
// CreateNode 的 billing 由 api 按 §9.4 校验过，这里不判取值，原样落库。
type NodeCreator interface {
	CreateNode(ctx context.Context, name string, billing store.Billing, tokenHash []byte) (int64, error)
	RegisterNode(ctx context.Context, keyHash []byte, name string, tokenHash []byte) (int64, error)
}

type Auth struct {
	mutMu     sync.Mutex
	loginGate sync.Mutex
	mu        sync.RWMutex
	store     *store.Store
	nodes     NodeCreator
	clk       clock.Clock
	log       *slog.Logger
	byHash    map[[32]byte]int64
	register  *failureTracker
	login     *failureTracker
	// loginSender 与 loc 构造后只读。loc 是 hub 的 --timezone，登录通知的摘要按它写事件时刻。
	loginSender LoginSender
	loc         *time.Location
	security    securityRuntime
}

// LoginSender 把已落库的登录通知交给投递队列（serve 里是 alert.Queue）。
type LoginSender interface{ Enqueue(store.AlertEvent) }

// New 要求 loc 非 nil：通知文案里的时刻按 hub 的 --timezone 写，与面板、流量周期、到期日同一个时区；
// 缺省成某个固定时区，通知与面板的时刻对不上，也不会有任何报错。
//
// sender 为 nil 时登录通知只写库、不入队：投递队列只在启动时的 Requeue 与溢出后的补货里从库取行，已写库的通知要
// 等 hub 下次重启才发出，而且没有任何报错。所以只有不经过 Login 的调用方（离线子命令）可以传 nil；serve 必须先建
// 投递队列再建 Auth（TestServeDeliversLoginNotification 从 serve 入口核对）。
func New(st *store.Store, nodes NodeCreator, sender LoginSender, clk clock.Clock, loc *time.Location, log *slog.Logger) *Auth {
	if loc == nil {
		panic("auth.New: loc must be set")
	}
	return &Auth{store: st, nodes: nodes, loginSender: sender, clk: clk, loc: loc, log: log, byHash: map[[32]byte]int64{}, register: newFailureTracker(failLimit, failWindow), login: newFailureTracker(failLimit, failWindow)}
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
	if !isTokenShaped(token) {
		return 0, false
	}
	return a.lookupToken(token)
}

// lookupToken 不裁决用途，只查完整凭据哈希；调用方必须先限定其允许的凭据形状。
func (a *Auth) lookupToken(token string) (int64, bool) {
	h := HashToken(token)
	a.mu.RLock()
	defer a.mu.RUnlock()
	id, ok := a.byHash[h]
	return id, ok
}

func (a *Auth) CreateNode(ctx context.Context, name string, billing store.Billing) (int64, string, error) {
	a.mutMu.Lock()
	defer a.mutMu.Unlock()
	plain, h := newInstallToken()
	id, err := a.nodes.CreateNode(ctx, name, billing, h[:])
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
	plain, h := newInstallToken()
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

// Register 用窗口 key 创建节点，或用管理员签发的安装凭据认领既有节点；两者都返回运行 token。
//
// 窗口关闭与 key 错误对外都是 ErrDenied；失败计数只在窗口开启且 key 错误时累加：
// 窗口关闭时没有可猜的秘密，计数只会误伤与他人共用出口地址的运维者。
// 计数按来源键（SourceKey：IPv4 按地址、IPv6 按 /64）、独立于任何登录失败计数：批量安装时用了过期 key 是配置失误，
// 不是对面板的攻击。from 可以是具体地址，也可以是已归一化的来源键（ingest 传的是 ratelimit.SourceOf 的键），
// failureTracker 与日志都先经 SourceKey，两种传法结果相同；日志的 source 按 DescribeSource 写，IPv6 带 /64，
// 不会被读成一个具体地址（/64 的网络地址本身也是合法地址）。
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
	// 只有独立用途的安装凭据能认领；完整前缀参与哈希，不能拿运行 token 自报安装用途。
	// mutMu 串行化认领和管理员换发，成功后旧凭据从库与映射移除，同一凭据只能消费一次。
	if random, installation := strings.CutPrefix(key, installTokenPrefix); installation {
		if !isTokenShaped(random) {
			return 0, "", ErrDenied
		}
		id, ok := a.lookupToken(key)
		if !ok {
			return 0, "", ErrDenied
		}
		plain, h := NewToken()
		if err := a.store.SetTokenHash(ctx, id, h[:]); err != nil {
			return 0, "", err
		}
		a.mu.Lock()
		a.dropLocked(id)
		a.byHash[h] = id
		a.register.clear(from)
		a.mu.Unlock()
		return id, plain, nil
	}
	keyHash := HashToken(key)
	plain, tokHash := NewToken()
	id, err := a.nodes.RegisterNode(ctx, keyHash[:], name, tokHash[:])
	switch {
	case errors.Is(err, store.ErrBadKey):
		a.mu.Lock()
		count, _ := a.register.record(from, a.clk.Mono())
		a.mu.Unlock()
		a.log.Warn("register key mismatch", "source", DescribeSource(SourceKey(from)), "failures", count)
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
