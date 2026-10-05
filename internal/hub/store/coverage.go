package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/xjetry/heron-probe/internal/hub/metric"
)

// 已证实为真不会被未知覆盖；SQLite 标量 max(NULL, 1) 不具备这个性质。
func coverageOr(a, b string) string {
	return "CASE WHEN " + a + " = 1 OR " + b + " = 1 THEN 1 WHEN " + a + " IS NULL OR " + b + " IS NULL THEN NULL ELSE 0 END"
}

func coverageAnd(a, b string) string {
	return "CASE WHEN " + a + " = 0 OR " + b + " = 0 THEN 0 WHEN " + a + " IS NULL OR " + b + " IS NULL THEN NULL ELSE 1 END"
}

func coverageSource(i int) []string {
	if i == 0 {
		return []string{"reported AS minutes", "observed", coverageAnd("reported", "observed") + " AS both"}
	}
	return []string{"minutes", "observed", "both"}
}

// 每列独立传播未知；SUM 单独使用会把未知分钟当成不存在。
func coverageAggregates() []string {
	var out []string
	for _, c := range []string{"minutes", "observed", "both"} {
		out = append(out, "CASE WHEN count("+c+") = count(*) THEN sum("+c+") ELSE NULL END")
	}
	return out
}

func coverageCount(n sql.NullInt64) *uint32 {
	if !n.Valid {
		return nil
	}
	v := uint32(n.Int64)
	return &v
}

func (s *Store) CoverageStarts(ctx context.Context) (map[int64]int64, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT node_id, start_ts FROM node_coverage")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var id, start int64
		if err := rows.Scan(&id, &start); err != nil {
			return nil, err
		}
		out[id] = start
	}
	return out, rows.Err()
}

// QueryMetricsCoverage 的起点、水位、指标和覆盖汇总共用 queryFamily 的同一个快照。
// 汇总在输出重分桶之前读取源桶宽度，maxPoints 只改变展示，不改变可证明的分钟数。
func (s *Store) QueryMetricsCoverage(ctx context.Context, nodeID, from, to int64, lv Level, step int64) ([]metric.Row, metric.CoverageSummary, error) {
	var summary metric.CoverageSummary
	now := alignDown(s.clk.Now().Unix(), 60)
	rows, err := queryFamily(ctx, s, metricFamily, nodeID, from, to, lv, step, scanBucketRows,
		func(tx *sql.Tx, sources string, args []any) error {
			var start int64
			err := tx.QueryRowContext(ctx, "SELECT start_ts FROM node_coverage WHERE node_id = ?", nodeID).Scan(&start)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			summary.CoverageStart = &start
			// 使用差值避免 ceil(from/60)*60 在极大 Unix 秒处溢出。
			end := min(alignDown(to, 60), now)
			begin := max(from, start)
			if end <= begin {
				return nil
			}
			summary.EligibleMinutes = uint64((end - begin) / 60)
			begin = end - int64(summary.EligibleMinutes)*60
			params := append(append([]any{}, args...), begin, end)
			return tx.QueryRowContext(ctx, "SELECT coalesce(sum(observed),0), coalesce(sum(both),0) FROM ("+sources+") WHERE ts >= ? AND source_width <= ? - ts", params...).Scan(&summary.ObservedMinutes, &summary.ObservedReportedMinutes)
		})
	return rows, summary, err
}

func writeCoverageStart(tx *sql.Tx, r metric.Row) error {
	if r.CoverageStart%60 != 0 {
		return fmt.Errorf("coverage start %d is not minute aligned", r.CoverageStart)
	}
	if _, err := tx.Exec("INSERT INTO node_coverage(node_id,start_ts) VALUES (?,?) ON CONFLICT DO NOTHING", r.NodeID, r.CoverageStart); err != nil {
		return err
	}
	var start int64
	if err := tx.QueryRow("SELECT start_ts FROM node_coverage WHERE node_id=?", r.NodeID).Scan(&start); err != nil {
		return err
	}
	if (r.Observed || r.ObservationOnly) && (start > r.TS || r.CoverageStart > r.TS) {
		return fmt.Errorf("observation precedes coverage start for node %d", r.NodeID)
	}
	if r.ObservationOnly {
		if !r.Observed || !r.LastSeen.IsZero() || r.Source != "" {
			return fmt.Errorf("observation-only row carries report state")
		}
		for _, n := range r.Bucket.N {
			if n != 0 {
				return fmt.Errorf("observation-only row carries metric samples")
			}
		}
	}
	return nil
}

func metricSourceSQL(table string, i int) string {
	return "SELECT node_id, ts, " + strings.Join(append(metricColumnNames(), coverageSource(i)...), ", ") + " FROM " + table
}
