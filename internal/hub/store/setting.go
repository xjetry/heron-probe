package store

import (
	"context"
	"database/sql"
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

// SaveSiteSettings 在一个事务里替换外观及显式提供的总闸，失败不发布内存值；总闸缺席表示不变，不写这个键，也不发布。
//
// 不变式：publicEnabled 等于库里最近一次提交的总闸键（publicEnabledKey）。前提有二：Open 从库加载它；hub 运行期间
// 只有这里写这个键（现有离线子命令都不写它；此外改库的途径，如 §6.7 整表覆盖的 restore，必须在 hub 停止时
// 进行，由下次 Open 重新加载）。runWriter 串行提交，但各调用方醒来后的发布顺序不受它约束：不持 siteWriteMu
// 时，两次并发保存可以按 A、B 提交却按 B、A 发布，内存与库从此分叉，直到下一次显式保存总闸或重启。所以写者持锁
// 直到提交与发布都完成。缺席时回显取锁内的内存值，由同一不变式保证它等于库值。读总闸（PublicEnabled）不取
// 这把锁，读到的是最近一次发布的值。
func (s *Store) SaveSiteSettings(ctx context.Context, in SiteSettingsUpdate) (SiteSettings, error) {
	s.siteWriteMu.Lock()
	defer s.siteWriteMu.Unlock()
	st := SiteSettings{SiteAppearance: in.SiteAppearance, PublicEnabled: s.publicEnabled.Load()}
	fields := st.fields()
	enabled := "0"
	if in.PublicEnabled != nil {
		st.PublicEnabled = *in.PublicEnabled
		if st.PublicEnabled {
			enabled = "1"
		}
		fields = append(fields, settingField{publicEnabledKey, &enabled})
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
