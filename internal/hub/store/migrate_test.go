package store

import (
	"database/sql"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/metric"
	"github.com/xjetry/probe/internal/sqlitetest"
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

	migrated, err = Open(old, clk, slog.Default(), MigrateSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { migrated.Close() })
	fresh, err = Open(filepath.Join(dir, "fresh.db"), clk, slog.Default(), MigrateSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fresh.Close() })

	return migrated, fresh
}

func TestMigrationFromV1MatchesFreshSchema(t *testing.T) {
	migrated, fresh := migrateFrom(t, schemaV1, 1, seedMinuteRow)

	if got, want := sqlitetest.Describe(t, migrated.r), sqlitetest.Describe(t, fresh.r); !reflect.DeepEqual(got, want) {
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
	for _, policy := range []SchemaPolicy{MigrateSchema, RequireCurrentSchema} {
		if st, err := Open(path, clock.NewFake(time.Unix(0, 0)), slog.Default(), policy); err == nil {
			st.Close()
			t.Errorf("policy %d opened a database written by a newer binary", policy)
		}
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
	if got, want := sqlitetest.Describe(t, migrated.r), sqlitetest.Describe(t, fresh.r); !reflect.DeepEqual(got, want) {
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
	if got, want := sqlitetest.Describe(t, migrated.r), sqlitetest.Describe(t, fresh.r); !reflect.DeepEqual(got, want) {
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

func TestAlertMigrationRejectsExistingObjects(t *testing.T) {
	// 对象清单取自迁移 5 实际执行的冻结语句；手工维护会漏掉新增对象的同名冲突断言。
	// 每条语句必须生成一个对象，解析失败直接终止，不能静默缩小覆盖范围。
	statements := alertStatementsV5
	objects := make([]struct{ kind, name string }, len(statements))
	createObject := regexp.MustCompile(`^CREATE\s+(TABLE|INDEX)\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)\b`)
	for i, statement := range statements {
		match := createObject.FindStringSubmatch(statement)
		if match == nil {
			t.Fatalf("cannot parse alert schema statement %d: %q", i, statement)
		}
		objects[i].kind, objects[i].name = match[1], match[2]
	}
	for _, object := range objects {
		kind, name := object.kind, object.name
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "conflict.db")
			db, err := sql.Open("sqlite", dsn(path, ""))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, ddl := range schemaV4 {
				if _, err := db.Exec(ddl); err != nil {
					t.Fatal(err)
				}
			}
			conflict := "CREATE TABLE " + name + " (wrong INTEGER, id INTEGER, rule_id INTEGER, node_id INTEGER, channel_id INTEGER, event_id INTEGER, ok INTEGER, attempts INTEGER)"
			if kind == "INDEX" {
				conflict = "CREATE INDEX " + name + " ON node(id)"
			}
			if _, err := db.Exec(conflict); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("PRAGMA user_version = 4"); err != nil {
				t.Fatal(err)
			}
			st, err := Open(path, clock.NewFake(time.Unix(1, 0)), slog.Default(), MigrateSchema)
			if st != nil {
				st.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "already exists") {
				t.Fatalf("conflicting %s accepted: %v", name, err)
			}
			if got := userVersion(t, db); got != 4 {
				t.Fatalf("failed migration advanced version: %d", got)
			}
		})
	}
}

func TestMigrationFromV2MatchesFreshSchemaAndKeepsRows(t *testing.T) {
	migrated, fresh := migrateFrom(t, schemaV2, 2, seedMinuteRow)
	if got, want := sqlitetest.Describe(t, migrated.r), sqlitetest.Describe(t, fresh.r); !reflect.DeepEqual(got, want) {
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

// schemaV5 冻结加入 api_token 之前的完整 DDL：生产常量以后会变，迁移的输入不能跟着变。
var schemaV5 = []string{
	"CREATE TABLE node (\n  -- AUTOINCREMENT 使 id 永不复用：分层备份恢复后两层可能各自漂移，\n  -- id 若复用，指标层里已删节点的历史会挂到同 id 的新节点上且无法肉眼分辨。\n  id INTEGER PRIMARY KEY AUTOINCREMENT,\n  name TEXT NOT NULL,\n  sort_order INTEGER NOT NULL DEFAULT 0,\n  public INTEGER NOT NULL DEFAULT 0,\n  note TEXT NOT NULL DEFAULT '',\n  -- NULL 表示\"用默认值（TTL）\"，是缺省不是放宽；读侧遇 NULL 必须取 TTL。\n  offline_grace_s INTEGER,\n  traffic_reset_day INTEGER NOT NULL DEFAULT 1,\n  token_hash BLOB NOT NULL UNIQUE,\n  created_at INTEGER NOT NULL,\n  -- 墙钟，只供展示与告警文案，不参与离线时长计算。\n  last_seen_at INTEGER\n)",
	"CREATE TABLE node_facts (\n  node_id INTEGER PRIMARY KEY,\n  facts_hash INTEGER NOT NULL,\n  hostname TEXT NOT NULL,\n  os TEXT NOT NULL,\n  kernel TEXT NOT NULL,\n  arch TEXT NOT NULL,\n  virtualization TEXT NOT NULL,\n  cpu_model TEXT NOT NULL,\n  cpu_cores INTEGER NOT NULL,\n  agent_version TEXT NOT NULL,\n  icmp_available INTEGER NOT NULL,\n  updated_at INTEGER NOT NULL\n)",
	"CREATE TABLE register_window (\n  id INTEGER PRIMARY KEY CHECK (id = 1),\n  key_hash BLOB NOT NULL,\n  expires_at INTEGER NOT NULL,\n  remaining INTEGER NOT NULL\n)",
	"CREATE TABLE rollup_state (\n  level TEXT PRIMARY KEY,\n  upto_ts INTEGER NOT NULL\n)",
	"INSERT INTO rollup_state (level, upto_ts) VALUES ('5m', 0), ('1h', 0)",
	"INSERT INTO rollup_state (level, upto_ts) VALUES ('probe_5m', 0), ('probe_1h', 0)",
	"CREATE TABLE admin (\n  -- 单管理员：CHECK 让第二行无法插入，\"多用户\"在 schema 上就不成立。\n  id INTEGER PRIMARY KEY CHECK (id = 1),\n  -- PHC 字符串，argon2id 的参数随哈希走：改参数不需要迁移，旧哈希按自带参数校验。\n  password_hash TEXT NOT NULL,\n  updated_at INTEGER NOT NULL\n)",
	"CREATE TABLE admin_session (\n  token_hash BLOB PRIMARY KEY,\n  created_at INTEGER NOT NULL,\n  last_used_at INTEGER NOT NULL,\n  -- 绝对过期，墙钟 Unix 秒。会话要跨 hub 重启存活，只能用墙钟；\n  -- 墙钟回拨会推迟按绝对过期时刻判定失效的时间。\n  expires_at INTEGER NOT NULL\n)",
	"CREATE TABLE traffic (\n  node_id INTEGER PRIMARY KEY,\n  boot_id TEXT NOT NULL,\n  last_rx INTEGER NOT NULL, -- -1 表示尚无基线，与 traffic.NoBaseline 同值；由校正建立的条目才有。\n  last_tx INTEGER NOT NULL, -- -1 的含义与 last_rx 相同。\n  total_rx INTEGER NOT NULL,\n  total_tx INTEGER NOT NULL,\n  period_rx INTEGER NOT NULL,\n  period_tx INTEGER NOT NULL,\n  -- 当前周期起点，Unix 秒；重置日零点按 hub 时区换算。\n  period_start INTEGER NOT NULL,\n  updated_at INTEGER NOT NULL\n)",
	"CREATE TABLE probe_task (\n  -- AUTOINCREMENT：历史行只带 task_id，删除任务后 id 若复用，旧历史会挂到新任务上。\n  id INTEGER PRIMARY KEY AUTOINCREMENT,\n  kind INTEGER NOT NULL,\n  target TEXT NOT NULL,\n  interval_s INTEGER NOT NULL,\n  timeout_ms INTEGER NOT NULL,\n  created_at INTEGER NOT NULL\n)",
	"CREATE TABLE probe_task_node (\n  task_id INTEGER NOT NULL,\n  node_id INTEGER NOT NULL,\n  PRIMARY KEY (task_id, node_id)\n) WITHOUT ROWID",
	"CREATE INDEX probe_task_node_by_node ON probe_task_node (node_id)",
	"CREATE TABLE probe_meta (\n  id INTEGER PRIMARY KEY CHECK (id = 1),\n  version INTEGER NOT NULL\n)",
	"INSERT INTO probe_meta (id, version) VALUES (1, 0)",
	"CREATE TABLE metric_1m (node_id INTEGER NOT NULL, ts INTEGER NOT NULL, cpu_sum REAL NOT NULL DEFAULT 0, cpu_n INTEGER NOT NULL DEFAULT 0, cpu_max REAL NOT NULL DEFAULT 0, mem_used_sum INTEGER NOT NULL DEFAULT 0, mem_used_n INTEGER NOT NULL DEFAULT 0, mem_used_max INTEGER NOT NULL DEFAULT 0, swap_used_sum INTEGER NOT NULL DEFAULT 0, swap_used_n INTEGER NOT NULL DEFAULT 0, disk_used_sum INTEGER NOT NULL DEFAULT 0, disk_used_n INTEGER NOT NULL DEFAULT 0, load1_sum REAL NOT NULL DEFAULT 0, load1_n INTEGER NOT NULL DEFAULT 0, tcp_sum INTEGER NOT NULL DEFAULT 0, tcp_n INTEGER NOT NULL DEFAULT 0, udp_sum INTEGER NOT NULL DEFAULT 0, udp_n INTEGER NOT NULL DEFAULT 0, procs_sum INTEGER NOT NULL DEFAULT 0, procs_n INTEGER NOT NULL DEFAULT 0, rx_bytes_sum INTEGER NOT NULL DEFAULT 0, rx_bytes_n INTEGER NOT NULL DEFAULT 0, tx_bytes_sum INTEGER NOT NULL DEFAULT 0, tx_bytes_n INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (node_id, ts)) WITHOUT ROWID",
	"CREATE TABLE metric_5m (node_id INTEGER NOT NULL, ts INTEGER NOT NULL, cpu_sum REAL NOT NULL DEFAULT 0, cpu_n INTEGER NOT NULL DEFAULT 0, cpu_max REAL NOT NULL DEFAULT 0, mem_used_sum INTEGER NOT NULL DEFAULT 0, mem_used_n INTEGER NOT NULL DEFAULT 0, mem_used_max INTEGER NOT NULL DEFAULT 0, swap_used_sum INTEGER NOT NULL DEFAULT 0, swap_used_n INTEGER NOT NULL DEFAULT 0, disk_used_sum INTEGER NOT NULL DEFAULT 0, disk_used_n INTEGER NOT NULL DEFAULT 0, load1_sum REAL NOT NULL DEFAULT 0, load1_n INTEGER NOT NULL DEFAULT 0, tcp_sum INTEGER NOT NULL DEFAULT 0, tcp_n INTEGER NOT NULL DEFAULT 0, udp_sum INTEGER NOT NULL DEFAULT 0, udp_n INTEGER NOT NULL DEFAULT 0, procs_sum INTEGER NOT NULL DEFAULT 0, procs_n INTEGER NOT NULL DEFAULT 0, rx_bytes_sum INTEGER NOT NULL DEFAULT 0, rx_bytes_n INTEGER NOT NULL DEFAULT 0, tx_bytes_sum INTEGER NOT NULL DEFAULT 0, tx_bytes_n INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (node_id, ts)) WITHOUT ROWID",
	"CREATE TABLE metric_1h (node_id INTEGER NOT NULL, ts INTEGER NOT NULL, cpu_sum REAL NOT NULL DEFAULT 0, cpu_n INTEGER NOT NULL DEFAULT 0, cpu_max REAL NOT NULL DEFAULT 0, mem_used_sum INTEGER NOT NULL DEFAULT 0, mem_used_n INTEGER NOT NULL DEFAULT 0, mem_used_max INTEGER NOT NULL DEFAULT 0, swap_used_sum INTEGER NOT NULL DEFAULT 0, swap_used_n INTEGER NOT NULL DEFAULT 0, disk_used_sum INTEGER NOT NULL DEFAULT 0, disk_used_n INTEGER NOT NULL DEFAULT 0, load1_sum REAL NOT NULL DEFAULT 0, load1_n INTEGER NOT NULL DEFAULT 0, tcp_sum INTEGER NOT NULL DEFAULT 0, tcp_n INTEGER NOT NULL DEFAULT 0, udp_sum INTEGER NOT NULL DEFAULT 0, udp_n INTEGER NOT NULL DEFAULT 0, procs_sum INTEGER NOT NULL DEFAULT 0, procs_n INTEGER NOT NULL DEFAULT 0, rx_bytes_sum INTEGER NOT NULL DEFAULT 0, rx_bytes_n INTEGER NOT NULL DEFAULT 0, tx_bytes_sum INTEGER NOT NULL DEFAULT 0, tx_bytes_n INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (node_id, ts)) WITHOUT ROWID",
	"CREATE TABLE probe_1m (\n  node_id INTEGER NOT NULL, ts INTEGER NOT NULL, task_id INTEGER NOT NULL,\n  sent INTEGER NOT NULL DEFAULT 0, lost INTEGER NOT NULL DEFAULT 0, errors INTEGER NOT NULL DEFAULT 0,\n  rtt_sum_us INTEGER NOT NULL DEFAULT 0, rtt_min_us INTEGER, rtt_max_us INTEGER,\n  PRIMARY KEY (node_id, ts, task_id)\n) WITHOUT ROWID",
	"CREATE TABLE probe_5m (\n  node_id INTEGER NOT NULL, ts INTEGER NOT NULL, task_id INTEGER NOT NULL,\n  sent INTEGER NOT NULL DEFAULT 0, lost INTEGER NOT NULL DEFAULT 0, errors INTEGER NOT NULL DEFAULT 0,\n  rtt_sum_us INTEGER NOT NULL DEFAULT 0, rtt_min_us INTEGER, rtt_max_us INTEGER,\n  PRIMARY KEY (node_id, ts, task_id)\n) WITHOUT ROWID",
	"CREATE TABLE probe_1h (\n  node_id INTEGER NOT NULL, ts INTEGER NOT NULL, task_id INTEGER NOT NULL,\n  sent INTEGER NOT NULL DEFAULT 0, lost INTEGER NOT NULL DEFAULT 0, errors INTEGER NOT NULL DEFAULT 0,\n  rtt_sum_us INTEGER NOT NULL DEFAULT 0, rtt_min_us INTEGER, rtt_max_us INTEGER,\n  PRIMARY KEY (node_id, ts, task_id)\n) WITHOUT ROWID",
	"CREATE TABLE alert_rule (\n  id INTEGER PRIMARY KEY AUTOINCREMENT,\n  name TEXT NOT NULL,\n  kind TEXT NOT NULL,\n  enabled INTEGER NOT NULL DEFAULT 1,\n  all_nodes INTEGER NOT NULL DEFAULT 0,\n  task_id INTEGER,\n  metric TEXT,\n  threshold REAL,\n  for_minutes INTEGER,\n  created_at INTEGER NOT NULL\n)",
	"CREATE TABLE alert_rule_node (\n  rule_id INTEGER NOT NULL,\n  node_id INTEGER NOT NULL,\n  PRIMARY KEY (rule_id, node_id)\n)",
	"CREATE INDEX alert_rule_node_by_node ON alert_rule_node(node_id)",
	"CREATE TABLE alert_rule_channel (\n  rule_id INTEGER NOT NULL,\n  channel_id INTEGER NOT NULL,\n  PRIMARY KEY (rule_id, channel_id)\n)",
	"CREATE INDEX alert_rule_channel_by_channel ON alert_rule_channel(channel_id)",
	"CREATE TABLE notify_channel (\n  id INTEGER PRIMARY KEY AUTOINCREMENT,\n  name TEXT NOT NULL,\n  kind TEXT NOT NULL,\n  config TEXT NOT NULL,\n  created_at INTEGER NOT NULL\n)",
	"CREATE TABLE alert_state (\n  rule_id INTEGER NOT NULL,\n  node_id INTEGER NOT NULL,\n  state TEXT NOT NULL,\n  since_at INTEGER NOT NULL,\n  PRIMARY KEY (rule_id, node_id)\n)",
	"CREATE TABLE alert_event (\n  id INTEGER PRIMARY KEY AUTOINCREMENT,\n  rule_id INTEGER NOT NULL,\n  node_id INTEGER NOT NULL,\n  transition TEXT NOT NULL,\n  at INTEGER NOT NULL,\n  summary TEXT NOT NULL,\n  value REAL NOT NULL\n)",
	"CREATE INDEX alert_event_by_node ON alert_event(node_id, id)",
	"CREATE INDEX alert_event_by_at ON alert_event(at)",
	"CREATE TABLE alert_delivery (\n  id INTEGER PRIMARY KEY AUTOINCREMENT,\n  event_id INTEGER NOT NULL,\n  channel_id INTEGER NOT NULL,\n  attempts INTEGER NOT NULL DEFAULT 0,\n  ok INTEGER NOT NULL DEFAULT 0,\n  done INTEGER NOT NULL DEFAULT 0,\n  last_error TEXT NOT NULL DEFAULT '',\n  delivered_at INTEGER\n)",
	"CREATE INDEX alert_delivery_by_event ON alert_delivery(event_id)",
	"CREATE INDEX alert_delivery_pending ON alert_delivery(done, id)",
}

func TestMigrationFromV5MatchesFreshSchemaAndKeepsRows(t *testing.T) {
	migrated, fresh := migrateFrom(t, schemaV5, 5, seedMinuteRow)
	if got, want := sqlitetest.Describe(t, migrated.r), sqlitetest.Describe(t, fresh.r); !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated schema differs from fresh schema:\n got: %+v\nwant: %+v", got, want)
	}
	if v := userVersion(t, migrated.r); v != schemaVersion {
		t.Fatalf("user_version = %d, want %d", v, schemaVersion)
	}
	rows, err := migrated.ReadMinuteRows(t.Context(), 7, 0, 120)
	if err != nil || len(rows) != 1 {
		t.Fatalf("minute row lost across migration: %v %v", rows, err)
	}
}

// 迁移只引用冻结的 DDL：schema.go 里的顶层名字（当前 DDL 与生成函数）一个都不能出现在 migrations.go。
func TestMigrationsReferenceOnlyFrozenDDL(t *testing.T) {
	fset := token.NewFileSet()
	schema, err := parser.ParseFile(fset, "schema.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	current := map[string]bool{}
	for name := range schema.Scope.Objects {
		current[name] = true
	}
	if !current["ddlAlertDelivery"] || !current["metricDDL"] {
		t.Fatalf("schema.go declarations not found: %v", current)
	}
	migrations, err := parser.ParseFile(fset, "migrations.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(migrations, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && current[id.Name] {
			t.Errorf("%s: migrations reference current DDL %s", fset.Position(id.Pos()), id.Name)
		}
		return true
	})
}
