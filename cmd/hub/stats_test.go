package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
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
			if freshVersion != 9 {
				t.Fatalf("fixture user_version = %d, want 9; this fixture is built for schema 9 by dropping its 7 added columns below, rebuild the v8 fixture for the new version", freshVersion)
			}
			// v9 只增加计费与到期列；去掉这些列得到可实际迁移的 v8 库，避免仅伪造版本号。
			// 这个夹具经 openOffline 建成，openStore 判定通过后已经把它切成 WAL；切回
			// DELETE 是因为提前生效的 journal_mode(WAL) 只在非 WAL 的库上改写文件头：本项目
			// 自己产出的 v8 库本就是 WAL，在它上面这个缺陷不显形，逐字节比较测不出。
			for _, stmt := range []string{
				"ALTER TABLE node DROP COLUMN price",
				"ALTER TABLE node DROP COLUMN currency",
				"ALTER TABLE node DROP COLUMN billing_cycle",
				"ALTER TABLE node DROP COLUMN expires_on",
				"ALTER TABLE node DROP COLUMN auto_renew",
				"ALTER TABLE alert_rule DROP COLUMN days_before",
				"ALTER TABLE alert_state DROP COLUMN fired_expires_on",
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
	if want := `msg="database schema created" version=9`; !strings.Contains(stderr.String(), want) {
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
	if _, _, err := st.CreateNode(context.Background(), "n", make([]byte, 32)); err != nil {
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
	if !slices.IsSorted(tables) {
		t.Fatalf("tables not sorted: %v", tables)
	}
	for _, want := range []string{"api_token: 0", "node: 1", "probe_meta: 1", "setting: 0"} {
		if !slices.Contains(tables, want) {
			t.Errorf("stats lacks %q: %v", want, tables)
		}
	}
}
