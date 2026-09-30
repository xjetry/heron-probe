package collect

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/clock"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func fixture(t *testing.T) *Collector {
	t.Helper()
	return &Collector{
		Host:    &ProcFS{FS: os.DirFS("testdata/docker-debian"), DiskUsage: func(string) (uint64, uint64, error) { return 1000, 400, nil }},
		Clock:   clock.NewFake(time.Unix(0, 0)),
		Version: "test",
	}
}

func TestMetricsFromRealProcSnapshot(t *testing.T) {
	c := fixture(t)
	m, err := c.Metrics()
	if err != nil {
		t.Fatalf("unexpected read failures: %v", err)
	}
	if len(m.GetBootId()) != 36 {
		t.Fatalf("boot_id %q", m.GetBootId())
	}
	if m.CpuPct != nil {
		t.Fatal("first sample has no previous /proc/stat to diff against; cpu_pct must be absent")
	}
	if m.MemTotal == nil || m.GetMemTotal() == 0 || m.MemUsed == nil {
		t.Fatalf("mem: %+v", m)
	}
	if m.Load1 == nil || m.Load5 == nil || m.Load15 == nil {
		t.Fatal("load triple missing")
	}
	if m.Procs == nil || m.GetProcs() == 0 || m.UptimeS == nil || m.GetUptimeS() == 0 {
		t.Fatalf("procs/uptime: %+v", m)
	}
	if m.TcpConns == nil || m.UdpConns == nil {
		t.Fatal("conn counts missing")
	}
	if m.GetDiskTotal() != 1000 || m.GetDiskUsed() != 400 {
		t.Fatalf("disk: %+v", m)
	}
	if m.NetRxTotal == nil {
		t.Fatal("net counters missing (eth0 should be included, lo excluded)")
	}
}

func TestCPUPercentAppearsOnSecondSample(t *testing.T) {
	fsys := fstest.MapFS{
		"proc/stat": {Data: []byte("cpu  100 0 50 800 20 0 10 0 0 0\n")},
	}
	c := &Collector{Host: &ProcFS{FS: fsys, DiskUsage: func(string) (uint64, uint64, error) { return 0, 0, nil }}, Clock: clock.NewFake(time.Unix(0, 0))}
	if m, _ := c.Metrics(); m.CpuPct != nil {
		t.Fatal("first sample must not carry cpu_pct")
	}
	fsys["proc/stat"] = &fstest.MapFile{Data: []byte("cpu  150 0 50 850 20 0 10 0 0 0\n")} // +100 tick，其中 50 空闲
	m, _ := c.Metrics()
	if m.CpuPct == nil || m.GetCpuPct() != 50 {
		t.Fatalf("cpu_pct set %v = %v, want 50", m.CpuPct != nil, m.GetCpuPct())
	}
}

func TestNetRateNeedsTwoSamples(t *testing.T) {
	clk := clock.NewFake(time.Unix(0, 0))
	fsys := fstest.MapFS{
		"sys/class/net/eth0/statistics/rx_bytes": {Data: []byte("1000\n")},
		"sys/class/net/eth0/statistics/tx_bytes": {Data: []byte("2000\n")},
	}
	c := &Collector{Host: &ProcFS{FS: fsys, DiskUsage: func(string) (uint64, uint64, error) { return 0, 0, nil }}, Clock: clk}
	m, _ := c.Metrics()
	if m.GetNetRxTotal() != 1000 || m.NetRxBps != nil {
		t.Fatalf("first: %+v", m)
	}
	fsys["sys/class/net/eth0/statistics/rx_bytes"] = &fstest.MapFile{Data: []byte("3000\n")}
	clk.Advance(2 * time.Second)
	m, _ = c.Metrics()
	if m.GetNetRxBps() != 1000 {
		t.Fatalf("rx_bps = %d, want (3000-1000)/2s = 1000", m.GetNetRxBps())
	}
}

func TestMissingFilesYieldMissingReadingsNotZero(t *testing.T) {
	c := &Collector{Host: &ProcFS{FS: fstest.MapFS{}, DiskUsage: func(string) (uint64, uint64, error) { return 0, 0, os.ErrNotExist }}, Clock: clock.NewFake(time.Unix(0, 0))}
	m, err := c.Metrics()
	if err == nil {
		t.Fatal("read failures must be reported for logging")
	}
	if m.MemTotal != nil || m.Load1 != nil || m.DiskTotal != nil || m.Procs != nil || m.NetRxTotal != nil {
		t.Fatalf("missing inputs must leave readings unset, got %+v", m)
	}
}

