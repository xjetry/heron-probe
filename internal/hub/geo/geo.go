// Package geo 按公网来源地址查询国家 / 地区，把答案与所查地址成对写回节点。
package geo

import (
	"context"
	"log/slog"
	"net/netip"
	"strings"
	"time"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/store"
)

const (
	// SweepEvery 是查询器巡检节点的间隔。来源地址随分钟行刷出落盘（WriteMinuteBatch），地址变化到重查至多再等一个间隔。
	SweepEvery = 30 * time.Second
	// RetryAfter 是一次失败之后同一节点同一地址的退避：没有退避，一个坏链路（服务宕机、限流、返回错误页）会让每一轮
	// 巡检都对同一地址外呼一次。
	RetryAfter = time.Hour
)

// Placeholder 是 geo.url 里被替换为地址的占位。
const Placeholder = "{ip}"

// reserved 是 netip 的谓词（见 IsPublic）之外的非公网段。
var reserved = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),   // CGNAT，RFC 6598
	netip.MustParsePrefix("0.0.0.0/8"),       // "本网络"，RFC 791；含未指定地址
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF 协议分配，RFC 6890
	netip.MustParsePrefix("192.0.2.0/24"),    // 文档，RFC 5737
	netip.MustParsePrefix("198.51.100.0/24"), // 文档，RFC 5737
	netip.MustParsePrefix("203.0.113.0/24"),  // 文档，RFC 5737
	netip.MustParsePrefix("198.18.0.0/15"),   // 基准测试，RFC 2544
	netip.MustParsePrefix("240.0.0.0/4"),     // 保留，RFC 1112；含受限广播
	netip.MustParsePrefix("64:ff9b:1::/48"),  // 本地 NAT64，RFC 8215
	netip.MustParsePrefix("100::/64"),        // 丢弃，RFC 6666
	netip.MustParsePrefix("2001:db8::/32"),   // 文档，RFC 3849
}

// IsPublic 报告 addr 是否是值得查国家的公网地址。非公网地址不发出查询：这类地址没有国家，发出去只是把内网拓扑
// 交给第三方。RFC 1918 与 ULA（IsPrivate）、回环、链路本地、组播、未指定由 netip 的谓词判定，其余保留段列在 reserved。IPv4 映射的 IPv6 地址按
// 其 IPv4 判定：来源地址已由 auth.SourceText 还原，这里再还原一次，让本函数对任何写法都给同一个答案。
func IsPublic(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() || addr.IsPrivate() {
		return false
	}
	for _, p := range reserved {
		if p.Contains(addr) {
			return false
		}
	}
	return true
}

// IsCountryCode 报告 s 是否恰为两个 ASCII 大写字母，是查询应答与手动指定共用的判定。只接受这一种形状：应答来自
// 第三方，收窄到 [A-Z]{2} 之后它不可能携带标记、文字或别的国家写法（小写、三字母、名称），应答体也就不进入任何
// 解释路径；页面按这两个字母算区域指示符旗帜，计算只对 A–Z 有定义。不核对是否是已分配的 ISO 3166-1 代码。
func IsCountryCode(s string) bool {
	return len(s) == 2 && isUpper(s[0]) && isUpper(s[1])
}

func isUpper(c byte) bool { return c >= 'A' && c <= 'Z' }

// Target 把地址填进服务地址的占位。地址取 netip 的文本（数字、十六进制字母、点与冒号），在路径与查询串里都无需转义。
func Target(tmpl string, addr netip.Addr) string {
	return strings.ReplaceAll(tmpl, Placeholder, addr.WithZone("").String())
}

// Resolver 是 hub 内唯一的国家查询者，由 Run 在一个协程里串行调用 Sweep。
type Resolver struct {
	store   *store.Store
	backend Backend
	clk     clock.Clock
	log     *slog.Logger
	// retryAt 是每个（节点, 地址）下次允许查询的时刻，只记失败。只在内存：hub 重启后清空，尚无答案的地址各重查
	// 一次。只由 Sweep 读写，Sweep 不并发，所以不加锁。
	retryAt map[target]time.Time
}

type target struct {
	node int64
	addr string
}

func New(st *store.Store, backend Backend, clk clock.Clock, log *slog.Logger) *Resolver {
	return &Resolver{store: st, backend: backend, clk: clk, log: log, retryAt: map[target]time.Time{}}
}

func (r *Resolver) Run(ctx context.Context) {
	t := time.NewTicker(SweepEvery)
	defer t.Stop()
	for {
		if err := r.Sweep(ctx); err != nil && ctx.Err() == nil {
			r.log.Error("country lookup sweep failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Sweep 查一轮：开关关闭时什么都不做、不出网；开启时对来源地址是公网、尚无该地址答案（last_source != country_ip）、
// 不在退避期的节点各查一次。查询逐个同步执行，同一个（节点, 地址）在一轮里至多查一次。
// geo.enabled 表达是否给节点标国家，后端仅改变地址是否离开本机，所以两个后端共用开关、准入与退避。
func (r *Resolver) Sweep(ctx context.Context) error {
	settings, err := r.store.GeoSettings(ctx)
	if err != nil || !settings.Enabled {
		return err
	}
	nodes, err := r.store.ListNodes(ctx)
	if err != nil {
		return err
	}
	now := r.clk.Now()
	pending := map[target]bool{}
	for _, n := range nodes {
		if n.LastSource == "" || n.LastSource == n.CountryIP {
			continue
		}
		addr, err := netip.ParseAddr(n.LastSource)
		if err != nil || !IsPublic(addr) {
			continue
		}
		k := target{n.ID, n.LastSource}
		pending[k] = true
		if at, ok := r.retryAt[k]; ok && now.Before(at) {
			continue
		}
		country, err := r.backend.Lookup(ctx, addr)
		if err == nil && !IsCountryCode(country) {
			err = errNotCountry
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			r.retryAt[k] = now.Add(RetryAfter)
			r.log.Warn("country lookup failed", "node", n.ID, "addr", n.LastSource, "retry_after", RetryAfter, "err", err)
			continue
		}
		delete(r.retryAt, k)
		set, err := r.store.SetLookupCountry(ctx, n.ID, n.LastSource, country)
		if err != nil {
			return err
		}
		if !set {
			r.log.Info("country lookup answer dropped: node address changed while querying", "node", n.ID, "addr", n.LastSource)
		}
	}
	// 退避只对仍待查的（节点, 地址）有意义：节点换了地址、被删除、已有答案或查询关闭后的旧条目在这里丢掉，表不随
	// 地址变化增长。
	for k := range r.retryAt {
		if !pending[k] {
			delete(r.retryAt, k)
		}
	}
	return nil
}
