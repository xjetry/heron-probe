package store

import (
	"context"
	"database/sql"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/metric"
	"github.com/xjetry/probe/internal/probelimit"
)

func probeValueColumns() []string {
	return []string{"sent", "lost", "errors", "rtt_sum_us", "rtt_min_us", "rtt_max_us"}
}

// 聚合 min()/max() 忽略 NULL：没有 rtt 样本的桶不会把 0 带进最小值。
func probeAggregates() []string {
	return []string{"sum(sent)", "sum(lost)", "sum(errors)", "sum(rtt_sum_us)", "min(rtt_min_us)", "max(rtt_max_us)"}
}

// 标量 min(a, b) 在任一参数为 NULL 时返回 NULL，所以两侧先各自 coalesce：
// 双方都 NULL 得 NULL，一方 NULL 得另一方，都有值取更小者；max 同理。
const upsertProbeMinute = `INSERT INTO probe_1m (node_id, ts, task_id, sent, lost, errors, rtt_sum_us, rtt_min_us, rtt_max_us)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (node_id, ts, task_id) DO UPDATE SET
  sent = sent + excluded.sent, lost = lost + excluded.lost, errors = errors + excluded.errors,
  rtt_sum_us = rtt_sum_us + excluded.rtt_sum_us,
  rtt_min_us = min(coalesce(rtt_min_us, excluded.rtt_min_us), coalesce(excluded.rtt_min_us, rtt_min_us)),
  rtt_max_us = max(coalesce(rtt_max_us, excluded.rtt_max_us), coalesce(excluded.rtt_max_us, rtt_max_us))`

func probeArgs(r metric.ProbeRow) []any {
	b := r.Bucket
	var mn, mx any
	if b.RttN > 0 {
		mn, mx = int64(b.RttMinUs), int64(b.RttMaxUs)
	}
	return []any{r.NodeID, r.TS, int64(r.TaskID), int64(b.Sent), int64(b.Lost), int64(b.Errors), int64(b.RttSumUs), mn, mx}
}

// scanProbeRows 扫描 (ts, task_id, 六个值列)。RttN 由 sent − lost − errors 推出，
// 与 rtt_min_us 是否为 NULL 必须一致：写侧 RttN 为 0 时才写 NULL。
func scanProbeRows(rows *sql.Rows, nodeID int64) ([]metric.ProbeRow, error) {
	var out []metric.ProbeRow
	for rows.Next() {
		var ts, taskID, sent, lost, errs, sum int64
		var mn, mx sql.NullInt64
		if err := rows.Scan(&ts, &taskID, &sent, &lost, &errs, &sum, &mn, &mx); err != nil {
			return nil, err
		}
		b := &metric.ProbeBucket{Sent: uint32(sent), Lost: uint32(lost), Errors: uint32(errs), RttSumUs: uint64(sum)}
		if mn.Valid {
			b.RttN = uint32(sent - lost - errs)
			b.RttMinUs, b.RttMaxUs = uint32(mn.Int64), uint32(mx.Int64)
		}
		out = append(out, metric.ProbeRow{NodeID: nodeID, TS: ts, TaskID: uint64(taskID), Bucket: b})
	}
	return out, rows.Err()
}

type ProbeTaskRecord struct {
	Task    *probev1.ProbeTask
	NodeIDs []int64
}

