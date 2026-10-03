package collect

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/xjetry/heron-probe/internal/clock"
)

func cpuinfoProcessors(n int) []byte {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "processor: %d\nmodel name: Synth CPU\n", i)
	}
	return []byte(b.String())
}

// statLines 给聚合 cpu 行补上 n 条逐核行：真实 /proc/stat 的逐核行与宿主核数一致，
// 夹具带上它们才能区分"有效核数"与"宿主核数"两种口径。
func statLines(agg string, n int) []byte {
	var b strings.Builder
	b.WriteString(agg)
	for i := range n {
		fmt.Fprintf(&b, "cpu%d 1 1 1 1 1 1 1 1 0 0\n", i)
	}
	return []byte(b.String())
}

func noDisk(string) (uint64, uint64, error) { return 0, 0, nil }

// 限额夹具：quota 2 核、cpuset 8 核、宿主 16 核，有效核数取较小者 2。
// /proc/stat 带 steal 列（8 个计数器）：走 /proc/stat 路径就会把 steal/iowait 报出来。
func limitedFS() fstest.MapFS {
	return fstest.MapFS{
		"proc/stat":                           {Data: statLines("cpu  100 0 50 800 20 0 10 5 0 0\n", 16)},
		"proc/cpuinfo":                        {Data: cpuinfoProcessors(16)},
		"sys/fs/cgroup/cpu.max":               {Data: []byte("200000 100000\n")},
		"sys/fs/cgroup/cpuset.cpus.effective": {Data: []byte("0-7\n")},
		"sys/fs/cgroup/cpu.stat":              {Data: []byte("usage_usec 1000000\nuser_usec 600000\nsystem_usec 400000\n")},
	}
}

// 有限额时 cpu_pct 是 Δusage_usec / (有效核数 × Δt)，steal/iowait 不设置，Facts 报有效核数。
func TestCgroupLimitedCPUFollowsUsageNotProcStat(t *testing.T) {
	fsys := limitedFS()
	clk := clock.NewFake(time.Unix(0, 0))
	c := &Collector{Host: &ProcFS{FS: fsys, DiskUsage: noDisk}, Clock: clk}
	if m, _ := c.Metrics(); m.CpuPct != nil {
		t.Fatal("first sample must not carry cpu_pct")
	}
	// Δusage = 2000000 µs（两秒跑满一核）；/proc/stat 也动了，若被采用会得出别的值。
	fsys["sys/fs/cgroup/cpu.stat"] = &fstest.MapFile{Data: []byte("usage_usec 3000000\nuser_usec 1600000\nsystem_usec 1400000\n")}
	fsys["proc/stat"] = &fstest.MapFile{Data: statLines("cpu  200 0 100 1600 40 0 20 10 0 0\n", 16)}
	clk.Advance(2 * time.Second)
	m, _ := c.Metrics()
	if m.CpuPct == nil || m.GetCpuPct() != 50 {
		t.Fatalf("cpu_pct set %v = %v, want Δusage 2000000 / (2 cores × 2000000 µs) = 50", m.CpuPct != nil, m.GetCpuPct())
	}
	if m.CpuStealPct != nil || m.CpuIowaitPct != nil {
		t.Fatalf("steal/iowait are host-wide counters, must be unset under a limit: %v/%v", m.GetCpuStealPct(), m.GetCpuIowaitPct())
	}
	if f := c.Facts(); f.GetCpuCores() != 2 {
		t.Fatalf("Facts.cpu_cores = %d, want the effective 2 of min(quota 2, cpuset 8)", f.GetCpuCores())
	}

	// burst：Δusage 超过 有效核数 × Δt，算出 >100 钳到 100（ingest 拒绝 >100）。
	fsys["sys/fs/cgroup/cpu.stat"] = &fstest.MapFile{Data: []byte("usage_usec 13000000\n")}
	clk.Advance(2 * time.Second)
	m, _ = c.Metrics()
	if m.CpuPct == nil || m.GetCpuPct() != 100 {
		t.Fatalf("burst cpu_pct = %v, want clamped 100 (raw 100×10000000/(2×2000000)=250)", m.CpuPct)
	}

	// 用量回退（cgroup 复用）：不设置，只换基线。
	fsys["sys/fs/cgroup/cpu.stat"] = &fstest.MapFile{Data: []byte("usage_usec 500000\n")}
	clk.Advance(2 * time.Second)
	m, _ = c.Metrics()
	if m.CpuPct != nil {
		t.Fatalf("usage regression must leave cpu_pct unset, got %v", m.GetCpuPct())
	}
}

