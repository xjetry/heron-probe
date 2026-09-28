package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/xjetry/probe/internal/hub/outbound"
)

type AlertKind string

const (
	KindOffline AlertKind = "offline"
	KindProbe   AlertKind = "probe"
	KindExpiry  AlertKind = "expiry"
)

type ProbeMetric string

const (
	MetricLossPct ProbeMetric = "loss_pct"
	MetricRttMs   ProbeMetric = "rtt_ms"
)

type AlertRule struct {
	ID      int64
	Name    string
	Kind    AlertKind
	Enabled bool
	// AllNodes 为真时不存联结行；否则 NodeIDs 是升序去重的显式集合，空集合不覆盖任何节点。
	// DeleteNode 只删联结行、不改 AllNodes，因此删除最后一个作用域节点不会放宽到全部节点。
	AllNodes   bool
	NodeIDs    []int64
	ChannelIDs []int64
	TaskID     uint64
	Metric     ProbeMetric
	Threshold  float64
	ForMinutes int
	// DaysBefore 只属于到期规则。它与 Threshold、ForMinutes 一样不是规则身份：改它保留状态（见 SaveAlertRule）。
	DaysBefore int
	CreatedAt  time.Time
}

type NotifyChannel struct {
	ID        int64
	Name      string
	Kind      ChannelKind
	Config    string
	CreatedAt time.Time
}

type ChannelKind string

const (
	ChannelTelegram ChannelKind = "telegram"
	ChannelWebhook  ChannelKind = "webhook"
)

type AlertState string

const (
	StateOK      AlertState = "ok"
	StatePending AlertState = "pending"
	StateFiring  AlertState = "firing"
)

type StateRow struct {
	RuleID, NodeID int64
	State          AlertState
	SinceAt        time.Time
	// FiredExpiresOn 只属于到期规则：进入 firing 时节点的到期日。恢复时拿它与节点当前的到期日比较，相等说明日期没变，
	// 恢复文案写"已不在提醒窗口内"而不是"到期日已更新"（§9.2）。这个日期随状态存，而不是从触发事件里读回：告警事件
	// 按保留期清理，一个过期节点却可以一直处在 firing。其余种类的状态、以及不经 RecordTransition 写入的状态，这一项为空。
	FiredExpiresOn string
	// RecoveredAt 只属于离线规则：这一对规则与节点上一次从 firing 恢复的时刻，零值表示从未恢复过（库里是 NULL）。
	// 它不是当前状态的属性，而是一段历史：状态再变（恢复后再次离线写成 pending）时由写入方沿用，只有下一次恢复
	// 才改写它。抖动抑制按它判断这次离线是否落在恢复后的窗口里；状态行被删除（规则不再适用）时一起消失。
	RecoveredAt time.Time
}

type Transition string

const (
	TransitionFiring    Transition = "firing"
	TransitionRecovered Transition = "recovered"
)

type AlertEvent struct {
	ID             int64
	RuleID, NodeID int64
	Transition     Transition
	At             time.Time
	Summary        string
	Value          float64
	Deliveries     []Delivery
}

type Delivery struct {
	ID         int64
	EventID    int64
	ChannelID  int64
	Attempts   int // 已开始的尝试次数；BeginDeliveryAttempt 在发送之前提交，发送前崩溃也计入。
	OK         bool
	Done       bool
	Failure    DeliveryFailure // 最近一次失败的类别；成功或尚无结果时为空。
	HTTPStatus int             // 仅 FailureHTTPStatus 非零，且 outbound.ValidHTTPStatus；写路径见 schema.go 的 http_status 列。
	// 最近一次失败的原文：HTTP 失败时是响应体片段，其余是出站错误文本。hub 不把 URL 写进原文，
	// 但响应体由接收方决定：接收方可能回显请求体模板里的密钥，也可能回显请求路径或头值这些只写
	// 不读的配置。所以只经仅会话的 GetAlertDeliveryError 读出（会话本就有权管理这些配置）；
	// 只读的 ListAlertEvents 逐字段映射 Delivery，不带这个字段。
	LastError   string
	DeliveredAt time.Time
}

