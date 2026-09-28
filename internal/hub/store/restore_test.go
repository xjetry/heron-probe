package store

import (
	"reflect"
	"slices"
	"testing"
)

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
