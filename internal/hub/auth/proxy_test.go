package auth

import (
	"net/netip"
	"testing"
)

func TestClientIPIgnoresForwardedHeaderFromUntrustedPeer(t *testing.T) {
	got := ClientIP("198.51.100.7:4321", "203.0.113.9", nil)
	if got != netip.MustParseAddr("198.51.100.7") {
		t.Fatalf("got %v", got)
	}
}

func TestClientIPUsesRightmostUntrustedForwardedAddress(t *testing.T) {
	trusted, _ := ParsePrefixes("10.0.0.0/8, 127.0.0.1")
	got := ClientIP("10.1.2.3:80", "203.0.113.9, 10.9.9.9", trusted)
	if got != netip.MustParseAddr("203.0.113.9") {
		t.Fatalf("got %v", got)
	}
}

func TestClientIPMalformedHeaderFallsBackToPeer(t *testing.T) {
	trusted, _ := ParsePrefixes("10.0.0.0/8")
	got := ClientIP("10.1.2.3:80", "not-an-ip", trusted)
	if got != netip.MustParseAddr("10.1.2.3") {
		t.Fatalf("got %v", got)
	}
}

func TestClientIPUnmapsIPv4InIPv6(t *testing.T) {
	got := ClientIP("[::ffff:198.51.100.7]:1", "", nil)
	if got != netip.MustParseAddr("198.51.100.7") {
		t.Fatalf("got %v", got)
	}
}

func TestParsePrefixesRejectsGarbage(t *testing.T) {
	if _, err := ParsePrefixes("10.0.0.0/8, banana"); err == nil {
		t.Fatal("expected error")
	}
}

func TestRequestScheme(t *testing.T) {
	trusted, _ := ParsePrefixes("10.0.0.0/8")
	cases := []struct {
		peer, xfp string
		want      string
	}{
		{"10.1.2.3:4444", "https", "https"},
		{"10.1.2.3:4444", "HTTPS, http", "https"},
		{"10.1.2.3:4444", "http", "http"},
		{"10.1.2.3:4444", "", "http"},
		{"203.0.113.9:4444", "https", "http"}, // 不可信对端的转发头不采信
		{"garbage", "https", "http"},
	}
	for _, c := range cases {
		t.Run(c.peer+"/"+c.xfp, func(t *testing.T) {
			if got := RequestScheme(c.peer, c.xfp, trusted); got != c.want {
				t.Fatalf("RequestScheme(%q, %q) = %q, want %q", c.peer, c.xfp, got, c.want)
			}
		})
	}
}

// 按来源计数的键：IPv4 一个地址一个键；IPv6 同一 /64 的地址合成一个键、不同 /64 各自一个；IPv4 映射地址按 IPv4 算，
// 不还原的话它们的前 64 位全是 0，会全部合进 ::/64。
func TestSourceKey(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"192.0.2.1", "192.0.2.1"},
		{"192.0.2.2", "192.0.2.2"},
		{"::ffff:192.0.2.1", "192.0.2.1"},
		{"2001:db8:1:2::1", "2001:db8:1:2::"},
		{"2001:db8:1:2:ffff:ffff:ffff:ffff", "2001:db8:1:2::"},
		{"2001:db8:1:3::1", "2001:db8:1:3::"},
		{"fe80::1%eth0", "fe80::"},
	} {
		if got := SourceKey(netip.MustParseAddr(c.in)).String(); got != c.want {
			t.Errorf("SourceKey(%s) = %s, want %s", c.in, got, c.want)
		}
	}
	if got := SourceKey(netip.Addr{}); got.IsValid() {
		t.Errorf("SourceKey(invalid) = %s, want the invalid address back", got)
	}
}

// IPv6 的键写成前缀，免得在报错与日志里被读成一个具体地址。
func TestDescribeSource(t *testing.T) {
	for in, want := range map[string]string{"192.0.2.1": "192.0.2.1", "2001:db8:1:2::": "2001:db8:1:2::/64"} {
		if got := DescribeSource(netip.MustParseAddr(in)); got != want {
			t.Errorf("DescribeSource(%s) = %s, want %s", in, got, want)
		}
	}
}
