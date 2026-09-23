package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/metric"
	"log/slog"
)

// schemaV1 是第一个发布版本的完整 DDL，逐字冻结：迁移测试用它建起旧库，
// 再由 Open 升级，与全新建库逐表比对。以后每个版本都在这里追加一份冻结文本。
var schemaV1 = []string{
	`CREATE TABLE node (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  sort_order INTEGER NOT NULL DEFAULT 0,
  public INTEGER NOT NULL DEFAULT 0,
  note TEXT NOT NULL DEFAULT '',
  offline_grace_s INTEGER,
  traffic_reset_day INTEGER NOT NULL DEFAULT 1,
  token_hash BLOB NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  last_seen_at INTEGER
)`,
	`CREATE TABLE node_facts (
  node_id INTEGER PRIMARY KEY,
  facts_hash INTEGER NOT NULL,
  hostname TEXT NOT NULL,
  os TEXT NOT NULL,
  kernel TEXT NOT NULL,
  arch TEXT NOT NULL,
  virtualization TEXT NOT NULL,
  cpu_model TEXT NOT NULL,
  cpu_cores INTEGER NOT NULL,
  agent_version TEXT NOT NULL,
  icmp_available INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
)`,
	`CREATE TABLE register_window (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  key_hash BLOB NOT NULL,
  expires_at INTEGER NOT NULL,
  remaining INTEGER NOT NULL
)`,
	`CREATE TABLE rollup_state (
  level TEXT PRIMARY KEY,
  upto_ts INTEGER NOT NULL
)`,
	`INSERT INTO rollup_state (level, upto_ts) VALUES ('5m', 0), ('1h', 0)`,
	`CREATE TABLE metric_1m (node_id INTEGER NOT NULL, ts INTEGER NOT NULL, cpu_sum REAL NOT NULL, cpu_n INTEGER NOT NULL, cpu_max REAL NOT NULL, mem_used_sum INTEGER NOT NULL, mem_used_n INTEGER NOT NULL, mem_used_max INTEGER NOT NULL, swap_used_sum INTEGER NOT NULL, swap_used_n INTEGER NOT NULL, disk_used_sum INTEGER NOT NULL, disk_used_n INTEGER NOT NULL, load1_sum REAL NOT NULL, load1_n INTEGER NOT NULL, tcp_sum INTEGER NOT NULL, tcp_n INTEGER NOT NULL, udp_sum INTEGER NOT NULL, udp_n INTEGER NOT NULL, procs_sum INTEGER NOT NULL, procs_n INTEGER NOT NULL, PRIMARY KEY (node_id, ts)) WITHOUT ROWID`,
}

type column struct {
	Name, Type string
	NotNull    bool
	Default    sql.NullString
	PK         int
}

// describe 用 PRAGMA table_info 而不是 sqlite_master.sql 比对：后者保留原文的
// 空白与注释，同一结构的两种写法会被判为不同。
func describe(t *testing.T, db *sql.DB) map[string][]column {
	t.Helper()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	rows.Close()
	out := map[string][]column{}
	for _, tbl := range tables {
		info, err := db.Query("PRAGMA table_info(" + tbl + ")")
		if err != nil {
			t.Fatal(err)
		}
		for info.Next() {
			var cid int
			var c column
			if err := info.Scan(&cid, &c.Name, &c.Type, &c.NotNull, &c.Default, &c.PK); err != nil {
				t.Fatal(err)
			}
			out[tbl] = append(out[tbl], c)
		}
		info.Close()
	}
	return out
}

func userVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func migrateFrom(t *testing.T, stmts []string, version int, seed func(*testing.T, *sql.DB)) (migrated, fresh *Store) {
	t.Helper()
	dir := t.TempDir()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	old := filepath.Join(dir, "old.db")
	raw, err := sql.Open("sqlite", dsn(old, ""))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range stmts {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("%v in %q", err, stmt)
		}
	}
	if _, err := raw.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		t.Fatal(err)
	}
	seed(t, raw)
	raw.Close()

	migrated, err = Open(old, clk, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { migrated.Close() })
	fresh, err = Open(filepath.Join(dir, "fresh.db"), clk, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fresh.Close() })

	return migrated, fresh
}

