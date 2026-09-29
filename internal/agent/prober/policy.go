package prober

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
)

// defaultDeny 是本地策略的默认拒绝集（§8.4）：本机、链路本地（含云厂商的 metadata 地址 169.254.169.254）、组播与广播。
// 私网不在其中：内网互测是常见用法，是否拒绝由宿主机的 probe_deny 决定。
var defaultDeny = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("255.255.255.255/32"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

// Policy 是宿主机对探测目标地址的本地策略。策略只来自本地配置，hub 改不了它（§5.7）。
//
// 零值即只有默认拒绝集：漏配策略的调用方落在收紧的一侧，而不是放行一切。
type Policy struct {
	allow, deny []netip.Prefix
}

// ParsePolicy 解析配置里的 probe_allow 与 probe_deny。前缀必须是规范形式（主机位为零）：10.1.2.3/8 这类写法
// 看不出作者要的是 10.0.0.0/8 还是 10.1.2.3/32，按配置错误拒绝。同一前缀同时出现在两个列表里也是配置错误，
// 否则最长前缀匹配在两者等长时没有答案。
func ParsePolicy(allow, deny []string) (Policy, error) {
	parse := func(field string, in []string) ([]netip.Prefix, error) {
		out := make([]netip.Prefix, 0, len(in))
		for _, s := range in {
			p, err := netip.ParsePrefix(s)
			if err != nil {
				return nil, fmt.Errorf("%s: %q is not a CIDR prefix", field, s)
			}
			if p != p.Masked() {
				return nil, fmt.Errorf("%s: %q has host bits set; write %s", field, s, p.Masked())
			}
			out = append(out, p)
		}
		return out, nil
	}
	a, err := parse("probe_allow", allow)
	if err != nil {
		return Policy{}, err
	}
	d, err := parse("probe_deny", deny)
	if err != nil {
		return Policy{}, err
	}
	for _, p := range a {
		if slices.Contains(d, p) {
			return Policy{}, fmt.Errorf("%s is in both probe_allow and probe_deny", p)
		}
	}
	return Policy{allow: a, deny: d}, nil
}

// Check 按最长前缀匹配裁决：本地前缀与默认前缀等长时本地优先，所以 probe_allow 写 127.0.0.0/8 就能放行本机。
// 没有任何前缀匹配时放行。
func (p Policy) Check(a netip.Addr) error {
	a = a.Unmap()
	best, allowed := -1, true
	var hit netip.Prefix
	consider := func(prefixes []netip.Prefix, allow, local bool) {
		for _, q := range prefixes {
			if !q.Contains(a) {
				continue
			}
			// 等长时后考虑的本地前缀覆盖默认前缀；本地两列表之间的等长已被 ParsePolicy 排除。
			if q.Bits() > best || (q.Bits() == best && local) {
				best, allowed, hit = q.Bits(), allow, q
			}
		}
	}
	consider(defaultDeny, false, false)
	consider(p.deny, false, true)
	consider(p.allow, true, true)
	if allowed {
		return nil
	}
	return fmt.Errorf("target %s denied by local probe policy (%s)", a, hit)
}

// String 供启动日志说明生效的策略。
func (p Policy) String() string {
	join := func(ps []netip.Prefix) string {
		s := make([]string, len(ps))
		for i, q := range ps {
			s[i] = q.String()
		}
		return "[" + strings.Join(s, " ") + "]"
	}
	return "default_deny=" + join(defaultDeny) + " probe_deny=" + join(p.deny) + " probe_allow=" + join(p.allow)
}

// Targets 是探测引擎取目标地址的唯一入口：解析与本地策略检查在同一处，今后新增的探测种类经过它即受策略约束（§8.4）。
// 检查的是解析后实际要连的地址，所以名字无法绕过；首选地址被拒时不换用其他地址，否则攻击者可以借 DNS 挑选。
type Targets struct {
	Resolver *net.Resolver
	Policy   Policy
}

func (t Targets) Resolve(ctx context.Context, host string, v4, v6 bool) (netip.Addr, error) {
	ip, err := resolve(ctx, t.Resolver, host, v4, v6)
	if err != nil {
		return netip.Addr{}, err
	}
	if err := t.Policy.Check(ip); err != nil {
		return netip.Addr{}, err
	}
	return ip, nil
}
