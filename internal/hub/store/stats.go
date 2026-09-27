package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

type TableRows struct {
	Name string
	Rows int64
}

// StorageStats 是库的规模与健康读数，GetStorageStats 与 probe-hub stats 都从它取：DBBytes 为
// page_count × page_size，即数据库的逻辑大小，等于 WAL 检查点之后主文件的大小（检查点之前主文件可能远小于它）；
// 不含 -wal 与 -shm 文件。Tables 按表名升序。Series 按 metric_1m、5m、1h、probe_1m、5m、1h 的固定顺序。
// LastPrune、LastRollup 是 maintenance_state 里的完成时刻（Unix 秒），nil 即从未整轮成功过。
type StorageStats struct {
	DBBytes    int64
	Tables     []TableRows
	Series     []SeriesHealth
	LastPrune  *int64
	LastRollup *int64
}

// StorageStats 在一个只读事务里读出，行数、大小与健康读数属于同一快照。表名取自 sqlite_master 而不是手写清单：
// 新增的表自动计入。名字以 sqlite_ 开头的是 SQLite 内部表（如 AUTOINCREMENT 的 sqlite_sequence），不计；
// 前缀按字面比较，不用 LIKE（它的 _ 是通配符，且对 ASCII 不分大小写）。
func (s *Store) StorageStats(ctx context.Context) (StorageStats, error) {
	tx, err := s.r.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return StorageStats{}, err
	}
	defer tx.Rollback()
	var names []string
	rows, err := tx.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND substr(name, 1, 7) <> 'sqlite_' ORDER BY name")
	if err != nil {
		return StorageStats{}, err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return StorageStats{}, err
		}
		names = append(names, n)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return StorageStats{}, err
	}
	var out StorageStats
	for _, n := range names {
		var c int64
		// 表名来自 sqlite_master，按 SQL 标识符规则加双引号并转义内部的双引号。
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+strings.ReplaceAll(n, `"`, `""`)+`"`).Scan(&c); err != nil {
			return StorageStats{}, err
		}
		out.Tables = append(out.Tables, TableRows{Name: n, Rows: c})
	}
	var pages, pageSize int64
	if err := tx.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		return StorageStats{}, err
	}
	if err := tx.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return StorageStats{}, err
	}
	out.DBBytes = pages * pageSize
	if out.Series, err = seriesHealth(ctx, tx); err != nil {
		return StorageStats{}, err
	}
	last, err := lastMaintenance(ctx, tx)
	if err != nil {
		return StorageStats{}, err
	}
	if at, ok := last[MaintenancePrune]; ok {
		out.LastPrune = &at
	}
	if at, ok := last[MaintenanceRollup]; ok {
		out.LastRollup = &at
	}
	return out, nil
}