// DeliveryFailure 在产生失败的地方确定，不从错误文本反推；与 transition、渠道 kind 一样按 TEXT 落库。
type DeliveryFailure string

const (
	FailureNone             DeliveryFailure = ""
	FailureHTTPStatus       DeliveryFailure = "http_status"
	FailureTransport        DeliveryFailure = "transport"
	FailureRequest          DeliveryFailure = "request"
	FailureChannelInvalid   DeliveryFailure = "channel_invalid"
	FailureChannelDeleted   DeliveryFailure = "channel_deleted"
	FailureResultUnrecorded DeliveryFailure = "result_unrecorded"
	FailureUnclassified     DeliveryFailure = "unclassified"
)

// DeliveryFailures 列出全部失败类别（不含 FailureNone）：写侧只接受其中之一；
// api 的协议映射测试据它核对与协议枚举一一对应，新增类别时漏改映射或枚举由该测试发现。
func DeliveryFailures() []DeliveryFailure {
	return []DeliveryFailure{FailureHTTPStatus, FailureTransport, FailureRequest, FailureChannelInvalid,
		FailureChannelDeleted, FailureResultUnrecorded, FailureUnclassified}
}

// DeliveryResult 是一次投递结果的完整写入；UpdateDelivery 在写库前校验字段之间的一致性。
type DeliveryResult struct {
	OK, Done    bool
	Failure     DeliveryFailure
	HTTPStatus  int
	Error       string
	DeliveredAt time.Time
}

// 投递尝试的唯一上限；队列达到上限时写入 done，重启也不能绕过它。
const MaxDeliveryAttempts = 3

var ErrDeliveryDone = errors.New("delivery is done")
var ErrDeliveryExhausted = errors.New("delivery attempts exhausted")