// cpuset 比 quota 更严格时取 cpuset：quota 4 核、cpuset 2 核，有效核数 2。
func TestCgroupEffectiveCoresTakeTheStricterOfQuotaAndCpuset(t *testing.T) {
	fsys := limitedFS()
	fsys["sys/fs/cgroup/cpu.max"] = &fstest.MapFile{Data: []byte("400000 100000\n")}
	fsys["sys/fs/cgroup/cpuset.cpus.effective"] = &fstest.MapFile{Data: []byte("0-1\n")}
	clk := clock.NewFake(time.Unix(0, 0))
	c := &Collector{Host: &ProcFS{FS: fsys, DiskUsage: noDisk}, Clock: clk}
	c.Metrics()
	fsys["sys/fs/cgroup/cpu.stat"] = &fstest.MapFile{Data: []byte("usage_usec 3000000\n")}
	clk.Advance(2 * time.Second)
	m, _ := c.Metrics()
	if m.CpuPct == nil || m.GetCpuPct() != 50 {
		t.Fatalf("cpu_pct set %v = %v, want Δusage 2000000 / (cpuset 2 × 2000000 µs) = 50", m.CpuPct != nil, m.GetCpuPct())
	}
	if f := c.Facts(); f.GetCpuCores() != 2 {
		t.Fatalf("Facts.cpu_cores = %d, want the cpuset 2 stricter than quota 4", f.GetCpuCores())
	}
}

// 无限额（cpu.max 为 max 且 cpuset 覆盖宿主全部核，或 cpuset 文件不存在）时一切照旧：
// cpu_pct 走 /proc/stat 差分，steal/iowait 照报，核数取 cpuinfo。
func TestCgroupUnlimitedKeepsProcStatPath(t *testing.T) {
	for name, cpuset := range map[string]*fstest.MapFile{
		"cpuset covers all": {Data: []byte("0-15\n")},
		"no cpuset file":    nil,
	} {
		fsys := limitedFS()
		fsys["sys/fs/cgroup/cpu.max"] = &fstest.MapFile{Data: []byte("max 100000\n")}
		if cpuset == nil {
			delete(fsys, "sys/fs/cgroup/cpuset.cpus.effective")
		} else {
			fsys["sys/fs/cgroup/cpuset.cpus.effective"] = cpuset
		}
		clk := clock.NewFake(time.Unix(0, 0))
		c := &Collector{Host: &ProcFS{FS: fsys, DiskUsage: noDisk}, Clock: clk}
		c.Metrics()
		fsys["proc/stat"] = &fstest.MapFile{Data: []byte("cpu  135 0 60 850 20 0 10 10 0 0\n")} // Δtotal 100：user 35、system 10、idle 50、steal 5
		clk.Advance(2 * time.Second)
		m, _ := c.Metrics()
		if m.CpuPct == nil || m.GetCpuPct() != 50 {
			t.Fatalf("%s: cpu_pct = %v, want /proc/stat diff 50", name, m.CpuPct)
		}
		if m.CpuStealPct == nil || m.GetCpuStealPct() != 5 {
			t.Fatalf("%s: steal = %v, want 5 from the same /proc/stat diff", name, m.CpuStealPct)
		}
		if f := c.Facts(); f.GetCpuCores() != 16 {
			t.Fatalf("%s: Facts.cpu_cores = %d, want 16 from cpuinfo", name, f.GetCpuCores())
		}
	}
}

// cgroup v1（没有 cpu.max、有 cpu/cpu.cfs_quota_us）：退回 /proc/stat 口径；回退只在状态
// 变化时记一行日志，第二个周期不再记，换回 v2 时再记一行。
func TestCgroupV1FallsBackToProcStatAndLogsOnce(t *testing.T) {
	fsys := fstest.MapFS{
		"proc/stat":                          {Data: []byte("cpu  100 0 50 800 20 0 10 5 0 0\n")},
		"proc/cpuinfo":                       {Data: cpuinfoProcessors(16)},
		"sys/fs/cgroup/cpu/cpu.cfs_quota_us": {Data: []byte("200000\n")},
	}
	clk := clock.NewFake(time.Unix(0, 0))
	var logBuf bytes.Buffer
	c := &Collector{Host: &ProcFS{FS: fsys, DiskUsage: noDisk}, Clock: clk, Log: slog.New(slog.NewTextHandler(&logBuf, nil))}
	c.Metrics()
	fsys["proc/stat"] = &fstest.MapFile{Data: []byte("cpu  135 0 60 850 20 0 10 10 0 0\n")}
	clk.Advance(2 * time.Second)
	m, _ := c.Metrics()
	if m.CpuPct == nil || m.GetCpuPct() != 50 {
		t.Fatalf("v1 must fall back to /proc/stat: cpu_pct = %v, want 50", m.CpuPct)
	}
	lines := strings.Count(strings.TrimRight(logBuf.String(), "\n"), "\n") + 1
	if lines != 1 || !strings.Contains(logBuf.String(), "cgroup v1") {
		t.Fatalf("v1 must be logged exactly once across two periods, got %d lines: %q", lines, logBuf.String())
	}

	// v1 → v2：再记一行；v2 无限额，口径仍是 /proc/stat。
	delete(fsys, "sys/fs/cgroup/cpu/cpu.cfs_quota_us")
	fsys["sys/fs/cgroup/cpu.max"] = &fstest.MapFile{Data: []byte("max 100000\n")}
	clk.Advance(2 * time.Second)
	c.Metrics()
	if got := strings.Count(logBuf.String(), "cgroup v2"); got != 1 {
		t.Fatalf("v1→v2 switch must add one log line, cgroup v2 appears %d times: %q", got, logBuf.String())
	}
}

