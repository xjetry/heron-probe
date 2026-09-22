package metric

import (
	"testing"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"google.golang.org/protobuf/proto"
)

func idx(t *testing.T, name string) int {
	t.Helper()
	for i, c := range Columns {
		if c.Name == name {
			return i
		}
	}
	t.Fatalf("no column %q", name)
	return -1
}

func TestAddCountsOnlyPresentReadings(t *testing.T) {
	b := NewBucket()
	b.Add(&probev1.Metrics{CpuPct: proto.Float64(10), MemUsed: proto.Uint64(100)})
	b.Add(&probev1.Metrics{CpuPct: proto.Float64(30)}) // 无 mem_used 读数
	cpu, mem := idx(t, "cpu"), idx(t, "mem_used")
	if got, ok := b.Mean(cpu); !ok || got != 20 {
		t.Fatalf("cpu mean = %v,%v want 20,true", got, ok)
	}
	if b.N[mem] != 1 {
		t.Fatalf("mem_used n = %d, want 1: a missing reading must not enter the count", b.N[mem])
	}
	if got, ok := b.Mean(mem); !ok || got != 100 {
		t.Fatalf("mem_used mean = %v,%v want 100,true", got, ok)
	}
	if b.Max[cpu] != 30 {
		t.Fatalf("cpu max = %v, want 30", b.Max[cpu])
	}
}

func TestMeanOfEmptyColumnIsNoData(t *testing.T) {
	b := NewBucket()
	b.Add(&probev1.Metrics{CpuPct: proto.Float64(1)})
	if _, ok := b.Mean(idx(t, "swap_used")); ok {
		t.Fatal("swap_used had no readings; mean must report no-data, not 0")
	}
}

func TestMergeIsAdditive(t *testing.T) {
	a, b := NewBucket(), NewBucket()
	a.Add(&probev1.Metrics{CpuPct: proto.Float64(10)})
	b.Add(&probev1.Metrics{CpuPct: proto.Float64(20)})
	b.Add(&probev1.Metrics{CpuPct: proto.Float64(60)})
	a.Merge(b)
	cpu := idx(t, "cpu")
	if a.Sum[cpu] != 90 || a.N[cpu] != 3 || a.Max[cpu] != 60 {
		t.Fatalf("merged sum/n/max = %v/%d/%v, want 90/3/60", a.Sum[cpu], a.N[cpu], a.Max[cpu])
	}
}

func TestColumnsCoverSpecifiedMetrics(t *testing.T) {
	want := map[string]struct {
		kind Kind
		typ  Type
	}{
		"cpu": {MeanMax, Float}, "mem_used": {MeanMax, Int}, "swap_used": {Mean, Int},
		"disk_used": {Mean, Int}, "load1": {Mean, Float}, "tcp": {Mean, Int},
		"udp": {Mean, Int}, "procs": {Mean, Int},
	}
	if len(Columns) != len(want) {
		t.Fatalf("%d columns, want %d", len(Columns), len(want))
	}
	for _, c := range Columns {
		w, ok := want[c.Name]
		if !ok {
			t.Fatalf("unexpected column %q", c.Name)
		}
		if c.Kind != w.kind || c.Type != w.typ {
			t.Fatalf("column %q kind/type = %v/%v, want %v/%v", c.Name, c.Kind, c.Type, w.kind, w.typ)
		}
	}
}
