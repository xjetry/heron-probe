package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

type AlertKind string

const (
	KindOffline AlertKind = "offline"
	KindProbe   AlertKind = "probe"
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
	HTTPStatus int             // 仅 FailureHTTPStatus 非零。
	// 最近一次失败的原文：HTTP 失败时是响应体片段，其余是出站错误文本，都不含 URL。
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
	rows, err := tx.QueryContext(ctx, `SELECT id, name, kind, enabled, all_nodes, task_id, metric, threshold, for_minutes, created_at FROM alert_rule ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertRule
	index := map[int64]int{}
	for rows.Next() {
		var r AlertRule
		var task, minutes sql.NullInt64
		var metric sql.NullString
		var threshold sql.NullFloat64
		var created int64
		if err := rows.Scan(&r.ID, &r.Name, &r.Kind, &r.Enabled, &r.AllNodes, &task, &metric, &threshold, &minutes, &created); err != nil {
			return nil, err
		}
		r.TaskID, r.Metric, r.Threshold, r.ForMinutes = uint64(task.Int64), ProbeMetric(metric.String), threshold.Float64, int(minutes.Int64)
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

// 引用检查与保存同在单写事务，删除不能插入两者之间造成孤儿引用。
func (s *Store) SaveAlertRule(ctx context.Context, r AlertRule) (AlertRule, error) {
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
		var task, metric, threshold, minutes any
		if r.Kind == KindProbe {
			if err := requireAlertReference(tx, "probe_task", ObjectProbeTask, int64(r.TaskID)); err != nil {
				return err
			}
			task, metric, threshold, minutes = int64(r.TaskID), r.Metric, r.Threshold, r.ForMinutes
		} else {
			r.TaskID, r.Metric, r.Threshold, r.ForMinutes = 0, "", 0, 0
		}
		var created int64
		var identityChanged bool
		if r.ID == 0 {
			created = s.clk.Now().Unix()
			err := tx.QueryRow(`INSERT INTO alert_rule (name, kind, enabled, all_nodes, task_id, metric, threshold, for_minutes, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`, r.Name, r.Kind, r.Enabled, r.AllNodes, task, metric, threshold, minutes, created).Scan(&r.ID)
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
			err = tx.QueryRow(`UPDATE alert_rule SET name = ?, kind = ?, enabled = ?, all_nodes = ?, task_id = ?, metric = ?, threshold = ?, for_minutes = ?
				WHERE id = ? RETURNING created_at`, r.Name, r.Kind, r.Enabled, r.AllNodes, task, metric, threshold, minutes, r.ID).Scan(&created)
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
		// threshold 与 for_minutes 不是身份：同一个量换阈值时保留状态，下一轮按新阈值判断是否恢复。
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
		_, err := tx.Exec("UPDATE alert_delivery SET done = 1, failure = ?, http_status = NULL, last_error = '' WHERE channel_id = ? AND done = 0", FailureChannelDeleted, id)
		return err
	})
}

func (s *Store) ListAlertStates(ctx context.Context) ([]StateRow, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT rule_id, node_id, state, since_at FROM alert_state ORDER BY rule_id, node_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StateRow
	for rows.Next() {
		var r StateRow
		var since int64
		if err := rows.Scan(&r.RuleID, &r.NodeID, &r.State, &since); err != nil {
			return nil, err
		}
		r.SinceAt = time.Unix(since, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// 两个状态写入口共用事务内准入，单写协程保证删除之后排队的写不能重建孤儿状态。
func setAlertState(tx *sql.Tx, ruleID, nodeID int64, state AlertState, since time.Time) error {
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
	_, err = tx.Exec("INSERT OR REPLACE INTO alert_state (rule_id, node_id, state, since_at) VALUES (?, ?, ?, ?)", ruleID, nodeID, state, since.Unix())
	return err
}

func (s *Store) SetAlertState(ctx context.Context, ruleID, nodeID int64, state AlertState, since time.Time) error {
	return s.write(ctx, func(tx *sql.Tx) error { return setAlertState(tx, ruleID, nodeID, state, since) })
}

// 候选集撤销不是恢复观测，删除状态不产生事件；重复清理同一对规则与节点仍然成功。
func (s *Store) DeleteAlertState(ctx context.Context, ruleID, nodeID int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM alert_state WHERE rule_id = ? AND node_id = ?", ruleID, nodeID)
		return err
	})
}

// 状态、事件与续投队列同事务提交，崩溃不能留下已转换但没有通知记录的状态。
func (s *Store) RecordTransition(ctx context.Context, ruleID, nodeID int64, state AlertState, ev AlertEvent, channelIDs []int64) (AlertEvent, error) {
	ev.ID, ev.RuleID, ev.NodeID, ev.Deliveries = 0, ruleID, nodeID, nil
	ev.At = time.Unix(ev.At.Unix(), 0).UTC()
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := setAlertState(tx, ruleID, nodeID, state, ev.At); err != nil {
			return err
		}
		if err := tx.QueryRow("INSERT INTO alert_event (rule_id, node_id, transition, at, summary, value) VALUES (?, ?, ?, ?, ?, ?) RETURNING id", ruleID, nodeID, ev.Transition, ev.At.Unix(), ev.Summary, ev.Value).Scan(&ev.ID); err != nil {
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
	})
	if err != nil {
		return AlertEvent{}, err
	}
	return ev, nil
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

// 读侧据类别解释状态码与原文、只读口径据类别给出状态码，都依赖这里的一致性；
// 违反即拒绝，不写库，也不改写成某个"最接近"的类别。
func (r DeliveryResult) check() error {
	if r.OK {
		if r.Failure != FailureNone || r.HTTPStatus != 0 || r.Error != "" {
			return fmt.Errorf("delivery result: success carries failure %q, status %d, error %q", r.Failure, r.HTTPStatus, r.Error)
		}
		return nil
	}
	switch r.Failure {
	case FailureNone:
		return errors.New("delivery result: failure without category")
	case FailureHTTPStatus, FailureTransport, FailureRequest, FailureChannelInvalid, FailureChannelDeleted, FailureResultUnrecorded, FailureUnclassified:
	default:
		return fmt.Errorf("delivery result: unknown failure category %q", r.Failure)
	}
	if (r.Failure == FailureHTTPStatus) != (r.HTTPStatus >= 100 && r.HTTPStatus <= 999) {
		return fmt.Errorf("delivery result: failure %q with HTTP status %d", r.Failure, r.HTTPStatus)
	}
	if (r.Failure == FailureChannelDeleted || r.Failure == FailureResultUnrecorded) && r.Error != "" {
		return fmt.Errorf("delivery result: failure %q carries error text", r.Failure)
	}
	return nil
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
