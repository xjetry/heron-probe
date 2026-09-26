package collect

import (
	"errors"
	"fmt"
	"path"
	"runtime"
	"time"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/clock"
	"google.golang.org/protobuf/proto"
)

// Collector 把 Host 的原始读数变成一次上报。
type Collector struct {
	Host  Host
	Clock clock.Clock
	// NetInclude 非空时只统计匹配的网卡；否则统计除 NetExclude 外的全部。
	NetInclude []string
	// NetExclude 为 nil 时用 Host.defaultNetExclude。
	NetExclude []string
	Version    string
	// IcmpAvailable 默认为 false；须在 Runner.Run 前赋值，运行期间只读。
	IcmpAvailable bool

	prevCPU  *cpuTimes
	prevNet  *netCounters
	prevNetT time.Duration
}

func (c *Collector) includeIface(name string) bool {
	if len(c.NetInclude) > 0 {
		return matchAny(c.NetInclude, name)
	}
	ex := c.NetExclude
	if ex == nil {
		ex = c.Host.defaultNetExclude()
	}
	return !matchAny(ex, name)
}

func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// checkUsage 承载 used ≤ total。平台的已用量都是差或和（Linux 的 total − available，
// darwin 的页计数之和），计数彼此不一致或无符号减法回绕都会得出超过总量的值；
// 这样的读数按读不到处理并记日志，不截到总量——截断会把错误的计数伪装成满载。
func checkUsage(u usage, err error) (usage, error) {
	if err != nil {
		return usage{}, err
	}
	if u.used > u.total {
		return usage{}, fmt.Errorf("used %d exceeds total %d", u.used, u.total)
	}
	return u, nil
}

// Metrics 生成一次上报。任何一个来源读不到只让对应读数缺失，其余照常；
// 返回的 error 汇总了这些失败，供调用方记日志，不阻止上报。
func (c *Collector) Metrics() (*probev1.Metrics, error) {
	m := &probev1.Metrics{}
	var errs []error
	fail := func(what string, err error) { errs = append(errs, fmt.Errorf("%s: %w", what, err)) }
	h := c.Host

	if id, err := h.bootID(); err == nil {
		m.BootId = id
	} else {
		fail("boot_id", err)
	}

	if cur, err := h.cpuTimes(); err == nil {
		if c.prevCPU != nil {
			if pct, ok := cpuPercent(*c.prevCPU, cur); ok {
				m.CpuPct = proto.Float64(pct)
			}
		}
		c.prevCPU = &cur
	} else {
		fail("cpu", err)
	}

	if u, err := checkUsage(h.memory()); err == nil {
		m.MemTotal, m.MemUsed = proto.Uint64(u.total), proto.Uint64(u.used)
	} else {
		fail("memory", err)
	}
	if u, err := checkUsage(h.swap()); err == nil {
		m.SwapTotal, m.SwapUsed = proto.Uint64(u.total), proto.Uint64(u.used)
	} else {
		fail("swap", err)
	}
	if u, err := checkUsage(h.disk()); err == nil {
		m.DiskTotal, m.DiskUsed = proto.Uint64(u.total), proto.Uint64(u.used)
	} else {
		fail("disk", err)
	}

	if l, err := h.load(); err == nil {
		m.Load1, m.Load5, m.Load15 = proto.Float64(l.l1), proto.Float64(l.l5), proto.Float64(l.l15)
	} else {
		fail("load", err)
	}
	if n, err := h.procs(); err == nil {
		m.Procs = proto.Uint32(n)
	} else {
		fail("procs", err)
	}
	if up, err := h.uptime(); err == nil {
		m.UptimeS = proto.Uint64(up)
	} else {
		fail("uptime", err)
	}
	if tcp, udp, err := h.conns(); err == nil {
		m.TcpConns, m.UdpConns = proto.Uint32(tcp), proto.Uint32(udp)
	} else {
		fail("conns", err)
	}

	if sum, err := c.netTotals(); err == nil {
		m.NetRxTotal, m.NetTxTotal = proto.Uint64(sum.rx), proto.Uint64(sum.tx)
		now := c.Clock.Mono()
		if c.prevNet != nil && now > c.prevNetT && sum.rx >= c.prevNet.rx && sum.tx >= c.prevNet.tx {
			secs := float64(now-c.prevNetT) / float64(time.Second)
			m.NetRxBps = proto.Uint64(uint64(float64(sum.rx-c.prevNet.rx) / secs))
			m.NetTxBps = proto.Uint64(uint64(float64(sum.tx-c.prevNet.tx) / secs))
		}
		c.prevNet, c.prevNetT = &sum, now
	} else {
		fail("net", err)
	}

	return m, errors.Join(errs...)
}

func (c *Collector) netTotals() (netCounters, error) {
	ifs, err := c.Host.ifaces()
	if err != nil {
		return netCounters{}, err
	}
	var sum netCounters
	counted := false
	for _, i := range ifs {
		if !c.includeIface(i.name) {
			continue
		}
		sum.rx, sum.tx, counted = sum.rx+i.rx, sum.tx+i.tx, true
	}
	if !counted {
		return netCounters{}, errors.New("no included interface")
	}
	return sum, nil
}

// Facts 收集静态信息；读不到的字段留空，由 hub 侧展示为未知。
// arch 取本二进制的 GOARCH：与发布资产名、安装脚本的架构名同一套词汇，两个平台一致。
func (c *Collector) Facts() *probev1.Facts {
	hf := c.Host.facts()
	f := &probev1.Facts{
		Hostname: hf.hostname, Os: hf.os, Kernel: hf.kernel, Arch: runtime.GOARCH,
		Virtualization: hf.virtualization, CpuModel: hf.cpuModel, CpuCores: hf.cpuCores,
		AgentVersion: c.Version, IcmpAvailable: c.IcmpAvailable,
	}
	if f.CpuCores == 0 {
		f.CpuCores = uint32(runtime.NumCPU())
	}
	return f
}
