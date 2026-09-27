// Package geo 按节点的来源地址查国家 / 地区（§4.9）：运维在设置里开启后，hub 把公网来源地址逐个发给 geo.url，
// 把应答与所查地址成对写回节点。
package geo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/store"
)

const (
	// SweepEvery 是查询器巡检节点的间隔。来源地址随分钟行刷出落盘（WriteMinuteBatch），地址变化到重查至多再等一个
	// 间隔加上正在进行的那一轮：一轮里的查询串行发出，最坏是待查节点数乘以客户端的总超时。
	SweepEvery = 30 * time.Second
	// RetryAfter 是一次失败之后同一节点、同一地址、同一服务地址的退避：没有退避，一个坏链路（服务宕机、限流、返回
	// 错误页）会让每一轮巡检都对同一地址外呼一次。
	RetryAfter = time.Hour
	// answersPerNode 是查询器为每个节点记住答案的地址数，保留最近用到的那几个。v4 与 v6 交替上报的节点在两个地址
	// 之间来回，记住它们，来回切换就命中而不外呼；上界让出口不断变化的节点不会让表无限增长。
	answersPerNode = 4
	// maxResponseBytes 是读取应答体的上限。合法应答是两个字母加少量空白，超出即判失败，不再往下读。
	maxResponseBytes = 64
)

// Placeholder 是 geo.url 里被替换为地址的占位。
const Placeholder = "{ip}"

// special 是 netip 的谓词（见 IsPublic）之外的非公网段。取舍的标准是 IANA 的两张登记表——IANA IPv4 Special-Purpose
// Address Registry 与 IANA IPv6 Special-Purpose Address Registry——里 Globally Reachable 为 False 的条目：这些地址
// 在公网上没有归属，自然也没有国家。段内登记表另标为全球可达的更细分配列在 except，按公网处理。
//
// 登记表之外的取舍：
//   - fec0::/10 是 RFC 3879 废弃的站点本地地址，不在特殊用途登记表里（记在 IPv6 地址空间登记表），语义与 ULA 相同，
//     按非公网处理。
//   - 6to4（2002::/16）与 Teredo（2001::/32）登记表标为 N/A，NAT64 知名前缀（64:ff9b::/96）标为全球可达，三者都按
//     公网处理：它们把 IPv4 地址编进 IPv6 地址，这里按外层地址判定、不解出内嵌的 IPv4。按各自的定义，内嵌的都是
//     公网 IPv4：6to4 编进站点全球唯一的 IPv4（RFC 3056），Teredo 编进 Teredo 服务器与客户端 NAT 的外部地址
//     （RFC 4380），知名前缀不得用来表示非全球的 IPv4（RFC 6052 §3.1）。内嵌私网 IPv4 的写法（如 2002:a00:1::）
//     不是有效部署，出现时照查。
var special = []struct {
	prefix netip.Prefix
	except []netip.Prefix
}{
	{prefix: netip.MustParsePrefix("0.0.0.0/8")},     // "本网络"，RFC 791；含未指定地址
	{prefix: netip.MustParsePrefix("100.64.0.0/10")}, // CGNAT，RFC 6598
	{prefix: netip.MustParsePrefix("192.0.0.0/24"), except: []netip.Prefix{ // IETF 协议分配，RFC 6890
		netip.MustParsePrefix("192.0.0.9/32"),  // PCP 任播，RFC 7723
		netip.MustParsePrefix("192.0.0.10/32"), // TURN 任播，RFC 8155
	}},
	{prefix: netip.MustParsePrefix("192.0.2.0/24")},    // 文档，RFC 5737
	{prefix: netip.MustParsePrefix("198.18.0.0/15")},   // 基准测试，RFC 2544
	{prefix: netip.MustParsePrefix("198.51.100.0/24")}, // 文档，RFC 5737
	{prefix: netip.MustParsePrefix("203.0.113.0/24")},  // 文档，RFC 5737
	{prefix: netip.MustParsePrefix("240.0.0.0/4")},     // 保留，RFC 1112；含受限广播
	{prefix: netip.MustParsePrefix("64:ff9b:1::/48")},  // 本地 NAT64，RFC 8215
	{prefix: netip.MustParsePrefix("100::/64")},        // 丢弃，RFC 6666
	{prefix: netip.MustParsePrefix("2001::/23"), except: []netip.Prefix{ // IETF 协议分配，RFC 2928；含基准测试 2001:2::/48（RFC 5180）
		netip.MustParsePrefix("2001::/32"),       // Teredo，RFC 4380：登记表标为 N/A，按公网处理（见上）
		netip.MustParsePrefix("2001:1::1/128"),   // PCP 任播，RFC 7723
		netip.MustParsePrefix("2001:1::2/128"),   // TURN 任播，RFC 8155
		netip.MustParsePrefix("2001:1::3/128"),   // DNS-SD 服务注册协议任播，RFC 9665
		netip.MustParsePrefix("2001:3::/32"),     // AMT，RFC 7450
		netip.MustParsePrefix("2001:4:112::/48"), // AS112-v6，RFC 7535
		netip.MustParsePrefix("2001:20::/28"),    // ORCHIDv2，RFC 7343
		netip.MustParsePrefix("2001:30::/28"),    // 无人机远程识别实体标签（DET），RFC 9374
	}},
	{prefix: netip.MustParsePrefix("2001:db8::/32")}, // 文档，RFC 3849
	{prefix: netip.MustParsePrefix("3fff::/20")},     // 文档，RFC 9637
	{prefix: netip.MustParsePrefix("5f00::/16")},     // SRv6 SID，RFC 9602
	{prefix: netip.MustParsePrefix("fec0::/10")},     // 废弃的站点本地，RFC 3879（见上）
}

