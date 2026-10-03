package main

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/xjetry/heron-probe/internal/clock"
	"github.com/xjetry/heron-probe/internal/hub/store"
	"github.com/xjetry/heron-probe/internal/sqlitetest"
)

func TestOfflineCommandsRejectV8(t *testing.T) {
	for _, args := range [][]string{{"stats"}, {"passwd"}, {"token", "list"}, {"node", "list"}, {"window", "show"}} {
		t.Run(args[0], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "v8.db")
			st, _, err := openOffline(path, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			var freshVersion int
			if err := raw.QueryRow("PRAGMA user_version").Scan(&freshVersion); err != nil {
				t.Fatal(err)
			}
			if freshVersion != 29 {
				t.Fatalf("fixture user_version = %d, want 29; rebuild the v8 fixture for the new version", freshVersion)
			}
			removeV29Config(t, raw)
			removeV26Config(t, raw)
			removeV27Config(t, raw)
			removeV25Config(t, raw)
			restoreExec(t, raw, `DROP TABLE register_window;
				CREATE TABLE register_window (id INTEGER PRIMARY KEY CHECK(id=1),key_hash BLOB NOT NULL,expires_at INTEGER NOT NULL,remaining INTEGER NOT NULL)`)
			removeV21Columns(t, raw, raw)
			removeV28Metrics(t, raw)
			// 后续 schema 增加列、索引及维护状态、标签、主题、恢复记录、认证配置与选择器关联表，并重建 alert_delivery：多出
			// batch_id 与 not_before 两列，alert_delivery_pending 从 (done, id) 改成 (done, batch_id, channel_id)，其余列的
			// 名称、类型、默认值与先后不变。逐项撤回得到可实际迁移的 v8 库，避免仅伪造版本号。
			// 这个夹具经 openOffline 建成，openStore 判定通过后已经把它切成 WAL；切回
			// DELETE 是因为提前生效的 journal_mode(WAL) 只在非 WAL 的库上改写文件头：本项目
			// 自己产出的 v8 库本就是 WAL，在它上面这个缺陷不显形，逐字节比较测不出。
			for _, stmt := range []string{
				"DROP TABLE node_update",
				"ALTER TABLE node_facts DROP COLUMN network",
				"DROP TABLE theme_selection",
				"DROP TABLE theme_version",
				"DROP TABLE admin_security",
				"DROP TABLE probe_task_tag",
				"DROP TABLE alert_rule_tag",
				"ALTER TABLE alert_rule DROP COLUMN resource_metric",
				"ALTER TABLE alert_rule DROP COLUMN recovery_threshold",
				"ALTER TABLE metric_1m DROP COLUMN memory_used_pct_sum",
				"ALTER TABLE metric_1m DROP COLUMN memory_used_pct_n",
				"ALTER TABLE metric_1m DROP COLUMN disk_used_pct_sum",
				"ALTER TABLE metric_1m DROP COLUMN disk_used_pct_n",
				"ALTER TABLE metric_5m DROP COLUMN memory_used_pct_sum",
				"ALTER TABLE metric_5m DROP COLUMN memory_used_pct_n",
				"ALTER TABLE metric_5m DROP COLUMN disk_used_pct_sum",
				"ALTER TABLE metric_5m DROP COLUMN disk_used_pct_n",
				"ALTER TABLE metric_1h DROP COLUMN memory_used_pct_sum",
				"ALTER TABLE metric_1h DROP COLUMN memory_used_pct_n",
				"ALTER TABLE metric_1h DROP COLUMN disk_used_pct_sum",
				"ALTER TABLE metric_1h DROP COLUMN disk_used_pct_n",
				"ALTER TABLE node DROP COLUMN price",
				"ALTER TABLE node DROP COLUMN currency",
				"ALTER TABLE node DROP COLUMN billing_cycle",
				"ALTER TABLE node DROP COLUMN expires_on",
				"ALTER TABLE node DROP COLUMN auto_renew",
				"ALTER TABLE alert_rule DROP COLUMN days_before",
				"ALTER TABLE alert_state DROP COLUMN fired_expires_on",
				"ALTER TABLE probe_task DROP COLUMN all_nodes",
				"DROP TABLE maintenance_state",
				"ALTER TABLE alert_state DROP COLUMN recovered_at",
				"ALTER TABLE node DROP COLUMN last_source",
				"ALTER TABLE node DROP COLUMN country",
				"ALTER TABLE node DROP COLUMN country_ip",
				"ALTER TABLE node DROP COLUMN country_pin",
				"DROP TABLE node_tag",
				"DROP TABLE tag",
				"DROP TABLE theme_file",
				"DROP TABLE theme_package",
				"DROP TABLE theme",
				"DROP TABLE restore_record",
				"DROP INDEX alert_delivery_by_batch",
				"DROP INDEX alert_delivery_pending",
				"ALTER TABLE alert_delivery DROP COLUMN batch_id",
				"ALTER TABLE alert_delivery DROP COLUMN not_before",
				"CREATE INDEX alert_delivery_pending ON alert_delivery(done, id)",
				"ALTER TABLE notify_channel DROP COLUMN rate_per_minute",
				"PRAGMA user_version = 8",
				"PRAGMA journal_mode=DELETE",
			} {
				if _, err := raw.Exec(stmt); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			cmd := hubCommand(t, append(args, "--db", path)...)
			cmd.Stdin = strings.NewReader("long enough test password\n")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err = cmd.Run()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() == 0 {
				t.Errorf("old database command exit = %v, want nonzero exit", err)
			}
			if !strings.Contains(stderr.String(), "older than this binary") {
				t.Errorf("old database stderr = %q, want older than this binary", stderr.String())
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Errorf("offline command changed database bytes: before %d bytes, after %d bytes", len(before), len(after))
			}
			migrated, err := store.Open(path, clock.Real(), slog.Default(), store.MigrateSchema)
			if err != nil {
				t.Fatalf("v8 fixture must migrate with serve policy: %v", err)
			}
			if err := migrated.Close(); err != nil {
				t.Fatal(err)
			}
			freshPath := filepath.Join(t.TempDir(), "fresh.db")
			fresh, _, err := openOffline(freshPath, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := fresh.Close(); err != nil {
				t.Fatal(err)
			}
			if got, want := sqlitetest.Describe(t, raw), sqlitetest.Describe(t, restoreDB(t, freshPath)); !reflect.DeepEqual(got, want) {
				t.Errorf("migrated v8 schema differs from fresh: got=%v want=%v", got, want)
			}
		})
	}
}

// openOffline 的注释声称建立状态的子命令必须放出 store 的 Info 级 schema 事件；
// created 是这类命令新建了文件的唯一信号，钉住这一行不被日志级别过滤掉。
func TestPasswdLogsSchemaCreationOnStderr(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.db")
	cmd := hubCommand(t, "passwd", "--db", path)
	cmd.Stdin = strings.NewReader("long enough test password\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("passwd command: %v, stderr: %s", err, stderr.String())
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var created int
	if err := raw.QueryRow("PRAGMA user_version").Scan(&created); err != nil {
		t.Fatal(err)
	}
	// 建库日志里的版本号必须是库里实际写下的那个；不写死数字，免得每次迁移都要改这里。
	if want := `msg="database schema created" version=` + strconv.Itoa(created); created == 0 || !strings.Contains(stderr.String(), want) {
		t.Errorf("passwd stderr = %q, want to contain %q", stderr.String(), want)
	}
}

// 统计的表清单来自库本身：原先手写清单漏掉的 api_token、probe_meta、setting 都在；
// 关库后主文件已检查点，db_bytes 等于它的大小。
func TestStatsPrintsSizeAndEveryTable(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hub.db")
	st, _, err := openOffline(db, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateNode(context.Background(), "n", store.Billing{}, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := runStatsWith([]string{"--db", db}, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	size, ok := strings.CutPrefix(lines[0], "db_bytes: ")
	info, statErr := os.Stat(db)
	if !ok || statErr != nil || size != strconv.FormatInt(info.Size(), 10) {
		t.Fatalf("first line %q, file size %v (%v)", lines[0], info, statErr)
	}
	tables := lines[1:]
	// 表行之后是健康行，它们的键带点号。
	if i := slices.IndexFunc(tables, func(l string) bool { k, _, _ := strings.Cut(l, ":"); return strings.Contains(k, ".") }); i >= 0 {
		tables = tables[:i]
	}
	if !slices.IsSorted(tables) {
		t.Fatalf("tables not sorted: %v", tables)
	}
	for _, want := range []string{"api_token: 0", "node: 1", "probe_meta: 1", "setting: 0"} {
		if !slices.Contains(tables, want) {
			t.Errorf("stats lacks %q: %v", want, tables)
		}
	}
}
