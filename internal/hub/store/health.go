package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// maintenance_state 的 name 取值：维护循环里两类会停摆而不自知的任务各一行。
const (
	MaintenancePrune  = "prune"
	MaintenanceRollup = "rollup"
)

// recordMaintenance 记下 name 这一轮整轮成功完成的时刻，取写协程执行时的时钟。调用方只在整轮成功之后调用
// （Rollup、Prune 的末尾）：中途失败若也写，读侧看到的"刚完成"会掩盖此后每一轮都失败的事实，簿记就失去了
// 区分"一直失败"与"正常"的唯一用途。
func (s *Store) recordMaintenance(ctx context.Context, name string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO maintenance_state (name, finished_at) VALUES (?, ?)
			ON CONFLICT (name) DO UPDATE SET finished_at = excluded.finished_at`, name, s.clk.Now().Unix())
		return err
	})
}

// SeriesHealth 是一张时序表的健康读数，全是库里的原值，不做换算。
type SeriesHealth struct {
	Table string
	Level Level
	// Oldest 是表里最老一行的 ts（桶起点，Unix 秒）；表为空时为 nil。
	Oldest *int64
	// Watermark 是本级在 rollup_state 里的 upto_ts 原值，与上卷读写的是同一个值；最细一级不经上卷写入、
	// 没有水位，为 nil。
	Watermark *int64
}

// Staleness 是一张表的两项标红结论。
type Staleness struct {
	Oldest, Watermark bool
}

// Staleness 是存储健康的唯一判定：API 响应带上它的结论，面板只渲染、不重算，阈值因此只有这一份，不会在
// 前端与 hub 之间各改各的。保留期取调用方传入的当前配置，与 Prune 用的是同一个 Retention。
//
//   - 最老桶早于 now − 保留期 − 一个桶长即标红。Prune 的截止点是 now − 保留期向下对齐到桶长，所以正常运行时
//     最老桶可以比 now − 保留期早不到一个桶长；再往前说明超期的行没有被清掉：prune 停了，或者上卷停了
//     （Prune 对细一级只清到粗一级的水位为止）。维护每分钟一轮，截止点越过桶边界之后、
//     下一轮 prune 删完之前，健康的表也会短暂越过阈值（至多约一个维护间隔加一轮维护的耗时），偶发一次不代表故障。
//   - 水位落后 now 超过三个桶长即标红。上卷只推进到 now − RollupLag 向下对齐到桶长，再加至多一个维护间隔，
//     健康的落后量 5m 级不到 300 + 300 + 60 秒、1h 级不到 300 + 3600 + 60 秒，都在三个桶长之内。
//
// 两项都是严格大于阈值才标红：恰好等于阈值不标。
func (h SeriesHealth) Staleness(now time.Time, r Retention) Staleness {
	n, bucket := now.Unix(), h.Level.Bucket
	var out Staleness
	if h.Oldest != nil {
		out.Oldest = *h.Oldest < n-int64(r.ForLevel(h.Level.Name)/time.Second)-bucket
	}
	if h.Watermark != nil {
		out.Watermark = n-*h.Watermark > 3*bucket
	}
	return out
}

// seriesHealth 在调用方的只读事务里按 families × levels 的顺序读出每张时序表的最老桶与水位：按表分别给、
// 不合并，因为各级保留期不同，合并后的最老值无法与任何一级的保留期对比。
func seriesHealth(ctx context.Context, tx *sql.Tx) ([]SeriesHealth, error) {
	var out []SeriesHealth
	for _, f := range families {
		for i, lv := range levels {
			h := SeriesHealth{Table: f.tables[i], Level: lv}
			var oldest sql.NullInt64
			if err := tx.QueryRowContext(ctx, "SELECT min(ts) FROM "+f.tables[i]).Scan(&oldest); err != nil {
				return nil, err
			}
			if oldest.Valid {
				h.Oldest = &oldest.Int64
			}
			if f.states[i] != "" {
				var upto int64
				if err := tx.QueryRowContext(ctx, "SELECT upto_ts FROM rollup_state WHERE level = ?", f.states[i]).Scan(&upto); err != nil {
					return nil, fmt.Errorf("rollup_state %s: %w", f.states[i], err)
				}
				h.Watermark = &upto
			}
			out = append(out, h)
		}
	}
	return out, nil
}

// lastMaintenance 读出各维护任务最近一次整轮成功的完成时刻；没有行的任务不在映射里（从未成功跑过）。
func lastMaintenance(ctx context.Context, tx *sql.Tx) (map[string]int64, error) {
	rows, err := tx.QueryContext(ctx, "SELECT name, finished_at FROM maintenance_state")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var name string
		var at int64
		if err := rows.Scan(&name, &at); err != nil {
			return nil, err
		}
		out[name] = at
	}
	return out, rows.Err()
}
