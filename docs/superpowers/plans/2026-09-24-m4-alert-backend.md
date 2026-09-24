# M4 告警（一）：hub 后端——规则、状态机、通知投递、管理接口 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** hub 能按规则判定节点离线与探测异常，每（规则 × 节点）维护持久化状态机，进入 firing 与回到 ok 时经 Telegram / Webhook 投递通知并逐条记录投递结果；管理接口能增删改查规则、渠道，查询事件，测试渠道。

**Architecture:** 新包 `internal/hub/alert`（依赖 `live`、`store`、`clock`；不依赖 `ingest`）持有规则、渠道与状态的内存副本（写库成功后才发布，与 `probe.Registry` 同一模式），两条评估路径：离线巡检每 10 秒；探测评估在分钟桶刷出之后按分钟边界触发。转换在同一事务里写 `alert_state`、`alert_event` 与每渠道一行 `alert_delivery`，再交给有界投递队列；单 worker 按渠道串行投递、有限次退避重试，每次尝试回写 `alert_delivery`；未成功且未耗尽次数的投递在 hub 重启后由 `Load` 重新入队。`AdminService` 新增八个方法；`node.offline_grace_s` 列接通读写链路。

**Tech Stack:** Go 1.27、connect-go、modernc SQLite（单写协程）、`text/template`、`net/http`（出站，禁跟随重定向）、buf 生成 Go 与 TS。

**Spec:** `docs/superpowers/specs/2026-09-17-probe-architecture-design.md` §3.1（包版图与依赖方向）、§3.3（方法清单）、§4.4（宽限期下限 TTL）、§6.6（表）、§9（规则、状态机、重启不变式、通知）、§11、§12；指导方针 `docs/guidelines/agent-first.md`。前置：M3 三份计划已合入 main（5fe8fad）。

## Global Constraints

**来自 spec 的硬值**

- 规则两类：离线（节点超过宽限期未上报；宽限期按节点可配，下限为 TTL，由保存节点时显式校验；NULL 表示取 TTL）；探测（某任务在某节点上的丢包率或平均 rtt 连续 N 分钟超过阈值，数据源 `probe_1m`）（§9.1）。
- 每（规则 × 节点）一个状态 `ok → pending → firing → ok`；进入 firing 发告警，回到 ok 发恢复；状态落盘于 `alert_state`，重启不重复触发、不遗忘未恢复的告警（§9.2）。
- 离线规则每 10 秒巡检；探测规则在分钟桶刷出后评估；离线恢复条件是收到一次上报；探测恢复条件是连续 1 分钟低于阈值（§9.2）。
- 重启不变式：本次启动以来未上报过的节点，离线时长从启动时刻起算（单调钟）；已上报过的从 `live` 的 `last_seen` 起算；重启前 firing 保持 firing 直到再次上报；重启前 pending 从启动时刻重新计时；落盘的 `node.last_seen_at`（墙钟）只用于文案（§9.2）。
- 渠道：Telegram、通用 Webhook（可配方法、头、请求体模板）；投递走有界队列，失败做有限次退避重试，每次投递结果落库并在面板可见；"已通知"只来自成功投递记录（§9.3）。
- 依赖方向：`alert → live, store`；`api → live, store, probe, alert, auth`；`alert` 不依赖 `ingest`（§3.1）。
- 时长一律单调钟（`clock.Clock.Mono`），墙钟只用于展示与落库时间戳（§4.5、§12）。
- 测试从用户可见入口测：`api` 经真实 Connect 处理器 + `httptest` + 生成客户端（§12）；时间经 `internal/clock` 注入。
- 面向 agent 设计：错误说清字段、约束与期望取值；不引入面板专用端点。

**本计划裁定的取值**

- 存储：schema 升到 5，新增 `alert_rule`、`alert_rule_node`、`alert_rule_channel`、`alert_state`、`alert_event`、`alert_delivery`、`notify_channel`；不建 `setting`（外观设置属公开页里程碑）。规则作用域用联结表 `alert_rule_node`，无行表示全部节点；渠道绑定用 `alert_rule_channel`。
- 离线状态机口径：`unseen` = 单调钟现在 − 该节点最近上报时刻（未上报过则用引擎 `Load` 时刻）；`unseen ≥ grace` → firing；`TTL ≤ unseen < grace` → pending；否则 ok。恢复：巡检看到 `unseen < TTL`（即 `live.Online`）→ ok。
- 探测状态机口径：取该 (节点, 任务) 最近 N 个已闭合分钟的 `probe_1m` 行（N = `for_minutes`）。某分钟"超阈"= 该行存在且 `metric ≥ threshold`（loss_pct = lost/sent×100；rtt_ms = rtt_sum_us/rtt_n/1000，rtt_n = 0 的分钟视为无数据）。最近 N 分钟全部存在且全部超阈 → firing；最近 1 分钟超阈但不足 N → pending；最近 1 分钟存在且不超阈 → ok；最近 1 分钟无数据 → 保持。
- 转换记录：`alert_event`（规则、节点、transition firing|recovered、墙钟时刻、summary 文本、触发值）；每个绑定渠道一行 `alert_delivery`（attempts、ok、last_error、delivered_at）。事件与投递行和状态更新同一事务。
- 投递：有界队列容量 256，满时丢弃最旧并记日志；单 worker；每条最多 3 次尝试，退避 1s、4s（可注入时钟与睡眠）；HTTP 4xx（除 408、429）视为永久失败不重试；每次尝试后回写 `alert_delivery`。`Load` 把 `ok = 0 且 attempts < 3` 的投递重新入队。
- 出站 HTTP：专用 `http.Client`，超时 10 s，`CheckRedirect` 返回 `http.ErrUseLastResponse`（3xx 当失败，防止凭据随跳转外泄），响应体最多读 64 KiB。
- Telegram：`POST {base}/bot{token}/sendMessage`，JSON `{chat_id, text}`，`base` 可注入（测试用 httptest）；文案纯文本。Webhook：方法限 POST/PUT/PATCH，头至多 16 个，请求体由 `text/template` 渲染，数据 `{Rule, Node, Kind, Transition, Value, At, Summary}`，缺省模板为 JSON。
- 凭据不回显：`ListNotifyChannels` 返回 Telegram 渠道时 `bot_token` 置空、`has_bot_token = true`；`SaveNotifyChannel` 更新时 `bot_token` 为空表示保留原值。
- 引用完整性显式拒绝：删除被规则引用的探测任务或渠道返回 `FailedPrecondition`，文本列出规则名；删除节点在同一事务清理 `alert_state` 与 `alert_rule_node`，事件保留。
- `ListAlertEvents`：`limit` 1–500（0 取 100），`before_id` 游标（0 表示从最新开始），按 id 降序，带每条的投递列表；可按 `node_id` 过滤。
- 探测评估时机：`cmd/hub/serve.go` 起独立定时器，分钟边界后 3 s 触发（晚于刷出的 0.5 s 与维护的 2 s），评估的"最近已闭合分钟"= 触发时刻所在分钟的前一分钟。

