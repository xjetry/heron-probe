// Package collect 读取 Linux 的 /proc 与 /sys 生成一次上报。
//
// 解析全部是对 fs.FS 的纯函数，不带 build tag：它们在任何平台上都能用真机
// 抓来的快照测试。只有取根文件系统与 statfs 的几行在 platform_linux.go 里。
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

type cpuTimes struct{ idle, total uint64 }

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
		var c cpuTimes
		for i, s := range counters {
			v, err := strconv.ParseUint(s, 10, 64)
			if err != nil {
				return cpuTimes{}, err
			}
			c.total += v
			if i == 3 || i == 4 {
				c.idle += v
			}
		}
		return c, nil
	}
	return cpuTimes{}, errors.New("/proc/stat: no cpu line")
}

// cpuPercent 由两次采样的差算出忙碌比例；没有流逝的 tick 就没有读数。
func cpuPercent(prev, cur cpuTimes) (float64, bool) {
	if cur.total <= prev.total || cur.idle < prev.idle {
		return 0, false
	}
	dt := float64(cur.total - prev.total)
	di := float64(cur.idle - prev.idle)
	pct := 100 * (1 - di/dt)
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct, true
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

type loadAvg struct {
	l1, l5, l15 float64
	procs       uint32
}

func parseLoadavg(r io.Reader) (loadAvg, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return loadAvg{}, err
	}
	f := strings.Fields(string(b))
	if len(f) < 4 {
		return loadAvg{}, errors.New("/proc/loadavg: short")
	}
	var l loadAvg
	if l.l1, err = strconv.ParseFloat(f[0], 64); err != nil {
		return l, err
	}
	if l.l5, err = strconv.ParseFloat(f[1], 64); err != nil {
		return l, err
	}
	if l.l15, err = strconv.ParseFloat(f[2], 64); err != nil {
		return l, err
	}
	_, total, ok := strings.Cut(f[3], "/")
	if !ok {
		return l, errors.New("/proc/loadavg: no running/total field")
	}
	n, err := strconv.ParseUint(total, 10, 32)
	if err != nil {
		return l, err
	}
	l.procs = uint32(n)
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

// parseNetDev 读 /proc/net/dev：前两行是表头；每行 "iface: rx_bytes … tx_bytes …"，
// 接收段 8 列后是发送段。
func parseNetDev(r io.Reader) (map[string]netCounters, error) {
	out := map[string]netCounters{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		rx, err1 := strconv.ParseUint(f[0], 10, 64)
		tx, err2 := strconv.ParseUint(f[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out[strings.TrimSpace(name)] = netCounters{rx: rx, tx: tx}
	}
	if len(out) == 0 {
		return nil, errors.New("/proc/net/dev: no interfaces")
	}
	return out, sc.Err()
}

// parseSockstat 同时认 sockstat（TCP:/UDP:）与 sockstat6（TCP6:/UDP6:）。
func parseSockstat(r io.Reader) (tcp, udp uint32, err error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 || f[1] != "inuse" {
			continue
		}
		n, perr := strconv.ParseUint(f[2], 10, 32)
		if perr != nil {
			continue
		}
		switch f[0] {
		case "TCP:", "TCP6:":
			tcp += uint32(n)
		case "UDP:", "UDP6:":
			udp += uint32(n)
		}
	}
	return tcp, udp, sc.Err()
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
