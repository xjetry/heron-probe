package alert

import (
	"time"

	"github.com/xjetry/probe/internal/hub/store"
)

type Observation struct {
	Unseen     time.Duration
	Grace, TTL time.Duration
}

func recovered(cur store.AlertState) (store.AlertState, *store.Transition) {
	if cur == store.StateFiring {
		tr := store.TransitionRecovered
		return store.StateOK, &tr
	}
	return store.StateOK, nil
}

func NextOffline(cur store.AlertState, o Observation) (store.AlertState, *store.Transition) {
	if o.Unseen < o.TTL {
		return recovered(cur)
	}
	if cur == store.StateFiring {
		return cur, nil
	}
	if o.Unseen >= max(o.TTL, o.Grace) {
		tr := store.TransitionFiring
		return store.StateFiring, &tr
	}
	return store.StatePending, nil
}

// EvaluateProbes 按时间升序铺满窗口；Present 为假不能当成一次恢复观测。
type MinuteSample struct {
	Present bool
	Exceeds bool
	Value   float64
}

// forMinutes 的 1–60 范围由保存入口 CheckRule 保证。
func NextProbe(cur store.AlertState, samples []MinuteSample, forMinutes int) (store.AlertState, *store.Transition) {
	if len(samples) == 0 || !samples[len(samples)-1].Present {
		return cur, nil
	}
	if !samples[len(samples)-1].Exceeds {
		return recovered(cur)
	}
	if cur == store.StateFiring {
		return cur, nil
	}
	if len(samples) < forMinutes {
		return store.StatePending, nil
	}
	for _, s := range samples[len(samples)-forMinutes:] {
		if !s.Present || !s.Exceeds {
			return store.StatePending, nil
		}
	}
	tr := store.TransitionFiring
	return store.StateFiring, &tr
}
