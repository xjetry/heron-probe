package alert

import (
	"time"

	"github.com/xjetry/probe/internal/hub/store"
)

type Observation struct {
	Reported   bool
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

// 第一条 return 保证离线告警不早于面板按 TTL 显示离线，也为零值宽限提供 TTL 下限。
// 即使保存入口校验宽限下限，也不能约束之后调大的 TTL；实际下限仍在这里承载。
// 没有本次启动后的上报时，短暂的启动时长不代表恢复，Reported 由 live 条目是否存在提供。
func NextOffline(cur store.AlertState, o Observation) (store.AlertState, *store.Transition) {
	if o.Unseen < o.TTL {
		if o.Reported {
			return recovered(cur)
		}
		return cur, nil
	}
	if cur == store.StateFiring {
		return cur, nil
	}
	if o.Unseen >= o.Grace {
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

// forMinutes 的 1–60 范围由保存入口及 Load 的 CheckRule 校验共同保证。
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

// ExpiryObservation 是一个节点在一次到期扫描里的观测。HasExpiry 为假时 DaysLeft 无意义。
type ExpiryObservation struct {
	HasExpiry  bool
	DaysLeft   int // 到期日减今天（hub 时区的日历日），负数是已过期天数
	DaysBefore int // 规则的提前天数，1–365
}

// NextExpiry 没有 pending：到期是日历事件，进入窗口即触发，不存在"持续多久才算"。没有到期日按恢复处理——
// 清除到期日是结束提醒的正当方式，不能让已触发的告警卡在 firing。
func NextExpiry(cur store.AlertState, o ExpiryObservation) (store.AlertState, *store.Transition) {
	if !o.HasExpiry || o.DaysLeft > o.DaysBefore {
		return recovered(cur)
	}
	if cur == store.StateFiring {
		return cur, nil
	}
	tr := store.TransitionFiring
	return store.StateFiring, &tr
}
