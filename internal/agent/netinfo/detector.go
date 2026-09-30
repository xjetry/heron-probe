// Package netinfo 异步探测节点的双栈公网出口，不参与指标采集和上报调度。
package netinfo

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"syscall"
	"time"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/netaddr"
	"google.golang.org/protobuf/proto"
)

const (
	endpoint       = "https://api64.ipify.org"
	interval       = 5 * time.Minute
	requestTimeout = 10 * time.Second
	maxResponse    = 64
)

type Detector struct {
	mu         sync.RWMutex
	current    *heronv1.NetworkInfo
	interfaces func() ([]netip.Addr, error)
	clients    [2]*http.Client
	now        func() time.Time
}

func New() *Detector {
	dialer := &net.Dialer{Timeout: requestTimeout}
	return newDetector(interfaceAddresses, dialer.DialContext)
}

func newDetector(addresses func() ([]netip.Addr, error), dial func(context.Context, string, string) (net.Conn, error)) *Detector {
	d := &Detector{interfaces: addresses, now: time.Now, current: &heronv1.NetworkInfo{}}
	for i, family := range []string{"tcp4", "tcp6"} {
		transport := &http.Transport{
			// 不继承环境代理；代理回显的是代理的出口，不能代表节点的地址族能力。
			DialContext:         func(ctx context.Context, _, address string) (net.Conn, error) { return dial(ctx, family, address) },
			TLSHandshakeTimeout: requestTimeout,
			DisableKeepAlives:   true,
		}
		d.clients[i] = &http.Client{Transport: transport, Timeout: requestTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return d
}

// Snapshot 返回独立副本；Run 在锁内整体替换，调用方修改 Facts 不会改变探测器的状态。
func (d *Detector) Snapshot() *heronv1.NetworkInfo {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return proto.Clone(d.current).(*heronv1.NetworkInfo)
}

// Run 由入口单独启动，首次立即检测；网络等待不占用 Runner 的采集循环。
func (d *Detector) Run(ctx context.Context) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		d.refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Detector) refresh(ctx context.Context) {
	addresses, err := d.interfaces()
	var results [2]*heronv1.AddressDetection
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() { defer wg.Done(); results[i] = d.detect(ctx, i, addresses, err) }()
	}
	wg.Wait()
	d.mu.Lock()
	d.current = &heronv1.NetworkInfo{Ipv4: results[0], Ipv6: results[1]}
	d.mu.Unlock()
}

func (d *Detector) detect(ctx context.Context, family int, addresses []netip.Addr, interfaceErr error) *heronv1.AddressDetection {
	out := &heronv1.AddressDetection{State: heronv1.AddressDetectionState_ADDRESS_DETECTION_STATE_FAILED}
	defer func() { out.CheckedAt = d.now().Unix() }()
	if interfaceErr != nil {
		return out
	}
	usable := false
	for _, address := range addresses {
		address = address.Unmap()
		// 私网接口可经 NAT 出网；回环与链路本地接口不能证明该族具有公网出口。
		if address.IsGlobalUnicast() && address.Is4() == (family == 0) {
			usable = true
			break
		}
	}
	if !usable {
		out.State = heronv1.AddressDetectionState_ADDRESS_DETECTION_STATE_UNSUPPORTED
		return out
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return out
	}
	resp, err := d.clients[family].Do(req)
	if err != nil {
		var dnsError *net.DNSError
		// DNS 服务器不可达不等于目标地址族不可用；只有实际连接返回的系统错误能证明不支持。
		if !errors.As(err, &dnsError) && (errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EAFNOSUPPORT)) {
			out.State = heronv1.AddressDetectionState_ADDRESS_DETECTION_STATE_UNSUPPORTED
		}
		return out
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil || len(body) > maxResponse {
		return out
	}
	address, valid := netaddr.ParsePublicFamily(strings.TrimSpace(string(body)), family == 0)
	if !valid {
		return out
	}
	out.State, out.Address = heronv1.AddressDetectionState_ADDRESS_DETECTION_STATE_AVAILABLE, address.String()
	return out
}

func interfaceAddresses() ([]netip.Addr, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			return nil, err
		}
		for _, address := range addresses {
			prefix, err := netip.ParsePrefix(address.String())
			if err != nil {
				return nil, err
			}
			out = append(out, prefix.Addr())
		}
	}
	return out, nil
}
