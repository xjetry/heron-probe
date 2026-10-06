package collect

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/agentwire"
	"github.com/xjetry/heron-probe/internal/clock"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestNetworkScopeChangeDoesNotCreateRate(t *testing.T) {
	clk := clock.NewFake(time.Unix(0, 0))
	h := &ifacesOnly{list: []ifaceCounters{{"eth0", 100, 200}}}
	c := &Collector{Host: h, Clock: clk}
	c.Metrics(c.Identify())
	clk.Advance(time.Second)
	h.list = []ifaceCounters{{"eth0", 110, 220}, {"eth1", 5000, 9000}}
	m, _ := c.Metrics(c.Identify())
	if m.NetRxBps != nil || m.NetTxBps != nil {
		t.Fatalf("added interface created rate: rx=%v tx=%v", m.NetRxBps, m.NetTxBps)
	}
	clk.Advance(time.Second)
	h.list = []ifaceCounters{{"eth1", 5020, 9040}, {"eth0", 120, 240}}
	m, _ = c.Metrics(c.Identify())
	if m.NetRxBps == nil || m.GetNetRxBps() != 30 || m.GetNetTxBps() != 60 {
		t.Fatalf("stable reordered scope must measure 30/60: %v", m)
	}
	clk.Advance(time.Second)
	c.NetInclude = []string{"eth1"}
	h.list = []ifaceCounters{{"eth0", 130, 260}, {"eth1", 10000, 20000}}
	m, _ = c.Metrics(c.Identify())
	if m.NetRxBps != nil || m.NetTxBps != nil {
		t.Fatalf("changed filter created rate: %v", m)
	}
	clk.Advance(time.Second)
	h.list = []ifaceCounters{{"eth1", 10005, 20010}, {"docker0", 999999, 999999}}
	m, _ = c.Metrics(c.Identify())
	if m.NetRxBps == nil || m.GetNetRxBps() != 5 || m.GetNetTxBps() != 10 {
		t.Fatalf("excluded interfaces must not reset scope: %v", m)
	}
}

func TestCollectionDiagnosticsReflectEffectiveSampling(t *testing.T) {
	c := fixture(t)
	c.NetInclude = []string{"eth*"}
	c.NetExclude = []string{"eth0"}
	c.Metrics(c.Identify())
	d := c.Facts(c.Identify()).GetDiagnostics()
	if d == nil || !slices.Equal(d.NetInclude, []string{"eth*"}) || len(d.NetExclude) != 0 || !slices.Equal(d.NetInterfaces, []string{"eth0"}) || d.NetInterfacesTotal != 1 || len(d.FailedCollectors) != 0 {
		t.Fatalf("effective collection diagnostics = %v", d)
	}
	c.Host.(*ProcFS).DiskUsage = func(string) (uint64, uint64, error) { return 0, 0, errors.New("secret raw filesystem error") }
	c.Metrics(c.Identify())
	d = c.Facts(c.Identify()).GetDiagnostics()
	if d == nil || !slices.Equal(d.FailedCollectors, []heronv1.CollectionComponent{heronv1.CollectionComponent_COLLECTION_COMPONENT_DISK}) {
		t.Fatalf("failed collector not identified: %v", d)
	}
	encoded, err := protojson.Marshal(d)
	if err != nil || strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "filesystem") {
		t.Fatalf("raw failure escaped into diagnostics: %s %v", encoded, err)
	}
	c.Host.(*ProcFS).DiskUsage = func(string) (uint64, uint64, error) { return 100, 50, nil }
	c.Metrics(c.Identify())
	if d = c.Facts(c.Identify()).GetDiagnostics(); len(d.GetFailedCollectors()) != 0 {
		t.Fatalf("recovered collector retained a failure: %v", d)
	}
}

// 磁盘 I/O 计数器读不到是独立失败类别：它不是 statfs 的磁盘用量失败，面板不能把它显示成"磁盘"。
func TestDiskCounterFailureHasItsOwnCollectorCategory(t *testing.T) {
	c := fixture(t)
	c.Host = &diskIOFail{ProcFS: c.Host.(*ProcFS)}
	m, err := c.Metrics(c.Identify())
	if m == nil || err == nil {
		t.Fatalf("disk counter failure must be reported: m=%v err=%v", m, err)
	}
	d := c.Facts(c.Identify()).GetDiagnostics()
	if !slices.Contains(d.GetFailedCollectors(), heronv1.CollectionComponent_COLLECTION_COMPONENT_DISK_IO) {
		t.Fatalf("disk I/O counter failure not identified: %v", d)
	}
	if slices.Contains(d.GetFailedCollectors(), heronv1.CollectionComponent_COLLECTION_COMPONENT_DISK) {
		t.Fatalf("disk usage category reused for an I/O counter failure: %v", d)
	}
	if err := agentwire.ValidateDiagnostics(d); err != nil {
		t.Fatalf("new component must pass the shared whitelist: %v", err)
	}
	if m.GetDiskTotal() != 1000 || m.GetDiskUsed() != 400 {
		t.Fatalf("disk usage must be unaffected: %+v", m)
	}
}

type diskIOFail struct{ *ProcFS }

func (h *diskIOFail) diskCounters() ([]diskCounters, error) {
	return nil, errors.New("no whole-disk device")
}

func TestDiagnosticsTruncationDoesNotTruncateCounterScope(t *testing.T) {
	h := &ifacesOnly{}
	for i := range agentwire.MaxDiagnosticInterfaces + 1 {
		h.list = append(h.list, ifaceCounters{fmt.Sprintf("eth%03d", i), 10, 20})
	}
	c := &Collector{Host: h, Clock: clock.NewFake(time.Unix(0, 0))}
	m, _ := c.Metrics(c.Identify())
	d := c.Facts(c.Identify()).Diagnostics
	if len(d.NetInterfaces) != 128 || d.NetInterfacesTotal != 129 || m.GetNetRxTotal() != 1290 {
		t.Fatalf("truncation lost counter members: %v / %v", d, m)
	}
	if err := agentwire.ValidateDiagnostics(d); err != nil {
		t.Fatal(err)
	}
	epoch := m.NetCounterEpoch
	h.list[128].name = "renamed-last-interface"
	m, _ = c.Metrics(c.Identify())
	if m.NetCounterEpoch == epoch {
		t.Fatal("scope hash ignored members beyond the displayed list")
	}
	h.list = nil
	c.Metrics(c.Identify())
	d = c.Facts(c.Identify()).Diagnostics
	if d.NetInterfacesTotal != 0 || len(d.NetInterfaces) != 0 || !slices.Contains(d.FailedCollectors, heronv1.CollectionComponent_COLLECTION_COMPONENT_NET) {
		t.Fatalf("failed network retained old scope: %v", d)
	}
}
