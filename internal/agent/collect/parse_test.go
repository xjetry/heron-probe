package collect

import (
	"strings"
	"testing"
)

func TestParseStatCPUTimes(t *testing.T) {
	in := "cpu  100 0 50 800 20 0 10 0 0 0\ncpu0 1 2 3 4 5 6 7 8 9 10\n"
	c, err := parseStat(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	// idle = idle + iowait = 820；total = 100+0+50+800+20+0+10+0 = 980
	if c.idle != 820 || c.total != 980 {
		t.Fatalf("idle/total = %d/%d, want 820/980", c.idle, c.total)
	}
}

func TestCPUPercentFromTwoSamples(t *testing.T) {
	a := cpuTimes{idle: 800, total: 1000}
	b := cpuTimes{idle: 850, total: 1100} // 100 个 tick 里 50 个空闲 → 50%
	got, ok := cpuPercent(a, b)
	if !ok || got != 50 {
		t.Fatalf("cpu%% = %v,%v", got, ok)
	}
	if _, ok := cpuPercent(b, b); ok {
		t.Fatal("no elapsed ticks must yield no reading, not 0%")
	}
}

func TestParseMeminfoKBToBytes(t *testing.T) {
	in := "MemTotal:       2048 kB\nMemFree:        100 kB\nMemAvailable:   1024 kB\nSwapTotal:      512 kB\nSwapFree:       256 kB\n"
	m, err := parseMeminfo(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if m.total != 2048*1024 || m.available != 1024*1024 || m.swapTotal != 512*1024 || m.swapFree != 256*1024 {
		t.Fatalf("%+v", m)
	}
}

func TestParseLoadavg(t *testing.T) {
	l, err := parseLoadavg(strings.NewReader("0.52 0.31 0.20 3/721 12345\n"))
	if err != nil {
		t.Fatal(err)
	}
	if l.l1 != 0.52 || l.l5 != 0.31 || l.l15 != 0.20 || l.procs != 721 {
		t.Fatalf("%+v", l)
	}
}

func TestParseNetDev(t *testing.T) {
	in := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:     100     1    0    0    0     0          0         0      100     1    0    0    0     0       0          0
  eth0:    5000    10    0    0    0     0          0         0     7000    12    0    0    0     0       0          0
`
	m, err := parseNetDev(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if m["eth0"].rx != 5000 || m["eth0"].tx != 7000 || m["lo"].rx != 100 {
		t.Fatalf("%+v", m)
	}
}

func TestParseSockstatBothFamilies(t *testing.T) {
	tcp, udp, err := parseSockstat(strings.NewReader("sockets: used 5\nTCP: inuse 3 orphan 0 tw 0 alloc 1 mem 0\nUDP: inuse 2 mem 1\n"))
	if err != nil || tcp != 3 || udp != 2 {
		t.Fatalf("v4: %d %d %v", tcp, udp, err)
	}
	tcp, udp, err = parseSockstat(strings.NewReader("TCP6: inuse 4\nUDP6: inuse 1\nUDPLITE6: inuse 0\n"))
	if err != nil || tcp != 4 || udp != 1 {
		t.Fatalf("v6: %d %d %v", tcp, udp, err)
	}
}

func TestParseCPUInfo(t *testing.T) {
	in := "processor\t: 0\nmodel name\t: Fancy CPU @ 3.0GHz\n\nprocessor\t: 1\nmodel name\t: Fancy CPU @ 3.0GHz\n"
	c := parseCPUInfo(strings.NewReader(in))
	if c.model != "Fancy CPU @ 3.0GHz" || c.cores != 2 {
		t.Fatalf("%+v", c)
	}
}

func TestParseOSReleasePrettyName(t *testing.T) {
	in := "NAME=\"Debian GNU/Linux\"\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nID=debian\n"
	if got := parseOSRelease(strings.NewReader(in)); got != "Debian GNU/Linux 12 (bookworm)" {
		t.Fatalf("%q", got)
	}
}

func TestInterfaceFilterDefaults(t *testing.T) {
	c := &Collector{}
	for _, n := range []string{"lo", "docker0", "veth1234", "br-abc", "virbr0"} {
		if c.includeIface(n) {
			t.Fatalf("%s must be excluded by default", n)
		}
	}
	for _, n := range []string{"eth0", "ens3", "wlan0"} {
		if !c.includeIface(n) {
			t.Fatalf("%s must be included by default", n)
		}
	}
	c = &Collector{NetInclude: []string{"eth*"}}
	if c.includeIface("ens3") || !c.includeIface("eth1") {
		t.Fatal("explicit include list must be exclusive")
	}
}
