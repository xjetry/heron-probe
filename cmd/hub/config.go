package main

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/net/idna"

	"github.com/xjetry/probe/internal/hub/ingest"
)

const (
	defaultTTL = 30 * time.Second
	minTTL     = ingest.MinTTL
)

// parseTTL 解析 PROBE_OFFLINE_AFTER。TTL 是离线发现延迟的上界，也是这条链上
// 唯一被直接配置的量：上报间隔、退避上限、告警宽限期下限都由它反推。
func parseTTL(s string) (time.Duration, error) {
	if s == "" {
		return defaultTTL, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("PROBE_OFFLINE_AFTER: %w", err)
	}
	if d < minTTL {
		return 0, fmt.Errorf("PROBE_OFFLINE_AFTER: %v is below the minimum %v", d, minTTL)
	}
	if d > ingest.MaxTTL {
		return 0, fmt.Errorf("PROBE_OFFLINE_AFTER: %v is above the maximum %v", d, ingest.MaxTTL)
	}
	return d, nil
}

// parseThemeOrigin 解析 --theme-origin，返回规范形态 scheme://host[:port]；空串表示未配置，主题功能整体关闭。只接受一个
// origin：§10.1 的主题托管按请求的 Host 与它的主机名比对来分流，路径、查询串与凭据在 origin 里没有位置，写了就说明写的人
// 以为主题能挂在某个路径下——与面板同源的路径恰恰是必须避免的。
//
// 主机名规范成浏览器放进 Host 头的形态（themeOriginHost），再经 canonicalHost 与请求的 Host 按同一规则比较。配置的写法与
// 浏览器发的不同时，要么在这里被改写成浏览器的写法，要么启动时就被拒绝；不会启动成功之后一个请求都分不到主题 origin。
func parseThemeOrigin(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("--theme-origin %q: %w", s, err)
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return "", fmt.Errorf("--theme-origin %q: scheme must be https or http, as in https://status.example.com", s)
	case u.Opaque != "" || u.Hostname() == "":
		return "", fmt.Errorf("--theme-origin %q: needs a hostname, as in https://status.example.com", s)
	case u.User != nil:
		return "", fmt.Errorf("--theme-origin %q: must not carry credentials", s)
	case (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return "", fmt.Errorf("--theme-origin %q: must be an origin (scheme and host) without a path, query or fragment; themes are served at the root of their own hostname", s)
	}
	host, err := themeOriginHost(u.Hostname())
	if err != nil {
		return "", fmt.Errorf("--theme-origin %q: %w", s, err)
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" {
		host += ":" + port
	}
	return u.Scheme + "://" + host, nil
}

// browserIDNA 与 WHATWG URL 的 domain to ASCII 取同一组 UTS #46 参数：CheckHyphens、UseSTD3ASCIIRules、VerifyDnsLength
// 关（浏览器接受 my_host、ab--cd 这样的标签），CheckBidi、CheckJoiners 开，非过渡处理（ß 不折成 ss）。浏览器按这一组把
// 地址栏里的非 ASCII 主机名转成 punycode 再放进 Host 头。
var browserIDNA = idna.New(idna.MapForLookup(), idna.BidiRule(), idna.Transitional(false), idna.StrictDomainName(false), idna.CheckHyphens(false))

// themeOriginHost 把 --theme-origin 的主机名（url.URL.Hostname，不带方括号与端口）转成浏览器在 Host 头里发送的形态：
// IP 字面量经 netip 规范，不经 IDNA（冒号不是域名字符）；域名经 browserIDNA 转成 punycode，再由 canonicalHost 规范
// （小写、去一个尾点）。
// 浏览器不会原样发出的写法一律拒绝，而不是接受后永远匹配不上：带 zone 的 IPv6 地址；最后一个标签是数字、按 WHATWG 会被
// 当作 IPv4 解析的主机名却不是四段十进制——127.1、0x7f.0.0.1、127.000.0.1 被浏览器改写成 127.0.0.1（WHATWG 的 IPv4 解析
// 接受省略的段、十六进制与八进制），status.example.123 被浏览器拒绝，netip 一个都不接受。
func themeOriginHost(h string) (string, error) {
	if a, err := netip.ParseAddr(h); err == nil {
		if a.Zone() != "" {
			return "", errors.New("an IPv6 zone never appears in a browser's Host header")
		}
		return canonicalHost(h), nil
	}
	ascii, err := browserIDNA.ToASCII(h)
	if err != nil {
		return "", fmt.Errorf("hostname is not a valid domain name: %w", err)
	}
	c := canonicalHost(ascii)
	if c == "" {
		return "", errors.New("needs a hostname, as in https://status.example.com")
	}
	if _, err := netip.ParseAddr(c); err != nil && endsInNumber(c) {
		return "", fmt.Errorf("hostname %q ends in a number, so browsers parse it as an IPv4 address; write the address as four decimal numbers, as in 127.0.0.1", h)
	}
	return c, nil
}

// endsInNumber 是 WHATWG URL 的 "ends in a number"：最后一个标签全是数字，或是 0x 开头的十六进制，这样的主机名按 IPv4 解析。
// h 已去掉尾点。
func endsInNumber(h string) bool {
	last := h[strings.LastIndexByte(h, '.')+1:]
	if last == "" {
		return false
	}
	if strings.Trim(last, "0123456789") == "" {
		return true
	}
	hex, ok := strings.CutPrefix(last, "0x")
	return ok && strings.Trim(hex, "0123456789abcdef") == ""
}

func isLoopback(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

var localtimePath = "/etc/localtime"

// loadZone 的三个来源都经 namedZone 规范化后加载，Location 的名字会随流量响应交给面板。
// fallback 只表示本机时区不可判定，serve 据此告警；显式指定的无效时区仍拒绝启动。
func loadZone(name string) (*time.Location, bool, error) {
	if name != "" {
		loc, err := namedZone(name)
		return loc, false, err
	}
	if name := os.Getenv("TZ"); name != "" {
		if loc, err := namedZone(name); err == nil {
			return loc, false, nil
		}
	}
	if target, err := filepath.EvalSymlinks(localtimePath); err == nil {
		if _, name, ok := strings.Cut(target, "zoneinfo/"); ok && name != "" {
			if loc, err := namedZone(name); err == nil {
				return loc, false, nil
			}
		}
	}
	return time.UTC, true, nil
}

// canonicalZone 去掉 tzdata 的编码目录前缀；同一区域的周期判定与面板 Intl 格式化必须共用规范名。
// Go 能加载的名字不一定能被 Intl 接受，Local 和空串也没有可下发的区域含义。
func canonicalZone(name string) (string, bool) {
	name = strings.TrimPrefix(name, "posix/")
	name = strings.TrimPrefix(name, "right/")
	return name, name != "" && name != "Local"
}

func namedZone(name string) (*time.Location, error) {
	canonical, ok := canonicalZone(name)
	if !ok {
		return nil, fmt.Errorf("--timezone: %q is not an IANA time zone", name)
	}
	loc, err := time.LoadLocation(canonical)
	if err != nil {
		return nil, fmt.Errorf("--timezone: %w", err)
	}
	return loc, nil
}
