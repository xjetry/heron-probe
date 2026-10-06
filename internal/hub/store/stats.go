package store

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"strings"
	"unicode/utf8"
)

type TableRows struct {
	Name string
	Rows int64
}

// StorageStats 是库的规模与健康读数，GetStorageStats 与 heron-hub stats 都从它取：DBBytes 为
// page_count × page_size，即数据库的逻辑大小，等于 WAL 检查点之后主文件的大小（检查点之前主文件可能远小于它）；
// 不含 -wal 与 -shm 文件。Tables 按表名升序。Series 按 metric_1m、5m、1h、probe_1m、5m、1h 的固定顺序。
// LastPrune、LastRollup 是 maintenance_state 里的完成时刻（Unix 秒），nil 即从未整轮成功过。
type StorageStats struct {
	DBBytes    int64
	Tables     []TableRows
	Series     []SeriesHealth
	LastPrune  *int64
	LastRollup *int64
	WAL        WALObservation
}

// WALObservation 是库旁 -wal 的一次 stat 结果，不含 -shm。Bytes 非 nil 表示存在（包括零字节），
// Absent 表示无文件，Error 表示未知；三者只取一个。ObservedAt 为 stat 完成时的 hub 墙钟 Unix 秒。
type WALObservation struct {
	ObservedAt int64
	Bytes      *int64
	Absent     bool
	Error      string
}

type fileStatter interface {
	Stat(string) (fs.FileInfo, error)
}

type osFileStatter struct{}

func (osFileStatter) Stat(path string) (fs.FileInfo, error) { return os.Stat(path) }

func (s *Store) observeWAL() WALObservation {
	info, err := s.files.Stat(s.path + "-wal")
	out := WALObservation{ObservedAt: s.clk.Now().Unix()}
	switch {
	case err == nil:
		size := info.Size()
		out.Bytes = &size
	case errors.Is(err, fs.ErrNotExist):
		out.Absent = true
	default:
		// 文件名可含非法 UTF-8；proto string 必须有效，且错误不能使整个统计响应无界增长。
		message := strings.ToValidUTF8(err.Error(), "\uFFFD")
		const maxBytes = 512
		if len(message) > maxBytes {
			end := maxBytes
			for !utf8.RuneStart(message[end]) {
				end--
			}
			message = message[:end]
		}
		if message == "" {
			message = "stat failed"
		}
		out.Error = message
	}
	return out
}

// StorageStats 的 SQL 读数在一个只读事务里读出，行数、逻辑大小与维护健康属于同一快照；
// WAL 在事务结束后独立 stat，不承诺与 SQL 同一时刻。表名取自 sqlite_master 而不是手写清单：
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
	if err := tx.Commit(); err != nil {
		return StorageStats{}, err
	}
	out.WAL = s.observeWAL()
	return out, nil
}
