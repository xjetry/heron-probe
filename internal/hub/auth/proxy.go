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
func ClientIP(peerAddr, xff string, trusted []netip.Prefix) netip.Addr {
	peer := peerIP(peerAddr)
	if !peer.IsValid() || !inAny(peer, trusted) {
		return peer
	}
	parts := strings.Split(xff, ",")
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
