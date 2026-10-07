package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/xjetry/heron-probe/internal/agentwire"
)

// TrafficRecord 是 traffic 表的一行。基线（BootID、NetCounterEpoch、LastRx、LastTx）与累计值必须同一行同事务
// 写入：只要 agent 未换启动周期、统计作用域且计数器未倒退，崩溃后下一次上报相对已落盘基线
// 做差分能补回丢失的内存增量；
// 若两者分开落盘，基线新、累计旧的一次崩溃就会永久少记一段。
type TrafficRecord struct {
	NodeID                           int64
	BootID                           string
	NetCounterEpoch                  string
	LastRx, LastTx, TotalRx, TotalTx int64
	PeriodRx, PeriodTx               int64
	PeriodStart                      time.Time
}

const upsertTraffic = `INSERT INTO traffic (node_id, boot_id, last_rx, last_tx, total_rx, total_tx, period_rx, period_tx, period_start, updated_at, net_counter_epoch)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (node_id) DO UPDATE SET boot_id = excluded.boot_id, last_rx = excluded.last_rx, last_tx = excluded.last_tx,
  total_rx = excluded.total_rx, total_tx = excluded.total_tx, period_rx = excluded.period_rx, period_tx = excluded.period_tx,
  period_start = excluded.period_start, updated_at = excluded.updated_at, net_counter_epoch = excluded.net_counter_epoch`

// WriteTraffic 在一个事务里写入全部记录，并返回实际写入的节点 ID。节点已删除的记录跳过：删除后迟到的
// 刷出不得重建从属行，与 WriteMinuteBatch 同一处理；存在性在写事务内判定，与 DeleteNode 串行。
func (s *Store) WriteTraffic(ctx context.Context, recs []TrafficRecord) ([]int64, error) {
	var written []int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		written = nil
		now := s.clk.Now().Unix()
		for _, r := range recs {
			if err := agentwire.ValidateCounterEpoch(r.NetCounterEpoch); err != nil {
				return err
			}
			exists, err := nodeExistsTx(tx, r.NodeID)
			if err != nil {
				return err
			}
			if !exists {
				continue
			}
			if _, err := tx.Exec(upsertTraffic, r.NodeID, r.BootID, r.LastRx, r.LastTx, r.TotalRx, r.TotalTx,
				r.PeriodRx, r.PeriodTx, r.PeriodStart.Unix(), now, r.NetCounterEpoch); err != nil {
				return err
			}
			written = append(written, r.NodeID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return written, nil
}

func (s *Store) LoadTraffic(ctx context.Context) ([]TrafficRecord, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT node_id, boot_id, last_rx, last_tx, total_rx, total_tx, period_rx, period_tx, period_start, net_counter_epoch FROM traffic`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrafficRecord
	for rows.Next() {
		var r TrafficRecord
		var start int64
		if err := rows.Scan(&r.NodeID, &r.BootID, &r.LastRx, &r.LastTx, &r.TotalRx, &r.TotalTx, &r.PeriodRx, &r.PeriodTx, &start, &r.NetCounterEpoch); err != nil {
			return nil, err
		}
		if err := agentwire.ValidateCounterEpoch(r.NetCounterEpoch); err != nil {
			return nil, fmt.Errorf("node %d net_counter_epoch: %w", r.NodeID, err)
		}
		r.PeriodStart = time.Unix(start, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// TrafficResetDays 返回每个节点的周期重置日。重置日住在 node 表：它是节点的配置，
// 在节点建好、尚无一次上报时就该可改，而 traffic 行只在有流量状态后才存在。
func (s *Store) TrafficResetDays(ctx context.Context) (map[int64]int, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT id, traffic_reset_day FROM node")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var day int
		if err := rows.Scan(&id, &day); err != nil {
			return nil, err
		}
		out[id] = day
	}
	return out, rows.Err()
}
