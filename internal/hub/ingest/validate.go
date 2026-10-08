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
// 无符号整数由协议类型定界；浮点、load 三元组和 boot_id 的写法在此检查。
func validateMetrics(m *heronv1.Metrics) error {
	if m == nil {
		return errors.New("metrics: required")
	}
	if err := validateBootID(m.BootId); err != nil {
		return err
	}
	if err := agentwire.ValidateCounterEpoch(m.NetCounterEpoch); err != nil {
		return err
	}
	floats := []struct {
		name string
		v    *float64
	}{
		{"cpu_pct", m.CpuPct}, {"cpu_steal_pct", m.CpuStealPct}, {"cpu_iowait_pct", m.CpuIowaitPct},
		{"load1", m.Load1}, {"load5", m.Load5}, {"load15", m.Load15},
		{"load1_per_core", m.Load1PerCore},
	}
	for _, f := range floats {
		if f.v != nil && (math.IsNaN(*f.v) || math.IsInf(*f.v, 0) || *f.v < 0) {
			return fmt.Errorf("%s: must be a finite non-negative number", f.name)
		}
	}
	// 三个占比各自独立，都以 100 为上界；disk_*_bps 是无符号整数，负数与非有限数由类型本身排除。
	for _, p := range []struct {
		name string
		v    *float64
	}{{"cpu_pct", m.CpuPct}, {"cpu_steal_pct", m.CpuStealPct}, {"cpu_iowait_pct", m.CpuIowaitPct}} {
		if p.v != nil && *p.v > 100 {
			return fmt.Errorf("%s: must not exceed 100", p.name)
		}
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
	// 按核负载由 agent 在同一次采样里算好。它出现时 load1 必须同时出现：没有分子的商没有意义，
	// 也不能把缺失的 load1 补成 0 再去除。合法的 0 与字段缺失都保留。
	if m.Load1PerCore != nil && m.Load1 == nil {
		return errors.New("load1_per_core: requires load1")
	}
	return nil
}

// validateResults 限制条数、error.message 字节数，并要求 outcome 给定。合法 agent 的 Runner
// 向 Queue.Take 传 MaxResultsPerReport 限条，ToProto 沿 rune 边界截断到 MaxErrorMessageLen 字节。
// 合法 agent 把超过任务超时的测量记为 timeout
// 而不是 rtt，由 agent 的 prober 保证；任务超时不超过 MaxTimeoutMs，由 probelimit.CheckTask 保证。
// cert_not_after_s 与 presented 的结构（正数、伴随的 outcome、指纹长度、reason、身份长度）与当前配置无关，
// 不满足就整批 InvalidArgument。任务是否仍在、是否仍是 https、身份是否相符只决定丢弃附带观测，
// 不在这里整批拒绝：任务被删或改掉之后，在途结果不能挡住下发新清单。
// 任一结构守卫违反都整批 InvalidArgument；Runner 丢弃本批而不回队，否则确定性拒绝会反复发生。
// 归属与超龄不是结构问题，由 Report 逐条丢弃而不是整条拒绝。
func (s *Service) validateResults(rs []*heronv1.ProbeResult) error {
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
		if c := r.CertNotAfterS; c != nil {
			if _, ok := r.GetOutcome().(*heronv1.ProbeResult_RttUs); !ok {
				return fmt.Errorf("probe_results[%d].cert_not_after_s: must only accompany a successful (rtt_us) result", i)
			}
			if *c <= 0 {
				return fmt.Errorf("probe_results[%d].cert_not_after_s: must be positive; got %d", i, *c)
			}
		}
		if p := r.GetPresented(); p != nil {
			if _, ok := r.GetOutcome().(*heronv1.ProbeResult_Timeout); !ok {
				return fmt.Errorf("probe_results[%d].presented: must only accompany a timeout result", i)
			}
			if len(p.GetSpkiSha256()) != probelimit.CertSPKISHA256Len {
				return fmt.Errorf("probe_results[%d].presented.spki_sha256: must be exactly %d bytes; got %d", i, probelimit.CertSPKISHA256Len, len(p.GetSpkiSha256()))
			}
			if p.GetNotAfterS() <= 0 {
				return fmt.Errorf("probe_results[%d].presented.not_after_s: must be positive; got %d", i, p.GetNotAfterS())
			}
			switch p.GetReason() {
			case heronv1.PresentedReason_PRESENTED_REASON_CA_VERIFY_FAILED, heronv1.PresentedReason_PRESENTED_REASON_PIN_MISMATCH, heronv1.PresentedReason_PRESENTED_REASON_OUTSIDE_VALIDITY:
			default:
				return fmt.Errorf("probe_results[%d].presented.reason: must be a known reason; got %s", i, p.GetReason())
			}
		}
		if n := len(r.GetTaskConfigId()); n != 0 && n != probelimit.ConfigIDLen {
			return fmt.Errorf("probe_results[%d].task_config_id: must be empty or exactly %d bytes; got %d", i, probelimit.ConfigIDLen, n)
		}
	}
	return nil
}

func validateCapabilities(caps []heronv1.AgentCapability) error {
	if len(caps) > agentwire.MaxCapabilities {
		return fmt.Errorf("capabilities: must contain at most %d items; got %d", agentwire.MaxCapabilities, len(caps))
	}
	return nil
}

func validateTasksDigest(d []byte) error {
	if len(d) != 0 && len(d) != agentwire.TasksDigestLen {
		return fmt.Errorf("tasks_digest: must be empty or exactly %d bytes; got %d", agentwire.TasksDigestLen, len(d))
	}
	return nil
}

// validateBootID 只收空串或 UUID 文本（8-4-4-4-12 位十六进制，大小写均可）：Linux 的 /proc/sys/kernel/random/boot_id
// 与 macOS 的 kern.bootsessionuuid 都是这个写法，agent 读不到时发空串。boot_id 随实时读数进管理端 GetSnapshot，
// 那条响应是压缩的（api.Service.Handler）；只收 UUID，agent 能放进去的就只有十六进制字符与连字符，不能拿任意文本
// 借压缩长度试探同一响应里的节点名。
func validateBootID(id string) error {
	if id == "" {
		return nil
	}
	if len(id) != 36 {
		return fmt.Errorf("boot_id: must be empty or a UUID; got %d bytes", len(id))
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return errors.New("boot_id: must be empty or a UUID")
			}
		default:
			if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
				return errors.New("boot_id: must be empty or a UUID")
			}
		}
	}
	return nil
}

// maxHostString 约束 Facts 的入参字节数，给报告的非探测部分提供体积上界。
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
		if err := agentwire.ValidateCPUCores(f.GetCpuCores()); err != nil {
			return err
		}
	}
	if err := agentwire.ValidateNetwork(f.GetNetwork()); err != nil {
		return err
	}
	if err := agentwire.ValidateDiagnostics(f.GetDiagnostics()); err != nil {
		return err
	}
	return agentwire.ValidateExecutionScope(f.GetExecution())
}

func sanitizeFacts(f *heronv1.Facts) {
	for _, field := range factStrings(f) {
		*field.value = sanitize.String(*field.value, maxHostString)
	}
}
