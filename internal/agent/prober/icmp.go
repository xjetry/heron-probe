package prober

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/clock"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// ICMP 每个地址族共享一个 socket，数据报不可用时退到 raw。
// Linux 数据报回包 ID 是本地端口，macOS 的公网回包 ID 也可能被改写；因此只按 payload 匹配。
// raw 与 macOS udp6 会收到自己的请求，macOS 数据报 socket 会收到同进程其他 socket 的回包；
// 单读协程只接受 Echo Reply，陌生 nonce 或任务序号不会交给 pending。
// x/net v0.58.0 在 Darwin 为 udp4 设置 IP_STRIPHDR，ReadFrom 返回的报文不需要剥 IP 头。
type ICMP struct {
	clk       clock.Clock
	log       *slog.Logger
	nonce     [8]byte
	seq       atomic.Uint32
	v4, v6    *icmpConn
	initErr   []string
	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

type icmpConn struct {
	pc      *icmp.PacketConn
	raw     bool
	proto   int
	mu      sync.Mutex
	pending map[pendingKey]chan time.Duration
}

type pendingKey struct {
	task uint64
	seq  uint32
}

func NewICMP(clk clock.Clock, log *slog.Logger) *ICMP {
	e := &ICMP{clk: clk, log: log, done: make(chan struct{})}
	rand.Read(e.nonce[:])
	e.v4 = e.open("udp4", "ip4:icmp", "0.0.0.0", ipv4.ICMPTypeEchoReply.Protocol())
	e.v6 = e.open("udp6", "ip6:ipv6-icmp", "::", ipv6.ICMPTypeEchoReply.Protocol())
	for _, c := range []*icmpConn{e.v4, e.v6} {
		if c != nil {
			e.wg.Add(1)
			go func() { defer e.wg.Done(); e.read(c) }()
		}
	}
	return e
}

// 两种 socket 的失败原因都保留，探测不可用时才能说明权限或协议限制。
func (e *ICMP) open(dgram, raw, addr string, proto int) *icmpConn {
	if pc, err := icmp.ListenPacket(dgram, addr); err == nil {
		return &icmpConn{pc: pc, proto: proto, pending: map[pendingKey]chan time.Duration{}}
	} else {
		e.initErr = append(e.initErr, dgram+": "+err.Error())
	}
	if pc, err := icmp.ListenPacket(raw, addr); err == nil {
		return &icmpConn{pc: pc, raw: true, proto: proto, pending: map[pendingKey]chan time.Duration{}}
	} else {
		e.initErr = append(e.initErr, raw+": "+err.Error())
	}
	return nil
}

// Available 表示初始化时至少一个地址族成功创建 socket，供 facts 报告能力。
func (e *ICMP) Available() bool { return e.v4 != nil || e.v6 != nil }

func (e *ICMP) InitErrors() []string { return slices.Clone(e.initErr) }

func (e *ICMP) Close() {
	e.closeOnce.Do(func() {
		close(e.done)
		for _, c := range []*icmpConn{e.v4, e.v6} {
			if c != nil {
				c.pc.Close()
			}
		}
	})
	e.wg.Wait()
}

const payloadLen = 32 // nonce 8 + task_id 8 + seq 4 + 零填充

func (e *ICMP) payload(task uint64, seq uint32) []byte {
	b := make([]byte, payloadLen)
	copy(b, e.nonce[:])
	binary.BigEndian.PutUint64(b[8:16], task)
	binary.BigEndian.PutUint32(b[16:20], seq)
	return b
}

func (e *ICMP) parsePayload(b []byte) (pendingKey, bool) {
	if len(b) != payloadLen || !bytes.Equal(b[:8], e.nonce[:]) {
		return pendingKey{}, false
	}
	return pendingKey{task: binary.BigEndian.Uint64(b[8:16]), seq: binary.BigEndian.Uint32(b[16:20])}, true
}

func (c *icmpConn) register(key pendingKey, reply chan time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending[key] = reply
}

func (c *icmpConn) unregister(key pendingKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.pending, key)
}

func (c *icmpConn) deliver(key pendingKey, at time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if reply, ok := c.pending[key]; ok {
		select {
		case reply <- at:
		default:
		}
	}
}

func (e *ICMP) Probe(ctx context.Context, t *probev1.ProbeTask) Outcome {
	select {
	case <-e.done:
		return Outcome{Err: "icmp closed"}
	default:
	}
	if !e.Available() {
		return Outcome{Err: "icmp unavailable: " + strings.Join(e.initErr, "; ")}
	}
	timeout := time.Duration(t.GetTimeoutMs()) * time.Millisecond
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ip, err := resolve(ctx, t.GetTarget(), e.v4 != nil, e.v6 != nil)
	if err != nil {
		return Outcome{Err: err.Error()}
	}
	c, typ := e.v4, icmp.Type(ipv4.ICMPTypeEcho)
	if ip.Is6() {
		c, typ = e.v6, ipv6.ICMPTypeEchoRequest
	}
	if c == nil {
		return Outcome{Err: "icmp unavailable: " + strings.Join(e.initErr, "; ")}
	}
	seq := e.seq.Add(1)
	key := pendingKey{task: t.GetId(), seq: seq}
	reply := make(chan time.Duration, 1)
	c.register(key, reply)
	defer c.unregister(key)
	msg := icmp.Message{Type: typ, Body: &icmp.Echo{ID: int(seq & 0xffff), Seq: int(seq & 0xffff), Data: e.payload(t.GetId(), seq)}}
	wire, err := msg.Marshal(nil)
	if err != nil {
		return Outcome{Err: err.Error()}
	}
	var dst net.Addr = &net.UDPAddr{IP: ip.AsSlice()}
	if c.raw {
		dst = &net.IPAddr{IP: ip.AsSlice()}
	}
	sent := e.clk.Mono()
	if _, err := c.pc.WriteTo(wire, dst); err != nil {
		return Outcome{Err: "send: " + err.Error()}
	}
	select {
	case at := <-reply:
		elapsed := at - sent
		if elapsed > timeout {
			return Outcome{Timeout: true}
		}
		return Outcome{RttUs: uint32(elapsed / time.Microsecond)}
	case <-ctx.Done():
		return Outcome{Timeout: true}
	case <-e.done:
		return Outcome{Err: "icmp closed"}
	}
}

func (e *ICMP) read(c *icmpConn) {
	buf := make([]byte, 1500)
	for {
		n, _, err := c.pc.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			e.log.Warn("reading ICMP failed", "err", err)
			continue
		}
		e.handle(c, buf[:n], e.clk.Mono())
	}
}

func (e *ICMP) handle(c *icmpConn, wire []byte, at time.Duration) {
	msg, err := icmp.ParseMessage(c.proto, wire)
	if err != nil || (msg.Type != ipv4.ICMPTypeEchoReply && msg.Type != ipv6.ICMPTypeEchoReply) {
		return
	}
	echo, ok := msg.Body.(*icmp.Echo)
	if !ok {
		return
	}
	if key, ok := e.parsePayload(echo.Data); ok {
		c.deliver(key, at)
	}
}
