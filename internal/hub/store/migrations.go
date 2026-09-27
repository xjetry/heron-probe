package store

import (
	"database/sql"
	"fmt"
)

// migrations[v] 把 user_version = v−1 的库升到 v。空库不重放历史，直接建到当前版本；
// 所以 schemaStatements 必须始终是"当前版本的完整 DDL"，迁移测试用逐表、逐索引比对钉住这一点。
//
// 不变式：每个迁移只引用冻结的该版本 DDL（下面的 *V<n> 常量），不引用 schema.go 里的当前 DDL。
var migrations = map[int]func(*sql.Tx) error{
	2: execAll(migrationV2),
	3: func(tx *sql.Tx) error {
		if _, err := tx.Exec(ddlTrafficV3); err != nil {
			return fmt.Errorf("%w in %q", err, ddlTrafficV3)
		}
		for _, t := range metricTablesV3 {
			if err := rebuildTable(tx, t, metricDDLV3); err != nil {
				return fmt.Errorf("rebuilding %s: %w", t, err)
			}
		}
		return nil
	},
	4:  execAll(migrationV4),
	5:  execAll(alertStatementsV5),
	6:  execAll([]string{ddlAPITokenV6}),
	7:  migrateDeliveryFailure,
	8:  execAll([]string{ddlSettingV8}),
	9:  execAll(migrationV9),
	10: execAll(migrationV10),
	11: execAll([]string{ddlMaintenanceStateV11}),
	12: execAll(migrationV12),
	13: execAll(migrationV13),
	14: execAll(migrationV14),
	15: execAll(migrationV15),
}

func execAll(stmts []string) func(*sql.Tx) error {
	return func(tx *sql.Tx) error {
		for _, stmt := range stmts {
			if _, err := tx.Exec(stmt); err != nil {
				return fmt.Errorf("%w in %q", err, stmt)
			}
		}
		return nil
	}
}

// 各迁移的输入逐字冻结在这里。当前版本的 DDL 只给 schemaStatements 用；迁移若引用它，DDL 以后
// 一变，旧库升级就会建出与后续迁移假设不符的表（例如先带上新列，再在加列的迁移里撞上重复列）。
// 冻结点是该 schema 版本在仓库里的最后状态（下一次升版本的父提交），不是迁移引入时：版本 5 期间
// 告警表在不升版本的情况下被改过（加了 alert_rule.all_nodes、alert_delivery.done 等），之后的迁移
// 与 migrate_test 的 schemaV5 夹具都以最后状态为准。

// v2（b5feb18）：管理员、会话与 5m/1h 指标表。指标表此时还没有流量列与列默认值，由迁移 3 重建。
var migrationV2 = []string{
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
	`CREATE TABLE metric_5m (node_id INTEGER NOT NULL, ts INTEGER NOT NULL, cpu_sum REAL NOT NULL, cpu_n INTEGER NOT NULL, cpu_max REAL NOT NULL, mem_used_sum INTEGER NOT NULL, mem_used_n INTEGER NOT NULL, mem_used_max INTEGER NOT NULL, swap_used_sum INTEGER NOT NULL, swap_used_n INTEGER NOT NULL, disk_used_sum INTEGER NOT NULL, disk_used_n INTEGER NOT NULL, load1_sum REAL NOT NULL, load1_n INTEGER NOT NULL, tcp_sum INTEGER NOT NULL, tcp_n INTEGER NOT NULL, udp_sum INTEGER NOT NULL, udp_n INTEGER NOT NULL, procs_sum INTEGER NOT NULL, procs_n INTEGER NOT NULL, PRIMARY KEY (node_id, ts)) WITHOUT ROWID`,
	`CREATE TABLE metric_1h (node_id INTEGER NOT NULL, ts INTEGER NOT NULL, cpu_sum REAL NOT NULL, cpu_n INTEGER NOT NULL, cpu_max REAL NOT NULL, mem_used_sum INTEGER NOT NULL, mem_used_n INTEGER NOT NULL, mem_used_max INTEGER NOT NULL, swap_used_sum INTEGER NOT NULL, swap_used_n INTEGER NOT NULL, disk_used_sum INTEGER NOT NULL, disk_used_n INTEGER NOT NULL, load1_sum REAL NOT NULL, load1_n INTEGER NOT NULL, tcp_sum INTEGER NOT NULL, tcp_n INTEGER NOT NULL, udp_sum INTEGER NOT NULL, udp_n INTEGER NOT NULL, procs_sum INTEGER NOT NULL, procs_n INTEGER NOT NULL, PRIMARY KEY (node_id, ts)) WITHOUT ROWID`,
}

// v3（fce35e9）：流量表与按当时描述表重建的指标表。
const ddlTrafficV3 = `CREATE TABLE traffic (
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
)`

var metricTablesV3 = []string{"metric_1m", "metric_5m", "metric_1h"}