// 主表与关联在同一读事务中读取，不能把并发保存前后的两份作用域拼在一起。
func (s *Store) ListAlertRules(ctx context.Context) ([]AlertRule, error) {
	tx, err := s.r.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id, name, kind, enabled, all_nodes, task_id, metric, threshold, for_minutes, days_before, created_at FROM alert_rule ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertRule
	index := map[int64]int{}
	for rows.Next() {
		var r AlertRule
		var task, minutes, daysBefore sql.NullInt64
		var metric sql.NullString
		var threshold sql.NullFloat64
		var created int64
		if err := rows.Scan(&r.ID, &r.Name, &r.Kind, &r.Enabled, &r.AllNodes, &task, &metric, &threshold, &minutes, &daysBefore, &created); err != nil {
			return nil, err
		}
		r.TaskID, r.Metric, r.Threshold, r.ForMinutes = uint64(task.Int64), ProbeMetric(metric.String), threshold.Float64, int(minutes.Int64)
		r.DaysBefore = int(daysBefore.Int64)
		r.CreatedAt = time.Unix(created, 0).UTC()
		index[r.ID] = len(out)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, rel := range []struct {
		table, column string
		nodes         bool
	}{
		{"alert_rule_node", "node_id", true}, {"alert_rule_channel", "channel_id", false},
	} {
		rows, err := tx.QueryContext(ctx, "SELECT rule_id, "+rel.column+" FROM "+rel.table+" ORDER BY rule_id, "+rel.column)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var rule, id int64
			if err := rows.Scan(&rule, &id); err != nil {
				rows.Close()
				return nil, err
			}
			if i, ok := index[rule]; ok {
				if rel.nodes {
					out[i].NodeIDs = append(out[i].NodeIDs, id)
				} else {
					out[i].ChannelIDs = append(out[i].ChannelIDs, id)
				}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func sortedAlertIDs(ids []int64) []int64 {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}

// KindFieldError 是规则带着不属于它种类的专用字段。Field 是协议里的字段名，Constraint 是违反的约束。
type KindFieldError struct {
	Field, Constraint string
}

func (e KindFieldError) Error() string { return e.Field + " " + e.Constraint }

// CheckKindFields 裁决种类与专用字段的组合：探测四项（任务、指标、阈值、持续分钟）只属于探测规则，days_before
// 只属于到期规则，别的种类上必须是零值。它是这条规则唯一的实现：SaveAlertRule 对非法组合报错而不改写，
// alert.CheckRule 在保存与载入时调它，协议层经 CheckRule 得到同样的字段与约束（§9.1）。阈值用 != 0 判：NaN 与任何数
// 都不等，也被拒绝。种类本身是否合法不在这里判断。
func CheckKindFields(r AlertRule) error {
	if r.Kind != KindProbe {
		switch {
		case r.TaskID != 0:
			return KindFieldError{"task_id", "must be 0 unless kind is probe"}
		case r.Metric != "":
			return KindFieldError{"metric", "must be unspecified unless kind is probe"}
		case r.Threshold != 0:
			return KindFieldError{"threshold", "must be 0 unless kind is probe"}
		case r.ForMinutes != 0:
			return KindFieldError{"for_minutes", "must be 0 unless kind is probe"}
		}
	}
	if r.Kind != KindExpiry && r.DaysBefore != 0 {
		return KindFieldError{"days_before", "must be 0 unless kind is expiry"}
	}
	return nil
}

// 引用检查与保存同在单写事务，删除不能插入两者之间造成孤儿引用。
func (s *Store) SaveAlertRule(ctx context.Context, r AlertRule) (AlertRule, error) {
	// 非法组合报错而不是清零：静默清零是放宽方向，调用方发了什么、存下的却是零值，无从察觉（§9.1）。
	if err := CheckKindFields(r); err != nil {
		return AlertRule{}, err
	}
	r.NodeIDs, r.ChannelIDs = sortedAlertIDs(r.NodeIDs), sortedAlertIDs(r.ChannelIDs)
	if r.AllNodes {
		r.NodeIDs = nil
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		for _, id := range r.NodeIDs {
			exists, err := nodeExistsTx(tx, id)
			if err != nil {
				return err
			}
			if !exists {
				return NotFoundError{Kind: ObjectNode, ID: id}
			}
		}
		for _, id := range r.ChannelIDs {
			if err := requireAlertReference(tx, "notify_channel", ObjectNotifyChannel, id); err != nil {
				return err
			}
		}
		// 每种规则只落自己的专用列，其余列写 NULL。CheckKindFields 已保证别的种类的专用字段都是零值，NULL 与回显的
		// 零值一致。
		var task, metric, threshold, minutes, daysBefore any
		if r.Kind == KindProbe {
			if err := requireAlertReference(tx, "probe_task", ObjectProbeTask, int64(r.TaskID)); err != nil {
				return err
			}
			task, metric, threshold, minutes = int64(r.TaskID), r.Metric, r.Threshold, r.ForMinutes
		}
		if r.Kind == KindExpiry {
			daysBefore = r.DaysBefore
		}
		var created int64
		var identityChanged bool
		if r.ID == 0 {
			created = s.clk.Now().Unix()
			err := tx.QueryRow(`INSERT INTO alert_rule (name, kind, enabled, all_nodes, task_id, metric, threshold, for_minutes, days_before, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`, r.Name, r.Kind, r.Enabled, r.AllNodes, task, metric, threshold, minutes, daysBefore, created).Scan(&r.ID)
			if err != nil {
				return err
			}
		} else {
			err := tx.QueryRow(`SELECT kind != ? OR COALESCE(task_id, 0) != ? OR COALESCE(metric, '') != ? FROM alert_rule WHERE id = ?`, r.Kind, r.TaskID, r.Metric, r.ID).Scan(&identityChanged)
			if errors.Is(err, sql.ErrNoRows) {
				return NotFoundError{Kind: ObjectAlertRule, ID: r.ID}
			}
			if err != nil {
				return err
			}
			err = tx.QueryRow(`UPDATE alert_rule SET name = ?, kind = ?, enabled = ?, all_nodes = ?, task_id = ?, metric = ?, threshold = ?, for_minutes = ?, days_before = ?
				WHERE id = ? RETURNING created_at`, r.Name, r.Kind, r.Enabled, r.AllNodes, task, metric, threshold, minutes, daysBefore, r.ID).Scan(&created)
			if errors.Is(err, sql.ErrNoRows) {
				return NotFoundError{Kind: ObjectAlertRule, ID: r.ID}
			}
			if err != nil {
				return err
			}
		}
		r.CreatedAt = time.Unix(created, 0).UTC()
		for _, rel := range []struct {
			table, column string
			ids           []int64
		}{
			{"alert_rule_node", "node_id", r.NodeIDs}, {"alert_rule_channel", "channel_id", r.ChannelIDs},
		} {
			if _, err := tx.Exec("DELETE FROM "+rel.table+" WHERE rule_id = ?", r.ID); err != nil {
				return err
			}
			for _, id := range rel.ids {
				if _, err := tx.Exec("INSERT INTO "+rel.table+" (rule_id, "+rel.column+") VALUES (?, ?)", r.ID, id); err != nil {
					return err
				}
			}
		}
		// 规则与状态由本次写事务一起提交；种类、任务或指标变化后，旧观测不再描述当前规则。
		// threshold、for_minutes 与 days_before 不是身份：同一个量换阈值时保留状态，下一轮按新阈值判断是否恢复。
		// 禁用与作用域收缩也在这里裁剪，重启不能重新载入已不适用的 firing。
		if identityChanged || !r.Enabled || !r.AllNodes {
			query := "DELETE FROM alert_state WHERE rule_id = ?"
			args := []any{r.ID}
			if !identityChanged && r.Enabled && len(r.NodeIDs) > 0 {
				query += " AND node_id NOT IN (" + placeholders(len(r.NodeIDs)) + ")"
				for _, id := range r.NodeIDs {
					args = append(args, id)
				}
			}
			if _, err := tx.Exec(query, args...); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return AlertRule{}, err
	}
	return r, nil
}

func requireAlertReference(tx *sql.Tx, table string, kind ObjectKind, id int64) error {
	var one int
	err := tx.QueryRow("SELECT 1 FROM "+table+" WHERE id = ?", id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return NotFoundError{Kind: kind, ID: id}
	}
	return err
}

func (s *Store) DeleteAlertRule(ctx context.Context, id int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if err := deleteAlertEntity(tx, "alert_rule", ObjectAlertRule, id); err != nil {
			return err
		}
		for _, table := range []string{"alert_rule_node", "alert_rule_channel", "alert_state"} {
			if _, err := tx.Exec("DELETE FROM "+table+" WHERE rule_id = ?", id); err != nil {
				return err
			}
		}
		return nil
	})
}

func deleteAlertEntity(tx *sql.Tx, table string, kind ObjectKind, id int64) error {
	res, err := tx.Exec("DELETE FROM "+table+" WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return NotFoundError{Kind: kind, ID: id}
	}
	return nil
}

func (s *Store) ListNotifyChannels(ctx context.Context) ([]NotifyChannel, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT id, name, kind, config, created_at FROM notify_channel ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NotifyChannel
	for rows.Next() {
		var c NotifyChannel
		var created int64
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.Config, &created); err != nil {
			return nil, err
		}
		c.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) SaveNotifyChannel(ctx context.Context, c NotifyChannel) (NotifyChannel, error) {
	err := s.write(ctx, func(tx *sql.Tx) error {
		var created int64
		if c.ID == 0 {
			created = s.clk.Now().Unix()
			if err := tx.QueryRow("INSERT INTO notify_channel (name, kind, config, created_at) VALUES (?, ?, ?, ?) RETURNING id", c.Name, c.Kind, c.Config, created).Scan(&c.ID); err != nil {
				return err
			}
		} else {
			err := tx.QueryRow("UPDATE notify_channel SET name = ?, kind = ?, config = ? WHERE id = ? RETURNING created_at", c.Name, c.Kind, c.Config, c.ID).Scan(&created)
			if errors.Is(err, sql.ErrNoRows) {
				return NotFoundError{Kind: ObjectNotifyChannel, ID: c.ID}
			}
			if err != nil {
				return err
			}
		}
		c.CreatedAt = time.Unix(created, 0).UTC()
		return nil
	})
	if err != nil {
		return NotifyChannel{}, err
	}
	return c, nil
}

// 检查与删除共用写事务；SaveAlertRule 也经单写协程，检查之后不会新添引用。
func checkAlertReferences(tx *sql.Tx, query string, kind ObjectKind, id int64) error {
	rows, err := tx.Query(query, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	var refs []RuleReference
	for rows.Next() {
		var ref RuleReference
		if err := rows.Scan(&ref.ID, &ref.Name); err != nil {
			return err
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(refs) > 0 {
		return InUseError{Kind: kind, ID: id, Rules: refs}
	}
	return nil
}

func (s *Store) DeleteNotifyChannel(ctx context.Context, id int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if err := checkAlertReferences(tx, `SELECT r.id, r.name FROM alert_rule r JOIN alert_rule_channel c ON c.rule_id = r.id WHERE c.channel_id = ? ORDER BY r.id`, ObjectNotifyChannel, id); err != nil {
			return err
		}
		if err := deleteAlertEntity(tx, "notify_channel", ObjectNotifyChannel, id); err != nil {
			return err
		}
		if err := removeBackupChannel(tx, id); err != nil {
			return err
		}
		_, err := tx.Exec("UPDATE alert_delivery SET done = 1, failure = ?, http_status = NULL, last_error = '' WHERE channel_id = ? AND done = 0", FailureChannelDeleted, id)
		return err
	})
}

func (s *Store) ListAlertStates(ctx context.Context) ([]StateRow, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT rule_id, node_id, state, since_at, fired_expires_on, recovered_at FROM alert_state ORDER BY rule_id, node_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StateRow
	for rows.Next() {
		var r StateRow
		var since int64
		var recovered sql.NullInt64
		if err := rows.Scan(&r.RuleID, &r.NodeID, &r.State, &since, &r.FiredExpiresOn, &recovered); err != nil {
			return nil, err
		}
		r.SinceAt = time.Unix(since, 0).UTC()
		if recovered.Valid {
			r.RecoveredAt = time.Unix(recovered.Int64, 0).UTC()
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// 两个状态写入口共用事务内准入，单写协程保证删除之后排队的写不能重建孤儿状态。整行替换：每次写都给出
// fired_expires_on 与 recovered_at，上一个状态记的到期日不会留到下一个状态；recovered_at 要沿用时由调用方显式带上。
func setAlertState(tx *sql.Tx, ruleID, nodeID int64, state AlertState, since time.Time, firedExpiresOn string, recoveredAt time.Time) error {
	if err := requireAlertReference(tx, "alert_rule", ObjectAlertRule, ruleID); err != nil {
		return err
	}
	exists, err := nodeExistsTx(tx, nodeID)
	if err != nil {
		return err
	}
	if !exists {
		return NotFoundError{Kind: ObjectNode, ID: nodeID}
	}
	var recovered any
	if !recoveredAt.IsZero() {
		recovered = recoveredAt.Unix()
	}
	_, err = tx.Exec("INSERT OR REPLACE INTO alert_state (rule_id, node_id, state, since_at, fired_expires_on, recovered_at) VALUES (?, ?, ?, ?, ?, ?)",
		ruleID, nodeID, state, since.Unix(), firedExpiresOn, recovered)
	return err
}

// SetAlertState 写不带事件的状态变化。alert 的状态机只经触发转换进入 firing（那一步走 RecordTransition），所以这里
// 写的状态没有触发时的到期日。recoveredAt 含义见 StateRow.RecoveredAt，零值写 NULL。
func (s *Store) SetAlertState(ctx context.Context, ruleID, nodeID int64, state AlertState, since, recoveredAt time.Time) error {
	return s.write(ctx, func(tx *sql.Tx) error { return setAlertState(tx, ruleID, nodeID, state, since, "", recoveredAt) })
}

// 候选集撤销不是恢复观测，删除状态不产生事件；重复清理同一对规则与节点仍然成功。
func (s *Store) DeleteAlertState(ctx context.Context, ruleID, nodeID int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM alert_state WHERE rule_id = ? AND node_id = ?", ruleID, nodeID)
		return err
	})
}

// 状态、事件与续投队列同事务提交，崩溃不能留下已转换但没有通知记录的状态。firedExpiresOn 与 recoveredAt 随状态写入，
// 含义见 StateRow.FiredExpiresOn 与 StateRow.RecoveredAt。
func (s *Store) RecordTransition(ctx context.Context, ruleID, nodeID int64, state AlertState, firedExpiresOn string, recoveredAt time.Time, ev AlertEvent, channelIDs []int64) (AlertEvent, error) {
	ev.ID, ev.RuleID, ev.NodeID, ev.Deliveries = 0, ruleID, nodeID, nil
	ev.At = time.Unix(ev.At.Unix(), 0).UTC()
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := setAlertState(tx, ruleID, nodeID, state, ev.At, firedExpiresOn, recoveredAt); err != nil {
			return err
		}
		return recordAlertEvent(tx, &ev, channelIDs)
	})
	if err != nil {
		return AlertEvent{}, err
	}
	return ev, nil
}

func recordAlertEvent(tx *sql.Tx, ev *AlertEvent, channelIDs []int64) error {
	if err := tx.QueryRow("INSERT INTO alert_event (rule_id, node_id, transition, at, summary, value) VALUES (?, ?, ?, ?, ?, ?) RETURNING id", ev.RuleID, ev.NodeID, ev.Transition, ev.At.Unix(), ev.Summary, ev.Value).Scan(&ev.ID); err != nil {
		return err
	}
	for _, channel := range channelIDs {
		if err := requireAlertReference(tx, "notify_channel", ObjectNotifyChannel, channel); err != nil {
			return err
		}
		d := Delivery{EventID: ev.ID, ChannelID: channel}
		if err := tx.QueryRow("INSERT INTO alert_delivery (event_id, channel_id) VALUES (?, ?) RETURNING id", d.EventID, d.ChannelID).Scan(&d.ID); err != nil {
			return err
		}
		ev.Deliveries = append(ev.Deliveries, d)
	}
	return nil
}

// 每个已提交的尝试只授权至多一次发送，不保证恰好一次：提交后发送前崩溃也消耗名额。
// 条件更新由 Store.write 串行化；结果写失败或重启不能重新使用已消耗的名额。
func (s *Store) BeginDeliveryAttempt(ctx context.Context, id int64) (Delivery, error) {
	var d Delivery
	var refused error
	err := s.write(ctx, func(tx *sql.Tx) error {
		var err error
		d, err = scanDelivery(tx.QueryRow("UPDATE alert_delivery SET attempts = attempts + 1 WHERE id = ? AND done = 0 AND attempts < ? RETURNING "+deliveryColumns, id, MaxDeliveryAttempts))
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		d, err = scanDelivery(tx.QueryRow(selectDeliveries+" WHERE id = ?", id))
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundError{Kind: ObjectAlertDelivery, ID: id}
		}
		if err != nil {
			return err
		}
		if d.Done {
			refused = ErrDeliveryDone
		} else {
			refused = ErrDeliveryExhausted
		}
		return nil
	})
	if err != nil {
		return Delivery{}, err
	}
	return d, refused
}

// 结果不能改写已开始的次数；计数只由 BeginDeliveryAttempt 推进。
func (s *Store) UpdateDelivery(ctx context.Context, id int64, r DeliveryResult) error {
	if err := r.check(); err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		var delivered, status any
		if !r.DeliveredAt.IsZero() {
			delivered = r.DeliveredAt.Unix()
		}
		if r.HTTPStatus != 0 {
			status = r.HTTPStatus
		}
		res, err := tx.Exec("UPDATE alert_delivery SET ok = ?, done = ?, failure = ?, http_status = ?, last_error = ?, delivered_at = ? WHERE id = ? AND done = 0", r.OK, r.Done, r.Failure, status, r.Error, delivered, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			// 删除渠道或其他终止已先提交，迟到的 HTTP 结果不能把终态重新打开。
			return requireAlertReference(tx, "alert_delivery", ObjectAlertDelivery, id)
		}
		return nil
	})
}

