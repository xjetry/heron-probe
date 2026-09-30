package ingest

import (
	"errors"
	"fmt"
	"math"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/agentwire"
	"github.com/xjetry/heron-probe/internal/hub/sanitize"
	"github.com/xjetry/heron-probe/internal/probelimit"
)

// validateMetrics 对整条上报做准入：任何一个字段非法就整条拒绝，live 不变。
// 无符号整数由协议类型定界；浮点、load 三元组和 boot_id 长度在此检查。
func validateMetrics(m *heronv1.Metrics) error {
	if m == nil {
		return errors.New("metrics: required")
	}
	if err := validateHostString("boot_id", m.BootId); err != nil {
		return err
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

// validateResults 限制条数、error.message 字节数，并要求 outcome 给定。合法 agent 的 Runner
// 向 Queue.Take 传 MaxResultsPerReport 限条，ToProto 沿 rune 边界截断到 MaxErrorMessageLen 字节。
// 合法 agent 把超过任务超时的测量记为 timeout
// 而不是 rtt，由 agent 的 prober 保证；任务超时不超过 MaxTimeoutMs，由 probelimit.CheckTask 保证。
// 任一守卫违反都整批 InvalidArgument；Runner 丢弃本批而不回队，否则确定性拒绝会反复发生。
// 归属与超龄不是结构问题，由 Report 逐条丢弃而不是整条拒绝。
func validateResults(rs []*heronv1.ProbeResult) error {
	if len(rs) > probelimit.MaxResultsPerReport {
		return fmt.Errorf("probe_results: must contain at most %d results; got %d", probelimit.MaxResultsPerReport, len(rs))
	}
	for i, r := range rs {
		switch o := r.GetOutcome().(type) {
		case *heronv1.ProbeResult_Error:
			if len(o.Error.GetMessage()) > probelimit.MaxErrorMessageLen {
				return fmt.Errorf("probe_results[%d].error.message: must be at most %d bytes; got %d", i, probelimit.MaxErrorMessageLen, len(o.Error.GetMessage()))
			}
		case nil:
			return fmt.Errorf("probe_results[%d].outcome: required (rtt_us, timeout or error)", i)
		case *heronv1.ProbeResult_RttUs:
			if o.RttUs > probelimit.MaxTimeoutMs*1000 {
				return fmt.Errorf("probe_results[%d].rtt_us: must not exceed %d (the maximum probe timeout in microseconds); got %d", i, probelimit.MaxTimeoutMs*1000, o.RttUs)
			}
		}
	}
	return nil
}

// maxHostString 约束 boot_id 与 Facts 的入参字节数，给报告的非探测部分提供体积上界。
const maxHostString = 256

func validateHostString(name, value string) error {
	if len(value) > maxHostString {
		return fmt.Errorf("%s: must be at most %d bytes; got %d", name, maxHostString, len(value))
	}
	return nil
}

// 准入长度校验与公开字段清理共用清单，不能只限制落库副本而放过原始请求。
func factStrings(f *heronv1.Facts) []struct {
	name  string
	value *string
} {
	return []struct {
		name  string
		value *string
	}{
		{"facts.hostname", &f.Hostname}, {"facts.os", &f.Os}, {"facts.kernel", &f.Kernel},
		{"facts.arch", &f.Arch}, {"facts.virtualization", &f.Virtualization},
		{"facts.cpu_model", &f.CpuModel}, {"facts.agent_version", &f.AgentVersion},
	}
}

func validateFacts(f *heronv1.Facts) error {
	if f != nil {
		for _, field := range factStrings(f) {
			if err := validateHostString(field.name, *field.value); err != nil {
				return err
			}
		}
	}
	return agentwire.ValidateNetwork(f.GetNetwork())
}

func sanitizeFacts(f *heronv1.Facts) {
	for _, field := range factStrings(f) {
		*field.value = sanitize.String(*field.value, maxHostString)
	}
}
