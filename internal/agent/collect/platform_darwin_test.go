//go:build darwin

package collect

import (
	"bufio"
	"errors"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/clock"
)

// 本文件把系统调用层的读数与系统自带命令行工具的输出对照：命令行工具走的是另一条代码路径，
// 字节偏移、单位或来源选错时两边对不上。gauge 类读数在两次读取之间会变，容差按变化幅度给，
// 远小于偏移或单位错误造成的偏差。

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	return strings.TrimSpace(string(out))
}

func cliUint(t *testing.T, name string, args ...string) uint64 {
	t.Helper()
	s := run(t, name, args...)
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		t.Fatalf("%s %v printed %q: %v", name, args, s, err)
	}
	return v
}

func within(a, b, tol uint64) bool {
	if a > b {
		return a-b <= tol
	}
	return b-a <= tol
}

func realHost(t *testing.T) *darwinHost {
	t.Helper()
	s := openDarwinSyscalls()
	if s.machErr != nil {
		t.Fatalf("libSystem: %v", s.machErr)
	}
	return &darwinHost{src: s}
}

func TestDarwinEveryMetricPresent(t *testing.T) {
	c, err := NewPlatform("test", clock.Real(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Metrics(); err != nil {
		t.Fatalf("first sample: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	m, err := c.Metrics()
	if err != nil {
		t.Fatalf("second sample: %v", err)
	}
	if m.GetBootId() == "" || m.CpuPct == nil || m.MemTotal == nil || m.MemUsed == nil || m.SwapTotal == nil || m.SwapUsed == nil ||
		m.DiskTotal == nil || m.DiskUsed == nil || m.Load1 == nil || m.Procs == nil || m.UptimeS == nil ||
		m.TcpConns == nil || m.UdpConns == nil || m.NetRxTotal == nil || m.NetTxTotal == nil || m.NetRxBps == nil {
		t.Fatalf("missing readings: %+v", m)
	}
}

func TestDarwinBootIDAndUptimeMatchSysctl(t *testing.T) {
	h := realHost(t)
	id, err := h.bootID()
	if err != nil || id != run(t, "sysctl", "-n", "kern.bootsessionuuid") {
		t.Fatalf("boot id %q, %v", id, err)
	}
	// kern.boottime 是 "{ sec = N, usec = M } ..."；now − boottime 含睡眠时间，与 CLOCK_MONOTONIC 同口径。
	bt := run(t, "sysctl", "-n", "kern.boottime")
	sec, _, _ := strings.Cut(strings.TrimPrefix(bt, "{ sec = "), ",")
	boot, err := strconv.ParseInt(sec, 10, 64)
	if err != nil {
		t.Fatalf("kern.boottime %q: %v", bt, err)
	}
	up, err := h.uptime()
	if err != nil || !within(up, uint64(time.Now().Unix()-boot), 2) {
		t.Fatalf("uptime %d, %v; now − kern.boottime = %d", up, err, time.Now().Unix()-boot)
	}
}

// vm_stat 打印页大小与各计数；"Anonymous pages" 是 internal_page_count。
func TestDarwinMemoryMatchesVMStat(t *testing.T) {
	h := realHost(t)
	mem, err := h.memory()
	if err != nil {
		t.Fatal(err)
	}
	if mem.total != cliUint(t, "sysctl", "-n", "hw.memsize") {
		t.Fatalf("total %d != hw.memsize", mem.total)
	}
	out := run(t, "vm_stat")
	pages := map[string]uint64{}
	var page uint64
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "page size of "); i >= 0 {
			page, _ = strconv.ParseUint(strings.Fields(line[i+len("page size of "):])[0], 10, 64)
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), "."), 10, 64)
		if err == nil {
			pages[strings.Trim(k, `"`)] = n
		}
	}
	want := (pages["Anonymous pages"] - pages["Pages purgeable"] + pages["Pages wired down"] + pages["Pages occupied by compressor"]) * page
	if page == 0 || !within(mem.used, want, mem.total/20) {
		t.Fatalf("used %d, vm_stat gives %d (page %d); tolerance 5%% of %d", mem.used, want, page, mem.total)
	}
}