func TestFactsFromRealProcSnapshot(t *testing.T) {
	f := fixture(t).Facts()
	if f.GetHostname() == "" || f.GetKernel() == "" || f.GetOs() == "" || f.GetCpuCores() == 0 || f.GetArch() == "" {
		t.Fatalf("%+v", f)
	}
	if f.GetAgentVersion() != "test" || f.GetIcmpAvailable() {
		t.Fatalf("%+v", f)
	}
}

// 不一致的计数、回绕的减法都可能给出 used > total；这样的读数按读不到处理，不截断成满载。
// 内存、swap、磁盘三处各有一个守卫，各自一条断言。
func TestUsageAboveTotalIsDroppedNotClamped(t *testing.T) {
	fsys := fstest.MapFS{
		"proc/meminfo": {Data: []byte("MemTotal: 1000 kB\nMemAvailable: 2000 kB\nSwapTotal: 10 kB\nSwapFree: 4 kB\n")},
	}
	c := &Collector{Host: &ProcFS{FS: fsys, DiskUsage: func(string) (uint64, uint64, error) { return 100, 101, nil }}, Clock: clock.NewFake(time.Unix(0, 0))}
	m, err := c.Metrics()
	if m.MemTotal != nil || m.MemUsed != nil || m.DiskTotal != nil || m.DiskUsed != nil {
		t.Fatalf("used above total must leave both readings unset, got mem %d/%d disk %d/%d", m.GetMemUsed(), m.GetMemTotal(), m.GetDiskUsed(), m.GetDiskTotal())
	}
	if err == nil || !strings.Contains(err.Error(), "memory: used") || !strings.Contains(err.Error(), "disk: used 101 exceeds total 100") {
		t.Fatalf("err = %v, want both rejections named", err)
	}
	if m.GetSwapTotal() != 10*1024 || m.GetSwapUsed() != 6*1024 {
		t.Fatalf("swap from the same file must be unaffected: %d/%d", m.GetSwapUsed(), m.GetSwapTotal())
	}

	fsys["proc/meminfo"] = &fstest.MapFile{Data: []byte("MemTotal: 1000 kB\nMemAvailable: 400 kB\nSwapTotal: 10 kB\nSwapFree: 12 kB\n")}
	m, err = c.Metrics()
	if m.SwapTotal != nil || m.SwapUsed != nil {
		t.Fatalf("swap used above total must leave both readings unset, got %d/%d", m.GetSwapUsed(), m.GetSwapTotal())
	}
	if err == nil || !strings.Contains(err.Error(), "swap: used") {
		t.Fatalf("err = %v, want the swap rejection named", err)
	}
	if m.GetMemTotal() != 1000*1024 || m.GetMemUsed() != 600*1024 {
		t.Fatalf("memory from the same file must be unaffected: %d/%d", m.GetMemUsed(), m.GetMemTotal())
	}
}

// ifacesOnly 是只给网卡读数的 Host，列出什么就原样返回什么，不做任何预过滤：
// 网卡过滤只由 Collector 承载，换一个平台实现也不会把被排除的网卡带进合计。
type ifacesOnly struct{ list []ifaceCounters }

var errNoReading = errors.New("no reading")

func (h *ifacesOnly) bootID() (string, error)          { return "", errNoReading }
func (h *ifacesOnly) cpuTimes() (cpuTimes, error)      { return cpuTimes{}, errNoReading }
func (h *ifacesOnly) memory() (usage, error)           { return usage{}, errNoReading }
func (h *ifacesOnly) swap() (usage, error)             { return usage{}, errNoReading }
func (h *ifacesOnly) disk() (usage, error)             { return usage{}, errNoReading }
func (h *ifacesOnly) load() (loadAvg, error)           { return loadAvg{}, errNoReading }
func (h *ifacesOnly) procs() (uint32, error)           { return 0, errNoReading }
func (h *ifacesOnly) uptime() (uint64, error)          { return 0, errNoReading }
func (h *ifacesOnly) conns() (uint32, uint32, error)   { return 0, 0, errNoReading }
func (h *ifacesOnly) ifaces() ([]ifaceCounters, error) { return h.list, nil }
func (h *ifacesOnly) defaultNetExclude() []string      { return []string{"lo", "docker*"} }
func (h *ifacesOnly) facts() hostFacts                 { return hostFacts{} }

