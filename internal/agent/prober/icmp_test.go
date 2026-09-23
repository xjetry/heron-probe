package prober

import (
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/clock"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

func TestICMPDatagramWireAndPayload(t *testing.T) {
	e := availableICMP(t, clock.Real())
	for _, family := range []struct {
		network, bind, target  string
		proto                  int
		request                icmp.Type
		requestByte, replyByte byte
	}{
		{"udp4", "0.0.0.0", "127.0.0.1", 1, ipv4.ICMPTypeEcho, 0x08, 0x00},
		{"udp6", "::", "::1", 58, ipv6.ICMPTypeEchoRequest, 0x80, 0x81},
	} {
		t.Run(family.network, func(t *testing.T) {
			pc, err := icmp.ListenPacket(family.network, family.bind)
			if err != nil {
				t.Fatal(err)
			}
			defer pc.Close()
			if err := pc.SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			key := pendingKey{task: 987654321, seq: 123456}
			c := &icmpConn{proto: family.proto, pending: map[pendingKey]chan time.Duration{}}
			reply := make(chan time.Duration, 1)
			c.register(key, reply)
			wire, err := (&icmp.Message{Type: family.request, Body: &icmp.Echo{ID: 0x1234, Seq: 7, Data: e.payload(key.task, key.seq)}}).Marshal(nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pc.WriteTo(wire, &net.UDPAddr{IP: net.ParseIP(family.target)}); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 1500)
			for {
				n, peer, err := pc.ReadFrom(buf)
				if err != nil {
					t.Fatal(err)
				}
				if n == 0 {
					t.Fatal("empty ICMP datagram")
				}
				if buf[0] == family.requestByte {
					continue
				}
				if buf[0] != family.replyByte {
					t.Fatalf("first_byte=0x%02x want=0x%02x raw_len=%d", buf[0], family.replyByte, n)
				}
				e.handle(c, buf[:n], time.Second)
				select {
				case at := <-reply:
					if at != time.Second {
						t.Fatalf("payload match time=%v", at)
					}
				default:
					t.Fatal("loopback payload did not match pending key")
				}
				t.Logf("network=%s first_byte=0x%02x raw_len=%d peer=%s payload_match=true", family.network, buf[0], n, peer)
				break
			}
		})
	}
}

func availableICMP(t *testing.T, clk clock.Clock) *ICMP {
	t.Helper()
	e := NewICMP(clk, logger())
	t.Cleanup(e.Close)
	if !e.Available() {
		t.Skipf("ICMP unavailable: %v", e.InitErrors())
	}
	return e
}

func TestICMPLoopbackAndConcurrentTasks(t *testing.T) {
	e := availableICMP(t, clock.Real())
	for _, target := range []string{"127.0.0.1", "::1"} {
		probe := task(1)
		probe.Target = target
		if out := e.Probe(t.Context(), probe); out.Err != "" || out.Timeout || out.RttUs == 0 {
			t.Fatalf("target=%s outcome=%+v", target, out)
		}
	}
	results := make(chan Outcome, 40)
	var wg sync.WaitGroup
	for _, id := range []uint64{11, 22} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				results <- e.Probe(t.Context(), task(id))
			}
		}()
	}
	wg.Wait()
	close(results)
	for out := range results {
		if out.Err != "" || out.Timeout || out.RttUs == 0 {
			t.Fatalf("concurrent outcome=%+v", out)
		}
	}
}

func TestICMPTimeoutCloseAndRTTLimit(t *testing.T) {
	e := availableICMP(t, clock.Real())
	probe := task(1)
	probe.Target, probe.TimeoutMs = "192.0.2.1", 200
	start := time.Now()
	out := e.Probe(t.Context(), probe)
	if !out.Timeout || out.Err != "" || time.Since(start) >= time.Second {
		t.Fatalf("blackhole=%+v elapsed=%v", out, time.Since(start))
	}
	e.Close()
	if out := e.Probe(t.Context(), task(1)); out.Err == "" || out.Timeout {
		t.Fatalf("closed=%+v", out)
	}
	late := availableICMP(t, &measuredClock{Fake: clock.NewFake(time.Unix(0, 0)), step: 1001 * time.Millisecond})
	if out := late.Probe(t.Context(), task(1)); !out.Timeout || out.Err != "" || out.RttUs != 0 {
		t.Fatalf("late=%+v", out)
	}
}