**代码与提交规范（来自用户全局规则，对子代理同样生效）**

- 注释、commit message 里禁止过程信息（任务 / 步骤编号、里程碑代号、审阅轮次、"按计划 / 简报 / 裁决"）。写 WHY 与不变式，前提指明保证方。
- 不打补丁；不复制第二份实现；不写 TODO；同形缺陷一次改全；不留兼容层。
- 每条新断言先红后绿；注入前 `git diff --quiet` 确认落地；红要红在正确原因。
- 判成败的命令不接管道：`cmd > log 2>&1; echo $?`；Go 测试加 `-count=1`。
- 改 proto 后 `make gen`，`git status --porcelain -- gen web/src/gen` 必须为空。

## 文件结构

```
internal/hub/store/schema.go               v5 DDL：alert_rule、alert_rule_node、alert_rule_channel、alert_state、alert_event、alert_delivery、notify_channel
internal/hub/store/store.go                schemaVersion = 5，migrations[5]
internal/hub/store/migrate_test.go         冻结 schemaV4 夹具；v4 → v5 迁移与新库逐表逐索引一致
internal/hub/store/node.go                 Node.OfflineGraceS、selectNodes、UpdateNode 增加宽限期、DeleteNode 清理告警状态与作用域、Counts 加新表
internal/hub/store/alert.go(+_test.go)     规则 / 渠道 / 状态 / 事件 / 投递的读写
internal/hub/store/errors.go               InUseError{Kind, ID, Rules}
internal/hub/store/probe.go                DeleteProbeTask 前检查规则引用
internal/hub/alert/rule.go                 规则与渠道的领域类型、校验（CheckRule、CheckChannel）
internal/hub/alert/state.go(+_test.go)     纯函数状态机：nextOffline、nextProbe
internal/hub/alert/engine.go(+_test.go)    Engine：Load、Rules/Channels/States 读侧、Save/Delete 写侧、SweepOffline、EvaluateProbes、Forget
internal/hub/alert/notify.go(+_test.go)    Channel 接口、telegram、webhook、HTTP 客户端、模板、可重试判定
internal/hub/alert/queue.go(+_test.go)     有界投递队列与 worker、退避、回写投递结果
internal/hub/alert/engine_race_test.go     巡检与管理写入并发
proto/probe/v1/admin.proto                 Node/UpdateNodeRequest 的 offline_grace_s；AlertRule、NotifyChannel、AlertEvent 消息与 8 个 RPC
gen/、web/src/gen/                          生成物
internal/hub/api/alerts.go(+_test.go)      8 个 handler
internal/hub/api/nodes.go(+_test.go)       UpdateNode 校验宽限期 ≥ TTL
internal/hub/api/service.go                Config.TTL、New 增加 *alert.Engine
internal/hub/api/probes.go                 DeleteProbeTask 的 InUse 映射
cmd/hub/serve.go                           装配 alert.Engine、三个后台协程
cmd/hub/mux_test.go                        api.New 签名变更
scripts/e2e.sh                             webhook 接收器、离线告警触发与恢复、事件与投递断言、stats 新表
```

---

### Task 1: 存储——schema v5、宽限期链路、规则 / 渠道 / 状态 / 事件 / 投递读写

**Files:**
- Modify: `internal/hub/store/schema.go`、`store.go`、`migrate_test.go`、`node.go`、`node_test.go`、`errors.go`、`probe.go`、`probe_test.go`
- Create: `internal/hub/store/alert.go`、`internal/hub/store/alert_test.go`

**Interfaces:**
- Consumes: 现有 `Store.write`、`NotFoundError`、`nodeExistsTx`、`probeTables`。
- Produces（Task 2–5 使用）：

```go
// node.go
type Node struct { …; OfflineGraceS int /* 0 表示 NULL：取 TTL */ }
func (s *Store) UpdateNode(ctx context.Context, id int64, name string, public bool, note string, resetDay int, offlineGraceS int) error // offlineGraceS 0 写 NULL

// errors.go
var ErrInUse = errors.New("in use")
type InUseError struct { Kind string; ID int64; Rules []string } // Error(): "<Kind> <ID> is referenced by alert rules: a, b"
func (e InUseError) Is(target error) bool { return target == ErrInUse }

// alert.go
type AlertKind string; const (KindOffline AlertKind = "offline"; KindProbe AlertKind = "probe")
type ProbeMetric string; const (MetricLossPct ProbeMetric = "loss_pct"; MetricRttMs ProbeMetric = "rtt_ms")
type AlertRule struct {
    ID int64; Name string; Kind AlertKind; Enabled bool
    NodeIDs []int64      // 升序去重；空 = 全部节点
    ChannelIDs []int64   // 升序去重
    TaskID uint64; Metric ProbeMetric; Threshold float64; ForMinutes int // 仅 probe
    CreatedAt time.Time
}
type NotifyChannel struct { ID int64; Name string; Kind ChannelKind; Config string /* JSON */; CreatedAt time.Time }
type ChannelKind string; const (ChannelTelegram ChannelKind = "telegram"; ChannelWebhook ChannelKind = "webhook")
type AlertState string; const (StateOK AlertState = "ok"; StatePending AlertState = "pending"; StateFiring AlertState = "firing")
type StateRow struct { RuleID, NodeID int64; State AlertState; SinceAt time.Time }
type Transition string; const (TransitionFiring Transition = "firing"; TransitionRecovered Transition = "recovered")
type AlertEvent struct { ID int64; RuleID, NodeID int64; Transition Transition; At time.Time; Summary string; Value float64; Deliveries []Delivery }
type Delivery struct { ID int64; EventID int64; ChannelID int64; Attempts int; OK bool; LastError string; DeliveredAt time.Time }

func (s *Store) ListAlertRules(ctx) ([]AlertRule, error)                  // 按 id 升序，含 NodeIDs/ChannelIDs
func (s *Store) SaveAlertRule(ctx, r AlertRule) (AlertRule, error)        // r.ID 0 创建；节点/渠道/任务不存在 → NotFoundError{Kind:"node"|"notify channel"|"probe task"}
func (s *Store) DeleteAlertRule(ctx, id int64) error                       // 级联删 alert_rule_node/alert_rule_channel/alert_state；事件保留
func (s *Store) ListNotifyChannels(ctx) ([]NotifyChannel, error)
func (s *Store) SaveNotifyChannel(ctx, c NotifyChannel) (NotifyChannel, error)
func (s *Store) DeleteNotifyChannel(ctx, id int64) error                   // 被规则引用 → InUseError{Kind:"notify channel"}
func (s *Store) ListAlertStates(ctx) ([]StateRow, error)
// RecordTransition 在同一事务里写状态、事件与每渠道一行投递（attempts 0、ok 0），返回带 Deliveries 的事件。
func (s *Store) RecordTransition(ctx, ruleID, nodeID int64, state AlertState, ev AlertEvent, channelIDs []int64) (AlertEvent, error)
func (s *Store) SetAlertState(ctx, ruleID, nodeID int64, state AlertState, since time.Time) error // ok↔pending 等不产生事件的转换
func (s *Store) UpdateDelivery(ctx, id int64, attempts int, ok bool, lastError string, at time.Time) error
func (s *Store) PendingDeliveries(ctx, maxAttempts int) ([]Delivery, error) // ok=0 且 attempts<maxAttempts，附事件（用 ListAlertEventsByID）
func (s *Store) ListAlertEvents(ctx, nodeID int64 /*0=全部*/, beforeID int64 /*0=最新*/, limit int) ([]AlertEvent, error) // id 降序，含 Deliveries
func (s *Store) GetAlertEvent(ctx, id int64) (AlertEvent, error)
```

