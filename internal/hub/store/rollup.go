package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/xjetry/probe/internal/hub/metric"
)

// RollupLag 是上卷不越过的滞后：只上卷结束时刻不晚于 now − RollupLag 的桶。
// 它必须覆盖一条数据最晚成为 1m 行的时间（迟到上限 + 一个刷出周期 + 排队余量），
// 关系由 ingest 包的编译期断言钉住。上下级一致性本身不依赖这个取值，只依赖
// 写协程拒绝水位之前的写入；滞后的作用是让那条拒绝在正常运行时不触发。
const RollupLag = 300 * time.Second

type Level struct {
	Name string
	// Bucket 是桶长，秒。
	Bucket int64
}

// levels 从细到粗；上卷按此顺序进行，粗一级只能用细一级已冻结的行。
var levels = []Level{
	{Name: "1m", Bucket: 60},
	{Name: "5m", Bucket: 300},
	{Name: "1h", Bucket: 3600},
}

func LevelByName(name string) (Level, bool) {
	for _, lv := range levels {
		if lv.Name == name {
			return lv, true
		}
	}
	return Level{}, false
}

func alignDown(ts, bucket int64) int64 { return ts - ts%bucket }

// aggregates 是"把若干行折叠成一行"的 SELECT 列表：和、样本数求和，最大值取最大。
// 上卷与查询时的二次分桶是同一种运算，所以共用。
func aggregates() []string {
	var out []string
	for _, c := range metric.Columns {
		out = append(out, "sum("+c.Name+"_sum)", "sum("+c.Name+"_n)")
		if c.Kind == metric.MeanMax {
			out = append(out, "max("+c.Name+"_max)")
		}
	}
	return out
}

// family 描述一个时间序列表族：三级表、各级水位在 rollup_state 里的键、键列与值列的
// SQL 片段。两族共用上卷与清理的控制流程，查询共用二次分桶 SQL；
// 表名、水位键和聚合列来自同一个描述，避免各操作把两族混用。
type family struct {
	name string
	// tables / states 与 levels 同序；states[0] 为空——最细一级没有水位。
	tables []string
	states []string
	// extraKey 是 node_id、ts 之外的键列（探测族为 task_id），为空则没有。
	extraKey string
	values   func() []string
	aggs     func() []string
}

var metricFamily = &family{name: "metric", tables: metricTables, states: []string{"", "5m", "1h"},
	values: metricColumnNames, aggs: aggregates}

var probeFamily = &family{name: "probe", tables: probeTables, states: []string{"", "probe_5m", "probe_1h"},
	extraKey: "task_id", values: probeValueColumns, aggs: probeAggregates}

// families 的顺序无关：两族各有水位，互不牵制（§6.4）。
var families = []*family{metricFamily, probeFamily}

func (f *family) keys() []string {
	k := []string{"node_id", "ts"}
	if f.extraKey != "" {
		k = append(k, f.extraKey)
	}
	return k
}

// groupBy 是"整桶重算"的分组键：ts 对齐到桶长，其余键原样。
func (f *family) groupBy(bucket string) string {
	g := "node_id, ts - ts % " + bucket
	if f.extraKey != "" {
		g += ", " + f.extraKey
	}
	return g
}

func (f *family) rollupSQL(i int) string {
	b := fmt.Sprint(levels[i].Bucket)
	return "INSERT OR REPLACE INTO " + f.tables[i] + " (" + strings.Join(append(f.keys(), f.values()...), ", ") + ") " +
		"SELECT " + f.groupBy(b) + ", " + strings.Join(f.aggs(), ", ") +
		" FROM " + f.tables[i-1] + " WHERE node_id IN (SELECT id FROM node) AND ts >= ? AND ts < ? GROUP BY " + f.groupBy(b)
}

