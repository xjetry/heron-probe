package store

import (
	"database/sql"
	"log/slog"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/sqlitetest"
)

func TestNodeDependentTablesComplete(t *testing.T) {
	s, _ := open(t)
	actual := tablesWithNodeID(t, s)
	want := append(slices.Clone(nodeDependentTables), keptOnNodeDelete...)
	slices.Sort(want)
	if !slices.Equal(actual, want) {
		t.Errorf("node-dependent classification must cover each node_id table exactly once: database=%v classified=%v", actual, want)
	}
}

// v16 的完整 DDL：v15 加上主题元数据、启用约束与文件表。
var schemaV16 = append(slices.Clone(schemaV15),
	`CREATE TABLE theme (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  version TEXT NOT NULL,
  preview TEXT NOT NULL,
  uploaded_at INTEGER NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1))
)`,
	`CREATE UNIQUE INDEX theme_enabled ON theme (enabled) WHERE enabled = 1`,
	`CREATE TABLE theme_file (
  theme_id TEXT NOT NULL,
  path TEXT NOT NULL,
  content BLOB NOT NULL,
  PRIMARY KEY (theme_id, path)
)`)

func TestRestoreRecordMigration(t *testing.T) {
	migrated, fresh := migrateFrom(t, schemaV16, 16, seedMinuteRow)
	if got, want := sqlitetest.Describe(t, migrated.r), sqlitetest.Describe(t, fresh.r); !reflect.DeepEqual(got, want) {
		t.Errorf("restore record migrated schema differs from fresh: got=%v want=%v", got, want)
	}
	var count int
	if err := migrated.r.QueryRow("SELECT count(*) FROM restore_record").Scan(&count); err != nil || count != 0 {
		t.Errorf("restore record migration: count=%d err=%v", count, err)
	}
	for _, s := range []*Store{migrated, fresh} {
		var primary int
		if err := s.r.QueryRow("SELECT coalesce(sum(pk),0) FROM pragma_table_info('restore_record') WHERE name='id' AND type='TEXT'").Scan(&primary); err != nil {
			t.Fatal(err)
		}
		if primary != 1 {
			t.Errorf("restore record id must be TEXT primary key: got pk=%d", primary)
		}
	}
}

func TestRestoreRecordUnion(t *testing.T) {
	source, _ := open(t)
	target, _ := open(t)
	const a = "00000000000000000000000000000001"
	const b = "00000000000000000000000000000002"
	const c = "00000000000000000000000000000003"
	for _, seed := range []struct {
		s   *Store
		ids []string
	}{{source, []string{a, b}}, {target, []string{a, c}}} {
		for _, id := range seed.ids {
			if _, err := seed.s.w.Exec("INSERT INTO restore_record (id,restored_at,config_taken_at,metrics_taken_at,orphans) VALUES (?,7,6,NULL,'{}')", id); err != nil {
				t.Fatal(err)
			}
		}
	}
	config := filepath.Join(t.TempDir(), "config.db")
	if err := source.SnapshotConfig(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		result, err := Restore(t.Context(), target.path, config, "", time.Unix(9000, 0), slog.Default())
		if err != nil {
			t.Fatalf("restore must retain independent records even at the same second: %v", err)
		}
		var actual []string
		for table := range result.Orphans {
			actual = append(actual, table)
		}
		want := slices.Clone(nodeDependentTables)
		slices.Sort(actual)
		slices.Sort(want)
		if !slices.Equal(actual, want) {
			t.Errorf("restore orphan summary must enumerate nodeDependentTables: got=%v want=%v", actual, want)
		}
	}
	db, err := sql.Open("sqlite", target.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var oldIDs string
	if err := db.QueryRow("SELECT group_concat(id, ',') FROM (SELECT id FROM restore_record WHERE restored_at=7 ORDER BY id)").Scan(&oldIDs); err != nil {
		t.Fatal(err)
	}
	if want := a + "," + b + "," + c; oldIDs != want {
		t.Errorf("audit union must retain snapshot and target IDs once, including equal business fields: got=%s want=%s", oldIDs, want)
	}
	rows, err := db.Query("SELECT id FROM restore_record WHERE restored_at=9000")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	idPattern := regexp.MustCompile(`^[0-9a-f]{32}$`)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		if !idPattern.MatchString(id) {
			t.Errorf("new audit id must be 128-bit lowercase hex: %q", id)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Errorf("two restores in the same second must retain two distinct records: %v", ids)
	}
}