- [ ] **Step 1: DDL 与迁移**

`schema.go` 新增（放在 probe 表之后）：

```go
// 规则作用域与渠道绑定用联结表：无作用域行表示全部节点——空条件匹配一切，属于放宽，
// 由 alert.Engine 在解析作用域时显式判定并注释后果。
const ddlAlertRule = `CREATE TABLE IF NOT EXISTS alert_rule (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  kind TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  task_id INTEGER,
  metric TEXT,
  threshold REAL,
  for_minutes INTEGER,
  created_at INTEGER NOT NULL
)`
const ddlAlertRuleNode = `CREATE TABLE IF NOT EXISTS alert_rule_node (
  rule_id INTEGER NOT NULL,
  node_id INTEGER NOT NULL,
  PRIMARY KEY (rule_id, node_id)
)`
const ddlAlertRuleNodeByNode = `CREATE INDEX IF NOT EXISTS alert_rule_node_by_node ON alert_rule_node(node_id)`
const ddlAlertRuleChannel = `CREATE TABLE IF NOT EXISTS alert_rule_channel (
  rule_id INTEGER NOT NULL,
  channel_id INTEGER NOT NULL,
  PRIMARY KEY (rule_id, channel_id)
)`
const ddlAlertRuleChannelByChannel = `CREATE INDEX IF NOT EXISTS alert_rule_channel_by_channel ON alert_rule_channel(channel_id)`
const ddlNotifyChannel = `CREATE TABLE IF NOT EXISTS notify_channel (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  kind TEXT NOT NULL,
  config TEXT NOT NULL,
  created_at INTEGER NOT NULL
)`
// 状态只在转换时写；since_at 是墙钟，只用于展示"自何时起"。计时用引擎内存里的单调钟。
const ddlAlertState = `CREATE TABLE IF NOT EXISTS alert_state (
  rule_id INTEGER NOT NULL,
  node_id INTEGER NOT NULL,
  state TEXT NOT NULL,
  since_at INTEGER NOT NULL,
  PRIMARY KEY (rule_id, node_id)
)`
const ddlAlertEvent = `CREATE TABLE IF NOT EXISTS alert_event (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  rule_id INTEGER NOT NULL,
  node_id INTEGER NOT NULL,
  transition TEXT NOT NULL,
  at INTEGER NOT NULL,
  summary TEXT NOT NULL,
  value REAL NOT NULL
)`
const ddlAlertEventByNode = `CREATE INDEX IF NOT EXISTS alert_event_by_node ON alert_event(node_id, id)`
// 每渠道一行；ok=0 且 attempts 未耗尽的行是重启后要续投的队列。
const ddlAlertDelivery = `CREATE TABLE IF NOT EXISTS alert_delivery (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id INTEGER NOT NULL,
  channel_id INTEGER NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  ok INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  delivered_at INTEGER
)`
const ddlAlertDeliveryByEvent = `CREATE INDEX IF NOT EXISTS alert_delivery_by_event ON alert_delivery(event_id)`
const ddlAlertDeliveryPending = `CREATE INDEX IF NOT EXISTS alert_delivery_pending ON alert_delivery(ok, attempts)`
```

`schemaStatements()` 追加这些常量；`store.go` 的 `schemaVersion = 5`，`migrations[5]` 逐条执行同样的语句。`migrate_test.go`：把当前 v4 的完整 DDL 冻结为字面量 `schemaV4`（从现行常量拷贝后不再随常量变化），新增 `TestMigrationFromV4MatchesFreshSchemaAndKeepsRows`（复用 `migrateFrom` 与 `seedMinuteRow`，比对表与索引），并保留 v1/v3 用例。

- [ ] **Step 2: 失败测试——`alert_test.go`**

```go
func TestAlertRuleRoundTripAndScope(t *testing.T)        // 创建 offline 规则（node_ids 空）、probe 规则（task、metric、threshold、for、node_ids [2,1] → 存为 [1,2]）；List 返回 id 升序且字段一致；更新替换全部字段与联结行
func TestSaveAlertRuleRejectsMissingReferences(t *testing.T) // 不存在的 node → NotFoundError{Kind:"node"}；不存在的 channel → {Kind:"notify channel"}；不存在的 task → {Kind:"probe task"}；均 errors.Is(ErrNotFound)
func TestDeleteAlertRuleCascadesButKeepsEvents(t *testing.T) // 删除后联结行与状态行为 0，事件行仍在
func TestDeleteNotifyChannelInUse(t *testing.T)          // 被规则引用 → InUseError{Kind:"notify channel", Rules:[名]}，errors.Is(ErrInUse)；解除引用后可删
func TestDeleteProbeTaskInUseByRule(t *testing.T)        // 被 probe 规则引用 → InUseError{Kind:"probe task"}
func TestRecordTransitionWritesStateEventAndDeliveries(t *testing.T) // 同一事务：alert_state 行、alert_event 行、每渠道一行 delivery（attempts 0）；返回值含 Deliveries
func TestUpdateDeliveryAndPending(t *testing.T)          // 两次失败后 PendingDeliveries(3) 仍含；第三次失败后不含；成功后不含
func TestListAlertEventsCursor(t *testing.T)             // 插入 5 条；limit 2 → 最新两条；before_id 取下两条；node_id 过滤；每条带 Deliveries
func TestDeleteNodeCleansAlertScopeAndState(t *testing.T) // DeleteNode 后 alert_rule_node 与 alert_state 中该节点行为 0，规则与事件保留
func TestNodeOfflineGraceRoundTrip(t *testing.T)         // UpdateNode 写 0 → 列 NULL、读回 0；写 90 → 读回 90
func TestCountsIncludesAlertTables(t *testing.T)         // Counts 含 7 张新表键
```

用 `store.Open` 到临时文件；插入节点用现有 `CreateNode`，任务用现有 `SaveProbeTask`。

- [ ] **Step 3: 运行，确认失败在"未定义"**

Run: `go test -count=1 ./internal/hub/store -run 'Alert|InUse|Delivery|OfflineGrace|Counts' > /tmp/m4a-t1-red.log 2>&1; echo $?`

- [ ] **Step 4: 实现**

