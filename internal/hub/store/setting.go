package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
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

// 键名是库里的持久标识，改名要迁移。外观只占 site.* 这五个键，同表若放别的设置不影响它们。
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
// 一个写事务里写五个键，两者合起来保证读者拿不到新旧混合的外观。改成逐键查询会失去前一半。
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

// SaveSiteSettings 在 s.write 的一个事务里写五个键，任一条失败整体回滚：库里不会留下半套外观，
// 与 SiteSettings 的单条 SELECT 一起保证读侧看不到新旧混合。
func (s *Store) SaveSiteSettings(ctx context.Context, st SiteSettings) error {
	_, err := s.UpdateSettings(ctx, &st, nil)
	return err
}

const loginChannelsKey = "notify.login_channels"

type Settings struct {
	Site            SiteSettings
	LoginChannelIDs []int64
}

type settingsReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readSettings(ctx context.Context, q settingsReader) (Settings, error) {
	out := Settings{Site: SiteSettings{Theme: DefaultTheme}}
	fields := map[string]*string{}
	for _, f := range out.Site.fields() {
		fields[f.key] = f.value
	}
	rows, err := q.QueryContext(ctx, "SELECT key, value FROM setting WHERE key GLOB 'site.*' OR key = ?", loginChannelsKey)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return out, err
		}
		if p := fields[key]; p != nil {
			*p = value
		}
		if key == loginChannelsKey {
			if err := json.Unmarshal([]byte(value), &out.LoginChannelIDs); err != nil {
				return out, err
			}
		}
	}
	return out, rows.Err()
}

func (s *Store) Settings(ctx context.Context) (Settings, error) { return readSettings(ctx, s.r) }

func saveSetting(tx *sql.Tx, key, value string) error {
	_, err := tx.Exec("INSERT INTO setting (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value", key, value)
	return err
}

func loginChannels(tx *sql.Tx) ([]int64, error) {
	var raw string
	err := tx.QueryRow("SELECT value FROM setting WHERE key = ?", loginChannelsKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []int64
	err = json.Unmarshal([]byte(raw), &ids)
	return ids, err
}

func saveLoginChannels(tx *sql.Tx, ids []int64) error {
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	for _, id := range ids {
		if err := requireAlertReference(tx, "notify_channel", ObjectNotifyChannel, id); err != nil {
			return err
		}
	}
	if ids == nil {
		ids = []int64{}
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return saveSetting(tx, loginChannelsKey, string(raw))
}

// 单写事务同时裁决引用、保存和回读；与删渠道串行，不能留下不存在的渠道引用或回显别人的更新。
// nil 表示不写这一组；非 nil 的空渠道列表是显式关闭，不是“不改”。
func (s *Store) UpdateSettings(ctx context.Context, site *SiteSettings, channels *[]int64) (Settings, error) {
	var out Settings
	err := s.write(ctx, func(tx *sql.Tx) error {
		if site != nil {
			for _, f := range site.fields() {
				if err := saveSetting(tx, f.key, *f.value); err != nil {
					return err
				}
			}
		}
		if channels != nil {
			if err := saveLoginChannels(tx, *channels); err != nil {
				return err
			}
		}
		var err error
		out, err = readSettings(ctx, tx)
		return err
	})
	return out, err
}
