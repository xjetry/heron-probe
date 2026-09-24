package store

import (
	"context"
	"database/sql"
	"errors"
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
	ID         int64
	Name       string
	Kind       AlertKind
	Enabled    bool
	NodeIDs    []int64 // 升序去重；空作用域表示全部节点。
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
	ID          int64
	EventID     int64
	ChannelID   int64
	Attempts    int
	OK          bool
	LastError   string
	DeliveredAt time.Time
}

// 主表与关联在同一读事务中读取，不能把并发保存前后的两份作用域拼在一起。
func (s *Store) ListAlertRules(ctx context.Context) ([]AlertRule, error) {
	tx, err := s.r.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id, name, kind, enabled, task_id, metric, threshold, for_minutes, created_at FROM alert_rule ORDER BY id`)
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
		if err := rows.Scan(&r.ID, &r.Name, &r.Kind, &r.Enabled, &task, &metric, &threshold, &minutes, &created); err != nil {
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
	err := s.write(ctx, func(tx *sql.Tx) error {
		for _, id := range r.NodeIDs {
			exists, err := nodeExistsTx(tx, id)
			if err != nil {
				return err
			}
			if !exists {
				return NotFoundError{Kind: "node", ID: id}
			}
		}
		for _, id := range r.ChannelIDs {
			if err := requireAlertReference(tx, "notify_channel", "notify channel", id); err != nil {
				return err
			}
		}
		var task, metric, threshold, minutes any
		if r.Kind == KindProbe {
			if err := requireAlertReference(tx, "probe_task", "probe task", int64(r.TaskID)); err != nil {
				return err
			}
			task, metric, threshold, minutes = int64(r.TaskID), r.Metric, r.Threshold, r.ForMinutes
		} else {
			r.TaskID, r.Metric, r.Threshold, r.ForMinutes = 0, "", 0, 0
		}
		var created int64
		if r.ID == 0 {
			created = s.clk.Now().Unix()
			err := tx.QueryRow(`INSERT INTO alert_rule (name, kind, enabled, task_id, metric, threshold, for_minutes, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`, r.Name, r.Kind, r.Enabled, task, metric, threshold, minutes, created).Scan(&r.ID)
			if err != nil {
				return err
			}
		} else {
			err := tx.QueryRow(`UPDATE alert_rule SET name = ?, kind = ?, enabled = ?, task_id = ?, metric = ?, threshold = ?, for_minutes = ?
				WHERE id = ? RETURNING created_at`, r.Name, r.Kind, r.Enabled, task, metric, threshold, minutes, r.ID).Scan(&created)
			if errors.Is(err, sql.ErrNoRows) {
				return NotFoundError{Kind: "alert rule", ID: r.ID}
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
		return nil
	})
	if err != nil {
		return AlertRule{}, err
	}
	return r, nil
}

func requireAlertReference(tx *sql.Tx, table, kind string, id int64) error {
	var one int
	err := tx.QueryRow("SELECT 1 FROM "+table+" WHERE id = ?", id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return NotFoundError{Kind: kind, ID: id}
	}
	return err
}

func (s *Store) DeleteAlertRule(ctx context.Context, id int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if err := deleteAlertEntity(tx, "alert_rule", "alert rule", id); err != nil {
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

func deleteAlertEntity(tx *sql.Tx, table, kind string, id int64) error {
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
				return NotFoundError{Kind: "notify channel", ID: c.ID}
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
func checkAlertReferences(tx *sql.Tx, query, kind string, id int64) error {
	rows, err := tx.Query(query, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(names) > 0 {
		return InUseError{Kind: kind, ID: id, Rules: names}
	}
	return nil
}

func (s *Store) DeleteNotifyChannel(ctx context.Context, id int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if err := checkAlertReferences(tx, `SELECT r.name FROM alert_rule r JOIN alert_rule_channel c ON c.rule_id = r.id WHERE c.channel_id = ? ORDER BY r.id`, "notify channel", id); err != nil {
			return err
		}
		return deleteAlertEntity(tx, "notify_channel", "notify channel", id)
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

func setAlertState(tx *sql.Tx, ruleID, nodeID int64, state AlertState, since time.Time) error {
	_, err := tx.Exec("INSERT OR REPLACE INTO alert_state (rule_id, node_id, state, since_at) VALUES (?, ?, ?, ?)", ruleID, nodeID, state, since.Unix())
	return err
}

func (s *Store) SetAlertState(ctx context.Context, ruleID, nodeID int64, state AlertState, since time.Time) error {
	return s.write(ctx, func(tx *sql.Tx) error { return setAlertState(tx, ruleID, nodeID, state, since) })
}

func (s *Store) DeleteAlertStates(ctx context.Context, ruleID int64, keepNodeIDs []int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		query := "DELETE FROM alert_state WHERE rule_id = ?"
		args := []any{ruleID}
		if len(keepNodeIDs) > 0 {
			query += " AND node_id NOT IN (" + placeholders(len(keepNodeIDs)) + ")"
			for _, id := range keepNodeIDs {
				args = append(args, id)
			}
		}
		_, err := tx.Exec(query, args...)
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

func (s *Store) UpdateDelivery(ctx context.Context, id int64, attempts int, ok bool, lastError string, at time.Time) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var delivered any
		if !at.IsZero() {
			delivered = at.Unix()
		}
		res, err := tx.Exec("UPDATE alert_delivery SET attempts = ?, ok = ?, last_error = ?, delivered_at = ? WHERE id = ?", attempts, ok, lastError, delivered, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return NotFoundError{Kind: "alert delivery", ID: id}
		}
		return nil
	})
}

const selectDeliveries = "SELECT id, event_id, channel_id, attempts, ok, last_error, delivered_at FROM alert_delivery"

func scanDeliveries(rows *sql.Rows) ([]Delivery, error) {
	var out []Delivery
	for rows.Next() {
		var d Delivery
		var delivered sql.NullInt64
		if err := rows.Scan(&d.ID, &d.EventID, &d.ChannelID, &d.Attempts, &d.OK, &d.LastError, &delivered); err != nil {
			return nil, err
		}
		if delivered.Valid {
			d.DeliveredAt = time.Unix(delivered.Int64, 0).UTC()
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) PendingDeliveries(ctx context.Context, maxAttempts int) ([]Delivery, error) {
	rows, err := s.r.QueryContext(ctx, selectDeliveries+" WHERE ok = 0 AND attempts < ? ORDER BY id", maxAttempts)
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
	rows, err := tx.QueryContext(ctx, "SELECT id, rule_id, node_id, transition, at, summary, value FROM alert_event "+predicate, args...)
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
	return s.readAlertEvents(ctx, "WHERE (? = 0 OR node_id = ?) AND (? = 0 OR id < ?) ORDER BY id DESC LIMIT ?", nodeID, nodeID, beforeID, beforeID, limit)
}

func (s *Store) GetAlertEvent(ctx context.Context, id int64) (AlertEvent, error) {
	events, err := s.readAlertEvents(ctx, "WHERE id = ?", id)
	if err != nil {
		return AlertEvent{}, err
	}
	if len(events) == 0 {
		return AlertEvent{}, NotFoundError{Kind: "alert event", ID: id}
	}
	return events[0], nil
}
