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
	MaxTargetLen    = 253
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
	if len(t.GetTarget()) > MaxTargetLen {
		return fmt.Errorf("target must be at most %d bytes; got %d", MaxTargetLen, len(t.GetTarget()))
	}
	switch t.GetKind() {
	case heronv1.ProbeKind_PROBE_KIND_ICMP:
		if !validHost(t.GetTarget()) {
			return fmt.Errorf("target for an ICMP task must be an IP address or a host name; got %q", t.GetTarget())
		}
	case heronv1.ProbeKind_PROBE_KIND_TCP:
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
	default:
		return fmt.Errorf("kind must be PROBE_KIND_ICMP or PROBE_KIND_TCP; got %s", t.GetKind())
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
