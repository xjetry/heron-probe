package store

import (
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/metric"
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

type schemaDescription struct {
	Tables  map[string][]column
	Indexes map[string]indexDescription
}

type indexDescription struct {
	Table   string
	Columns []string
	Unique  bool
}

// describe 比较结构而不是 SQL 原文：空白与注释不改变结构，索引列序与唯一性会改变访问路径或约束。
func describe(t *testing.T, db *sql.DB) schemaDescription {
	t.Helper()
	rows, err := db.Query("SELECT name, type, tbl_name FROM sqlite_master WHERE type IN ('table', 'index') AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	out := schemaDescription{Tables: map[string][]column{}, Indexes: map[string]indexDescription{}}
	for rows.Next() {
		var name, kind, table string
		if err := rows.Scan(&name, &kind, &table); err != nil {
			t.Fatal(err)
		}
		if kind == "table" {
			out.Tables[name] = nil
		} else {
			out.Indexes[name] = indexDescription{Table: table}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for table := range out.Tables {
		info, err := db.Query(fmt.Sprintf("PRAGMA table_info(%q)", table))
		if err != nil {
			t.Fatal(err)
		}
		for info.Next() {
			var cid int
			var c column
			if err := info.Scan(&cid, &c.Name, &c.Type, &c.NotNull, &c.Default, &c.PK); err != nil {
				t.Fatal(err)
			}
			out.Tables[table] = append(out.Tables[table], c)
		}
		if err := info.Err(); err != nil {
			t.Fatal(err)
		}
		if err := info.Close(); err != nil {
			t.Fatal(err)
		}

		indexes, err := db.Query(fmt.Sprintf("PRAGMA index_list(%q)", table))
		if err != nil {
			t.Fatal(err)
		}
		for indexes.Next() {
			var seq int
			var name, origin string
			var unique, partial bool
			if err := indexes.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
				t.Fatal(err)
			}
			if index, ok := out.Indexes[name]; ok {
				index.Unique = unique
				out.Indexes[name] = index
			}
		}
		if err := indexes.Err(); err != nil {
			t.Fatal(err)
		}
		if err := indexes.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for name, index := range out.Indexes {
		info, err := db.Query(fmt.Sprintf("PRAGMA index_info(%q)", name))
		if err != nil {
			t.Fatal(err)
		}
		for info.Next() {
			var seq, cid int
			var column string
			if err := info.Scan(&seq, &cid, &column); err != nil {
				t.Fatal(err)
			}
			for len(index.Columns) <= seq {
				index.Columns = append(index.Columns, "")
			}
			index.Columns[seq] = column
		}
		if err := info.Err(); err != nil {
			t.Fatal(err)
		}
		if err := info.Close(); err != nil {
			t.Fatal(err)
		}
		out.Indexes[name] = index
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

// schemaV3 冻结发布时的 DDL，避免生产常量变化后旧库夹具也随之改变。
var schemaV3 = []string{
	`CREATE TABLE node (
  -- AUTOINCREMENT 使 id 永不复用：分层备份恢复后两层可能各自漂移，
  -- id 若复用，指标层里已删节点的历史会挂到同 id 的新节点上且无法肉眼分辨。
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  sort_order INTEGER NOT NULL DEFAULT 0,
  public INTEGER NOT NULL DEFAULT 0,
  note TEXT NOT NULL DEFAULT '',
  -- NULL 表示"用默认值（TTL）"，是缺省不是放宽；读侧遇 NULL 必须取 TTL。
  offline_grace_s INTEGER,
  traffic_reset_day INTEGER NOT NULL DEFAULT 1,
  token_hash BLOB NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  -- 墙钟，只供展示与告警文案，不参与离线时长计算。
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
	`CREATE TABLE admin (
  -- 单管理员：CHECK 让第二行无法插入，"多用户"在 schema 上就不成立。
  id INTEGER PRIMARY KEY CHECK (id = 1),
  -- PHC 字符串，argon2id 的参数随哈希走：改参数不需要迁移，旧哈希按自带参数校验。
  password_hash TEXT NOT NULL,
  updated_at INTEGER NOT NULL
)`,
	`CREATE TABLE admin_session (
  token_hash BLOB PRIMARY KEY,
  created_at INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL,
  -- 绝对过期，墙钟 Unix 秒。会话要跨 hub 重启存活，只能用墙钟；
  -- 墙钟回拨会推迟按绝对过期时刻判定失效的时间。
  expires_at INTEGER NOT NULL
)`,
	`CREATE TABLE traffic (
  node_id INTEGER PRIMARY KEY,
  boot_id TEXT NOT NULL,
  last_rx INTEGER NOT NULL, -- -1 表示尚无基线，与 traffic.NoBaseline 同值；由校正建立的条目才有。
  last_tx INTEGER NOT NULL, -- -1 的含义与 last_rx 相同。
  total_rx INTEGER NOT NULL,
  total_tx INTEGER NOT NULL,
  period_rx INTEGER NOT NULL,
  period_tx INTEGER NOT NULL,
  -- 当前周期起点，Unix 秒；重置日零点按 hub 时区换算。
  period_start INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
)`,
	`CREATE TABLE metric_1m (node_id INTEGER NOT NULL, ts INTEGER NOT NULL, cpu_sum REAL NOT NULL DEFAULT 0, cpu_n INTEGER NOT NULL DEFAULT 0, cpu_max REAL NOT NULL DEFAULT 0, mem_used_sum INTEGER NOT NULL DEFAULT 0, mem_used_n INTEGER NOT NULL DEFAULT 0, mem_used_max INTEGER NOT NULL DEFAULT 0, swap_used_sum INTEGER NOT NULL DEFAULT 0, swap_used_n INTEGER NOT NULL DEFAULT 0, disk_used_sum INTEGER NOT NULL DEFAULT 0, disk_used_n INTEGER NOT NULL DEFAULT 0, load1_sum REAL NOT NULL DEFAULT 0, load1_n INTEGER NOT NULL DEFAULT 0, tcp_sum INTEGER NOT NULL DEFAULT 0, tcp_n INTEGER NOT NULL DEFAULT 0, udp_sum INTEGER NOT NULL DEFAULT 0, udp_n INTEGER NOT NULL DEFAULT 0, procs_sum INTEGER NOT NULL DEFAULT 0, procs_n INTEGER NOT NULL DEFAULT 0, rx_bytes_sum INTEGER NOT NULL DEFAULT 0, rx_bytes_n INTEGER NOT NULL DEFAULT 0, tx_bytes_sum INTEGER NOT NULL DEFAULT 0, tx_bytes_n INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (node_id, ts)) WITHOUT ROWID`,
	`CREATE TABLE metric_5m (node_id INTEGER NOT NULL, ts INTEGER NOT NULL, cpu_sum REAL NOT NULL DEFAULT 0, cpu_n INTEGER NOT NULL DEFAULT 0, cpu_max REAL NOT NULL DEFAULT 0, mem_used_sum INTEGER NOT NULL DEFAULT 0, mem_used_n INTEGER NOT NULL DEFAULT 0, mem_used_max INTEGER NOT NULL DEFAULT 0, swap_used_sum INTEGER NOT NULL DEFAULT 0, swap_used_n INTEGER NOT NULL DEFAULT 0, disk_used_sum INTEGER NOT NULL DEFAULT 0, disk_used_n INTEGER NOT NULL DEFAULT 0, load1_sum REAL NOT NULL DEFAULT 0, load1_n INTEGER NOT NULL DEFAULT 0, tcp_sum INTEGER NOT NULL DEFAULT 0, tcp_n INTEGER NOT NULL DEFAULT 0, udp_sum INTEGER NOT NULL DEFAULT 0, udp_n INTEGER NOT NULL DEFAULT 0, procs_sum INTEGER NOT NULL DEFAULT 0, procs_n INTEGER NOT NULL DEFAULT 0, rx_bytes_sum INTEGER NOT NULL DEFAULT 0, rx_bytes_n INTEGER NOT NULL DEFAULT 0, tx_bytes_sum INTEGER NOT NULL DEFAULT 0, tx_bytes_n INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (node_id, ts)) WITHOUT ROWID`,
	`CREATE TABLE metric_1h (node_id INTEGER NOT NULL, ts INTEGER NOT NULL, cpu_sum REAL NOT NULL DEFAULT 0, cpu_n INTEGER NOT NULL DEFAULT 0, cpu_max REAL NOT NULL DEFAULT 0, mem_used_sum INTEGER NOT NULL DEFAULT 0, mem_used_n INTEGER NOT NULL DEFAULT 0, mem_used_max INTEGER NOT NULL DEFAULT 0, swap_used_sum INTEGER NOT NULL DEFAULT 0, swap_used_n INTEGER NOT NULL DEFAULT 0, disk_used_sum INTEGER NOT NULL DEFAULT 0, disk_used_n INTEGER NOT NULL DEFAULT 0, load1_sum REAL NOT NULL DEFAULT 0, load1_n INTEGER NOT NULL DEFAULT 0, tcp_sum INTEGER NOT NULL DEFAULT 0, tcp_n INTEGER NOT NULL DEFAULT 0, udp_sum INTEGER NOT NULL DEFAULT 0, udp_n INTEGER NOT NULL DEFAULT 0, procs_sum INTEGER NOT NULL DEFAULT 0, procs_n INTEGER NOT NULL DEFAULT 0, rx_bytes_sum INTEGER NOT NULL DEFAULT 0, rx_bytes_n INTEGER NOT NULL DEFAULT 0, tx_bytes_sum INTEGER NOT NULL DEFAULT 0, tx_bytes_n INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (node_id, ts)) WITHOUT ROWID`,
}

func TestMigrationFromV3MatchesFreshSchemaAndKeepsRows(t *testing.T) {
	migrated, fresh := migrateFrom(t, schemaV3, 3, seedMinuteRow)
	if got, want := describe(t, migrated.r), describe(t, fresh.r); !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated schema differs from fresh schema:\n got: %+v\nwant: %+v", got, want)
	}
	if v := userVersion(t, migrated.r); v != schemaVersion {
		t.Fatalf("user_version = %d, want %d", v, schemaVersion)
	}
	rows, err := migrated.ReadMinuteRows(t.Context(), 7, 0, 120)
	if err != nil || len(rows) != 1 || rows[0].Bucket.Sum[0] != 50 || rows[0].Bucket.N[0] != 1 {
		t.Fatalf("minute row lost across migration: %v %v", rows, err)
	}
	for _, level := range []string{"probe_5m", "probe_1h"} {
		if got := watermark(t, migrated, level); got != 0 {
			t.Fatalf("%s watermark = %d, want 0", level, got)
		}
	}
	var version int
	if err := migrated.r.QueryRow("SELECT version FROM probe_meta WHERE id = 1").Scan(&version); err != nil || version != 0 {
		t.Fatalf("probe_meta version = %d, err = %v, want 0", version, err)
	}
}

// schemaV4 固定旧库结构，生产 DDL 的变化不能同时改掉迁移的输入。
var schemaV4 = []string{
	`CREATE TABLE node (
  -- AUTOINCREMENT 使 id 永不复用：分层备份恢复后两层可能各自漂移，
  -- id 若复用，指标层里已删节点的历史会挂到同 id 的新节点上且无法肉眼分辨。
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  sort_order INTEGER NOT NULL DEFAULT 0,
  public INTEGER NOT NULL DEFAULT 0,
  note TEXT NOT NULL DEFAULT '',
  -- NULL 表示"用默认值（TTL）"，是缺省不是放宽；读侧遇 NULL 必须取 TTL。
  offline_grace_s INTEGER,
  traffic_reset_day INTEGER NOT NULL DEFAULT 1,
  token_hash BLOB NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  -- 墙钟，只供展示与告警文案，不参与离线时长计算。
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
	`CREATE TABLE admin (
  -- 单管理员：CHECK 让第二行无法插入，"多用户"在 schema 上就不成立。
  id INTEGER PRIMARY KEY CHECK (id = 1),
  -- PHC 字符串，argon2id 的参数随哈希走：改参数不需要迁移，旧哈希按自带参数校验。
  password_hash TEXT NOT NULL,
  updated_at INTEGER NOT NULL
)`,
	`CREATE TABLE admin_session (
  token_hash BLOB PRIMARY KEY,
  created_at INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL,
  -- 绝对过期，墙钟 Unix 秒。会话要跨 hub 重启存活，只能用墙钟；
  -- 墙钟回拨会推迟按绝对过期时刻判定失效的时间。
  expires_at INTEGER NOT NULL
)`,
	`CREATE TABLE traffic (
  node_id INTEGER PRIMARY KEY,
  boot_id TEXT NOT NULL,
  last_rx INTEGER NOT NULL, -- -1 表示尚无基线，与 traffic.NoBaseline 同值；由校正建立的条目才有。
  last_tx INTEGER NOT NULL, -- -1 的含义与 last_rx 相同。
  total_rx INTEGER NOT NULL,
  total_tx INTEGER NOT NULL,
  period_rx INTEGER NOT NULL,
  period_tx INTEGER NOT NULL,
  -- 当前周期起点，Unix 秒；重置日零点按 hub 时区换算。
  period_start INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
)`,
	`CREATE TABLE metric_1m (node_id INTEGER NOT NULL, ts INTEGER NOT NULL, cpu_sum REAL NOT NULL DEFAULT 0, cpu_n INTEGER NOT NULL DEFAULT 0, cpu_max REAL NOT NULL DEFAULT 0, mem_used_sum INTEGER NOT NULL DEFAULT 0, mem_used_n INTEGER NOT NULL DEFAULT 0, mem_used_max INTEGER NOT NULL DEFAULT 0, swap_used_sum INTEGER NOT NULL DEFAULT 0, swap_used_n INTEGER NOT NULL DEFAULT 0, disk_used_sum INTEGER NOT NULL DEFAULT 0, disk_used_n INTEGER NOT NULL DEFAULT 0, load1_sum REAL NOT NULL DEFAULT 0, load1_n INTEGER NOT NULL DEFAULT 0, tcp_sum INTEGER NOT NULL DEFAULT 0, tcp_n INTEGER NOT NULL DEFAULT 0, udp_sum INTEGER NOT NULL DEFAULT 0, udp_n INTEGER NOT NULL DEFAULT 0, procs_sum INTEGER NOT NULL DEFAULT 0, procs_n INTEGER NOT NULL DEFAULT 0, rx_bytes_sum INTEGER NOT NULL DEFAULT 0, rx_bytes_n INTEGER NOT NULL DEFAULT 0, tx_bytes_sum INTEGER NOT NULL DEFAULT 0, tx_bytes_n INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (node_id, ts)) WITHOUT ROWID`,
	`CREATE TABLE metric_5m (node_id INTEGER NOT NULL, ts INTEGER NOT NULL, cpu_sum REAL NOT NULL DEFAULT 0, cpu_n INTEGER NOT NULL DEFAULT 0, cpu_max REAL NOT NULL DEFAULT 0, mem_used_sum INTEGER NOT NULL DEFAULT 0, mem_used_n INTEGER NOT NULL DEFAULT 0, mem_used_max INTEGER NOT NULL DEFAULT 0, swap_used_sum INTEGER NOT NULL DEFAULT 0, swap_used_n INTEGER NOT NULL DEFAULT 0, disk_used_sum INTEGER NOT NULL DEFAULT 0, disk_used_n INTEGER NOT NULL DEFAULT 0, load1_sum REAL NOT NULL DEFAULT 0, load1_n INTEGER NOT NULL DEFAULT 0, tcp_sum INTEGER NOT NULL DEFAULT 0, tcp_n INTEGER NOT NULL DEFAULT 0, udp_sum INTEGER NOT NULL DEFAULT 0, udp_n INTEGER NOT NULL DEFAULT 0, procs_sum INTEGER NOT NULL DEFAULT 0, procs_n INTEGER NOT NULL DEFAULT 0, rx_bytes_sum INTEGER NOT NULL DEFAULT 0, rx_bytes_n INTEGER NOT NULL DEFAULT 0, tx_bytes_sum INTEGER NOT NULL DEFAULT 0, tx_bytes_n INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (node_id, ts)) WITHOUT ROWID`,
	`CREATE TABLE metric_1h (node_id INTEGER NOT NULL, ts INTEGER NOT NULL, cpu_sum REAL NOT NULL DEFAULT 0, cpu_n INTEGER NOT NULL DEFAULT 0, cpu_max REAL NOT NULL DEFAULT 0, mem_used_sum INTEGER NOT NULL DEFAULT 0, mem_used_n INTEGER NOT NULL DEFAULT 0, mem_used_max INTEGER NOT NULL DEFAULT 0, swap_used_sum INTEGER NOT NULL DEFAULT 0, swap_used_n INTEGER NOT NULL DEFAULT 0, disk_used_sum INTEGER NOT NULL DEFAULT 0, disk_used_n INTEGER NOT NULL DEFAULT 0, load1_sum REAL NOT NULL DEFAULT 0, load1_n INTEGER NOT NULL DEFAULT 0, tcp_sum INTEGER NOT NULL DEFAULT 0, tcp_n INTEGER NOT NULL DEFAULT 0, udp_sum INTEGER NOT NULL DEFAULT 0, udp_n INTEGER NOT NULL DEFAULT 0, procs_sum INTEGER NOT NULL DEFAULT 0, procs_n INTEGER NOT NULL DEFAULT 0, rx_bytes_sum INTEGER NOT NULL DEFAULT 0, rx_bytes_n INTEGER NOT NULL DEFAULT 0, tx_bytes_sum INTEGER NOT NULL DEFAULT 0, tx_bytes_n INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (node_id, ts)) WITHOUT ROWID`,
	`CREATE TABLE probe_task (
  -- AUTOINCREMENT：历史行只带 task_id，删除任务后 id 若复用，旧历史会挂到新任务上。
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  kind INTEGER NOT NULL,
  target TEXT NOT NULL,
  interval_s INTEGER NOT NULL,
  timeout_ms INTEGER NOT NULL,
  created_at INTEGER NOT NULL
)`,
	`CREATE TABLE probe_task_node (
  task_id INTEGER NOT NULL,
  node_id INTEGER NOT NULL,
  PRIMARY KEY (task_id, node_id)
) WITHOUT ROWID`,
	`CREATE INDEX probe_task_node_by_node ON probe_task_node (node_id)`,
	`CREATE TABLE probe_meta (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  version INTEGER NOT NULL
)`,
	`INSERT INTO probe_meta (id, version) VALUES (1, 0)`,
	`INSERT INTO rollup_state (level, upto_ts) VALUES ('probe_5m', 0), ('probe_1h', 0)`,
	`CREATE TABLE probe_1m (
  node_id INTEGER NOT NULL, ts INTEGER NOT NULL, task_id INTEGER NOT NULL,
  sent INTEGER NOT NULL DEFAULT 0, lost INTEGER NOT NULL DEFAULT 0, errors INTEGER NOT NULL DEFAULT 0,
  rtt_sum_us INTEGER NOT NULL DEFAULT 0, rtt_min_us INTEGER, rtt_max_us INTEGER,
  PRIMARY KEY (node_id, ts, task_id)
) WITHOUT ROWID`,
	`CREATE TABLE probe_5m (
  node_id INTEGER NOT NULL, ts INTEGER NOT NULL, task_id INTEGER NOT NULL,
  sent INTEGER NOT NULL DEFAULT 0, lost INTEGER NOT NULL DEFAULT 0, errors INTEGER NOT NULL DEFAULT 0,
  rtt_sum_us INTEGER NOT NULL DEFAULT 0, rtt_min_us INTEGER, rtt_max_us INTEGER,
  PRIMARY KEY (node_id, ts, task_id)
) WITHOUT ROWID`,
	`CREATE TABLE probe_1h (
  node_id INTEGER NOT NULL, ts INTEGER NOT NULL, task_id INTEGER NOT NULL,
  sent INTEGER NOT NULL DEFAULT 0, lost INTEGER NOT NULL DEFAULT 0, errors INTEGER NOT NULL DEFAULT 0,
  rtt_sum_us INTEGER NOT NULL DEFAULT 0, rtt_min_us INTEGER, rtt_max_us INTEGER,
  PRIMARY KEY (node_id, ts, task_id)
) WITHOUT ROWID`,
}

func TestMigrationFromV4MatchesFreshSchemaAndKeepsRows(t *testing.T) {
	migrated, fresh := migrateFrom(t, schemaV4, 4, seedMinuteRow)
	if got, want := describe(t, migrated.r), describe(t, fresh.r); !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated schema differs from fresh schema:\n got: %+v\nwant: %+v", got, want)
	}
	if v := userVersion(t, migrated.r); v != schemaVersion {
		t.Fatalf("user_version = %d, want %d", v, schemaVersion)
	}
	rows, err := migrated.ReadMinuteRows(t.Context(), 7, 0, 120)
	if err != nil || len(rows) != 1 || rows[0].Bucket.Sum[0] != 50 || rows[0].Bucket.N[0] != 1 {
		t.Fatalf("minute row lost across migration: %v %v", rows, err)
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
