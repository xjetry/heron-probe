package alert

import (
	"context"
	"errors"
	"fmt"

	"github.com/xjetry/heron-probe/internal/hub/metric"
	"github.com/xjetry/heron-probe/internal/hub/store"
)

// NextResource 的触发与恢复各需完整的连续窗口；滞回区间保持 firing，缺失读数不能证明健康。
func NextResource(current store.AlertState, samples []MinuteSample, recovery float64) (store.AlertState, *store.Transition) {
	if len(samples) == 0 {
		return current, nil
	}
	bad, healthy := true, true
	for _, sample := range samples {
		bad = bad && sample.Present && sample.Exceeds
		healthy = healthy && sample.Present && sample.Value <= recovery
	}
	if current == store.StateFiring {
		if healthy {
			t := store.TransitionRecovered
			return store.StateOK, &t
		}
		return current, nil
	}
	if bad {
		t := store.TransitionFiring
		return store.StateFiring, &t
	}
	last := samples[len(samples)-1]
	if last.Present && last.Exceeds {
		return store.StatePending, nil
	}
	return store.StateOK, nil
}

func (e *Engine) EvaluateResources(ctx context.Context, minuteTS int64) error {
	if minuteTS%60 != 0 {
		return invalid("minuteTS", "must be aligned to 60 seconds")
	}
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	cy := newCycle()
	defer e.flush(cy)
	nodes, err := e.st.ListMonitoringNodes(ctx)
	if err != nil {
		return err
	}
	lv, _ := store.LevelByName("1m")
	var errs []error
	for _, rule := range e.Rules() {
		if !rule.Enabled || rule.Kind != store.KindResource {
			continue
		}
		keep := map[int64]bool{}
		for _, node := range nodes {
			if !inScope(rule, node.ID) {
				continue
			}
			keep[node.ID] = true
			from := minuteTS - int64(rule.ForMinutes-1)*60
			rows, err := e.st.QueryMetrics(ctx, node.ID, from, minuteTS+60, lv, 60)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			samples := make([]MinuteSample, rule.ForMinutes)
			index := metric.Index(string(rule.ResourceMetric))
			for _, row := range rows {
				value, present := row.Bucket.Mean(index)
				samples[(row.TS-from)/60] = MinuteSample{Present: present, Value: value, Exceeds: value >= rule.Threshold}
			}
			next, transition := NextResource(e.current(stateKey{rule.ID, node.ID}), samples, rule.RecoveryThreshold)
			value := samples[len(samples)-1].Value
			label := "内存"
			if rule.ResourceMetric == store.MetricDiskUsedPct {
				label = "磁盘"
			}
			summary := fmt.Sprintf("节点 %s %s持续 %d 分钟达到 %.1f%%（规则 %s）", node.Name, label, rule.ForMinutes, rule.Threshold, rule.Name)
			if transition != nil && *transition == store.TransitionRecovered {
				summary = fmt.Sprintf("节点 %s %s持续 %d 分钟不高于 %.1f%%，已恢复（规则 %s）", node.Name, label, rule.ForMinutes, rule.RecoveryThreshold, rule.Name)
			}
			if err := e.apply(ctx, cy, rule, node.ID, next, false, "", transition, summary, value); err != nil {
				errs = append(errs, err)
			}
		}
		if err := e.pruneCandidates(ctx, rule.ID, keep); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
