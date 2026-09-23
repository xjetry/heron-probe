package probelimit

import (
	"strings"
	"testing"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
)

func TestCheckTask(t *testing.T) {
	for _, tc := range []struct {
		name              string
		kind              probev1.ProbeKind
		target            string
		interval, timeout uint32
		want              string
	}{
		{"icmp_ip", probev1.ProbeKind_PROBE_KIND_ICMP, "1.1.1.1", 5, 100, ""},
		{"icmp_dns", probev1.ProbeKind_PROBE_KIND_ICMP, "example.com.", 3600, 5000, ""},
		{"icmp_ipv6", probev1.ProbeKind_PROBE_KIND_ICMP, "::1", 5, 5000, ""},
		{"tcp_dns", probev1.ProbeKind_PROBE_KIND_TCP, "example.com:443", 5, 1000, ""},
		{"tcp_ipv6", probev1.ProbeKind_PROBE_KIND_TCP, "[::1]:80", 5, 1000, ""},
		{"interval_low", probev1.ProbeKind_PROBE_KIND_ICMP, "localhost", 4, 1000, "interval_s must be between 5 and 3600"},
		{"interval_high", probev1.ProbeKind_PROBE_KIND_ICMP, "localhost", 3601, 1000, "interval_s must be between 5 and 3600"},
		{"timeout_low", probev1.ProbeKind_PROBE_KIND_ICMP, "localhost", 5, 99, "timeout_ms must be between"},
		{"timeout_high", probev1.ProbeKind_PROBE_KIND_ICMP, "localhost", 5, 5001, "timeout_ms must be between"},
		{"icmp_port", probev1.ProbeKind_PROBE_KIND_ICMP, "example.com:80", 5, 1000, "IP address or a host name"},
		{"bad_label", probev1.ProbeKind_PROBE_KIND_ICMP, "-bad.example", 5, 1000, "IP address or a host name"},
		{"empty", probev1.ProbeKind_PROBE_KIND_ICMP, "", 5, 1000, "IP address or a host name"},
		{"tcp_no_port", probev1.ProbeKind_PROBE_KIND_TCP, "example.com", 5, 1000, "host:port"},
		{"port_low", probev1.ProbeKind_PROBE_KIND_TCP, "example.com:0", 5, 1000, "port must be between"},
		{"port_high", probev1.ProbeKind_PROBE_KIND_TCP, "example.com:65536", 5, 1000, "port must be between"},
		{"kind_missing", probev1.ProbeKind_PROBE_KIND_UNSPECIFIED, "localhost", 5, 1000, "kind must be"},
		{"long_target", probev1.ProbeKind_PROBE_KIND_ICMP, strings.Repeat("a", 254), 5, 1000, "at most 253 bytes"},
		{"label_long", probev1.ProbeKind_PROBE_KIND_ICMP, strings.Repeat("a", 64), 5, 1000, "IP address or a host name"},
		{"label_empty", probev1.ProbeKind_PROBE_KIND_ICMP, "a..b", 5, 1000, "IP address or a host name"},
		{"label_end", probev1.ProbeKind_PROBE_KIND_ICMP, "bad-.example", 5, 1000, "IP address or a host name"},
		{"label_character", probev1.ProbeKind_PROBE_KIND_ICMP, "a_b.example", 5, 1000, "IP address or a host name"},
		{"tcp_bad_host", probev1.ProbeKind_PROBE_KIND_TCP, "-bad:80", 5, 1000, "target host for a TCP task must be an IP address or a host name"},
		{"port_text", probev1.ProbeKind_PROBE_KIND_TCP, "example.com:http", 5, 1000, "port must be between"},
		{"icmp_zone", probev1.ProbeKind_PROBE_KIND_ICMP, "fe80::1%eth0", 5, 1000, "IP address or a host name"},
		{"icmp_zone_control", probev1.ProbeKind_PROBE_KIND_ICMP, "fe80::1%\n<x>", 5, 1000, "IP address or a host name"},
		{"tcp_zone", probev1.ProbeKind_PROBE_KIND_TCP, "[fe80::1%eth0]:80", 5, 1000, "target host for a TCP task must be an IP address or a host name"},
		{"tcp_zone_control", probev1.ProbeKind_PROBE_KIND_TCP, "[fe80::1%\n<x>]:80", 5, 1000, "target host for a TCP task must be an IP address or a host name"},
		{"tcp_bracket_dns", probev1.ProbeKind_PROBE_KIND_TCP, "[example.com]:80", 5, 1000, "canonical host:port"},
		{"tcp_bracket_ipv4", probev1.ProbeKind_PROBE_KIND_TCP, "[1.1.1.1]:80", 5, 1000, "canonical host:port"},
		{"tcp_port_plus", probev1.ProbeKind_PROBE_KIND_TCP, "example.com:+80", 5, 1000, "canonical host:port"},
		{"tcp_port_zeroes", probev1.ProbeKind_PROBE_KIND_TCP, "example.com:0080", 5, 1000, "canonical host:port"},
		{"label_boundary", probev1.ProbeKind_PROBE_KIND_ICMP, strings.Repeat("a", 63), 5, 1000, ""},
		{"target_boundary", probev1.ProbeKind_PROBE_KIND_ICMP, strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61), 5, 1000, ""},
		{"port_min", probev1.ProbeKind_PROBE_KIND_TCP, "example.com:1", 5, 1000, ""},
		{"port_max", probev1.ProbeKind_PROBE_KIND_TCP, "example.com:65535", 5, 1000, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckTask(&probev1.ProbeTask{Kind: tc.kind, Target: tc.target, IntervalS: tc.interval, TimeoutMs: tc.timeout})
			if tc.want == "" {
				if err != nil {
					t.Fatalf("valid task rejected: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v; want substring %q", err, tc.want)
			}
		})
	}
	t.Run("nil", func(t *testing.T) {
		if err := CheckTask(nil); err == nil || err.Error() != "task: required" {
			t.Fatalf("nil task error=%v", err)
		}
	})
}