// 被排除的网卡不进合计，未给 --net-exclude 时用 Host 的默认列表，给了就整个替换默认列表。
func TestExcludedInterfacesAreNotSummed(t *testing.T) {
	h := &ifacesOnly{list: []ifaceCounters{{"eth0", 1000, 2000}, {"lo", 50000, 50000}, {"docker0", 70000, 70000}}}
	c := &Collector{Host: h, Clock: clock.NewFake(time.Unix(0, 0))}
	if m, _ := c.Metrics(); m.GetNetRxTotal() != 1000 || m.GetNetTxTotal() != 2000 {
		t.Fatalf("net = %d/%d, want eth0 only", m.GetNetRxTotal(), m.GetNetTxTotal())
	}
	c = &Collector{Host: h, Clock: c.Clock, NetExclude: []string{"eth*"}}
	if m, _ := c.Metrics(); m.GetNetRxTotal() != 120000 {
		t.Fatalf("explicit exclude list replaces the default: rx = %d, want lo + docker0", m.GetNetRxTotal())
	}
	c = &Collector{Host: h, Clock: c.Clock, NetInclude: []string{"docker*"}}
	if m, _ := c.Metrics(); m.GetNetRxTotal() != 70000 {
		t.Fatalf("include list is exclusive: rx = %d, want docker0 only", m.GetNetRxTotal())
	}
}

// failingFS 让指定文件的读取失败，其余照常：表示文件存在、内容读不出。
// failingFS 让 name 能打开、读时报 err，其余路径照常：文件还在却读不出。
type failingFS struct {
	fs.FS
	name string
	err  error
}

type failingFile struct {
	fs.File
	err error
}

func (f failingFile) Read([]byte) (int, error) { return 0, f.err }

func (f failingFS) Open(name string) (fs.File, error) {
	file, err := f.FS.Open(name)
	if err != nil || name != f.name {
		return file, err
	}
	return failingFile{file, f.err}, nil
}

// vanishingFS 模拟网卡在列出之后被删除：读 name 时报 err，此后 dir 下的一切都不存在。
// 与实测的 sysfs 一致（alpine 容器里删 dummy 网卡）：删除后打开计数文件报 ENOENT，
// 删除前已打开的文件读时报 ENODEV，网卡目录随之消失。
type vanishingFS struct {
	fs.FS
	name, dir string
	err       error
	gone      *bool
}

func (v vanishingFS) Open(name string) (fs.File, error) {
	if *v.gone && (name == v.dir || strings.HasPrefix(name, v.dir+"/")) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	if name != v.name {
		return v.FS.Open(name)
	}
	*v.gone = true
	if errors.Is(v.err, fs.ErrNotExist) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: v.err}
	}
	file, err := v.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return failingFile{file, v.err}, nil
}

const (
	sockstat4 = "sockets: used 50\nTCP: inuse 11 orphan 0 tw 3 alloc 12 mem 1\nUDP: inuse 13 mem 2\n"
	sockstat6 = "TCP6: inuse 17\nUDP6: inuse 19\n"
)

