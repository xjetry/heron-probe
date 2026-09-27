package store

import (
	"context"
	"database/sql"
	"fmt"
)

// SiteSettings 是公开页设置，整体读写。外观约束由 api 的 UpdateSettings 裁决，
// 这里只维持"整体替换"——每项都写入，空串也照写，不存在"不改"。标题、主色、logo、自定义 CSS 为空表示
// 用内置的；明暗的取值由 api 的 themes 白名单限定，不会是空串。
type SiteSettings struct {
	Title         string
	Theme         string
	AccentColor   string
	Logo          string
	CustomCSS     string
	PublicEnabled bool
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
// 一个写事务里写全部键，两者合起来保证读者拿不到新旧混合的设置。改成逐键查询会失去前一半。
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

// SaveSiteSettings 在一个事务里写全部键，失败不发布内存值。siteWriteMu 覆盖提交与发布，
// 避免并发保存的后一次提交先发布、再被前一次的迟到发布覆盖；读总闸不取这把锁。
func (s *Store) SaveSiteSettings(ctx context.Context, st SiteSettings) error {
	s.siteWriteMu.Lock()
	defer s.siteWriteMu.Unlock()
	enabled := "0"
	if st.PublicEnabled {
		enabled = "1"
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		for _, f := range append(st.fields(), settingField{"site.public_enabled", &enabled}) {
			if _, err := tx.Exec("INSERT INTO setting (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value", f.key, *f.value); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		s.publicEnabled.Store(st.PublicEnabled)
	}
	return err
}
