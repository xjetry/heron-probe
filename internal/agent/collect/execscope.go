package collect

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"slices"
	"strconv"
	"strings"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
)

// 执行环境识别（spec §4.2）：每个上报周期先识别一次，Metrics 与 Facts 只读识别快照，
// 不再各自读识别文件。识别回答两件事：
//   - cgroup 挂载根（/sys/fs/cgroup）是真正的根 cgroup 还是本执行环境（容器/guest）的
//     命名空间根——判据是 cgroup.type 的存在性：内核只在非根 cgroup 上提供它（实验
//     （spec §13-12）：控制器未下放的子 cgroup 也有 cgroup.type 与 cpu.stat，却没有
//     cpu.max 与 memory.current；GitHub runner 的真根上则没有 cgroup.type）。真根上 cpu.stat
//     存在，所以不能用"cpu.stat 存在"当环境判据。
//   - 被消费的每个 /proc 文件（stat、meminfo、loadavg、cpuinfo）实际来自哪个文件系统——
//     stat 它们各自的 st_dev，对到 /proc/self/mountinfo 里的挂载：fstype 是 proc 才是
//     procfs，fuse.lxcfs 是 lxcfs（lxcfs 按 guest 而不是读者所在的 cgroup 虚构视图，实验
//     见 §13-12），其余按来源不明处理。lxcfs 与 docker 的部分挂载只替换个别文件
//     （spec §13-12），所以必须按文件判断，不能拿 /proc 挂载点一概而论。
//
// 一个周期内识别只做一次：runner 先 Identify，把快照交给 Metrics 与 Facts。识别的原始
// 读取都在 gather（ProcFS.identify），决策在 buildExecSnapshot（纯函数，决策表在这里，
// 用实验证据逐格测试）；决策结果既有 Facts.execution 原样的 proto，也有 Metrics 读数时
// 要用的内部口径（读哪个文件、差分基线的来源身份、归一分母）。

// fsTypeCgroup2 是 cgroup2 文件系统的 statfs 类型（CGROUP2_SUPER_MAGIC，"cgroup2fs"）。
// 在这里定义而不是用 x/sys/unix 的常量：识别的比较逻辑不带 build tag。
const fsTypeCgroup2 = 0x63677270

// procSource 是一个被消费的 /proc 文件的来源。
type procSource int

const (
	procSourceUnknown procSource = iota // stat 不到、mountinfo 对不上或 mountinfo 读不了
	procSourceProcfs                    // fstype proc：真 procfs
	procSourceLxcfs                     // fstype fuse.lxcfs：lxcfs 虚构的视图
	procSourceOther                     // 其余文件系统（tmpfs 覆盖、其他 fuse）
)

// execSnapshot 是一次执行环境识别的快照。runner 每周期 Identify 一次，交给 Metrics 与
// Facts；两个消费者只读它。exec 就是 Facts.execution 的内容；其余字段是 Metrics 的读数
// 口径，不进 proto。
type execSnapshot struct {
	exec heronv1.ExecutionScope

	// cpuBaseline 是 CPU 差分基线的身份：来源（procfs 的 /proc/stat 或挂载根 cpu.stat，
	// 后者以 cgroup 目录的设备号与 inode 标识，容器重建即换）加上归一用的容量（有效核数）。
	// 任一变化都让旧差分作废。空表示本周期没有 CPU 读数。
	cpuBaseline string
	// cpuCores 是环境路径 cpu_pct 的归一分母，与 exec.CpuEffectiveCores 同值；仅环境路径使用。
	cpuCores float64
	// memLimit、swapLimit 是环境路径的分母，与 exec 的上限字段同值。
	memLimit, swapLimit uint64

	// 环境路径的每周期读数入口，identify 按来源挂上；对应 scope 不是 ENVIRONMENT 时为 nil。
	// 挂载与读取之间文件可能消失（控制器被摘掉），读取失败按"读不到即缺失"处理。
	readCPUUsage func() (uint64, error)
	readMemEnv   func() (usage, error)
	readSwapEnv  func() (usage, error)
}

