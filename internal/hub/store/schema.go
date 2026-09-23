package store

import (
	"strings"

	"github.com/xjetry/probe/internal/hub/metric"
)

// 每张表一个常量：全新建库与增量迁移复用同一段 DDL，不存在第二份字段清单。
const ddlNode = `CREATE TABLE node (
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
)`

const ddlNodeFacts = `CREATE TABLE node_facts (
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
)`

const ddlRegisterWindow = `CREATE TABLE register_window (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  key_hash BLOB NOT NULL,
  expires_at INTEGER NOT NULL,
  remaining INTEGER NOT NULL
)`

const ddlRollupState = `CREATE TABLE rollup_state (
  level TEXT PRIMARY KEY,
  upto_ts INTEGER NOT NULL
)`

const seedRollupState = `INSERT INTO rollup_state (level, upto_ts) VALUES ('5m', 0), ('1h', 0)`

const ddlAdmin = `CREATE TABLE admin (
  -- 单管理员：CHECK 让第二行无法插入，"多用户"在 schema 上就不成立。
  id INTEGER PRIMARY KEY CHECK (id = 1),
  -- PHC 字符串，argon2id 的参数随哈希走：改参数不需要迁移，旧哈希按自带参数校验。
  password_hash TEXT NOT NULL,
  updated_at INTEGER NOT NULL
)`

const ddlAdminSession = `CREATE TABLE admin_session (
  token_hash BLOB PRIMARY KEY,
  created_at INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL,
  -- 绝对过期，墙钟 Unix 秒。会话要跨 hub 重启存活，只能用墙钟；
  -- 墙钟回拨会推迟按绝对过期时刻判定失效的时间。
  expires_at INTEGER NOT NULL
)`

// traffic 是 §7 累加器的持久化形态：基线（boot_id、last_*）与累计值同一行、同一事务落盘，
// 崩溃后首次上报相对已落盘基线做差分恰好补上内存里丢失的增量。
const ddlTraffic = `CREATE TABLE traffic (
  node_id INTEGER PRIMARY KEY,
  boot_id TEXT NOT NULL,
  last_rx INTEGER NOT NULL,
  last_tx INTEGER NOT NULL,
  total_rx INTEGER NOT NULL,
  total_tx INTEGER NOT NULL,
  period_rx INTEGER NOT NULL,
  period_tx INTEGER NOT NULL,
  -- 当前周期起点，Unix 秒；重置日零点按 hub 时区换算。
  period_start INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
)`

// metricTables 按级别从细到粗；建库、DeleteNode 与 Counts 共用此清单，
// 避免新增级别后遗漏删除或计数；已有库仍需对应的增量迁移。
var metricTables = []string{"metric_1m", "metric_5m", "metric_1h"}

// schemaStatements 是当前版本的完整 DDL：空库直接建到当前版本，不重放历史。
func schemaStatements() []string {
	out := []string{ddlNode, ddlNodeFacts, ddlRegisterWindow, ddlRollupState, seedRollupState, ddlAdmin, ddlAdminSession, ddlTraffic}
	for _, t := range metricTables {
		out = append(out, metricDDL(t))
	}
	return out
}

// metricDDL 从描述表生成分钟表。主键顺序 (node_id, ts) 即唯一查询路径，
// WITHOUT ROWID 使主键索引就是表本身。
// 每列都带 DEFAULT 0：后加的列在迁移里要由重建搬运旧行，旧行在新列上没有值，
// 只能取默认值；而全新建库与迁移后的库必须逐列相同（含默认值），所以默认值
// 由这一处统一给出。所有写路径都显式写全部列，默认值不参与任何业务取值。
func metricDDL(table string) string {
	cols := []string{"node_id INTEGER NOT NULL", "ts INTEGER NOT NULL"}
	for _, c := range metric.Columns {
		cols = append(cols, c.Name+"_sum "+c.SQLType()+" NOT NULL DEFAULT 0", c.Name+"_n INTEGER NOT NULL DEFAULT 0")
		if c.Kind == metric.MeanMax {
			cols = append(cols, c.Name+"_max "+c.SQLType()+" NOT NULL DEFAULT 0")
		}
	}
	return "CREATE TABLE " + table + " (" + strings.Join(cols, ", ") + ", PRIMARY KEY (node_id, ts)) WITHOUT ROWID"
}

// metricColumnNames 是 SQL 里列的顺序：与 Bucket 切片按描述表下标对应。
func metricColumnNames() []string {
	var names []string
	for _, c := range metric.Columns {
		names = append(names, c.Name+"_sum", c.Name+"_n")
		if c.Kind == metric.MeanMax {
			names = append(names, c.Name+"_max")
		}
	}
	return names
}

// metricUpsert 生成加法合并语句。合并正确的前提由 live 保证：每个内存桶至多
// 成功写入一次（刷出即取走）；事务原子性保证失败即未应用，重试不会重复计入。
func metricUpsert(table string) string {
	names := metricColumnNames()
	var sets []string
	for _, c := range metric.Columns {
		sets = append(sets,
			c.Name+"_sum = "+c.Name+"_sum + excluded."+c.Name+"_sum",
			c.Name+"_n = "+c.Name+"_n + excluded."+c.Name+"_n")
		if c.Kind == metric.MeanMax {
			sets = append(sets, c.Name+"_max = max("+c.Name+"_max, excluded."+c.Name+"_max)")
		}
	}
	all := append([]string{"node_id", "ts"}, names...)
	return "INSERT INTO " + table + " (" + strings.Join(all, ", ") + ") VALUES (" +
		strings.TrimSuffix(strings.Repeat("?, ", len(all)), ", ") + ") ON CONFLICT (node_id, ts) DO UPDATE SET " +
		strings.Join(sets, ", ")
}

func metricSelect(table string) string {
	return "SELECT ts, " + strings.Join(metricColumnNames(), ", ") + " FROM " + table +
		" WHERE node_id = ? AND ts >= ? AND ts < ? ORDER BY ts"
}

// bucketArgs 把桶按描述表顺序展开成绑定参数；整数列在此处转回整型。
func bucketArgs(b *metric.Bucket) []any {
	var args []any
	for i, c := range metric.Columns {
		if c.Type == metric.Int {
			args = append(args, int64(b.Sum[i]), int64(b.N[i]))
			if c.Kind == metric.MeanMax {
				args = append(args, int64(b.Max[i]))
			}
		} else {
			args = append(args, b.Sum[i], int64(b.N[i]))
			if c.Kind == metric.MeanMax {
				args = append(args, b.Max[i])
			}
		}
	}
	return args
}