// netstat -ib 的 <Link#n> 行给出每块网卡的完整 64 位字节数。计数只增不减：
// 前后各读一次本实现，netstat 的值必须夹在两次之间，没有容差。
func TestDarwinInterfaceCountersSandwichNetstat(t *testing.T) {
	h := realHost(t)
	before, err := h.ifaces()
	if err != nil {
		t.Fatal(err)
	}
	out := run(t, "netstat", "-ib", "-n")
	after, err := h.ifaces()
	if err != nil {
		t.Fatal(err)
	}
	index := func(ifs []ifaceCounters) map[string]ifaceCounters {
		m := map[string]ifaceCounters{}
		for _, i := range ifs {
			m[i.name] = i
		}
		return m
	}
	b, a := index(before), index(after)
	checked := 0
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 7 || !strings.HasPrefix(f[2], "<Link#") {
			continue
		}
		// 有链路地址的行多一列 Address；字节列按从行尾倒数取：Ibytes 在倒数第五，Obytes 在倒数第二。
		rx, err1 := strconv.ParseUint(f[len(f)-5], 10, 64)
		tx, err2 := strconv.ParseUint(f[len(f)-2], 10, 64)
		if err1 != nil || err2 != nil {
			t.Fatalf("cannot parse netstat line %q", line)
		}
		x, ok1 := b[f[0]]
		y, ok2 := a[f[0]]
		if !ok1 || !ok2 {
			continue // 两次读取之间出现或消失的网卡
		}
		if x.rx > rx || rx > y.rx || x.tx > tx || tx > y.tx {
			t.Errorf("%s: netstat rx=%d tx=%d not within [%d,%d]/[%d,%d]", f[0], rx, tx, x.rx, y.rx, x.tx, y.tx)
		}
		checked++
	}
	if checked == 0 || b["lo0"].name == "" {
		t.Fatalf("no interface compared (lo0 present: %v)", b["lo0"].name != "")
	}
}

func TestDarwinSwapLoadProcsDiskMatchCLI(t *testing.T) {
	h := realHost(t)
	// "total = 0.00M  used = 0.00M  free = 0.00M  (encrypted)"，单位 MiB，两位小数。
	sw, err := h.swap()
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(run(t, "sysctl", "-n", "vm.swapusage"))
	mib := func(s string) uint64 {
		v, err := strconv.ParseFloat(strings.TrimSuffix(s, "M"), 64)
		if err != nil {
			t.Fatalf("vm.swapusage field %q: %v", s, err)
		}
		return uint64(v * (1 << 20))
	}
	if !within(sw.total, mib(f[2]), 1<<20) || !within(sw.used, mib(f[5]), 64<<20) {
		t.Fatalf("swap %d/%d vs vm.swapusage %v", sw.used, sw.total, f)
	}
	// "{ 1.23 4.56 7.89 }"：一分钟负载每 5 秒更新一次，两次读取之间变化不超过 1。
	l, err := h.load()
	if err != nil {
		t.Fatal(err)
	}
	lf := strings.Fields(strings.Trim(run(t, "sysctl", "-n", "vm.loadavg"), "{ }"))
	cli, _ := strconv.ParseFloat(lf[0], 64)
	if d := l.l1 - cli; d > 1 || d < -1 {
		t.Fatalf("load1 %v vs vm.loadavg %v", l.l1, lf)
	}
	n, err := h.procs()
	if err != nil {
		t.Fatal(err)
	}
	ps := uint64(len(strings.Split(run(t, "ps", "-A", "-o", "pid="), "\n")))
	if !within(uint64(n), ps, ps/20+20) {
		t.Fatalf("procs %d vs ps %d", n, ps)
	}
	d, err := h.disk()
	if err != nil {
		t.Fatal(err)
	}
	df := strings.Fields(strings.Split(run(t, "df", "-k", "/"), "\n")[1])
	if kb, _ := strconv.ParseUint(df[1], 10, 64); d.total != kb*1024 {
		t.Fatalf("disk total %d vs df %s KiB", d.total, df[1])
	}
}