// execEvidence 是一次识别读到的原始证据。buildExecSnapshot 只看它，不再碰系统，
// 决策表的每个格子都能直接构造证据来测。
type execEvidence struct {
	// cgroup2 为 /sys/fs/cgroup 是 cgroup2（statfs 类型）。false 涵盖 v1、混合挂载与没有
	// cgroup2，一律走 legacy 读法。
	cgroup2 bool
	// rootStatErr 非 nil 且不是 ErrNotExist：挂载根的 cgroup.type stat 出错（权限等），
	// 整个识别失败。hostRoot 为 true：cgroup.type 不存在，挂载根是真根。
	// 两者都零：cgroup.type 存在，挂载根是环境 cgroup，env 有效。
	hostRoot    bool
	rootStatErr error

	// 来源判断：mountinfo 全表（nil 表示读不出或解析失败，来源全部按未知），以及四个
	// 被消费文件的设备号键（"major:minor"；errk 非 nil 按 stat 失败即来源未知）。
	mounts        []mountEntry
	mountErr      error
	statDev       fileDev
	meminfoDev    fileDev
	loadavgDev    fileDev
	cpuinfoDev    fileDev
	cpuProcessors uint32 // /proc/cpuinfo 的处理器行数，读不出为 0

	// meminfo 是识别时读的 /proc/meminfo，供上限的 min() 与主机/legacy 的可见上限用；
	// meminfoErr 非 nil 表示读不出。
	meminfo    memInfo
	meminfoErr error

	env *envEvidence // 挂载根是环境 cgroup时的证据

	// containerSignal：挂载根是真根而容器标识（/.dockerenv 等）存在，读数实为整机。
	containerSignal bool
}

// fileDev 是一次 stat 的设备号键。
type fileDev struct {
	dev  string // "major:minor"
	errk error  // stat 失败
}

// envEvidence 是挂载根是环境 cgroup（cgroup.type 存在）时的容量与文件存在性证据。
type envEvidence struct {
	// cpuMax：cpu.max 折算的配额核数（numeric 为真时有效）；缺失或 max 都按不限——
	// 控制器未下放的 cgroup 上它不存在而 cpu.stat 存在（spec §13-12），缺它不是错误；
	// 存在却认不出（err）是坏值，CPU 按未知记说明。
	cpuMax limitValue[float64]
	// cpuset：cpuset.cpus.effective 的核数与三态。fileMissing（控制器未下放）回退
	// cpuinfo 处理器数；fileBad（读错误或认不出）CPU 按未知记说明。
	cpusetCount int
	cpuset      fileState
	// cpuStatOK：cpu.stat 存在。它在真根上也有，但环境路径的用量读数离不开它；
	// 不存在（某些 slice 只下放了部分控制器，spec §13-12）时 CPU 按未知记说明。
	cpuStatOK bool
	// memMax、swapMax：memory.max / memory.swap.max；memCurrentOK、memStatOK、swapCurrentOK
	// 是对应文件的存在性。memory 控制器未下放时这些一起缺失（spec §13-12），
	// 内存与 swap 都按未知处理。
	memMax        limitValue[uint64]
	memCurrentOK  bool
	memStatOK     bool
	swapMax       limitValue[uint64]
	swapCurrentOK bool
	// dirID：挂载根 cgroup 目录的 "dev:ino"，cpu 差分基线的身份。
	dirID string
}

// fileState 是控制器文件读取的三态：文件不存在（控制器未下放）、读错误或认不出、
// 数值有效。前两者对 CPU 的 cpuset 与对限额文件的意义不同，见各使用处。
type fileState int

const (
	fileMissing fileState = iota // 文件不存在（控制器未下放）
	fileBad                      // 读错误或认不出
	fileOK                       // 数值有效
)

// limitValue 是一个限额文件的三态：数值、"max"（不限）、缺失或读不出。
type limitValue[T uint64 | float64] struct {
	value   T
	numeric bool // true 时 value 有效；false 且 err==nil 为 max 或缺失（见 missing）
	missing bool // 文件不存在（与 max、读错误区分开）
	err     error
}

func readLimit[T uint64 | float64](fsys fs.FS, name string, parse func(string) (T, bool, error)) limitValue[T] {
	s, err := readTrim(fsys, name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return limitValue[T]{missing: true}
	case err != nil:
		return limitValue[T]{err: err}
	}
	if s == "max" {
		return limitValue[T]{}
	}
	v, ok, err := parse(s)
	if err != nil {
		return limitValue[T]{err: err}
	}
	if !ok {
		return limitValue[T]{}
	}
	return limitValue[T]{value: v, numeric: true}
}

