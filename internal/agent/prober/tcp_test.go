package prober

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/clock"
)

// DNS 由回环服务作答，名字失败与地址族选择不依赖宿主 DNS 或公网。
func localDNS(t *testing.T, answer bool, before func()) {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	previous := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp4", pc.LocalAddr().String())
	}}
	var once sync.Once
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 1500)
		for {
			n, peer, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 17 {
				continue
			}
			pos := 12
			for pos < n && buf[pos] != 0 {
				pos += int(buf[pos]) + 1
			}
			if pos+5 > n {
				continue
			}
			end := pos + 5
			qtype := binary.BigEndian.Uint16(buf[pos+1 : pos+3])
			msg := append([]byte(nil), buf[:end]...)
			msg[2], msg[3] = 0x81, 0x80
			clear(msg[6:12])
			if answer && (qtype == 1 || qtype == 28) {
				msg[7] = 1
				ip := net.ParseIP("127.0.0.1").To4()
				if qtype == 28 {
					ip = net.ParseIP("::1").To16()
				}
				msg = append(msg, 0xc0, 0x0c, 0, byte(qtype), 0, 1, 0, 0, 0, 0, 0, byte(len(ip)))
				msg = append(msg, ip...)
			} else {
				msg[3] = 0x83
			}
			if before != nil {
				once.Do(before)
			}
			_, _ = pc.WriteTo(msg, peer)
		}
	}()
	t.Cleanup(func() { net.DefaultResolver = previous; pc.Close(); <-done })
}

func tcpListener(t *testing.T) (string, <-chan net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		<-done
		close(accepted)
		for c := range accepted {
			c.Close()
		}
	})
	return ln.Addr().String(), accepted
}

func tcpTask(target string) *probev1.ProbeTask {
	t := task(1)
	t.Kind, t.Target = probev1.ProbeKind_PROBE_KIND_TCP, target
	return t
}

type measuredClock struct {
	*clock.Fake
	mu           sync.Mutex
	calls        int
	step         time.Duration
	beforeSecond func()
}

func (c *measuredClock) Mono() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls == 2 {
		if c.beforeSecond != nil {
			c.beforeSecond()
		}
		c.Advance(c.step)
	}
	return c.Fake.Mono()
}

func TestTCPOutcomes(t *testing.T) {
	addr, _ := tcpListener(t)
	p := TCP{Clock: clock.Real()}
	if out := p.Probe(t.Context(), tcpTask(addr)); out.Err != "" || out.Timeout || out.RttUs == 0 {
		t.Fatalf("loopback=%+v", out)
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	ln.Close()
	if out := p.Probe(t.Context(), tcpTask(closed)); !out.Timeout || out.Err != "" {
		t.Fatalf("refused=%+v", out)
	}
	localDNS(t, false, nil)
	if out := p.Probe(t.Context(), tcpTask("missing.prober.invalid:9")); !strings.Contains(out.Err, "missing.prober.invalid") || out.Timeout {
		t.Fatalf("dns=%+v", out)
	}
	if out := p.Probe(t.Context(), tcpTask("no-port")); out.Err == "" || out.Timeout {
		t.Fatalf("address=%+v", out)
	}
	blackhole := tcpTask("192.0.2.1:9")
	blackhole.TimeoutMs = 100
	p.DialContext = func(ctx context.Context, network, target string) (net.Conn, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 100*time.Millisecond || network != "tcp" || target != blackhole.Target {
			t.Errorf("dial budget/address: deadline=%v valid=%v network=%s target=%s", deadline, ok, network, target)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
			t.Error("dial not bounded by task timeout")
			return nil, context.DeadlineExceeded
		}
	}
	start := time.Now()
	out := p.Probe(t.Context(), blackhole)
	if !out.Timeout || out.Err != "" || time.Since(start) >= time.Second {
		t.Fatalf("blackhole=%+v elapsed=%v", out, time.Since(start))
	}
}

func TestTCPMeasuresOnlyConnectionAndCapsRTT(t *testing.T) {
	addr, accepted := tcpListener(t)
	_, port, _ := net.SplitHostPort(addr)
	clk := &measuredClock{Fake: clock.NewFake(time.Unix(0, 0)), step: time.Millisecond}
	localDNS(t, true, func() { clk.Advance(50 * time.Millisecond) })
	var closedEarly bool
	clk.beforeSecond = func() {
		c := receive(t, accepted)
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
		_, err := c.Read(make([]byte, 1))
		closedEarly = err == io.EOF
	}
	out := (TCP{Clock: clk}).Probe(t.Context(), tcpTask(net.JoinHostPort("local.prober.invalid", port)))
	if out != (Outcome{RttUs: 1000}) || closedEarly {
		t.Fatalf("connection=%+v closed_before_measurement=%v", out, closedEarly)
	}
	late := &measuredClock{Fake: clock.NewFake(time.Unix(0, 0)), step: 1001 * time.Millisecond}
	if out := (TCP{Clock: late}).Probe(t.Context(), tcpTask(addr)); !out.Timeout || out.Err != "" || out.RttUs != 0 {
		t.Fatalf("late=%+v", out)
	}
}

func TestResolveChoosesAvailableFamilies(t *testing.T) {
	localDNS(t, true, nil)
	for _, tc := range []struct {
		host   string
		v4, v6 bool
		want   string
	}{
		{"127.0.0.1", true, false, "127.0.0.1"},
		{"::1", false, true, "::1"},
		{"local.prober.invalid", true, true, "127.0.0.1"},
		{"local.prober.invalid", false, true, "::1"},
	} {
		ip, err := resolve(t.Context(), tc.host, tc.v4, tc.v6)
		if err != nil || ip.String() != tc.want {
			t.Fatalf("resolve %+v = %v/%v", tc, ip, err)
		}
	}
	if _, err := resolve(t.Context(), "local.prober.invalid", false, false); err == nil {
		t.Fatal("resolved without an available family")
	}
}
