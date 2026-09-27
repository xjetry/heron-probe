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

// ProbeTaskRecord 是任务与它当前覆盖的节点。AllNodes 为真时 NodeIDs 是读取时刻全部节点的展开结果，库里没有
// 对应的分配行；为假时 NodeIDs 就是分配行。两种情形 NodeIDs 都升序去重，读侧不必再区分。
type ProbeTaskRecord struct {
	Task     *probev1.ProbeTask
	AllNodes bool
	NodeIDs  []int64
}

// probeCoverage 是"任务覆盖哪些节点"的唯一读法：展开（LoadProbeTasks、SaveProbeTask 的回读、ProbeTaskNodeIDs）
// 与每节点上限的计数（SaveProbeTask、insertNode）都从它取，两处口径因此不会分叉。UNION ALL 的两支不重叠，靠的是
// all_nodes 任务没有分配行，由写侧保证：SaveProbeTask 更新任务时先删掉它的全部分配行、只在 all_nodes 为假时写回；
// 新建任务的 id 由 AUTOINCREMENT 分配、不复用，DeleteProbeTask 与任务同事务删掉分配行，所以新 id 没有旧分配行。
const probeCoverage = `SELECT t.id AS task_id, n.id AS node_id FROM probe_task t CROSS JOIN node n WHERE t.all_nodes = 1
UNION ALL SELECT task_id, node_id FROM probe_task_node`

const coveredNodesQuery = "SELECT node_id FROM (" + probeCoverage + ") WHERE task_id = ? ORDER BY node_id"

// LoadProbeTasks 在同一读事务内取得版本、任务和覆盖；并发保存不能把不同版本的行拼成一个清单。
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
	rows, err := tx.QueryContext(ctx, "SELECT id, kind, target, interval_s, timeout_ms, all_nodes FROM probe_task ORDER BY id")
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	var out []ProbeTaskRecord
	index := map[uint64]int{}
	for rows.Next() {
		t := &probev1.ProbeTask{}
		var id, kind, interval, timeout int64
		var all bool
		if err := rows.Scan(&id, &kind, &t.Target, &interval, &timeout, &all); err != nil {
			return 0, nil, err
		}
		t.Id, t.Kind, t.IntervalS, t.TimeoutMs = uint64(id), probev1.ProbeKind(kind), uint32(interval), uint32(timeout)
		index[t.Id] = len(out)
		out = append(out, ProbeTaskRecord{Task: t, AllNodes: all})
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	cover, err := tx.QueryContext(ctx, "SELECT task_id, node_id FROM ("+probeCoverage+") ORDER BY task_id, node_id")
	if err != nil {
		return 0, nil, err
	}
	defer cover.Close()
	for cover.Next() {
		var taskID, nodeID int64
		if err := cover.Scan(&taskID, &nodeID); err != nil {
			return 0, nil, err
		}
		if i, ok := index[uint64(taskID)]; ok {
			out[i].NodeIDs = append(out[i].NodeIDs, nodeID)
		}
	}
	if err := cover.Err(); err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return uint64(version), out, nil
}