// 读侧据类别解释状态码与原文、只读口径据类别给出状态码、PendingDeliveries 据 done 续投，
// 都依赖这里的一致性；违反即拒绝，不写库，也不改写成某个"最接近"的类别。
func (r DeliveryResult) check() error {
	if r.OK {
		if r.Failure != FailureNone || r.HTTPStatus != 0 || r.Error != "" {
			return fmt.Errorf("delivery result: success carries failure %q, status %d, error %q", r.Failure, r.HTTPStatus, r.Error)
		}
		// 成功未终态会被 PendingDeliveries 重新入队，接收方收到重复通知。
		if !r.Done {
			return errors.New("delivery result: success must be done")
		}
		if r.DeliveredAt.IsZero() {
			return errors.New("delivery result: success without delivery time")
		}
		return nil
	}
	if !r.DeliveredAt.IsZero() {
		return errors.New("delivery result: failure with delivery time")
	}
	if r.Failure == FailureNone {
		return errors.New("delivery result: failure without category")
	}
	if !slices.Contains(DeliveryFailures(), r.Failure) {
		return fmt.Errorf("delivery result: unknown failure category %q", r.Failure)
	}
	if r.Failure == FailureHTTPStatus && !outbound.ValidHTTPStatus(r.HTTPStatus) {
		return fmt.Errorf("delivery result: failure %q with invalid HTTP status %d", r.Failure, r.HTTPStatus)
	}
	if r.Failure != FailureHTTPStatus && r.HTTPStatus != 0 {
		return fmt.Errorf("delivery result: failure %q carries HTTP status %d", r.Failure, r.HTTPStatus)
	}
	if (r.Failure == FailureChannelDeleted || r.Failure == FailureResultUnrecorded) && r.Error != "" {
		return fmt.Errorf("delivery result: failure %q carries error text", r.Failure)
	}
	// 这五类原样重发只会得到同样结果（或已无从重发），不能留在续投队列里。
	switch r.Failure {
	case FailureChannelDeleted, FailureResultUnrecorded, FailureChannelInvalid, FailureRequest, FailureUnclassified:
		if !r.Done {
			return fmt.Errorf("delivery result: failure %q must be done", r.Failure)
		}
	}
	return nil
}

