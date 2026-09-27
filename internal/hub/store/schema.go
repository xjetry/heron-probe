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
  last_seen_at INTEGER,
  -- 计费与到期（§9.4）：提醒用的展示值，空串与 0 是"未填"。取值约束由 api 的 UpdateNode 裁决，库里不设 CHECK。
  -- 列序与迁移 9 的 ADD COLUMN 结果一致。
  price TEXT NOT NULL DEFAULT '',
  currency TEXT NOT NULL DEFAULT '',
  billing_cycle TEXT NOT NULL DEFAULT '',
  expires_on TEXT NOT NULL DEFAULT '',
  auto_renew INTEGER NOT NULL DEFAULT 0
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
// agent 未换启动周期且计数器未倒退时，崩溃后首次上报相对已落盘基线的差分会补回丢失的内存增量。
const ddlTraffic = `CREATE TABLE traffic (
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

// metricTables 按级别从细到粗。读者有三处：建库、DeleteNode、上卷（rollup.go 的 metricFamily.tables，
// 必须与 levels、states 同序同长，上卷按 levels 循环，多出的表不会被上卷也不报错）。新增级别要三处同改，
// 已有库还需对应的增量迁移。存储统计按 sqlite_master 列表，不读它。
var metricTables = []string{"metric_1m", "metric_5m", "metric_1h"}

// schemaStatements 是当前版本的完整 DDL：空库直接建到当前版本，不重放历史。
func schemaStatements() []string {
	out := []string{ddlNode, ddlNodeFacts, ddlRegisterWindow, ddlRollupState, seedRollupState, seedProbeRollupState,
		ddlAdmin, ddlAdminSession, ddlTraffic, ddlProbeTask, ddlProbeTaskNode, ddlProbeTaskNodeIndex, ddlProbeMeta, seedProbeMeta}
	for _, t := range metricTables {
		out = append(out, metricDDL(t))
	}
	for _, t := range probeTables {
		out = append(out, probeDDL(t))
	}
	return append(append(out, alertStatements()...), ddlAPIToken, ddlSetting)
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

// 探测表族：键比指标表多一维 task_id，值列固定六个。rtt_min_us / rtt_max_us 可空——
// 全部丢包或错误的桶没有 rtt 样本，NULL 让 min()/max() 聚合自动跳过它。
func probeDDL(table string) string {
	return "CREATE TABLE " + table + ` (
  node_id INTEGER NOT NULL, ts INTEGER NOT NULL, task_id INTEGER NOT NULL,
  sent INTEGER NOT NULL DEFAULT 0, lost INTEGER NOT NULL DEFAULT 0, errors INTEGER NOT NULL DEFAULT 0,
  rtt_sum_us INTEGER NOT NULL DEFAULT 0, rtt_min_us INTEGER, rtt_max_us INTEGER,
  PRIMARY KEY (node_id, ts, task_id)
) WITHOUT ROWID`
}

// probeTables 与 metricTables 同一口径：建库、DeleteNode 与上卷（rollup.go 的 probeFamily.tables，
// 与 levels、states 同序同长）共用。
var probeTables = []string{"probe_1m", "probe_5m", "probe_1h"}

const ddlProbeTask = `CREATE TABLE probe_task (
  -- AUTOINCREMENT：历史行只带 task_id，删除任务后 id 若复用，旧历史会挂到新任务上。
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  kind INTEGER NOT NULL,
  target TEXT NOT NULL,
  interval_s INTEGER NOT NULL,
  timeout_ms INTEGER NOT NULL,
  created_at INTEGER NOT NULL
)`

const ddlProbeTaskNode = `CREATE TABLE probe_task_node (
  task_id INTEGER NOT NULL,
  node_id INTEGER NOT NULL,
  PRIMARY KEY (task_id, node_id)
) WITHOUT ROWID`

const ddlProbeTaskNodeIndex = `CREATE INDEX probe_task_node_by_node ON probe_task_node (node_id)`

// probe_meta.version 由任务保存与删除事务递增，agent 用它对账任务清单。
// 删除节点时仅清理其分配，不递增：auth.DeleteNode 在删除成功后撤销 token，
// 其余节点的清单不变，无需因此重新对账。
// 单行表，CHECK 让第二行无法插入。
const ddlProbeMeta = `CREATE TABLE probe_meta (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  version INTEGER NOT NULL
)`

const seedProbeMeta = `INSERT INTO probe_meta (id, version) VALUES (1, 0)`

const seedProbeRollupState = `INSERT INTO rollup_state (level, upto_ts) VALUES ('probe_5m', 0), ('probe_1h', 0)`

// all_nodes 显式区分全部节点与有限作用域：为真时 SaveAlertRule 不写节点联结行；
// 为假时空联结集合不覆盖任何节点，DeleteNode 删除最后一个联结也不会放宽规则。
const ddlAlertRule = `CREATE TABLE alert_rule (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  kind TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  all_nodes INTEGER NOT NULL DEFAULT 0,
  task_id INTEGER,
  metric TEXT,
  threshold REAL,
  for_minutes INTEGER,
  created_at INTEGER NOT NULL,
  -- 仅到期规则非 NULL（1–365），种类与字段的对应由 alert.CheckRule 裁决；列序与迁移 9 的 ADD COLUMN 结果一致。
  days_before INTEGER
)`
const ddlAlertRuleNode = `CREATE TABLE alert_rule_node (
  rule_id INTEGER NOT NULL,
  node_id INTEGER NOT NULL,
  PRIMARY KEY (rule_id, node_id)
)`
const ddlAlertRuleNodeByNode = `CREATE INDEX alert_rule_node_by_node ON alert_rule_node(node_id)`
const ddlAlertRuleChannel = `CREATE TABLE alert_rule_channel (
  rule_id INTEGER NOT NULL,
  channel_id INTEGER NOT NULL,
  PRIMARY KEY (rule_id, channel_id)
)`
const ddlAlertRuleChannelByChannel = `CREATE INDEX alert_rule_channel_by_channel ON alert_rule_channel(channel_id)`
const ddlNotifyChannel = `CREATE TABLE notify_channel (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  kind TEXT NOT NULL,
  config TEXT NOT NULL,
  created_at INTEGER NOT NULL
)`

// 状态只在转换时写；since_at 是墙钟，只用于展示"自何时起"。计时用引擎内存里的单调钟。
const ddlAlertState = `CREATE TABLE alert_state (
  rule_id INTEGER NOT NULL,
  node_id INTEGER NOT NULL,
  state TEXT NOT NULL,
  since_at INTEGER NOT NULL,
  -- 引擎只在到期规则进入 firing 时写入非空值：当时节点的到期日（见 StateRow.FiredExpiresOn）。
  -- 列序与迁移 9 的 ADD COLUMN 结果一致：ADD COLUMN 把列排在最后，与写在 PRIMARY KEY 约束之前的这一行同为第五列。
  fired_expires_on TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (rule_id, node_id)
)`
const ddlAlertEvent = `CREATE TABLE alert_event (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  rule_id INTEGER NOT NULL,
  node_id INTEGER NOT NULL,
  transition TEXT NOT NULL,
  at INTEGER NOT NULL,
  summary TEXT NOT NULL,
  value REAL NOT NULL
)`
const ddlAlertEventByNode = `CREATE INDEX alert_event_by_node ON alert_event(node_id, id)`
const ddlAlertEventByAt = `CREATE INDEX alert_event_by_at ON alert_event(at)`

// 每渠道一行；done 显式区分可续投与终态，不用虚增 attempts 冒充不可重试。
const ddlAlertDelivery = `CREATE TABLE alert_delivery (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id INTEGER NOT NULL,
  channel_id INTEGER NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  ok INTEGER NOT NULL DEFAULT 0,
  done INTEGER NOT NULL DEFAULT 0,
  -- 最近一次失败的原文，谁能读到它见 Delivery.LastError。
  last_error TEXT NOT NULL DEFAULT '',
  delivered_at INTEGER,
  -- 最近一次失败的类别（DeliveryFailure），空表示没有失败；列序与迁移 7 的 ADD COLUMN 结果一致。
  failure TEXT NOT NULL DEFAULT '',
  -- 仅 failure = 'http_status' 时非 NULL，且在 100–999。三条写路径各自保证：UpdateDelivery 经
  -- DeliveryResult.check；DeleteNotifyChannel 写 channel_deleted 时一并写 NULL；迁移 7 只对首位
  -- 1–9 的三位数写入，其余行保持 ADD COLUMN 的 NULL。
  http_status INTEGER
)`
const ddlAlertDeliveryByEvent = `CREATE INDEX alert_delivery_by_event ON alert_delivery(event_id)`
const ddlAlertDeliveryPending = `CREATE INDEX alert_delivery_pending ON alert_delivery(done, id)`

// api_token 是 AdminService 的程序化凭据（§5.6）。
const ddlAPIToken = `CREATE TABLE api_token (
  -- AUTOINCREMENT：id 永不复用。吊销按 id 进行，复用会让针对旧 token 的吊销落到新 token 上。
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  -- 整串明文（含前缀）的 SHA-256；明文不落库。
  token_hash BLOB NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  -- NULL 表示从未使用。只供展示：距已落库值满一分钟才刷新。
  last_used_at INTEGER
)`

// setting 是全站设置的键值表（公开页外观等）。值可达 128 KiB（logo 的 data: URL），不用 WITHOUT ROWID：
// 那种表把整行放进主键 B 树，SQLite 文档建议其行不超过页大小的约 1/20。
const ddlSetting = `CREATE TABLE setting (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
)`

func alertStatements() []string {
	return []string{ddlAlertRule, ddlAlertRuleNode, ddlAlertRuleNodeByNode, ddlAlertRuleChannel,
		ddlAlertRuleChannelByChannel, ddlNotifyChannel, ddlAlertState, ddlAlertEvent,
		ddlAlertEventByNode, ddlAlertEventByAt, ddlAlertDelivery, ddlAlertDeliveryByEvent, ddlAlertDeliveryPending}
}
