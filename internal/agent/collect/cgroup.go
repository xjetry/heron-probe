package collect

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"strconv"
	"strings"
	"time"
)

// cgroupKind 是识别出的 cgroup 形态。零值是"无 cgroup"，非 Linux 平台恒为它。
type cgroupKind int

const (
	cgroupKindNone cgroupKind = iota
	cgroupKindV1
	cgroupKindV2
)

// cgroupCPU 是 cgroup v2 给出的本执行环境 CPU 口径。
type cgroupCPU struct {
	kind cgroupKind
	// limited 表示存在限额：quota 非 max，或 cpuset 小于宿主核数。无限额时 /proc/stat 口径
	// 与本环境可用算力一致，cores 与 usageUsec 无意义。
	limited bool
	// cores 是有效核数：cpuset.cpus.effective 的核数与 cpu.max 的 quota/period 的较小者，
	// 缺的项视为不限。
	cores float64
	// usageUsec 是 cpu.stat 的 usage_usec：本 cgroup 的累计 CPU 用量，两次采样之差是本环境
	// 在区间内实际耗费的 CPU 时间。
	usageUsec uint64
}

// cgroupCPU 读 cgroup v2 的 CPU 限额与用量。v2 的判据是根上的 cpu.max：v1 没有它，限额在
// cpu/cpu.cfs_quota_us；两者都不在按无 cgroup 处理。v1 的用量与限额散落在 cpuacct 与 cpu 两个
// 控制器里，读不出与 v2 统一的口径，退回 /proc/stat，由 Collector 在状态切换时记一行日志。
//
// 判定"cpuset 是否小于宿主核数"用的宿主核数取自 /proc/cpuinfo：Facts 的 cpu_cores 与
// /proc/stat 差分的基数都是它，三处共用同一个"全部"，口径才不会各说各话。
func (p *ProcFS) cgroupCPU() (cgroupCPU, error) {
	if _, err := fs.Stat(p.FS, "sys/fs/cgroup/cpu.max"); err != nil {
		if _, verr := fs.Stat(p.FS, "sys/fs/cgroup/cpu/cpu.cfs_quota_us"); verr == nil {
			return cgroupCPU{kind: cgroupKindV1}, nil
		}
		return cgroupCPU{}, nil
	}
	out := cgroupCPU{kind: cgroupKindV2}
	maxLine, err := readTrim(p.FS, "sys/fs/cgroup/cpu.max")
	if err != nil {
		return cgroupCPU{}, err
	}
	quota, hasQuota, err := parseCPUMax(maxLine)
	if err != nil {
		return cgroupCPU{}, err
	}
	cores := math.Inf(1)
	if hasQuota {
		cores = quota
	}
	var cpuset float64 = math.Inf(1)
	// cpuset.cpus.effective 缺失按不限核数：没挂 cpuset 控制器的系统没有这个文件。
	if line, err := readTrim(p.FS, "sys/fs/cgroup/cpuset.cpus.effective"); err == nil {
		n, err := parseCPUSetCount(line)
		if err != nil {
			return cgroupCPU{}, err
		}
		cpuset = float64(n)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return cgroupCPU{}, err
	}
	out.cores = min(cores, cpuset)
	out.limited = hasQuota
	if !out.limited && !math.IsInf(cpuset, 1) {
		var hostCores uint32
		if r, err := p.FS.Open("proc/cpuinfo"); err == nil {
			hostCores = parseCPUInfo(r).cores
			r.Close()
		}
		out.limited = hostCores > 0 && cpuset < float64(hostCores)
	}
	if !out.limited {
		return out, nil
	}
	f, err := p.FS.Open("sys/fs/cgroup/cpu.stat")
	if err != nil {
		return cgroupCPU{}, err
	}
	defer f.Close()
	usage, err := parseCPUStatUsage(f)
	if err != nil {
		return cgroupCPU{}, err
	}
	out.usageUsec = usage
	return out, nil
}

// parseCPUMax 解析 cpu.max 的 "$QUOTA $PERIOD" 或 "max $PERIOD"（微秒），把 quota 折成核数。
// max 表示不限。quota 或 period 为 0 是内核不会写出的值，按格式错误处理。
func parseCPUMax(s string) (cores float64, ok bool, err error) {
	f := strings.Fields(s)
	if len(f) != 2 {
		return 0, false, fmt.Errorf("cpu.max: %q", s)
	}
	period, err := strconv.ParseUint(f[1], 10, 64)
	if err != nil || period == 0 {
		return 0, false, fmt.Errorf("cpu.max period: %q", s)
	}
	if f[0] == "max" {
		return 0, false, nil
	}
	quota, err := strconv.ParseUint(f[0], 10, 64)
	if err != nil || quota == 0 {
		return 0, false, fmt.Errorf("cpu.max quota: %q", s)
	}
	return float64(quota) / float64(period), true, nil
}

// parseCPUSetCount 数 cpuset 列表（"0-3,8,10-11"）里的 CPU 个数。
func parseCPUSetCount(s string) (int, error) {
	if s == "" {
		return 0, errors.New("cpuset: empty")
	}
	n := 0
	for _, part := range strings.Split(s, ",") {
		lo, hi, hasRange := strings.Cut(part, "-")
		a, err := strconv.Atoi(lo)
		if err != nil {
			return 0, fmt.Errorf("cpuset: %q", s)
		}
		b := a
		if hasRange {
			if b, err = strconv.Atoi(hi); err != nil || b < a {
				return 0, fmt.Errorf("cpuset: %q", s)
			}
		}
		n += b - a + 1
	}
	return n, nil
}

// parseCPUStatUsage 取 cpu.stat 的 usage_usec。usage_usec 是 v2 的必选字段，缺它是格式错误。
func parseCPUStatUsage(r io.Reader) (uint64, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "usage_usec "); ok {
			return strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, errors.New("cpu.stat: no usage_usec")
}

// cgroupCPUPercent 由两次 usage_usec 采样算占用比：Δusage 是本执行环境在区间内实际耗费的
// CPU 时间，cores × Δt 是它在同一区间内的可用算力。首样本、用量回退（cgroup 复用）、Δt 不足
// 一微秒时不设置。
func cgroupCPUPercent(prev, cur uint64, cores float64, dt time.Duration) (float64, bool) {
	if cur < prev || cores <= 0 || dt.Microseconds() <= 0 {
		return 0, false
	}
	return 100 * float64(cur-prev) / (cores * float64(dt.Microseconds())), true
}
