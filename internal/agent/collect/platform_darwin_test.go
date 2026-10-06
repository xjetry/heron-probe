//go:build darwin

package collect

import (
	"bufio"
	"net"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/clock"
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
	for _, sym := range libSystemSyms {
		if err := s.need(sym.name); err != nil {
			t.Fatalf("libSystem: %v", err)
		}
	}
	return &darwinHost{src: s}
}

func TestDarwinEveryMetricPresent(t *testing.T) {
	c, err := NewPlatform("test", clock.Real(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Metrics(c.Identify()); err != nil {
		t.Fatalf("first sample: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	m, err := c.Metrics(c.Identify())
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

// vmStat 解析 vm_stat 的页大小与各行计数。
func vmStat(t *testing.T) (page uint64, pages map[string]uint64) {
	t.Helper()
	pages = map[string]uint64{}
	sc := bufio.NewScanner(strings.NewReader(run(t, "vm_stat")))
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
		if n, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), "."), 10, 64); err == nil {
			pages[strings.Trim(k, `"`)] = n
		}
	}
	return page, pages
}

// 内存读数的口径对照。不能与 vm_stat 做数值对照，原因（macOS 26.3.1 / kernel 25.3.0 / M4 Max 实测）：
// host_statistics64(HOST_VM_INFO64) 给第三方调用者返回内核按 flavor 全局缓存的快照，约每秒刷新一次——
// 同一时刻，长期进程内相邻两次调用与单次调用的新进程拿到完全相同的 38 字冻结值（实测冻结窗口
// 0.75–0.97s），而 Apple 平台二进制（vm_stat、/usr/bin/python3 经 ctypes 调同一函数）每次调用都拿到
// 新值；请求 38、40、62 字拿到的值完全相同，与请求长度无关。负载下 wired/purgeable/internal 每秒变动
// 数万页（实测释放 6 GiB 时 internal 一秒差 20.8 万页），本实现的读数（至多约 1s 前的快照）与 vm_stat
// 的新值必然超出按"同一时刻"设定的容差；前后两次读数完全相同正是快照冻结的表现，不是读错。
// 上报的 mem_used 只能来自 host_statistics64：wire/purgeable/compressor/internal 没有第三方可用的其他
// 接口（sysctl 只有 free/speculative/external），秒级滞后对按间隔上报的 gauge 可接受。
// 这里守住的是读数口径：页大小与内存总量是静态量，与 vm_stat、hw.memsize 对照；队列分类合计
// （free+active+inactive+wired+compressor+throttled）与总页数的差是 REV3 的 tag storage 页（实测约
// 0.7%），偏移读错会把零填充等 lifetime 计数（十亿级）混进页计数让合计爆掉；free 含 speculative 的
// 语义（头文件注明）与 free/speculative/external 三个 sysctl 对照——sysctl 每次读都是新值而本实现的
// 快照可滞后约 1s，所以在读数前后各采一段 sysctl 窗口，本实现的值须落在窗口范围内外扩 5%，
// 范围随负载下的真实漂移自适应，而不是与一个瞬时值比。
func TestDarwinMemoryMatchesVMStat(t *testing.T) {
	h := realHost(t)
	s := h.src.(*darwinSyscalls)
	type sample struct{ free, spec, external uint64 }
	sysctlSample := func() sample {
		t.Helper()
		f := strings.Fields(run(t, "sysctl", "-n", "vm.page_free_count", "vm.page_speculative_count", "vm.vm_page_external_count"))
		if len(f) != 3 {
			t.Fatalf("sysctl printed %v", f)
		}
		var s sample
		var err error
		if s.free, err = strconv.ParseUint(f[0], 10, 64); err != nil {
			t.Fatalf("vm.page_free_count %q: %v", f[0], err)
		}
		if s.spec, err = strconv.ParseUint(f[1], 10, 64); err != nil {
			t.Fatalf("vm.page_speculative_count %q: %v", f[1], err)
		}
		if s.external, err = strconv.ParseUint(f[2], 10, 64); err != nil {
			t.Fatalf("vm.vm_page_external_count %q: %v", f[2], err)
		}
		return s
	}
	// 快照冻结窗口实测不超过 1s，前窗取 1.6s 覆盖快照时刻。
	var win []sample
	for t0 := time.Now(); time.Since(t0) < 1600*time.Millisecond; time.Sleep(100 * time.Millisecond) {
		win = append(win, sysctlSample())
	}
	raw, err := s.vmStatistics64()
	if err != nil {
		t.Fatal(err)
	}
	c, err := parseVMStatistics64(raw)
	if err != nil {
		t.Fatal(err)
	}
	for t0 := time.Now(); time.Since(t0) < 300*time.Millisecond; time.Sleep(100 * time.Millisecond) {
		win = append(win, sysctlSample())
	}
	cliPage, _ := vmStat(t)
	page, err := s.pageSize()
	if err != nil || page != cliPage {
		t.Fatalf("page size %d, %v; vm_stat says %d", page, err, cliPage)
	}
	mem, err := h.memory()
	if err != nil || mem.total != cliUint(t, "sysctl", "-n", "hw.memsize") {
		t.Fatalf("memory %+v, %v; total must equal hw.memsize", mem, err)
	}
	total := mem.total / page
	// 队列分类合计：free+active+inactive+wired+compressor+throttled 与总页数的差是 tag storage 页，
	// 必须非负且远小于总量（实测约 0.7%，上限取 1/25）；任一页计数读成 lifetime 计数都会让合计溢出总量。
	queues := c.free + c.active + c.inactive + c.wire + c.compressor + c.throttle
	if residual := int64(total) - int64(queues); residual < 0 || residual > int64(total/25) {
		t.Errorf("queue pages free %d + active %d + inactive %d + wired %d + compressor %d + throttled %d = %d, total %d: residual %d out of [0, %d]",
			c.free, c.active, c.inactive, c.wire, c.compressor, c.throttle, queues, total, residual, total/25)
	}
	// sysctl 对照：窗口涵盖快照时刻，值须落在窗口范围内外扩 5%+64（与本实现读错字段的量级相比可忽略，
	// 与快照滞后导致的漂移同形自适应）。
	windowRange := func(pick func(sample) uint64) (uint64, uint64) {
		lo, hi := pick(win[0]), pick(win[0])
		for _, w := range win[1:] {
			lo, hi = min(lo, pick(w)), max(hi, pick(w))
		}
		return lo, hi
	}
	checkWindow := func(name string, got, lo, hi uint64) {
		t.Helper()
		if slack := got/20 + 64; got+slack < lo || got > hi+slack {
			t.Errorf("%s %d pages, sysctl window [%d, %d] (slack %d)", name, got, lo, hi, slack)
		}
	}
	// free_count 含 speculative（头文件注明），sysctl 把两者分列。
	if c.free < c.speculative {
		t.Errorf("free %d below speculative %d: header says speculative is counted in free_count", c.free, c.speculative)
	} else {
		lo, hi := windowRange(func(w sample) uint64 { return w.free })
		checkWindow("free − speculative =", c.free-c.speculative, lo, hi)
	}
	lo, hi := windowRange(func(w sample) uint64 { return w.spec })
	checkWindow("speculative", c.speculative, lo, hi)
	lo, hi = windowRange(func(w sample) uint64 { return w.external })
	checkWindow("external", c.external, lo, hi)
}

// netstat -ib 的 <Link#n> 行给出每块网卡的完整 64 位字节数。先比网卡集合：实现漏读一块网卡，
// 合计就少了它的全部字节（netstat 给 down 的网卡名加 *，先去掉）；只放过两次读取之间新建或销毁的网卡。
// 再比计数：计数只增不减，前后各读一次本实现，netstat 的值必须夹在两次之间，没有容差。
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
	cli := map[string]bool{}
	checked := 0
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 7 || !strings.HasPrefix(f[2], "<Link#") {
			continue
		}
		name := strings.TrimSuffix(f[0], "*")
		cli[name] = true
		// 有链路地址的行多一列 Address；字节列按从行尾倒数取：Ibytes 在倒数第五，Obytes 在倒数第二。
		rx, err1 := strconv.ParseUint(f[len(f)-5], 10, 64)
		tx, err2 := strconv.ParseUint(f[len(f)-2], 10, 64)
		if err1 != nil || err2 != nil {
			t.Fatalf("cannot parse netstat line %q", line)
		}
		x, ok1 := b[name]
		y, ok2 := a[name]
		if ok1 != ok2 {
			continue // 两次读取之间新建或销毁的网卡
		}
		if !ok1 {
			t.Errorf("%s: listed by netstat, missing from both reads", name)
			continue
		}
		if x.rx > rx || rx > y.rx || x.tx > tx || tx > y.tx {
			t.Errorf("%s: netstat rx=%d tx=%d not within [%d,%d]/[%d,%d]", name, rx, tx, x.rx, y.rx, x.tx, y.tx)
		}
		checked++
	}
	for name := range b {
		if _, again := a[name]; again && !cli[name] {
			t.Errorf("%s: read twice by the implementation, not listed by netstat", name)
		}
	}
	if checked == 0 || !cli["lo0"] {
		t.Fatalf("no interface compared (lo0 listed: %v)", cli["lo0"])
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
	// "{ 1.23 4.56 7.89 }"，两位小数。负载每 5 秒更新一次，两次读取之间可能恰好更新：
	// 前后各读一次本实现，CLI 值须落在两次读数围成的区间内（外扩 0.005 容下舍入）。
	l0, err := h.load()
	if err != nil {
		t.Fatal(err)
	}
	lf := strings.Fields(strings.Trim(run(t, "sysctl", "-n", "vm.loadavg"), "{ }"))
	l1, err := h.load()
	if err != nil {
		t.Fatal(err)
	}
	for i, pair := range [][2]float64{{l0.l1, l1.l1}, {l0.l5, l1.l5}, {l0.l15, l1.l15}} {
		cli, err := strconv.ParseFloat(lf[i], 64)
		if err != nil {
			t.Fatalf("vm.loadavg %v: %v", lf, err)
		}
		if lo, hi := min(pair[0], pair[1])-0.005, max(pair[0], pair[1])+0.005; cli < lo || cli > hi {
			t.Fatalf("load[%d]: vm.loadavg %v not within [%v, %v]", i, cli, lo, hi)
		}
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
	for _, c := range []struct {
		name string
		got  uint32
	}{{"net.inet.tcp.pcbcount", tcp}, {"net.inet.udp.pcbcount", udp}} {
		cli := cliUint(t, "sysctl", "-n", c.name)
		if !within(uint64(c.got), cli, cli/10+16) {
			t.Fatalf("%s: implementation %d, sysctl %d", c.name, c.got, cli)
		}
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
	h := realHost(t)
	f := h.facts()
	if f.os != "macOS "+run(t, "sw_vers", "-productVersion") || f.kernel != run(t, "uname", "-r") ||
		f.cpuModel != run(t, "sysctl", "-n", "machdep.cpu.brand_string") || f.hostname != run(t, "sysctl", "-n", "kern.hostname") {
		t.Fatalf("%+v", f)
	}
	// Facts.cpu_cores 来自识别快照：darwin 的有效核数是 hw.logicalcpu。
	if cores := h.identify().exec.GetCpuEffectiveCores(); uint64(cores) != cliUint(t, "sysctl", "-n", "hw.logicalcpu") {
		t.Fatalf("identify cores = %v, want hw.logicalcpu", cores)
	}
}

// 两次采样之后的全部读数；第二次才有 cpu_pct。
func twoSamples(t *testing.T, c *Collector) (*heronv1.Metrics, error) {
	t.Helper()
	c.Metrics(c.Identify())
	time.Sleep(1100 * time.Millisecond)
	return c.Metrics(c.Identify())
}

// 缺一个 libSystem 函数只让依赖它的读数缺失：NewPlatform 与这里共用 newDarwinCollector，
// 构造不失败、不 panic，否则 launchd 的 KeepAlive 会每 5 秒拉起一个立即退出的进程。
func TestDarwinMissingLibSystemFunctionOnlyDropsItsReading(t *testing.T) {
	syms := slices.Clone(libSystemSyms)
	for i := range syms {
		if syms[i].name == "proc_listallpids" {
			syms[i].name = "proc_listallpids_nosuch"
		}
	}
	c, err := newDarwinCollector(openLibSystem(libSystemPath, syms), "t", clock.Real(), nil, nil)
	if err != nil {
		t.Fatalf("a missing function must not fail construction: %v", err)
	}
	m, err := twoSamples(t, c)
	if m.Procs != nil || err == nil || !strings.Contains(err.Error(), "proc_listallpids") {
		t.Fatalf("procs set %v, err = %v; want procs missing and the symbol named", m.Procs != nil, err)
	}
	if m.CpuPct == nil || m.MemUsed == nil {
		t.Fatalf("cpu_pct set %v, mem_used set %v: readings that do not need proc_listallpids must survive", m.CpuPct != nil, m.MemUsed != nil)
	}
}

// libSystem 整个打不开：依赖它的 CPU、已用内存与进程数缺失，sysctl 读数照常，构造不失败。
func TestDarwinWithoutLibSystemKeepsSysctlReadings(t *testing.T) {
	c, err := newDarwinCollector(openLibSystem("/nonexistent/libSystem.B.dylib", libSystemSyms), "t", clock.Real(), nil, nil)
	if err != nil {
		t.Fatalf("an unloadable libSystem must not fail construction: %v", err)
	}
	m, err := twoSamples(t, c)
	if err == nil || !strings.Contains(err.Error(), "dlopen /nonexistent/libSystem.B.dylib") {
		t.Fatalf("err = %v, want the libSystem failure reported", err)
	}
	if m.CpuPct != nil || m.MemUsed != nil || m.Procs != nil {
		t.Fatalf("readings that need libSystem must be missing: cpu_pct %v mem_used %v procs %v", m.CpuPct != nil, m.MemUsed != nil, m.Procs != nil)
	}
	if m.GetBootId() == "" || m.Load1 == nil || m.SwapTotal == nil || m.DiskTotal == nil || m.TcpConns == nil || m.NetRxTotal == nil || m.UptimeS == nil {
		t.Fatalf("sysctl readings must survive: %+v", m)
	}
}