// IsPublic 报告 addr 是否是值得查国家的公网地址。非公网地址不发出查询：这类地址没有国家，发出去只是把内网拓扑
// 交给第三方。RFC 1918 与 ULA（IsPrivate）、回环、链路本地、组播、未指定由 netip 的谓词判定，其余非公网段列在
// special。IPv4 映射的 IPv6 地址按其 IPv4 判定：来源地址已由 auth.SourceText 还原，这里再还原一次，让本函数对任何
// 写法都给同一个答案。
func IsPublic(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() || addr.IsPrivate() {
		return false
	}
	for _, sp := range special {
		if sp.prefix.Contains(addr) && !slices.ContainsFunc(sp.except, func(p netip.Prefix) bool { return p.Contains(addr) }) {
			return false
		}
	}
	return true
}

// Target 把地址填进服务地址的占位。地址取 netip 的文本（数字、十六进制字母、点与冒号），在路径与查询串里都无需转义。
func Target(tmpl string, addr netip.Addr) string {
	return strings.ReplaceAll(tmpl, Placeholder, addr.WithZone("").String())
}

// Resolver 是 hub 内唯一的国家查询者，由 Run 在一个协程里串行调用 Sweep。
type Resolver struct {
	store  *store.Store
	client *http.Client
	clk    clock.Clock
	log    *slog.Logger
	// 下面两张表只在内存、只由 Sweep 读写（Sweep 不并发，不加锁）。hub 重启后清空，尚无答案的地址各重查一次。
	//
	// answers 是每个节点记住的答案，最近用到的地址在前，至多 answersPerNode 个。库里只存节点当前地址的那一对
	// （country、country_ip），地址一变就清空；节点换回之前查过的地址时由这里写回、不再外呼，每节点每地址至多查一次
	// 靠的是这张表，不是库。
	answers map[int64][]answer
	// retryAt 是每个（节点, 地址, 服务地址）下次允许查询的单调钟时刻，只记失败。键含服务地址：运维换了服务，旧服务
	// 留下的退避不挡住对新服务的查询。用单调钟：墙钟被拨动时退避不会提前结束或拖长（见 clock）。
	retryAt map[target]time.Duration
}

type answer struct{ addr, country string }

type target struct {
	node int64
	addr string
	url  string
}