要点（不是全部代码）：
- `SaveAlertRule`：`s.write` 内先校验引用（`nodeExistsTx`、渠道与任务 `SELECT 1`），INSERT 或 UPDATE 主表，删除并重插两张联结表；`NodeIDs`/`ChannelIDs` 去重升序后写入。
- `DeleteProbeTask`（`probe.go`）在删除前 `SELECT name FROM alert_rule WHERE task_id = ?`，非空返回 `InUseError{Kind: "probe task", ID: id, Rules: names}`。
- `DeleteNotifyChannel` 同形（经 `alert_rule_channel` JOIN `alert_rule`）。
- `DeleteNode` 追加 `DELETE FROM alert_state WHERE node_id = ?` 与 `DELETE FROM alert_rule_node WHERE node_id = ?`。
- `RecordTransition`：`INSERT OR REPLACE INTO alert_state`，`INSERT INTO alert_event … RETURNING id`，逐渠道 `INSERT INTO alert_delivery`。
- `ListAlertEvents`：`WHERE (? = 0 OR node_id = ?) AND (? = 0 OR id < ?) ORDER BY id DESC LIMIT ?`，再按事件 id 批量取投递行。
- `Node.OfflineGraceS`：`selectNodes` 增加 `n.offline_grace_s`，扫描到 `sql.NullInt64`；`UpdateNode` 的 SQL 用 `NULLIF(?, 0)`。
- `Counts` 追加七张表。

- [ ] **Step 5: 运行 store 全部测试与迁移测试**

Run: `go test -count=1 ./internal/hub/store > /tmp/m4a-t1-green.log 2>&1; echo $?`

- [ ] **Step 6: 缺陷注入**

- 迁移 5 漏建 `alert_delivery_pending` 索引 → v4 迁移比对用例红。
- `DeleteNode` 不清理 `alert_state` → 用例红。
- `DeleteProbeTask` 不查引用 → InUse 用例红。
- `RecordTransition` 漏写投递行 → 用例红。
- `PendingDeliveries` 条件写成 `attempts <= max` → 用例红。

- [ ] **Step 7: 提交**

```bash
git add internal/hub/store
git commit -m "hub: 告警规则、状态、事件与投递落库，删除被引用对象时显式拒绝"
```

---

### Task 2: alert 引擎——领域类型、校验、纯函数状态机、离线巡检与探测评估

**Files:**
- Create: `internal/hub/alert/rule.go`、`state.go`、`state_test.go`、`engine.go`、`engine_test.go`、`engine_race_test.go`

**Interfaces:**
- Consumes: Task 1 的 store 类型与方法；`live.Live.Get`（`Entry{LastSeen mono, Online}`）；`store.ListNodes`；`store.QueryProbes(ctx, nodeID, from, to, LevelByName("1m"), 60)`；`clock.Clock`。
- Produces（Task 3–5 使用）：

```go
package alert

var ErrInvalid = errors.New("invalid")
func CheckRule(r store.AlertRule) error       // name 1–64 rune；kind ∈ {offline, probe}；probe: task_id ≠ 0、metric ∈ {loss_pct, rtt_ms}、loss 阈值 0–100、rtt 阈值 > 0、for_minutes 1–60；错误文本说清字段与约束
func CheckChannel(c store.NotifyChannel) error // name 1–64 rune；telegram: chat_id 非空、bot_token 非空（更新时允许空表示保留）；webhook: url 为 http/https、method ∈ {POST,PUT,PATCH}、headers ≤ 16 且键为合法 token、模板可解析

// 状态机纯函数：输入当前状态与观测，输出下一状态与是否产生转换事件。
type Observation struct { Unseen time.Duration; Grace, TTL time.Duration }              // 离线
func NextOffline(cur store.AlertState, o Observation) (store.AlertState, *store.Transition)
type MinuteSample struct { Present bool; Exceeds bool; Value float64 }                    // 探测，按时间升序，最后一个是最近已闭合分钟
func NextProbe(cur store.AlertState, samples []MinuteSample, forMinutes int) (store.AlertState, *store.Transition)

type Sender interface { Enqueue(ev store.AlertEvent) }   // Task 3 实现；引擎在转换落库后调用
type Config struct { TTL time.Duration }
func New(cfg Config, st *store.Store, l *live.Live, clk clock.Clock, log *slog.Logger) *Engine
func (e *Engine) SetSender(s Sender)
func (e *Engine) Load(ctx context.Context) error      // 规则、渠道、状态入内存；记录 started = clk.Mono()
func (e *Engine) Rules() []store.AlertRule             // 拷贝
func (e *Engine) Channels() []store.NotifyChannel      // 拷贝
func (e *Engine) States() []store.StateRow             // 拷贝
func (e *Engine) SaveRule(ctx, r store.AlertRule) (store.AlertRule, error)   // CheckRule → store → 内存发布；禁用或作用域缩小时把不再覆盖的 (rule,node) 状态删除（store.SetAlertState 的删除形式：DeleteAlertStatesNotIn）
func (e *Engine) DeleteRule(ctx, id int64) error
func (e *Engine) SaveChannel(ctx, c store.NotifyChannel) (store.NotifyChannel, error)
func (e *Engine) DeleteChannel(ctx, id int64) error
func (e *Engine) Forget(nodeID int64)                  // 删除节点后清内存状态（库由 store.DeleteNode 清）
func (e *Engine) SweepOffline(ctx context.Context) error          // 每 10 秒
func (e *Engine) EvaluateProbes(ctx context.Context, minuteTS int64) error // minuteTS = 最近已闭合分钟起始（Unix 秒，60 对齐）
```

- [ ] **Step 1: 失败测试——状态机纯函数（表驱动）**

```go
func TestNextOffline(t *testing.T) {
  ttl, grace := 30*time.Second, 90*time.Second
  cases := []struct{ name string; cur store.AlertState; unseen time.Duration; want store.AlertState; tr *store.Transition }{
    {"在线保持 ok", store.StateOK, 5 * time.Second, store.StateOK, nil},
    {"超过 TTL 进入 pending", store.StateOK, 31 * time.Second, store.StatePending, nil},
    {"超过宽限期触发", store.StatePending, 90 * time.Second, store.StateFiring, ptr(store.TransitionFiring)},
    {"ok 直接超过宽限期也触发", store.StateOK, 100 * time.Second, store.StateFiring, ptr(store.TransitionFiring)},
    {"firing 保持", store.StateFiring, 200 * time.Second, store.StateFiring, nil},
    {"firing 收到上报恢复", store.StateFiring, 3 * time.Second, store.StateOK, ptr(store.TransitionRecovered)},
    {"pending 收到上报回 ok 不发事件", store.StatePending, 3 * time.Second, store.StateOK, nil},
    {"firing 但仍超过 TTL 未到宽限期仍 firing", store.StateFiring, 40 * time.Second, store.StateFiring, nil},
  }
  …
}
func TestNextProbe(t *testing.T) {
  // forMinutes = 3；样本按时间升序
  // 三分钟全超阈 → firing 事件；两分钟超阈 → pending；最近一分钟不超阈 → ok（从 firing 来时发 recovered，从 pending 来时不发）；最近一分钟缺失 → 保持当前；firing 时最近三分钟里有缺失但最近一分钟超阈 → 保持 firing
}
```

- [ ] **Step 2: 失败测试——引擎（假时钟、真实 store、真实 live）**