func TestMigrationFromV1MatchesFreshSchema(t *testing.T) {
	migrated, fresh := migrateFrom(t, schemaV1, 1, seedMinuteRow)

	if got, want := describe(t, migrated.r), describe(t, fresh.r); !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated schema differs from fresh schema:\n got: %+v\nwant: %+v", got, want)
	}
	if v := userVersion(t, migrated.r); v != schemaVersion {
		t.Fatalf("user_version = %d, want %d", v, schemaVersion)
	}
	nodes, err := migrated.ListNodes(t.Context())
	if err != nil || len(nodes) != 1 || nodes[0].Name != "kept" {
		t.Fatalf("existing rows lost across migration: %v %v", nodes, err)
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	raw, err := sql.Open("sqlite", dsn(path, ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	if _, err := Open(path, clock.NewFake(time.Unix(0, 0)), slog.Default()); err == nil {
		t.Fatal("opened a database written by a newer binary")
	}
}

// schemaV2 = v1 加 admin、admin_session 与两级上卷表，逐字冻结。
var schemaV2 = append(append([]string{}, schemaV1...),
	`CREATE TABLE admin (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  password_hash TEXT NOT NULL,
  updated_at INTEGER NOT NULL
)`,
	`CREATE TABLE admin_session (
  token_hash BLOB PRIMARY KEY,
  created_at INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
)`,
	`CREATE TABLE metric_5m (node_id INTEGER NOT NULL, ts INTEGER NOT NULL, cpu_sum REAL NOT NULL, cpu_n INTEGER NOT NULL, cpu_max REAL NOT NULL, mem_used_sum INTEGER NOT NULL, mem_used_n INTEGER NOT NULL, mem_used_max INTEGER NOT NULL, swap_used_sum INTEGER NOT NULL, swap_used_n INTEGER NOT NULL, disk_used_sum INTEGER NOT NULL, disk_used_n INTEGER NOT NULL, load1_sum REAL NOT NULL, load1_n INTEGER NOT NULL, tcp_sum INTEGER NOT NULL, tcp_n INTEGER NOT NULL, udp_sum INTEGER NOT NULL, udp_n INTEGER NOT NULL, procs_sum INTEGER NOT NULL, procs_n INTEGER NOT NULL, PRIMARY KEY (node_id, ts)) WITHOUT ROWID`,
	`CREATE TABLE metric_1h (node_id INTEGER NOT NULL, ts INTEGER NOT NULL, cpu_sum REAL NOT NULL, cpu_n INTEGER NOT NULL, cpu_max REAL NOT NULL, mem_used_sum INTEGER NOT NULL, mem_used_n INTEGER NOT NULL, mem_used_max INTEGER NOT NULL, swap_used_sum INTEGER NOT NULL, swap_used_n INTEGER NOT NULL, disk_used_sum INTEGER NOT NULL, disk_used_n INTEGER NOT NULL, load1_sum REAL NOT NULL, load1_n INTEGER NOT NULL, tcp_sum INTEGER NOT NULL, tcp_n INTEGER NOT NULL, udp_sum INTEGER NOT NULL, udp_n INTEGER NOT NULL, procs_sum INTEGER NOT NULL, procs_n INTEGER NOT NULL, PRIMARY KEY (node_id, ts)) WITHOUT ROWID`,
)

func seedMinuteRow(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec("INSERT INTO node (id, name, token_hash, created_at) VALUES (7, 'kept', x'00', 1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO metric_1m (node_id, ts, cpu_sum, cpu_n, cpu_max, mem_used_sum, mem_used_n, mem_used_max,
		swap_used_sum, swap_used_n, disk_used_sum, disk_used_n, load1_sum, load1_n, tcp_sum, tcp_n, udp_sum, udp_n, procs_sum, procs_n)
		VALUES (7, 60, 50, 1, 50, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)`); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationFromV2MatchesFreshSchemaAndKeepsRows(t *testing.T) {
	migrated, fresh := migrateFrom(t, schemaV2, 2, seedMinuteRow)
	if got, want := describe(t, migrated.r), describe(t, fresh.r); !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated schema differs from fresh schema:\n got: %+v\nwant: %+v", got, want)
	}
	rows, err := migrated.ReadMinuteRows(t.Context(), 7, 0, 120)
	if err != nil || len(rows) != 1 {
		t.Fatalf("minute row lost across rebuild: %v %v", rows, err)
	}
	if mean, ok := rows[0].Bucket.Mean(0); !ok || mean != 50 {
		t.Fatalf("cpu mean after rebuild = %v/%v, want 50", mean, ok)
	}
	if rows[0].Bucket.N[metric.RxBytes] != 0 {
		t.Fatal("rebuilt row must have no traffic accounted")
	}
	var n int64
	if err := migrated.r.QueryRow("SELECT COUNT(*) FROM traffic").Scan(&n); err != nil || n != 0 {
		t.Fatalf("traffic table missing or non-empty: %v %v", n, err)
	}
}
