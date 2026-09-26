package collect

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/xjetry/probe/internal/clock"
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
		t.Fatalf("cpu_pct = %v", m.CpuPct)
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

// used > total 只能来自不一致的计数或回绕的减法：按读不到处理，不截断成满载。
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
		t.Fatalf("swap from the same file must be unaffected: %v/%v", m.SwapTotal, m.SwapUsed)
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
type failingFS struct {
	fs.FS
	name string
}

type failingFile struct{ fs.File }

func (failingFile) Read([]byte) (int, error) { return 0, errors.New("injected read error") }

func (f failingFS) Open(name string) (fs.File, error) {
	file, err := f.FS.Open(name)
	if err != nil || name != f.name {
		return file, err
	}
	return failingFile{file}, nil
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
		"unreadable sockstat":   failingFS{both, "proc/net/sockstat"},
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
// 真网卡的计数读不出时整个网络读数缺失：少一块的合计会让 hub 只换基线，恢复时把那块的历史计数当增量（spec §7）。
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
	c := &Collector{Host: &ProcFS{FS: failingFS{fsys, "sys/class/net/eth1/statistics/rx_bytes"}, DiskUsage: func(string) (uint64, uint64, error) { return 0, 0, nil }}, Clock: clock.NewFake(time.Unix(0, 0))}
	m, err := c.Metrics()
	if m.NetRxTotal != nil || m.NetTxTotal != nil {
		t.Fatalf("partial interface set must not be reported: rx %d tx %d", m.GetNetRxTotal(), m.GetNetTxTotal())
	}
	if err == nil || !strings.Contains(err.Error(), "net: eth1:") {
		t.Fatalf("err = %v, want the failing interface named", err)
	}
}

// 进程数是 /proc 下的进程目录数：线程不列在 /proc 下，loadavg 第 4 字段的调度实体数不用（spec §7）。
func TestProcsCountsProcessDirectoriesNotSchedulingEntities(t *testing.T) {
	fsys := fstest.MapFS{
		"proc/loadavg":      {Data: []byte("0.10 0.20 0.30 3/900 77\n")},
		"proc/1/comm":       {Data: []byte("init\n")},
		"proc/42/comm":      {Data: []byte("sshd\n")},
		"proc/4242/comm":    {Data: []byte("probe-agent\n")},
		"proc/self":         {Data: []byte("4242")},
		"proc/sys/kernel/x": {Data: []byte("")},
		"proc/1a/comm":      {Data: []byte("")},
	}
	n, err := (&ProcFS{FS: fsys}).procs()
	if err != nil || n != 3 {
		t.Fatalf("procs = %d, %v; want 3 process directories", n, err)
	}
	if _, err := (&ProcFS{FS: fstest.MapFS{"proc/loadavg": {Data: []byte("0 0 0 1/2 3\n")}}}).procs(); err == nil {
		t.Fatal("a /proc without process directories must be an error, not 0 processes")
	}
}
