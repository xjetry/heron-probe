package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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

// SiteSettings 读公开页的外观，与 Settings 同用 readSettings，只是不读登录通知的渠道列表：列表不下发给公开页，
// 它的值损坏只让管理设置报错，不连累公开页。
func (s *Store) SiteSettings(ctx context.Context) (SiteSettings, error) {
	st, err := readSettings(ctx, s.r, false)
	return st.Site, err
}

const loginChannelsKey = "notify.login_channels"

// Settings 是管理端读写的设置：外观与登录通知的渠道列表。
type Settings struct {
	Site            SiteSettings
	LoginChannelIDs []int64
}

type settingsReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// readSettings 用一条 SELECT 读出外观的 site.* 键，withLogin 为真时连同登录通知的渠道列表。单条语句在 WAL 下
// 读同一个快照，UpdateSettings 又在一个写事务里写入，两者合起来保证读者拿不到新旧混合的设置；改成逐键查询
// 会失去前一半。从未保存过的键取默认值：明暗为 DefaultTheme，其余外观为空串，渠道列表为空（不通知）。
func readSettings(ctx context.Context, q settingsReader, withLogin bool) (Settings, error) {
	out := Settings{Site: SiteSettings{Theme: DefaultTheme}}
	fields := map[string]*string{}
	for _, f := range out.Site.fields() {
		fields[f.key] = f.value
	}
	query, args := "SELECT key, value FROM setting WHERE key GLOB 'site.*'", []any(nil)
	if withLogin {
		query, args = query+" OR key = ?", []any{loginChannelsKey}
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return Settings{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return Settings{}, err
		}
		if p := fields[key]; p != nil {
			*p = value
		}
		if key == loginChannelsKey {
			if out.LoginChannelIDs, err = decodeChannelIDs(value); err != nil {
				return Settings{}, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return Settings{}, err
	}
	return out, nil
}

func (s *Store) Settings(ctx context.Context) (Settings, error) { return readSettings(ctx, s.r, true) }

// decodeChannelIDs 是渠道列表键的唯一解码处，读设置与写事务里取列表都经过它；值是 saveLoginChannels 写的
// JSON 整数数组。
func decodeChannelIDs(raw string) ([]int64, error) {
	var ids []int64
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, fmt.Errorf("setting %s holds %q, not a JSON array of channel IDs: %w", loginChannelsKey, raw, err)
	}
	return ids, nil
}

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
	return decodeChannelIDs(raw)
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

// UpdateSettings 是设置的写者。site 非 nil 时五项外观都写入，空串也照写（整体替换，没有"不改"的取值）；
// channels 非 nil 时替换登录通知的渠道列表，空列表是显式关闭，不是"不改"；nil 表示不写这一组。
// 单写事务同时裁决引用、保存和回读：任一条失败整体回滚，库里不会留下半套外观；与删渠道串行，不能留下
// 不存在的渠道引用，也不会回显别人的更新。
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
		out, err = readSettings(ctx, tx, true)
		return err
	})
	return out, err
}
