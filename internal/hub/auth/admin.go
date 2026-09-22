package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"

	"github.com/xjetry/probe/internal/hub/store"
)

var (
	ErrNoAdmin      = errors.New("no admin password has been set")
	ErrBadPassword  = errors.New("wrong password")
	ErrLocked       = errors.New("too many failed logins from this address")
	ErrWeakPassword = fmt.Errorf("password must be at least %d characters", MinPasswordLen)
)

const (
	MinPasswordLen = 12
	// 面板是长期打开的运维页面：24 小时无操作视为离开；30 天是无论如何都要
	// 重新证明身份的上限。
	SessionAbsolute = 30 * 24 * time.Hour
	SessionIdle     = 24 * time.Hour
	// 只有距已落库的最近使用时刻满一分钟才异步刷新，避免为每次轮询写库。
	// 并发请求可能在刷新落库前重复投递，不能把此阈值理解为严格的写频率上限。
	touchEvery = time.Minute

	argonTime    uint32 = 3
	argonMemory  uint32 = 64 * 1024
	argonThreads uint8  = 4
	argonKeyLen  uint32 = 32
)

// HashPassword 输出 PHC 字符串。成本参数随哈希保存，校验预算内的旧哈希按
// 自带参数重算，下次改密码时自然换成新参数。
func HashPassword(plain string) (string, error) {
	var salt [16]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(plain), salt[:], argonTime, argonMemory, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		enc.EncodeToString(salt[:]), enc.EncodeToString(key)), nil
}

// VerifyPassword 按字符串自带的参数重算并常量时间比较。格式错误是存储损坏，
// 作为错误返回而不是当作"密码不对"。
func VerifyPassword(phc, plain string) (bool, error) {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errors.New("stored password hash is not an argon2id PHC string")
	}
	if parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return false, fmt.Errorf("stored password hash has unsupported argon2 version %q", parts[2])
	}
	var m, t uint32
	var p uint8
	if n, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || n != 3 || parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", m, t, p) {
		return false, fmt.Errorf("stored password hash has malformed parameters %q", parts[3])
	}
	// 自带参数允许旧哈希继续校验，但损坏的存储不能触发 panic 或无界内存、CPU 消耗。
	// 校验预算独立于新哈希的默认成本：最多 256 MiB、10 轮、16 路并行。
	if t < 1 || t > 10 || p < 1 || p > 16 || m < 8*uint32(p) || m > 256*1024 {
		return false, fmt.Errorf("stored password hash parameters out of range: require 1<=t<=10, 1<=p<=16, 8*p<=m<=262144 KiB; got %q", parts[3])
	}
	enc := base64.RawStdEncoding.Strict()
	salt, err := enc.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("stored password hash has malformed salt: %w", err)
	}
	want, err := enc.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("stored password hash has malformed digest: %w", err)
	}
	if len(salt) != 16 || len(want) != int(argonKeyLen) {
		return false, errors.New("stored password hash requires a 16-byte salt and a 32-byte digest")
	}
	got := argon2.IDKey([]byte(plain), salt, t, m, p, uint32(len(want)))
	// 比较耗时不应泄露摘要匹配的位置，即使 Argon2 的耗时远大于比较本身。
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// SetPassword 只由 hub 主机上的 probe-hub passwd 调用：没有经网络的首次设置页，
// 也就没有"谁先访问谁占有"的窗口。写库的同一事务清空全部会话（store 保证）。
func (a *Auth) SetPassword(ctx context.Context, plain string) error {
	if utf8.RuneCountInString(plain) < MinPasswordLen {
		return ErrWeakPassword
	}
	phc, err := HashPassword(plain)
	if err != nil {
		return err
	}
	return a.store.SetAdminPassword(ctx, phc)
}

// Login 用密码换会话 token，明文只返回这一次。
//
// admin 表为空时一律失败：空表的语义是"无人可登录"而不是"无需认证"，由这里的
// 显式检查承载，并在日志里指明该跑 probe-hub passwd。失败按来源 IP 计数，
// 锁定期间的拒绝不依赖输入的密码，正确密码也不能提前解除锁定。
func (a *Auth) Login(ctx context.Context, password string, from netip.Addr) (string, error) {
	// 裁决与记失败必须串行，否则并发请求都能在第五次失败落账前通过检查。
	// 沿用 mutMu -> mu 锁序；慢哈希和数据库等待不占用上报鉴权所用的 mu。
	a.mutMu.Lock()
	defer a.mutMu.Unlock()
	now := a.clk.Mono()
	a.mu.Lock()
	locked := a.login.locked(from, now)
	a.mu.Unlock()
	if locked {
		return "", ErrLocked
	}
	phc, ok, err := a.store.AdminPasswordHash(ctx)
	if err != nil {
		return "", err
	}
	if !ok {
		a.log.Warn("login refused: no admin password is set; run `probe-hub passwd`", "from", from)
		return "", ErrNoAdmin
	}
	match, err := VerifyPassword(phc, password)
	if err != nil {
		return "", err
	}
	if !match {
		a.mu.Lock()
		count := a.login.record(from, a.clk.Mono())
		a.mu.Unlock()
		a.log.Warn("login failed", "from", from, "failures", count)
		return "", ErrBadPassword
	}
	plain, h := NewToken()
	wall := a.clk.Now()
	if err := a.store.CreateSession(ctx, h, wall, wall.Add(SessionAbsolute), phc); err != nil {
		if errors.Is(err, store.ErrAdminChanged) {
			return "", ErrBadPassword
		}
		return "", err
	}
	a.mu.Lock()
	a.login.clear(from)
	a.mu.Unlock()
	if _, err := a.store.DeleteExpiredSessions(ctx, wall); err != nil {
		a.log.Warn("purging expired sessions failed", "err", err)
	}
	return plain, nil
}

// AuthenticateSession 判定 cookie 里的 token 是否对应一个活着的会话。
//
// 会话时刻必须跨重启持久化，所以过期与最近使用用墙钟；回拨会推迟过期，
// 前拨可能提前过期。过期时尝试删除，成功后回拨也不会复活该会话；删除失败记日志。
func (a *Auth) AuthenticateSession(ctx context.Context, token string) (bool, error) {
	h := HashToken(token)
	sess, ok, err := a.store.Session(ctx, h)
	if err != nil || !ok {
		return false, err
	}
	now := a.clk.Now()
	if !now.Before(sess.ExpiresAt) || now.Sub(sess.LastUsedAt) >= SessionIdle {
		if err := a.store.DeleteSession(ctx, h); err != nil {
			a.log.Warn("deleting expired session failed", "err", err)
		}
		return false, nil
	}
	if now.Sub(sess.LastUsedAt) >= touchEvery {
		a.store.TouchSessionAsync(h, now, func(err error) {
			if err != nil {
				a.log.Warn("recording session use failed", "err", err)
			}
		})
	}
	return true, nil
}

func (a *Auth) Logout(ctx context.Context, token string) error {
	return a.store.DeleteSession(ctx, HashToken(token))
}
