package collect

import (
	"errors"
	"fmt"
	"io/fs"
)

// linuxNetExclude 是回环与常见的虚拟桥接口（spec §7）。
var linuxNetExclude = []string{"lo", "docker*", "veth*", "br-*", "virbr*"}

// ProcFS 从 Linux 的 /proc 与 /sys 取原始读数。
type ProcFS struct {
	// FS 是主机根文件系统；路径相对根，如 "proc/stat"。
	FS fs.FS
	// DiskUsage 取根分区的总量与已用量；statfs 是系统调用，由平台文件注入。
	DiskUsage func(path string) (total, used uint64, err error)
}

func (p *ProcFS) bootID() (string, error) { return readTrim(p.FS, "proc/sys/kernel/random/boot_id") }

func (p *ProcFS) cpuTimes() (cpuTimes, error) {
	f, err := p.FS.Open("proc/stat")
	if err != nil {
		return cpuTimes{}, err
	}
	defer f.Close()
	return parseStat(f)
}

func (p *ProcFS) meminfo() (memInfo, error) {
	f, err := p.FS.Open("proc/meminfo")
	if err != nil {
		return memInfo{}, err
	}
	defer f.Close()
	return parseMeminfo(f)
}

// memory 按 total − MemAvailable 计已用：页缓存与可回收的 slab 不算占用。
// MemAvailable 大于 MemTotal 时差值回绕，由 Collector 的 checkUsage 拒收。
func (p *ProcFS) memory() (usage, error) {
	mi, err := p.meminfo()
	if err != nil {
		return usage{}, err
	}
	return usage{total: mi.total, used: mi.total - mi.available}, nil
}

func (p *ProcFS) swap() (usage, error) {
	mi, err := p.meminfo()
	if err != nil {
		return usage{}, err
	}
	return usage{total: mi.swapTotal, used: mi.swapTotal - mi.swapFree}, nil
}

func (p *ProcFS) disk() (usage, error) {
	total, used, err := p.DiskUsage("/")
	return usage{total: total, used: used}, err
}

func (p *ProcFS) load() (loadAvg, error) {
	f, err := p.FS.Open("proc/loadavg")
	if err != nil {
		return loadAvg{}, err
	}
	defer f.Close()
	return parseLoadavg(f)
}

// procs 数 /proc 下名字全是数字的目录。/proc 的目录列表只列线程组（进程），线程只在
// /proc/<pid>/task 下列出，所以这是进程数，与 darwin 的 proc_listallpids 同口径（spec §7）。
// /proc/loadavg 第 4 字段的分母是含线程的调度实体数，而且在容器里是整个内核的数，不用。
// 能列出 /proc 却一个进程目录都没有，说明读的不是 procfs（至少有 agent 自己），按读不到处理。
func (p *ProcFS) procs() (uint32, error) {
	// /proc 以 hidepid=2（invisible）挂载时，非 root 进程只列得出自己同 uid 的进程目录，数出来是
	// 个位数而不报错（debian 容器里 remount 后以 nobody 实测：ls 只剩自己的两个 pid，stat /proc/1
	// 报 ENOENT）。PID 1 在任何 pid 命名空间里都存在，看不到它就说明列表不全；按"要么正确要么
	// 缺失"让进程数缺失，报错经 Collector 进日志，点名 hidepid 供排查。
	if _, err := fs.Stat(p.FS, "proc/1"); err != nil {
		return 0, fmt.Errorf("proc/1 is not visible, /proc is likely mounted with hidepid so other users' processes are hidden: %w", err)
	}
	entries, err := fs.ReadDir(p.FS, "proc")
	if err != nil {
		return 0, err
	}
	var n uint32
	for _, e := range entries {
		if e.IsDir() && isPID(e.Name()) {
			n++
		}
	}
	if n == 0 {
		return 0, errors.New("proc: no process directories")
	}
	return n, nil
}

func isPID(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (p *ProcFS) uptime() (uint64, error) {
	f, err := p.FS.Open("proc/uptime")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return parseUptime(f)
}

// conns 合计 IPv4 与 IPv6 两张表，按 Host 对合计型读数的约定：表不存在（如以 ipv6.disable=1
// 启动时没有 sockstat6）即不计；表存在却打不开、读不出或认不出 inuse 行时，整个读数返回 error。
func (p *ProcFS) conns() (uint32, uint32, error) {
	var tcp, udp uint32
	got := false
	for _, name := range []string{"proc/net/sockstat", "proc/net/sockstat6"} {
		f, err := p.FS.Open(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, 0, err
		}
		t, u, err := parseSockstat(f)
		f.Close()
		if err != nil {
			return 0, 0, fmt.Errorf("%s: %w", name, err)
		}
		tcp, udp, got = tcp+t, udp+u, true
	}
	if !got {
		return 0, 0, fs.ErrNotExist
	}
	return tcp, udp, nil
}

// ifaces 从 /sys/class/net/<if>/statistics 读计数器；每个网卡一对文件，比 /proc/net/dev 少一次整表解析。
// 按 Host 对合计型读数的约定：没有 statistics 目录的条目（bonding_masters 这类文件）不是网卡，不计；
// 是网卡而计数读不出时，整个读数返回 error。
func (p *ProcFS) ifaces() ([]ifaceCounters, error) {
	entries, err := fs.ReadDir(p.FS, "sys/class/net")
	if err != nil {
		return nil, err
	}
	var out []ifaceCounters
	for _, e := range entries {
		dir := "sys/class/net/" + e.Name()
		base := dir + "/statistics"
		// 真实 /sys 里网卡条目是指向设备目录的符号链接，DirEntry 的类型看不出是不是目录，按路径 Stat。
		// 普通文件下的子路径，os.DirFS 报 ENOTDIR 而不是 ENOENT，所以先看条目本身是不是目录。
		if st, err := fs.Stat(p.FS, dir); err == nil && !st.IsDir() {
			continue
		}
		st, err := fs.Stat(p.FS, base)
		switch {
		case errors.Is(err, fs.ErrNotExist), err == nil && !st.IsDir():
			continue
		case err != nil:
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		rx, err := readUint(p.FS, base+"/rx_bytes")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		tx, err := readUint(p.FS, base+"/tx_bytes")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		out = append(out, ifaceCounters{name: e.Name(), rx: rx, tx: tx})
	}
	return out, nil
}

func (p *ProcFS) defaultNetExclude() []string { return linuxNetExclude }

func (p *ProcFS) facts() hostFacts {
	var f hostFacts
	f.hostname, _ = readTrim(p.FS, "proc/sys/kernel/hostname")
	f.kernel, _ = readTrim(p.FS, "proc/sys/kernel/osrelease")
	if r, err := p.FS.Open("etc/os-release"); err == nil {
		f.os = parseOSRelease(r)
		r.Close()
	}
	if r, err := p.FS.Open("proc/cpuinfo"); err == nil {
		ci := parseCPUInfo(r)
		r.Close()
		f.cpuModel, f.cpuCores = ci.model, ci.cores
	}
	f.virtualization = detectVirtualization(p.FS)
	return f
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
