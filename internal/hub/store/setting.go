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

// SiteSettings 是已解析默认值的公开页设置：外观与总闸。国家查询的设置见 GeoSettings。
type SiteSettings struct {
	SiteAppearance
	PublicEnabled bool
}

// SettingsUpdate 是 SaveSettings 的输入：外观整体替换；总闸与 Geo 里的各项为 nil 时不修改。
// 更新与读取分用不同类型，避免把缺席误当作关闭，也避免向读者泄漏未解析的值。
type SettingsUpdate struct {
	SiteAppearance
	PublicEnabled *bool
	Geo           GeoUpdate
}

// DefaultTheme 是从未保存过外观时的明暗：跟随访客系统。
const DefaultTheme = "auto"

// publicEnabledKey 是总闸在 setting 表里的键，值编码见 flagField。键名是库里的持久标识，改名要迁移。
const publicEnabledKey = "site.public_enabled"

type settingField struct {
	key   string
	value *string
}

// fields 列出每项外观的键，readSettings 与 SaveSettings 都按它读写：SiteAppearance 新增的字段不在这里登记，
// 就存不进库、读出来恒为空（TestSiteAppearanceRoundTripsEveryField 逐字段核对）。键名是库里的持久标识，改名要迁移。
// site.* 下除这五个外观键外还有总闸（publicEnabledKey）；国家查询占 geo.* 两个键（见 GeoSettings）。
func (a *SiteAppearance) fields() []settingField {
	return []settingField{
		{"site.title", &a.Title},
		{"site.theme", &a.Theme},
		{"site.accent_color", &a.AccentColor},
		{"site.logo", &a.Logo},
		{"site.custom_css", &a.CustomCSS},
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

// 国家查询开关与总闸同一编码（见 flagField）。它的值不按任一方向猜的理由：猜成关会静默停掉运维开启的查询，猜成开
// 会在运维不知情时向第三方发地址。
const (
	geoEnabledKey = "geo.enabled"
	geoURLKey     = "geo.url"
)

// flagField 与 parseFlag 是开关（总闸、国家查询开关）在 setting 表里的唯一编码："0" / "1"。写只经 SaveSettings
// 调 flagField，所以库里出现别的值只可能来自 hub 之外改库的途径；parseFlag 对它返回错误，不按任一方向猜。Open 读一次
// 设置（见 openStore），所以这样的库在打开时就被拒绝：总闸的内存副本不会从非法值加载，查询器也不会等到第一轮才在
// 日志里报错。
func flagField(key string, on bool) settingField {
	v := "0"
	if on {
		v = "1"
	}
	return settingField{key, &v}
}

func parseFlag(key, v string) (bool, error) {
	if v != "0" && v != "1" {
		return false, fmt.Errorf("setting %s must be 0 or 1; got %q", key, v)
	}
	return v == "1", nil
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// readSettings 用一条 SELECT 读出全部 site.*（外观与总闸）与 geo.* 键：单条语句在 WAL 下读同一个快照，SaveSettings
// 又在一个写事务里写全部给出的键，两者合起来保证读者拿不到新旧混合的设置。改成逐键或分组查询会失去前一半。
func readSettings(ctx context.Context, q querier) (SiteSettings, GeoSettings, error) {
	// 只有键缺失表示默认开放；非法的已保存值不能被解释成允许公开（parseFlag 报错）。
	site := SiteSettings{SiteAppearance: SiteAppearance{Theme: DefaultTheme}, PublicEnabled: true}
	geo := GeoSettings{URL: DefaultGeoURL}
	strs := map[string]*string{geoURLKey: &geo.URL}
	for _, f := range site.fields() {
		strs[f.key] = f.value
	}
	flags := map[string]*bool{publicEnabledKey: &site.PublicEnabled, geoEnabledKey: &geo.Enabled}
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
		if p := flags[k]; p != nil {
			if *p, err = parseFlag(k, v); err != nil {
				return SiteSettings{}, GeoSettings{}, err
			}
		} else if p := strs[k]; p != nil {
			*p = v
		}
	}
	if err := rows.Err(); err != nil {
		return SiteSettings{}, GeoSettings{}, err
	}
	return site, geo, nil
}

// Settings 读出公开页设置与国家查询设置，两者来自同一个快照。
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

// PublicEnabled 读启动时加载、SaveSettings 提交成功后发布的内存值。
// 匿名请求与静态资源都要检查总闸，读原子副本避免每次准入都占用数据库连接。
func (s *Store) PublicEnabled() bool { return s.publicEnabled.Load() }

// SaveSettings 在 s.write 的一个事务里写外观五个键、给出的总闸与 Geo 里给出的项，任一条失败整体回滚：库里不会留下
// 半套设置，与 readSettings 的单条 SELECT 一起保证读侧看不到新旧混合。总闸与 Geo 各项缺席表示不变，不写对应的键。
// 返回的国家查询设置在同一个事务里读出（未给出的项是库里原值），就是这次写入之后的状态；返回的总闸是给出的值，缺席时
// 是锁内的内存值。失败时不发布内存值。
//
// 不变式：publicEnabled 等于库里最近一次提交的总闸键（publicEnabledKey）。前提有二：Open 从库加载它；hub 运行期间
// 只有这里写这个键（现有离线子命令都不写它；此外改库的途径，如 §6.7 整表覆盖的 restore，必须在 hub 停止时
// 进行，由下次 Open 重新加载）。runWriter 串行提交，但各调用方醒来后的发布顺序不受它约束：不持 siteWriteMu
// 时，两次并发保存可以按 A、B 提交却按 B、A 发布，内存与库从此分叉，直到下一次显式保存总闸或重启。所以写者持锁
// 直到提交与发布都完成。缺席时回显取锁内的内存值，由同一不变式保证它等于库值。读总闸（PublicEnabled）不取
// 这把锁，读到的是最近一次发布的值。国家查询没有内存副本：读者（GeoSettings）每次读库，回显在写事务里读回，
// 它的一致性由单个写事务与单条 SELECT 承载，不依赖这把锁。
func (s *Store) SaveSettings(ctx context.Context, in SettingsUpdate) (SiteSettings, GeoSettings, error) {
	s.siteWriteMu.Lock()
	defer s.siteWriteMu.Unlock()
	site := SiteSettings{SiteAppearance: in.SiteAppearance, PublicEnabled: s.publicEnabled.Load()}
	puts := site.fields()
	if in.PublicEnabled != nil {
		site.PublicEnabled = *in.PublicEnabled
		puts = append(puts, flagField(publicEnabledKey, site.PublicEnabled))
	}
	if in.Geo.Enabled != nil {
		puts = append(puts, flagField(geoEnabledKey, *in.Geo.Enabled))
	}
	if in.Geo.URL != nil {
		puts = append(puts, settingField{geoURLKey, in.Geo.URL})
	}
	var geo GeoSettings
	err := s.write(ctx, func(tx *sql.Tx) error {
		for _, f := range puts {
			if _, err := tx.Exec("INSERT INTO setting (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value", f.key, *f.value); err != nil {
				return err
			}
		}
		var err error
		_, geo, err = readSettings(ctx, tx)
		return err
	})
	if err != nil {
		return SiteSettings{}, GeoSettings{}, err
	}
	if in.PublicEnabled != nil {
		s.publicEnabled.Store(site.PublicEnabled)
	}
	return site, geo, nil
}
