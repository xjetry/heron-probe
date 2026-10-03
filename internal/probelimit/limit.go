// Package probelimit 定义探测任务的硬限制，hub 保存前与 agent 应用前各自调用。
//
// 每个节点的探测速率有上界，hub 失守也不能把节点变成扫描器或反射器。
// 常量只在此处定义一次；两侧各有自己的入口与测试，缺一侧就只剩另一侧在守。
package probelimit

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
)

const (
	MinIntervalS    = 5
	MaxIntervalS    = 3600
	MinTimeoutMs    = 100
	MaxTimeoutMs    = 5000
	MaxTasksPerNode = 64
	// MaxTargetLen 是 ICMP/TCP/DNS 的 target 上限（主机名与 host:port）；HTTP 的 target 是绝对 URL，
	// 走 MaxHTTPTargetLen。上限按种类分，CheckTask 是 hub 与 agent 两侧共同的裁决点。
	MaxTargetLen = 253
	// MaxHTTPTargetLen 取 512 而不是更大：hub 下发的任何合法清单都必须装进 agent 的响应体上限
	// agentwire.MaxResponseBytes（64 KiB，§5.9）——每节点至多 MaxTasksPerNode 个任务、每个 target
	// 至多这么长，加上其余字段的满载值必须小于它，由 internal/hub/ingest 的满载不变式测试机械地
	// 守着；该测试按登记表给出放宽任一上限时还剩多少余量。
	MaxHTTPTargetLen = 512
	// MaxDNSServerLen 是 dns_server 的上限：最长规范形式是 39 字节 IPv6 + 方括号 2 + 冒号 1 + 端口 5 位。
	// 上报响应的上界登记表引用这个常量，两处写同一个数迟早漂移。
	MaxDNSServerLen = 47
	// 一次上报须排空 MaxTasksPerNode/MinIntervalS 条每秒的满速产出，上报间隔为 TTL/3。
	// 条数和文本共同约束体积；超出 hub 读上限会被 ResourceExhausted 拒绝并回队，形成永久失败。
	MaxResultsPerReport = 1024
	MaxErrorMessageLen  = 128 // 字节；协议字符串仍须为合法 UTF-8。
	// MaxResultAge 是结果的迟到预算（§6.4 第 3 条）：agent 取走时丢弃更老的，hub 拒收更老的，两侧同一个数。
	MaxResultAge = 120 * time.Second
)

// 超时上限不超过最短间隔，单个任务的探测不会因超时预算而重叠；放宽边界必须重新审视此关系。
const _ = uint(MinIntervalS*1000 - MaxTimeoutMs)

// CheckTask 校验一个任务的字段。错误文本说清字段、约束与期望取值——agent 手上只有这个字符串。
func CheckTask(t *heronv1.ProbeTask) error {
	if t == nil {
		return errors.New("task: required")
	}
	if t.GetIntervalS() < MinIntervalS || t.GetIntervalS() > MaxIntervalS {
		return fmt.Errorf("interval_s must be between %d and %d; got %d", MinIntervalS, MaxIntervalS, t.GetIntervalS())
	}
	if t.GetTimeoutMs() < MinTimeoutMs || t.GetTimeoutMs() > MaxTimeoutMs {
		return fmt.Errorf("timeout_ms must be between %d and %d; got %d", MinTimeoutMs, MaxTimeoutMs, t.GetTimeoutMs())
	}
	switch t.GetKind() {
	case heronv1.ProbeKind_PROBE_KIND_ICMP:
		if err := checkTargetLen(t.GetTarget(), MaxTargetLen); err != nil {
			return err
		}
		if !validHost(t.GetTarget()) {
			return fmt.Errorf("target for an ICMP task must be an IP address or a host name; got %q", t.GetTarget())
		}
	case heronv1.ProbeKind_PROBE_KIND_TCP:
		if err := checkTargetLen(t.GetTarget(), MaxTargetLen); err != nil {
			return err
		}
		host, port, err := net.SplitHostPort(t.GetTarget())
		if err != nil {
			return fmt.Errorf("target for a TCP task must be host:port; got %q", t.GetTarget())
		}
		if !validHost(host) {
			return fmt.Errorf("target host for a TCP task must be an IP address or a host name; got %q", host)
		}
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 {
			return fmt.Errorf("target port must be between 1 and 65535; got %q", port)
		}
		if canonical := net.JoinHostPort(host, strconv.Itoa(p)); canonical != t.GetTarget() {
			return fmt.Errorf("target for a TCP task must use canonical host:port form; got %q; want %q", t.GetTarget(), canonical)
		}
	case heronv1.ProbeKind_PROBE_KIND_HTTP:
		if err := checkTargetLen(t.GetTarget(), MaxHTTPTargetLen); err != nil {
			return err
		}
		if err := checkHTTPURL(t.GetTarget()); err != nil {
			return err
		}
	case heronv1.ProbeKind_PROBE_KIND_DNS:
		if err := checkTargetLen(t.GetTarget(), MaxTargetLen); err != nil {
			return err
		}
		if !validHost(t.GetTarget()) {
			return fmt.Errorf("target for a DNS task must be a DNS name; got %q", t.GetTarget())
		}
		// IP 字面量没有解析可言：任务配成 DNS 名才是探测解析路径，探测 IP 字面量量的是本机组包。
		if _, err := netip.ParseAddr(t.GetTarget()); err == nil {
			return fmt.Errorf("target for a DNS task must be a DNS name, not an IP address; got %q", t.GetTarget())
		}
		return checkDNSServer(t.GetDnsServer())
	default:
		return fmt.Errorf("kind must be PROBE_KIND_ICMP, PROBE_KIND_TCP, PROBE_KIND_HTTP or PROBE_KIND_DNS; got %s", t.GetKind())
	}
	// dns_server 只对 DNS 任务有意义：其他种类携带即拒绝，静默忽略会让调用方以为它生效了。
	if t.GetDnsServer() != "" {
		return fmt.Errorf("dns_server only applies to a DNS task; got %q", t.GetDnsServer())
	}
	return nil
}