func TestICMPPayloadAndPendingIsolation(t *testing.T) {
	e := &ICMP{nonce: [8]byte{1, 2, 3, 4, 5, 6, 7, 8}}
	key := pendingKey{task: 0x123456789abcdef0, seq: 0x12345678}
	payload := e.payload(key.task, key.seq)
	if got, ok := e.parsePayload(payload); !ok || got != key || len(payload) != payloadLen {
		t.Fatalf("roundtrip=%v/%v len=%d", got, ok, len(payload))
	}
	for _, b := range [][]byte{nil, payload[:payloadLen-1], append(append([]byte(nil), payload...), 0)} {
		if _, ok := e.parsePayload(b); ok {
			t.Fatalf("accepted length=%d", len(b))
		}
	}
	c := &icmpConn{proto: 1, pending: map[pendingKey]chan time.Duration{}}
	reply := make(chan time.Duration, 1)
	c.register(key, reply)
	foreign := append([]byte(nil), payload...)
	foreign[0] ^= 1
	if parsed, ok := e.parsePayload(foreign); ok {
		c.deliver(parsed, time.Second)
	}
	select {
	case <-reply:
		t.Fatal("foreign nonce delivered to own pending key")
	default:
	}
	c.deliver(pendingKey{task: key.task + 1, seq: key.seq}, time.Second)
	c.deliver(pendingKey{task: key.task, seq: key.seq + 1}, time.Second)
	select {
	case <-reply:
		t.Fatal("foreign key delivered")
	default:
	}
	c.deliver(key, 2*time.Second)
	c.deliver(key, 3*time.Second)
	if at := receive(t, reply); at != 2*time.Second {
		t.Fatalf("duplicate delivery=%v", at)
	}
	c.unregister(key)
	c.deliver(key, time.Second)
	select {
	case <-reply:
		t.Fatal("unregistered key delivered")
	default:
	}
}

func TestICMPAcceptsOnlyEchoRepliesRegardlessOfID(t *testing.T) {
	e := &ICMP{nonce: [8]byte{42}}
	for _, family := range []struct {
		proto          int
		request, reply icmp.Type
	}{
		{1, ipv4.ICMPTypeEcho, ipv4.ICMPTypeEchoReply},
		{58, ipv6.ICMPTypeEchoRequest, ipv6.ICMPTypeEchoReply},
	} {
		c := &icmpConn{proto: family.proto, pending: map[pendingKey]chan time.Duration{}}
		ch := make(chan time.Duration, 1)
		c.register(pendingKey{task: 9, seq: 123}, ch)
		wire, err := (&icmp.Message{Type: family.request, Body: &icmp.Echo{ID: 999, Seq: 1, Data: e.payload(9, 123)}}).Marshal(nil)
		if err != nil {
			t.Fatal(err)
		}
		e.handle(c, wire, time.Second)
		select {
		case <-ch:
			t.Fatal("echo request treated as reply")
		default:
		}
		wire, err = (&icmp.Message{Type: family.reply, Body: &icmp.Echo{ID: 999, Seq: 1, Data: e.payload(9, 123)}}).Marshal(nil)
		if err != nil {
			t.Fatal(err)
		}
		e.handle(c, wire, 2*time.Second)
		if at := receive(t, ch); at != 2*time.Second {
			t.Fatalf("echo reply time=%v", at)
		}
	}
}

func TestICMPUnavailableExplainsSocketFailures(t *testing.T) {
	e := &ICMP{clk: clock.Real(), initErr: []string{"udp4: denied", "ip4:icmp: denied"}}
	out := e.Probe(t.Context(), task(1))
	if e.Available() || out.Timeout || !strings.Contains(out.Err, "udp4: denied; ip4:icmp: denied") {
		t.Fatalf("unavailable=%+v", out)
	}
	errs := e.InitErrors()
	errs[0] = "changed"
	if e.InitErrors()[0] != "udp4: denied" {
		t.Fatal("initialization diagnostics aliased")
	}
}