// rebuildTable 以 name_new 建临时表，所以冻结的是按表名生成的函数，列定义取自 fce35e9 的 metricDDL。
func metricDDLV3(table string) string {
	return "CREATE TABLE " + table + " (" + metricColumnsV3 + ") WITHOUT ROWID"
}

const metricColumnsV3 = `node_id INTEGER NOT NULL, ts INTEGER NOT NULL, cpu_sum REAL NOT NULL DEFAULT 0, cpu_n INTEGER NOT NULL DEFAULT 0, cpu_max REAL NOT NULL DEFAULT 0, mem_used_sum INTEGER NOT NULL DEFAULT 0, mem_used_n INTEGER NOT NULL DEFAULT 0, mem_used_max INTEGER NOT NULL DEFAULT 0, swap_used_sum INTEGER NOT NULL DEFAULT 0, swap_used_n INTEGER NOT NULL DEFAULT 0, disk_used_sum INTEGER NOT NULL DEFAULT 0, disk_used_n INTEGER NOT NULL DEFAULT 0, load1_sum REAL NOT NULL DEFAULT 0, load1_n INTEGER NOT NULL DEFAULT 0, tcp_sum INTEGER NOT NULL DEFAULT 0, tcp_n INTEGER NOT NULL DEFAULT 0, udp_sum INTEGER NOT NULL DEFAULT 0, udp_n INTEGER NOT NULL DEFAULT 0, procs_sum INTEGER NOT NULL DEFAULT 0, procs_n INTEGER NOT NULL DEFAULT 0, rx_bytes_sum INTEGER NOT NULL DEFAULT 0, rx_bytes_n INTEGER NOT NULL DEFAULT 0, tx_bytes_sum INTEGER NOT NULL DEFAULT 0, tx_bytes_n INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (node_id, ts)`

// v4（1996a98）：探测任务、分配、版本与探测表族。
var migrationV4 = []string{
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

// v5（9140ab1）：告警各表。
var alertStatementsV5 = []string{
	`CREATE TABLE alert_rule (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  kind TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  all_nodes INTEGER NOT NULL DEFAULT 0,
  task_id INTEGER,
  metric TEXT,
  threshold REAL,
  for_minutes INTEGER,
  created_at INTEGER NOT NULL
)`,
	`CREATE TABLE alert_rule_node (
  rule_id INTEGER NOT NULL,
  node_id INTEGER NOT NULL,
  PRIMARY KEY (rule_id, node_id)
)`,
	`CREATE INDEX alert_rule_node_by_node ON alert_rule_node(node_id)`,
	`CREATE TABLE alert_rule_channel (
  rule_id INTEGER NOT NULL,
  channel_id INTEGER NOT NULL,
  PRIMARY KEY (rule_id, channel_id)
)`,
	`CREATE INDEX alert_rule_channel_by_channel ON alert_rule_channel(channel_id)`,
	`CREATE TABLE notify_channel (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  kind TEXT NOT NULL,
  config TEXT NOT NULL,
  created_at INTEGER NOT NULL
)`,
	`CREATE TABLE alert_state (
  rule_id INTEGER NOT NULL,
  node_id INTEGER NOT NULL,
  state TEXT NOT NULL,
  since_at INTEGER NOT NULL,
  PRIMARY KEY (rule_id, node_id)
)`,
	`CREATE TABLE alert_event (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  rule_id INTEGER NOT NULL,
  node_id INTEGER NOT NULL,
  transition TEXT NOT NULL,
  at INTEGER NOT NULL,
  summary TEXT NOT NULL,
  value REAL NOT NULL
)`,
	`CREATE INDEX alert_event_by_node ON alert_event(node_id, id)`,
	`CREATE INDEX alert_event_by_at ON alert_event(at)`,
	`CREATE TABLE alert_delivery (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id INTEGER NOT NULL,
  channel_id INTEGER NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  ok INTEGER NOT NULL DEFAULT 0,
  done INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  delivered_at INTEGER
)`,
	`CREATE INDEX alert_delivery_by_event ON alert_delivery(event_id)`,
	`CREATE INDEX alert_delivery_pending ON alert_delivery(done, id)`,
}

// v6（8d989b0）：API token。
const ddlAPITokenV6 = `CREATE TABLE api_token (
  -- AUTOINCREMENT：id 永不复用。吊销按 id 进行，复用会让针对旧 token 的吊销落到新 token 上。
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  -- 整串明文（含前缀）的 SHA-256；明文不落库。
  token_hash BLOB NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  -- NULL 表示从未使用。只供展示：距已落库值满一分钟才刷新。
  last_used_at INTEGER
)`

// 旧版本把类别与原文混写在 last_error 里。只按旧版本写入的确定形状归类，其余失败无法确定类别，
// 归入 unclassified 且原文不动；成功行与尚无结果（未终态且没有文本）的行不碰。终态失败却没有
// 文本的行旧版本不会写出，若有也归 unclassified，不能迁成"没有失败"。
// 下面两个字符串是旧版本写入 last_error 的文本，是库里的历史值，不随代码变。
// 旧 HTTP 失败的格式是 "HTTP %d %s: %s"：%d 不带前导 0，所以只认首位 1–9 的三位数，写入的状态码
// 因而落在 100–999（http_status 列的不变式）；http.StatusText 不含 ": "，所以第一个 ": " 之后就是
// 响应体片段；没有 ": " 的不是这个格式写出的，不按 HTTP 形状归类。
//
// 历史数据的边界：旧版本在"次数耗尽而最后一次结果未落盘"时沿用更早一次的失败作终态，写下的行
// 与"最后一次真的以同样原因失败"在库里分不出来，这里按那次失败归类（例如 http_status 503）；
// 这部分历史无法修正。只有一次失败都没记下的行才带着 result_unrecorded 的旧文本。
func migrateDeliveryFailure(tx *sql.Tx) error {
	for _, stmt := range []string{
		`ALTER TABLE alert_delivery ADD COLUMN failure TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE alert_delivery ADD COLUMN http_status INTEGER`,
		`UPDATE alert_delivery SET failure = 'channel_deleted', last_error = ''
		  WHERE ok = 0 AND last_error = 'channel deleted'`,
		`UPDATE alert_delivery SET failure = 'result_unrecorded', last_error = ''
		  WHERE ok = 0 AND last_error = 'attempts exhausted but last result was not recorded'`,
		`UPDATE alert_delivery SET failure = 'http_status', http_status = CAST(substr(last_error, 6, 3) AS INTEGER),
		    last_error = substr(last_error, instr(last_error, ': ') + 2)
		  WHERE ok = 0 AND failure = '' AND last_error GLOB 'HTTP [1-9][0-9][0-9] *' AND instr(last_error, ': ') > 0`,
		`UPDATE alert_delivery SET failure = 'unclassified'
		  WHERE ok = 0 AND failure = '' AND (last_error <> '' OR done = 1)`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("%w in %q", err, stmt)
		}
	}
	return nil
}

