package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

type RestoreResult struct {
	RestoredAt     int64            `json:"restored_at"`
	ConfigTakenAt  int64            `json:"config_taken_at"`
	MetricsTakenAt *int64           `json:"metrics_taken_at"`
	Orphans        map[string]int64 `json:"orphans"`
}

// Restore 只供停止 hub 后的离线入口使用：直接覆写库不会同步运行中 hub 的设置缓存与鉴权索引。
// 不探测 WAL 写者来声称已停机；是否已停 hub 由命令行的显式确认承担。
func Restore(ctx context.Context, path, config, metrics string, now time.Time) (result RestoreResult, err error) {
	if config == "" {
		return result, errors.New("config snapshot is required")
	}
	db, err := sql.Open("sqlite", dsn(path, ""))
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	db.SetMaxOpenConns(1)
	type source struct {
		layer, path string
		tables      []string
	}
	sources := []source{{"config", config, configSnapshotTables}}
	if metrics != "" {
		sources = append(sources, source{"metrics", metrics, metricsSnapshotTables})
	}
	for _, src := range sources {
		if _, err = db.ExecContext(ctx, "ATTACH DATABASE ? AS "+src.layer, dsn(src.path, "&mode=ro")); err != nil {
			return result, fmt.Errorf("attach %s snapshot: %w", src.layer, err)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var pageSize int
	if err = tx.QueryRowContext(ctx, "PRAGMA main.page_size").Scan(&pageSize); err != nil {
		return result, err
	}
	// 校验和读取在同一事务中固定每份来源；检查失败前不建表、不迁移或改写目标数据。
	for _, src := range sources {
		at, e := validateSnapshot(ctx, tx, src.layer, src.tables, pageSize)
		if e != nil {
			return result, e
		}
		if src.layer == "config" {
			result.ConfigTakenAt = at
		} else {
			result.MetricsTakenAt = &at
		}
	}
	if err = prepareRestoreTarget(ctx, tx); err != nil {
		return result, err
	}
	for _, src := range sources {
		for _, table := range src.tables {
			for _, query := range []string{
				"DELETE FROM main." + table,
				"INSERT INTO main." + table + " SELECT * FROM " + src.layer + "." + table,
			} {
				if _, err = tx.ExecContext(ctx, query); err != nil {
					return result, fmt.Errorf("restore %s.%s: %w", src.layer, table, err)
				}
			}
		}
	}
	sequenceSources := []string{"main", "config"}
	if metrics != "" {
		sequenceSources = append(sequenceSources, "metrics")
	}
	if err = restoreSequences(ctx, tx, sequenceSources); err != nil {
		return result, err
	}
	// node 来自配置快照，始终由它决定哪些节点存在，与两层时刻的先后无关。
	// schema 没有级联外键，整表替换也不能表达跨层清理；逐表显式删除并记录数量。
	// nodeDependentTables 同时约束 DeleteNode，审计历史的排除口径不在恢复侧另列。
	result.Orphans = make(map[string]int64)
	for _, table := range nodeDependentTables {
		res, e := tx.ExecContext(ctx, "DELETE FROM main."+table+" WHERE NOT EXISTS (SELECT 1 FROM main.node WHERE id = "+table+".node_id)")
		if e != nil {
			return result, e
		}
		count, e := res.RowsAffected()
		if e != nil {
			return result, e
		}
		result.Orphans[table] = count
	}
	result.RestoredAt = now.Unix()
	orphans, err := json.Marshal(result.Orphans)
	if err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO main.restore_record (restored_at,config_taken_at,metrics_taken_at,orphans) VALUES (?,?,?,?)",
		result.RestoredAt, result.ConfigTakenAt, result.MetricsTakenAt, string(orphans)); err != nil {
		return result, err
	}
	err = tx.Commit()
	return result, err
}

func validateSnapshot(ctx context.Context, tx *sql.Tx, layer string, tables []string, pageSize int) (int64, error) {
	for _, table := range append(slices.Clone(tables), "snapshot_meta", "sqlite_sequence") {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+layer+".sqlite_schema WHERE type='table' AND name=?", table).Scan(&count); err != nil {
			return 0, err
		}
		if count != 1 {
			return 0, fmt.Errorf("%s snapshot missing table %s", layer, table)
		}
	}
	var count, version, size int
	var at int64
	var gotLayer string
	if err := tx.QueryRowContext(ctx, "SELECT count(*),max(schema_version),max(taken_at),max(layer) FROM "+layer+".snapshot_meta").Scan(&count, &version, &at, &gotLayer); err != nil {
		return 0, fmt.Errorf("%s snapshot metadata: %w", layer, err)
	}
	if count != 1 || version != schemaVersion || gotLayer != layer {
		return 0, fmt.Errorf("%s snapshot metadata: rows=%d schema_version=%d layer=%q; want one row, schema_version=%d layer=%q", layer, count, version, gotLayer, schemaVersion, layer)
	}
	if err := tx.QueryRowContext(ctx, "PRAGMA "+layer+".page_size").Scan(&size); err != nil {
		return 0, err
	}
	if size != pageSize {
		return 0, fmt.Errorf("%s snapshot page_size=%d; target page_size=%d", layer, size, pageSize)
	}
	return at, nil
}

func prepareRestoreTarget(ctx context.Context, tx *sql.Tx) error {
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA main.user_version").Scan(&version); err != nil {
		return err
	}
	if version == schemaVersion {
		return nil
	}
	if version != 0 {
		return fmt.Errorf("target schema version %d differs from current %d; upgrade older databases with probe-hub serve first (back up the database first)", version, schemaVersion)
	}
	var objects int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM main.sqlite_schema").Scan(&objects); err != nil {
		return err
	}
	if objects != 0 {
		return errors.New("target database has objects but no schema version; not a probe database")
	}
	for _, stmt := range schemaStatements() {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA main.user_version = %d", schemaVersion))
	return err
}

func restoreSequences(ctx context.Context, tx *sql.Tx, sources []string) error {
	// DELETE 数据行不降低 main.sqlite_sequence；因此此时 main 仍承载恢复前的高水位。
	// 三方按表名取最大，既不复用目标库的旧 ID，也不复用任一快照已删掉的 ID。
	var queries []string
	for _, source := range sources {
		queries = append(queries, "SELECT name,seq FROM "+source+".sqlite_sequence")
	}
	rows, err := tx.QueryContext(ctx, "SELECT name,max(seq) FROM ("+strings.Join(queries, " UNION ALL ")+") GROUP BY name")
	if err != nil {
		return err
	}
	seqs := make(map[string]int64)
	for rows.Next() {
		var name string
		var seq int64
		if err := rows.Scan(&name, &seq); err != nil {
			rows.Close()
			return err
		}
		seqs[name] = seq
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM main.sqlite_sequence"); err != nil {
		return err
	}
	for name, seq := range seqs {
		if _, err := tx.ExecContext(ctx, "INSERT INTO main.sqlite_sequence (name,seq) VALUES (?,?)", name, seq); err != nil {
			return err
		}
	}
	return nil
}