func checkTargetLen(target string, maxLen int) error {
	if len(target) > maxLen {
		return fmt.Errorf("target must be at most %d bytes; got %d", maxLen, len(target))
	}
	return nil
}

// HTTP 的 target 是绝对 http(s) URL：有主机、不含用户信息、不含片段；端口若给出在 1–65535。
// 不跟随重定向由执行侧保证，这里只管形状。
func checkHTTPURL(target string) error {
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("target for an HTTP task must be an absolute http or https URL; got %q", target)
	}
	if u.Host == "" {
		return fmt.Errorf("target for an HTTP task must have a host; got %q", target)
	}
	if u.User != nil {
		return fmt.Errorf("target for an HTTP task must not contain user info; got %q", target)
	}
	if u.Fragment != "" {
		return fmt.Errorf("target for an HTTP task must not contain a fragment; got %q", target)
	}
	if port := u.Port(); port != "" {
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 {
			return fmt.Errorf("target port must be between 1 and 65535; got %q", port)
		}
	}
	return nil
}

// dns_server 是解析器的 ip:port 规范形式，只允许 IP 字面量（协议注释给了原因）；非规范写法
// 看不出作者要的值（前导零、未压缩的 IPv6），按配置错误拒绝并给出规范形式。
func checkDNSServer(s string) error {
	if len(s) > MaxDNSServerLen {
		return fmt.Errorf("dns_server must be at most %d bytes; got %d", MaxDNSServerLen, len(s))
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return fmt.Errorf("dns_server must be ip:port; got %q", s)
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("dns_server must use an IP literal, not a name; got %q", host)
	}
	// zone 是单机接口标识，不能随任务下发到多个节点。
	if addr.Zone() != "" {
		return fmt.Errorf("dns_server must not have a zone; got %q", host)
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("dns_server port must be between 1 and 65535; got %q", port)
	}
	if canonical := net.JoinHostPort(addr.String(), strconv.Itoa(p)); canonical != s {
		return fmt.Errorf("dns_server must use canonical ip:port form; got %q; want %q", s, canonical)
	}
	return nil
}

// validHost 接受无 zone 的 IP 字面量或 DNS 名：标签由字母、数字、连字符组成，不以连字符开头或结尾。
// 不做解析：hub 不替 agent 决定名字在 agent 所在网络里解析成什么。
func validHost(h string) bool {
	if h == "" {
		return false
	}
	if addr, err := netip.ParseAddr(h); err == nil {
		// zone 是单机接口标识，不能在分配到多个节点的任务中共享；ParseAddr 不替此处约束 zone 字符。
		return addr.Zone() == ""
	}
	for _, label := range strings.Split(strings.TrimSuffix(h, "."), ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
