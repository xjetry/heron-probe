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

// WriteMinuteRows 是 1m 行的唯一写入口。
//
// 冻结不变式：5m 水位之前的 1m 桶不再被写入，否则上级行不再反映下级行。
// 这里比较的是已持久化的水位而不是时钟，所以墙钟被向后拨、待重试列表里的
// 旧桶迟到，都越不过它。被拒绝的行计数返回并记日志，其余行照常写入。
func (s *Store) WriteMinuteRows(ctx context.Context, rows []metric.Row) (int, error) {
	rejected := 0
	err := s.write(ctx, func(tx *sql.Tx) error {
		var upto int64
		if err := tx.QueryRow("SELECT upto_ts FROM rollup_state WHERE level = '5m'").Scan(&upto); err != nil {
			return err
		}
		for _, r := range rows {
			if r.TS < upto {
				rejected++
				s.log.Warn("minute row before rollup watermark dropped", "node", r.NodeID, "ts", r.TS, "watermark", upto)
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

func (s *Store) SetRollupWatermark(ctx context.Context, level string, uptoTS int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec("UPDATE rollup_state SET upto_ts = ? WHERE level = ?", uptoTS, level)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}