// mountEntry 是 /proc/self/mountinfo 的一行里识别要用的两件事：文件系统所在设备
// （"major:minor"，与 st_dev 的键同构）与文件系统类型。
type mountEntry struct{ dev, fstype string }

// mountTypes 把挂载表折叠成设备号到 fstype 的映射。同一设备号多次出现是常态：bind
// 挂载把同一文件系统实例挂到多个挂载点（lxcfs 的逐文件绑定即如此），fstype 必然相同；
// 而叠加挂载（mount -t proc 之类的新实例）拿的是新设备号（spec §13-12 ③），不产生
// 重复设备号。出现两种 fstype 视为表不可信：该设备从表里剔除，来源按未知处理，
// 结论与行序无关。
func mountTypes(mounts []mountEntry) map[string]string {
	out := make(map[string]string, len(mounts))
	drop := map[string]bool{}
	for _, m := range mounts {
		if prev, ok := out[m.dev]; ok {
			if prev != m.fstype {
				drop[m.dev] = true
			}
			continue
		}
		out[m.dev] = m.fstype
	}
	for dev := range drop {
		delete(out, dev)
	}
	return out
}

// sourceOf 把一个文件的设备号对到挂载表，判来源。stat 失败、表里没有这个设备或
// mountinfo 读不了，都按未知：宁可缺读数，不把来源猜成 procfs。
func sourceOf(types map[string]string, d fileDev) procSource {
	if d.errk != nil {
		return procSourceUnknown
	}
	switch types[d.dev] {
	case "proc":
		return procSourceProcfs
	case "fuse.lxcfs":
		return procSourceLxcfs
	case "":
		return procSourceUnknown
	default:
		return procSourceOther
	}
}

// identify 做一次识别：收集证据（本方法里是全部系统读取），交给 buildExecSnapshot 决策，
// 再按决策结果挂上环境路径的读数入口。系统调用层（StatID、FSKind）由平台文件注入；
// 未注入时按识别失败处理——识别不出就缺读数，不退回任何猜测的口径。
func (p *ProcFS) identify() *execSnapshot {
	if p.StatID == nil || p.FSKind == nil {
		return identifyFailedSnapshot()
	}
	ev := execEvidence{}
	if t, err := p.FSKind("/sys/fs/cgroup"); err == nil && t == fsTypeCgroup2 {
		ev.cgroup2 = true
	}
	if f, err := p.FS.Open("proc/self/mountinfo"); err == nil {
		mounts, perr := parseMountinfo(f)
		f.Close()
		if perr != nil {
			ev.mountErr = perr
		} else {
			ev.mounts = mounts
		}
	} else {
		ev.mountErr = err
	}
	ev.statDev = p.devOf("/proc/stat")
	ev.meminfoDev = p.devOf("/proc/meminfo")
	ev.loadavgDev = p.devOf("/proc/loadavg")
	ev.cpuinfoDev = p.devOf("/proc/cpuinfo")
	if r, err := p.FS.Open("proc/cpuinfo"); err == nil {
		ev.cpuProcessors = parseCPUInfo(r).cores
		r.Close()
	}
	ev.meminfo, ev.meminfoErr = p.meminfo()
	if ev.cgroup2 {
		switch _, err := fs.Stat(p.FS, "sys/fs/cgroup/cgroup.type"); {
		case err == nil:
			ev.env = p.gatherEnv()
		case errors.Is(err, fs.ErrNotExist):
			ev.hostRoot = true
			ev.containerSignal = p.containerSignal()
		default:
			ev.rootStatErr = err
		}
	}
	snap := buildExecSnapshot(ev)
	if snap.exec.GetCpu() == heronv1.ResourceScope_RESOURCE_SCOPE_ENVIRONMENT {
		snap.readCPUUsage = p.readCgroupCPUUsage
	}
	if snap.exec.GetMemory() == heronv1.ResourceScope_RESOURCE_SCOPE_ENVIRONMENT {
		snap.readMemEnv = func() (usage, error) {
			u, err := p.readCgroupMemory()
			return usage{total: snap.memLimit, used: u}, err
		}
	}
	if snap.exec.GetSwap() == heronv1.ResourceScope_RESOURCE_SCOPE_ENVIRONMENT {
		snap.readSwapEnv = func() (usage, error) {
			used, err := readUint(p.FS, "sys/fs/cgroup/memory.swap.current")
			return usage{total: snap.swapLimit, used: used}, err
		}
	}
	return snap
}

