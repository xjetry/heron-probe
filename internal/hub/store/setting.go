package store

import (
	"context"
	"database/sql"
	"fmt"
)

// SiteSettings 是公开页的外观，五项整体读写。存储不校验取值：约束由 api 的 UpdateSettings 裁决，
// 这里只维持"整体替换"——每项都写入，空串也照写，不存在"不改"。标题、主色、logo、自定义 CSS 为空表示
// 用内置的；明暗的取值由 api 的 themes 白名单限定，不会是空串。
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

// 键名是库里的持久标识，改名要迁移。外观只占 site.* 这五个键，国家查询占 geo.* 两个键（见 GeoSettings）。
func (st *SiteSettings) fields() []settingField {
	return []settingField{
		{"site.title", &st.Title},
		{"site.theme", &st.Theme},
		{"site.accent_color", &st.AccentColor},
		{"site.logo", &st.Logo},
		{"site.custom_css", &st.CustomCSS},
	}
}

// GeoSettings 是国家查询（§4.9）的开关与服务地址。与外观同存 setting 表、同一个写入口，但不是整体替换：
// SaveSettings 只写调用方给出的项（GeoUpdate 里非 nil 的），见 GeoUpdate。
type GeoSettings struct {
	Enabled bool
	// URL 含 {ip} 占位，查询时替换为节点的来源地址；取值约束由 api 的 UpdateSettings 裁决。
	URL string
}

// DefaultGeoURL 是从未保存过服务地址时的值。只是默认的目标，不是默认出网：Enabled 默认关。
const DefaultGeoURL = "https://ipinfo.io/{ip}/country"

// GeoUpdate 里 nil 表示不改。国家查询的开关决定 hub 是否把节点地址发给第三方，不认识这两项的客户端（改个标题的
// 脚本、旧面板）按外观的整体替换提交时不得顺手改掉它们：缺席若等于"关"与"默认地址"，一个只带 enabled 的请求会把
// 运维选定的服务换回默认服务并开始向它发送地址。
type GeoUpdate struct {
	Enabled *bool
	URL     *string
}

// 开关存 "0" / "1"（与 §10 公开页总闸同一编码），只由 SaveSettings 写。读到别的值返回错误，不按任一方向猜：别的值
// 只可能来自 hub 之外改库的途径，猜成关会静默停掉运维开启的查询，猜成开会在运维不知情时向第三方发地址。Open 读一次
// 设置（见 openStore），所以这样的库在打开时就被拒绝，而不是等到查询器的第一轮才在日志里报错。
const (
	geoEnabledKey = "geo.enabled"
	geoURLKey     = "geo.url"
)

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// readSettings 用一条 SELECT 读出全部 site.* 与 geo.* 键：单条语句在 WAL 下读同一个快照，SaveSettings 又在
// 一个写事务里写全部键，两者合起来保证读者拿不到新旧混合的设置。改成逐键或分组查询会失去前一半。
func readSettings(ctx context.Context, q querier) (SiteSettings, GeoSettings, error) {
	site := SiteSettings{Theme: DefaultTheme}
	geo := GeoSettings{URL: DefaultGeoURL}
	byKey := map[string]*string{geoURLKey: &geo.URL}
	for _, f := range site.fields() {
		byKey[f.key] = f.value
	}
	rows, err := q.QueryContext(ctx, "SELECT key, value FROM setting WHERE key GLOB 'site.*' OR key GLOB 'geo.*'")
	if err != nil {
		return SiteSettings{}, GeoSettings{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return SiteSettings{}, GeoSettings{}, err
		}
		if k == geoEnabledKey {
			if v != "0" && v != "1" {
				return SiteSettings{}, GeoSettings{}, fmt.Errorf("setting %s must be 0 or 1; got %q", geoEnabledKey, v)
			}
			geo.Enabled = v == "1"
		} else if p := byKey[k]; p != nil {
			*p = v
		}
	}
	if err := rows.Err(); err != nil {
		return SiteSettings{}, GeoSettings{}, err
	}
	return site, geo, nil
}

// Settings 读出外观与国家查询设置，两者来自同一个快照。
func (s *Store) Settings(ctx context.Context) (SiteSettings, GeoSettings, error) {
	return readSettings(ctx, s.r)
}

func (s *Store) SiteSettings(ctx context.Context) (SiteSettings, error) {
	site, _, err := readSettings(ctx, s.r)
	return site, err
}

func (s *Store) GeoSettings(ctx context.Context) (GeoSettings, error) {
	_, geo, err := readSettings(ctx, s.r)
	return geo, err
}

// SaveSettings 在 s.write 的一个事务里写外观五个键与 geo 里给出的项，任一条失败整体回滚：库里不会留下半套设置，
// 与 readSettings 的单条 SELECT 一起保证读侧看不到新旧混合。返回写入后的国家查询设置（未给出的项是库里原值），
// 在同一个事务里读出，所以就是这次写入之后的状态。
func (s *Store) SaveSettings(ctx context.Context, site SiteSettings, geo GeoUpdate) (GeoSettings, error) {
	var out GeoSettings
	err := s.write(ctx, func(tx *sql.Tx) error {
		put := func(key, value string) error {
			_, err := tx.Exec("INSERT INTO setting (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value", key, value)
			return err
		}
		for _, f := range site.fields() {
			if err := put(f.key, *f.value); err != nil {
				return err
			}
		}
		if geo.Enabled != nil {
			enabled := "0"
			if *geo.Enabled {
				enabled = "1"
			}
			if err := put(geoEnabledKey, enabled); err != nil {
				return err
			}
		}
		if geo.URL != nil {
			if err := put(geoURLKey, *geo.URL); err != nil {
				return err
			}
		}
		var err error
		_, out, err = readSettings(ctx, tx)
		return err
	})
	return out, err
}
