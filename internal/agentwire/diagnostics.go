package agentwire

import (
	"encoding/hex"
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
)

const (
	MaxNetPatterns          = 64
	MaxNetPatternBytes      = 128
	MaxDiagnosticInterfaces = 128
	MaxInterfaceNameBytes   = 64
)

// ValidateCounterEpoch 接受旧 agent 的空标识；空与非空切换仍由流量账本重建基线。
func ValidateCounterEpoch(epoch string) error {
	if epoch == "" {
		return nil
	}
	if len(epoch) != 64 || strings.ToLower(epoch) != epoch {
		return fmt.Errorf("net_counter_epoch: must be empty or a lowercase SHA-256 digest")
	}
	if _, err := hex.DecodeString(epoch); err != nil {
		return fmt.Errorf("net_counter_epoch: must be empty or a lowercase SHA-256 digest")
	}
	return nil
}

func safeDiagnosticText(s string, limit int) bool {
	return s != "" && len(s) <= limit && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

func ValidateInterfaceName(name string) error {
	if !safeDiagnosticText(name, MaxInterfaceNameBytes) {
		return fmt.Errorf("diagnostics.net_interfaces: names must be non-empty UTF-8 without control characters, at most %d bytes", MaxInterfaceNameBytes)
	}
	return nil
}

// ValidateNetFilters 同时约束本机启动参数和上报预算，避免本机接受的配置只能生成被 hub 永久拒绝的请求。
func ValidateNetFilters(include, exclude []string) error {
	for i, patterns := range [][]string{include, exclude} {
		name := []string{"net_include", "net_exclude"}[i]
		if len(patterns) > MaxNetPatterns {
			return fmt.Errorf("%s: must contain at most %d patterns", name, MaxNetPatterns)
		}
		for _, p := range patterns {
			if !safeDiagnosticText(p, MaxNetPatternBytes) {
				return fmt.Errorf("%s: patterns must be non-empty UTF-8 without control characters, at most %d bytes", name, MaxNetPatternBytes)
			}
			if _, err := path.Match(p, ""); err != nil {
				return fmt.Errorf("%s: invalid glob pattern", name)
			}
		}
	}
	return nil
}

// ValidateDiagnostics 是上报、落库、读库与恢复共用的白名单边界；不接收任意错误文本或配置 map。
func ValidateDiagnostics(d *heronv1.AgentDiagnostics) error {
	if d == nil {
		return nil
	}
	if len(d.ProtoReflect().GetUnknown()) != 0 {
		return fmt.Errorf("diagnostics: unknown fields are not accepted")
	}
	if err := ValidateNetFilters(d.NetInclude, d.NetExclude); err != nil {
		return err
	}
	if len(d.NetInclude) > 0 && len(d.NetExclude) > 0 {
		return fmt.Errorf("diagnostics: only the effective include or exclude policy may be provided")
	}
	if len(d.NetInterfaces) > MaxDiagnosticInterfaces || uint32(len(d.NetInterfaces)) != min(d.NetInterfacesTotal, MaxDiagnosticInterfaces) {
		return fmt.Errorf("diagnostics.net_interfaces: must contain the first %d names or the complete smaller set", MaxDiagnosticInterfaces)
	}
	for i, name := range d.NetInterfaces {
		if err := ValidateInterfaceName(name); err != nil {
			return err
		}
		if i > 0 && d.NetInterfaces[i-1] >= name {
			return fmt.Errorf("diagnostics.net_interfaces: names must be unique, sorted, and at most %d bytes without control characters", MaxInterfaceNameBytes)
		}
	}
	seen := map[heronv1.CollectionComponent]bool{}
	for _, part := range d.FailedCollectors {
		if part <= heronv1.CollectionComponent_COLLECTION_COMPONENT_UNSPECIFIED || part > heronv1.CollectionComponent_COLLECTION_COMPONENT_DISK_IO || seen[part] {
			return fmt.Errorf("diagnostics.failed_collectors: must contain distinct known components")
		}
		seen[part] = true
	}
	if slices.Contains(d.FailedCollectors, heronv1.CollectionComponent_COLLECTION_COMPONENT_NET) && d.NetInterfacesTotal != 0 {
		return fmt.Errorf("diagnostics.net_interfaces: failed network collection must not claim a sampled set")
	}
	if d.ReportIntervalMs != 0 {
		if _, ok := ClampReportInterval(d.ReportIntervalMs); !ok {
			return fmt.Errorf("diagnostics.report_interval_ms: outside agent reporting bounds")
		}
	}
	return nil
}
