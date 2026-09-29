package alert

import (
	"github.com/xjetry/heron-probe/internal/hub/metric"
	"github.com/xjetry/heron-probe/internal/hub/store"
	"testing"
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