// v8：全站设置的键值表。
const ddlSettingV8 = `CREATE TABLE setting (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
)`

// v9：节点的计费与到期五列、到期规则的提前天数、到期告警状态记下的触发时到期日。旧行取列默认值：没有计费信息，
// 规则的 days_before 为 NULL，已有状态的 fired_expires_on 为空（它们都属于离线与探测规则）。
var migrationV9 = []string{
	`ALTER TABLE node ADD COLUMN price TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE node ADD COLUMN currency TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE node ADD COLUMN billing_cycle TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE node ADD COLUMN expires_on TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE node ADD COLUMN auto_renew INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE alert_rule ADD COLUMN days_before INTEGER`,
	`ALTER TABLE alert_state ADD COLUMN fired_expires_on TEXT NOT NULL DEFAULT ''`,
}

// v10：探测任务的全部节点开关。旧任务取默认值 0，保持原来的显式分配。
var migrationV10 = []string{
	`ALTER TABLE probe_task ADD COLUMN all_nodes INTEGER NOT NULL DEFAULT 0`,
}

// v11：维护任务的簿记表。旧库升级后没有行，读侧按"从未成功跑过"呈现，直到下一轮成功的上卷与 prune 写入。
const ddlMaintenanceStateV11 = `CREATE TABLE maintenance_state (
  name TEXT PRIMARY KEY,
  finished_at INTEGER NOT NULL
)`

// v12：离线规则×节点上次恢复的时刻。旧行取 NULL（从未恢复过）：升级前的恢复没有记录，升级后的第一次恢复开始计窗口。
var migrationV12 = []string{
	`ALTER TABLE alert_state ADD COLUMN recovered_at INTEGER`,
}

// v13：节点最近一次上报的来源地址。旧行取空串（从未上报过的记法）：升级前的上报没有记录，下一次刷出即补上。
var migrationV13 = []string{
	`ALTER TABLE node ADD COLUMN last_source TEXT NOT NULL DEFAULT ''`,
}

// v14：节点的国家（查得值与所属地址成对）与手动指定的国家。旧行取空串：没有国家；查询开启后按来源地址补查。
var migrationV14 = []string{
	`ALTER TABLE node ADD COLUMN country TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE node ADD COLUMN country_ip TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE node ADD COLUMN country_pin TEXT NOT NULL DEFAULT ''`,
}

// v15：节点标签与关联。旧库升级后没有标签，节点的标签集合为空。
var migrationV15 = []string{
	`CREATE TABLE tag (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  name_fold TEXT NOT NULL UNIQUE
)`,
	`CREATE TABLE node_tag (
  node_id INTEGER NOT NULL,
  tag_id INTEGER NOT NULL,
  PRIMARY KEY (node_id, tag_id)
) WITHOUT ROWID`,
	`CREATE INDEX node_tag_by_tag ON node_tag (tag_id)`,
}
