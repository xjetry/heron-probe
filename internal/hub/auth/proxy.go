package auth

import (
	"fmt"
	"net/netip"
	"strings"
)

// ClientIP 决定一个请求的来源地址。
//
// 只有 TCP 对端落在可信列表内时，X-Forwarded-For 才被采信；此时从右向左跳过
// 可信地址，第一个不可信的就是客户端。空列表 = 不信任任何转发头、一律用对端
// 地址，是收紧方向。畸形头同样回落到对端地址。hub 不从请求头推断自己是否
// 在反代之后。
//
// xff 是该头的全部字段行（Header.Values），按出现顺序：同名的多行字段等价于按顺序用逗号拼接
// （RFC 9110 §5.3），代理可以不动客户端自带的那一行、另起一行追加它看到的地址。只读第一行，
// "从右向左"就是在客户端写的那一行里找，客户端改一个头就能换来源。只传一行（如 []string{Header.Get(…)}）
// 照样能编译，"每一行都被读到"由多行用例钉住：ratelimit 的 TestBySourceReadsEveryForwardedForLine、
// auth 的 TestClientIPReadsEveryForwardedForLine、api 的 TestLoginLockoutKeysOnEveryForwardedForLine。
func ClientIP(peerAddr string, xff []string, trusted []netip.Prefix) netip.Addr {
	peer := peerIP(peerAddr)
	if !peer.IsValid() || !inAny(peer, trusted) {
		return peer
	}
	parts := strings.Split(strings.Join(xff, ","), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		s := strings.TrimSpace(parts[i])
		if s == "" {
			continue
		}
		ip, err := netip.ParseAddr(s)
		if err != nil {
			return peer
		}
		ip = ip.Unmap()
		if !inAny(ip, trusted) {
			return ip
		}
	}
	return peer
}

// SourceKey 把 ClientIP 得到的来源地址归一化成按来源计数的键：IPv4 按单个地址，IPv6 截到所在 /64 的网络地址。
// 一台主机通常独占整个 /64（SLAAC 与隐私扩展地址随时可换），逐地址计键等于在 /64 里换个地址就换一份计数。
// 按来源计数的三处都经它：匿名入口的限流（ratelimit.BySource）、Register 的窗口失败计数与登录失败锁定（failureTracker）。
// IPv4 映射地址先还原成 IPv4：它们的前 64 位全是 0，不还原就全部落进 ::/64。netip.PrefixFrom 丢掉区域标识（%eth0），
// 键里没有它。无效地址（取不到对端）原样返回，这类请求共用一个键。
func SourceKey(a netip.Addr) netip.Addr {
	a = a.Unmap()
	if !a.Is6() {
		return a
	}
	return netip.PrefixFrom(a, 64).Masked().Addr()
}

// DescribeSource 把 SourceKey 的结果写成给人看的形式：IPv6 的键带上 /64，免得被读成一个具体地址。
func DescribeSource(key netip.Addr) string {
	if key.Is6() {
		return netip.PrefixFrom(key, 64).String()
	}
	return key.String()
}

func peerIP(addr string) netip.Addr {
	if ap, err := netip.ParseAddrPort(addr); err == nil {
		return ap.Addr().Unmap()
	}
	if ip, err := netip.ParseAddr(addr); err == nil {
		return ip.Unmap()
	}
	return netip.Addr{}
}

func inAny(ip netip.Addr, prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// ParsePrefixes 解析逗号分隔的 CIDR 列表；裸地址按单主机前缀处理。
func ParsePrefixes(list string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range strings.Split(list, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p)
			continue
		}
		ip, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: not a CIDR or address", s)
		}
		out = append(out, netip.PrefixFrom(ip, ip.BitLen()))
	}
	return out, nil
}

// RequestScheme 决定请求在客户端一侧是 https 还是 http。hub 只听明文，所以只有
// 可信代理转发的 X-Forwarded-Proto 能说明这一点；对端不可信时按 http 处理。
// 后果：hub 若实际在 TLS 反代之后而运维没有配置 --trusted-proxies，会话 cookie
// 就不带 Secure——配置可信代理是这条链成立的前提，不从请求头猜。
//
// xfProto 与 ClientIP 一样是全部字段行，取拼接后的第一个值，即最外层那一跳写的协议。这个头不带逐跳地址，
// 没法像 X-Forwarded-For 那样跳过可信代理；代理若追加而不覆盖、客户端又自带该头，第一个值就是客户端写的。
// 这里有意不处理：它只决定 Login/Logout 回给请求者自己的 cookie 带不带 Secure（service.go 的 sessionCookie），
// 客户端只能改到自己，影响不到别的来源。
func RequestScheme(peerAddr string, xfProto []string, trusted []netip.Prefix) string {
	peer := peerIP(peerAddr)
	if !peer.IsValid() || !inAny(peer, trusted) {
		return "http"
	}
	first, _, _ := strings.Cut(strings.Join(xfProto, ","), ",")
	if strings.EqualFold(strings.TrimSpace(first), "https") {
		return "https"
	}
	return "http"
}
