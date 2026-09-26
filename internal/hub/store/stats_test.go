package store

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/clock"
)

// rowCounts 是测试里按表名取行数的写法；来源与 GetStorageStats、probe-hub stats 相同。
func rowCounts(t testing.TB, s *Store) map[string]int64 {
	t.Helper()
	stats, err := s.StorageStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, tr := range stats.Tables {
		out[tr.Name] = tr.Rows
	}
	return out
}

func openAt(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.db")
	s, err := Open(path, clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

// 统计恰好覆盖 schemaStatements 建出的每一张表，不多不少，行数与表一一对应。新库的 sqlite_master 就是执行这些
// 语句建出来的，所以按它列表的实现自然满足；统计若改成手写的表名清单，以后新增表而漏改清单时这个用例会红。
func TestStorageStatsCoversEveryTableAndCountsRows(t *testing.T) {
	s, _ := openAt(t)
	for i := range 3 {
		if _, err := s.CreateNode(t.Context(), fmt.Sprint("n", i), hash(byte(i))); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := s.StorageStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tr := range stats.Tables {
		names = append(names, tr.Name)
	}
	var want []string
	create := regexp.MustCompile(`^CREATE TABLE (\w+)`)
	for _, stmt := range schemaStatements() {
		if m := create.FindStringSubmatch(stmt); m != nil {
			want = append(want, m[1])
		}
	}
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Fatalf("tables = %v, want %v", names, want)
	}
	rows := rowCounts(t, s)
	if rows["node"] != 3 || rows["setting"] != 0 || rows["probe_meta"] != 1 || rows["api_token"] != 0 {
		t.Fatalf("rows = %v", rows)
	}
}

// db_bytes 是逻辑大小 page_count × page_size：WAL 检查点之前主文件可能远小于它，之后二者相等。
func TestStorageStatsReportsLogicalDatabaseSize(t *testing.T) {
	s, path := openAt(t)
	for i := range 200 {
		if _, err := s.CreateNode(t.Context(), "n", hash(byte(i))); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := s.StorageStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.w.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if stats.DBBytes <= 4096 || stats.DBBytes != info.Size() {
		t.Fatalf("db_bytes = %d, main file after checkpoint = %d", stats.DBBytes, info.Size())
	}
}
