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
		{"icmp_ip", 1, "1.1.1.1", 5, 100, ""},
		{"icmp_dns", 1, "example.com.", 3600, 5000, ""},
		{"icmp_ipv6", 1, "::1", 5, 5000, ""},
		{"tcp_dns", 2, "example.com:443", 5, 1000, ""},
		{"tcp_ipv6", 2, "[::1]:80", 5, 1000, ""},
		{"interval_low", 1, "localhost", 4, 1000, "interval_s must be between 5 and 3600"},
		{"interval_high", 1, "localhost", 3601, 1000, "interval_s must be between 5 and 3600"},
		{"timeout_low", 1, "localhost", 5, 99, "timeout_ms must be between"},
		{"timeout_high", 1, "localhost", 5, 5001, "timeout_ms must be between"},
		{"icmp_port", 1, "example.com:80", 5, 1000, "IP address or a host name"},
		{"bad_label", 1, "-bad.example", 5, 1000, "IP address or a host name"},
		{"empty", 1, "", 5, 1000, "IP address or a host name"},
		{"tcp_no_port", 2, "example.com", 5, 1000, "host:port"},
		{"port_low", 2, "example.com:0", 5, 1000, "port must be between"},
		{"port_high", 2, "example.com:65536", 5, 1000, "port must be between"},
		{"kind_missing", 0, "localhost", 5, 1000, "kind must be"},
		{"long_target", 1, strings.Repeat("a", 254), 5, 1000, "at most 253 characters"},
		{"label_long", 1, strings.Repeat("a", 64), 5, 1000, "IP address or a host name"},
		{"label_empty", 1, "a..b", 5, 1000, "IP address or a host name"},
		{"label_end", 1, "bad-.example", 5, 1000, "IP address or a host name"},
		{"label_character", 1, "a_b.example", 5, 1000, "IP address or a host name"},
		{"tcp_bad_host", 2, "-bad:80", 5, 1000, "host:port"},
		{"port_text", 2, "example.com:http", 5, 1000, "port must be between"},
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