// GetDeliveryError 读出最近一次失败的原文；只供仅会话的方法使用（见 Delivery.LastError）。
func (s *Store) GetDeliveryError(ctx context.Context, id int64) (string, error) {
	var text string
	err := s.r.QueryRowContext(ctx, "SELECT last_error FROM alert_delivery WHERE id = ?", id).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return "", NotFoundError{Kind: ObjectAlertDelivery, ID: id}
	}
	return text, err
}

const deliveryColumns = "id, event_id, channel_id, attempts, ok, done, failure, http_status, last_error, delivered_at"
const selectDeliveries = "SELECT " + deliveryColumns + " FROM alert_delivery"

func scanDelivery(row interface{ Scan(...any) error }) (Delivery, error) {
	var d Delivery
	var delivered, status sql.NullInt64
	if err := row.Scan(&d.ID, &d.EventID, &d.ChannelID, &d.Attempts, &d.OK, &d.Done, &d.Failure, &status, &d.LastError, &delivered); err != nil {
		return Delivery{}, err
	}
	d.HTTPStatus = int(status.Int64)
	if delivered.Valid {
		d.DeliveredAt = time.Unix(delivered.Int64, 0).UTC()
	}
	return d, nil
}

func scanDeliveries(rows *sql.Rows) ([]Delivery, error) {
	var out []Delivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) PendingDeliveries(ctx context.Context) ([]Delivery, error) {
	rows, err := s.r.QueryContext(ctx, selectDeliveries+" WHERE done = 0 ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDeliveries(rows)
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// 事件与投递行共用一个读快照，返回值不混合通知更新前后的状态。
func (s *Store) readAlertEvents(ctx context.Context, predicate string, args ...any) ([]AlertEvent, error) {
	tx, err := s.r.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, selectAlertEvents+predicate, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertEvent
	var ids []any
	index := map[int64]int{}
	for rows.Next() {
		var ev AlertEvent
		var at int64
		if err := rows.Scan(&ev.ID, &ev.RuleID, &ev.NodeID, &ev.Transition, &at, &ev.Summary, &ev.Value); err != nil {
			return nil, err
		}
		ev.At = time.Unix(at, 0).UTC()
		index[ev.ID] = len(out)
		out = append(out, ev)
		ids = append(ids, ev.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		rows, err := tx.QueryContext(ctx, selectDeliveries+" WHERE event_id IN ("+placeholders(len(ids))+") ORDER BY id", ids...)
		if err != nil {
			return nil, err
		}
		deliveries, err := scanDeliveries(rows)
		rows.Close()
		if err != nil {
			return nil, err
		}
		for _, d := range deliveries {
			i := index[d.EventID]
			out[i].Deliveries = append(out[i].Deliveries, d)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) ListAlertEvents(ctx context.Context, nodeID int64, beforeID int64, limit int) ([]AlertEvent, error) {
	where, args := alertEventWindow(nodeID, beforeID, limit)
	return s.readAlertEvents(ctx, where, args...)
}

const selectAlertEvents = "SELECT id, rule_id, node_id, transition, at, summary, value FROM alert_event "

// 只拼接实际过滤条件，node_id 的等值条件才能走 alert_event_by_node 的前缀。
func alertEventWindow(nodeID, beforeID int64, limit int) (string, []any) {
	var conditions []string
	var args []any
	if nodeID != 0 {
		conditions = append(conditions, "node_id = ?")
		args = append(args, nodeID)
	}
	if beforeID != 0 {
		conditions = append(conditions, "id < ?")
		args = append(args, beforeID)
	}
	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}
	return where + " ORDER BY id DESC LIMIT ?", append(args, limit)
}

const countExpiredPendingDeliveries = "SELECT COUNT(*) FROM alert_delivery WHERE done = 0 AND event_id IN (SELECT id FROM alert_event WHERE at < ?)"
const deleteExpiredDeliveries = "DELETE FROM alert_delivery WHERE event_id IN (SELECT id FROM alert_event WHERE at < ?)"
const deleteExpiredAlertEvents = "DELETE FROM alert_event WHERE at < ?"

// 事件与投递共享事件时间的保留期；同一写事务内先删投递，不能留下孤行。
// 未完成投递也随过期事件删除，提交后再告警计数，避免把回滚误报成投递丢失。
func (s *Store) PruneAlertEvents(ctx context.Context, before time.Time) (int64, error) {
	var deleted, unfinished int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRow(countExpiredPendingDeliveries, before.Unix()).Scan(&unfinished); err != nil {
			return err
		}
		if _, err := tx.Exec(deleteExpiredDeliveries, before.Unix()); err != nil {
			return err
		}
		result, err := tx.Exec(deleteExpiredAlertEvents, before.Unix())
		if err != nil {
			return err
		}
		deleted, err = result.RowsAffected()
		return err
	})
	if err != nil {
		return 0, err
	}
	if unfinished > 0 {
		s.log.Warn("expired alert events discarded unfinished deliveries", "unfinished_deliveries", unfinished)
	}
	return deleted, nil
}

func (s *Store) GetAlertEvent(ctx context.Context, id int64) (AlertEvent, error) {
	events, err := s.readAlertEvents(ctx, "WHERE id = ?", id)
	if err != nil {
		return AlertEvent{}, err
	}
	if len(events) == 0 {
		return AlertEvent{}, NotFoundError{Kind: ObjectAlertEvent, ID: id}
	}
	return events[0], nil
}