```go
func TestSweepOfflineFollowsRestartInvariant(t *testing.T)
// 两个节点、一条离线规则（全部节点）、宽限 60s、TTL 30s。节点 1 通过 live.Observe 上报过；节点 2 从未上报。
// 假时钟从 Load 起前进 31s：节点 2 pending（从启动时刻算）、节点 1 也 pending；前进到 61s：两者 firing，各一条事件、每渠道一行投递，Sender 收到两条；
// 节点 1 再次 Observe 后 Sweep：节点 1 recovered 事件；节点 2 仍 firing。
// 重启：new Engine + Load（同一 store，live 为空）；Sweep 立即：节点 1 之前 firing→ 保持 firing（不再发事件）；再前进 61s 无新事件；节点 1 Observe 后恢复。

func TestSweepUsesNodeGraceWithTTLFloor(t *testing.T)   // 节点宽限 NULL → 用 TTL；节点宽限 120 → 120
func TestSweepRespectsScopeAndEnabled(t *testing.T)     // 作用域只含节点 1 → 节点 2 无状态；禁用规则 → 不评估且已有状态被删除
func TestEvaluateProbesReadsClosedMinutes(t *testing.T) // 写入 probe_1m 三分钟 loss 50%（阈值 20、for 3）→ firing 事件 value=50；再写一分钟 loss 0 → recovered；rtt 规则同形（rtt_n=0 的分钟视为无数据 → 保持）
func TestEvaluateProbesOnlyForAssignedNodes(t *testing.T) // 任务未分配给节点 2 → 节点 2 不评估
func TestSaveRuleShrinkingScopeDropsStates(t *testing.T) // 作用域从全部改为 [1] → 节点 2 的状态行被删
```

`engine_race_test.go`：巡检协程持续 `SweepOffline`，同时另一协程 `SaveRule`/`DeleteRule`，`-race` 下无数据竞争、无死锁；用 `-count=1`。

- [ ] **Step 3: 运行，确认失败在"包不存在"**

- [ ] **Step 4: 实现要点**

- 引擎内存：`mu sync.RWMutex` 保护 `rules map[int64]store.AlertRule`、`channels map[int64]store.NotifyChannel`、`states map[stateKey]stateEntry{state, sinceMono time.Duration}`；写侧串行 `writeMu`（同 `probe.Registry` 的两锁模式：先写库成功再在 `mu` 下发布）。
- `SweepOffline`：`nodes := store.ListNodes`（节点全集，权威来源——`live` 重启后为空，只枚举它会漏掉未上报的节点）；对每条启用的离线规则 × 作用域内节点：`unseen = now − lastSeen`，`lastSeen` 取 `live.Get(id).LastSeen`，无条目则取 `e.started`；`grace = node.OfflineGraceS 秒 或 TTL`；`NextOffline` → 若转换：`store.RecordTransition`（summary 形如 "节点 <name> 离线 <时长>" / "节点 <name> 已恢复上报"）并 `sender.Enqueue`；若仅状态变化：`store.SetAlertState`。状态写库成功后才更新内存。
- `EvaluateProbes(minuteTS)`：对每条启用的探测规则 × 作用域 ∩ 已分配节点（`store.ProbeTaskNodes(taskID)` 或读 `probe_task_node`）：`QueryProbes(node, minuteTS−(N−1)×60, minuteTS+60, 1m, 60)` → 按 TS 铺成 N 个 `MinuteSample`（缺分钟 Present=false）；loss_pct = lost/sent×100（sent=0 视为无数据）；rtt_ms = rtt_sum_us/rtt_n/1000（rtt_n=0 视为无数据）；`NextProbe`。
- summary 文案用节点名与规则名，值保留一位小数；At 用墙钟。
- 注释写明：作用域为空匹配全部节点是放宽（后果：新建节点自动纳入），由 `CheckRule` 之外的 `scopeOf` 显式判定。

- [ ] **Step 5: 运行 alert 包测试与 race**

Run: `go test -count=1 ./internal/hub/alert > /tmp/m4a-t2-green.log 2>&1; echo $?` 与 `go test -race -count=1 ./internal/hub/alert > /tmp/m4a-t2-race.log 2>&1; echo $?`

- [ ] **Step 6: 缺陷注入**

- 未上报节点用 0 而不是 `started` 作起点 → 重启不变式用例红（重启后立刻 firing）。
- `NextOffline` 在 firing 且 unseen ≥ TTL 时回 ok → 用例红。
- `NextProbe` 把缺失分钟当不超阈 → 保持用例红。
- 作用域为空时按"无节点"处理 → 全部节点用例红。
- 状态先更新内存再写库（顺序颠倒）→ 在 store 写失败注入下内存与库不一致（用 `HoldWriterForTest` 或关闭库制造失败）用例红。

- [ ] **Step 7: 提交**

```bash
git add internal/hub/alert
git commit -m "hub: 告警状态机以单调钟计时，重启后未上报节点从启动时刻起算"
```

---

### Task 3: 通知——渠道实现、出站 HTTP、模板、有界投递队列与重试

**Files:**
- Create: `internal/hub/alert/notify.go`、`notify_test.go`、`queue.go`、`queue_test.go`

**Interfaces:**
- Consumes: `store.NotifyChannel`、`store.AlertEvent`、`store.UpdateDelivery`、`store.PendingDeliveries`、`store.GetAlertEvent`；Task 2 的 `Engine.Channels()`、`Sender`。
- Produces（Task 4–5 使用）：

```go
type Message struct { Rule, Node, Kind, Transition, Summary string; Value float64; At time.Time }
type Channel interface { Send(ctx context.Context, m Message) error }
type Retryable interface{ Retryable() bool }           // 错误可实现；HTTP 4xx（非 408/429）与模板错误为不可重试
func NewHTTPClient() *http.Client                       // 10s 超时、禁跟随重定向
func NewTelegram(cfg TelegramConfig, client *http.Client, base string) Channel // base 默认 "https://api.telegram.org"
func NewWebhook(cfg WebhookConfig, client *http.Client) (Channel, error)       // 解析模板
type TelegramConfig struct { BotToken, ChatID string }
type WebhookConfig struct { URL, Method string; Headers map[string]string; BodyTemplate string }
func ParseChannel(c store.NotifyChannel, client *http.Client, telegramBase string) (Channel, error) // 从 store 行构造
const DefaultWebhookTemplate = `{"rule":{{json .Rule}},"node":{{json .Node}},"kind":{{json .Kind}},"transition":{{json .Transition}},"value":{{.Value}},"at":{{.At.Unix}},"summary":{{json .Summary}}}`

type Queue struct{ … }
func NewQueue(st *store.Store, channels func() []store.NotifyChannel, client *http.Client, telegramBase string, clk clock.Clock, sleep func(context.Context, time.Duration) error, log *slog.Logger) *Queue
const QueueCap = 256; const MaxAttempts = 3
var backoff = [...]time.Duration{time.Second, 4 * time.Second}
func (q *Queue) Enqueue(ev store.AlertEvent)                    // 实现 Sender；满则丢最旧并 Warn
func (q *Queue) Requeue(ctx context.Context) error              // Load 后调用：把 PendingDeliveries(MaxAttempts) 入队
func (q *Queue) Run(ctx context.Context)                         // 单 worker：逐投递尝试；每次尝试后 UpdateDelivery；可重试失败按 backoff 睡眠后重试直到 MaxAttempts
func (q *Queue) SendTest(ctx context.Context, c store.NotifyChannel) error // 同步发一条测试消息，不落库
```

