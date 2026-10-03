package alert

import (
	"testing"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/hub/metric"
	"github.com/xjetry/heron-probe/internal/hub/store"
)

func TestResourceContinuousWindowsAndMissingReadings(t *testing.T) {
	for _, resource := range []store.ResourceMetric{store.MetricMemoryUsedPct, store.MetricDiskUsedPct} {
		t.Run(string(resource), func(t *testing.T) {
			f := newFixture(t)
			r := f.rule(t, store.AlertRule{Name: "容量", Kind: store.KindResource, Enabled: true, NodeIDs: f.ids[:1], ResourceMetric: resource, Threshold: 90, RecoveryThreshold: 80, ForMinutes: 2})
			base := f.clk.Now().Unix()
			write := func(offset int64, value float64) {
				b := metric.NewBucket()
				i := metric.Index(string(resource))
				b.Sum[i], b.N[i] = value, 1
				_, err := f.st.WriteMinuteBatch(t.Context(), metric.Batch{Rows: []metric.Row{{NodeID: f.ids[0], TS: base + offset*60, Bucket: b}}})
				must(t, err)
			}
			eval := func(offset int64, want store.AlertState) {
				must(t, f.e.EvaluateResources(t.Context(), base+offset*60))
				wantState(t, f.e, r.ID, f.ids[0], want)
			}
			write(0, 90)
			eval(0, store.StatePending)
			write(1, 95)
			eval(1, store.StateFiring)
			write(2, 85)
			eval(2, store.StateFiring)
			write(3, 80)
			eval(3, store.StateFiring)
			eval(4, store.StateFiring)
			f.restart(t)
			write(5, 75)
			eval(5, store.StateFiring)
			write(6, 80)
			eval(6, store.StateOK)
			events := f.events(t)
			if len(events) != 2 || events[0].Transition != store.TransitionRecovered || events[1].Transition != store.TransitionFiring {
				t.Fatalf("events=%+v", events)
			}
		})
	}
}

// 四个新指标的窗口语义与既有百分比指标同形：写入值是列的原始读数（按核负载写原始 load1），
// high 达到阈值、higher 更高、mid 落在滞回区间、low 低于恢复阈值；无写入的分钟验证缺读数不恢复。
func TestResourceNewMetricWindows(t *testing.T) {
	for _, tc := range []struct {
		name      string
		metric    store.ResourceMetric
		column    string
		threshold float64
		recovery  float64
		high      float64
		higher    float64
		mid       float64
		low       float64
	}{
		{"cpu_pct", store.MetricCpuPct, "cpu", 90, 80, 90, 95, 85, 75},
		{"net_rx_bps", store.MetricNetRxBps, "net_rx_bps", 1.25e8, 1e8, 1.25e8, 1.3e8, 1.1e8, 9e7},
		{"net_tx_bps", store.MetricNetTxBps, "net_tx_bps", 1.25e8, 1e8, 1.25e8, 1.3e8, 1.1e8, 9e7},
		// 节点 4 核：按核负载 2 触发、1 恢复，写入原始 load1。
		{"load1_per_core", store.MetricLoad1PerCore, "load1", 2, 1, 8, 10, 6, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			if tc.metric == store.MetricLoad1PerCore {
				must(t, f.st.UpsertFacts(t.Context(), f.ids[0], 1, &heronv1.Facts{CpuCores: 4}))
			}
			r := f.rule(t, store.AlertRule{Name: "资源", Kind: store.KindResource, Enabled: true, NodeIDs: f.ids[:1], ResourceMetric: tc.metric, Threshold: tc.threshold, RecoveryThreshold: tc.recovery, ForMinutes: 2})
			base := f.clk.Now().Unix()
			write := func(offset int64, value float64) {
				b := metric.NewBucket()
				i := metric.Index(tc.column)
				b.Sum[i], b.N[i] = value, 1
				_, err := f.st.WriteMinuteBatch(t.Context(), metric.Batch{Rows: []metric.Row{{NodeID: f.ids[0], TS: base + offset*60, Bucket: b}}})
				must(t, err)
			}
			eval := func(offset int64, want store.AlertState) {
				must(t, f.e.EvaluateResources(t.Context(), base+offset*60))
				wantState(t, f.e, r.ID, f.ids[0], want)
			}
			write(0, tc.high)
			eval(0, store.StatePending)
			write(1, tc.higher)
			eval(1, store.StateFiring)
			write(2, tc.mid)
			eval(2, store.StateFiring)
			eval(3, store.StateFiring)
			write(4, tc.low)
			eval(4, store.StateFiring)
			write(5, tc.recovery)
			eval(5, store.StateOK)
			events := f.events(t)
			if len(events) != 2 || events[0].Transition != store.TransitionRecovered || events[1].Transition != store.TransitionFiring {
				t.Fatalf("events=%+v", events)
			}
		})
	}
}

// 按核负载的分母来自 node_facts：没有 facts 或核数为 0 时该分钟无读数，既不触发也不恢复，
// 不退回原始 load1。
func TestResourceLoad1PerCoreMissingCores(t *testing.T) {
	f := newFixture(t)
	r := f.rule(t, store.AlertRule{Name: "按核负载", Kind: store.KindResource, Enabled: true, NodeIDs: f.ids[:1], ResourceMetric: store.MetricLoad1PerCore, Threshold: 2, RecoveryThreshold: 1, ForMinutes: 2})
	base := f.clk.Now().Unix()
	write := func(offset int64, load1 float64) {
		b := metric.NewBucket()
		i := metric.Index("load1")
		b.Sum[i], b.N[i] = load1, 1
		_, err := f.st.WriteMinuteBatch(t.Context(), metric.Batch{Rows: []metric.Row{{NodeID: f.ids[0], TS: base + offset*60, Bucket: b}}})
		must(t, err)
	}
	eval := func(offset int64, want store.AlertState) {
		must(t, f.e.EvaluateResources(t.Context(), base+offset*60))
		wantState(t, f.e, r.ID, f.ids[0], want)
	}
	// 从未离开 ok 的规则×节点没有状态记录：缺读数不评估时断言"无记录或 ok"。
	evalOK := func(offset int64) {
		must(t, f.e.EvaluateResources(t.Context(), base+offset*60))
		if got := stateOf(f.e, r.ID, f.ids[0]); got != "" && got != store.StateOK {
			t.Fatalf("node %d state=%q want ok or no record", f.ids[0], got)
		}
	}
	write(0, 100)
	write(1, 100)
	evalOK(1)
	must(t, f.st.UpsertFacts(t.Context(), f.ids[0], 1, &heronv1.Facts{CpuCores: 4}))
	write(3, 100)
	eval(3, store.StatePending)
	write(4, 100)
	eval(4, store.StateFiring)
	must(t, f.st.UpsertFacts(t.Context(), f.ids[0], 2, &heronv1.Facts{CpuCores: 0}))
	write(5, 0.1)
	eval(5, store.StateFiring)
	write(6, 0.1)
	eval(6, store.StateFiring)
	must(t, f.st.UpsertFacts(t.Context(), f.ids[0], 3, &heronv1.Facts{CpuCores: 4}))
	write(7, 2)
	eval(7, store.StateOK)
}
