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
	}{{7, schemaV7}, {8, schemaV8}, {9, schemaV9}, {10, schemaV10}} {
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

// store.go 的注释声称"每步事务提交成功后才记日志，避免把回滚的迁移记成已完成"：
// 让迁移 9 的第一条 ALTER 撞上已存在的同名列，使那一步的事务回滚，验证日志确实
// 止步于最后一步已提交的迁移，不多写一行从未持久化的 to:9。
func TestSchemaPolicyPartialMigrationLogsOnlyCommittedSteps(t *testing.T) {
	path, raw := schemaPolicyFixture(t, schemaV7, 7)
	if _, err := raw.Exec("ALTER TABLE node ADD COLUMN price TEXT NOT NULL DEFAULT ''"); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	st, err := Open(path, clock.Real(), slog.New(slog.NewJSONHandler(&logs, nil)), MigrateSchema)
	if st != nil {
		st.Close()
	}
	want := "duplicate column name: price"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("partial migration error = %v, want to contain %q", err, want)
	}
	if got := userVersion(t, raw); got != 8 {
		t.Errorf("partial migration left user_version = %d, want 8 (only the committed 7->8 step)", got)
	}
	assertSchemaLogs(t, &logs, map[string]any{"level": "INFO", "msg": "database schema migrated", "from": float64(7), "to": float64(8)})
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

// 非本项目建的库通常从没调用过 PRAGMA user_version，读出来正好是 0；如果只看版本号
// 就当空库处理，CREATE TABLE 会把 schemaStatements 的全部对象叠进陌生库已有的数据上。
// 夹具不经 dsn 的任何 pragma 建库（sqlite3 建库的默认日志模式就是 DELETE），拒绝时
// 逐字节比较而不只比较表结构：journal_mode(WAL) 这类 pragma 一旦在某个连接上生效就
// 立即改写文件头（第 18—19 字节标出日志模式），比对表结构看不出这种改写。
func TestSchemaPolicyRejectsDatabaseWithTablesButNoVersion(t *testing.T) {
	for _, policy := range []SchemaPolicy{MigrateSchema, RequireCurrentSchema} {
		t.Run(fmt.Sprint(policy), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "foreign.db")
			raw, err := sql.Open("sqlite", "file:"+path)
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
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			st, err := Open(path, clock.Real(), slog.Default(), policy)
			if st != nil {
				st.Close()
			}
			want := "not a probe database"
			if err == nil {
				t.Fatalf("foreign database open error = <nil>, want to contain %q", want)
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("foreign database open error = %v, want to contain %q", err, want)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Errorf("foreign database bytes changed: before %d bytes, after %d bytes", len(before), len(after))
			}
		})
	}
}

// 负数版本落进旧库分支会去找不存在的 migrations[0]（MigrateSchema）或建议改跑
// serve 升级（RequireCurrentSchema）——两条提示都假定这是本项目的旧库，跟着做都走不通。
// 逐字节比较的理由同上一条；夹具在下面独立建成 DELETE 模式库。
func TestSchemaPolicyRejectsNegativeVersionWithoutSuggestingServe(t *testing.T) {
	for _, policy := range []SchemaPolicy{MigrateSchema, RequireCurrentSchema} {
		t.Run(fmt.Sprint(policy), func(t *testing.T) {
			// 不经 schemaPolicyFixture：它建夹具用的是 dsn，dsn 的 pragma 列表一旦重新
			// 带回 journal_mode(WAL)，"改写前"的快照会在夹具建立的那一刻就已经被写成
			// WAL，测不出 Open 自己有没有再碰这个文件。这条用例要单独验证 Open，
			// 夹具必须独立于 dsn 的实现。
			path := filepath.Join(t.TempDir(), "negative.db")
			raw, err := sql.Open("sqlite", "file:"+path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { raw.Close() })
			if _, err := raw.Exec("PRAGMA user_version = -1"); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			st, err := Open(path, clock.Real(), slog.Default(), policy)
			if st != nil {
				st.Close()
			}
			want := "not a probe database"
			if err == nil {
				t.Fatalf("negative version open error = <nil>, want to contain %q", want)
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("negative version open error = %v, want to contain %q", err, want)
			}
			if strings.Contains(err.Error(), "serve") {
				t.Errorf("negative version error = %v, must not suggest running serve", err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Errorf("negative version database bytes changed: before %d bytes, after %d bytes", len(before), len(after))
			}
		})
	}
}

// 守卫挪到 openStore 之后，panic 仍会发生：能观察到挪动的不是 panic 本身，而是
// panic 之前多做的那部分工作留下的副作用。用一个已存在的 v8 库：挪动后的守卫会先
// 把它迁到 v9 再 panic，这正是 store.go 的 SchemaPolicy 注释所说守卫要防的
// "遗漏选择时意外迁移"。这条钉住"库被迁走"这一种副作用；空路径上的另一种副作用见
// 下一条用例。
func TestSchemaPolicyRejectsInvalidPolicy(t *testing.T) {
	for _, policy := range []SchemaPolicy{0, -1, 3} {
		t.Run(fmt.Sprint(policy), func(t *testing.T) {
			path, raw := schemaPolicyFixture(t, schemaV8, 8)
			before := describe(t, raw)
			defer func() {
				if got := recover(); got != "store.Open requires a valid SchemaPolicy" {
					t.Errorf("invalid policy panic = %v, want explicit SchemaPolicy panic", got)
				}
				if got := userVersion(t, raw); got != 8 {
					t.Errorf("invalid policy changed user_version to %d before panicking, want 8", got)
				}
				if after := describe(t, raw); !reflect.DeepEqual(after, before) {
					t.Error("invalid policy changed schema before panicking")
				}
			}()
			st, _ := Open(path, clock.Real(), slog.Default(), policy)
			if st != nil {
				st.Close()
			}
		})
	}
}

// 同一种挪动在空路径上留下另一种副作用：挪到 openStore 之后会先建出这个文件再
// panic。这条钉住"文件被建出"，与上一条钉住的"库被迁走"是两种不同的副作用，各自
// 只覆盖自己这条路径上的挪动。
func TestSchemaPolicyRejectsInvalidPolicyBeforeTouchingAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.db")
	defer func() {
		if got := recover(); got != "store.Open requires a valid SchemaPolicy" {
			t.Errorf("invalid policy panic = %v, want explicit SchemaPolicy panic", got)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("invalid policy created a file at %s before panicking: %v", path, err)
		}
	}()
	st, _ := Open(path, clock.Real(), slog.Default(), SchemaPolicy(0))
	if st != nil {
		st.Close()
	}
}