// LoadProbeTasks 在同一读事务内取得版本、任务和分配；并发保存不能把不同版本的行拼成一个清单。
func (s *Store) LoadProbeTasks(ctx context.Context) (uint64, []ProbeTaskRecord, error) {
	tx, err := s.r.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()
	var version int64
	if err := tx.QueryRowContext(ctx, "SELECT version FROM probe_meta WHERE id = 1").Scan(&version); err != nil {
		return 0, nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id, kind, target, interval_s, timeout_ms FROM probe_task ORDER BY id")
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	var out []ProbeTaskRecord
	index := map[uint64]int{}
	for rows.Next() {
		t := &probev1.ProbeTask{}
		var id, kind, interval, timeout int64
		if err := rows.Scan(&id, &kind, &t.Target, &interval, &timeout); err != nil {
			return 0, nil, err
		}
		t.Id, t.Kind, t.IntervalS, t.TimeoutMs = uint64(id), probev1.ProbeKind(kind), uint32(interval), uint32(timeout)
		index[t.Id] = len(out)
		out = append(out, ProbeTaskRecord{Task: t})
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	assign, err := tx.QueryContext(ctx, "SELECT task_id, node_id FROM probe_task_node ORDER BY task_id, node_id")
	if err != nil {
		return 0, nil, err
	}
	defer assign.Close()
	for assign.Next() {
		var taskID, nodeID int64
		if err := assign.Scan(&taskID, &nodeID); err != nil {
			return 0, nil, err
		}
		if i, ok := index[uint64(taskID)]; ok {
			out[i].NodeIDs = append(out[i].NodeIDs, nodeID)
		}
	}
	if err := assign.Err(); err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return uint64(version), out, nil
}

// SaveProbeTask 在一个事务里写任务、整份替换分配、更新版本。分配行写入前检查节点存在
// 与每节点上限：上限在这里而不是调用方检查，因为只有事务内的计数才与其他保存互斥。
func (s *Store) SaveProbeTask(ctx context.Context, t *probev1.ProbeTask, nodeIDs []int64) (*probev1.ProbeTask, uint64, error) {
	saved := &probev1.ProbeTask{Id: t.GetId(), Kind: t.GetKind(), Target: t.GetTarget(), IntervalS: t.GetIntervalS(), TimeoutMs: t.GetTimeoutMs()}
	var version int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		if saved.Id == 0 {
			res, err := tx.Exec("INSERT INTO probe_task (kind, target, interval_s, timeout_ms, created_at) VALUES (?, ?, ?, ?, ?)",
				int64(saved.Kind), saved.Target, int64(saved.IntervalS), int64(saved.TimeoutMs), s.clk.Now().Unix())
			if err != nil {
				return err
			}
			id, err := res.LastInsertId()
			if err != nil {
				return err
			}
			saved.Id = uint64(id)
		} else {
			res, err := tx.Exec("UPDATE probe_task SET kind = ?, target = ?, interval_s = ?, timeout_ms = ? WHERE id = ?",
				int64(saved.Kind), saved.Target, int64(saved.IntervalS), int64(saved.TimeoutMs), int64(saved.Id))
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return NotFoundError{Kind: "probe task", ID: int64(saved.Id)}
			}
			if _, err := tx.Exec("DELETE FROM probe_task_node WHERE task_id = ?", int64(saved.Id)); err != nil {
				return err
			}
		}
		for _, nodeID := range nodeIDs {
			exists, err := nodeExistsTx(tx, nodeID)
			if err != nil {
				return err
			}
			if !exists {
				return NotFoundError{Kind: "node", ID: nodeID}
			}
			var n int
			if err := tx.QueryRow("SELECT COUNT(*) FROM probe_task_node WHERE node_id = ?", nodeID).Scan(&n); err != nil {
				return err
			}
			if n >= probelimit.MaxTasksPerNode {
				return NodeLimitError{NodeID: nodeID, Max: probelimit.MaxTasksPerNode}
			}
			if _, err := tx.Exec("INSERT INTO probe_task_node (task_id, node_id) VALUES (?, ?)", int64(saved.Id), nodeID); err != nil {
				return err
			}
		}
		v, err := bumpProbeVersion(tx, s.clk.Now().Unix())
		version = v
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	return saved, uint64(version), nil
}

// DeleteProbeTask 删任务与分配并加版本；历史行不删（§8.3），到期由 prune 清理。
func (s *Store) DeleteProbeTask(ctx context.Context, id uint64) (uint64, error) {
	var version int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec("DELETE FROM probe_task WHERE id = ?", int64(id))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return NotFoundError{Kind: "probe task", ID: int64(id)}
		}
		if _, err := tx.Exec("DELETE FROM probe_task_node WHERE task_id = ?", int64(id)); err != nil {
			return err
		}
		v, err := bumpProbeVersion(tx, s.clk.Now().Unix())
		version = v
		return err
	})
	return uint64(version), err
}

// agent 只比较整数相等；版本与任务及分配同事务更新，并以修改时刻的 Unix 秒托底。
// 恢复后修改的 Unix 秒大于旧库最后版本值时不会碰撞；同秒重做、时钟回拨或旧版本超前
// 仍可能碰撞，此时需重启 agent 使它重新对账，不能把时间托底当作全局唯一保证。
func bumpProbeVersion(tx *sql.Tx, now int64) (int64, error) {
	var version int64
	err := tx.QueryRow("UPDATE probe_meta SET version = max(version + 1, ?) WHERE id = 1 RETURNING version", now).Scan(&version)
	return version, err
}
