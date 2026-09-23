package collect

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"runtime"
	"time"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/clock"
	"google.golang.org/protobuf/proto"
)

// defaultNetExclude 是不计入流量的网卡：回环与常见的虚拟桥接口。
var defaultNetExclude = []string{"lo", "docker*", "veth*", "br-*", "virbr*"}

type Collector struct {
	// FS 是主机根文件系统；路径相对根，如 "proc/stat"。
	FS fs.FS
	// DiskUsage 取根分区用量；statfs 是系统调用，由平台文件注入。
	DiskUsage func(path string) (total, used uint64, err error)
	Clock     clock.Clock
	// NetInclude 非空时只统计匹配的网卡；否则统计除 NetExclude（默认列表）外的全部。
	NetInclude    []string
	NetExclude    []string
	Version       string
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
		ex = defaultNetExclude
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

func (c *Collector) open(name string) (fs.File, error) { return c.FS.Open(name) }

// Metrics 生成一次上报。任何一个来源读不到只让对应读数缺失，其余照常；
// 返回的 error 汇总了这些失败，供调用方记日志，不阻止上报。
func (c *Collector) Metrics() (*probev1.Metrics, error) {
	m := &probev1.Metrics{}
	var errs []error
	fail := func(what string, err error) { errs = append(errs, fmt.Errorf("%s: %w", what, err)) }

	if id, err := readTrim(c.FS, "proc/sys/kernel/random/boot_id"); err == nil {
		m.BootId = id
	} else {
		fail("boot_id", err)
	}

	if f, err := c.open("proc/stat"); err == nil {
		cur, perr := parseStat(f)
		f.Close()
		if perr != nil {
			fail("stat", perr)
		} else {
			if c.prevCPU != nil {
				if pct, ok := cpuPercent(*c.prevCPU, cur); ok {
					m.CpuPct = proto.Float64(pct)
				}
			}
			c.prevCPU = &cur
		}
	} else {
		fail("stat", err)
	}

	if f, err := c.open("proc/meminfo"); err == nil {
		mi, perr := parseMeminfo(f)
		f.Close()
		if perr != nil {
			fail("meminfo", perr)
		} else {
			m.MemTotal, m.MemUsed = proto.Uint64(mi.total), proto.Uint64(mi.total-mi.available)
			m.SwapTotal, m.SwapUsed = proto.Uint64(mi.swapTotal), proto.Uint64(mi.swapTotal-mi.swapFree)
		}
	} else {
		fail("meminfo", err)
	}

	if f, err := c.open("proc/loadavg"); err == nil {
		l, perr := parseLoadavg(f)
		f.Close()
		if perr != nil {
			fail("loadavg", perr)
		} else {
			m.Load1, m.Load5, m.Load15 = proto.Float64(l.l1), proto.Float64(l.l5), proto.Float64(l.l15)
			m.Procs = proto.Uint32(l.procs)
		}
	} else {
		fail("loadavg", err)
	}

	if f, err := c.open("proc/uptime"); err == nil {
		up, perr := parseUptime(f)
		f.Close()
		if perr != nil {
			fail("uptime", perr)
		} else {
			m.UptimeS = proto.Uint64(up)
		}
	} else {
		fail("uptime", err)
	}

	if total, used, err := c.DiskUsage("/"); err == nil {
		m.DiskTotal, m.DiskUsed = proto.Uint64(total), proto.Uint64(used)
	} else {
		fail("disk", err)
	}

	var tcp, udp uint32
	gotConns := false
	for _, name := range []string{"proc/net/sockstat", "proc/net/sockstat6"} {
		f, err := c.open(name)
		if err != nil {
			continue
		}
		t, u, perr := parseSockstat(f)
		f.Close()
		if perr != nil {
			fail(name, perr)
			continue
		}
		tcp, udp, gotConns = tcp+t, udp+u, true
	}
	if gotConns {
		m.TcpConns, m.UdpConns = proto.Uint32(tcp), proto.Uint32(udp)
	} else {
		fail("sockstat", fs.ErrNotExist)
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

// netTotals 从 /sys/class/net/<if>/statistics 汇总计数器；每个网卡一对文件，
// 比 /proc/net/dev 少一次整表解析，且缺某块网卡时不影响其余。
func (c *Collector) netTotals() (netCounters, error) {
	entries, err := fs.ReadDir(c.FS, "sys/class/net")
	if err != nil {
		return netCounters{}, err
	}
	var sum netCounters
	found := false
	for _, e := range entries {
		if !c.includeIface(e.Name()) {
			continue
		}
		base := "sys/class/net/" + e.Name() + "/statistics/"
		rx, err1 := readUint(c.FS, base+"rx_bytes")
		tx, err2 := readUint(c.FS, base+"tx_bytes")
		if err1 != nil || err2 != nil {
			continue
		}
		sum.rx, sum.tx, found = sum.rx+rx, sum.tx+tx, true
	}
	if !found {
		return netCounters{}, errors.New("no interface with readable counters")
	}
	return sum, nil
}

func readUint(fsys fs.FS, name string) (uint64, error) {
	s, err := readTrim(fsys, name)
	if err != nil {
		return 0, err
	}
	var v uint64
	_, err = fmt.Sscan(s, &v)
	return v, err
}

// Facts 收集静态信息；读不到的字段留空，由 hub 侧展示为未知。
func (c *Collector) Facts() *probev1.Facts {
	f := &probev1.Facts{Arch: runtime.GOARCH, AgentVersion: c.Version, IcmpAvailable: c.IcmpAvailable}
	f.Hostname, _ = readTrim(c.FS, "proc/sys/kernel/hostname")
	f.Kernel, _ = readTrim(c.FS, "proc/sys/kernel/osrelease")
	if r, err := c.open("etc/os-release"); err == nil {
		f.Os = parseOSRelease(r)
		r.Close()
	}
	if r, err := c.open("proc/cpuinfo"); err == nil {
		ci := parseCPUInfo(r)
		r.Close()
		f.CpuModel, f.CpuCores = ci.model, ci.cores
	}
	if f.CpuCores == 0 {
		f.CpuCores = uint32(runtime.NumCPU())
	}
	f.Virtualization = detectVirtualization(c.FS)
	return f
}
