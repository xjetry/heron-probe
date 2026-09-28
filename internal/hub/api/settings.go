package api

import (
	"context"
	"encoding/base64"
	"errors"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
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
	// maxChannelIDs 是登录通知渠道列表的条数上限，按请求里的原始条数计、重复也算：去重在解码之后，约束不了
	// 请求的字节数。没有它，合法请求的字节数就没有上界，算不出解码预算的最坏请求（service.go 的 maxBody）。
	maxChannelIDs = 16
)

var (
	themes    = []string{"auto", "light", "dark"}
	logoTypes = []string{"image/png", "image/jpeg", "image/webp", "image/svg+xml"}
	accentRE  = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

// cleanSettings 校验并清洗外观，返回可以原样存储与下发的值；任一项不合约束即返回错误，调用方什么都不写。
// 标题会显示在页面与标签页上，与节点名（cleanName）同用 sanitize.Text 清洗；logo 与 CSS 是数据与代码，改写任何字节都可能改变含义，只校验不清洗。
func cleanSettings(in *probev1.Settings) (store.SiteSettings, error) {
	if n := len(in.GetTitle()); n > maxTitleBytes {
		return store.SiteSettings{}, invalid("settings.title must be at most %d bytes before cleaning; got %d", maxTitleBytes, n)
	}
	title := sanitize.Text(in.GetTitle(), len(in.GetTitle()))
	if n := utf8.RuneCountInString(title); n > maxTitleRunes {
		return store.SiteSettings{}, invalid("settings.title must be at most %d characters after removing control characters and surrounding whitespace; got %d", maxTitleRunes, n)
	}
	if !slices.Contains(themes, in.GetTheme()) {
		return store.SiteSettings{}, invalid("settings.theme must be one of %s; got %q", strings.Join(themes, ", "), in.GetTheme())
	}
	if c := in.GetAccentColor(); c != "" && !accentRE.MatchString(c) {
		return store.SiteSettings{}, invalid("settings.accent_color must be empty (the default color) or #rrggbb with six hex digits; got %q", c)
	}
	if err := checkLogo(in.GetLogo()); err != nil {
		return store.SiteSettings{}, err
	}
	if err := checkCSS(in.GetCustomCss()); err != nil {
		return store.SiteSettings{}, err
	}
	return store.SiteSettings{
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

func settingsProto(st store.SiteSettings) *probev1.Settings {
	return &probev1.Settings{Title: st.Title, Theme: st.Theme, AccentColor: st.AccentColor, Logo: st.Logo, CustomCss: st.CustomCSS}
}

// adminSettingsProto 总带 login_notify，渠道列表为空即关闭。省掉它，"已关闭"与"hub 不认识这个字段"在响应里
// 就分不出来；把读到的整份设置原样写回时，回显的空 message 是显式关闭，与当前状态一致，读改写不改变它。
func adminSettingsProto(st store.Settings) *probev1.Settings {
	out := settingsProto(st.Site)
	out.LoginNotify = &probev1.LoginNotify{ChannelIds: st.LoginChannelIDs}
	return out
}

func (s *Service) GetSettings(ctx context.Context, _ *connect.Request[probev1.GetSettingsRequest]) (*connect.Response[probev1.GetSettingsResponse], error) {
	st, err := s.store.Settings(ctx)
	if err != nil {
		s.log.Error("reading settings failed", "err", err)
		return nil, internalError("reading settings failed")
	}
	return connect.NewResponse(&probev1.GetSettingsResponse{Settings: adminSettingsProto(st)}), nil
}

// appearanceGiven 判定请求是否给出了外观：五项任一非空即给出，按整体替换校验（theme 必须合法，其余为空串即
// 回到内置值）。合法外观必须带 theme，五项全空本来就不是一次有效的外观替换，把它当作"没给外观"不会吞掉任何
// 合法写入；只提交带 presence 的设置分组（login_notify）的请求因此不碰外观，不会用空值覆盖另一页面刚保存的
// 外观。新增的 presence 分组只需各自判定是否在场，不改这里。
func appearanceGiven(in *probev1.Settings) bool {
	return in.GetTitle() != "" || in.GetTheme() != "" || in.GetAccentColor() != "" || in.GetLogo() != "" || in.GetCustomCss() != ""
}

func (s *Service) UpdateSettings(ctx context.Context, req *connect.Request[probev1.UpdateSettingsRequest]) (*connect.Response[probev1.UpdateSettingsResponse], error) {
	in := req.Msg.GetSettings()
	var site *store.SiteSettings
	if appearanceGiven(in) {
		st, err := cleanSettings(in)
		if err != nil {
			return nil, err
		}
		site = &st
	}
	var channels *[]int64
	if ln := in.GetLoginNotify(); ln != nil {
		if n := len(ln.GetChannelIds()); n > maxChannelIDs {
			return nil, invalid("settings.login_notify.channel_ids must list at most %d channel IDs, duplicates included; got %d", maxChannelIDs, n)
		}
		channels = &ln.ChannelIds
	}
	// 一组都没给出的请求什么也改不了。拒绝并点名两组，客户端漏填字段时得到能照着改的错误，而不是一次什么都
	// 没写的"成功"。
	if site == nil && channels == nil {
		return nil, invalid("settings must give the appearance (settings.theme is required, one of %s) or settings.login_notify; got neither", strings.Join(themes, ", "))
	}
	st, err := s.store.UpdateSettings(ctx, site, channels)
	if err != nil {
		var missing store.NotFoundError
		if errors.As(err, &missing) && missing.Kind == store.ObjectNotifyChannel {
			return nil, invalid("settings.login_notify.channel_ids: channel %d does not exist", missing.ID)
		}
		s.log.Error("saving settings failed", "err", err)
		return nil, internalError("saving settings failed")
	}
	return connect.NewResponse(&probev1.UpdateSettingsResponse{Settings: adminSettingsProto(st)}), nil
}

func (s *Service) GetStorageStats(ctx context.Context, _ *connect.Request[probev1.GetStorageStatsRequest]) (*connect.Response[probev1.GetStorageStatsResponse], error) {
	stats, err := s.store.StorageStats(ctx)
	if err != nil {
		s.log.Error("reading storage stats failed", "err", err)
		return nil, internalError("reading storage stats failed")
	}
	out := &probev1.GetStorageStatsResponse{DbBytes: uint64(stats.DBBytes)}
	for _, t := range stats.Tables {
		out.Tables = append(out.Tables, &probev1.TableRows{Name: t.Name, Rows: uint64(t.Rows)})
	}
	return connect.NewResponse(out), nil
}
