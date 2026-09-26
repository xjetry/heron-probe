package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// 统计的表清单来自库本身：原先手写清单漏掉的 api_token、probe_meta、setting 都在；
// 关库后主文件已检查点，db_bytes 等于它的大小。
func TestStatsPrintsSizeAndEveryTable(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hub.db")
	st, _, err := openOffline(db, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateNode(context.Background(), "n", make([]byte, 32)); err != nil {
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
