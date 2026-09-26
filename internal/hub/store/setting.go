package store

import (
	"context"
	"database/sql"
)

// SiteSettings 是公开页的外观，五项整体读写。存储不校验取值：约束由 api 的 UpdateSettings 裁决，
// 这里只维持"整体替换"——空串照样写入，表示该项回到默认，不存在"不改"。
type SiteSettings struct {
	Title       string
	Theme       string
	AccentColor string
	Logo        string
	CustomCSS   string
}

// DefaultTheme 是从未保存过外观时的明暗：跟随访客系统。
const DefaultTheme = "auto"

type settingField struct {
	key   string
	value *string
}

// 键名是库里的持久标识，改名要迁移。setting 表还会放别的设置，外观只占 site.* 这五个键。
func (st *SiteSettings) fields() []settingField {
	return []settingField{
		{"site.title", &st.Title},
		{"site.theme", &st.Theme},
		{"site.accent_color", &st.AccentColor},
		{"site.logo", &st.Logo},
		{"site.custom_css", &st.CustomCSS},
	}
}

func (s *Store) SiteSettings(ctx context.Context) (SiteSettings, error) {
	out := SiteSettings{Theme: DefaultTheme}
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
	}
	return out, rows.Err()
}

// SaveSiteSettings 在一个事务里写五个键：读侧不会看到新旧混合的外观。
func (s *Store) SaveSiteSettings(ctx context.Context, st SiteSettings) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		for _, f := range st.fields() {
			if _, err := tx.Exec("INSERT INTO setting (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value", f.key, *f.value); err != nil {
				return err
			}
		}
		return nil
	})
}