- [ ] **Step 1: 失败测试**

```go
func TestWebhookRendersTemplateAndHeaders(t *testing.T)  // httptest 记录方法、头、体；默认模板产出合法 JSON；自定义模板可用 .Node
func TestWebhookRefusesRedirectAndClassifies(t *testing.T) // 302 → 错误且不跟随（记录只收到一次请求）；500 → Retryable true；400 → false；429 → true；超时 → true
func TestTelegramPostsSendMessage(t *testing.T)           // httptest 作 base：路径 /bot<token>/sendMessage，JSON 含 chat_id 与 text；非 200 报错含响应体前 200 字节
func TestResponseBodyIsBounded(t *testing.T)              // 服务端返回 1 MiB 体 → 只读 64 KiB 且成功
func TestQueueDeliversAndRecords(t *testing.T)            // 事件两渠道：一个成功一次、一个先 500 后 200 → delivery 行 attempts/ok/last_error 正确；sleep 桩记录退避 1s
func TestQueueGivesUpAfterMaxAttempts(t *testing.T)       // 持续 500 → attempts 3、ok 0、last_error 非空；不可重试 400 → attempts 1 即停
func TestQueueRequeuesPendingOnLoad(t *testing.T)         // 库里 attempts 1 ok 0 的投递 → Requeue 后被投递
func TestQueueDropsOldestWhenFull(t *testing.T)           // 容量注入为 2：入队 3 个 → 最旧被丢并 Warn
func TestSendTestDoesNotRecord(t *testing.T)
```

- [ ] **Step 2: 运行，确认失败在"未定义"**

- [ ] **Step 3: 实现要点**

- 模板 `funcs`: `json`（`encoding/json` 转义字符串）；解析失败在 `CheckChannel`/`NewWebhook` 时报错。
- HTTP 错误类型 `httpError{status int; body string}` 实现 `Retryable()`：`status >= 500 || status == 408 || status == 429`；`net` 超时与连接错误可重试；3xx 因禁跟随以 `httpError` 报出，不可重试。
- `Run`：从通道取投递项 `{delivery, event}`；查渠道（`channels()` 找不到 → 记 last_error "channel deleted"、attempts=MaxAttempts 终止）；循环尝试；每次 `UpdateDelivery`；成功或不可重试或耗尽即结束。
- worker 退出：`ctx.Done()` 时立即返回，未投递项留在库里（`ok=0`）由下次 `Requeue` 续投。

- [ ] **Step 4: 运行并注入**

- 跟随重定向 → 302 用例红；不设体上限 → 有界用例红；退避表少一档 → 记录退避用例红；`Requeue` 条件 `attempts <= Max` → 用例红。

- [ ] **Step 5: 提交**

```bash
git add internal/hub/alert
git commit -m "hub: 通知投递走有界队列与有限退避，投递结果逐次落库并在重启后续投"
```

---

### Task 4: proto 与 AdminService——八个方法、节点宽限期、引用冲突映射

**Files:**
- Modify: `proto/probe/v1/admin.proto`、`gen/`、`web/src/gen/`、`internal/hub/api/service.go`、`nodes.go`、`nodes_test.go`、`probes.go`、`probes_test.go`
- Create: `internal/hub/api/alerts.go`、`internal/hub/api/alerts_test.go`

**Interfaces:**
- Consumes: Task 2 的 `Engine`（Rules/Channels/States/SaveRule/DeleteRule/SaveChannel/DeleteChannel）、Task 3 的 `Queue.SendTest`、Task 1 的 `store.ListAlertEvents`、`store.ErrInUse`。
- Produces: proto 消息与 RPC；`api.Config.TTL time.Duration`；`api.New(cfg, st, a, l, nodes, book, probes, alerts *alert.Engine, notifier *alert.Queue, clk, log)`。

proto 追加（注释全中文，写取值范围与含义）：

```proto
// Node 增加：
  // 离线告警宽限期（秒）；缺失表示取 PROBE_OFFLINE_AFTER。设置值不得小于 PROBE_OFFLINE_AFTER。
  optional uint32 offline_grace_s = 11;
// UpdateNodeRequest 增加：
  // 0 表示清除（取 PROBE_OFFLINE_AFTER）；非 0 须 ≥ PROBE_OFFLINE_AFTER 的秒数。
  uint32 offline_grace_s = 6;

enum AlertKind { ALERT_KIND_UNSPECIFIED = 0; ALERT_KIND_OFFLINE = 1; ALERT_KIND_PROBE = 2; }
enum ProbeMetric { PROBE_METRIC_UNSPECIFIED = 0; PROBE_METRIC_LOSS_PCT = 1; PROBE_METRIC_RTT_MS = 2; }
message AlertRule {
  int64 id = 1;                 // 0 表示创建
  string name = 2;              // 1–64 个字符
  AlertKind kind = 3;
  bool enabled = 4;
  repeated int64 node_ids = 5;  // 作用域，升序去重；为空表示全部节点，新建的节点自动纳入
  repeated int64 channel_ids = 6;
  // 以下仅探测规则
  uint64 task_id = 7;
  ProbeMetric metric = 8;
  double threshold = 9;         // loss_pct 0–100；rtt_ms > 0
  uint32 for_minutes = 10;      // 1–60：连续多少个已闭合分钟超阈值才触发
  int64 created_at = 11;
}
message ListAlertRulesRequest {}
message ListAlertRulesResponse { repeated AlertRule rules = 1; repeated AlertStateEntry states = 2; }
message AlertStateEntry { int64 rule_id = 1; int64 node_id = 2; string state = 3; int64 since_at = 4; } // state: ok|pending|firing
message SaveAlertRuleRequest { AlertRule rule = 1; }
message SaveAlertRuleResponse { AlertRule rule = 1; }
message DeleteAlertRuleRequest { int64 id = 1; }
message DeleteAlertRuleResponse {}

enum ChannelKind { CHANNEL_KIND_UNSPECIFIED = 0; CHANNEL_KIND_TELEGRAM = 1; CHANNEL_KIND_WEBHOOK = 2; }
message NotifyChannel {
  int64 id = 1; string name = 2; ChannelKind kind = 3;
  TelegramConfig telegram = 4;  // kind 为 TELEGRAM 时必填
  WebhookConfig webhook = 5;    // kind 为 WEBHOOK 时必填
  int64 created_at = 6;
}
message TelegramConfig {
  // 列表响应里恒为空；保存时为空表示保留已存的 token（新建时必填）。
  string bot_token = 1;
  bool has_bot_token = 2;       // 只在响应里有意义
  string chat_id = 3;
}
message WebhookConfig {
  string url = 1;               // http 或 https
  string method = 2;            // POST、PUT、PATCH；空取 POST
  map<string, string> headers = 3; // 至多 16 个
  // text/template；数据字段 Rule、Node、Kind、Transition、Value、At、Summary，函数 json；空取默认 JSON 模板
  string body_template = 4;
}
message ListNotifyChannelsRequest {}
message ListNotifyChannelsResponse { repeated NotifyChannel channels = 1; }
message SaveNotifyChannelRequest { NotifyChannel channel = 1; }
message SaveNotifyChannelResponse { NotifyChannel channel = 1; }
message DeleteNotifyChannelRequest { int64 id = 1; }
message DeleteNotifyChannelResponse {}
message TestNotifyChannelRequest { int64 id = 1; }   // 向已保存的渠道发一条测试消息，同步返回结果
message TestNotifyChannelResponse {}

message ListAlertEventsRequest { int64 node_id = 1; int64 before_id = 2; uint32 limit = 3; } // node_id 0 全部；before_id 0 从最新；limit 0 取 100，最大 500
message ListAlertEventsResponse { repeated AlertEvent events = 1; }
message AlertEvent {
  int64 id = 1; int64 rule_id = 2; int64 node_id = 3;
  string transition = 4;        // firing|recovered
  int64 at = 5; string summary = 6; double value = 7;
  repeated AlertDelivery deliveries = 8;
}
message AlertDelivery { int64 channel_id = 1; uint32 attempts = 2; bool ok = 3; string last_error = 4; optional int64 delivered_at = 5; }
```

