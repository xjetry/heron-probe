package collect

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"runtime"
	"slices"
	"strings"
	"time"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/agentwire"
	"github.com/xjetry/heron-probe/internal/clock"
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

	prevCPU       *cpuTimes
	prevNet       *netCounters
	prevNetT      time.Duration
	prevNetEpoch  string
	prevDisk      *diskCounters
	prevDiskT     time.Duration
	prevDiskEpoch string
	diagnostics   *heronv1.AgentDiagnostics
}

func (c *Collector) netFilters() (include, exclude []string) {
	if len(c.NetInclude) > 0 {
		return c.NetInclude, nil
	}
	if c.NetExclude != nil {
		return nil, c.NetExclude
	}
	return nil, c.Host.defaultNetExclude()
}

// Validate 在上报循环启动前约束参数；错误配置不进入不断上报、不断被拒绝的循环。
func (c *Collector) Validate() error {
	return agentwire.ValidateNetFilters(c.NetInclude, c.NetExclude)
}

func (c *Collector) includeIface(name string) bool {
	include, exclude := c.netFilters()
	if len(include) > 0 {
		return matchAny(include, name)
	}
	return !matchAny(exclude, name)
}

func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// checkUsage 拒收超过总量的读数：按读不到处理并记日志，不截到总量——截断会把错误的计数伪装成满载。
// 单次无符号减法回绕的结果必然大于被减数，所以 Linux 的 total − available、total − free 回绕由这里兜住。
// 先减后加的组合（darwin 的 internal − purgeable + wire + compressor）回绕后可能落回总量以内，
// 这里看不出；这类组合的每个中间差由 Host 自己拒收（Host.memory 的约定，darwin 由 vmCounts.usedPages 承担）。
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
func (c *Collector) Metrics() (*heronv1.Metrics, error) {
	m := &heronv1.Metrics{}
	include, exclude := c.netFilters()
	c.diagnostics = &heronv1.AgentDiagnostics{NetInclude: slices.Clone(include), NetExclude: slices.Clone(exclude)}
	var errs []error
	fail := func(part heronv1.CollectionComponent, err error) {
		what := strings.ToLower(strings.TrimPrefix(part.String(), "COLLECTION_COMPONENT_"))
		errs = append(errs, fmt.Errorf("%s: %w", what, err))
		c.diagnostics.FailedCollectors = append(c.diagnostics.FailedCollectors, part)
	}
	h := c.Host

	if id, err := h.bootID(); err == nil {
		m.BootId = id
	} else {
		fail(heronv1.CollectionComponent_COLLECTION_COMPONENT_BOOT_ID, err)
	}

	if cur, err := h.cpuTimes(); err == nil {
		if c.prevCPU != nil {
			if busy, steal, iowait, ok := cpuRatios(*c.prevCPU, cur); ok {
				m.CpuPct = proto.Float64(busy)
				if cur.hasSteal {
					m.CpuStealPct = proto.Float64(steal)
				}
				if cur.hasIowait {
					m.CpuIowaitPct = proto.Float64(iowait)
				}
			}
		}
		c.prevCPU = &cur
	} else {
		fail(heronv1.CollectionComponent_COLLECTION_COMPONENT_CPU, err)
	}

	if u, err := checkUsage(h.memory()); err == nil {
		m.MemTotal, m.MemUsed = proto.Uint64(u.total), proto.Uint64(u.used)
	} else {
		fail(heronv1.CollectionComponent_COLLECTION_COMPONENT_MEMORY, err)
	}
	if u, err := checkUsage(h.swap()); err == nil {
		m.SwapTotal, m.SwapUsed = proto.Uint64(u.total), proto.Uint64(u.used)
	} else {
		fail(heronv1.CollectionComponent_COLLECTION_COMPONENT_SWAP, err)
	}
	if u, err := checkUsage(h.disk()); err == nil {
		m.DiskTotal, m.DiskUsed = proto.Uint64(u.total), proto.Uint64(u.used)
	} else {
		fail(heronv1.CollectionComponent_COLLECTION_COMPONENT_DISK, err)
	}

	if l, err := h.load(); err == nil {
		m.Load1, m.Load5, m.Load15 = proto.Float64(l.l1), proto.Float64(l.l5), proto.Float64(l.l15)
	} else {
		fail(heronv1.CollectionComponent_COLLECTION_COMPONENT_LOAD, err)
	}
	if n, err := h.procs(); err == nil {
		m.Procs = proto.Uint32(n)
	} else {
		fail(heronv1.CollectionComponent_COLLECTION_COMPONENT_PROCS, err)
	}
	if up, err := h.uptime(); err == nil {
		m.UptimeS = proto.Uint64(up)
	} else {
		fail(heronv1.CollectionComponent_COLLECTION_COMPONENT_UPTIME, err)
	}
	if tcp, udp, err := h.conns(); err == nil {
		m.TcpConns, m.UdpConns = proto.Uint32(tcp), proto.Uint32(udp)
	} else {
		fail(heronv1.CollectionComponent_COLLECTION_COMPONENT_CONNS, err)
	}

	if sum, names, err := c.netTotals(); err == nil {
		encoded, _ := json.Marshal(names)
		digest := sha256.Sum256(encoded)
		m.NetCounterEpoch = hex.EncodeToString(digest[:])
		c.diagnostics.NetInterfacesTotal = uint32(len(names))
		c.diagnostics.NetInterfaces = names[:min(len(names), agentwire.MaxDiagnosticInterfaces)]
		m.NetRxTotal, m.NetTxTotal = proto.Uint64(sum.rx), proto.Uint64(sum.tx)
		now := c.Clock.Mono()
		// 同一个网卡集合的累计计数才可相减；新网卡已有的计数不是这段采样区间的流量。
		if c.prevNet != nil && c.prevNetEpoch == m.NetCounterEpoch && now > c.prevNetT && sum.rx >= c.prevNet.rx && sum.tx >= c.prevNet.tx {
			secs := float64(now-c.prevNetT) / float64(time.Second)
			m.NetRxBps = proto.Uint64(uint64(float64(sum.rx-c.prevNet.rx) / secs))
			m.NetTxBps = proto.Uint64(uint64(float64(sum.tx-c.prevNet.tx) / secs))
		}
		c.prevNet, c.prevNetT = &sum, now
		c.prevNetEpoch = m.NetCounterEpoch
	} else {
		fail(heronv1.CollectionComponent_COLLECTION_COMPONENT_NET, err)
	}

	if sum, epoch, err := c.diskTotals(); err == nil {
		now := c.Clock.Mono()
		// 整盘设备集合不变、计数不回退，两次采样才可相减；首样本与集合变化都只换基线，不伪造速率。
		if c.prevDisk != nil && c.prevDiskEpoch == epoch && now > c.prevDiskT && sum.read >= c.prevDisk.read && sum.write >= c.prevDisk.write {
			secs := float64(now-c.prevDiskT) / float64(time.Second)
			m.DiskReadBps = proto.Uint64(uint64(float64(sum.read-c.prevDisk.read) / secs))
			m.DiskWriteBps = proto.Uint64(uint64(float64(sum.write-c.prevDisk.write) / secs))
		}
		c.prevDisk, c.prevDiskT, c.prevDiskEpoch = &sum, now, epoch
	} else if !errors.Is(err, errNoDiskCounters) {
		fail(heronv1.CollectionComponent_COLLECTION_COMPONENT_DISK_IO, err)
	}

	return m, errors.Join(errs...)
}

