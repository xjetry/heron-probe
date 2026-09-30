package netinfo

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
)

func testAddresses() ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("10.0.0.2"), netip.MustParseAddr("fd00::2")}, nil
}

// 固定请求仍指向正式主机名，拨号仅重定向到受控 TLS 服务，不依赖本机公网或 IPv6 路由。
func localDetector(t *testing.T, handler http.HandlerFunc) *Detector {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	d := newDetector(testAddresses, func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp4" && network != "tcp6" {
			t.Errorf("dial family=%q", network)
		}
		if address != "api64.ipify.org:443" {
			t.Errorf("dial address=%q", address)
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp4", srv.Listener.Addr().String())
	})
	for _, client := range d.clients {
		client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs, ServerName: "example.com"}
	}
	d.now = func() time.Time { return time.Unix(123, 0) }
	return d
}

func TestDetectionResponseAndFailureStates(t *testing.T) {
	for _, tc := range []struct {
		name, body   string
		family, code int
		state        heronv1.AddressDetectionState
	}{
		{"v4", "8.8.8.8\n", 0, 200, 1}, {"v6", "2606:4700::1111", 1, 200, 1},
		{"wrong_family", "8.8.8.8", 1, 200, 3}, {"mapped_v6", "::ffff:8.8.8.8", 1, 200, 3},
		{"private", "10.0.0.1", 0, 200, 3}, {"documentation", "2001:db8::1", 1, 200, 3},
		{"bad_body", "not an IP", 0, 200, 3}, {"oversized", "8.8.8.8" + strings.Repeat(" ", 65), 0, 200, 3},
		{"redirect", "8.8.8.8", 0, 302, 3}, {"server_error", "8.8.8.8", 0, 500, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			d := localDetector(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Location", "/redirected")
				w.WriteHeader(tc.code)
				io.WriteString(w, tc.body)
			})
			addresses, _ := testAddresses()
			result := d.detect(t.Context(), tc.family, addresses, nil)
			if result.State != tc.state || result.CheckedAt != 123 || (tc.state != 1 && result.Address != "") {
				t.Fatalf("result=%v", result)
			}
			if tc.state == 1 && result.Address != strings.TrimSpace(tc.body) {
				t.Fatalf("address=%q", result.Address)
			}
			if calls != 1 {
				t.Fatalf("requests=%d, redirects must not be followed", calls)
			}
		})
	}
}

func TestInterfacesAndDialFailures(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		addresses             []netip.Addr
		interfaceErr, dialErr error
		state                 heronv1.AddressDetectionState
		calls                 int
	}{
		{"absent", nil, nil, nil, 2, 0},
		{"loopback_linklocal", []netip.Addr{netip.MustParseAddr("::1"), netip.MustParseAddr("fe80::1")}, nil, nil, 2, 0},
		{"enumeration_error", nil, errors.New("interfaces"), nil, 3, 0},
		{"no_route", []netip.Addr{netip.MustParseAddr("fd00::1")}, nil, syscall.ENETUNREACH, 2, 1},
		{"no_family", []netip.Addr{netip.MustParseAddr("fd00::1")}, nil, syscall.EAFNOSUPPORT, 2, 1},
		{"timeout", []netip.Addr{netip.MustParseAddr("fd00::1")}, nil, context.DeadlineExceeded, 3, 1},
		{"dns", []netip.Addr{netip.MustParseAddr("fd00::1")}, nil, &net.DNSError{Err: "not found", IsNotFound: true}, 3, 1},
		{"dns_no_route", []netip.Addr{netip.MustParseAddr("fd00::1")}, nil, &net.DNSError{Err: "network unreachable", UnwrapErr: syscall.ENETUNREACH}, 3, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			d := newDetector(testAddresses, func(context.Context, string, string) (net.Conn, error) { calls++; return nil, tc.dialErr })
			result := d.detect(t.Context(), 1, tc.addresses, tc.interfaceErr)
			if result.State != tc.state || result.Address != "" || result.CheckedAt <= 0 || calls != tc.calls {
				t.Fatalf("result=%v calls=%d", result, calls)
			}
		})
	}
}

func TestRefreshClearsOldAddressAndSnapshotsAreIndependent(t *testing.T) {
	var mu sync.Mutex
	var fail bool
	d := localDetector(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			w.WriteHeader(500)
		} else {
			io.WriteString(w, "8.8.8.8")
		}
	})
	d.refresh(t.Context())
	first := d.Snapshot()
	if first.GetIpv4().GetAddress() != "8.8.8.8" || first.GetIpv6().GetState() != 3 {
		t.Fatalf("first=%v", first)
	}
	first.Ipv4.Address = "changed"
	if d.Snapshot().GetIpv4().Address != "8.8.8.8" {
		t.Fatal("snapshot aliases detector")
	}
	mu.Lock()
	fail = true
	mu.Unlock()
	d.refresh(t.Context())
	if got := d.Snapshot().Ipv4; got.State != 3 || got.Address != "" {
		t.Fatalf("stale address after failure=%v", got)
	}
}

func TestTransportPolicyAndCancellation(t *testing.T) {
	d := New()
	for _, client := range d.clients {
		if client.Timeout != 10*time.Second || client.Transport.(*http.Transport).Proxy != nil {
			t.Fatalf("unsafe transport=%+v", client)
		}
	}
	d = localDetector(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	addresses, _ := testAddresses()
	if got := d.detect(ctx, 0, addresses, nil); got.State != 3 {
		t.Fatalf("timeout=%v", got)
	}
	ctx, cancel = context.WithCancel(t.Context())
	cancel()
	d.Run(ctx)
	if d.Snapshot().Ipv4 != nil {
		t.Fatal("canceled detector must not probe")
	}
}

func TestRunProbesBothFamiliesImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := make(chan string, 2)
	d := newDetector(testAddresses, func(ctx context.Context, network, address string) (net.Conn, error) {
		calls <- network
		return nil, syscall.ENETUNREACH
	})
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	seen := map[string]int{}
	for range 2 {
		select {
		case network := <-calls:
			seen[network]++
		case <-time.After(time.Second):
			t.Fatal("initial probe did not run")
		}
	}
	cancel()
	<-done
	if seen["tcp4"] != 1 || seen["tcp6"] != 1 {
		t.Fatalf("families=%v", seen)
	}
	if got := d.Snapshot(); got.GetIpv4().GetState() != 2 || got.GetIpv6().GetState() != 2 {
		t.Fatalf("snapshot=%v", got)
	}
}

func TestTLSVerificationFailureIsFailed(t *testing.T) {
	d := localDetector(t, func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted TLS request reached HTTP handler") })
	d.clients[0].Transport.(*http.Transport).TLSClientConfig = &tls.Config{ServerName: "invalid.example"}
	addresses, _ := testAddresses()
	if got := d.detect(t.Context(), 0, addresses, nil); got.State != 3 || got.Address != "" {
		t.Fatalf("TLS error=%v", got)
	}
}
