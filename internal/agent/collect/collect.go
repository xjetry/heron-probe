package collect

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
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
	// Log 记采集口径的状态切换（v1 ↔ v2 等，noteScopeKind）；nil 时不记。切换不是采集失败，
	// 失败走 Metrics 返回的 error 与 diagnostics。
	Log *slog.Logger
	// NetInclude 非空时只统计匹配的网卡；否则统计除 NetExclude 外的全部。
	NetInclude []string
	// NetExclude 为 nil 时用 Host.defaultNetExclude。
	NetExclude []string
	Version    string
	// IcmpAvailable 默认为 false；须在 Runner.Run 前赋值，运行期间只读。
	IcmpAvailable bool

	// 两次采样的差分基线。这组 prev* 字段都在休眠重置范围内：跨越休眠的区间会把停机时段
	// 混进分母，休眠信号到达时全部作废，下一个周期只重建基线、不出速率与占比读数。
	prevCPU       *cpuTimes
	prevCPUKey    string
	prevCgroup    *uint64
	prevCgroupT   time.Duration
	prevNet       *netCounters
	prevNetT      time.Duration
	prevNetEpoch  string
	prevDisk      *diskCounters
	prevDiskT     time.Duration
	prevDiskEpoch string
	scopeKind     heronv1.ScopeKind
	execErrPrev   string // 上一次 Facts 自检的错误文本；空 = 合法。只在变化时记日志
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

// Identify 由 runner 每个上报周期调一次：识别一次执行环境得到快照，交给 Metrics 与 Facts。
// 识别文件（mountinfo、cgroup.type、cpu.max……）每周期只读一遍；Metrics 与 Facts 不再
// 各自判断口径，同一周期内两者的范围与容量一致（spec §4.2）。
func (c *Collector) Identify() *execSnapshot { return c.Host.identify() }

