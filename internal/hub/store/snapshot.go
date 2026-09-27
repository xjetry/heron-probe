package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
)

// 清单显式列出每张表，不能用名称前缀推断层：probe_task 是配置而 probe_1m 是历史。
// 新表的归属由分类完备性测试约束，不能在快照时静默跳过不存在的表。
var configSnapshotTables = []string{
	"node", "node_facts", "traffic", "probe_task", "probe_task_node", "probe_meta",
	"alert_rule", "alert_rule_node", "alert_rule_channel", "alert_state", "alert_event", "alert_delivery",
	"notify_channel", "setting", "admin", "api_token", "sqlite_sequence",
}

var metricsSnapshotTables = []string{
	"metric_1m", "metric_5m", "metric_1h", "probe_1m", "probe_5m", "probe_1h",
	"rollup_state", "maintenance_state",
}

func (s *Store) SnapshotConfig(ctx context.Context, path string) error {
	return s.snapshot(ctx, path, "config", configSnapshotTables)
}

func (s *Store) SnapshotMetrics(ctx context.Context, path string) error {
	return s.snapshot(ctx, path, "metrics", metricsSnapshotTables)
}

func (s *Store) snapshot(ctx context.Context, path, layer string, tables []string) (result error) {
	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed {
		return ErrClosed
	}
	// 快照含凭据；独占创建既限制权限，也拒绝覆盖已有文件或经符号链接改写别的库。
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if result != nil {
			result = errors.Join(result, os.Remove(path))
		}
	}()
	if err := f.Close(); err != nil {
		return err
	}
	// mode=ro 只保护源库；query_only 会连 ATTACH 的目标库也禁止写入。
	// 独立连接不占用主库写队列，且关闭时释放 ATTACH，不把连接状态带回共享读池。
	db, err := sql.Open("sqlite", dsn(s.path, "&mode=ro"))
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, db.Close()) }()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "ATTACH DATABASE ? AS snap", path); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 版本读取先固定源库快照；后续每张表都在同一个事务读，节点与规则不能来自不同提交。
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA main.user_version").Scan(&version); err != nil {
		return err
	}
	at := s.clk.Now().Unix()
	for _, table := range tables {
		query := "CREATE TABLE snap." + table + " AS SELECT * FROM main." + table
		if table == "sqlite_sequence" {
			// CREATE TABLE AS 不保留 AUTOINCREMENT；先让 SQLite 建出内部序列表，
			// 再搬源库高水位，恢复后才不会复用已删节点的 id。
			if _, err := tx.ExecContext(ctx, "CREATE TABLE snap.sequence_seed (id INTEGER PRIMARY KEY AUTOINCREMENT)"); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "DROP TABLE snap.sequence_seed"); err != nil {
				return err
			}
			query = "INSERT INTO snap.sqlite_sequence SELECT * FROM main.sqlite_sequence"
		}
		if _, err := tx.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("snapshot %s: %w", table, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "CREATE TABLE snap.snapshot_meta (schema_version INTEGER NOT NULL, taken_at INTEGER NOT NULL, layer TEXT NOT NULL)"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO snap.snapshot_meta VALUES (?, ?, ?)", version, at, layer); err != nil {
		return err
	}
	return tx.Commit()
}
