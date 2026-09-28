package store

import (
	"reflect"
	"slices"
	"testing"
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
}
