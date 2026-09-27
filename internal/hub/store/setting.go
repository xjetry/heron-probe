package store

import (
	"context"
	"database/sql"
	"fmt"
)

// SiteSettings 是已解析默认值的完整设置，外观约束由 api 的 UpdateSettings 裁决。
type SiteSettings struct {
	Title         string
	Theme         string
	AccentColor   string
	Logo          string
	CustomCSS     string
	PublicEnabled bool
}

// SiteSettingsUpdate 整体替换外观，标题、主色、logo、自定义 CSS 为空串表示使用内置值；
// 明暗由 api 限定为 auto/light/dark，总闸为 nil 时不修改。
// 更新与读取分用两种类型，避免把缺席误当作关闭，也避免向读者泄漏未解析的值。
type SiteSettingsUpdate struct {
	Title         string
	Theme         string
	AccentColor   string
	Logo          string
	CustomCSS     string
	PublicEnabled *bool
}

// DefaultTheme 是从未保存过外观时的明暗：跟随访客系统。
const DefaultTheme = "auto"

type settingField struct {
	key   string
	value *string
}

// 键名是库里的持久标识，改名要迁移；这里列出外观字符串，总闸单独编码为 0/1。
func (st *SiteSettings) fields() []settingField {
	return []settingField{
		{"site.title", &st.Title},
		{"site.theme", &st.Theme},
		{"site.accent_color", &st.AccentColor},
		{"site.logo", &st.Logo},
		{"site.custom_css", &st.CustomCSS},
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
		if k == "site.public_enabled" {
			if v != "0" && v != "1" {
				return SiteSettings{}, fmt.Errorf("site.public_enabled must be 0 or 1; got %q", v)
			}
			out.PublicEnabled = v == "1"
		}
	}
	return out, rows.Err()
}

// PublicEnabled 读启动时加载、SaveSiteSettings 提交成功后发布的内存值。
// 匿名请求与静态资源都要检查总闸，读原子副本避免每次准入都占用数据库连接。
func (s *Store) PublicEnabled() bool { return s.publicEnabled.Load() }

// SaveSiteSettings 在一个事务里替换外观及显式提供的总闸，失败不发布内存值。
// Open 初始化总闸，后续写者均持 siteWriteMu 至提交与发布完成；缺席时在锁内取该副本回显，
// 不写总闸键，避免并发的外观保存覆盖总闸修改。读总闸不取这把锁。
func (s *Store) SaveSiteSettings(ctx context.Context, in SiteSettingsUpdate) (SiteSettings, error) {
	s.siteWriteMu.Lock()
	defer s.siteWriteMu.Unlock()
	st := SiteSettings{Title: in.Title, Theme: in.Theme, AccentColor: in.AccentColor,
		Logo: in.Logo, CustomCSS: in.CustomCSS, PublicEnabled: s.publicEnabled.Load()}
	fields := st.fields()
	enabled := "0"
	if in.PublicEnabled != nil {
		st.PublicEnabled = *in.PublicEnabled
		if st.PublicEnabled {
			enabled = "1"
		}
		fields = append(fields, settingField{"site.public_enabled", &enabled})
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		for _, f := range fields {
			if _, err := tx.Exec("INSERT INTO setting (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value", f.key, *f.value); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return SiteSettings{}, err
	}
	if in.PublicEnabled != nil {
		s.publicEnabled.Store(st.PublicEnabled)
	}
	return st, nil
}
