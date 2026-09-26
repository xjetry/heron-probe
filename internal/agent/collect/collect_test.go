package collect

import (
	"os"
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

// 过滤在 Collector 里决定、由 Host 执行：被排除的网卡不进合计，未给 --net-exclude 时用平台默认列表。
func TestExcludedInterfacesAreNotSummed(t *testing.T) {
	fsys := fstest.MapFS{
		"sys/class/net/eth0/statistics/rx_bytes":    {Data: []byte("1000\n")},
		"sys/class/net/eth0/statistics/tx_bytes":    {Data: []byte("2000\n")},
		"sys/class/net/lo/statistics/rx_bytes":      {Data: []byte("50000\n")},
		"sys/class/net/lo/statistics/tx_bytes":      {Data: []byte("50000\n")},
		"sys/class/net/docker0/statistics/rx_bytes": {Data: []byte("70000\n")},
		"sys/class/net/docker0/statistics/tx_bytes": {Data: []byte("70000\n")},
	}
	c := &Collector{Host: &ProcFS{FS: fsys, DiskUsage: func(string) (uint64, uint64, error) { return 0, 0, nil }}, Clock: clock.NewFake(time.Unix(0, 0))}
	if m, _ := c.Metrics(); m.GetNetRxTotal() != 1000 || m.GetNetTxTotal() != 2000 {
		t.Fatalf("net = %d/%d, want eth0 only", m.GetNetRxTotal(), m.GetNetTxTotal())
	}
	c = &Collector{Host: c.Host, Clock: c.Clock, NetExclude: []string{"eth*"}}
	if m, _ := c.Metrics(); m.GetNetRxTotal() != 120000 {
		t.Fatalf("explicit exclude list replaces the default: rx = %d, want lo + docker0", m.GetNetRxTotal())
	}
}
