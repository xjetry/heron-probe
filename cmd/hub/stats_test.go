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
			// v9 只增加计费与到期列；去掉这些列得到可实际迁移的 v8 库，避免仅伪造版本号。
			for _, stmt := range []string{
				"ALTER TABLE node DROP COLUMN price",
				"ALTER TABLE node DROP COLUMN currency",
				"ALTER TABLE node DROP COLUMN billing_cycle",
				"ALTER TABLE node DROP COLUMN expires_on",
				"ALTER TABLE node DROP COLUMN auto_renew",
				"ALTER TABLE alert_rule DROP COLUMN days_before",
				"ALTER TABLE alert_state DROP COLUMN fired_expires_on",
				"PRAGMA user_version = 8",
			} {
				if _, err := raw.Exec(stmt); err != nil {
					t.Fatal(err)
				}
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
			var version int
			if err := raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
				t.Fatal(err)
			}
			if version != 8 {
				t.Errorf("offline command changed user_version to %d, want 8", version)
			}
		})
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
