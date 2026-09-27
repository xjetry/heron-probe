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
	want := "database schema version 8 is older than this binary (9); start the new probe-hub serve once to upgrade it (back up the database first)"
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
	}{{7, schemaV7}, {8, schemaV8}} {
		t.Run(fmt.Sprint(fixture.version), func(t *testing.T) {
			path, raw := schemaPolicyFixture(t, fixture.schema, fixture.version)
			var logs bytes.Buffer
			st, err := Open(path, clock.Real(), slog.New(slog.NewJSONHandler(&logs, nil)), MigrateSchema)
			if err != nil {
				t.Fatalf("MigrateSchema failed: %v", err)
			}
			defer st.Close()
			if got := userVersion(t, raw); got != 9 {
				t.Errorf("migrated user_version = %d, want 9", got)
			}
			var want []map[string]any
			for from := fixture.version; from < 9; from++ {
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
			if got := userVersion(t, st.r); got != 9 {
				t.Errorf("created user_version = %d, want 9", got)
			}
			if _, err := st.CreateNode(t.Context(), "new", hash(1)); err != nil {
				t.Errorf("created schema is not usable: %v", err)
			}
			st.Close()
			assertSchemaLogs(t, &logs, map[string]any{"level": "INFO", "msg": "database schema created", "version": float64(9)})
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

// 非本项目建的库通常从没调用过 PRAGMA user_version，读出来正好是 0；如果只看版本号
// 就当空库处理，CREATE TABLE 会把 schemaStatements 的全部对象叠进陌生库已有的数据上。
func TestSchemaPolicyRejectsDatabaseWithTablesButNoVersion(t *testing.T) {
	for _, policy := range []SchemaPolicy{MigrateSchema, RequireCurrentSchema} {
		t.Run(fmt.Sprint(policy), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "foreign.db")
			raw, err := sql.Open("sqlite", dsn(path, ""))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { raw.Close() })
			if _, err := raw.Exec("CREATE TABLE unrelated (id INTEGER PRIMARY KEY, note TEXT)"); err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec("INSERT INTO unrelated (note) VALUES ('not a probe database')"); err != nil {
				t.Fatal(err)
			}
			before := describe(t, raw)
			st, err := Open(path, clock.Real(), slog.Default(), policy)
			if st != nil {
				st.Close()
			}
			want := "not a probe database"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("foreign database open error = %v, want to contain %q", err, want)
			}
			if got := userVersion(t, raw); got != 0 {
				t.Errorf("foreign database user_version = %d, want 0", got)
			}
			if after := describe(t, raw); !reflect.DeepEqual(after, before) {
				t.Error("foreign database schema changed")
			}
			var rows int
			if err := raw.QueryRow("SELECT count(*) FROM unrelated").Scan(&rows); err != nil || rows != 1 {
				t.Errorf("foreign database rows = %d, %v, want 1 row kept", rows, err)
			}
		})
	}
}

// 负数版本落进旧库分支会去找不存在的 migrations[0]（MigrateSchema）或建议改跑
// serve 升级（RequireCurrentSchema）——两条提示都假定这是本项目的旧库，跟着做都走不通。
func TestSchemaPolicyRejectsNegativeVersionWithoutSuggestingServe(t *testing.T) {
	for _, policy := range []SchemaPolicy{MigrateSchema, RequireCurrentSchema} {
		t.Run(fmt.Sprint(policy), func(t *testing.T) {
			path, raw := schemaPolicyFixture(t, nil, -1)
			st, err := Open(path, clock.Real(), slog.Default(), policy)
			if st != nil {
				st.Close()
			}
			want := "not a probe database"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("negative version open error = %v, want to contain %q", err, want)
			}
			if strings.Contains(err.Error(), "serve") {
				t.Errorf("negative version error = %v, must not suggest running serve", err)
			}
			if got := userVersion(t, raw); got != -1 {
				t.Errorf("negative version database user_version = %d, want -1", got)
			}
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
