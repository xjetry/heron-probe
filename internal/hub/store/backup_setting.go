package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/xjetry/probe/internal/hub/s3"
)

type BackupSettings struct {
	Target           s3.Config
	Prefix           string
	ConfigIntervalS  uint32
	MetricsIntervalS uint32
	ConfigKeep       uint32
	MetricsKeep      uint32
	Channels         []int64
}

// BackupSettingsUpdate 的可省略项直接对应“缺席不变”，保存时不先读旧值再整体回写，
// 避免两个更新之间把另一请求刚写的凭据或周期覆盖掉。
type BackupSettingsUpdate struct {
	Endpoint, Bucket, Region, AccessKey, Prefix                string
	Secret                                                     *string
	ConfigIntervalS, MetricsIntervalS, ConfigKeep, MetricsKeep *uint32
	Channels                                                   []int64
}

func readSettings(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) (SiteSettings, BackupSettings, error) {
	site := SiteSettings{Theme: DefaultTheme}
	backup := BackupSettings{Target: s3.Config{Region: "auto"}, ConfigIntervalS: 300, MetricsIntervalS: 86400, ConfigKeep: 48, MetricsKeep: 14}
	text := map[string]*string{
		"backup.endpoint": &backup.Target.Endpoint, "backup.bucket": &backup.Target.Bucket,
		"backup.region": &backup.Target.Region, "backup.access_key": &backup.Target.AccessKey,
		"backup.secret": &backup.Target.Secret, "backup.prefix": &backup.Prefix,
	}
	for _, f := range site.fields() {
		text[f.key] = f.value
	}
	numbers := map[string]*uint32{"backup.config_interval_s": &backup.ConfigIntervalS, "backup.metrics_interval_s": &backup.MetricsIntervalS, "backup.config_keep": &backup.ConfigKeep, "backup.metrics_keep": &backup.MetricsKeep}
	rows, err := db.QueryContext(ctx, "SELECT key, value FROM setting WHERE key GLOB 'site.*' OR key GLOB 'backup.*' OR key = 'notify.backup_channels'")
	if err != nil {
		return site, backup, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return site, backup, err
		}
		if p := text[k]; p != nil {
			*p = v
		}
		if p := numbers[k]; p != nil {
			n, err := strconv.ParseUint(v, 10, 32)
			if err != nil {
				return site, backup, fmt.Errorf("invalid stored %s", k)
			}
			*p = uint32(n)
		}
		if k == "notify.backup_channels" {
			if err := json.Unmarshal([]byte(v), &backup.Channels); err != nil {
				return site, backup, fmt.Errorf("invalid stored %s", k)
			}
		}
	}
	return site, backup, rows.Err()
}

// Settings 用一条 SELECT 取同一快照，不能把公开外观与管理员备份设置分开读取后拼接。
func (s *Store) Settings(ctx context.Context) (SiteSettings, BackupSettings, error) {
	return readSettings(ctx, s.r)
}

func (s *Store) BackupSettings(ctx context.Context) (BackupSettings, error) {
	_, backup, err := s.Settings(ctx)
	return backup, err
}

func saveSiteSettings(tx *sql.Tx, site SiteSettings) error {
	for _, f := range site.fields() {
		if err := putSetting(tx, f.key, *f.value); err != nil {
			return err
		}
	}
	return nil
}

func putSetting(tx *sql.Tx, key, value string) error {
	_, err := tx.Exec("INSERT INTO setting (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value", key, value)
	return err
}

func removeBackupChannel(tx *sql.Tx, id int64) error {
	var value string
	err := tx.QueryRow("SELECT value FROM setting WHERE key = 'notify.backup_channels'").Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var ids []int64
	if err := json.Unmarshal([]byte(value), &ids); err != nil {
		return err
	}
	if !slices.Contains(ids, id) {
		return nil
	}
	ids = slices.DeleteFunc(ids, func(v int64) bool { return v == id })
	data, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return putSetting(tx, "notify.backup_channels", string(data))
}

// SaveSettings 把全部修改及回显快照放在同一写事务中；凭据缺席不产生写操作。
// 渠道引用也在事务内检查，不能在 API 的先查后写窗口里接受已经删除的渠道。
func (s *Store) SaveSettings(ctx context.Context, site SiteSettings, update *BackupSettingsUpdate) (SiteSettings, BackupSettings, error) {
	var savedSite SiteSettings
	var savedBackup BackupSettings
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := saveSiteSettings(tx, site); err != nil {
			return err
		}
		if update != nil {
			for _, id := range update.Channels {
				if err := requireAlertReference(tx, "notify_channel", ObjectNotifyChannel, id); err != nil {
					return err
				}
			}
			channels, err := json.Marshal(update.Channels)
			if err != nil {
				return err
			}
			for _, f := range []struct{ key, value string }{
				{"backup.endpoint", update.Endpoint}, {"backup.bucket", update.Bucket}, {"backup.region", update.Region},
				{"backup.access_key", update.AccessKey}, {"backup.prefix", update.Prefix}, {"notify.backup_channels", string(channels)},
			} {
				if err := putSetting(tx, f.key, f.value); err != nil {
					return err
				}
			}
			if update.Secret != nil {
				if err := putSetting(tx, "backup.secret", *update.Secret); err != nil {
					return err
				}
			}
			for _, f := range []struct {
				key   string
				value *uint32
			}{
				{"backup.config_interval_s", update.ConfigIntervalS}, {"backup.metrics_interval_s", update.MetricsIntervalS}, {"backup.config_keep", update.ConfigKeep}, {"backup.metrics_keep", update.MetricsKeep},
			} {
				if f.value != nil {
					if err := putSetting(tx, f.key, strconv.FormatUint(uint64(*f.value), 10)); err != nil {
						return err
					}
				}
			}
		}
		var err error
		savedSite, savedBackup, err = readSettings(ctx, tx)
		return err
	})
	return savedSite, savedBackup, err
}