func (p *ProcFS) devOf(path string) fileDev {
	dev, _, err := p.StatID(path)
	return fileDev{dev: dev, errk: err}
}

func (p *ProcFS) gatherEnv() *envEvidence {
	env := &envEvidence{}
	env.cpuMax = readLimit(p.FS, "sys/fs/cgroup/cpu.max", parseCPUMax)
	env.cpusetCount, env.cpuset = p.readCpusetCount()
	env.cpuStatOK = cgroupFileExists(p.FS, "cpu.stat")
	env.memMax = readLimit(p.FS, "sys/fs/cgroup/memory.max", parseUintLimit)
	env.memCurrentOK = cgroupFileExists(p.FS, "memory.current")
	env.memStatOK = cgroupFileExists(p.FS, "memory.stat")
	env.swapMax = readLimit(p.FS, "sys/fs/cgroup/memory.swap.max", parseUintLimit)
	env.swapCurrentOK = cgroupFileExists(p.FS, "memory.swap.current")
	if dev, ino, err := p.StatID("/sys/fs/cgroup"); err == nil {
		env.dirID = dev + ":" + strconv.FormatUint(ino, 10)
	}
	return env
}

func cgroupFileExists(fsys fs.FS, name string) bool {
	_, err := fs.Stat(fsys, "sys/fs/cgroup/"+name)
	return err == nil
}

func parseUintLimit(s string) (uint64, bool, error) {
	fields := strings.Fields(s)
	if len(fields) != 1 {
		return 0, false, fmt.Errorf("limit: %q", s)
	}
	v, err := strconv.ParseUint(fields[0], 10, 64)
	return v, err == nil, err
}

// containerSignal 只在挂载根是真根时看：docker/podman 的 /.dockerenv 与
// /run/.containerenv、systemd 的 /run/systemd/container、PID 1 的 container= 环境变量。
// 挂载根已是环境 cgroup 时不必看——范围本来就是它；真根上有这些标识说明 agent 与监控
// 对象不是同一个环境，读数实为整机，记说明提醒读者。
func (p *ProcFS) containerSignal() bool {
	for _, name := range []string{".dockerenv", "run/.containerenv", "run/systemd/container"} {
		if _, err := fs.Stat(p.FS, name); err == nil {
			return true
		}
	}
	if b, err := fs.ReadFile(p.FS, "proc/1/environ"); err == nil {
		for _, kv := range strings.Split(string(b), "\x00") {
			if v, ok := strings.CutPrefix(kv, "container="); ok && v != "" {
				return true
			}
		}
	}
	return false
}

func identifyFailedSnapshot() *execSnapshot {
	s := &execSnapshot{exec: heronv1.ExecutionScope{
		Kind:   heronv1.ScopeKind_SCOPE_KIND_IDENTIFY_FAILED,
		Cpu:    heronv1.ResourceScope_RESOURCE_SCOPE_UNKNOWN,
		Memory: heronv1.ResourceScope_RESOURCE_SCOPE_UNKNOWN,
		Swap:   heronv1.ResourceScope_RESOURCE_SCOPE_UNKNOWN,
		Load:   heronv1.ResourceScope_RESOURCE_SCOPE_UNKNOWN,
	}}
	return s
}