RPC：`ListAlertRules`、`SaveAlertRule`、`DeleteAlertRule`、`ListAlertEvents`、`ListNotifyChannels`、`SaveNotifyChannel`、`DeleteNotifyChannel`、`TestNotifyChannel`。

- [ ] **Step 1: 失败测试——`alerts_test.go`（真实处理器 + httptest + 生成客户端，沿用 `probes_test.go` 的夹具）**

```go
func TestAlertRuleCRUD(t *testing.T)                 // 创建离线规则（全部节点）→ List 含之与状态列表为空；更新作用域与渠道；删除后 List 为空
func TestSaveAlertRuleValidationTexts(t *testing.T)  // 表驱动：name 空、kind 未指定、probe 缺 task_id、metric 未指定、loss 阈值 101、for_minutes 0/61、node_ids 不存在（NotFound "rule.node_ids: node 9 does not exist"）、channel_ids 不存在、task 不存在 → 各自 code 与文本前缀
func TestNotifyChannelCRUDHidesToken(t *testing.T)   // 创建 telegram → List 里 bot_token 为空且 has_bot_token；更新时 bot_token 空保留原值（用 SendTest 打到 httptest 断言 token 仍在路径里）
func TestDeleteNotifyChannelInUse(t *testing.T)      // → FailedPrecondition，文本含规则名
func TestDeleteProbeTaskInUse(t *testing.T)          // → FailedPrecondition，文本 "task in use by alert rules: <名>"
func TestTestNotifyChannel(t *testing.T)             // webhook 指向 httptest → 200；指向返回 500 的 → Unavailable 且文本含状态码
func TestListAlertEventsPaging(t *testing.T)         // 直接用 store.RecordTransition 造 3 条 → limit 2 + before_id
func TestUpdateNodeOfflineGraceFloor(t *testing.T)   // TTL 30s：grace 29 → InvalidArgument "offline_grace_s: must be 0 or at least 30"；30 通过；0 清除；ListNodes 回显 optional
```

- [ ] **Step 2: 运行，确认失败在"生成类型不存在 / 方法未实现"**

- [ ] **Step 3: 实现**

- `make gen` 后实现 `alerts.go`：请求 → store 类型（校验用 `alert.CheckRule`/`CheckChannel`，前缀 `rule.`/`channel.`）；错误映射：`alert.ErrInvalid` → InvalidArgument；`store.ErrNotFound` → NotFound，字段名按 `NotFoundError.Kind`（node → `rule.node_ids`，notify channel → `rule.channel_ids`/`id`，probe task → `rule.task_id`）；`store.ErrInUse` → FailedPrecondition；`TestNotifyChannel` 的投递错误 → Unavailable（文本为原文）。
- Telegram 渠道回显：`bot_token` 清空、`has_bot_token` 由存储 config 判定；保存时空 token 且已存在 → 合并旧 token。
- `nodes.go`：`UpdateNode` 校验 `offline_grace_s == 0 || ≥ cfg.TTL 秒`；`Node` 回显 `optional`。
- `probes.go`：`DeleteProbeTask` 增加 `errors.Is(err, store.ErrInUse)` → FailedPrecondition。

- [ ] **Step 4: 运行 api 测试与 `make ci`**

- [ ] **Step 5: 注入**

- 列表不清空 bot_token → 用例红；宽限期下限校验去掉 → 用例红；InUse 映射去掉 → 用例红；`limit` 上限不裁 → 分页用例（limit 1000 → 返回 500 条上限？用 6 条数据断言请求 limit 0 得 6、limit 2 得 2、limit 1000 被接受但仍 6）。

- [ ] **Step 6: 提交**

```bash
git add proto gen web/src/gen internal/hub/api
git commit -m "hub: 管理接口维护告警规则与通知渠道，凭据不回显，删除被引用对象显式拒绝"
```

---

### Task 5: 装配与端到端——后台协程、节点删除清理、e2e 的告警触发与投递

**Files:**
- Modify: `cmd/hub/serve.go`、`cmd/hub/mux_test.go`、`cmd/hub/serve_test.go`、`internal/hub/api/nodes.go`（DeleteNode 后调 `alerts.Forget`）、`scripts/e2e.sh`

**Interfaces:**
- Consumes: `alert.New`、`Engine.Load`、`Engine.SetSender`、`alert.NewQueue`、`Queue.Requeue`、`Queue.Run`、`Engine.SweepOffline`、`Engine.EvaluateProbes`。

- [ ] **Step 1: 装配（`serve.go`）**

```go
	alerts := alert.New(alert.Config{TTL: ttl}, st, l, clk, log)
	notifier := alert.NewQueue(st, alerts.Channels, alert.NewHTTPClient(), "", clk, nil, log)
	alerts.SetSender(notifier)
	if err := errors.Join(a.Load(ctx), svc.Load(ctx), book.Load(ctx), reg.Load(ctx), alerts.Load(ctx), notifier.Requeue(ctx)); err != nil { … }
	admin := api.New(api.Config{ReportInterval: svc.Interval(), TrustedProxies: trusted, TTL: ttl}, st, a, l, svc, book, reg, alerts, notifier, clk, log)
	// 三个后台协程与现有三者同形：各自 context、done 通道，stopBackground 里先取消再等待。
	go alerts.RunOfflineSweep(sweepCtx)        // Engine 方法：ticker 10s → SweepOffline，错误只记日志
	go alerts.RunProbeEvaluation(evalCtx)      // Engine 方法：分钟边界 + 3s → EvaluateProbes(上一分钟起始)
	go notifier.Run(notifyCtx)
```

