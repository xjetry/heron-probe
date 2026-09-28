package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/geo"
	"github.com/xjetry/probe/internal/hub/sanitize"
	"github.com/xjetry/probe/internal/hub/store"
)

// 外观的上限（§10）。面板的 web/src/lib/appearance.ts 用同值做提交前提示，由 appearanceLimits.test.ts 对照本文件。
const (
	maxTitleRunes = 64
	// maxTitleBytes 限制清洗前的标题：清洗会去掉控制字符与首尾空白，只限清洗后的字符数，原始标题就没有上限，
	// 装不进解码预算的合法请求也就存在（maxBody 的推导要求每个字段都有字节上限）。
	maxTitleBytes = 1 << 10
	maxLogoBytes  = 128 << 10
	maxCSSBytes   = 64 << 10
	// maxGeoURLBytes 限制国家查询的服务地址，同样是 maxBody 推导的前提。
	maxGeoURLBytes = 2 << 10
)

var (
	themes    = []string{"auto", "light", "dark"}
	logoTypes = []string{"image/png", "image/jpeg", "image/webp", "image/svg+xml"}
	accentRE  = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

// cleanSettings 按组判定并校验一次更新，各组彼此独立（§10）：
//   - 外观五项是一组。proto3 的 string 没有 presence，分不开"没给"与"给了空串"，所以五项任一非空即视为给出
//     （appearanceGiven），给出就整体替换并按整体校验（cleanAppearance：theme 必填，其余为空即清空）。按任一项非空
//     判定，只带 title 不带 theme 的请求得到点名 theme 的错误，而不是被当作"没给外观"静默丢弃。
//   - 总闸、国家查询两项与 backup 是 presence 字段，给出即改、缺席即不变（见 store.SettingsUpdate、cleanGeo；backup
//     各项的 presence 见 cleanBackup）：只改其中一组的客户端不必重发外观，也就不会把它手里可能已过时的外观写回去。
//
// 一组都没给出的请求什么都不会改，返回 InvalidArgument 点名各组，而不是回一个看似成功的空操作。任一项不合约束即返回
// 错误，调用方什么都不写。
func cleanSettings(in *probev1.Settings) (store.SettingsUpdate, error) {
	if in == nil {
		in = &probev1.Settings{}
	}
	out := store.SettingsUpdate{PublicEnabled: in.PublicEnabled}
	if appearanceGiven(in) {
		appearance, err := cleanAppearance(in)
		if err != nil {
			return store.SettingsUpdate{}, err
		}
		out.Appearance = &appearance
	}
	geoUpdate, err := cleanGeo(in)
	if err != nil {
		return store.SettingsUpdate{}, err
	}
	out.Geo = geoUpdate
	if out.Backup, err = cleanBackup(in.GetBackup()); err != nil {
		return store.SettingsUpdate{}, err
	}
	if out.Appearance == nil && out.PublicEnabled == nil && out.Geo.Enabled == nil && out.Geo.URL == nil && out.Backup == nil {
		return store.SettingsUpdate{}, invalid("settings must give at least one group: the appearance (title, theme, accent_color, logo, custom_css; given when any of them is non-empty), public_enabled, the country lookup (geo_enabled, geo_url), or backup")
	}
	return out, nil
}

// appearanceGiven 是外观这一组"给出"的判定：五项任一非空。Settings 新增外观字段时要同时加进这里与 cleanAppearance，
// TestUpdateSettingsEveryAppearanceFieldGivesTheGroup 按 proto 描述逐个核对没有 presence 的字符串字段。
func appearanceGiven(in *probev1.Settings) bool {
	return in.GetTitle() != "" || in.GetTheme() != "" || in.GetAccentColor() != "" || in.GetLogo() != "" || in.GetCustomCss() != ""
}

// cleanAppearance 校验并清洗给出的外观，返回可以原样存储与下发的值。
// 标题会显示在页面与标签页上，与节点名（cleanName）同用 sanitize.Text 清洗；logo 与 CSS 是数据与代码，改写任何字节都可能改变含义，只校验不清洗。
func cleanAppearance(in *probev1.Settings) (store.SiteAppearance, error) {
	if n := len(in.GetTitle()); n > maxTitleBytes {
		return store.SiteAppearance{}, invalid("settings.title must be at most %d bytes before cleaning; got %d", maxTitleBytes, n)
	}
	title := sanitize.Text(in.GetTitle(), len(in.GetTitle()))
	if n := utf8.RuneCountInString(title); n > maxTitleRunes {
		return store.SiteAppearance{}, invalid("settings.title must be at most %d characters after removing control characters and surrounding whitespace; got %d", maxTitleRunes, n)
	}
	if !slices.Contains(themes, in.GetTheme()) {
		return store.SiteAppearance{}, invalid("settings.theme must be one of %s; got %q", strings.Join(themes, ", "), in.GetTheme())
	}
	if c := in.GetAccentColor(); c != "" && !accentRE.MatchString(c) {
		return store.SiteAppearance{}, invalid("settings.accent_color must be empty (the default color) or #rrggbb with six hex digits; got %q", c)
	}
	if err := checkLogo(in.GetLogo()); err != nil {
		return store.SiteAppearance{}, err
	}
	if err := checkCSS(in.GetCustomCss()); err != nil {
		return store.SiteAppearance{}, err
	}
	return store.SiteAppearance{
		Title: title, Theme: in.GetTheme(), AccentColor: strings.ToLower(in.GetAccentColor()),
		Logo: in.GetLogo(), CustomCSS: in.GetCustomCss(),
	}, nil
}

// checkLogo 只接受 data:<type>;base64,<data> 这一种写法：type 在白名单内、全小写、不带参数，data 是带填充的
// 标准 base64。写法收窄到一种，"是不是白名单里的图片"就只有一个答案——宽松解析与浏览器的解析一旦不一致
// （参数、大小写、非 base64 形态），白名单就能被绕过。公开页只把它放进 <img src>，SVG 在 <img> 里不执行脚本。
func checkLogo(logo string) error {
	if logo == "" {
		return nil
	}
	if len(logo) > maxLogoBytes {
		return invalid("settings.logo must be at most %d bytes as a data: URL; got %d", maxLogoBytes, len(logo))
	}
	rest, ok := strings.CutPrefix(logo, "data:")
	mediaType, data, found := strings.Cut(rest, ";base64,")
	if !ok || !found || !slices.Contains(logoTypes, mediaType) {
		return invalid("settings.logo must be empty or data:<type>;base64,<data> with <type> one of %s; got a value starting with %q", strings.Join(logoTypes, ", "), head(logo))
	}
	const want = "settings.logo: the data after ;base64, must be non-empty standard base64 (A–Z, a–z, 0–9, + and /, padded with =)"
	// 标准库的解码器会跳过 \r 与 \n，先逐字节核对字母表，换行与空白都不算合法数据。
	if data == "" || strings.IndexFunc(data, func(r rune) bool { return !isBase64Char(r) }) >= 0 {
		return invalid("%s", want)
	}
	if _, err := base64.StdEncoding.Strict().DecodeString(data); err != nil {
		return invalid("%s: %v", want, err)
	}
	return nil
}

func isBase64Char(r rune) bool {
	return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '+' || r == '/' || r == '='
}

// head 是错误信息里回显的开头：到第一个逗号为止、最多 64 字节，不把整张图片写进错误。
func head(s string) string {
	if i := strings.IndexByte(s, ','); i >= 0 {
		s = s[:i+1]
	}
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

// checkCSS 拒绝字面的 "</"。公开页用 textContent 写进 <style>，不经 HTML 解析；但第三方主题与任何把它内联进
// HTML 的消费者都会让 "</style" 结束元素，所以约束放在唯一的写入口。只查字面序列就覆盖了全部大小写变体
// （"</" 本身不含字母）；CSS 转义（\3c/）与 HTML 实体（&lt;/）在 <style> 的 RAWTEXT 里都不被解码，结束不了元素。
func checkCSS(css string) error {
	if len(css) > maxCSSBytes {
		return invalid("settings.custom_css must be at most %d bytes; got %d", maxCSSBytes, len(css))
	}
	if i := strings.Index(css, "</"); i >= 0 {
		return invalid(`settings.custom_css must not contain "</" (it could end the page's <style> element); found at byte %d`, i)
	}
	return nil
}

// geoSamples 是校验服务地址时填入 {ip} 的样例，两个地址族各一个，都取文档地址（RFC 5737、RFC 3849）。
var geoSamples = [2]netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("2001:db8::1")}

// cleanGeo 校验国家查询的两项；缺失的项不改（见 store.GeoUpdate）。服务地址是 hub 的出站目标之一（§4.9），是它
// 唯一的写入口：只接受 http(s)、必须含 {ip}，{ip} 不得在主机或端口位置、必须在路径或查询串里，且不含用户信息——请求只发地址、不带凭据，
// 而 URL 里的用户信息会被 net/http 的客户端转成 Basic 认证的 Authorization 头；这项还会原样回显，本身也不该是凭据。
//
// "{ip} 不在主机或端口位置"按它要保证的性质判定，不去切分 URL 文本：两族样例各填一次，都必须解析成带 host 的绝对
// http(s) URL，而且 host（含端口）相同。host 随地址变化，hub 会连到哪里就读不出来（§4.9），写成 [{ip}] 时 hub 会直连
// IPv6 节点自己的端口；只对一族能解析的模板会让另一族的节点每次查询都失败。按 go1.27 的 url.Parse 实测：裸写在主机
// 位置或与主机名拼在一起（{ip}.example）时 IPv6 样例的冒号让解析失败；[{ip}] 时 IPv4 样例不是合法的 IP 字面量；放在
// IPv6 字面量的区域标识里（[fe80::1%25{ip}]）时两族都能解析、host 不同。这几种都报"不得在主机或端口"。裸写在端口
// 位置时两族都解析失败，与别的非法 URL 分不开，报通用的那条，它同样写明这条约束。
func cleanGeo(in *probev1.Settings) (store.GeoUpdate, error) {
	out := store.GeoUpdate{Enabled: in.GeoEnabled}
	if in.GeoUrl == nil {
		return out, nil
	}
	raw := in.GetGeoUrl()
	if n := len(raw); n > maxGeoURLBytes {
		return store.GeoUpdate{}, invalid("settings.geo_url must be at most %d bytes; got %d", maxGeoURLBytes, n)
	}
	if !strings.Contains(raw, geo.Placeholder) {
		return store.GeoUpdate{}, invalid("settings.geo_url must contain the %s placeholder for the node address; got %q", geo.Placeholder, raw)
	}
	var parsed [2]*url.URL
	for i, sample := range geoSamples {
		if u, err := url.Parse(geo.Target(raw, sample)); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.Opaque == "" {
			parsed[i] = u
		}
	}
	v4, v6 := parsed[0], parsed[1]
	switch {
	case v4 == nil && v6 == nil:
		return store.GeoUpdate{}, invalid("settings.geo_url must be an absolute http:// or https:// URL once {ip} is filled in, with {ip} outside the host and port; got %q", raw)
	case v4 == nil || v6 == nil || v4.Host != v6.Host:
		return store.GeoUpdate{}, invalid("settings.geo_url must not put {ip} in the host or port, including the [{ip}] form: the server the lookup goes to must not depend on the node address; got %q", raw)
	}
	if v6.User != nil {
		return store.GeoUpdate{}, invalid("settings.geo_url must not contain user information (user:password@); the lookup request carries only the address")
	}
	// {ip} 只有在请求真正发出的部分（路径或查询串）才起作用：片段不随请求发出。两族样例填入后请求行仍相同，说明地址
	// 没有进请求，每个节点发出的请求都一样；服务若按请求方地址作答，所有节点都会被记成 hub 所在的国家，且地址不变就不再重查。
	if v4.RequestURI() == v6.RequestURI() {
		return store.GeoUpdate{}, invalid("settings.geo_url must put {ip} in the path or query so the request carries the node address; a fragment (#...) is never sent; got %q", raw)
	}
	out.URL = &raw
	return out, nil
}

func settingsProto(st store.Settings) *probev1.Settings {
	return &probev1.Settings{Title: st.Site.Title, Theme: st.Site.Theme, AccentColor: st.Site.AccentColor, Logo: st.Site.Logo, CustomCss: st.Site.CustomCSS,
		PublicEnabled: proto.Bool(st.Site.PublicEnabled), GeoEnabled: proto.Bool(st.Geo.Enabled), GeoUrl: proto.String(st.Geo.URL), Backup: backupProto(st.Backup)}
}

func (s *Service) GetSettings(ctx context.Context, _ *connect.Request[probev1.GetSettingsRequest]) (*connect.Response[probev1.GetSettingsResponse], error) {
	st, err := s.store.Settings(ctx)
	if err != nil {
		s.log.Error("reading settings failed", "err", err)
		return nil, internalError("reading settings failed")
	}
	return connect.NewResponse(&probev1.GetSettingsResponse{Settings: settingsProto(st)}), nil
}

func (s *Service) UpdateSettings(ctx context.Context, req *connect.Request[probev1.UpdateSettingsRequest]) (*connect.Response[probev1.UpdateSettingsResponse], error) {
	in, err := cleanSettings(req.Msg.GetSettings())
	if err != nil {
		return nil, err
	}
	saved, err := s.store.SaveSettings(ctx, in)
	if err != nil {
		var missing store.NotFoundError
		var outOfRange store.BackupRangeError
		switch {
		case errors.As(err, &missing) && missing.Kind == store.ObjectNotifyChannel:
			return nil, invalid("backup.notify.channel_ids: channel %d does not exist", missing.ID)
		case errors.As(err, &outOfRange):
			return nil, invalid("%s", outOfRange)
		}
		s.log.Error("saving settings failed", "err", err)
		return nil, internalError("saving settings failed")
	}
	return connect.NewResponse(&probev1.UpdateSettingsResponse{Settings: settingsProto(saved)}), nil
}

func (s *Service) GetStorageStats(ctx context.Context, _ *connect.Request[probev1.GetStorageStatsRequest]) (*connect.Response[probev1.GetStorageStatsResponse], error) {
	stats, err := s.store.StorageStats(ctx)
	if err != nil {
		s.log.Error("reading storage stats failed", "err", err)
		return nil, internalError("reading storage stats failed")
	}
	out := &probev1.GetStorageStatsResponse{DbBytes: uint64(stats.DBBytes), LastPruneAt: stats.LastPrune, LastRollupAt: stats.LastRollup}
	for _, t := range stats.Tables {
		out.Tables = append(out.Tables, &probev1.TableRows{Name: t.Name, Rows: uint64(t.Rows)})
	}
	// 标红只在 store.SeriesHealth.Staleness 一处判定，这里原样带上它的结论与判定用的原始数值。
	now := s.clk.Now()
	for _, h := range stats.Series {
		stale := h.Staleness(now, s.cfg.Retention)
		out.Series = append(out.Series, &probev1.SeriesTableHealth{
			Table: h.Table, BucketS: uint32(h.Level.Bucket), RetentionS: uint64(s.cfg.Retention.ForLevel(h.Level.Name) / time.Second),
			OldestTs: h.Oldest, WatermarkTs: h.Watermark, OldestStale: stale.Oldest, WatermarkStale: stale.Watermark,
		})
	}
	return connect.NewResponse(out), nil
}
