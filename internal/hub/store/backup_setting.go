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

// BackupSettings 是读侧的备份设置：Target 交给 s3.New，Prefix 与四个数值交给调度与保留，Channels 是失败通知的渠道。
// 四个数值恒在 backupNumber 的范围内：readSettings 对库里的每个数值按同一张表核对，出范围即报错。
type BackupSettings struct {
	Target           s3.Config
	Prefix           string
	ConfigIntervalS  uint32
	MetricsIntervalS uint32
	ConfigKeep       uint32
	MetricsKeep      uint32
	Channels         []int64
}

// BackupSettingsUpdate 是一次 UpdateSettings 里给出的 backup。Endpoint、Bucket、Region、AccessKey、Prefix 整体替换；
// 指针项为 nil 表示请求里缺席，SaveSettings 不写对应的键；Channels 非 nil 而为空是显式关闭，写 []。
//
// 缺席必须以 nil 一路传进写事务，而不是由调用方先读旧值再当作给出的值整体回写：写事务由单写连接串行化
// （openStore 的 SetMaxOpenConns(1) 与 runWriter 这一个写协程），事务内读旧值再回写不会丢别的更新；
// 事务之外先读、再回写，两次之间另一请求刚写的凭据或周期就会被旧值覆盖。
type BackupSettingsUpdate struct {
	Endpoint, Bucket, Region, AccessKey, Prefix                string
	Secret                                                     *string
	ConfigIntervalS, MetricsIntervalS, ConfigKeep, MetricsKeep *uint32
	Channels                                                   *[]int64
}

// backupNumber 是一个数值项：库里的持久键、协议字段名、取值范围（§6.7）与库里无键时的默认值。写侧（saveBackup）
// 与读侧（readSettings）都按这张表裁决，范围只此一份。
type backupNumber struct {
	key, field         string
	min, max, fallback uint32
}

var (
	configInterval  = backupNumber{"backup.config_interval_s", "config_interval_s", 60, 86400, 300}
	metricsInterval = backupNumber{"backup.metrics_interval_s", "metrics_interval_s", 3600, 604800, 86400}
	configKeep      = backupNumber{"backup.config_keep", "config_keep", 1, 1000, 48}
	metricsKeep     = backupNumber{"backup.metrics_keep", "metrics_keep", 1, 1000, 14}
)

// BackupRangeError 是写侧给出的数值出范围，api 把它映射为 InvalidArgument。0 同样出范围：份数 0 会在上传成功后
// 把这一层删光，周期 0 会空转。读侧遇到库里出范围的值不返回这个类型（见 readSettings），那是库的问题而不是请求的。
type BackupRangeError struct {
	Field         string
	Min, Max, Got uint32
}

func (e BackupRangeError) Error() string {
	return fmt.Sprintf("backup.%s must be in [%d, %d]; got %d", e.Field, e.Min, e.Max, e.Got)
}

func (n backupNumber) check(v uint32) error {
	if v < n.min || v > n.max {
		return BackupRangeError{Field: n.field, Min: n.min, Max: n.max, Got: v}
	}
	return nil
}

type numberField struct {
	n     backupNumber
	value *uint32
}

func (b *BackupSettings) numbers() []numberField {
	return []numberField{{configInterval, &b.ConfigIntervalS}, {metricsInterval, &b.MetricsIntervalS}, {configKeep, &b.ConfigKeep}, {metricsKeep, &b.MetricsKeep}}
}

func (u *BackupSettingsUpdate) numbers() []numberField {
	return []numberField{{configInterval, u.ConfigIntervalS}, {metricsInterval, u.MetricsIntervalS}, {configKeep, u.ConfigKeep}, {metricsKeep, u.MetricsKeep}}
}

const backupChannelsKey = "notify.backup_channels"