// 合计型读数：一张表不存在即不计；表存在却读不出或认不出时，整个 conns 缺失并记日志，
// 不上报缺一族的合计（spec §4.2 读不到即缺失）。
func TestConnsAreCorrectOrMissing(t *testing.T) {
	only4 := fstest.MapFS{"proc/net/sockstat": {Data: []byte(sockstat4)}}
	if tcp, udp, err := (&ProcFS{FS: only4}).conns(); err != nil || tcp != 11 || udp != 13 {
		t.Fatalf("without sockstat6 (ipv6 disabled): %d/%d, %v; want the IPv4 table alone", tcp, udp, err)
	}
	both := fstest.MapFS{"proc/net/sockstat": {Data: []byte(sockstat4)}, "proc/net/sockstat6": {Data: []byte(sockstat6)}}
	if tcp, udp, err := (&ProcFS{FS: both}).conns(); err != nil || tcp != 28 || udp != 32 {
		t.Fatalf("both tables: %d/%d, %v; want 28/32", tcp, udp, err)
	}
	for name, fsys := range map[string]fs.FS{
		"unreadable sockstat":   failingFS{both, "proc/net/sockstat", fs.ErrPermission},
		"unrecognized sockstat": fstest.MapFS{"proc/net/sockstat": {Data: []byte("sockets: used 50\n")}, "proc/net/sockstat6": {Data: []byte(sockstat6)}},
		"sockstat without UDP":  fstest.MapFS{"proc/net/sockstat": {Data: []byte("TCP: inuse 11\n")}, "proc/net/sockstat6": {Data: []byte(sockstat6)}},
	} {
		c := &Collector{Host: &ProcFS{FS: fsys, DiskUsage: func(string) (uint64, uint64, error) { return 0, 0, nil }}, Clock: clock.NewFake(time.Unix(0, 0))}
		m, err := c.Metrics()
		if m.TcpConns != nil || m.UdpConns != nil {
			t.Errorf("%s: conns = %d/%d, want both missing rather than the IPv6 part alone", name, m.GetTcpConns(), m.GetUdpConns())
		}
		if err == nil || !strings.Contains(err.Error(), "conns: proc/net/sockstat:") {
			t.Errorf("%s: err = %v, want the failing table named", name, err)
		}
	}
	if _, _, err := (&ProcFS{FS: fstest.MapFS{}}).conns(); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("no table at all: err = %v, want fs.ErrNotExist", err)
	}
}