// Rollup 对每一粗级：取水位之后、滞后期已过的下级桶，整桶重算写入本级，并在
// 同一事务推进水位。levels 必须从细到粗：lowerUpto 取自刚完成的下级，
// 按本级桶长对齐后限制上界，只消费下级已冻结的整桶。当前正常推进路径中，
// 此上界与 ceiling 的对齐上界相同；min 显式保留两项约束，不替代处理顺序。
func (s *Store) Rollup(ctx context.Context) error {
	ceiling := s.clk.Now().Add(-RollupLag).Unix()
	for _, f := range families {
		lowerUpto := int64(math.MaxInt64)
		for i := 1; i < len(levels); i++ {
			lv := levels[i]
			limit := min(alignDown(ceiling, lv.Bucket), alignDown(lowerUpto, lv.Bucket))
			upto, err := s.rollupLevel(ctx, f, i, limit)
			if err != nil {
				return fmt.Errorf("rollup %s %s: %w", f.name, lv.Name, err)
			}
			lowerUpto = upto
		}
	}
	return nil
}

// rollupSlice 限制每次占用写协程的历史跨度，让其他写请求能在追赶的片间提交。
var rollupSlice = map[string]int64{"5m": 86400, "1h": 7 * 86400}

// rollupLevel 每片通过同一次 write 事务插入桶并推进水位，二者一起提交或回滚。
// 每片重读水位，允许其他维护调用在片间推进；空白历史在同一事务中确认后跳过，
// 避免新库从零水位逐日提交空事务。非空片的起止均对齐目标桶，不拆开整桶。
func (s *Store) rollupLevel(ctx context.Context, f *family, i int, limit int64) (int64, error) {
	lv := levels[i]
	var upto int64
	for {
		err := s.write(ctx, func(tx *sql.Tx) error {
			if err := tx.QueryRow("SELECT upto_ts FROM rollup_state WHERE level = ?", f.states[i]).Scan(&upto); err != nil {
				return err
			}
			if limit <= upto {
				return nil
			}
			var first sql.NullInt64
			// 每个节点从复合主键的时间范围取首行，再求全局最小值，不扫描全部历史。
			if err := tx.QueryRow("SELECT min((SELECT ts FROM "+f.tables[i-1]+
				" WHERE node_id = node.id AND ts >= ? AND ts < ? ORDER BY ts LIMIT 1)) FROM node", upto, limit).Scan(&first); err != nil {
				return err
			}
			end := limit
			if first.Valid {
				start := max(upto, alignDown(first.Int64, lv.Bucket))
				end = start + min(rollupSlice[lv.Name], limit-start)
				if _, err := tx.Exec(f.rollupSQL(i), start, end); err != nil {
					return err
				}
			}
			res, err := tx.Exec("UPDATE rollup_state SET upto_ts = ? WHERE level = ?", end, f.states[i])
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n != 1 {
				return fmt.Errorf("rollup_state has no row for level %s", f.states[i])
			}
			upto = end
			return nil
		})
		if err != nil || upto >= limit {
			return upto, err
		}
	}
}

type Retention struct {
	M1, M5, H1 time.Duration
}

var DefaultRetention = Retention{M1: 7 * 24 * time.Hour, M5: 30 * 24 * time.Hour, H1: 365 * 24 * time.Hour}

// 保留期准入与命令帮助共用下限，避免显示可用的配置在实际启动时被拒绝。
const (
	MinRetentionM1 = 6 * time.Hour
	MinRetentionM5 = 7 * 24 * time.Hour
	MinRetentionH1 = 7 * 24 * time.Hour
)

// Validate 的下限与 ChooseLevel 的选级阈值同向：1m 覆盖六小时、5m 覆盖七天，
// 且粗级不短于细级，避免刚跨选级边界就因保留期更短而失去历史。
func (r Retention) Validate() error {
	if r.M1 < MinRetentionM1 {
		return fmt.Errorf("retention for 1m level is %v, minimum is %v", r.M1, MinRetentionM1)
	}
	if r.M5 < MinRetentionM5 {
		return fmt.Errorf("retention for 5m level is %v, minimum is %v", r.M5, MinRetentionM5)
	}
	if r.H1 < MinRetentionH1 {
		return fmt.Errorf("retention for 1h level is %v, minimum is %v", r.H1, MinRetentionH1)
	}
	if r.M5 < r.M1 || r.H1 < r.M5 {
		return errors.New("retention must not shrink as the level gets coarser (1m <= 5m <= 1h)")
	}
	return nil
}

