package store

import (
	"context"
	"database/sql"
	"errors"
)

// probe_cert 是 (节点, 任务) 的最新一份证书到期观测（§8.3）：ingest 校验通过后的覆盖写，
// 与分钟桶的折叠路径分开——这是"最新值"，不是时间序列。

// upsertProbeCertTx 覆盖写一份观测，返回 not_after 是否发生变化（含首次写入）。
// 变化信号给调用方决定是否触发一次证书到期评估：同值重复上报不必重新评估。
func upsertProbeCertTx(tx *sql.Tx, nodeID int64, taskID uint64, notAfter, observedAt int64) (bool, error) {
	var prev int64
	err := tx.QueryRow("SELECT not_after FROM probe_cert WHERE node_id = ? AND task_id = ?", nodeID, int64(taskID)).Scan(&prev)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		prev = 0
	case err != nil:
		return false, err
	}
	if _, err := tx.Exec(`INSERT INTO probe_cert (node_id, task_id, not_after, observed_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (node_id, task_id) DO UPDATE SET not_after = excluded.not_after, observed_at = excluded.observed_at`,
		nodeID, int64(taskID), notAfter, observedAt); err != nil {
		return false, err
	}
	return prev != notAfter, nil
}

// UpsertProbeCert 同步覆盖写一份观测，返回 not_after 是否变化。管理面与测试用它；
// 上报路径用 UpsertProbeCertAsync（上报只碰内存，写走写协程）。
func (s *Store) UpsertProbeCert(ctx context.Context, nodeID int64, taskID uint64, notAfter, observedAt int64) (bool, error) {
	var changed bool
	err := s.write(ctx, func(tx *sql.Tx) error {
		var err error
		changed, err = upsertProbeCertTx(tx, nodeID, taskID, notAfter, observedAt)
		return err
	})
	return changed, err
}

// UpsertProbeCertAsync 经写协程覆盖写一份观测；done 在写协程里收到 not_after 是否变化，
// 需要据此调用会写库的入口（如证书到期评估）时必须在回调里另起协程，不能让写协程等自己。
func (s *Store) UpsertProbeCertAsync(nodeID int64, taskID uint64, notAfter, observedAt int64, done func(changed bool, err error)) {
	var changed bool
	s.writeAsync(func(tx *sql.Tx) error {
		var err error
		changed, err = upsertProbeCertTx(tx, nodeID, taskID, notAfter, observedAt)
		return err
	}, func(err error) {
		done(changed, err)
	})
}

// ProbeCertsByTask 返回该任务全部节点的最新证书观测（node_id → not_after，Unix 秒），
// 供证书到期告警评估；没有行的 (节点, 任务) 不在结果里——无读数，不是任何证书状态。
func (s *Store) ProbeCertsByTask(ctx context.Context, taskID uint64) (map[int64]int64, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT node_id, not_after FROM probe_cert WHERE task_id = ? ORDER BY node_id", int64(taskID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var nodeID, notAfter int64
		if err := rows.Scan(&nodeID, &notAfter); err != nil {
			return nil, err
		}
		out[nodeID] = notAfter
	}
	return out, rows.Err()
}