// buildExecSnapshot 是决策表本体（spec §4.2）。输入证据，输出快照；不做任何系统读取。
// 资源范围未知时对应读数缺失，不改用另一个范围的数：缺读数是"未知"，错读数是"伪装"。
func buildExecSnapshot(ev execEvidence) *execSnapshot {
	if ev.rootStatErr != nil {
		return identifyFailedSnapshot()
	}
	snap := &execSnapshot{}
	e := &snap.exec
	types := mountTypes(ev.mounts)
	stat := sourceOf(types, ev.statDev)
	meminfoSrc := sourceOf(types, ev.meminfoDev)
	loadavg := sourceOf(types, ev.loadavgDev)
	cpuinfo := sourceOf(types, ev.cpuinfoDev)
	meminfoOK := ev.meminfoErr == nil
	hostCores := float64(ev.cpuProcessors)
	var notes []heronv1.ScopeNote
	note := func(n heronv1.ScopeNote) { notes = append(notes, n) }

	switch {
	case !ev.cgroup2:
		// v1 / 无 cgroup2：全部沿用现状读法（/proc/stat 差分、meminfo、loadavg），
		// 范围标 legacy、不区分。上限取 meminfo 的读数（lxcfs 的 v1 场景即 guest 视图）。
		e.Kind = heronv1.ScopeKind_SCOPE_KIND_CGROUP_V1_LEGACY
		e.Cpu, e.Memory, e.Swap, e.Load = legacy, legacy, legacy, legacy
		// v1 沿用现状：核数就是 cpuinfo 的处理器行数（不问来源，lxcfs 的 v1 场景即 guest 视图）。
		e.CpuEffectiveCores = coresField(hostCores)
		snap.cpuBaseline = procStatBaseline("legacy", hostCores)
		setMemLimits(e, snap, ev.meminfo, meminfoOK)
		// 按核负载的分母仍要求 loadavg 与 cpuinfo 都是 procfs，否则不设置。
		if loadavg == procSourceProcfs && cpuinfo == procSourceProcfs {
			e.LoadCores = coresFieldU(hostCores)
		}
		if cpuinfo != procSourceProcfs {
			note(heronv1.ScopeNote_SCOPE_NOTE_CPUINFO_NOT_PROCFS)
		}

	case ev.hostRoot:
		// 真根：读数是整台主机。CPU 要 /proc/stat 是 procfs；核数与按核负载要 cpuinfo
		// 是 procfs（真根上绑了 lxcfs 的个别文件时主机核数无从得知）；内存、swap、负载
		// 各自要求自己的来源是 procfs。
		e.Kind = heronv1.ScopeKind_SCOPE_KIND_HOST
		e.Cpu = unknown
		if stat == procSourceProcfs {
			e.Cpu = heronv1.ResourceScope_RESOURCE_SCOPE_HOST
			// 主机核数只在 cpuinfo 是 procfs 时可用；真根上绑了 lxcfs 的 cpuinfo 时
			// 无从得知（那是别的环境的视图），核数缺、基线分母也按未知记。
			hostScopeCores := 0.0
			if cpuinfo == procSourceProcfs {
				hostScopeCores = hostCores
				e.CpuEffectiveCores = coresField(hostCores)
			}
			snap.cpuBaseline = procStatBaseline("host", hostScopeCores)
		} else {
			// /proc/stat 不是 procfs（tmpfs 覆盖、lxcfs）或来源无法确定：CPU 缺读数。
			note(heronv1.ScopeNote_SCOPE_NOTE_PROC_STAT_NOT_PROCFS)
		}
		e.Memory, e.Swap = unknown, unknown
		if meminfoSrc == procSourceProcfs {
			e.Memory = heronv1.ResourceScope_RESOURCE_SCOPE_HOST
			e.Swap = heronv1.ResourceScope_RESOURCE_SCOPE_HOST
			setMemLimits(e, snap, ev.meminfo, meminfoOK)
		} else {
			// meminfo 来源不可用：内存与 swap 缺读数（没有可比的第二口径）。
			note(heronv1.ScopeNote_SCOPE_NOTE_MEMINFO_UNUSABLE)
		}
		e.Load = unknown
		if loadavg == procSourceProcfs {
			e.Load = heronv1.ResourceScope_RESOURCE_SCOPE_HOST
			if cpuinfo == procSourceProcfs {
				e.LoadCores = coresFieldU(hostCores)
			}
		} else {
			note(heronv1.ScopeNote_SCOPE_NOTE_LOADAVG_NOT_PROCFS)
		}
		if ev.containerSignal {
			note(heronv1.ScopeNote_SCOPE_NOTE_CONTAINER_SIGNAL_ON_HOST_ROOT)
		}
		if cpuinfo != procSourceProcfs {
			note(heronv1.ScopeNote_SCOPE_NOTE_CPUINFO_NOT_PROCFS)
		}

	default:
		// 挂载根是环境 cgroup（容器/guest 的命名空间根）。识别失败不可能到这（rootStatErr
		// 已挡掉），env 证据缺失只是防御——gatherEnv 与 cgroup.type 的判定同源。
		if ev.env == nil {
			return identifyFailedSnapshot()
		}
		env := ev.env
		e.Kind = heronv1.ScopeKind_SCOPE_KIND_CGROUP_NAMESPACE

		// CPU：用量走挂载根 cpu.stat；有效核数 = min(cpu.max 折算核数, cpuset 核数)。
		// cpu.max 缺失或 max 按不限；认不出（读错误、格式坏）则相反：核数未知，CPU 整体
		// 未知，不把坏限额当不限。cpuset 文件不存在（控制器未下放）回退 cpuinfo 处理器数
		// ——仅当它是 procfs（主机核数）或 lxcfs（本环境的 cpuset 视图），其余来源一律
		// 未知；读出或认不出时 CPU 未知。不取进程亲和性：taskset 只是调度建议，cgroup 的
		// 配额与 cpuset 才是可见上限（spec §13-12）。cpu.stat 不存在（部分控制器未下放的
		// slice）同样让 CPU 未知。这三种情形都记 CPU_CONTROLLER_UNREADABLE。
		quota := math.Inf(1)
		switch {
		case env.cpuMax.err != nil:
			quota = 0
		case env.cpuMax.numeric:
			quota = env.cpuMax.value
		}
		// cpuset 文件缺失（控制器未下放）按"不限"理解，但决策表要求此时用 cpuinfo 的
		// 处理器数做估计；cpuinfo 来源也不可用时核数未知——不能把"估计不出来"当成
		// "确认没有限制"，CPU 整体未知（原因由 CPUINFO 说明承载）。
		cpuset, cpusetKnown := math.Inf(1), true
		switch env.cpuset {
		case fileOK:
			cpuset = float64(env.cpusetCount)
		case fileMissing:
			if cpuinfo == procSourceProcfs || cpuinfo == procSourceLxcfs {
				cpuset = hostCores
			} else {
				cpusetKnown = false
			}
		case fileBad:
			cpuset, cpusetKnown = 0, false // 认不出：核数未知，不把坏 cpuset 当不限
		}
		if env.cpuMax.err != nil || env.cpuset == fileBad || !env.cpuStatOK {
			note(heronv1.ScopeNote_SCOPE_NOTE_CPU_CONTROLLER_UNREADABLE)
		}
		e.Cpu = unknown
		if env.cpuStatOK && quota > 0 && cpuset > 0 && cpusetKnown {
			cores := min(quota, cpuset)
			e.Cpu = environment
			e.CpuEffectiveCores = coresField(cores)
			snap.cpuCores = cores
			snap.cpuBaseline = "cgroup:" + env.dirID + "|cores=" + formatCores(cores)
		}

		// meminfo 只在来源可用时参与上限的 min（procfs 是主机总量、lxcfs 是 guest 视图，
		// spec §13-12）；总量为 0 也参与——那是"宿主没有 swap"这一已知事实，不是未知。
		// 来源不可用本身记一条说明：范围仍可能由数值上限决定，但 meminfo 没参与这件事
		// 该被看见。
		meminfoUsable := meminfoOK && (meminfoSrc == procSourceProcfs || meminfoSrc == procSourceLxcfs)
		if !meminfoUsable {
			note(heronv1.ScopeNote_SCOPE_NOTE_MEMINFO_UNUSABLE)
		}

		// 内存：memory.current/memory.stat/memory.max 缺失或读不出（控制器未下放时一起
		// 缺失，spec §13-12）→ 内存未知并记说明。上限 = min(memory.max, MemTotal)；
		// memory.max 为 max 且 meminfo 不可用 → 上限未知、内存整体未知（说明已由
		// MEMINFO_UNUSABLE 记下）。内存上限出现时必为正（proto 约定）：0 视同未知。
		e.Memory = unknown
		memFiles := env.memCurrentOK && env.memStatOK && !env.memMax.missing && env.memMax.err == nil
		if memFiles {
			if total, ok := visibleLimit(env.memMax, ev.meminfo.total, meminfoUsable); ok && total > 0 {
				e.Memory = environment
				e.MemoryLimitBytes = protoUint64(total)
				snap.memLimit = total
			}
		} else {
			note(heronv1.ScopeNote_SCOPE_NOTE_MEMORY_CONTROLLER_MISSING)
		}

		// swap：记账文件缺失或读不出（宿主没开 swap 记账）只让 swap 未知并记说明；
		// 上限规则同内存，但已知的 0 照报（memory.swap.max 写 0 的容器禁 swap，
		// spec §13-12），读数也是 0/0。
		e.Swap = unknown
		swapFiles := env.swapCurrentOK && !env.swapMax.missing && env.swapMax.err == nil
		if swapFiles {
			if total, ok := visibleLimit(env.swapMax, ev.meminfo.swapTotal, meminfoUsable); ok {
				e.Swap = environment
				e.SwapLimitBytes = protoUint64(total)
				snap.swapLimit = total
			}
		} else {
			note(heronv1.ScopeNote_SCOPE_NOTE_SWAP_ACCOUNTING_MISSING)
		}

		// 负载：loadavg 是 procfs 才是主机范围；lxcfs 的负载是 guest 的调度队列，不是
		// 主机的也不是本 cgroup 的，来源不明就未知。按核负载的分母要 cpuinfo 是 procfs
		// （主机核数）；lxcfs 的 cpuinfo 是本环境视图，不能当主机核数。
		e.Load = unknown
		if loadavg == procSourceProcfs {
			e.Load = heronv1.ResourceScope_RESOURCE_SCOPE_HOST
			if cpuinfo == procSourceProcfs {
				e.LoadCores = coresFieldU(hostCores)
			}
		} else {
			note(heronv1.ScopeNote_SCOPE_NOTE_LOADAVG_NOT_PROCFS)
		}
		if cpuinfo != procSourceProcfs {
			note(heronv1.ScopeNote_SCOPE_NOTE_CPUINFO_NOT_PROCFS)
		}
	}

	if ev.mountErr != nil {
		note(heronv1.ScopeNote_SCOPE_NOTE_MOUNTINFO_UNREADABLE)
	}
	e.Notes = dedupNotes(notes)
	return snap
}