// ResetRates 作废全部速率与差分基线，下一次 Metrics 回到首样本语义（无 cpu_pct、无网卡/磁盘速率）。
// 跨越休眠的那一次差分，分子含休眠前后的计数变化、分母（单调钟增量）不含休眠时长，
// 速率会偏大数十倍；"计数回退不置速率"的既有防线照不到这种情形，只能在休眠信号（§4.5）
// 触发时把基线整体置空，让本轮采样成为新基线的首样本。
// 属于重置范围的基线清单——新增差分基线必须加进来，否则跨休眠的那一次差分会漏过：
//   - prevCPU（cpu_pct / steal / iowait 的两次采样差分）
//   - prevNet、prevNetT、prevNetEpoch（网卡速率）
//   - prevDisk、prevDiskT、prevDiskEpoch（磁盘速率）
func (c *Collector) ResetRates() {
	c.prevCPU = nil
	c.prevNet, c.prevNetT, c.prevNetEpoch = nil, 0, ""
	c.prevDisk, c.prevDiskT, c.prevDiskEpoch = nil, 0, ""
}

// diskTotals 把整盘设备的计数合计，并给出设备集合标识：集合变化时标识随之改变，Collector 据此只换基线。
func (c *Collector) diskTotals() (diskCounters, string, error) {
	devs, err := c.Host.diskCounters()
	if err != nil {
		return diskCounters{}, "", err
	}
	var sum diskCounters
	var names []string
	for _, d := range devs {
		sum.read, sum.write = sum.read+d.read, sum.write+d.write
		names = append(names, d.name)
	}
	if len(names) == 0 {
		return diskCounters{}, "", errors.New("no whole-disk device")
	}
	slices.Sort(names)
	return sum, strings.Join(names, "\x00"), nil
}

func (c *Collector) netTotals() (netCounters, []string, error) {
	ifs, err := c.Host.ifaces()
	if err != nil {
		return netCounters{}, nil, err
	}
	var sum netCounters
	var names []string
	for _, i := range ifs {
		if !c.includeIface(i.name) {
			continue
		}
		if err := agentwire.ValidateInterfaceName(i.name); err != nil {
			return netCounters{}, nil, err
		}
		sum.rx, sum.tx = sum.rx+i.rx, sum.tx+i.tx
		names = append(names, i.name)
	}
	if len(names) == 0 {
		return netCounters{}, nil, errors.New("no included interface")
	}
	slices.Sort(names)
	return sum, names, nil
}

// Facts 收集静态信息；读不到的字段留空，由 hub 侧展示为未知。
// arch 取本二进制的 GOARCH：与发布资产名、安装脚本的架构名同一套词汇，两个平台一致。
func (c *Collector) Facts() *heronv1.Facts {
	hf := c.Host.facts()
	f := &heronv1.Facts{
		Hostname: hf.hostname, Os: hf.os, Kernel: hf.kernel, Arch: runtime.GOARCH,
		Virtualization: hf.virtualization, CpuModel: hf.cpuModel, CpuCores: hf.cpuCores,
		AgentVersion: c.Version, IcmpAvailable: c.IcmpAvailable,
	}
	if f.CpuCores == 0 {
		f.CpuCores = uint32(runtime.NumCPU())
	}
	if c.diagnostics != nil {
		f.Diagnostics = proto.Clone(c.diagnostics).(*heronv1.AgentDiagnostics)
	}
	return f
}