func (r Retention) forLevel(name string) time.Duration {
	switch name {
	case "5m":
		return r.M5
	case "1h":
		return r.H1
	}
	return r.M1
}

// pruneSlice 限制每块删除覆盖的时间片（秒）；deleteRange 每次只处理一个节点，
// 不把该节点的全部过期历史合并成一个写事务。
var pruneSlice = map[string]int64{"1m": 86400, "5m": 7 * 86400, "1h": 30 * 86400}

// Prune 删除各级保留期之外的行。每块一个短事务，按节点、按时间片：
// WHERE node_id = ? AND ts >= ? AND ts < ? 走主键。节点集合取自表本身而不是
// node 表：已删节点若留有孤儿行，也要随保留期消失。
func (s *Store) Prune(ctx context.Context, r Retention) (int64, error) {
	now := s.clk.Now().Unix()
	var total int64
	for _, f := range families {
		for i, lv := range levels {
			cutoff := alignDown(now-int64(r.forLevel(lv.Name)/time.Second), lv.Bucket)
			// 初始化后只有 rollupLevel 写水位，每片 end > upto，且与聚合原子提交。
			// 因此消费水位只前进，读到旧值至多延迟清理，不会提前删掉未聚合的行。
			if i+1 < len(levels) {
				var consumed int64
				if err := s.r.QueryRowContext(ctx, "SELECT upto_ts FROM rollup_state WHERE level = ?", f.states[i+1]).Scan(&consumed); err != nil {
					return total, err
				}
				cutoff = min(cutoff, consumed)
			}
			ids, err := s.distinctNodes(ctx, f.tables[i])
			if err != nil {
				return total, err
			}
			for _, id := range ids {
				for {
					var oldest sql.NullInt64
					if err := s.r.QueryRowContext(ctx, "SELECT min(ts) FROM "+f.tables[i]+" WHERE node_id = ?", id).Scan(&oldest); err != nil {
						return total, err
					}
					if !oldest.Valid || oldest.Int64 >= cutoff {
						break
					}
					end := min(oldest.Int64+pruneSlice[lv.Name], cutoff)
					n, err := s.deleteRange(ctx, f.tables[i], id, oldest.Int64, end)
					if err != nil {
						return total, err
					}
					total += n
				}
			}
		}
	}
	return total, nil
}