// /sys/class/net 里没有 statistics 目录的条目（bonding_masters 这类文件）不是网卡，跳过；
// ifaces 不返回仅含可读网卡的不完整快照；Collector.Metrics 将计数读取失败保留为缺读数，而非网卡集合变化。
func TestInterfaceReadErrorDropsTheWholeReadingOnLinux(t *testing.T) {
	fsys := fstest.MapFS{
		"sys/class/net/bonding_masters":          {Data: []byte("\n")},
		"sys/class/net/eth0/statistics/rx_bytes": {Data: []byte("1000\n")},
		"sys/class/net/eth0/statistics/tx_bytes": {Data: []byte("2000\n")},
		"sys/class/net/eth1/statistics/rx_bytes": {Data: []byte("3000\n")},
		"sys/class/net/eth1/statistics/tx_bytes": {Data: []byte("4000\n")},
	}
	ifs, err := (&ProcFS{FS: fsys}).ifaces()
	if err != nil || len(ifs) != 2 {
		t.Fatalf("ifaces = %v, %v; want eth0 and eth1, bonding_masters skipped", ifs, err)
	}
	// 同样的形状落在真实目录上：普通文件下的 statistics 由 os.DirFS 报 ENOTDIR，不是 ENOENT。
	root := t.TempDir()
	for name, f := range fsys {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, f.Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if ifs, err := (&ProcFS{FS: os.DirFS(root)}).ifaces(); err != nil || len(ifs) != 2 {
		t.Fatalf("on a real directory: ifaces = %v, %v; want eth0 and eth1", ifs, err)
	}
	// 网卡目录还在、计数读不出（EACCES 之类）：不是网卡消失，整个流量读数缺失。
	c := &Collector{Host: &ProcFS{FS: failingFS{fsys, "sys/class/net/eth1/statistics/rx_bytes", fs.ErrPermission}, DiskUsage: func(string) (uint64, uint64, error) { return 0, 0, nil }}, Clock: clock.NewFake(time.Unix(0, 0))}
	m, err := c.Metrics()
	if m.NetRxTotal != nil || m.NetTxTotal != nil {
		t.Fatalf("partial interface set must not be reported: rx %d tx %d", m.GetNetRxTotal(), m.GetNetTxTotal())
	}
	if err == nil || !strings.Contains(err.Error(), "net: eth1:") {
		t.Fatalf("err = %v, want the failing interface named", err)
	}
}

// 网卡在列出之后被删除（veth、tun 随容器与 VPN 来去）：只略过它，其余网卡照常合计。
// 两个错误码对应删除落在打开之前与打开之后两个窗口；rx 与 tx 两次读取各自都可能撞上。
func TestVanishedInterfaceIsSkippedOnLinux(t *testing.T) {
	fsys := fstest.MapFS{
		"sys/class/net/eth0/statistics/rx_bytes": {Data: []byte("1000\n")},
		"sys/class/net/eth0/statistics/tx_bytes": {Data: []byte("2000\n")},
		"sys/class/net/eth1/statistics/rx_bytes": {Data: []byte("3000\n")},
		"sys/class/net/eth1/statistics/tx_bytes": {Data: []byte("4000\n")},
	}
	for _, file := range []string{"rx_bytes", "tx_bytes"} {
		for _, readErr := range []error{fs.ErrNotExist, syscall.ENODEV} {
			gone := false
			v := vanishingFS{fsys, "sys/class/net/eth1/statistics/" + file, "sys/class/net/eth1", readErr, &gone}
			ifs, err := (&ProcFS{FS: v}).ifaces()
			if err != nil || len(ifs) != 1 || ifs[0].name != "eth0" || ifs[0].rx != 1000 || ifs[0].tx != 2000 {
				t.Fatalf("%s fails with %v and eth1 is gone: ifaces = %v, %v; want eth0 alone", file, readErr, ifs, err)
			}
		}
	}
}

// 进程数是 /proc 下的进程目录数：线程不列在 /proc 下，loadavg 第 4 字段的调度实体数不用（spec §7）。
func TestProcsCountsProcessDirectoriesNotSchedulingEntities(t *testing.T) {
	fsys := fstest.MapFS{
		"proc/loadavg":      {Data: []byte("0.10 0.20 0.30 3/900 77\n")},
		"proc/1/comm":       {Data: []byte("init\n")},
		"proc/42/comm":      {Data: []byte("sshd\n")},
		"proc/4242/comm":    {Data: []byte("heron-agent\n")},
		"proc/self":         {Data: []byte("4242")},
		"proc/sys/kernel/x": {Data: []byte("")},
		"proc/1a/comm":      {Data: []byte("")},
	}
	n, err := (&ProcFS{FS: fsys}).procs()
	if err != nil || n != 3 {
		t.Fatalf("procs = %d, %v; want 3 process directories", n, err)
	}
	// 没有 PID 1 的进程目录，列表就不是完整的 procfs：不论是只有别的文件，还是 1 不是目录。
	for name, fsys := range map[string]fstest.MapFS{
		"no process directories": {"proc/loadavg": {Data: []byte("0 0 0 1/2 3\n")}},
		"1 is not a directory":   {"proc/1": {Data: []byte("")}, "proc/42/comm": {Data: []byte("sshd\n")}},
	} {
		if n, err := (&ProcFS{FS: fsys}).procs(); err == nil {
			t.Errorf("%s: procs = %d, want an error for a /proc without PID 1's directory", name, n)
		}
	}
}

// protoDiff 按字段描述逐个比较两条消息，含"有无"（optional 字段），列出不同的字段名与两边的值。
// 字段表来自消息描述符，proto 新增的字段自动进入比较。
func protoDiff(got, want proto.Message) []string {
	g, w := got.ProtoReflect(), want.ProtoReflect()
	fields := g.Descriptor().Fields()
	var diff []string
	for i := range fields.Len() {
		fd := fields.Get(i)
		if g.Has(fd) == w.Has(fd) && g.Get(fd).Equal(w.Get(fd)) {
			continue
		}
		show := func(m protoreflect.Message) string {
			if !m.Has(fd) {
				return "unset"
			}
			return m.Get(fd).String()
		}
		diff = append(diff, fmt.Sprintf("%s: got %s, want %s", fd.Name(), show(g), show(w)))
	}
	return diff
}

// Linux 上报值逐字段钉住：期望值由快照文件算出，写成算式，读者能对着 testdata/docker-debian 核对。
func TestGoldenMetricsFromRealProcSnapshot(t *testing.T) {
	m, err := fixture(t).Metrics()
	if err != nil {
		t.Fatalf("unexpected read failures: %v", err)
	}
	want := &heronv1.Metrics{
		BootId:          "319b05cd-78d2-479e-b8ef-4c2478582043",     // proc/sys/kernel/random/boot_id
		MemTotal:        proto.Uint64(16424476 * 1024),              // meminfo MemTotal
		MemUsed:         proto.Uint64((16424476 - 14608052) * 1024), // MemTotal − MemAvailable
		SwapTotal:       proto.Uint64(17473044 * 1024),
		SwapUsed:        proto.Uint64(0), // SwapTotal − SwapFree
		DiskTotal:       proto.Uint64(1000),
		DiskUsed:        proto.Uint64(400),
		Load1:           proto.Float64(0.27),
		Load5:           proto.Float64(0.50),
		Load15:          proto.Float64(0.46),
		Procs:           proto.Uint32(1), // 进程目录只有 proc/1
		UptimeS:         proto.Uint64(202088),
		TcpConns:        proto.Uint32(0),
		UdpConns:        proto.Uint32(0),
		NetRxTotal:      proto.Uint64(110), // 只有 eth0，lo 被默认排除
		NetTxTotal:      proto.Uint64(42),
		NetCounterEpoch: "fd943bfeaf4f69406c92467340e6009c5a065c5453af9acebbd8c9af3a66eeb8", // SHA-256(["eth0"])
	}
	if d := protoDiff(m, want); d != nil {
		t.Fatalf("metrics differ from the snapshot:\n%s", strings.Join(d, "\n"))
	}
	f := fixture(t).Facts()
	wantFacts := &heronv1.Facts{
		Hostname: "fa6437c2745e", Os: "Debian GNU/Linux 12 (bookworm)", Kernel: "7.0.14-orbstack-00380-ga7e0a2dc9535",
		Arch: runtime.GOARCH, CpuCores: 16, AgentVersion: "test",
	}
	if d := protoDiff(f, wantFacts); d != nil {
		t.Fatalf("facts differ from the snapshot:\n%s", strings.Join(d, "\n"))
	}
}

// 合成快照的每个字段取互不相同的非零值：字段对调、公式写错都得不出期望值。采两次，覆盖差分出的读数。
func TestGoldenMetricsFromSyntheticSnapshot(t *testing.T) {
	fsys := fstest.MapFS{
		"proc/sys/kernel/random/boot_id":         {Data: []byte("0b7c3a1e-5d2f-4e6a-9c8b-1a2b3c4d5e6f\n")},
		"proc/stat":                              {Data: []byte("cpu  100 20 50 800 30 5 7 3 0 0\ncpu0 1 1 1 1 1 1 1 1 0 0\n")},
		"proc/meminfo":                           {Data: []byte("MemTotal: 8000 kB\nMemFree: 1000 kB\nMemAvailable: 3000 kB\nSwapTotal: 4000 kB\nSwapFree: 1500 kB\n")},
		"proc/loadavg":                           {Data: []byte("1.25 2.5 3.75 4/555 99\n")},
		"proc/uptime":                            {Data: []byte("4321.99 100.00\n")},
		"proc/net/sockstat":                      {Data: []byte(sockstat4)},
		"proc/net/sockstat6":                     {Data: []byte(sockstat6)},
		"proc/1/comm":                            {Data: []byte("init\n")},
		"proc/42/comm":                           {Data: []byte("sshd\n")},
		"proc/99/comm":                           {Data: []byte("heron-agent\n")},
		"proc/sys/kernel/hostname":               {Data: []byte("synth\n")},
		"proc/sys/kernel/osrelease":              {Data: []byte("6.1.0-synth\n")},
		"proc/cpuinfo":                           {Data: []byte("processor: 0\nmodel name: Synth CPU\nprocessor: 1\nmodel name: Synth CPU\nprocessor: 2\nmodel name: Synth CPU\n")},
		"proc/1/environ":                         {Data: []byte("PATH=/bin\x00container=lxc\x00")},
		"etc/os-release":                         {Data: []byte("NAME=Synth\nPRETTY_NAME=\"Synth Linux 1\"\n")},
		"sys/class/net/eth0/statistics/rx_bytes": {Data: []byte("1000\n")},
		"sys/class/net/eth0/statistics/tx_bytes": {Data: []byte("3000\n")},
		"sys/class/net/lo/statistics/rx_bytes":   {Data: []byte("700\n")},
		"sys/class/net/lo/statistics/tx_bytes":   {Data: []byte("800\n")},
	}
	clk := clock.NewFake(time.Unix(0, 0))
	c := &Collector{Host: &ProcFS{FS: fsys, DiskUsage: func(string) (uint64, uint64, error) { return 9000, 1234, nil }}, Clock: clk, Version: "v9", IcmpAvailable: true}
	if _, err := c.Metrics(); err != nil {
		t.Fatalf("first sample: %v", err)
	}
	// 各状态增量：user 80、nice 10、system 40、idle 50、iowait 10、irq 5、softirq 3、steal 2，合计 200，空闲 60。
	fsys["proc/stat"] = &fstest.MapFile{Data: []byte("cpu  180 30 90 850 40 10 10 5 0 0\n")}
	fsys["sys/class/net/eth0/statistics/rx_bytes"] = &fstest.MapFile{Data: []byte("5000\n")}
	fsys["sys/class/net/eth0/statistics/tx_bytes"] = &fstest.MapFile{Data: []byte("4000\n")}
	clk.Advance(2 * time.Second)
	m, err := c.Metrics()
	if err != nil {
		t.Fatalf("second sample: %v", err)
	}
	want := &heronv1.Metrics{
		BootId:          "0b7c3a1e-5d2f-4e6a-9c8b-1a2b3c4d5e6f",
		CpuPct:          proto.Float64(100 * (1 - 60.0/200)),
		MemTotal:        proto.Uint64(8000 * 1024),
		MemUsed:         proto.Uint64((8000 - 3000) * 1024),
		SwapTotal:       proto.Uint64(4000 * 1024),
		SwapUsed:        proto.Uint64((4000 - 1500) * 1024),
		DiskTotal:       proto.Uint64(9000),
		DiskUsed:        proto.Uint64(1234),
		Load1:           proto.Float64(1.25),
		Load5:           proto.Float64(2.5),
		Load15:          proto.Float64(3.75),
		Procs:           proto.Uint32(3),
		UptimeS:         proto.Uint64(4321),
		TcpConns:        proto.Uint32(11 + 17),
		UdpConns:        proto.Uint32(13 + 19),
		NetRxTotal:      proto.Uint64(5000),
		NetTxTotal:      proto.Uint64(4000),
		NetRxBps:        proto.Uint64((5000 - 1000) / 2),
		NetTxBps:        proto.Uint64((4000 - 3000) / 2),
		NetCounterEpoch: "fd943bfeaf4f69406c92467340e6009c5a065c5453af9acebbd8c9af3a66eeb8",
	}
	if d := protoDiff(m, want); d != nil {
		t.Fatalf("metrics differ:\n%s", strings.Join(d, "\n"))
	}
	wantFacts := &heronv1.Facts{
		Hostname: "synth", Os: "Synth Linux 1", Kernel: "6.1.0-synth", Arch: runtime.GOARCH, Virtualization: "lxc",
		CpuModel: "Synth CPU", CpuCores: 3, AgentVersion: "v9", IcmpAvailable: true,
		Diagnostics: &heronv1.AgentDiagnostics{NetExclude: slices.Clone(linuxNetExclude), NetInterfaces: []string{"eth0"}, NetInterfacesTotal: 1},
	}
	if d := protoDiff(c.Facts(), wantFacts); d != nil {
		t.Fatalf("facts differ:\n%s", strings.Join(d, "\n"))
	}
}

// /proc 以 hidepid=2 挂载时非 root 的 agent 只看得到自己的进程：按目录数会静默错成个位数。
// PID 1 在任何 pid 命名空间里都存在，看不到它就说明列表不全，进程数缺失并记日志。
func TestProcsMissingWhenOtherProcessesAreHidden(t *testing.T) {
	fsys := fstest.MapFS{
		"proc/loadavg":   {Data: []byte("0.10 0.20 0.30 3/900 77\n")},
		"proc/4242/comm": {Data: []byte("heron-agent\n")},
		"proc/4243/comm": {Data: []byte("heron-agent\n")},
	}
	c := &Collector{Host: &ProcFS{FS: fsys, DiskUsage: func(string) (uint64, uint64, error) { return 0, 0, nil }}, Clock: clock.NewFake(time.Unix(0, 0))}
	m, err := c.Metrics()
	if m.Procs != nil {
		t.Fatalf("procs = %d from a /proc that hides PID 1, want missing", m.GetProcs())
	}
	if err == nil || !strings.Contains(err.Error(), "procs: proc/1 is not visible") {
		t.Fatalf("err = %v, want the hidden PID 1 named", err)
	}
}