// SaveProbeTask 在一个事务里写任务、整份替换分配、检查每节点上限、更新版本，并回读保存后的覆盖。
// allNodes 为真时 nodeIDs 被忽略、不写分配行。
//
// 上限在这里而不是调用方检查，因为只有事务内的计数才与其他保存互斥。它按写入之后的覆盖计数，所以 all_nodes 任务
// 计入每个现有节点，显式分配只计入被分配的节点。之后新建的节点也会继承 all_nodes 任务，这里管不到它们，
// 由 insertNode 在建节点时再查一次。
func (s *Store) SaveProbeTask(ctx context.Context, t *probev1.ProbeTask, allNodes bool, nodeIDs []int64) (ProbeTaskRecord, uint64, error) {
	saved := &probev1.ProbeTask{Id: t.GetId(), Kind: t.GetKind(), Target: t.GetTarget(), IntervalS: t.GetIntervalS(), TimeoutMs: t.GetTimeoutMs()}
	rec := ProbeTaskRecord{Task: saved, AllNodes: allNodes}
	var version int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		if saved.Id == 0 {
			res, err := tx.Exec("INSERT INTO probe_task (kind, target, interval_s, timeout_ms, created_at, all_nodes) VALUES (?, ?, ?, ?, ?, ?)",
				int64(saved.Kind), saved.Target, int64(saved.IntervalS), int64(saved.TimeoutMs), s.clk.Now().Unix(), allNodes)
			if err != nil {
				return err
			}
			id, err := res.LastInsertId()
			if err != nil {
				return err
			}
			saved.Id = uint64(id)
		} else {
			res, err := tx.Exec("UPDATE probe_task SET kind = ?, target = ?, interval_s = ?, timeout_ms = ?, all_nodes = ? WHERE id = ?",
				int64(saved.Kind), saved.Target, int64(saved.IntervalS), int64(saved.TimeoutMs), allNodes, int64(saved.Id))
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return NotFoundError{Kind: ObjectProbeTask, ID: int64(saved.Id)}
			}
			if _, err := tx.Exec("DELETE FROM probe_task_node WHERE task_id = ?", int64(saved.Id)); err != nil {
				return err
			}
		}
		if !allNodes {
			for _, nodeID := range nodeIDs {
				exists, err := nodeExistsTx(tx, nodeID)
				if err != nil {
					return err
				}
				if !exists {
					return NotFoundError{Kind: ObjectNode, ID: nodeID}
				}
				if _, err := tx.Exec("INSERT INTO probe_task_node (task_id, node_id) VALUES (?, ?)", int64(saved.Id), nodeID); err != nil {
					return err
				}
			}
		}
		var over, tasks int64
		err := tx.QueryRow("SELECT node_id, COUNT(*) FROM ("+probeCoverage+") GROUP BY node_id HAVING COUNT(*) > ? ORDER BY node_id LIMIT 1",
			probelimit.MaxTasksPerNode).Scan(&over, &tasks)
		if err == nil {
			return NodeLimitError{NodeID: over, Tasks: int(tasks), Max: probelimit.MaxTasksPerNode}
		}
		if err != sql.ErrNoRows {
			return err
		}
		if rec.NodeIDs, err = coveredNodesTx(tx, saved.Id); err != nil {
			return err
		}
		v, err := bumpProbeVersion(tx, s.clk.Now().Unix())
		version = v
		return err
	})
	if err != nil {
		return ProbeTaskRecord{}, 0, err
	}
	return rec, uint64(version), nil
}

func coveredNodesTx(tx *sql.Tx, taskID uint64) ([]int64, error) {
	return scanIDs(tx.Query(coveredNodesQuery, int64(taskID)))
}

// DeleteProbeTask 删任务与分配并加版本；历史行不删（§8.3），到期由 prune 清理。
func (s *Store) DeleteProbeTask(ctx context.Context, id uint64) (uint64, error) {
	var version int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := checkAlertReferences(tx, "SELECT id, name FROM alert_rule WHERE task_id = ? ORDER BY id", ObjectProbeTask, int64(id)); err != nil {
			return err
		}
		res, err := tx.Exec("DELETE FROM probe_task WHERE id = ?", int64(id))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return NotFoundError{Kind: ObjectProbeTask, ID: int64(id)}
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

// 版本的不变式：任何一个节点的清单变了，版本就变。agent 只比较相等，版本相同即认定清单未变、不再重取，所以
// 每个改变某节点清单的事务都必须调用它：任务的保存与删除，以及建节点——新节点的清单从空变为全部 all_nodes 任务
// （insertNode）。删除节点不调用：只有被删节点的清单消失，它的 token 已撤销，其余节点的清单不变。
//
// 版本与任务及分配同事务更新，并以修改时刻的 Unix 秒托底。恢复后修改的 Unix 秒大于旧库最后版本值时不会碰撞；
// 同秒重做、时钟回拨或旧版本超前仍可能碰撞，此时需重启 agent 使它重新对账，不能把时间托底当作全局唯一保证。
func bumpProbeVersion(tx *sql.Tx, now int64) (int64, error) {
	var version int64
	err := tx.QueryRow("UPDATE probe_meta SET version = max(version + 1, ?) WHERE id = 1 RETURNING version", now).Scan(&version)
	return version, err
}

// ProbeTaskNodeIDs 返回任务当前覆盖的节点（all_nodes 任务展开为全部节点），升序。
func (s *Store) ProbeTaskNodeIDs(ctx context.Context, taskID uint64) ([]int64, error) {
	return scanIDs(s.r.QueryContext(ctx, coveredNodesQuery, int64(taskID)))
}

func scanIDs(rows *sql.Rows, err error) ([]int64, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