// New 的 client 应当是通知渠道用的那一个（alert.NewHTTPClient：不跟随重定向、带总超时），hub 的出站行为只有一套。
func New(st *store.Store, client *http.Client, clk clock.Clock, log *slog.Logger) *Resolver {
	return &Resolver{store: st, client: client, clk: clk, log: log, answers: map[int64][]answer{}, retryAt: map[target]time.Duration{}}
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

// Sweep 查一轮：对来源地址是公网、库里尚无该地址答案（last_source != country_ip）的节点，记住过这个地址的答案就
// 直接写回，没有且不在退避期就按服务地址发出一次查询。
//
// 不变式：每次外呼都由发出时刻的开关与服务地址授权。一轮开头读一次设置，此后每个待查节点处理之前再读一次
// （GeoSettings 是读池上的一条 SELECT），读到关闭即结束本轮，服务地址取这次读到的值。查询在本协程里串行发出，
// 所以关闭之后至多还有一个在途请求，上界是客户端的总超时。一轮最坏是待查节点数乘以总超时，只在开头读一次、
// 整轮沿用，关闭开关或换服务就要等这么久才生效。
func (r *Resolver) Sweep(ctx context.Context) error {
	settings, err := r.store.GeoSettings(ctx)
	if err != nil || !settings.Enabled {
		return err
	}
	nodes, err := r.store.ListNodes(ctx)
	if err != nil {
		return err
	}
	listed := make(map[int64]bool, len(nodes))
	pending := map[target]bool{}
	for _, n := range nodes {
		listed[n.ID] = true
		if n.LastSource == "" || n.LastSource == n.CountryIP {
			continue
		}
		addr, err := netip.ParseAddr(n.LastSource)
		if err != nil || !IsPublic(addr) {
			continue
		}
		if settings, err = r.store.GeoSettings(ctx); err != nil || !settings.Enabled {
			return err
		}
		if country, ok := r.recall(n.ID, n.LastSource); ok {
			if err := r.write(ctx, n, country); err != nil {
				return err
			}
			continue
		}
		k := target{n.ID, n.LastSource, settings.URL}
		pending[k] = true
		if at, ok := r.retryAt[k]; ok && r.clk.Mono() < at {
			continue
		}
		country, err := r.lookup(ctx, settings.URL, addr)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			r.retryAt[k] = r.clk.Mono() + RetryAfter
			r.log.Warn("country lookup failed", "node", n.ID, "addr", n.LastSource, "retry_after", RetryAfter, "err", err)
			continue
		}
		delete(r.retryAt, k)
		r.remember(n.ID, n.LastSource, country)
		if err := r.write(ctx, n, country); err != nil {
			return err
		}
	}
	// 两张表只为仍在的节点保留：节点删除后它的答案与退避在这里丢掉。退避另外只保留这一轮仍待查的（节点, 地址,
	// 服务地址）：节点换了地址、已有答案、运维换了服务地址之后的旧条目都丢掉，表不随地址或服务的变化增长。读到关闭时
	// Sweep 在这之前就返回了，关闭期间的旧条目到重新开启后的第一轮才丢；关闭期间的巡检不添条目，表也不增长。
	for id := range r.answers {
		if !listed[id] {
			delete(r.answers, id)
		}
	}
	for k := range r.retryAt {
		if !pending[k] {
			delete(r.retryAt, k)
		}
	}
	return nil
}

// recall 返回节点在 addr 上记住的答案，命中的地址移到最前：表里保留的是节点最近用到的地址。
func (r *Resolver) recall(node int64, addr string) (string, bool) {
	as := r.answers[node]
	for i, a := range as {
		if a.addr == addr {
			copy(as[1:i+1], as[:i])
			as[0] = a
			return a.country, true
		}
	}
	return "", false
}

// remember 把节点在 addr 上查得的答案放到最前，超出 answersPerNode 的最久未用的地址丢掉。只在 recall 对同一个
// 地址未命中之后调用，表里没有 addr，不会出现重复的地址。
func (r *Resolver) remember(node int64, addr, country string) {
	as := append([]answer{{addr, country}}, r.answers[node]...)
	r.answers[node] = as[:min(len(as), answersPerNode)]
}

// write 把答案写回库。答案对节点被列出时的地址成立；写入之前节点换了地址或被删除时不写，只记日志。
func (r *Resolver) write(ctx context.Context, n store.Node, country string) error {
	set, err := r.store.SetLookupCountry(ctx, n.ID, n.LastSource, country)
	switch {
	case errors.Is(err, store.ErrNotFound):
		r.log.Info("country answer dropped: node deleted before it was written", "node", n.ID, "addr", n.LastSource)
	case err != nil:
		return err
	case !set:
		r.log.Info("country answer dropped: node address changed before it was written", "node", n.ID, "addr", n.LastSource)
	}
	return nil
}

var errNotCountry = errors.New("response is not two uppercase letters")

// lookup 发出一次查询。请求只带地址：GET、无请求体，不设任何凭据头；服务地址不含用户信息由 api 的 UpdateSettings
// 保证。只认 200：客户端不跟随重定向，3xx 在这里按失败处理。
func (r *Resolver) lookup(ctx context.Context, tmpl string, addr netip.Addr) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Target(tmpl, addr), nil)
	if err != nil {
		return "", err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxResponseBytes {
		return "", fmt.Errorf("response longer than %d bytes", maxResponseBytes)
	}
	country := strings.TrimSpace(string(data))
	if !store.IsCountryCode(country) {
		return "", errNotCountry
	}
	return country, nil
}
