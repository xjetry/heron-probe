package store

import (
	"context"
	"database/sql"

	"github.com/xjetry/probe/internal/hub/metric"
)

var (
	upsertMinute = metricUpsert("metric_1m")
	selectMinute = metricSelect("metric_1m")
)

// WriteMinuteBatch 是两族 1m 行的唯一写入口：同一事务里写指标行与探测行，节点存在性
// 在事务内检查（与 DeleteNode 串行），各族按自己的 5m 水位做冻结检查——两族各自上卷，
// 一族的水位不能替另一族决定是否接受写入。被拒绝的行计数返回并记日志，其余行照常写入。
// 比较的是已持久化的水位而不是时钟，墙钟回拨或重试旧桶都不能改写已冻结的历史。
func (s *Store) WriteMinuteBatch(ctx context.Context, batch metric.Batch) (int, error) {
	rejected := 0
	err := s.write(ctx, func(tx *sql.Tx) error {
		var metricUpto, probeUpto int64
		if err := tx.QueryRow("SELECT upto_ts FROM rollup_state WHERE level = '5m'").Scan(&metricUpto); err != nil {
			return err
		}
		if err := tx.QueryRow("SELECT upto_ts FROM rollup_state WHERE level = 'probe_5m'").Scan(&probeUpto); err != nil {
			return err
		}
		existing := map[int64]bool{}
		exists := func(nodeID int64) (bool, error) {
			if ok, checked := existing[nodeID]; checked {
				return ok, nil
			}
			ok, err := nodeExistsTx(tx, nodeID)
			if err == nil {
				existing[nodeID] = ok
			}
			return ok, err
		}
		for _, r := range batch.Rows {
			ok, err := exists(r.NodeID)
			if err != nil {
				return err
			}
			if !ok {
				rejected++
				s.log.Warn("minute row for deleted node dropped", "node", r.NodeID)
				continue
			}
			if r.TS < metricUpto {
				rejected++
				s.log.Warn("minute row before rollup watermark dropped", "node", r.NodeID, "ts", r.TS, "watermark", metricUpto)
				continue
			}
			args := append([]any{r.NodeID, r.TS}, bucketArgs(r.Bucket)...)
			if _, err := tx.Exec(upsertMinute, args...); err != nil {
				return err
			}
			if !r.LastSeen.IsZero() {
				if _, err := tx.Exec("UPDATE node SET last_seen_at = ? WHERE id = ?", r.LastSeen.Unix(), r.NodeID); err != nil {
					return err
				}
			}
		}
		for _, r := range batch.Probes {
			ok, err := exists(r.NodeID)
			if err != nil {
				return err
			}
			if !ok {
				rejected++
				s.log.Warn("probe row for deleted node dropped", "node", r.NodeID, "task", r.TaskID)
				continue
			}
			if r.TS < probeUpto {
				rejected++
				s.log.Warn("probe row before rollup watermark dropped", "node", r.NodeID, "task", r.TaskID, "ts", r.TS, "watermark", probeUpto)
				continue
			}
			if _, err := tx.Exec(upsertProbeMinute, probeArgs(r)...); err != nil {
				return err
			}
		}
		return nil
	})
	return rejected, err
}

func (s *Store) ReadMinuteRows(ctx context.Context, nodeID int64, from, to int64) ([]metric.Row, error) {
	rows, err := s.r.QueryContext(ctx, selectMinute, nodeID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBucketRows(rows, nodeID)
}

// scanBucketRows 按 metricColumnNames 的顺序扫描 (ts, 列…) 行；整数列先落到
// int64 再转回 float64。分钟读与聚合读共用它，列序只在描述表里定义一次。
func scanBucketRows(rows *sql.Rows, nodeID int64) ([]metric.Row, error) {
	var out []metric.Row
	for rows.Next() {
		b := metric.NewBucket()
		var ts int64
		dest := []any{&ts}
		// 扫描目标与 metricColumnNames 同序；整数列先落到 int64 再转回 float64。
		ints := make([]int64, 0, 3*len(metric.Columns))
		for _, c := range metric.Columns {
			if c.Type == metric.Int {
				ints = append(ints, 0, 0)
				dest = append(dest, &ints[len(ints)-2], &ints[len(ints)-1])
				if c.Kind == metric.MeanMax {
					ints = append(ints, 0)
					dest = append(dest, &ints[len(ints)-1])
				}
			} else {
				ints = append(ints, 0)
				dest = append(dest, new(float64), &ints[len(ints)-1])
				if c.Kind == metric.MeanMax {
					dest = append(dest, new(float64))
				}
			}
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		di := 1
		for i, c := range metric.Columns {
			if c.Type == metric.Int {
				b.Sum[i] = float64(*dest[di].(*int64))
				b.N[i] = uint32(*dest[di+1].(*int64))
				di += 2
				if c.Kind == metric.MeanMax {
					b.Max[i] = float64(*dest[di].(*int64))
					di++
				}
			} else {
				b.Sum[i] = *dest[di].(*float64)
				b.N[i] = uint32(*dest[di+1].(*int64))
				di += 2
				if c.Kind == metric.MeanMax {
					b.Max[i] = *dest[di].(*float64)
					di++
				}
			}
		}
		out = append(out, metric.Row{NodeID: nodeID, TS: ts, Bucket: b})
	}
	return out, rows.Err()
}
