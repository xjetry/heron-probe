package prober

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"syscall"
	"testing"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/probelimit"
)

// loopbackTargets 放行本机回环：引擎测试只连本机的服务，不依赖宿主网络。
func loopbackTargets(t *testing.T, r *net.Resolver) Targets {
	t.Helper()
	p, err := ParsePolicy([]string{"127.0.0.0/8", "::1/128"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return Targets{Resolver: r, Policy: p}
}

func TestPolicyDefaults(t *testing.T) {
	var p Policy
	for _, a := range []string{"0.0.0.0", "0.1.2.3", "127.0.0.1", "127.255.255.254", "169.254.169.254", "224.0.0.1", "239.255.255.250", "255.255.255.255", "::", "::1", "fe80::1", "ff02::1", "::ffff:127.0.0.1", "::ffff:169.254.169.254"} {
		if err := p.Check(netip.MustParseAddr(a)); err == nil {
			t.Errorf("%s allowed by the zero policy", a)
		}
	}
	for _, a := range []string{"1.1.1.1", "8.8.8.8", "10.0.0.1", "172.16.0.1", "192.168.1.1", "100.64.0.1", "2606:4700::1111", "fd00::1", "::2", "255.255.255.254"} {
		if err := p.Check(netip.MustParseAddr(a)); err != nil {
			t.Errorf("%s denied by the zero policy: %v", a, err)
		}
	}
}

func TestPolicyLongestPrefixAndLocalOverride(t *testing.T) {
	p, err := ParsePolicy([]string{"127.0.0.0/8", "10.1.0.0/16", "169.254.169.0/24"}, []string{"10.0.0.0/8", "10.1.2.0/24", "fd00::/8"})
	if err != nil {
		t.Fatal(err)
	}
	for a, allowed := range map[string]bool{
		"127.0.0.1":       true,  // 本地放行与默认拒绝等长：本地优先
		"10.9.9.9":        false, // 本地拒绝
		"10.1.9.9":        true,  // 更长的本地放行
		"10.1.2.3":        false, // 再长一级的本地拒绝
		"169.254.169.254": true,  // 本地放行比默认拒绝更长
		"169.254.1.1":     false, // 默认拒绝仍在
		"fd12::1":         false,
		"::1":             false, // 只放行了 v4 回环
		"192.168.0.1":     true,
	} {
		if err := p.Check(netip.MustParseAddr(a)); (err == nil) != allowed {
			t.Errorf("%s: err=%v, want allowed=%v", a, err, allowed)
		}
	}
}

func TestParsePolicyRejects(t *testing.T) {
	for _, tc := range []struct {
		name        string
		allow, deny []string
		want        string
	}{
		{"not_cidr", []string{"10.0.0.1"}, nil, `probe_allow: "10.0.0.1" is not a CIDR prefix`},
		{"host_bits", nil, []string{"10.1.2.3/8"}, "has host bits set; write 10.0.0.0/8"},
		{"both", []string{"10.0.0.0/8"}, []string{"10.0.0.0/8"}, "10.0.0.0/8 is in both probe_allow and probe_deny"},
		{"garbage", nil, []string{"x"}, `probe_deny: "x" is not a CIDR prefix`},
	} {
		if _, err := ParsePolicy(tc.allow, tc.deny); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err=%v, want %q", tc.name, err, tc.want)
		}
	}
}

// 拒绝文本要进 ProbeError.message，最长的情形也不能超出 hub 的上限，否则 agent 截断后面板看不到命中的前缀。
func TestDenialMessageFitsProbeError(t *testing.T) {
	p, err := ParsePolicy(nil, []string{"ffff:ffff:ffff:ffff:ffff:ffff:ffff:fff0/128"})
	if err != nil {
		t.Fatal(err)
	}
	err = p.Check(netip.MustParseAddr("ffff:ffff:ffff:ffff:ffff:ffff:ffff:fff0"))
	if err == nil || len(err.Error()) > probelimit.MaxErrorMessageLen {
		t.Fatalf("denial %q (%d bytes) must fit %d bytes", err, len(err.Error()), probelimit.MaxErrorMessageLen)
	}
}

// 两个引擎都经 Targets：默认策略下回环目标不发包，错误写明地址与前缀；名字解析到被拒地址同样被拒。
func TestEnginesApplyPolicyAfterResolution(t *testing.T) {
	socket := newQuietSocket()
	var sent int
	socket.write = func([]byte, net.Addr) (int, error) { sent++; return 0, syscall.EPERM }
	ic := icmpWithSocket(t, socket)
	ic.Targets = Targets{Resolver: localDNS(t, dnsDual, nil)}
	var dialed int
	tcp := TCP{Clock: clock.Real(), Targets: Targets{Resolver: localDNS(t, dnsDual, nil)}, DialContext: func(context.Context, string, string) (net.Conn, error) {
		dialed++
		return nil, syscall.ECONNREFUSED
	}}
	icmpTask := func(target string) Outcome { p := task(1); p.Target = target; return ic.Probe(t.Context(), p) }
	for name, out := range map[string]Outcome{
		"icmp_literal": icmpTask("127.0.0.1"),
		"icmp_name":    icmpTask("local.prober.invalid"),
		"tcp_literal":  tcp.Probe(t.Context(), tcpTask("127.0.0.1:22")),
		"tcp_name":     tcp.Probe(t.Context(), tcpTask("local.prober.invalid:22")),
		"tcp_mapped":   tcp.Probe(t.Context(), tcpTask("[::ffff:127.0.0.1]:22")),
	} {
		if out.Timeout || out.Err != "target 127.0.0.1 denied by local probe policy (127.0.0.0/8)" {
			t.Errorf("%s: %+v", name, out)
		}
	}
	if sent != 0 || dialed != 0 {
		t.Fatalf("denied targets reached the network: icmp sends=%d tcp dials=%d", sent, dialed)
	}
}
