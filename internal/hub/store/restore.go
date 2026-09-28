package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
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
func Restore(ctx context.Context, path, config, metrics string, now time.Time, log *slog.Logger) (result RestoreResult, err error) {
	if config == "" {
		return result, errors.New("config snapshot is required")
	}
	sources := []restoreSource{{"config", config, configSnapshotTables}}
	if metrics != "" {
		sources = append(sources, restoreSource{"metrics", metrics, metricsSnapshotTables})
	}
	if err := preflightRestoreSources(ctx, path, sources); err != nil {
		return result, err
	}
	db, err := sql.Open("sqlite", dsn(path, ""))
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	db.SetMaxOpenConns(1)
	if err = attachRestoreSources(ctx, db, sources); err != nil {
		return result, err
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
	// 预检不打开目标；这里再次校验并在恢复事务内固定来源，避免预检后来源被替换而绕过检查。
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
	action, _, err := schemaAdmission(ctx, tx, RequireCurrentSchema)
	if err != nil {
		return result, fmt.Errorf("open database %s: %w", path, err)
	}
	if action == schemaCreate {
		if err = createSchema(ctx, tx); err != nil {
			return result, err
		}
	}
	for _, src := range sources {
		for _, table := range src.tables {
			columns, e := restoreColumnList(ctx, tx, table)
			if e != nil {
				return result, e
			}
			insert := "INSERT INTO "
			if table == "restore_record" {
				// 审计事件以创建时的 id 标识，回退配置不能删除目标已有的历史，重复快照也不重复记账。
				insert = "INSERT OR IGNORE INTO "
			} else {
				if _, err = tx.ExecContext(ctx, "DELETE FROM main."+table); err != nil {
					return result, fmt.Errorf("restore %s.%s: %w", src.layer, table, err)
				}
			}
			if _, err = tx.ExecContext(ctx, insert+"main."+table+" ("+columns+") SELECT "+columns+" FROM "+src.layer+"."+table); err != nil {
				return result, fmt.Errorf("restore %s.%s: %w", src.layer, table, err)
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
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO main.restore_record (id,restored_at,config_taken_at,metrics_taken_at,orphans) VALUES (?,?,?,?,?)",
		hex.EncodeToString(id[:]), result.RestoredAt, result.ConfigTakenAt, result.MetricsTakenAt, string(orphans)); err != nil {
		return result, err
	}
	err = tx.Commit()
	if err == nil && action == schemaCreate {
		logSchemaCreated(log)
	}
	return result, err
}

type restoreSource struct {
	layer, path string
	tables      []string
}

func attachRestoreSources(ctx context.Context, db *sql.DB, sources []restoreSource) error {
	for _, src := range sources {
		if _, err := db.ExecContext(ctx, "ATTACH DATABASE ? AS "+src.layer, dsn(src.path, "&mode=ro")); err != nil {
			return fmt.Errorf("attach %s snapshot: %w", src.layer, err)
		}
	}
	return nil
}

func preflightRestoreSources(ctx context.Context, target string, sources []restoreSource) (err error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	db.SetMaxOpenConns(1)
	if err := attachRestoreSources(ctx, db, sources); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var pageSize int
	pageSchema := "config"
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		// 空的 main 库给出 SQLite 默认页大小，新目标采用这个值；创建文件之前就拒绝不匹配的来源。
		pageSchema = "main"
	} else if err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, "PRAGMA "+pageSchema+".page_size").Scan(&pageSize); err != nil {
		return err
	}
	for _, src := range sources {
		if _, err := validateSnapshot(ctx, tx, src.layer, src.tables, pageSize); err != nil {
			return err
		}
	}
	return nil
}

func restoreColumnList(ctx context.Context, tx *sql.Tx, table string) (string, error) {
	// 目标 schema 决定要恢复的列，INSERT 与 SELECT 共用列名，不依赖快照中列的物理顺序。
	rows, err := tx.QueryContext(ctx, "SELECT name FROM pragma_table_info(?, 'main') ORDER BY cid", table)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return "", err
		}
		columns = append(columns, `"`+strings.ReplaceAll(name, `"`, `""`)+`"`)
	}
	return strings.Join(columns, ","), rows.Err()
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
	var count, size int
	var version, at sql.NullInt64
	var gotLayer sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT count(*),max(schema_version),max(taken_at),max(layer) FROM "+layer+".snapshot_meta").Scan(&count, &version, &at, &gotLayer); err != nil {
		return 0, fmt.Errorf("%s snapshot metadata: %w", layer, err)
	}
	if count != 1 || version.Int64 != schemaVersion || gotLayer.String != layer {
		return 0, fmt.Errorf("%s snapshot metadata: rows=%d schema_version=%d layer=%q; want one row, schema_version=%d layer=%q", layer, count, version.Int64, gotLayer.String, schemaVersion, layer)
	}
	if err := tx.QueryRowContext(ctx, "PRAGMA "+layer+".page_size").Scan(&size); err != nil {
		return 0, err
	}
	if size != pageSize {
		return 0, fmt.Errorf("%s snapshot page_size=%d; expected page_size=%d", layer, size, pageSize)
	}
	return at.Int64, nil
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