func readSettings(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) (SiteSettings, BackupSettings, error) {
	site := SiteSettings{Theme: DefaultTheme, PublicEnabled: true}
	backup := BackupSettings{Target: s3.Config{Region: "auto"}}
	text := map[string]*string{
		"backup.endpoint": &backup.Target.Endpoint, "backup.bucket": &backup.Target.Bucket,
		"backup.region": &backup.Target.Region, "backup.access_key": &backup.Target.AccessKey,
		"backup.secret": &backup.Target.Secret, "backup.prefix": &backup.Prefix,
	}
	for _, f := range site.fields() {
		text[f.key] = f.value
	}
	numbers := map[string]numberField{}
	for _, f := range backup.numbers() {
		*f.value = f.n.fallback
		numbers[f.n.key] = f
	}
	rows, err := db.QueryContext(ctx, "SELECT key, value FROM setting WHERE key GLOB 'site.*' OR key GLOB 'backup.*' OR key = ?", backupChannelsKey)
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
		if k == publicEnabledKey {
			if v != "0" && v != "1" {
				return site, backup, fmt.Errorf("%s must be 0 or 1; got %q", publicEnabledKey, v)
			}
			site.PublicEnabled = v == "1"
		}
		if f, ok := numbers[k]; ok {
			n, err := strconv.ParseUint(v, 10, 32)
			if err != nil {
				return site, backup, fmt.Errorf("invalid stored %s", k)
			}
			// %v 而不是 %w：库里的坏值不能被 api 当作请求的 InvalidArgument 报回去。
			if err := f.n.check(uint32(n)); err != nil {
				return site, backup, fmt.Errorf("invalid stored %s: %v", k, err)
			}
			*f.value = uint32(n)
		}
		if k == backupChannelsKey {
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

func saveSiteSettings(tx *sql.Tx, site SiteSettingsUpdate) error {
	for _, f := range site.fields() {
		if err := putSetting(tx, f.key, *f.value); err != nil {
			return err
		}
	}
	if site.PublicEnabled != nil {
		value := "0"
		if *site.PublicEnabled {
			value = "1"
		}
		return putSetting(tx, publicEnabledKey, value)
	}
	return nil
}

func putSetting(tx *sql.Tx, key, value string) error {
	_, err := tx.Exec("INSERT INTO setting (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value", key, value)
	return err
}

// saveChannelIDs 保存一个通知渠道选择列表：重复 ID 合并（与告警规则同用 sortedAlertIDs）；每个 ID 在这个写事务里
// 核对存在，与删渠道串行，不会留下指向已删渠道的引用；空选择写 "[]"，不写 JSON null。
func saveChannelIDs(tx *sql.Tx, key string, ids []int64) error {
	ids = sortedAlertIDs(ids)
	for _, id := range ids {
		if err := requireAlertReference(tx, "notify_channel", ObjectNotifyChannel, id); err != nil {
			return err
		}
	}
	return putChannelIDs(tx, key, ids)
}

func putChannelIDs(tx *sql.Tx, key string, ids []int64) error {
	if len(ids) == 0 {
		ids = []int64{}
	}
	data, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return putSetting(tx, key, string(data))
}

// removeChannelID 在删渠道的同一事务里把它从一个选择列表中摘除；键不存在即没有选择，不写。其余 ID 不重新核对：
// 它们由 saveChannelIDs 写入时核对过，此后每次删渠道都在同一事务里摘除，列表里只会有存在的渠道。
func removeChannelID(tx *sql.Tx, key string, id int64) error {
	var value string
	err := tx.QueryRow("SELECT value FROM setting WHERE key = ?", key).Scan(&value)
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
	return putChannelIDs(tx, key, slices.DeleteFunc(ids, func(v int64) bool { return v == id }))
}

func removeBackupChannel(tx *sql.Tx, id int64) error {
	return removeChannelID(tx, backupChannelsKey, id)
}

func saveBackup(tx *sql.Tx, u *BackupSettingsUpdate) error {
	for _, f := range u.numbers() {
		if f.value != nil {
			if err := f.n.check(*f.value); err != nil {
				return err
			}
		}
	}
	for _, f := range []struct{ key, value string }{
		{"backup.endpoint", u.Endpoint}, {"backup.bucket", u.Bucket}, {"backup.region", u.Region},
		{"backup.access_key", u.AccessKey}, {"backup.prefix", u.Prefix},
	} {
		if err := putSetting(tx, f.key, f.value); err != nil {
			return err
		}
	}
	if u.Secret != nil {
		if err := putSetting(tx, "backup.secret", *u.Secret); err != nil {
			return err
		}
	}
	for _, f := range u.numbers() {
		if f.value != nil {
			if err := putSetting(tx, f.n.key, strconv.FormatUint(uint64(*f.value), 10)); err != nil {
				return err
			}
		}
	}
	if u.Channels != nil {
		return saveChannelIDs(tx, backupChannelsKey, *u.Channels)
	}
	return nil
}

// SaveSettings 把全部修改及回显快照放在同一写事务中：数值范围、渠道引用都在事务内裁决，任一不合即整体回滚，
// 也不会在 api 的先查后写窗口里接受已经删除的渠道。update 为 nil 时不改备份设置。
// publicEnabled 在 Open 时从库加载，此后写入只经这里；siteWriteMu 覆盖提交与发布，避免并发调用按提交的
// 反序发布旧值。失败不发布，总闸缺席时不写也不发布；离线改库须停 hub，由下次 Open 重载。
func (s *Store) SaveSettings(ctx context.Context, site SiteSettingsUpdate, update *BackupSettingsUpdate) (SiteSettings, BackupSettings, error) {
	s.siteWriteMu.Lock()
	defer s.siteWriteMu.Unlock()
	var savedSite SiteSettings
	var savedBackup BackupSettings
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := saveSiteSettings(tx, site); err != nil {
			return err
		}
		if update != nil {
			if err := saveBackup(tx, update); err != nil {
				return err
			}
		}
		var err error
		savedSite, savedBackup, err = readSettings(ctx, tx)
		return err
	})
	if err == nil && site.PublicEnabled != nil {
		s.publicEnabled.Store(savedSite.PublicEnabled)
	}
	return savedSite, savedBackup, err
}
