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
)

func TestNodeDependentTablesComplete(t *testing.T) {
	s, _ := open(t)
	rows, err := s.r.Query(`SELECT s.name FROM sqlite_schema s
		WHERE s.type='table' AND EXISTS (SELECT 1 FROM pragma_table_info(s.name) WHERE name='node_id')
		ORDER BY s.name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actual []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		actual = append(actual, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// alert_event 保留审计历史，DeleteNode 也不删除它；系统事件使用 node_id=0。
	want := append(slices.Clone(nodeDependentTables), "alert_event")
	slices.Sort(want)
	if !slices.Equal(actual, want) {
		t.Errorf("node-dependent classification must cover each node_id table exactly once: database=%v classified=%v", actual, want)
	}
}

func TestRestoreRecordMigration(t *testing.T) {
	schemaV13 := append(slices.Clone(schemaV12), "ALTER TABLE node ADD COLUMN last_source TEXT NOT NULL DEFAULT ''")
	migrated, fresh := migrateFrom(t, schemaV13, 13, seedMinuteRow)
	if got, want := describe(t, migrated.r), describe(t, fresh.r); !reflect.DeepEqual(got, want) {
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
