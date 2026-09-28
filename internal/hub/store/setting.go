package store

import (
	"context"
	"fmt"
)

// SiteAppearance 是公开页外观，UpdateSettings 整体替换它：标题、主色、logo、自定义 CSS 为空串表示使用内置值，
// 明暗由 api 限定为 auto/light/dark；约束由 api 的 UpdateSettings 裁决。读写两种设置类型都嵌入它，保存时整体
// 取用而不逐字段转抄：转抄漏掉的外观项编译照过，每次保存都被写成空串。
type SiteAppearance struct {
	Title       string
	Theme       string
	AccentColor string
	Logo        string
	CustomCSS   string
}

// SiteSettings 是已解析默认值的完整设置。
type SiteSettings struct {
	SiteAppearance
	PublicEnabled bool
}

// SiteSettingsUpdate 整体替换外观；总闸为 nil 时不修改。
// 更新与读取分用两种类型，避免把缺席误当作关闭，也避免向读者泄漏未解析的值。
type SiteSettingsUpdate struct {
	SiteAppearance
	PublicEnabled *bool
}

// DefaultTheme 是从未保存过外观时的明暗：跟随访客系统。
const DefaultTheme = "auto"

// publicEnabledKey 是总闸在 setting 表里的键，值编码为 0/1，读写都经这个常量。键名是库里的持久标识，改名要迁移。
const publicEnabledKey = "site.public_enabled"

type settingField struct {
	key   string
	value *string
}

// fields 列出每项外观的键，SiteSettings 与 SaveSiteSettings 都按它读写：SiteAppearance 新增的字段不在这里登记，
// 就存不进库、读出来恒为空（TestSiteAppearanceRoundTripsEveryField 逐字段核对）。键名是库里的持久标识，改名要迁移。
func (a *SiteAppearance) fields() []settingField {
	return []settingField{
		{"site.title", &a.Title},
		{"site.theme", &a.Theme},
		{"site.accent_color", &a.AccentColor},
		{"site.logo", &a.Logo},
		{"site.custom_css", &a.CustomCSS},
	}
}

// SiteSettings 用一条 SELECT 读出全部 site.* 键：单条语句在 WAL 下读同一个快照，SaveSiteSettings 又在
// 一个写事务里保存更新，两者合起来保证读者拿不到新旧混合的设置。改成逐键查询会失去前一半。
func (s *Store) SiteSettings(ctx context.Context) (SiteSettings, error) {
	// 只有键缺失表示默认开放；非法的已保存值不能被解释成允许公开。
	out := SiteSettings{Theme: DefaultTheme, PublicEnabled: true}
	byKey := map[string]*string{}
	for _, f := range out.fields() {
		byKey[f.key] = f.value
	}
	rows, err := s.r.QueryContext(ctx, "SELECT key, value FROM setting WHERE key GLOB 'site.*'")
	if err != nil {
		return SiteSettings{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return SiteSettings{}, err
		}
		if p := byKey[k]; p != nil {
			*p = v
		}
		if k == publicEnabledKey {
			if v != "0" && v != "1" {
				return SiteSettings{}, fmt.Errorf("%s must be 0 or 1; got %q", publicEnabledKey, v)
			}
			out.PublicEnabled = v == "1"
		}
	}
	return out, rows.Err()
}

// PublicEnabled 读启动时加载、SaveSiteSettings 提交成功后发布的内存值。
// 匿名请求与静态资源都要检查总闸，读原子副本避免每次准入都占用数据库连接。
func (s *Store) PublicEnabled() bool { return s.publicEnabled.Load() }

// SaveSiteSettings 与管理接口共用 SaveSettings 的事务及总闸发布路径；不提交备份配置。
func (s *Store) SaveSiteSettings(ctx context.Context, in SiteSettingsUpdate) (SiteSettings, error) {
	st, _, err := s.SaveSettings(ctx, in, nil)
	return st, err
}
