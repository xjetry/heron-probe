package store

import (
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

// backupDefaults 是库里没有任何备份键时的读侧值：区域 auto（R2），四个数值取各自的 fallback，其余为空；Target 因而
// 未启用（s3.Config.Enabled）。
func backupDefaults() BackupSettings {
	b := BackupSettings{Target: s3.Config{Region: "auto"}}
	for _, f := range b.numbers() {
		*f.value = f.n.fallback
	}
	return b
}

// texts 列出备份的六个字符串键，readSettings 按它读；写侧见 saveBackup。secret 同样读进 Target（s3.Config 用它签名），
// 协议的读侧只回显由它推出的 has_secret（api 的 backupProto）。
func (b *BackupSettings) texts() []settingField {
	return []settingField{
		{"backup.endpoint", &b.Target.Endpoint}, {"backup.bucket", &b.Target.Bucket}, {"backup.region", &b.Target.Region},
		{"backup.access_key", &b.Target.AccessKey}, {"backup.secret", &b.Target.Secret}, {"backup.prefix", &b.Prefix},
	}
}

// parseStored 解析库里的一个数值项，范围按写侧同一张表（backupNumber）核对。%v 而不是 %w：库里的坏值不能被 api 当作
// 请求的 InvalidArgument 报回去。
func (f numberField) parseStored(v string) error {
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil {
		return fmt.Errorf("invalid stored %s", f.n.key)
	}
	if err := f.n.check(uint32(n)); err != nil {
		return fmt.Errorf("invalid stored %s: %v", f.n.key, err)
	}
	*f.value = uint32(n)
	return nil
}

func parseStoredChannels(key, v string) ([]int64, error) {
	var ids []int64
	if err := json.Unmarshal([]byte(v), &ids); err != nil {
		return nil, fmt.Errorf("invalid stored %s", key)
	}
	return ids, nil
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