// Metrics 生成一次上报。任何一个来源读不到只让对应读数缺失，其余照常；
// 返回的 error 汇总了这些失败，供调用方记日志，不阻止上报。
// snap 必须来自同周期的 Identify；nil 按识别失败处理（依赖它的读数缺失，不硬编一个口径）。
func (c *Collector) Metrics(snap *execSnapshot) (*heronv1.Metrics, error) {
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
	if snap == nil {
		snap = identifyFailedSnapshot()
	}
	c.noteScopeKind(snap.exec.GetKind())

	if id, err := h.bootID(); err == nil {
		m.BootId = id
	} else {
		fail(heronv1.CollectionComponent_COLLECTION_COMPONENT_BOOT_ID, err)
	}

	// CPU：识别已定口径。环境 cgroup 走 cpu.stat 差分，真根或 v1 退回 /proc/stat 差分；
	// 范围未知时不读——按别的口径报出的是伪装成真值的错值。基线键（来源身份 + 有效核数）
	// 变了就丢弃旧基线：容器重建、限额或 cpuset 变化后，旧差分的分母不再是本次口径。
	switch {
	case snap.exec.GetCpu() == heronv1.ResourceScope_RESOURCE_SCOPE_ENVIRONMENT && snap.readCPUUsage != nil:
		c.changeCPUBaseline(snap.cpuBaseline)
		// 环境内 /proc/stat 是宿主全机口径：cpu_pct 只由本环境用量差分得出，
		// steal/iowait 是全机计数的一部分、不归属于本环境的算力，不设置。
		c.prevCPU = nil
		usage, err := snap.readCPUUsage()
		if err != nil {
			fail(heronv1.CollectionComponent_COLLECTION_COMPONENT_CPU, err)
			break
		}
		now := c.Clock.Mono()
		if c.prevCgroup != nil {
			if pct, ok := cgroupCPUPercent(*c.prevCgroup, usage, snap.cpuCores, now-c.prevCgroupT); ok {
				// cpu.max 允许 burst 时用量可超过 cores × Δt（内核先记账后扣），算出 >100；
				// ingest 拒绝一切 >100 的百分比，钳到 100。
				m.CpuPct = proto.Float64(min(pct, 100))
			}
		}
		c.prevCgroup, c.prevCgroupT = &usage, now
	case snap.exec.GetCpu() == heronv1.ResourceScope_RESOURCE_SCOPE_HOST ||
		snap.exec.GetCpu() == heronv1.ResourceScope_RESOURCE_SCOPE_LEGACY:
		c.changeCPUBaseline(snap.cpuBaseline)
		c.prevCgroup = nil
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
	default:
		// 范围未知：缺读数，也不记采集失败——原因在 Facts.execution 的范围与说明里。
		c.changeCPUBaseline("")
	}

	// 内存与 swap：环境 cgroup 走 controller 文件（分母是识别给出的可见上限），
	// 真根或 v1 走 meminfo；范围未知时缺读数，不记采集失败。
	if u, err := c.readUsage(snap.exec.GetMemory(), snap.readMemEnv, h.memory); err == nil {
		m.MemTotal, m.MemUsed = proto.Uint64(u.total), proto.Uint64(u.used)
	} else if !errors.Is(err, errScopeUnknown) {
		fail(heronv1.CollectionComponent_COLLECTION_COMPONENT_MEMORY, err)
	}
	if u, err := c.readUsage(snap.exec.GetSwap(), snap.readSwapEnv, h.swap); err == nil {
		m.SwapTotal, m.SwapUsed = proto.Uint64(u.total), proto.Uint64(u.used)
	} else if !errors.Is(err, errScopeUnknown) {
		fail(heronv1.CollectionComponent_COLLECTION_COMPONENT_SWAP, err)
	}
	if u, err := checkUsage(h.disk()); err == nil {
		m.DiskTotal, m.DiskUsed = proto.Uint64(u.total), proto.Uint64(u.used)
	} else {
		fail(heronv1.CollectionComponent_COLLECTION_COMPONENT_DISK, err)
	}

	if snap.exec.GetLoad() == heronv1.ResourceScope_RESOURCE_SCOPE_HOST ||
		snap.exec.GetLoad() == heronv1.ResourceScope_RESOURCE_SCOPE_LEGACY {
		if l, err := h.load(); err == nil {
			m.Load1, m.Load5, m.Load15 = proto.Float64(l.l1), proto.Float64(l.l5), proto.Float64(l.l15)
			// 按核负载：分母是识别给出的负载范围核数；缺分母时不设置（不猜）。
			if cores := snap.exec.GetLoadCores(); cores > 0 {
				m.Load1PerCore = proto.Float64(l.l1 / float64(cores))
			}
		} else {
			fail(heronv1.CollectionComponent_COLLECTION_COMPONENT_LOAD, err)
		}
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
//   - prevCPU、prevCPUKey（cpu_pct / steal / iowait 的两次采样差分与它的基线键）
//   - prevCgroup、prevCgroupT（cgroup 限额下 cpu_pct 的 usage_usec 差分）
//   - prevNet、prevNetT、prevNetEpoch（网卡速率）
//   - prevDisk、prevDiskT、prevDiskEpoch（磁盘速率）
func (c *Collector) ResetRates() {
	c.prevCPU = nil
	c.prevCPUKey = ""
	c.prevCgroup, c.prevCgroupT = nil, 0
	c.prevNet, c.prevNetT, c.prevNetEpoch = nil, 0, ""
	c.prevDisk, c.prevDiskT, c.prevDiskEpoch = nil, 0, ""
}

// errScopeUnknown 表示资源的范围未知：读数缺失，但不记采集失败——
// 原因上报在 Facts.execution 的范围与说明里。
var errScopeUnknown = errors.New("scope unknown")

// readUsage 按识别给出的范围读一次内存或 swap：环境 cgroup 用快照挂上的读数入口
// （used 来自 controller 文件，total 是识别给出的可见上限），真根或 v1 用 meminfo 口径。
func (c *Collector) readUsage(scope heronv1.ResourceScope, readEnv func() (usage, error), readHost func() (usage, error)) (usage, error) {
	switch scope {
	case heronv1.ResourceScope_RESOURCE_SCOPE_ENVIRONMENT:
		if readEnv == nil {
			return usage{}, errScopeUnknown
		}
		return checkUsage(readEnv())
	case heronv1.ResourceScope_RESOURCE_SCOPE_HOST, heronv1.ResourceScope_RESOURCE_SCOPE_LEGACY:
		return checkUsage(readHost())
	default:
		return usage{}, errScopeUnknown
	}
}

// changeCPUBaseline 在基线键变化时丢弃两条 CPU 基线：键是来源身份（/proc/stat 或
// 挂载根 cgroup 目录的 dev:inode）加有效核数，任一变化都意味着旧差分的分母不再是本次口径。
func (c *Collector) changeCPUBaseline(key string) {
	if c.prevCPUKey == key {
		return
	}
	c.prevCPUKey = key
	c.prevCPU, c.prevCgroup = nil, nil
}

// noteScopeKind 在执行环境形态切换时记一行日志：识别失败、v1/无 cgroup2（口径退回
// /proc/stat、范围标 legacy），以及 v1 与 cgroup2 各形态之间的切换。每个周期都识别，
// 但只有切换才记——切换本身少见（容器重建、宿主机改挂载），按周期记会把日志刷满。
func (c *Collector) noteScopeKind(kind heronv1.ScopeKind) {
	prev := c.scopeKind
	if prev == kind {
		return
	}
	c.scopeKind = kind
	if c.Log == nil {
		return
	}
	switch {
	case kind == heronv1.ScopeKind_SCOPE_KIND_IDENTIFY_FAILED:
		c.Log.Info("execution scope identify failed: readings depending on it are missing")
	case kind == heronv1.ScopeKind_SCOPE_KIND_CGROUP_V1_LEGACY:
		// v1 的 cpu.cfs_quota_us 在宿主 root 的 cpu 控制器上也存在（与 v2 的 cpu.max 不同，
		// 它没有"只在非 root"的属性），裸机 v1 宿主同样走到这里：这不是故障，也不预设容器。
		c.Log.Info("cgroup v1: cpu limits are not readable, cpu_pct follows host-wide /proc/stat")
	case kind == heronv1.ScopeKind_SCOPE_KIND_CGROUP_NAMESPACE && prev != heronv1.ScopeKind_SCOPE_KIND_CGROUP_NAMESPACE:
		c.Log.Info("cgroup v2: mount root is the environment cgroup, cpu_pct follows cpu.stat")
	case kind == heronv1.ScopeKind_SCOPE_KIND_HOST && prev == heronv1.ScopeKind_SCOPE_KIND_CGROUP_V1_LEGACY:
		c.Log.Info("cgroup v2: mount root is the host root, readings are host-wide")
	}
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
func (c *Collector) Facts(snap *execSnapshot) *heronv1.Facts {
	hf := c.Host.facts()
	f := &heronv1.Facts{
		Hostname: hf.hostname, Os: hf.os, Kernel: hf.kernel, Arch: runtime.GOARCH,
		Virtualization: hf.virtualization, CpuModel: hf.cpuModel,
		AgentVersion: c.Version, IcmpAvailable: c.IcmpAvailable,
	}
	// 核数只来自本周期识别的有效核数（spec §4.2）：环境是 min(配额, cpuset) 的上取整
	// （1.5 核报 2），真根是主机处理器数；识别不出是 0——不再退回 runtime.NumCPU()，
	// 那会把"识别失败"伪装成一个像样的值。
	if snap != nil {
		// 上报前自检：块由本方构造，非法即构造 bug；不发非法块（hub 的同形校验会拒整份
		// Facts）。只在错误变化时记日志——按周期记会把同一个 bug 刷满日志（noteScopeKind
		// 的做法）；块被丢弃时同一快照算出的核数也不发（报 0），不让静态信息与被丢弃的
		// 范围块互相矛盾。
		if err := agentwire.ValidateExecutionScope(&snap.exec); err != nil {
			if c.Log != nil && err.Error() != c.execErrPrev {
				c.Log.Error("dropping invalid execution scope", "err", err)
			}
			c.execErrPrev = err.Error()
		} else {
			c.execErrPrev = ""
			if cores := snap.exec.GetCpuEffectiveCores(); cores > 0 {
				f.CpuCores = uint32(math.Ceil(cores))
			}
			f.Execution = proto.Clone(&snap.exec).(*heronv1.ExecutionScope)
		}
	}
	if c.diagnostics != nil {
		f.Diagnostics = proto.Clone(c.diagnostics).(*heronv1.AgentDiagnostics)
	}
	return f
}