var (
	legacy      = heronv1.ResourceScope_RESOURCE_SCOPE_LEGACY
	unknown     = heronv1.ResourceScope_RESOURCE_SCOPE_UNKNOWN
	environment = heronv1.ResourceScope_RESOURCE_SCOPE_ENVIRONMENT
)

// visibleLimit 折算一个资源的可见上限：限额是数值时取 min(限额, 外部总量)，限额不限
// 时上限就是外部总量；外部总量只在 meminfo 来源可用时参与，参与时 0 也一样参与——
// 宿主没有 swap 时 min(memory.swap.max, 0) 就是 0，这是已知的"没有可换页空间"，不是
// 未知（proto：swap 上限的已知 0 照报）。限额读不出（err/缺失）时 ok=false，资源按
// 未知处理。
func visibleLimit(limit limitValue[uint64], external uint64, externalOK bool) (uint64, bool) {
	switch {
	case limit.err != nil || limit.missing:
		return 0, false
	case limit.numeric:
		if externalOK {
			return min(limit.value, external), true
		}
		return limit.value, true
	default: // max：不限
		if externalOK {
			return external, true
		}
		return 0, false
	}
}

// setMemLimits 填主机/legacy 口径的可见上限：就是 meminfo 的总量读数。swap 的 0 照报
// ——宿主没配 swap 时上限就是 0，与读数 0/0 一致；内存总量为 0 的活系统不存在，防御
// 地不设字段（proto：内存上限出现时必为正）。
func setMemLimits(e *heronv1.ExecutionScope, snap *execSnapshot, mi memInfo, ok bool) {
	if !ok || mi.total == 0 {
		return
	}
	e.MemoryLimitBytes = protoUint64(mi.total)
	snap.memLimit = mi.total
	e.SwapLimitBytes = protoUint64(mi.swapTotal)
	snap.swapLimit = mi.swapTotal
}

func coresField(v float64) *float64 {
	if v <= 0 {
		return nil
	}
	return &v
}

func coresFieldU(v float64) *uint32 {
	if v <= 0 {
		return nil
	}
	n := uint32(v)
	return &n
}

func procStatBaseline(scope string, cores float64) string {
	return scope + ":/proc/stat|cores=" + formatCores(cores)
}

func formatCores(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func protoUint64(v uint64) *uint64 { return &v }

// dedupNotes 去重、按枚举值排序、至多 8 个（协议上限）；识别只产生固定几类，排序让
// 输出确定，测试与读者都不用管收集顺序。
func dedupNotes(notes []heronv1.ScopeNote) []heronv1.ScopeNote {
	slices.Sort(notes)
	out := slices.Compact(notes)
	if len(out) > 8 {
		out = out[:8]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
