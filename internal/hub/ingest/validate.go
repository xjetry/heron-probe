package ingest

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
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

// maxFactString 是 Facts 里每个字符串的字节上限；其中数个字段会出现在匿名公开页。
const maxFactString = 256

func sanitizeString(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > maxFactString {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

func sanitizeFacts(f *probev1.Facts) {
	f.Hostname = sanitizeString(f.Hostname)
	f.Os = sanitizeString(f.Os)
	f.Kernel = sanitizeString(f.Kernel)
	f.Arch = sanitizeString(f.Arch)
	f.Virtualization = sanitizeString(f.Virtualization)
	f.CpuModel = sanitizeString(f.CpuModel)
	f.AgentVersion = sanitizeString(f.AgentVersion)
}