`RunOfflineSweep`/`RunProbeEvaluation` 在 Task 2 的 `engine.go` 里补上（用 `clk.Now()` 算下一分钟边界，与 `nextFlushAt` 同形：`wall.Truncate(time.Minute).Add(time.Minute + 3*time.Second)`）。注释写明 3 s 晚于刷出的 0.5 s 与维护的 2 s，评估读到的是已刷出的分钟行。

`nodes.go` 的 `DeleteNode` handler：`store.DeleteNode` 成功后依次 `svc.Forget(id)`、`alerts.Forget(id)`。

- [ ] **Step 2: `serve_test.go` 增加用例**

- 启动后三个协程都在跑：用假 webhook（httptest）+ 一条离线规则 + 一个从未上报的节点，把 `PROBE_OFFLINE_AFTER` 设为下限 10s、节点宽限 10s，等待 ≤ 25s 收到 firing 投递（真实时钟；断言 `ListAlertEvents` 里 delivery.ok）。若现有 serve_test 用假时钟则改用注入的时钟推进。

- [ ] **Step 3: e2e（`scripts/e2e.sh`）**

在现有探测断言之后、重启之前追加：

```sh
# webhook 接收器：把每次 POST 的请求体逐行追加到文件；只在本脚本生命周期内存在。
python3 - "$work/hooks.txt" <<'PY' > "$work/hookrecv.log" 2>&1 &
import sys, json
from http.server import BaseHTTPRequestHandler, HTTPServer
out = sys.argv[1]
class H(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        with open(out, "ab") as f: f.write(body + b"\n")
        self.send_response(200); self.end_headers()
    def log_message(self, *a): pass
HTTPServer(("127.0.0.1", 18081), H).serve_forever()
PY
hookrecv=$!
# cleanup 里加 kill "$hookrecv"
[ "$(rpc SaveNotifyChannel "$(jq -nc '{channel: {name: "e2e hook", kind: "CHANNEL_KIND_WEBHOOK", webhook: {url: "http://127.0.0.1:18081/hook"}}}')")" = 200 ] || { echo "FAIL: SaveNotifyChannel"; cat "$work/SaveNotifyChannel.json"; exit 1; }
channel=$(jq -r '.channel.id' "$work/SaveNotifyChannel.json")
[ "$(rpc TestNotifyChannel "$(jq -nc --arg id "$channel" '{id: $id}')")" = 200 ] || { echo "FAIL: TestNotifyChannel"; cat "$work/TestNotifyChannel.json"; exit 1; }
[ "$(rpc SaveAlertRule "$(jq -nc --arg c "$channel" '{rule: {name: "e2e offline", kind: "ALERT_KIND_OFFLINE", enabled: true, channelIds: [$c]}}')")" = 200 ] || { echo "FAIL: SaveAlertRule"; cat "$work/SaveAlertRule.json"; exit 1; }
# 停掉 arm64 容器：PROBE_OFFLINE_AFTER=12s 且节点宽限缺省 = TTL，最多 12s + 一次巡检 10s 触发。
docker stop "$(cat "$work/cid-arm64")" > /dev/null
i=0; until [ "$(rpc ListAlertEvents '{}')" = 200 ] && jq -e --arg n "$node2" '[.events[] | select(.transition == "firing" and .nodeId == $n and any(.deliveries[]; .ok == true))] | length == 1' "$work/ListAlertEvents.json" > /dev/null; do
  i=$((i + 1)); [ "$i" -lt 40 ] || { echo "FAIL: offline alert not delivered"; cat "$work/ListAlertEvents.json"; exit 1; }; sleep 1
done
grep -q '"transition":"firing"' "$work/hooks.txt" || { echo "FAIL: webhook body"; cat "$work/hooks.txt"; exit 1; }
# 恢复：重启 arm64 容器（同一 config 已注册，直接 run），等待 recovered 事件。
run_agent_again arm64 …   # 复用 run_agent 的 run 分支（把注册与运行拆成两段以便复用）
i=0; until [ "$(rpc ListAlertEvents '{}')" = 200 ] && jq -e --arg n "$node2" '[.events[] | select(.transition == "recovered" and .nodeId == $n)] | length == 1' "$work/ListAlertEvents.json" > /dev/null; do
  i=$((i + 1)); [ "$i" -lt 40 ] || { echo "FAIL: recovery not recorded"; cat "$work/ListAlertEvents.json"; exit 1; }; sleep 1
done
```

重启后断言：`ListAlertRules` 仍有 1 条、`ListAlertEvents` 仍 2 条（持久化）；stats 加 `alert_rule=1`、`alert_event=2`、`alert_delivery=2`、`notify_channel=1`。注意 e2e 里 agent 容器用 `timeout 75` 运行，需要把 arm64 的重启放在窗口内或改为不限时并在 cleanup 里 `docker rm -f`（现有 cleanup 已如此）。

- [ ] **Step 4: 运行 `make ci` 与 `make e2e`**

- [ ] **Step 5: 注入**

- `serve.go` 不起 `RunOfflineSweep` → serve_test 用例红；e2e 里去掉 `docker stop` → firing 断言超时红（只作为一次性验证，不入提交）。

- [ ] **Step 6: 提交**

```bash
git add cmd/hub internal/hub/api scripts/e2e.sh
git commit -m "hub: 装配离线巡检、探测评估与投递协程，端到端验证离线告警触发与恢复"
```

---

## 收尾

- 控制端更新 spec：§6.6 表清单加 `alert_rule_node`、`alert_rule_channel`、`alert_delivery`；§9.2 写明离线 pending 的口径（超过 TTL 未到宽限期）与探测缺分钟保持的口径；§9.3 写明重试 3 次、退避 1s/4s、4xx 不重试、禁跟随重定向、未完成投递重启后续投；§3.3 不变。
- 控制端用真实 hub + 两容器做一次手工验收：curl 建渠道与规则 → 停容器 → 事件与 webhook 收到 → 恢复。
- 面板侧（规则与渠道管理、事件列表、节点宽限期编辑）留给下一计划。

## 自检

**Spec 覆盖**：§9.1 两类规则（Task 1/2）；§9.2 状态机、巡检节奏、恢复条件、重启不变式（Task 2/5）；§9.3 两种渠道、有界队列、有限重试、逐次记录、"已通知"口径（Task 3）；§3.3 八个方法（Task 4）；§4.4 宽限期下限（Task 1/4）；§11 投递失败重试并记录（Task 3）；§12 从入口测、假时钟（各任务）。

**类型一致性**：`store.AlertRule`/`NotifyChannel`/`AlertEvent`/`Delivery` 在 Task 1 定义、Task 2–4 使用；`alert.Sender` 在 Task 2 定义、Task 3 的 `Queue` 实现；`api.New` 新签名在 Task 4 定义、Task 5 装配与 `mux_test.go` 同步。

**占位扫描**：无 TBD；每个任务可独立运行与测试。
