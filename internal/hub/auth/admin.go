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
	ErrLocked       = errors.New("too many failed logins from this source (one IPv4 address, or one IPv6 /64)")
	ErrLoginBusy    = errors.New("password verification is busy; please try again later")
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

	// 未设置管理员也支付相同的默认哈希成本；固定摘要不授予身份，存在性另行守卫。
	absentAdminPHC = "$argon2id$v=19$m=65536,t=3,p=4$MDEyMzQ1Njc4OWFiY2RlZg$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
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
// 显式检查承载，并在日志里指明该跑 probe-hub passwd。失败按来源键计数（SourceKey：IPv4 按地址、IPv6 按 /64），
// 锁定期间的拒绝不依赖输入的密码，正确密码也不能提前解除锁定。
func (a *Auth) Login(ctx context.Context, password string, from netip.Addr) (string, error) {
	phc, err := a.verifyLoginPassword(ctx, password, from)
	if err != nil {
		return "", err
	}
	plain, h := NewToken()
	wall := a.clk.Now()
	if err := a.store.CreateSession(ctx, h, wall, wall.Add(SessionAbsolute), phc); err != nil {
		if errors.Is(err, store.ErrAdminChanged) {
			return "", ErrBadPassword
		}
		return "", err
	}
	if _, err := a.store.DeleteExpiredSessions(ctx, wall); err != nil {
		a.log.Warn("purging expired sessions failed", "err", err)
	}
	return plain, nil
}

func (a *Auth) verifyLoginPassword(ctx context.Context, password string, from netip.Addr) (string, error) {
	a.mu.Lock()
	if a.login.locked(from, a.clk.Mono()) {
		a.mu.Unlock()
		return "", ErrLocked
	}
	// 排队会保留整波匿名请求的慢哈希成本，并把合法登录推到队尾，故直接拒绝。
	//
	// 容量固定为一，不按核数推导。一次校验按存储的 PHC 自带参数分配 m KiB：新哈希取
	// argonMemory 即 64 MiB，VerifyPassword 最多接受 256 MiB，已存的旧哈希按它自带的 m 算，
	// 调小 argonMemory 降不下它。一次校验还为 p 条 lane 各起一个协程并行计算，新哈希取
	// argonThreads 即 4。所以核数既不约束 m，一次校验也已能并行占用 p 个核。
	//
	// loginGate 不分来源：ErrLoginBusy 只说明另一个请求正在校验，与本来源给的密码无关；
	// 失败按请求自己的 SourceKey 记。把忙碌记作失败，任何来源占住门都能让另一来源的
	// 管理员重试几次后被锁满 failWindow。
	if !a.loginGate.TryLock() {
		a.mu.Unlock()
		return "", ErrLoginBusy
	}
	a.mu.Unlock()
	defer a.loginGate.Unlock()
	phc, ok, err := a.store.AdminPasswordHash(ctx)
	if err != nil {
		return "", err
	}
	if !ok {
		phc = absentAdminPHC
	}
	match, err := VerifyPassword(phc, password)
	if err != nil {
		return "", err
	}
	if !ok || !match {
		a.mu.Lock()
		count := a.login.record(from, a.clk.Mono())
		a.mu.Unlock()
		a.log.Warn("login failed", "from", from, "failures", count)
		if !ok {
			a.log.Warn("login refused: no admin password is set; run `probe-hub passwd`", "from", from)
			return "", ErrNoAdmin
		}
		return "", ErrBadPassword
	}
	// 清账以密码校验通过为准，不以会话签发为准。会话写库不占门，放门后另一次校验
	// 可能已记下新的失败，写库返回后再清会把它抹掉，所以在放门前清。由此签发失败时
	// （写库出错，或改密并发使 CreateSession 返回 ErrAdminChanged）计数也已清掉：
	// 是否签发了会话只看 Login 的返回，以签发成功为条件的动作不能挂在这里。
	// 清零只删请求自己的来源键，且以给出校验当时有效的密码为前提；成功登录本来就会
	// 清零，这里不多给能力。
	a.mu.Lock()
	a.login.clear(from)
	a.mu.Unlock()
	return phc, nil
}

