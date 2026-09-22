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