func (s *Store) distinctNodes(ctx context.Context, table string) ([]int64, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT DISTINCT node_id FROM "+table)
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

func (s *Store) deleteRange(ctx context.Context, table string, nodeID, from, to int64) (int64, error) {
	var n int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec("DELETE FROM "+table+" WHERE node_id = ? AND ts >= ? AND ts < ?", nodeID, from, to)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}

func ceilDiv(n, d int64) int64 {
	q := n / d
	if n%d > 0 {
		q++
	}
	return q
}

// ChooseLevel 按窗口跨度选级，步长保持为桶长的整数倍。点数预算按
// QueryMetrics 对齐后的窗口计算，不能只限制原始跨度，以免首尾扩展后超限。
func ChooseLevel(from, to int64, maxPoints int) (Level, int64) {
	span := to - from
	lv := levels[0]
	switch {
	case span <= 6*3600:
	case span <= 7*86400:
		lv = levels[1]
	default:
		lv = levels[2]
	}
	step := lv.Bucket
	if maxPoints > 0 {
		budget := int64(maxPoints)
		step = max(step, ceilDiv(ceilDiv(span, budget), lv.Bucket)*lv.Bucket)
		for ceilDiv(to, step)-from/step > budget {
			// k 是当前首桶编号。增大步长后首桶编号只会减小，所以任何可行的
			// 下一步长至少是 ceil(to/(k+budget))；跳过更小值不会丢掉可行解。
			need := ceilDiv(to, from/step+budget)
			step = ceilDiv(need, lv.Bucket) * lv.Bucket
		}
	}
	return lv, step
}

// aggregateSQL 是查询时的二次分桶：与上卷同一种运算，步长作为绑定参数。
// 探测族多按 task_id 分组并先按任务再按时间排序，调用方据此切成每任务一条序列。
func (f *family) aggregateSQL(table string) string {
	sel, group, order := "ts - ts % ?", "1", "1"
	if f.extraKey != "" {
		sel += ", " + f.extraKey
		group, order = "1, 2", "2, 1"
	}
	return "SELECT " + sel + ", " + strings.Join(f.aggs(), ", ") + " FROM " + table +
		" WHERE node_id = ? AND ts >= ? AND ts <= ? GROUP BY " + group + " ORDER BY " + order
}

// QueryMetrics 返回 [from, to) 内按 step 聚合的桶；from 向下、to 向上对齐到 step，
// 结果的 TS 都是 step 的整数倍。只返回有行的桶：缺失的桶就是没有数据。
func (s *Store) QueryMetrics(ctx context.Context, nodeID int64, from, to int64, lv Level, step int64) ([]metric.Row, error) {
	if err := checkStep(lv, step); err != nil {
		return nil, err
	}
	from, to = alignWindow(from, to, step)
	rows, err := s.r.QueryContext(ctx, metricFamily.aggregateSQL(metricFamily.tables[levelIndex(lv)]), step, nodeID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBucketRows(rows, nodeID)
}

// nextMaintenanceAt 落在分钟边界后 2 秒：分钟刷出在 +0.5s，两者的顺序其实不
// 重要——上卷只碰至少 RollupLag 之前闭合的桶——错开只是避免同时争写协程。
func nextMaintenanceAt(wall time.Time) time.Time {
	return wall.Truncate(time.Minute).Add(time.Minute + 2*time.Second)
}

// RunMaintenance 按分钟边界调度上卷与清理；一轮维护使用 Background，
// 因而取消只在等待下一轮时生效，已开始的一轮会完成后再退出。
func (s *Store) RunMaintenance(ctx context.Context, r Retention) {
	for {
		wall := s.clk.Now()
		timer := time.NewTimer(nextMaintenanceAt(wall).Sub(wall))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			if err := s.Rollup(context.Background()); err != nil {
				s.log.Error("rollup failed", "err", err)
				// 上卷失败时保留本轮全部历史；Prune 自身的消费水位守卫仍独立生效。
				continue
			}
			if n, err := s.Prune(context.Background(), r); err != nil {
				s.log.Error("prune failed", "err", err)
			} else if n > 0 {
				s.log.Info("pruned expired rows", "rows", n)
			}
		}
	}
}

func levelIndex(lv Level) int {
	for i, level := range levels {
		if level.Name == lv.Name {
			return i
		}
	}
	return -1
}

func checkStep(lv Level, step int64) error {
	i := levelIndex(lv)
	if i < 0 || lv.Bucket != levels[i].Bucket {
		return fmt.Errorf("unknown level %q with bucket %d", lv.Name, lv.Bucket)
	}
	if step < lv.Bucket || step%lv.Bucket != 0 {
		return fmt.Errorf("step %d is not a multiple of the %s bucket (%d)", step, lv.Name, lv.Bucket)
	}
	return nil
}

func alignWindow(from, to, step int64) (int64, int64) {
	from = alignDown(from, step)
	// 用最后一秒所在桶的闭区间上界表达对齐，避免 to + step 溢出。
	last := alignDown(to-1, step)
	to = math.MaxInt64
	if last <= math.MaxInt64-(step-1) {
		to = last + step - 1
	}

	return from, to
}

// QueryProbes 与 QueryMetrics 同一套选级与对齐；每任务的桶按 TaskID、TS 升序返回。
func (s *Store) QueryProbes(ctx context.Context, nodeID int64, from, to int64, lv Level, step int64) ([]metric.ProbeRow, error) {
	if err := checkStep(lv, step); err != nil {
		return nil, err
	}
	from, to = alignWindow(from, to, step)
	rows, err := s.r.QueryContext(ctx, probeFamily.aggregateSQL(probeFamily.tables[levelIndex(lv)]), step, nodeID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanProbeRows(rows, nodeID)
}