// AuthenticateSession 在会话 cookie 的全部候选值里找出第一个对应活着的会话的，返回它；一个都没有时第二个返回值为 false。
//
// 候选是请求里全部非空的 probe_session 值，按出现顺序排列（api 包的 sessionCandidates），其中可以混着别的主机写进
// 浏览器的值。不变式：多出来的候选不改变有效候选的结论——逐个校验、任一有效即通过，不设个数上限；设上限等于让写
// cookie 的一方用更多的值把有效值挤出去。校验不通过只返回 false：这里不调用 Login，也不碰按来源的登录失败计数
// （计数只在 verifyLoginPassword 里记），所以无效候选再多也不会让任何来源被锁定。
//
// 多个候选都有效时取顺序上第一个。hub 只有一个管理员，任一有效 token 证明的是同一身份，选哪个不影响准入；
// 调用方把选中的那个放进 ctx，它决定 Logout 吊销哪个会话、撤销当前会话时是否清 cookie、会话列表把哪个标为当前，
// 这里也只刷新它的最近使用时刻。按候选顺序选，会话状态不变时同一个 Cookie 头每次选中同一个会话。
//
// 形状不是 NewToken 明文的候选不可能是会话，查库前丢弃；其余去重后合成一条 SessionsByHash 查询，每个候选占一个
// SQL 变量。变量数有界：一个成形的候选在 Cookie 头里至少占 79 字节（"probe_session=" 14 字节、token 64 字节、
// 分隔符 ";" 1 字节）；net/http 为一个 HTTP/1.1 请求头读入的字节不超过 MaxHeaderBytes 加两份 4096 字节（读取上限
// 自带的余量，以及设上限之前已在 4096 字节读缓冲区里的部分，例如空闲连接上等待下一个请求时的预读）；cmd/hub 的 http.Server
// 在明文监听上只说 HTTP/1.1、不设 MaxHeaderBytes，取默认 1 MiB。所以候选至多 (1 MiB + 8 KiB) / 79 向下取整即
// 13376 个，低于 SQLite 的变量上限 32766。推导在 MaxHeaderBytes 加 8 KiB 不超过 32766 × 79 字节时成立，即
// MaxHeaderBytes 约 2.46 MiB 以内。形状过滤是推导的另一个前提：不成形的值可以只有几个字节，默认头部上限里
// 能放下五万多个互不相同的这种值，超过变量上限。cmd/hub 的 TestServeSessionCookieFilledToHeaderLimit 在真实
// serve 上用两种伪造值把 Cookie 头填到上限，分别钉住这两个前提。
//
// 会话时刻必须跨重启持久化，所以过期与最近使用用墙钟；回拨会推迟过期，前拨可能提前过期。查到的过期会话尝试删除，
// 成功后回拨也不会复活它；删除失败记日志。
func (a *Auth) AuthenticateSession(ctx context.Context, candidates []string) (string, bool, error) {
	var tokens []string
	var hashes [][32]byte
	seen := map[string]bool{}
	for _, c := range candidates {
		if isTokenShaped(c) && !seen[c] {
			seen[c] = true
			tokens = append(tokens, c)
			hashes = append(hashes, HashToken(c))
		}
	}
	if len(tokens) == 0 {
		return "", false, nil
	}
	found, err := a.store.SessionsByHash(ctx, hashes)
	if err != nil {
		return "", false, err
	}
	now := a.clk.Now()
	chosen := -1
	for i, h := range hashes {
		sess, ok := found[h]
		switch {
		case !ok:
		case !sessionAlive(sess, now):
			if err := a.store.DeleteSession(ctx, h); err != nil {
				a.log.Warn("deleting expired session failed", "err", err)
			}
		case chosen < 0:
			chosen = i
		}
	}
	if chosen < 0 {
		return "", false, nil
	}
	h := hashes[chosen]
	if now.Sub(found[h].LastUsedAt) >= touchEvery {
		a.store.TouchSessionAsync(h, now, func(err error) {
			if err != nil {
				a.log.Warn("recording session use failed", "err", err)
			}
		})
	}
	return tokens[chosen], true, nil
}

func (a *Auth) Logout(ctx context.Context, token string) error {
	return a.store.DeleteSession(ctx, HashToken(token))
}
