package prober

import (
	"context"
	"fmt"
	"net"
	"net/netip"
)

// IP 字面量直接使用；名字只选可用的地址族，v4 优先。
// 调用方让解析与连接共用超时上下文，解析完成后才开始测量 RTT。
func resolve(ctx context.Context, host string, v4, v6 bool) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Unmap(), nil
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return netip.Addr{}, err
	}
	if v4 {
		for _, ip := range ips {
			if ip.Unmap().Is4() {
				return ip.Unmap(), nil
			}
		}
	}
	if v6 {
		for _, ip := range ips {
			if ip.Is6() && !ip.Is4In6() {
				return ip, nil
			}
		}
	}
	return netip.Addr{}, fmt.Errorf("no address in an available family for %q", host)
}