func TestDarwinConnsCountOpenSockets(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	tcp, udp, err := realHost(t).conns()
	if err != nil || tcp == 0 || udp == 0 {
		t.Fatalf("tcp=%d udp=%d err=%v with one listener and one udp socket open", tcp, udp, err)
	}
	cli := cliUint(t, "sysctl", "-n", "net.inet.tcp.pcbcount")
	if !within(uint64(tcp), cli, cli/10+16) {
		t.Fatalf("tcp %d vs sysctl %d", tcp, cli)
	}
}

// 每个 CPU 每秒走 hz 个 tick（kern.clockrate）；逐 CPU 之和除以 CPU 数与经过的秒数应接近 hz。
// 单位或字段错位会差出数量级。
func TestDarwinProcessorTicksAdvanceAtClockRate(t *testing.T) {
	s := openDarwinSyscalls()
	a, err := s.processorTicks()
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	time.Sleep(time.Second)
	b, err := s.processorTicks()
	if err != nil {
		t.Fatal(err)
	}
	dt := time.Since(t0).Seconds()
	var sum uint64
	for i := range a {
		sum += uint64(b[i] - a[i])
	}
	cr := run(t, "sysctl", "-n", "kern.clockrate")
	hzs, _, _ := strings.Cut(strings.TrimPrefix(cr, "{ hz = "), ",")
	hz, err := strconv.ParseFloat(hzs, 64)
	if err != nil {
		t.Fatalf("kern.clockrate %q", cr)
	}
	cpus := float64(len(a) / cpuStateMax)
	if rate := float64(sum) / cpus / dt; rate < hz*0.75 || rate > hz*1.25 {
		t.Fatalf("%.1f ticks per cpu per second, hz = %v", rate, hz)
	}
	if uint64(cpus) != cliUint(t, "sysctl", "-n", "hw.logicalcpu") {
		t.Fatalf("%v cpus vs hw.logicalcpu", cpus)
	}
}

func TestDarwinFactsMatchCLI(t *testing.T) {
	f := realHost(t).facts()
	if f.os != "macOS "+run(t, "sw_vers", "-productVersion") || f.kernel != run(t, "uname", "-r") ||
		f.cpuModel != run(t, "sysctl", "-n", "machdep.cpu.brand_string") || f.hostname != run(t, "sysctl", "-n", "kern.hostname") ||
		uint64(f.cpuCores) != cliUint(t, "sysctl", "-n", "hw.logicalcpu") {
		t.Fatalf("%+v", f)
	}
}

// libSystem 的函数取不到时只让 CPU、内存与进程数缺失：NewPlatform 不因此失败，agent 不退出，
// 否则 launchd 的 KeepAlive 会每 5 秒拉起一个立即退出的进程。
func TestDarwinWithoutLibSystemKeepsSysctlReadings(t *testing.T) {
	c := &Collector{Host: &darwinHost{src: &darwinSyscalls{machErr: errors.New("dlopen libSystem: test")}}, Clock: clock.Real()}
	m, err := c.Metrics()
	if err == nil || !strings.Contains(err.Error(), "dlopen libSystem: test") {
		t.Fatalf("err = %v, want the libSystem failure reported", err)
	}
	if m.CpuPct != nil || m.MemTotal != nil || m.Procs != nil {
		t.Fatalf("readings that need libSystem must be missing: %+v", m)
	}
	if m.GetBootId() == "" || m.Load1 == nil || m.SwapTotal == nil || m.DiskTotal == nil || m.TcpConns == nil || m.NetRxTotal == nil || m.UptimeS == nil {
		t.Fatalf("sysctl readings must survive: %+v", m)
	}
}
