package ingest

import (
	"errors"
	"fmt"
	"math"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/sanitize"
	"github.com/xjetry/probe/internal/probelimit"
)

// validateMetrics 对整条上报做准入：任何一个字段非法就整条拒绝，live 不变。
// 无符号整数字段没有非法取值；需要判定的只有浮点与 load 三元组的形状。
func validateMetrics(m *probev1.Metrics) error {
	if m == nil {
		return errors.New("metrics: required")
	}
	floats := []struct {
		name string
		v    *float64
	}{{"cpu_pct", m.CpuPct}, {"load1", m.Load1}, {"load5", m.Load5}, {"load15", m.Load15}}
	for _, f := range floats {
		if f.v != nil && (math.IsNaN(*f.v) || math.IsInf(*f.v, 0) || *f.v < 0) {
			return fmt.Errorf("%s: must be a finite non-negative number", f.name)
		}
	}
	if m.CpuPct != nil && *m.CpuPct > 100 {
		return errors.New("cpu_pct: must not exceed 100")
	}
	set := 0
	for _, v := range []*float64{m.Load1, m.Load5, m.Load15} {
		if v != nil {
			set++
		}
	}
	if set != 0 && set != 3 {
		return errors.New("load: load1, load5 and load15 must be given together")
	}
	return nil
}

// validateResults 只判结构：outcome 必须给定。合法 agent 把超过任务超时的测量记为 timeout
// 而不是 rtt，由 agent 的 prober 保证；任务超时不超过 MaxTimeoutMs，由 probelimit.CheckTask 保证。
// 违反时整条拒绝，因此 agent 不得把被 InvalidArgument 拒绝的结果放回队列。
// 归属与超龄不是结构问题，由 Report 逐条丢弃而不是整条拒绝。
func validateResults(rs []*probev1.ProbeResult) error {
	if len(rs) > probelimit.MaxResultsPerReport {
		return fmt.Errorf("probe_results: must contain at most %d results; got %d", probelimit.MaxResultsPerReport, len(rs))
	}
	for i, r := range rs {
		switch o := r.GetOutcome().(type) {
		case *probev1.ProbeResult_Error:
			if len(o.Error.GetMessage()) > probelimit.MaxErrorMessageLen {
				return fmt.Errorf("probe_results[%d].error.message: must be at most %d bytes; got %d", i, probelimit.MaxErrorMessageLen, len(o.Error.GetMessage()))
			}
		case nil:
			return fmt.Errorf("probe_results[%d].outcome: required (rtt_us, timeout or error)", i)
		case *probev1.ProbeResult_RttUs:
			if o.RttUs > probelimit.MaxTimeoutMs*1000 {
				return fmt.Errorf("probe_results[%d].rtt_us: must not exceed %d (the maximum probe timeout in microseconds); got %d", i, probelimit.MaxTimeoutMs*1000, o.RttUs)
			}
		}
	}
	return nil
}

// maxFactString 是 Facts 里每个字符串的字节上限；其中数个字段会出现在匿名公开页。
const maxFactString = 256

func sanitizeFacts(f *probev1.Facts) {
	f.Hostname = sanitize.String(f.Hostname, maxFactString)
	f.Os = sanitize.String(f.Os, maxFactString)
	f.Kernel = sanitize.String(f.Kernel, maxFactString)
	f.Arch = sanitize.String(f.Arch, maxFactString)
	f.Virtualization = sanitize.String(f.Virtualization, maxFactString)
	f.CpuModel = sanitize.String(f.CpuModel, maxFactString)
	f.AgentVersion = sanitize.String(f.AgentVersion, maxFactString)
}
