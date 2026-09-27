package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xjetry/probe/internal/clock"
)

func schemaPolicyFixture(t *testing.T, statements []string, version int) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schema.db")
	db, err := sql.Open("sqlite", dsn(path, ""))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		t.Fatal(err)
	}
	return path, db
}

func assertSchemaLogs(t *testing.T, buf *bytes.Buffer, want ...map[string]any) {
	t.Helper()
	var got []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	for decoder.More() {
		var record map[string]any
		if err := decoder.Decode(&record); err != nil {
			t.Fatal(err)
		}
		delete(record, "time")
		got = append(got, record)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("schema logs = %v, want %v", got, want)
	}
}

func TestSchemaPolicyRequiresCurrentWithoutChangingV8(t *testing.T) {
	path, raw := schemaPolicyFixture(t, schemaV8, 8)
	seedMinuteRow(t, raw)
	before := describe(t, raw)
	var logs bytes.Buffer
	st, err := Open(path, clock.Real(), slog.New(slog.NewJSONHandler(&logs, nil)), RequireCurrentSchema)
	if st != nil {
		st.Close()
	}
	want := fmt.Sprintf("database schema version 8 is older than this binary (%d); start the new probe-hub serve once to upgrade it (back up the database first)", schemaVersion)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("RequireCurrentSchema error = %v, want %q", err, want)
	}
	if got := userVersion(t, raw); got != 8 {
		t.Errorf("rejected database user_version = %d, want 8", got)
	}
	after := describe(t, raw)
	if got, want := len(after.Tables["node"]), len(before.Tables["node"]); got != want {
		t.Errorf("rejected database node columns = %d, want %d", got, want)
	}
	if !reflect.DeepEqual(after, before) {
		t.Error("rejected database schema changed")
	}
	var name string
	if err := raw.QueryRow("SELECT name FROM node WHERE id = 7").Scan(&name); err != nil || name != "kept" {
		t.Errorf("rejected database node = %q, %v, want kept", name, err)
	}
	assertSchemaLogs(t, &logs)
}

func TestSchemaPolicyMigratesAndLogsEachStep(t *testing.T) {
	for _, fixture := range []struct {
		version int
		schema  []string
	}{{7, schemaV7}, {8, schemaV8}, {9, schemaV9}, {10, schemaV10}, {11, schemaV11}} {
		t.Run(fmt.Sprint(fixture.version), func(t *testing.T) {
			path, raw := schemaPolicyFixture(t, fixture.schema, fixture.version)
			var logs bytes.Buffer
			st, err := Open(path, clock.Real(), slog.New(slog.NewJSONHandler(&logs, nil)), MigrateSchema)
			if err != nil {
				t.Fatalf("MigrateSchema failed: %v", err)
			}
			defer st.Close()
			if got := userVersion(t, raw); got != schemaVersion {
				t.Errorf("migrated user_version = %d, want %d", got, schemaVersion)
			}
			var want []map[string]any
			for from := fixture.version; from < schemaVersion; from++ {
				want = append(want, map[string]any{"level": "INFO", "msg": "database schema migrated", "from": float64(from), "to": float64(from + 1)})
			}
			assertSchemaLogs(t, &logs, want...)
		})
	}
}

func TestSchemaPolicyCreatesAndReopensWithoutMigration(t *testing.T) {
	for _, policy := range []SchemaPolicy{MigrateSchema, RequireCurrentSchema} {
		t.Run(fmt.Sprint(policy), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "empty.db")
			if err := os.WriteFile(path, nil, 0600); err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&logs, nil))
			st, err := Open(path, clock.Real(), log, policy)
			if err != nil {
				t.Fatalf("empty database open failed: %v", err)
			}
			if got := userVersion(t, st.r); got != schemaVersion {
				t.Errorf("created user_version = %d, want %d", got, schemaVersion)
			}
			if _, _, err := st.CreateNode(t.Context(), "new", hash(1)); err != nil {
				t.Errorf("created schema is not usable: %v", err)
			}
			st.Close()
			assertSchemaLogs(t, &logs, map[string]any{"level": "INFO", "msg": "database schema created", "version": float64(schemaVersion)})
			logs.Reset()
			st, err = Open(path, clock.Real(), log, policy)
			if err != nil {
				t.Fatalf("current database reopen failed: %v", err)
			}
			st.Close()
			assertSchemaLogs(t, &logs)
		})
	}
}

func TestSchemaPolicyRejectsInvalidPolicy(t *testing.T) {
	for _, policy := range []SchemaPolicy{0, -1, 3} {
		t.Run(fmt.Sprint(policy), func(t *testing.T) {
			defer func() {
				if got := recover(); got != "store.Open requires a valid SchemaPolicy" {
					t.Errorf("invalid policy panic = %v, want explicit SchemaPolicy panic", got)
				}
			}()
			st, _ := Open(filepath.Join(t.TempDir(), "invalid.db"), clock.Real(), slog.Default(), policy)
			if st != nil {
				st.Close()
			}
		})
	}
}