// 无 cgroup 的环境（连 v1 的标记文件都没有）：走 /proc/stat，不记日志。
func TestNoCgroupKeepsProcStatPath(t *testing.T) {
	fsys := fstest.MapFS{
		"proc/stat":    {Data: []byte("cpu  100 0 50 800 20 0 10 5 0 0\n")},
		"proc/cpuinfo": {Data: cpuinfoProcessors(4)},
	}
	clk := clock.NewFake(time.Unix(0, 0))
	var logBuf bytes.Buffer
	c := &Collector{Host: &ProcFS{FS: fsys, DiskUsage: noDisk}, Clock: clk, Log: slog.New(slog.NewTextHandler(&logBuf, nil))}
	c.Metrics()
	fsys["proc/stat"] = &fstest.MapFile{Data: []byte("cpu  135 0 60 850 20 0 10 10 0 0\n")}
	clk.Advance(2 * time.Second)
	m, _ := c.Metrics()
	if m.CpuPct == nil || m.GetCpuPct() != 50 {
		t.Fatalf("cpu_pct = %v, want /proc/stat diff 50", m.CpuPct)
	}
	if logBuf.Len() != 0 {
		t.Fatalf("no cgroup, no log expected, got %q", logBuf.String())
	}
}

// cpu.max 存在却认不出（存在即应可解析）：CPU 读数整体缺失并报失败，不回退到全机口径。
func TestGarbledCgroupFilesFailTheCPUReading(t *testing.T) {
	fsys := limitedFS()
	fsys["sys/fs/cgroup/cpu.max"] = &fstest.MapFile{Data: []byte("bogus\n")}
	c := &Collector{Host: &ProcFS{FS: fsys, DiskUsage: noDisk}, Clock: clock.NewFake(time.Unix(0, 0))}
	m, err := c.Metrics()
	if m.CpuPct != nil {
		t.Fatalf("garbled cpu.max must not fall back to host-wide cpu_pct, got %v", m.GetCpuPct())
	}
	if err == nil || !strings.Contains(err.Error(), "cpu:") {
		t.Fatalf("err = %v, want the cpu component named", err)
	}
}

// 解析层：cpu.max 的 quota 折核、cpuset 列表计数、cpu.stat 取 usage_usec。
func TestParseCgroupFiles(t *testing.T) {
	if cores, ok, err := parseCPUMax("200000 100000"); err != nil || !ok || cores != 2 {
		t.Fatalf("quota 200000/100000 = %v/%v, %v; want 2 cores", cores, ok, err)
	}
	if cores, ok, err := parseCPUMax("150000 100000"); err != nil || !ok || cores != 1.5 {
		t.Fatalf("quota 150000/100000 = %v/%v, %v; want 1.5 cores", cores, ok, err)
	}
	if _, ok, err := parseCPUMax("max 100000"); err != nil || ok {
		t.Fatalf("max = unlimited %v, %v; want not ok", ok, err)
	}
	for _, s := range []string{"", "200000", "max 0", "0 100000", "x 100000"} {
		if _, _, err := parseCPUMax(s); err == nil {
			t.Fatalf("parseCPUMax(%q) must fail", s)
		}
	}
	for s, want := range map[string]int{"0-15": 16, "0-1": 2, "0": 1, "0-3,8,10-11": 7} {
		if n, err := parseCPUSetCount(s); err != nil || n != want {
			t.Fatalf("parseCPUSetCount(%q) = %d, %v; want %d", s, n, err, want)
		}
	}
	for _, s := range []string{"", "a", "0-", "3-1", "0-1,"} {
		if _, err := parseCPUSetCount(s); err == nil {
			t.Fatalf("parseCPUSetCount(%q) must fail", s)
		}
	}
	if v, err := parseCPUStatUsage(strings.NewReader("usage_usec 14507514\nuser_usec 6709141\n")); err != nil || v != 14507514 {
		t.Fatalf("usage_usec = %d, %v; want 14507514", v, err)
	}
	if _, err := parseCPUStatUsage(strings.NewReader("user_usec 1\n")); err == nil {
		t.Fatal("cpu.stat without usage_usec must fail")
	}
}
