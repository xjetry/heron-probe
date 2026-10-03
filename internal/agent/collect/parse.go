package collect

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"strings"
)

type cpuTimes struct {
	idle, total, steal, iowait uint64
	// hasSteal、hasIowait 表示来源是否给出该累计计数：darwin 两者都不可得，老内核可能没有 steal。
	// 缺失时对应指标不设置，而不是报 0。
	hasSteal, hasIowait bool
}

// parseStat 取 /proc/stat 首行的聚合 CPU 时间。
//
// 计数器个数随内核版本变化：user nice system idle iowait 自 2.5.41 起都在，
// irq softirq steal 陆续加入，guest 与 guest_nice 已计入 user 与 nice 不再相加。
// 所以至少要 5 个、最多取前 8 个；不足 5 个是格式错误，不能越界。
func parseStat(r io.Reader) (cpuTimes, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 || f[0] != "cpu" {
			continue
		}
		counters := f[1:]
		if len(counters) < 5 {
			return cpuTimes{}, fmt.Errorf("/proc/stat: cpu line has %d counters, need at least 5", len(counters))
		}
		if len(counters) > 8 {
			counters = counters[:8]
		}
		c := cpuTimes{hasIowait: true, hasSteal: len(counters) > 7}
		for i, s := range counters {
			v, err := strconv.ParseUint(s, 10, 64)
			if err != nil {
				return cpuTimes{}, err
			}
			c.total += v
			if i == 3 || i == 4 {
				c.idle += v
			}
			if i == 4 {
				c.iowait = v
			}
			if i == 7 {
				c.steal = v
			}
		}
		return c, nil
	}
	return cpuTimes{}, errors.New("/proc/stat: no cpu line")
}

// cpuRatios 由两次 /proc/stat 采样算出忙碌、steal、iowait 三个占比，共用同一个分母与有效性判定：
// Δtotal = 0 或任一被采用的计数回退，就整次放弃，三项一起缺失，不单独保留某一项。
// idle 含 iowait，所以 cpu_pct 把 iowait 计为不忙（忙时不含 iowait）；steal 与 iowait 是另两个
// 独立占比，都不是 cpu_pct 的子集，三者可以同时有值。
func cpuRatios(prev, cur cpuTimes) (busy, steal, iowait float64, ok bool) {
	if cur.total <= prev.total || cur.idle < prev.idle || cur.steal < prev.steal || cur.iowait < prev.iowait {
		return 0, 0, 0, false
	}
	dt := float64(cur.total - prev.total)
	busy = 100 * (1 - float64(cur.idle-prev.idle)/dt)
	busy = min(max(busy, 0), 100)
	if cur.hasSteal {
		steal = 100 * float64(cur.steal-prev.steal) / dt
	}
	if cur.hasIowait {
		iowait = 100 * float64(cur.iowait-prev.iowait) / dt
	}
	return busy, steal, iowait, true
}

// cpuPercent 是 cpu_pct 的取值入口，判据与 cpuRatios 相同。
func cpuPercent(prev, cur cpuTimes) (float64, bool) {
	busy, _, _, ok := cpuRatios(prev, cur)
	return busy, ok
}

type memInfo struct{ total, available, swapTotal, swapFree uint64 }

func parseMeminfo(r io.Reader) (memInfo, error) {
	var m memInfo
	found := 0
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		v, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		v *= 1024
		switch f[0] {
		case "MemTotal:":
			m.total, found = v, found+1
		case "MemAvailable:":
			m.available, found = v, found+1
		case "SwapTotal:":
			m.swapTotal, found = v, found+1
		case "SwapFree:":
			m.swapFree, found = v, found+1
		}
	}
	if found < 4 {
		return m, errors.New("/proc/meminfo: missing fields")
	}
	return m, nil
}

type loadAvg struct{ l1, l5, l15 float64 }

// parseLoadavg 取 /proc/loadavg 的负载三元组。
func parseLoadavg(r io.Reader) (loadAvg, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return loadAvg{}, err
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return loadAvg{}, errors.New("/proc/loadavg: short")
	}
	var l loadAvg
	if l.l1, err = strconv.ParseFloat(f[0], 64); err != nil {
		return loadAvg{}, err
	}
	if l.l5, err = strconv.ParseFloat(f[1], 64); err != nil {
		return loadAvg{}, err
	}
	if l.l15, err = strconv.ParseFloat(f[2], 64); err != nil {
		return loadAvg{}, err
	}
	return l, nil
}

func parseUptime(r io.Reader) (uint64, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	f := strings.Fields(string(b))
	if len(f) < 1 {
		return 0, errors.New("/proc/uptime: empty")
	}
	sec, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0, err
	}
	return uint64(sec), nil
}

type netCounters struct{ rx, tx uint64 }

// parseSockstat 同时认 sockstat（TCP:/UDP:）与 sockstat6（TCP6:/UDP6:）。两个 inuse 行缺一，
// 或数值认不出，都是 error：把认不出的表当成 0 个套接字，合计就静默少了一族。
func parseSockstat(r io.Reader) (tcp, udp uint32, err error) {
	var gotTCP, gotUDP bool
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 || f[1] != "inuse" {
			continue
		}
		var isTCP bool
		switch f[0] {
		case "TCP:", "TCP6:":
			isTCP = true
		case "UDP:", "UDP6:":
		default:
			continue
		}
		n, err := strconv.ParseUint(f[2], 10, 32)
		if err != nil {
			return 0, 0, fmt.Errorf("%s inuse: %w", f[0], err)
		}
		if isTCP {
			tcp, gotTCP = uint32(n), true
		} else {
			udp, gotUDP = uint32(n), true
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, err
	}
	if !gotTCP || !gotUDP {
		return 0, 0, errors.New("no TCP and UDP inuse lines")
	}
	return tcp, udp, nil
}

type cpuInfo struct {
	model string
	cores uint32
}

func parseCPUInfo(r io.Reader) cpuInfo {
	var c cpuInfo
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "processor":
			c.cores++
		case "model name":
			if c.model == "" {
				c.model = strings.TrimSpace(v)
			}
		}
	}
	return c
}

func parseOSRelease(r io.Reader) string {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

func readTrim(fsys fs.FS, name string) (string, error) {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// detectVirtualization 只做几条廉价而明确的判定，认不出就返回空串。
func detectVirtualization(fsys fs.FS) string {
	if b, err := fs.ReadFile(fsys, "proc/1/environ"); err == nil {
		for _, kv := range strings.Split(string(b), "\x00") {
			if v, ok := strings.CutPrefix(kv, "container="); ok && v != "" {
				return v
			}
		}
	}
	if _, err := fs.Stat(fsys, "proc/vz"); err == nil {
		return "openvz"
	}
	if _, err := fs.Stat(fsys, "proc/xen"); err == nil {
		return "xen"
	}
	if p, err := readTrim(fsys, "sys/class/dmi/id/product_name"); err == nil {
		switch l := strings.ToLower(p); {
		case strings.Contains(l, "kvm"), strings.Contains(l, "qemu"):
			return "kvm"
		case strings.Contains(l, "vmware"):
			return "vmware"
		case strings.Contains(l, "virtualbox"):
			return "virtualbox"
		case strings.Contains(l, "hyper-v"), strings.Contains(l, "virtual machine"):
			return "hyperv"
		}
	}
	if b, err := fs.ReadFile(fsys, "proc/cpuinfo"); err == nil && strings.Contains(string(b), " hypervisor") {
		return "vm"
	}
	return ""
}
