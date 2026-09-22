package store

import (
	"strings"

	"github.com/xjetry/probe/internal/hub/metric"
)

// 与描述表无关的表写成常量；metric_1m 由 metricDDL 生成。
const schemaFixed = `
CREATE TABLE node (
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
);
CREATE TABLE node_facts (
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
);
CREATE TABLE register_window (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  key_hash BLOB NOT NULL,
  expires_at INTEGER NOT NULL,
  remaining INTEGER NOT NULL
);
CREATE TABLE rollup_state (
  level TEXT PRIMARY KEY,
  upto_ts INTEGER NOT NULL
);
INSERT INTO rollup_state (level, upto_ts) VALUES ('5m', 0), ('1h', 0);
`

// schemaStatements 依赖 DDL 的注释与字符串里不出现 `;`；违反时 Exec 会在
// 建表阶段失败，测试立刻红。
func schemaStatements() []string {
	var out []string
	for _, stmt := range strings.Split(schemaFixed, ";") {
		if strings.TrimSpace(stmt) != "" {
			out = append(out, stmt)
		}
	}
	return append(out, metricDDL("metric_1m"))
}

// metricDDL 从描述表生成分钟表。主键顺序 (node_id, ts) 即唯一查询路径，
// WITHOUT ROWID 使主键索引就是表本身。
func metricDDL(table string) string {
	cols := []string{"node_id INTEGER NOT NULL", "ts INTEGER NOT NULL"}
	for _, c := range metric.Columns {
		cols = append(cols, c.Name+"_sum "+c.SQLType()+" NOT NULL", c.Name+"_n INTEGER NOT NULL")
		if c.Kind == metric.MeanMax {
			cols = append(cols, c.Name+"_max "+c.SQLType()+" NOT NULL")
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
