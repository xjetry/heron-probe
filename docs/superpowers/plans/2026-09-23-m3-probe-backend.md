# 延迟探测后端与 agent prober Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 管理员在 hub 上定义 ICMP / TCP 探测任务并分配给节点，agent 在每次上报的响应里按版本对账取得任务、按间隔执行、把结果随下一次上报送回，hub 把结果折叠进按 (节点, 分钟, 任务) 键的分钟桶并与指标桶同一条路径落盘、上卷、清理，管理接口可增删改任务并按窗口查询探测历史。

**Architecture:** 存储侧新增探测表族 `probe_1m/5m/1h`（键多一维 `task_id`，值列固定六个），上卷与查询代码从"只认指标描述表"重构成按"表族"参数化的同一套函数，各族有自己的 `rollup_state` 水位；新包 `internal/hub/probe` 是任务、分配与版本号的唯一写入口并在内存里缓存每节点任务清单，`ingest` 用它对账与校验结果归属；`live` 为每节点增加探测桶，`Flush`/`Drain` 一次交出两族行，写协程在同一事务里写两族并各按自己的水位做冻结检查；agent 新包 `internal/agent/prober` 每地址族一个共享 ICMP socket（非特权数据报优先、raw 回退）、单读协程按 payload 匹配回包、每任务一个定时协程、有界结果队列按单调钟折算 `age_ms`；`AdminService` 新增任务增删改查与 `QueryProbes`。前端在下一份计划里做。

**Tech Stack:** Go 1.27（stdlib、modernc.org/sqlite、connect-go、`golang.org/x/net` v0.57.0 的 `icmp`/`ipv4`/`ipv6`）、protobuf/buf、Docker 双架构 e2e。

**Spec:** `docs/superpowers/specs/2026-09-17-probe-architecture-design.md` —— §4.2（`ReportRequest.probe_results`/`tasks_version`、`ReportResponse.tasks`、`Facts.icmp_available`、`ProbeResult` 三态）、§4.3（电平触发对账）、§6.3（探测表）、§6.4（两族各自水位与冻结不变式）、§6.5（保留与选级）、§8（任务与版本、执行、对账与入库、硬限制）、§11、§12、§14（systemd 单元的 `AmbientCapabilities=CAP_NET_RAW`）。计划从 spec 论证；冲突时以 spec 为准，本计划对 spec 的两处偏离在下面"对 spec 的修订"里说明并随计划提交回写 spec。

## Global Constraints

- 上报路径只碰内存（§6.1）：`Report` 里探测结果只折叠进 `live` 的内存桶，落盘由分钟刷出承担。
- 结果准入（§8.3，逐字）：`task_id` 必须分配给本节点，否则丢弃并记日志；`age_ms ≤ MAX_AGE`（120s，`ingest.MaxProbeAge`），超龄丢弃；测量时刻 = 收到时刻 − `age_ms`；桶键 `(node_id, 分钟, task_id)`；`sent` 每条加一，`timeout` 计入 `lost`，`error` 计入 `errors`，`rtt_us` 累加到 `rtt_sum` 并更新 `rtt_min`/`rtt_max`。
- 结构非法整条拒绝（§11）：任一 `ProbeResult` 缺 `outcome`、或 `rtt_us` 超过最大超时（5 000 000 µs）→ `InvalidArgument`，`live`、流量基线、探测桶均不变。
- 版本对账（§8.1、§8.3）：任务或分配的任何修改经 `probe` 包唯一写入口，在同一事务内把 `probe_meta.version` 加一；版本全局唯一；`Report` 只比较两个整数，不一致时响应携带该节点的 `ProbeTasks`（版本 + 分配给它的任务，可能为空列表）。
- 冻结不变式（§6.4）：写协程拒绝 `ts` 早于本族 5m 水位的 1m 写入；指标族水位键 `5m`/`1h`，探测族 `probe_5m`/`probe_1h`；上卷只推进到 `桶结束 < now − RollupLag` 的桶；`ingest` 拒收超龄结果。`RollupLag ≥ MaxProbeAge + FlushPeriod + 60s` 的编译期断言已存在，不改常量。
- 硬限制（§8.4，两侧各自断言）：探测间隔 ≥ 5s、每节点任务数 ≤ 64、单次探测 1 个包、超时 ≤ 5s。常量与单任务字段检查在叶子包 `internal/probelimit` 定义一次，hub 保存前与 agent 应用前各自调用并各自有测试。
- ICMP 执行（§8.2 + spike 结论）：优先非特权数据报 socket（`udp4`/`udp6`），不可用时退到 raw（`ip4:icmp`/`ip6:ipv6-icmp`），都不可用则每次回报 `error` 并说明原因；启动时探测可用性写入 `Facts.icmp_available`。回包匹配只靠 payload（进程 nonce + task_id + seq），不靠 ICMP ID；读侧只接受 Echo Reply 类型。
- 队列与 age（§8.2）：结果进有界队列，上报时整体取走并按单调钟折算 `age_ms`；队列满丢最旧并计数；上报失败的结果放回队列，取走时丢弃已超过 `MAX_AGE` 的；任务集更新时停掉消失的、启动新增的，未变化的不重启计时；首次触发加随机偏移。
- 删除任务不删已有历史（§8.3），到期由 prune 清理；`QueryProbes` 对这些行只带 `task_id`。删除节点时同一事务删除它的探测历史与分配行，内存里的分配清单随 `Forget` 清空。
- 写协程不变式（store）：所有写经 `Store.write`/`writeAsync` 串行；从属行写入在事务内检查节点存在。迁移不变式：`schemaStatements` 永远是当前版本的完整 DDL，迁移后的库与全新库经 `PRAGMA table_info` 逐表相同。
- 测试从用户可见入口打（§12）：`ingest`/`api` 经真实 Connect 处理器 + `httptest` + 生成客户端；匿名枚举测试自动覆盖新方法。
- 项目规则：注释与提交信息只写 WHY 与不变式，不得出现任务编号、里程碑代号、审阅轮次、"按计划 / brief"等过程信息；不打补丁（根因层修、同形状一次改齐）；每条新断言做缺陷注入并确认红在正确原因；判成败的命令不接管道（`cmd > log 2>&1; echo $?`）；`go test -count=1`；`make ci` 含 `gen`（生成物与工作树一致）、`lint`、`test`、`web-test`、`web`、`build`。

## 实验结论（spike，2026-09-23，报告 `/tmp/probe-icmp-spike/report.md`）

实现直接依赖下列事实，全部来自实测（macOS 26.3 / OrbStack Linux 7.0 / Debian 12 容器 amd64 与 arm64 / Go 1.27.1 / x/net v0.57.0）：

1. macOS 非 root：`udp4`/`udp6` 可创建并完成回环往返；`ip4:icmp`/`ip6:ipv6-icmp` 创建即 `operation not permitted`。
2. Linux：`udp4`/`udp6` 只受 `net.ipv4.ping_group_range` 管，v4/v6 同一开关，与 CAP_NET_RAW 无关（root 去掉 NET_RAW 仍可用；范围 `1 0` 时 root 带 NET_RAW 也是 EACCES）。Docker 默认容器为 `0 2147483647`；内核新网络命名空间默认 `1 0`；Debian 12 的 systemd 包（252.39）不带 `50-default.conf`，裸机 Debian 默认很可能不可用。raw 需要有效的 CAP_NET_RAW；文件能力 + bounding set 缺位时 exec 阶段即失败。
3. ID 语义：Linux 数据报 socket 的回包 ID 被内核改成本地端口；macOS 回环保留发送值，但公网回包 ID 被改写。因此匹配只能靠 payload。
4. x/net v0.57.0 在 Darwin 对 `udp4` 已设 `IP_STRIPHDR`，`ReadFrom` 缓冲不带 IPv4 头；`udp6` 两边都不带头。不要手工剥头。
5. macOS 同进程两个 `udp4` socket 会互相收到对方的 Echo Reply；Linux 按端口分流。raw socket 与 macOS `udp6` 会先读到自己发出的 Echo Request。读侧必须按 Type 过滤、按 payload 匹配、忽略陌生 payload。
6. 向 `192.0.2.1` 探测在 2s 窗口内只观察到超时，没有 ICMP 错误报文；不把 ICMP 错误报文当作可依赖的错误分类来源。

## 对 spec 的修订（随本计划提交回写）

- §6.3 `rtt_min_us`/`rtt_max_us` 改为可空：桶内没有任何 rtt 样本（全部丢包或错误）时为 NULL，`min()`/`max()` 聚合自动忽略 NULL；`NOT NULL DEFAULT 0` 会让 0 参与最小值而污染上卷。
- §8.3 `QueryProbes` 的每任务序列改为样本消息 `ProbeSample{ts, sent, lost, errors, optional rtt_mean_us, optional rtt_min_us, optional rtt_max_us}`，而不是七个平行数组：rtt 三项在 `sent − lost − errors = 0` 的桶里缺失，`optional` 在协议层表达缺失（agent-first：不用零值冒充）。
- §8.2 补一句：每地址族一个共享 socket、单读协程按 payload 分发（spike 第 3、5 条）。§14 的 `AmbientCapabilities=CAP_NET_RAW` 从"可选"改为单元默认（spike 第 2 条）；单元文件本身属 M6，本计划只改措辞。

## 文件结构

| 文件 | 职责 |
|---|---|
| `internal/hub/metric/probe.go` | 探测桶 `ProbeBucket`（可加折叠）、`ProbeRow`、两族一起刷出的 `Batch` |
| `internal/hub/store/schema.go` | `probeDDL`、`ddlProbeTask`、`ddlProbeTaskNode`、`ddlProbeMeta`、`probeTables`、`schemaStatements` 加入探测族 |
| `internal/hub/store/store.go` | `schemaVersion = 4`；迁移 4 |
| `internal/hub/store/rollup.go` | `family` 抽象：上卷、prune、查询按表族参数化；`QueryProbes` |
| `internal/hub/store/metric.go` | `WriteMinuteRows(ctx, metric.Batch)`：同一事务写两族，各按自己的水位冻结检查 |
| `internal/hub/store/probe.go` | 探测行的 upsert / 扫描 / 聚合 SQL；任务与分配的事务：`LoadProbeTasks`、`SaveProbeTask`、`DeleteProbeTask` |
| `internal/hub/store/node.go` | `DeleteNode`/`Counts` 含探测表与分配表 |
| `internal/probelimit/limit.go` | 硬限制常量与 `CheckTask`（hub 与 agent 共用的叶子包） |
| `internal/hub/probe/registry.go` | `Registry`：版本与每节点任务清单的内存缓存；`Load`、`Version`、`TasksFor`、`Assigned`、`List`、`Save`、`Delete`、`Forget` |
| `internal/hub/live/live.go` | 每节点探测桶；`AddProbe`；`Flush`/`Drain` 返回 `metric.Batch` |
| `internal/hub/ingest/service.go`、`validate.go`、`flush.go` | 结果校验与折叠、版本对账、两族同批刷出、`Forget` 清分配清单 |
| `cmd/hub/serve.go` | 装配 `probe.Registry` |
| `proto/probe/v1/admin.proto` | `ListProbeTasks`、`SaveProbeTask`、`DeleteProbeTask`、`QueryProbes` 及消息 |
| `internal/hub/api/probes.go` | 上述四个方法 |
| `internal/agent/prober/limits.go`、`queue.go`、`scheduler.go`、`icmp.go`、`tcp.go` | agent 侧硬限制、有界队列、调度、ICMP 与 TCP 引擎 |
| `internal/agent/client/runner.go` | 上报携带版本与结果，响应里的任务交给调度器 |
| `internal/agent/collect/collect.go` | `Facts.icmp_available` 由 prober 初始化结果决定 |
| `cmd/agent/main.go` | 装配 prober |
| `scripts/e2e.sh` | 建任务、查结果、重启后任务与历史仍在 |

任务顺序：1 存储 → 2 任务注册表 → 3 上报路径 → 4 管理接口 → 5 agent prober → 6 agent 接入与 e2e。每个任务结束时 `make ci` 绿；Task 4 与 Task 6 结束时另跑 `make e2e`。

---

### Task 1: 存储——探测桶、schema v4、表族化的上卷 / 清理 / 查询、两族同批写入

**Files:**
- Create: `internal/hub/metric/probe.go`、`internal/hub/store/probe.go`、`internal/hub/store/probe_test.go`
- Modify: `internal/hub/store/schema.go`、`internal/hub/store/store.go`（`schemaVersion`、`migrations[4]`）、`internal/hub/store/rollup.go`（`family`）、`internal/hub/store/metric.go`（`WriteMinuteBatch`）、`internal/hub/store/node.go`（`DeleteNode`/`Counts`）
- Test: `internal/hub/store/migrate_test.go`（v3→v4 用例）、`internal/hub/store/rollup_test.go`（探测族用例）、`internal/hub/store/store_test.go`（`TestDeleteNodeClearsEveryLevel` 加探测表）

**Interfaces:**
- Consumes: `metric.Row`/`metric.Bucket`、`Store.write`、`nodeExistsTx`、`rebuildTable`（不用）、`Level`/`levels`/`ChooseLevel`（保持签名）。
- Produces（后续任务依赖，名字与签名逐字）：
  - `metric.ProbeBucket{Sent, Lost, Errors uint32; RttSumUs uint64; RttN, RttMinUs, RttMaxUs uint32}`，`(*ProbeBucket).Add(*probev1.ProbeResult)`，`(*ProbeBucket).Merge(*ProbeBucket)`
  - `metric.ProbeRow{NodeID, TS int64; TaskID uint64; Bucket *ProbeBucket}`
  - `metric.Batch{Rows []Row; Probes []ProbeRow}`，`(Batch).Empty() bool`
  - `(*Store).WriteMinuteBatch(ctx, metric.Batch) (rejected int, err error)`；`WriteMinuteRows(ctx, []metric.Row)` 保留为 `WriteMinuteBatch(ctx, metric.Batch{Rows: rows})` 的包装
  - `(*Store).QueryProbes(ctx, nodeID, from, to int64, lv Level, step int64) ([]metric.ProbeRow, error)`（结果按 `TaskID`、`TS` 升序）
  - `(*Store).LoadProbeTasks(ctx) (version uint64, tasks []ProbeTaskRecord, err error)`；`ProbeTaskRecord{Task *probev1.ProbeTask; NodeIDs []int64}`
  - `(*Store).SaveProbeTask(ctx, t *probev1.ProbeTask, nodeIDs []int64) (saved *probev1.ProbeTask, version uint64, err error)`；`(*Store).DeleteProbeTask(ctx, id uint64) (version uint64, err error)`
  - 哨兵：`ErrNodeLimit = errors.New("node already has the maximum number of probe tasks")`；节点不存在用 `fmt.Errorf("%w: node %d", ErrNotFound, id)`（`errors.Is(err, ErrNotFound)` 成立且文本带节点号）
  - `store.probeTables = []string{"probe_1m", "probe_5m", "probe_1h"}`

- [ ] **Step 1: 探测桶（`internal/hub/metric/probe.go`）**

```go
package metric

import probev1 "github.com/xjetry/probe/gen/probe/v1"

// ProbeBucket 是一分钟内某任务探测结果的可加折叠。
//
// RttN 是带 rtt 的结果数，恒等于 Sent − Lost − Errors；RttN 为 0 时 RttMinUs / RttMaxUs
// 没有含义，落库为 NULL——若落成 0，上卷的 min() 会把"没有样本"当成 0 µs。
type ProbeBucket struct {
	Sent, Lost, Errors uint32
	RttSumUs           uint64
	RttN               uint32
	RttMinUs, RttMaxUs uint32
}

func (b *ProbeBucket) Add(r *probev1.ProbeResult) {
	b.Sent++
	switch o := r.GetOutcome().(type) {
	case *probev1.ProbeResult_RttUs:
		b.addRtt(o.RttUs)
	case *probev1.ProbeResult_Timeout:
		b.Lost++
	case *probev1.ProbeResult_Error:
		b.Errors++
	}
}

func (b *ProbeBucket) addRtt(us uint32) {
	if b.RttN == 0 || us < b.RttMinUs {
		b.RttMinUs = us
	}
	if b.RttN == 0 || us > b.RttMaxUs {
		b.RttMaxUs = us
	}
	b.RttSumUs += uint64(us)
	b.RttN++
}

// Merge 把 o 加进 b；与写库时的 ON CONFLICT 合并是同一种运算。
func (b *ProbeBucket) Merge(o *ProbeBucket) {
	b.Sent += o.Sent
	b.Lost += o.Lost
	b.Errors += o.Errors
	if o.RttN > 0 {
		if b.RttN == 0 || o.RttMinUs < b.RttMinUs {
			b.RttMinUs = o.RttMinUs
		}
		if b.RttN == 0 || o.RttMaxUs > b.RttMaxUs {
			b.RttMaxUs = o.RttMaxUs
		}
		b.RttSumUs += o.RttSumUs
		b.RttN += o.RttN
	}
}

// RttMean 的第二个返回值为 false 表示桶内没有任何 rtt 样本。
func (b *ProbeBucket) RttMean() (uint32, bool) {
	if b.RttN == 0 {
		return 0, false
	}
	return uint32(b.RttSumUs / uint64(b.RttN)), true
}

// ProbeRow 是一个 (节点, 分钟, 任务) 桶：live 刷出，store 也用它返回上卷与查询聚合后的桶。
type ProbeRow struct {
	NodeID int64
	TS     int64
	TaskID uint64
	Bucket *ProbeBucket
}

// Batch 是一次刷出的全部行：指标桶与探测桶来自同一批闭合的分钟，由写协程在同一事务落盘。
type Batch struct {
	Rows   []Row
	Probes []ProbeRow
}

func (b Batch) Empty() bool { return len(b.Rows) == 0 && len(b.Probes) == 0 }
```

- [ ] **Step 2: schema v4（`schema.go`、`store.go`）**

在 `schema.go` 追加：

```go
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

// probe_meta.version 是任务与分配的全局版本：任何修改都在同一事务里加一，
// agent 只比较这个整数（§8.1）。单行表，CHECK 让第二行无法插入。
const ddlProbeMeta = `CREATE TABLE probe_meta (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  version INTEGER NOT NULL
)`

const seedProbeMeta = `INSERT INTO probe_meta (id, version) VALUES (1, 0)`

const seedProbeRollupState = `INSERT INTO rollup_state (level, upto_ts) VALUES ('probe_5m', 0), ('probe_1h', 0)`
```

`schemaStatements` 改为：

```go
func schemaStatements() []string {
	out := []string{ddlNode, ddlNodeFacts, ddlRegisterWindow, ddlRollupState, seedRollupState, seedProbeRollupState,
		ddlAdmin, ddlAdminSession, ddlTraffic, ddlProbeTask, ddlProbeTaskNode, ddlProbeTaskNodeIndex, ddlProbeMeta, seedProbeMeta}
	for _, t := range metricTables {
		out = append(out, metricDDL(t))
	}
	for _, t := range probeTables {
		out = append(out, probeDDL(t))
	}
	return out
}
```

`store.go`：`const schemaVersion = 4`，`migrations[4]`：

```go
	4: func(tx *sql.Tx) error {
		stmts := []string{ddlProbeTask, ddlProbeTaskNode, ddlProbeTaskNodeIndex, ddlProbeMeta, seedProbeMeta, seedProbeRollupState}
		for _, t := range probeTables {
			stmts = append(stmts, probeDDL(t))
		}
		for _, stmt := range stmts {
			if _, err := tx.Exec(stmt); err != nil {
				return fmt.Errorf("%w in %q", err, stmt)
			}
		}
		return nil
	},
```

- [ ] **Step 3: 迁移测试先红后绿（`migrate_test.go`）**

仿照 `TestMigrationFromV2MatchesFreshSchemaAndKeepsRows` 加 `TestMigrationFromV3MatchesFreshSchemaAndKeepsRows`：用 v3 的完整 DDL 建库（把当前 `schemaStatements()` 去掉探测族的版本写成测试内的 `v3Statements()`——注意 v3 的 `rollup_state` 只有两行种子）、`seedMinuteRow` 播一行、`migrateFrom(t, v3Statements(), 3, seedMinuteRow)`，断言 `describe(migrated) == describe(fresh)`、`userVersion == 4`、分钟行仍在、`rollup_state` 有 `probe_5m`/`probe_1h` 两行且为 0、`probe_meta.version == 0`。

Run: `go test -count=1 ./internal/hub/store -run 'TestMigrationFromV3' > /tmp/m3b-t1-migrate.log 2>&1; echo $?`
Expected: 先红（无迁移 4 → "no migration to schema version 4"），实现 Step 2 后绿。

- [ ] **Step 4: 表族抽象（`rollup.go`）**

把"表来自 `Level.Table`/`Level.Source`、列来自 `metric.Columns`"改成按族参数化。`Level` 去掉 `Table`/`Source`，只保留 `Name`、`Bucket`；`levels` 仍是 `1m/5m/1h` 三级；族描述：

```go
// family 描述一个时间序列表族：三级表、各级水位在 rollup_state 里的键、键列与值列的
// SQL 片段。上卷、清理、查询对两族是同一种运算，只有这些片段不同；把它们收进一个值，
// 新增一族不需要复制任何函数。
type family struct {
	name string
	// tables / states 与 levels 同序；states[0] 为空——最细一级没有水位。
	tables []string
	states []string
	// extraKey 是 node_id、ts 之外的键列（探测族为 task_id），为空则没有。
	extraKey string
	values   func() []string
	aggs     func() []string
}

var metricFamily = &family{name: "metric", tables: metricTables, states: []string{"", "5m", "1h"},
	values: metricColumnNames, aggs: aggregates}

var probeFamily = &family{name: "probe", tables: probeTables, states: []string{"", "probe_5m", "probe_1h"},
	extraKey: "task_id", values: probeValueColumns, aggs: probeAggregates}

// families 的顺序无关：两族各有水位，互不牵制（§6.4）。
var families = []*family{metricFamily, probeFamily}

func (f *family) keys() []string {
	k := []string{"node_id", "ts"}
	if f.extraKey != "" {
		k = append(k, f.extraKey)
	}
	return k
}

// groupBy 是"整桶重算"的分组键：ts 对齐到桶长，其余键原样。
func (f *family) groupBy(bucket string) string {
	g := "node_id, ts - ts % " + bucket
	if f.extraKey != "" {
		g += ", " + f.extraKey
	}
	return g
}

func (f *family) rollupSQL(i int) string {
	b := fmt.Sprint(levels[i].Bucket)
	return "INSERT OR REPLACE INTO " + f.tables[i] + " (" + strings.Join(append(f.keys(), f.values()...), ", ") + ") " +
		"SELECT " + f.groupBy(b) + ", " + strings.Join(f.aggs(), ", ") +
		" FROM " + f.tables[i-1] + " WHERE node_id IN (SELECT id FROM node) AND ts >= ? AND ts < ? GROUP BY " + f.groupBy(b)
}
```

`Rollup` 对每族独立走一遍现有逻辑（`lowerUpto` 每族重置），`rollupLevel(ctx, f, i, limit)` 用 `f.states[i]` 读写水位、`f.tables[i-1]` 做 `first` 探测、`f.rollupSQL(i)` 写入；`rollupSlice`/`pruneSlice` 仍按级名。`Prune` 外层多一层 `for _, f := range families`，消费水位取 `f.states[i+1]`。查询：

```go
// aggregateSQL 是查询时的二次分桶：与上卷同一种运算，步长作为绑定参数。
// 探测族多按 task_id 分组并先按任务再按时间排序，调用方据此切成每任务一条序列。
func (f *family) aggregateSQL(table string) string {
	sel, group, order := "ts - ts % ?", "1", "1"
	if f.extraKey != "" {
		sel += ", " + f.extraKey
		group, order = "1, 2", "2, 1"
	}
	return "SELECT " + sel + ", " + strings.Join(f.aggs(), ", ") + " FROM " + table +
		" WHERE node_id = ? AND ts >= ? AND ts <= ? GROUP BY " + group + " ORDER BY " + order
}
```

`QueryMetrics` 的对齐逻辑抽成 `alignWindow(from, to, step) (from, to int64)` 供两个查询共用；`QueryMetrics` 用 `metricFamily`，新增：

```go
// QueryProbes 与 QueryMetrics 同一套选级与对齐；每任务的桶按 TaskID、TS 升序返回。
func (s *Store) QueryProbes(ctx context.Context, nodeID int64, from, to int64, lv Level, step int64) ([]metric.ProbeRow, error) {
	if err := checkStep(lv, step); err != nil {
		return nil, err
	}
	from, to = alignWindow(from, to, step)
	rows, err := s.r.QueryContext(ctx, probeFamily.aggregateSQL(probeFamily.tables[levelIndex(lv)]), step, nodeID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanProbeRows(rows, nodeID)
}
```

`levelIndex(lv Level) int` 按 `Name` 在 `levels` 里找下标；`readLevel` 等测试辅助随 `Level` 字段变化改为按族取表。

- [ ] **Step 5: 探测行的 SQL 与任务事务（`store/probe.go`）**

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/metric"
	"github.com/xjetry/probe/internal/probelimit"
)

var ErrNodeLimit = errors.New("node already has the maximum number of probe tasks")

func probeValueColumns() []string {
	return []string{"sent", "lost", "errors", "rtt_sum_us", "rtt_min_us", "rtt_max_us"}
}

// 聚合 min()/max() 忽略 NULL：没有 rtt 样本的桶不会把 0 带进最小值。
func probeAggregates() []string {
	return []string{"sum(sent)", "sum(lost)", "sum(errors)", "sum(rtt_sum_us)", "min(rtt_min_us)", "max(rtt_max_us)"}
}

// 标量 min(a, b) 在任一参数为 NULL 时返回 NULL，所以两侧先各自 coalesce：
// 双方都 NULL 得 NULL，一方 NULL 得另一方，都有值取更小者；max 同理。
const upsertProbeMinute = `INSERT INTO probe_1m (node_id, ts, task_id, sent, lost, errors, rtt_sum_us, rtt_min_us, rtt_max_us)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (node_id, ts, task_id) DO UPDATE SET
  sent = sent + excluded.sent, lost = lost + excluded.lost, errors = errors + excluded.errors,
  rtt_sum_us = rtt_sum_us + excluded.rtt_sum_us,
  rtt_min_us = min(coalesce(rtt_min_us, excluded.rtt_min_us), coalesce(excluded.rtt_min_us, rtt_min_us)),
  rtt_max_us = max(coalesce(rtt_max_us, excluded.rtt_max_us), coalesce(excluded.rtt_max_us, rtt_max_us))`

func probeArgs(r metric.ProbeRow) []any {
	b := r.Bucket
	var mn, mx any
	if b.RttN > 0 {
		mn, mx = int64(b.RttMinUs), int64(b.RttMaxUs)
	}
	return []any{r.NodeID, r.TS, int64(r.TaskID), int64(b.Sent), int64(b.Lost), int64(b.Errors), int64(b.RttSumUs), mn, mx}
}

// scanProbeRows 扫描 (ts, task_id, 六个值列)。RttN 由 sent − lost − errors 推出，
// 与 rtt_min_us 是否为 NULL 必须一致：写侧 RttN 为 0 时才写 NULL。
func scanProbeRows(rows *sql.Rows, nodeID int64) ([]metric.ProbeRow, error) {
	var out []metric.ProbeRow
	for rows.Next() {
		var ts, taskID, sent, lost, errs, sum int64
		var mn, mx sql.NullInt64
		if err := rows.Scan(&ts, &taskID, &sent, &lost, &errs, &sum, &mn, &mx); err != nil {
			return nil, err
		}
		b := &metric.ProbeBucket{Sent: uint32(sent), Lost: uint32(lost), Errors: uint32(errs), RttSumUs: uint64(sum)}
		if mn.Valid {
			b.RttN = uint32(sent - lost - errs)
			b.RttMinUs, b.RttMaxUs = uint32(mn.Int64), uint32(mx.Int64)
		}
		out = append(out, metric.ProbeRow{NodeID: nodeID, TS: ts, TaskID: uint64(taskID), Bucket: b})
	}
	return out, rows.Err()
}

type ProbeTaskRecord struct {
	Task    *probev1.ProbeTask
	NodeIDs []int64
}

// LoadProbeTasks 读全部任务与分配及当前版本，供注册表在启动时重建内存缓存。
func (s *Store) LoadProbeTasks(ctx context.Context) (uint64, []ProbeTaskRecord, error) {
	var version int64
	if err := s.r.QueryRowContext(ctx, "SELECT version FROM probe_meta WHERE id = 1").Scan(&version); err != nil {
		return 0, nil, err
	}
	rows, err := s.r.QueryContext(ctx, "SELECT id, kind, target, interval_s, timeout_ms FROM probe_task ORDER BY id")
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	var out []ProbeTaskRecord
	index := map[uint64]int{}
	for rows.Next() {
		t := &probev1.ProbeTask{}
		var id, kind, interval, timeout int64
		if err := rows.Scan(&id, &kind, &t.Target, &interval, &timeout); err != nil {
			return 0, nil, err
		}
		t.Id, t.Kind, t.IntervalS, t.TimeoutMs = uint64(id), probev1.ProbeKind(kind), uint32(interval), uint32(timeout)
		index[t.Id] = len(out)
		out = append(out, ProbeTaskRecord{Task: t})
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	assign, err := s.r.QueryContext(ctx, "SELECT task_id, node_id FROM probe_task_node ORDER BY task_id, node_id")
	if err != nil {
		return 0, nil, err
	}
	defer assign.Close()
	for assign.Next() {
		var taskID, nodeID int64
		if err := assign.Scan(&taskID, &nodeID); err != nil {
			return 0, nil, err
		}
		if i, ok := index[uint64(taskID)]; ok {
			out[i].NodeIDs = append(out[i].NodeIDs, nodeID)
		}
	}
	return uint64(version), out, assign.Err()
}

// SaveProbeTask 在一个事务里写任务、整份替换分配、把版本加一。分配行写入前检查节点存在
// 与每节点上限：上限在这里而不是调用方检查，因为只有事务内的计数才与其他保存互斥。
func (s *Store) SaveProbeTask(ctx context.Context, t *probev1.ProbeTask, nodeIDs []int64) (*probev1.ProbeTask, uint64, error) {
	saved := &probev1.ProbeTask{Id: t.GetId(), Kind: t.GetKind(), Target: t.GetTarget(), IntervalS: t.GetIntervalS(), TimeoutMs: t.GetTimeoutMs()}
	var version int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		if saved.Id == 0 {
			res, err := tx.Exec("INSERT INTO probe_task (kind, target, interval_s, timeout_ms, created_at) VALUES (?, ?, ?, ?, ?)",
				int64(saved.Kind), saved.Target, int64(saved.IntervalS), int64(saved.TimeoutMs), s.clk.Now().Unix())
			if err != nil {
				return err
			}
			id, err := res.LastInsertId()
			if err != nil {
				return err
			}
			saved.Id = uint64(id)
		} else {
			res, err := tx.Exec("UPDATE probe_task SET kind = ?, target = ?, interval_s = ?, timeout_ms = ? WHERE id = ?",
				int64(saved.Kind), saved.Target, int64(saved.IntervalS), int64(saved.TimeoutMs), int64(saved.Id))
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return fmt.Errorf("%w: probe task %d", ErrNotFound, saved.Id)
			}
			if _, err := tx.Exec("DELETE FROM probe_task_node WHERE task_id = ?", int64(saved.Id)); err != nil {
				return err
			}
		}
		for _, nodeID := range nodeIDs {
			exists, err := nodeExistsTx(tx, nodeID)
			if err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("%w: node %d", ErrNotFound, nodeID)
			}
			var n int
			if err := tx.QueryRow("SELECT COUNT(*) FROM probe_task_node WHERE node_id = ?", nodeID).Scan(&n); err != nil {
				return err
			}
			if n >= probelimit.MaxTasksPerNode {
				return fmt.Errorf("%w: node %d", ErrNodeLimit, nodeID)
			}
			if _, err := tx.Exec("INSERT INTO probe_task_node (task_id, node_id) VALUES (?, ?)", int64(saved.Id), nodeID); err != nil {
				return err
			}
		}
		return tx.QueryRow("UPDATE probe_meta SET version = version + 1 WHERE id = 1 RETURNING version").Scan(&version)
	})
	if err != nil {
		return nil, 0, err
	}
	return saved, uint64(version), nil
}

// DeleteProbeTask 删任务与分配并加版本；历史行不删（§8.3），到期由 prune 清理。
func (s *Store) DeleteProbeTask(ctx context.Context, id uint64) (uint64, error) {
	var version int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec("DELETE FROM probe_task WHERE id = ?", int64(id))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: probe task %d", ErrNotFound, id)
		}
		if _, err := tx.Exec("DELETE FROM probe_task_node WHERE task_id = ?", int64(id)); err != nil {
			return err
		}
		return tx.QueryRow("UPDATE probe_meta SET version = version + 1 WHERE id = 1 RETURNING version").Scan(&version)
	})
	return uint64(version), err
}
```

`nodeIDs` 里的重复 id 由调用方（注册表）去重排序后传入；本函数遇到重复会因主键冲突返回错误，测试钉住"去重是注册表的责任"。`probelimit` 包在 Task 2 才创建：本任务先只放常量文件 `internal/probelimit/limit.go`（`MaxTasksPerNode = 64` 等七个常量，见 Task 2 Step 1；`import "time"`），`CheckTask` 留给 Task 2。

- [ ] **Step 6: 同批写入（`store/metric.go`）**

```go
// WriteMinuteBatch 是两族 1m 行的唯一写入口：同一事务里写指标行与探测行，节点存在性
// 在事务内检查（与 DeleteNode 串行），各族按自己的 5m 水位做冻结检查——两族各自上卷，
// 一族的水位不能替另一族裁决。被拒绝的行计数返回并记日志，其余行照常写入。
func (s *Store) WriteMinuteBatch(ctx context.Context, batch metric.Batch) (int, error) {
	rejected := 0
	err := s.write(ctx, func(tx *sql.Tx) error {
		var metricUpto, probeUpto int64
		if err := tx.QueryRow("SELECT upto_ts FROM rollup_state WHERE level = '5m'").Scan(&metricUpto); err != nil {
			return err
		}
		if err := tx.QueryRow("SELECT upto_ts FROM rollup_state WHERE level = 'probe_5m'").Scan(&probeUpto); err != nil {
			return err
		}
		existing := map[int64]bool{}
		exists := func(nodeID int64) (bool, error) { /* 现有的按节点缓存逻辑 */ }
		for _, r := range batch.Rows {
			// 现有 WriteMinuteRows 的循环体，水位用 metricUpto
		}
		for _, r := range batch.Probes {
			ok, err := exists(r.NodeID)
			if err != nil {
				return err
			}
			if !ok {
				rejected++
				s.log.Warn("probe row for deleted node dropped", "node", r.NodeID, "task", r.TaskID)
				continue
			}
			if r.TS < probeUpto {
				rejected++
				s.log.Warn("probe row before rollup watermark dropped", "node", r.NodeID, "task", r.TaskID, "ts", r.TS, "watermark", probeUpto)
				continue
			}
			if _, err := tx.Exec(upsertProbeMinute, probeArgs(r)...); err != nil {
				return err
			}
		}
		return nil
	})
	return rejected, err
}

func (s *Store) WriteMinuteRows(ctx context.Context, rows []metric.Row) (int, error) {
	return s.WriteMinuteBatch(ctx, metric.Batch{Rows: rows})
}
```

- [ ] **Step 7: 节点删除与计数（`node.go`）**

`DeleteNode` 在 `metricTables` 循环后追加 `probeTables` 循环与 `DELETE FROM probe_task_node WHERE node_id = ?`；`Counts` 的表清单追加 `probeTables...`、`"probe_task"`、`"probe_task_node"`。

- [ ] **Step 8: 测试（`store/probe_test.go`，另改 `rollup_test.go`、`store_test.go`）**

```go
func probeRow(nodeID int64, ts int64, task uint64, rtts []uint32, lost, errs uint32) metric.ProbeRow {
	b := &metric.ProbeBucket{}
	for _, us := range rtts {
		b.Add(&probev1.ProbeResult{TaskId: task, Outcome: &probev1.ProbeResult_RttUs{RttUs: us}})
	}
	for range lost {
		b.Add(&probev1.ProbeResult{TaskId: task, Outcome: &probev1.ProbeResult_Timeout{Timeout: &probev1.Timeout{}}})
	}
	for range errs {
		b.Add(&probev1.ProbeResult{TaskId: task, Outcome: &probev1.ProbeResult_Error{Error: &probev1.ProbeError{Message: "x"}}})
	}
	return metric.ProbeRow{NodeID: nodeID, TS: ts, TaskID: task, Bucket: b}
}

func TestProbeRowsMergeAdditivelyAndKeepNullRtt(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	id, _ := s.CreateNode(ctx, "n", hash(1))
	// 先写一个只有丢包的桶：rtt_min/max 必须是 NULL 而不是 0。
	if _, err := s.WriteMinuteBatch(ctx, metric.Batch{Probes: []metric.ProbeRow{probeRow(id, 600, 7, nil, 2, 0)}}); err != nil {
		t.Fatal(err)
	}
	var mn sql.NullInt64
	if err := s.r.QueryRow("SELECT rtt_min_us FROM probe_1m WHERE node_id = ? AND ts = 600 AND task_id = 7", id).Scan(&mn); err != nil || mn.Valid {
		t.Fatalf("rtt_min_us should be NULL without samples; got valid=%v err=%v", mn.Valid, err)
	}
	// 再并入有 rtt 的半桶：min 不能被 NULL 或 0 污染。
	if _, err := s.WriteMinuteBatch(ctx, metric.Batch{Probes: []metric.ProbeRow{probeRow(id, 600, 7, []uint32{300, 100}, 0, 1)}}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.QueryProbes(ctx, id, 600, 660, levels[0], 60)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	b := rows[0].Bucket
	if b.Sent != 5 || b.Lost != 2 || b.Errors != 1 || b.RttN != 2 || b.RttMinUs != 100 || b.RttMaxUs != 300 || b.RttSumUs != 400 {
		t.Fatalf("merged bucket %+v", *b)
	}
}

func TestProbeRollupIsExactIdempotentAndIndependentOfMetrics(t *testing.T) {
	// 写 15 分钟 × 2 个任务的探测行与 15 分钟指标行；推进时钟越过 RollupLag；Rollup 两遍。
	// 断言：probe_5m 有 3 × 2 行、每行 sent 等于 5 个分钟行之和、rtt_min 是分钟行 min 中的最小值、
	// 全丢包任务的 rtt_min 为 NULL；两遍结果逐行相同；rollup_state 的 'probe_5m' 与 '5m' 各自推进。
	// 缺陷注入：把 probeFamily.states[1] 改成 "5m" → 两族共用水位，本测试的"各自推进"断言红。
}

func TestProbeWriterRejectsRowsBeforeProbeWatermarkOnly(t *testing.T) {
	// setRollupWatermark(ctx, "probe_5m", 1200) 后：ts=600 的探测行被拒（rejected=1）、ts=600 的指标行照常写入；
	// 反向 setRollupWatermark(ctx, "5m", 1200)：指标行被拒、探测行照常。两方向都钉。
}

func TestQueryProbesRebucketsPerTask(t *testing.T) {
	// 两个任务各 10 分钟行；QueryProbes step=300 → 每任务 2 个桶，按 TaskID、TS 升序；
	// 只有一个任务有行的桶不给另一个任务凑 0 桶。
}

func TestSaveProbeTaskAssignsAndBumpsVersion(t *testing.T) {
	// 建 2 节点；Save(id 0) → id=1、version=1、LoadProbeTasks 返回它与两个 node_ids；
	// Save(id 1, 只留一个节点) → version=2、分配整份替换；Save(id 99) → ErrNotFound 且文本含 "probe task 99"；
	// Save 到不存在的节点 → ErrNotFound 且文本含 "node 42"，事务回滚（version 仍为 2，任务表无新行）；
	// Delete(1) → version=3、Load 为空；Delete(1) 再次 → ErrNotFound。
}

func TestSaveProbeTaskEnforcesPerNodeLimit(t *testing.T) {
	// 对同一节点保存 probelimit.MaxTasksPerNode 个任务成功，第 65 个返回 ErrNodeLimit（errors.Is）且文本含节点号；
	// 注入：把 n >= 改成 n > → 第 65 个成功，测试红。
}

func TestDeleteNodeRemovesProbeRowsAndAssignments(t *testing.T) {
	// 节点 A、B 各有探测行与分配；DeleteNode(A) 后 probe_* 与 probe_task_node 中 A 消失、B 完好；Counts 含 probe_task_node。
}
```

`TestDeleteNodeClearsEveryLevel` 加探测表；`TestPruneDeletesBeyondRetentionInChunks` 加探测族的一组行并断言按保留期删除且不越过 `probe_5m` 消费水位。

Run: `go test -count=1 ./internal/hub/store > /tmp/m3b-t1-store.log 2>&1; echo $?`
Expected: 0

- [ ] **Step 9: 全量门禁与提交**

Run: `make ci > /tmp/m3b-t1-ci.log 2>&1; echo $?`（`ingest`/`api` 仍调用 `WriteMinuteRows`，签名未变，应绿）
Expected: 0

```bash
git add internal/hub/metric/probe.go internal/hub/store internal/probelimit
git commit -m "hub: 探测表族与任务事务，上卷、清理与查询按表族参数化"
```

---

### Task 2: 硬限制叶子包与任务注册表（`internal/probelimit`、`internal/hub/probe`）

**Files:**
- Create: `internal/probelimit/limit.go`、`internal/probelimit/limit_test.go`、`internal/hub/probe/registry.go`、`internal/hub/probe/registry_test.go`
- Modify: 无（store 的任务事务在 Task 1 已就位）

**Interfaces:**
- Consumes: `store.LoadProbeTasks`、`store.SaveProbeTask`、`store.DeleteProbeTask`、`store.ErrNotFound`、`store.ErrNodeLimit`。
- Produces：
  - `probelimit.MinIntervalS = 5`、`MaxIntervalS = 3600`、`MinTimeoutMs = 100`、`MaxTimeoutMs = 5000`、`MaxTasksPerNode = 64`、`MaxTargetLen = 253`、`MaxResultAge = 120 * time.Second`、`probelimit.CheckTask(*probev1.ProbeTask) error`
  - `probe.Registry`：`New(st *store.Store, log *slog.Logger) *Registry`、`Load(ctx) error`、`Version() uint64`、`TasksFor(nodeID int64) *probev1.ProbeTasks`、`Assigned(nodeID int64, taskID uint64) bool`、`List() (uint64, []Detail)`、`Save(ctx, *probev1.ProbeTask, nodeIDs []int64) (Detail, uint64, error)`、`Delete(ctx, id uint64) (uint64, error)`、`Forget(nodeID int64)`
  - `probe.Detail{Task *probev1.ProbeTask; NodeIDs []int64}`（`NodeIDs` 升序去重）
  - 错误：`probe.ErrInvalid = errors.New("invalid probe task")`；字段非法时返回 `fmt.Errorf("%w: %v", ErrInvalid, err)`（保留 `CheckTask` 的字段文本，api 据 `errors.Is` 映射为 `InvalidArgument`）；`store.ErrNotFound` / `store.ErrNodeLimit` 原样透传。

- [ ] **Step 1: 常量与单任务检查（`internal/probelimit/limit.go`）**

```go
// Package probelimit 定义探测任务的硬限制，hub 保存前与 agent 应用前各自调用。
//
// 限制的目的（§8.4）：hub 被接管时，全部节点合起来也不能被指挥成高频扫描或流量放大器。
// 常量只在此处定义一次；两侧各有自己的入口与测试，缺一侧就只剩另一侧在守。
package probelimit

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
)

const (
	MinIntervalS    = 5
	MaxIntervalS    = 3600
	MinTimeoutMs    = 100
	MaxTimeoutMs    = 5000
	MaxTasksPerNode = 64
	MaxTargetLen    = 253
	// MaxResultAge 是结果的迟到预算（§6.4 第 3 条）：agent 取走时丢弃更老的，hub 拒收更老的，两侧同一个数。
	MaxResultAge = 120 * time.Second
)

// CheckTask 校验一个任务的字段。错误文本说清字段、约束与期望取值——agent 手上只有这个字符串。
func CheckTask(t *probev1.ProbeTask) error {
	if t == nil {
		return errors.New("task: required")
	}
	if t.GetIntervalS() < MinIntervalS || t.GetIntervalS() > MaxIntervalS {
		return fmt.Errorf("interval_s must be between %d and %d; got %d", MinIntervalS, MaxIntervalS, t.GetIntervalS())
	}
	if t.GetTimeoutMs() < MinTimeoutMs || t.GetTimeoutMs() > MaxTimeoutMs {
		return fmt.Errorf("timeout_ms must be between %d and %d; got %d", MinTimeoutMs, MaxTimeoutMs, t.GetTimeoutMs())
	}
	if uint64(t.GetTimeoutMs()) > uint64(t.GetIntervalS())*1000 {
		return fmt.Errorf("timeout_ms (%d) must not exceed interval_s (%d) in milliseconds", t.GetTimeoutMs(), t.GetIntervalS())
	}
	if len(t.GetTarget()) > MaxTargetLen {
		return fmt.Errorf("target must be at most %d characters; got %d", MaxTargetLen, len(t.GetTarget()))
	}
	switch t.GetKind() {
	case probev1.ProbeKind_PROBE_KIND_ICMP:
		if !validHost(t.GetTarget()) {
			return fmt.Errorf("target for an ICMP task must be an IP address or a host name; got %q", t.GetTarget())
		}
	case probev1.ProbeKind_PROBE_KIND_TCP:
		host, port, err := net.SplitHostPort(t.GetTarget())
		if err != nil || !validHost(host) {
			return fmt.Errorf("target for a TCP task must be host:port; got %q", t.GetTarget())
		}
		if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
			return fmt.Errorf("target port must be between 1 and 65535; got %q", port)
		}
	default:
		return fmt.Errorf("kind must be PROBE_KIND_ICMP or PROBE_KIND_TCP; got %s", t.GetKind())
	}
	return nil
}

// validHost 接受 IP 字面量或 DNS 名：标签由字母、数字、连字符组成，不以连字符开头或结尾。
// 不做解析：hub 不替 agent 决定名字在 agent 所在网络里解析成什么。
func validHost(h string) bool {
	if h == "" {
		return false
	}
	if _, err := netip.ParseAddr(h); err == nil {
		return true
	}
	for _, label := range strings.Split(strings.TrimSuffix(h, "."), ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
```

- [ ] **Step 2: 限制的表驱动测试（`limit_test.go`）**

用例（每条断言错误文本含指定子串）：合法 ICMP `1.1.1.1`/`example.com.`/`::1`、合法 TCP `example.com:443`/`[::1]:80`；`interval_s` 4 与 3601 → "interval_s must be between 5 and 3600"；`timeout_ms` 99 与 5001 → "timeout_ms must be between"；`timeout_ms 6000, interval_s 5` → "must not exceed interval_s"；ICMP 目标 `example.com:80`、`-bad.example`、空串 → "IP address or a host name"；TCP 目标 `example.com`（无端口）→ "host:port"；TCP 端口 `0`/`65536` → "port must be between"；kind 未指定 → "kind must be"；254 字符目标 → "at most 253 characters"。

Run: `go test -count=1 ./internal/probelimit > /tmp/m3b-t2-limit.log 2>&1; echo $?`

- [ ] **Step 3: 注册表（`internal/hub/probe/registry.go`）**

```go
// Package probe 是探测任务、分配与版本号在 hub 进程内的唯一写入口与内存缓存。
//
// 不变式：内存里的版本与每节点任务清单总是某个已提交事务后的状态，且与库中版本一致；
// 任何修改都先经 store 的事务（版本加一在事务内），成功后才改内存。writeMu 让"提交 → 改内存"
// 成为原子序列，两次保存不会以与提交相反的顺序更新内存；Forget 也取 writeMu，
// 保证删除节点后不会被一个稍早开始的保存在内存里复活该节点的分配。
package probe

type Detail struct {
	Task    *probev1.ProbeTask
	NodeIDs []int64
}

type Registry struct {
	store *store.Store
	log   *slog.Logger

	writeMu sync.Mutex // 锁序 writeMu → mu
	mu      sync.RWMutex
	version uint64
	tasks   map[uint64]*probev1.ProbeTask
	byNode  map[int64]map[uint64]struct{}
	nodesOf map[uint64][]int64
}

func New(st *store.Store, log *slog.Logger) *Registry

// Load 从库重建缓存；只在启动时调用一次。
func (r *Registry) Load(ctx context.Context) error {
	version, recs, err := r.store.LoadProbeTasks(ctx)
	if err != nil { return err }
	r.mu.Lock(); defer r.mu.Unlock()
	r.version = version
	r.tasks, r.byNode, r.nodesOf = map[uint64]*probev1.ProbeTask{}, map[int64]map[uint64]struct{}{}, map[uint64][]int64{}
	for _, rec := range recs { r.put(rec.Task, rec.NodeIDs) }
	return nil
}

func (r *Registry) put(t *probev1.ProbeTask, nodeIDs []int64) // 写 tasks / nodesOf，并把 t.Id 加进每个 byNode[node]
func (r *Registry) remove(id uint64)                          // 反向

func (r *Registry) Version() uint64

// TasksFor 返回该节点当前应持有的任务清单与版本，任务按 id 升序；没有任务时也返回空清单——
// 版本不一致就必须下发，让 agent 把已消失的任务停掉。
func (r *Registry) TasksFor(nodeID int64) *probev1.ProbeTasks

func (r *Registry) Assigned(nodeID int64, taskID uint64) bool

// List 从内存返回全部任务（含未分配的）与版本；内存与库一致由本包不变式保证。
func (r *Registry) List() (uint64, []Detail)

// Save 先校验字段，再去重排序节点列表，事务成功后更新内存。返回保存后的任务与新版本。
func (r *Registry) Save(ctx context.Context, t *probev1.ProbeTask, nodeIDs []int64) (Detail, uint64, error) {
	if err := probelimit.CheckTask(t); err != nil { return Detail{}, 0, fmt.Errorf("%w: %v", ErrInvalid, err) }
	ids := dedupSorted(nodeIDs)
	r.writeMu.Lock(); defer r.writeMu.Unlock()
	saved, version, err := r.store.SaveProbeTask(ctx, t, ids)
	if err != nil { return Detail{}, 0, err }
	r.mu.Lock(); defer r.mu.Unlock()
	r.remove(saved.Id)
	r.put(saved, ids)
	r.version = version
	return Detail{Task: saved, NodeIDs: ids}, version, nil
}

func (r *Registry) Delete(ctx context.Context, id uint64) (uint64, error) // 同样的 writeMu → 事务 → mu 序列

// Forget 清掉节点在内存里的分配；库侧分配行由 DeleteNode 的事务删除。取 writeMu 的原因见包注释。
func (r *Registry) Forget(nodeID int64)
```

`TasksFor` 返回的 `ProbeTask` 用 `proto.Clone`，调用方（响应序列化）不会与后续保存共享可变对象。

- [ ] **Step 4: 注册表测试（`registry_test.go`，真实 store）**

```go
func TestRegistrySaveDeleteVersionAndAssignments(t *testing.T) {
	// 建 2 节点；Save 任务 A 到 [n2, n1, n1] → NodeIDs == [n1, n2]、Version == 1、TasksFor(n1).Version == 1 且含 A、Assigned(n1, A)；
	// Save 任务 B 到 [] → Version 2、TasksFor(n1) 仍只含 A、List 含 A 与 B；
	// Delete(A) → Version 3、Assigned(n1, A) false、TasksFor(n1).Tasks 为空但 Version == 3。
}

func TestRegistryRejectsInvalidTaskWithoutTouchingStore(t *testing.T) {
	// interval_s 1 → errors.Is(err, ErrInvalid) 且文本含 "interval_s must be between"；Version 仍为 0；LoadProbeTasks 为空。
}

func TestRegistryPassesThroughNotFoundAndLimit(t *testing.T) {
	// 分配到不存在的节点 → errors.Is(err, store.ErrNotFound) 且文本含 "node 42"；
	// 第 65 个任务 → errors.Is(err, store.ErrNodeLimit)。
}

func TestRegistryReloadMatchesMemory(t *testing.T) {
	// 若干 Save/Delete 后，新建 Registry 并 Load，List 与旧实例逐项相等（proto.Equal）、Version 相等。
}

func TestRegistryForgetCannotBeRevivedByEarlierSave(t *testing.T) {
	// 用 store 的 HoldWriterForTest 让 Save 的事务停在写协程里；并发调用 Forget(node)（它会等 writeMu）；
	// 放开写协程 → Save 完成后 Forget 才执行 → Assigned(node, task) 最终为 false。
	// 注入：去掉 Forget 里的 writeMu → Forget 先于 Save 的内存更新完成 → Assigned 为 true，测试红。
}
```

Run: `go test -count=1 -race ./internal/hub/probe ./internal/probelimit > /tmp/m3b-t2-probe.log 2>&1; echo $?`
Expected: 0

- [ ] **Step 5: 门禁与提交**

Run: `make ci > /tmp/m3b-t2-ci.log 2>&1; echo $?`

```bash
git add internal/probelimit internal/hub/probe
git commit -m "hub: 探测任务注册表与两侧共用的硬限制"
```

---

### Task 3: 上报路径——结果校验与折叠、版本对账、两族同批刷出、装配

**Files:**
- Modify: `internal/hub/live/live.go`、`internal/hub/ingest/service.go`、`internal/hub/ingest/validate.go`、`internal/hub/ingest/flush.go`、`cmd/hub/serve.go`、`internal/hub/api/api_test.go`（`newHarness` 装配 registry）
- Test: `internal/hub/live/live_test.go`、`internal/hub/ingest/ingest_test.go`

**Interfaces:**
- Consumes: `probe.Registry`（作为 `ingest.TaskSource`）、`metric.Batch`/`ProbeRow`、`store.WriteMinuteBatch`、`probelimit.MaxResultAge`（`flush.go` 里的 `MaxProbeAge` 改为 `const MaxProbeAge = probelimit.MaxResultAge`，编译期断言不变）。
- Produces：
  - `live.(*Live).AddProbe(nodeID int64, at time.Time, taskID uint64, r *probev1.ProbeResult)`；`live.(*Live).Flush() metric.Batch`、`Drain() metric.Batch`（原来返回 `[]metric.Row`）
  - `ingest.TaskSource interface { Version() uint64; TasksFor(nodeID int64) *probev1.ProbeTasks; Assigned(nodeID int64, taskID uint64) bool; Forget(nodeID int64) }`
  - `ingest.New(cfg, l, st, a, book, tasks TaskSource, clk, log)`（多一个参数）
  - `storeWriter.WriteMinuteBatch(ctx, metric.Batch) (int, error)`（替换 `WriteMinuteRows`）

- [ ] **Step 1: live 的探测桶**

`entry` 增加 `probes map[probeKey]*metric.ProbeBucket`，`type probeKey struct { ts int64; task uint64 }`。

```go
// AddProbe 把一条结果折叠进测量时刻所在分钟的 (任务) 桶。at 由调用方按 收到时刻 − age_ms 算出，
// 所以同一次上报里的结果可以落进不同分钟；迟到结果所属的分钟若已刷出，会在这里开一个同键的
// 新桶，刷出后由写库的加法合并并入已有的行。节点已被 Forget 时丢弃——Forget 之后不得再建内存状态。
func (l *Live) AddProbe(nodeID int64, at time.Time, taskID uint64, r *probev1.ProbeResult) {
	ts := minuteOf(at)
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.nodes[nodeID]
	if e == nil {
		return
	}
	k := probeKey{ts: ts, task: taskID}
	b := e.probes[k]
	if b == nil {
		b = &metric.ProbeBucket{}
		e.probes[k] = b
	}
	b.Add(r)
}
```

`take(before)` 同时取走两族：`ts < before` 的指标桶进 `Batch.Rows`，`k.ts < before` 的探测桶进 `Batch.Probes`（`metric.ProbeRow{NodeID: id, TS: k.ts, TaskID: k.task, Bucket: b}`），取走即删除。`Flush`/`Drain` 返回 `metric.Batch`。`Observe` 里新建 entry 时初始化 `probes`。

live 测试：`TestAddProbeFoldsIntoMeasuredMinuteAndFlushesWithMetrics`（同一节点：Observe 一次；AddProbe 两条到当前分钟、一条到上一分钟；`Flush()` 只取上一分钟的探测桶与已闭合指标桶，`Drain()` 取剩余；Forget 后 AddProbe 不建状态）。

- [ ] **Step 2: 结果校验（`validate.go`）**

```go
// validateResults 只判结构：outcome 必须给定，rtt 不得超过最大超时——探测超时上限是 5 s，
// 更大的 rtt 不可能来自合法的 agent。归属与超龄不是结构问题，由 Report 逐条丢弃而不是整条拒绝。
func validateResults(rs []*probev1.ProbeResult) error {
	for i, r := range rs {
		switch o := r.GetOutcome().(type) {
		case nil:
			return fmt.Errorf("probe_results[%d].outcome: required (rtt_us, timeout or error)", i)
		case *probev1.ProbeResult_RttUs:
			if o.RttUs > probelimit.MaxTimeoutMs*1000 {
				return fmt.Errorf("probe_results[%d].rtt_us: must not exceed %d (the maximum probe timeout in microseconds); got %d", i, probelimit.MaxTimeoutMs*1000, o.RttUs)
			}
		}
	}
	return nil
}
```

- [ ] **Step 3: Report 的折叠与对账（`service.go`）**

`Service` 增加 `tasks TaskSource`；`New` 多一个参数。`Report`：

```go
	m := req.Msg.GetMetrics()
	if err := validateMetrics(m); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := validateResults(req.Msg.GetProbeResults()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	ts, gap, first := s.live.Observe(id, m)
	// …流量入账不变…
	s.foldResults(id, req.Msg.GetProbeResults())
	want := s.reconcileFacts(...)
	resp := &probev1.ReportResponse{ReportIntervalMs: ..., WantFacts: want}
	// 电平触发：agent 报它持有的版本，hub 只在不一致时下发整份清单（可能为空——消失的任务要靠它停掉）。
	if req.Msg.GetTasksVersion() != s.tasks.Version() {
		resp.Tasks = s.tasks.TasksFor(id)
	}
	return connect.NewResponse(resp), nil
```

```go
// foldResults 按 §8.3 逐条准入：归属不对的结果丢弃（token 被挪用或分配已撤销的旧结果不得写进
// 别的任务的历史）；超龄的丢弃（它可能落在已冻结的分钟里）。测量时刻 = 收到时刻 − age_ms。
func (s *Service) foldResults(id int64, rs []*probev1.ProbeResult) {
	if len(rs) == 0 {
		return
	}
	now := s.clk.Now()
	var foreign, late int
	for _, r := range rs {
		if !s.tasks.Assigned(id, r.GetTaskId()) {
			foreign++
			continue
		}
		age := time.Duration(r.GetAgeMs()) * time.Millisecond
		if age > MaxProbeAge {
			late++
			continue
		}
		s.live.AddProbe(id, now.Add(-age), r.GetTaskId(), r)
	}
	if foreign > 0 || late > 0 {
		s.log.Warn("probe results dropped", "node", id, "unassigned", foreign, "too_old", late)
	}
}
```

`Forget` 增加 `s.tasks.Forget(nodeID)`，并把 `pending` 的按节点过滤扩展到 `Batch.Probes`。

- [ ] **Step 4: 刷出改为两族同批（`flush.go`）**

`pending [][]metric.Row` → `pending []metric.Batch`；`Flush` 里 `batch := s.live.Flush()`（或 `Drain()`），`if !batch.Empty() { s.pending = append(s.pending, batch) }`；写入调 `s.writer.WriteMinuteBatch(ctx, batch)`；丢最旧一批的日志同时打印两族行数。`storeWriter` 接口方法改名为 `WriteMinuteBatch`，`ingest_test.go` 的 `failingWriter` 随之改。

- [ ] **Step 5: 装配（`serve.go`、`api_test.go` 的 `newHarness`、`ingest_test.go` 的 `newHubAt`）**

`serve.go`：`reg := probe.New(st, log)`；`ingest.New(..., book, reg, clk, log)`；`errors.Join(a.Load(ctx), svc.Load(ctx), book.Load(ctx), reg.Load(ctx))`；`api.New(..., reg, ...)`（api 的参数在 Task 4 加，本任务先不传——Task 3 只让 ingest 拿到它；`newHarness` 同理先只给 ingest）。

- [ ] **Step 6: ingest 测试**

```go
func (h *hub) task(t *testing.T, nodeID int64) uint64 {
	t.Helper()
	d, _, err := h.reg.Save(context.Background(), &probev1.ProbeTask{Kind: probev1.ProbeKind_PROBE_KIND_ICMP, Target: "127.0.0.1", IntervalS: 5, TimeoutMs: 1000}, []int64{nodeID})
	if err != nil { t.Fatal(err) }
	return d.Task.Id
}

func rtt(task uint64, ageMs, us uint32) *probev1.ProbeResult {
	return &probev1.ProbeResult{TaskId: task, AgeMs: ageMs, Outcome: &probev1.ProbeResult_RttUs{RttUs: us}}
}

func TestReportFoldsResultsIntoMeasuredMinuteBuckets(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	task := h.task(t, id)
	// 墙钟 00:00:30；age 0 落进 00:00 的桶，age 45 000 ms 落进 23:59 的桶。
	h.clk.SetWall(time.Date(2026, 1, 1, 0, 0, 30, 0, time.UTC))
	req := report(tok, &probev1.Metrics{})
	req.Msg.TasksVersion = h.reg.Version()
	req.Msg.ProbeResults = []*probev1.ProbeResult{rtt(task, 0, 1200), rtt(task, 45_000, 800),
		{TaskId: task, Outcome: &probev1.ProbeResult_Timeout{Timeout: &probev1.Timeout{}}}}
	if _, err := h.client.Report(t.Context(), req); err != nil { t.Fatal(err) }
	batch := h.live.Drain()
	if len(batch.Probes) != 2 { t.Fatalf("probe rows %+v", batch.Probes) }
	byTS := map[int64]*metric.ProbeBucket{}
	for _, r := range batch.Probes { byTS[r.TS] = r.Bucket }
	cur, prev := byTS[time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix()], byTS[time.Date(2025, 12, 31, 23, 59, 0, 0, time.UTC).Unix()]
	if cur == nil || cur.Sent != 2 || cur.Lost != 1 || cur.RttN != 1 || cur.RttMinUs != 1200 { t.Fatalf("current minute %+v", cur) }
	if prev == nil || prev.Sent != 1 || prev.RttMinUs != 800 { t.Fatalf("previous minute %+v", prev) }
}

func TestReportDropsUnassignedAndStaleResultsButKeepsTheRest(t *testing.T) {
	// 任务 A 分配给节点 1；节点 2 上报 A 的结果 → 丢弃（Drain 无探测行），Report 仍 200；
	// 节点 1 上报 age_ms = 120 001 的结果 → 丢弃；age_ms = 120 000 的 → 折叠。
	// 注入：去掉 Assigned 判定 → 节点 2 的结果被折叠，红。
}

func TestMalformedResultRejectsWholeReport(t *testing.T) {
	// outcome 为空 → InvalidArgument 且文本含 "probe_results[0].outcome"；live.Get 无该节点、流量 View 为零、Drain 为空。
	// rtt_us = 5 000 001 → InvalidArgument 含 "rtt_us"。
}

func TestReportReconcilesTaskVersion(t *testing.T) {
	// 无任务时 agent 报 0 → 响应 Tasks 为 nil；Save 一个任务后 agent 仍报 0 → Tasks.Version == 1 且含该任务；
	// agent 报 1 → nil；Delete 后 agent 报 1 → Tasks.Version == 2 且 Tasks 为空列表（非 nil）。
}

func TestFlushWritesBothFamiliesInOneBatchAndRetriesTogether(t *testing.T) {
	// 一次上报同时产生指标桶与探测桶；用 failingWriter 让第一次 Flush 失败 → pending 一批（两族都在）；
	// 放开后 Flush → probe_1m 与 metric_1m 各有行。
}

func TestForgetDropsPendingProbeRowsAndAssignments(t *testing.T) {
	// 节点有待重试批次（含探测行）与任务分配；Forget 后 pending 中该节点两族行都消失，reg.Assigned 为 false。
}
```

`hub` 结构增加 `reg *probe.Registry`。

Run: `go test -count=1 -race ./internal/hub/live ./internal/hub/ingest > /tmp/m3b-t3-ingest.log 2>&1; echo $?`
Expected: 0

- [ ] **Step 7: 门禁与提交**

Run: `make ci > /tmp/m3b-t3-ci.log 2>&1; echo $?`

```bash
git add internal/hub/live internal/hub/ingest cmd/hub/serve.go internal/hub/api/api_test.go
git commit -m "hub: 上报路径折叠探测结果并按版本下发任务，两族分钟桶同批落盘"
```

---

### Task 4: 协议与 AdminService——任务增删改查、`QueryProbes`；hub 侧 e2e 断言

**Files:**
- Modify: `proto/probe/v1/admin.proto`（+ `make gen`）、`internal/hub/api/service.go`（`Service.probes`、`New`）、`internal/hub/api/data.go`（抽出 `queryWindow` 校验供两个查询共用）、`cmd/hub/serve.go`、`scripts/e2e.sh`
- Create: `internal/hub/api/probes.go`、`internal/hub/api/probes_test.go`

**Interfaces:**
- Consumes: `probe.Registry.List/Save/Delete`、`store.QueryProbes`、`store.ChooseLevel`、`probelimit` 的错误文本、`store.ErrNotFound`/`ErrNodeLimit`。
- Produces（proto，逐字）：

```proto
  // 探测任务：列出全部任务及其分配；保存（id 为 0 即创建）提交整份分配列表；删除不删历史。
  rpc ListProbeTasks(ListProbeTasksRequest) returns (ListProbeTasksResponse);
  rpc SaveProbeTask(SaveProbeTaskRequest) returns (SaveProbeTaskResponse);
  rpc DeleteProbeTask(DeleteProbeTaskRequest) returns (DeleteProbeTaskResponse);
  // 某节点在窗口内全部任务的探测历史，选级与对齐规则同 QueryMetrics。
  rpc QueryProbes(QueryProbesRequest) returns (QueryProbesResponse);

message ProbeTaskDetail {
  ProbeTask task = 1;
  // 分配到的节点，升序去重。
  repeated int64 node_ids = 2;
}
message ListProbeTasksRequest {}
message ListProbeTasksResponse {
  // 任务与分配的全局版本；任何修改都加一，agent 用它对账。
  uint64 version = 1;
  repeated ProbeTaskDetail tasks = 2;
}
message SaveProbeTaskRequest {
  // task.id 为 0 时创建，否则整体替换该任务的字段；node_ids 是保存后的完整分配列表。
  // 约束：interval_s 5–3600，timeout_ms 100–5000 且不超过 interval_s 的毫秒数，
  // ICMP 目标为 IP 或主机名，TCP 目标为 host:port；每节点至多 64 个任务。
  ProbeTask task = 1;
  repeated int64 node_ids = 2;
}
message SaveProbeTaskResponse {
  ProbeTaskDetail task = 1;
  uint64 version = 2;
}
message DeleteProbeTaskRequest { uint64 id = 1; }
message DeleteProbeTaskResponse { uint64 version = 1; }

message QueryProbesRequest {
  int64 node_id = 1;
  // 窗口 [from, to)，Unix 秒。跨度最长 400 天。
  int64 from = 2;
  int64 to = 3;
  // 返回点数上限；0 取默认 720，最大 2000。hub 据此选择步长。
  uint32 max_points = 4;
}
message QueryProbesResponse {
  string level = 1;
  uint32 step_s = 2;
  // 每个任务一条；只含在窗口内有结果的任务，已删除任务的历史同样按 task_id 返回。
  repeated ProbeSeries series = 3;
}
message ProbeSeries {
  uint64 task_id = 1;
  // 按 ts 升序；只包含 sent > 0 的点，缺失的 ts 表示该段没有结果。
  repeated ProbeSample samples = 2;
}
message ProbeSample {
  // 点起始，Unix 秒，已对齐到 step_s 的整数倍。
  int64 ts = 1;
  uint32 sent = 2;
  // 计入丢包的超时数；丢包率 = lost / sent，errors 不计入。
  uint32 lost = 3;
  uint32 errors = 4;
  // 只有 sent − lost − errors > 0 时才有：该点内成功探测的 rtt 均值 / 最小 / 最大，微秒。
  optional uint32 rtt_mean_us = 5;
  optional uint32 rtt_min_us = 6;
  optional uint32 rtt_max_us = 7;
}
```

- [ ] **Step 1: proto 与生成**

按上面写进 `admin.proto`（rpc 放在 `AdjustTraffic` 之后，消息放在文件末尾），`make gen > /tmp/m3b-t4-gen.log 2>&1; echo $?`，`git status --porcelain -- gen web/src/gen` 应列出新生成物（提交时一并加入）。`buf breaking` 对主干只有新增，应通过。

- [ ] **Step 2: api 方法（`probes.go`）**

```go
// SaveProbeTask 的校验全部在注册表里（字段规则与 agent 共用一份）；这里只把错误翻译成响应码：
// 字段非法 → InvalidArgument，节点或任务不存在 → NotFound，每节点上限 → ResourceExhausted。
func (s *Service) SaveProbeTask(ctx context.Context, req *connect.Request[probev1.SaveProbeTaskRequest]) (*connect.Response[probev1.SaveProbeTaskResponse], error) {
	d, version, err := s.probes.Save(ctx, req.Msg.GetTask(), req.Msg.GetNodeIds())
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, store.ErrNodeLimit):
		return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("%w (maximum %d)", err, probelimit.MaxTasksPerNode))
	case errors.Is(err, probe.ErrInvalid):
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	case err != nil:
		s.log.Error("saving probe task failed", "err", err)
		return nil, internalError("saving probe task failed")
	}
	return connect.NewResponse(&probev1.SaveProbeTaskResponse{Task: detailProto(d), Version: version}), nil
}
```

上面的 `case err != nil && !isStoreFailure(err)` 写成 `case errors.Is(err, probe.ErrInvalid)`：注册表已把字段错误包成 `probe.ErrInvalid`（Task 2），api 只按哨兵映射，不猜错误类型；其余错误按现有惯例记日志并返回 `Internal`。

`ListProbeTasks`：`version, details := s.probes.List()` → 响应。`DeleteProbeTask`：NotFound 映射同上。`QueryProbes`：

```go
func (s *Service) QueryProbes(ctx context.Context, req *connect.Request[probev1.QueryProbesRequest]) (*connect.Response[probev1.QueryProbesResponse], error) {
	m := req.Msg
	maxPoints, err := s.queryWindow(ctx, m.GetNodeId(), m.GetFrom(), m.GetTo(), m.GetMaxPoints())
	if err != nil {
		return nil, err
	}
	lv, step := store.ChooseLevel(m.GetFrom(), m.GetTo(), maxPoints)
	rows, err := s.store.QueryProbes(ctx, m.GetNodeId(), m.GetFrom(), m.GetTo(), lv, step)
	if err != nil {
		s.log.Error("probe query failed", "err", err)
		return nil, internalError("probe query failed")
	}
	resp := &probev1.QueryProbesResponse{Level: lv.Name, StepS: uint32(step)}
	var cur *probev1.ProbeSeries
	for _, r := range rows { // store 已按 TaskID、TS 排序
		if r.Bucket.Sent == 0 {
			continue
		}
		if cur == nil || cur.TaskId != r.TaskID {
			cur = &probev1.ProbeSeries{TaskId: r.TaskID}
			resp.Series = append(resp.Series, cur)
		}
		sample := &probev1.ProbeSample{Ts: r.TS, Sent: r.Bucket.Sent, Lost: r.Bucket.Lost, Errors: r.Bucket.Errors}
		if mean, ok := r.Bucket.RttMean(); ok {
			sample.RttMeanUs, sample.RttMinUs, sample.RttMaxUs = proto.Uint32(mean), proto.Uint32(r.Bucket.RttMinUs), proto.Uint32(r.Bucket.RttMaxUs)
		}
		cur.Samples = append(cur.Samples, sample)
	}
	return connect.NewResponse(resp), nil
}
```

`queryWindow` 从现有 `QueryMetrics` 抽出（from ≥ 0、from < to、跨度 ≤ 400 天、max_points 默认与上限、节点存在），`QueryMetrics` 改为调用它。`Service` 加 `probes *probe.Registry`，`New` 多一个参数；`serve.go`、`newHarness` 传入。

- [ ] **Step 3: api 测试（`probes_test.go`）**

```go
func TestProbeTaskLifecycleThroughAdminAPI(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	n1, _ := h.createNode(t, "a")
	n2, _ := h.createNode(t, "b")
	ctx := t.Context()
	save := func(task *probev1.ProbeTask, nodes ...int64) (*probev1.SaveProbeTaskResponse, error) {
		resp, err := h.admin.SaveProbeTask(ctx, connect.NewRequest(&probev1.SaveProbeTaskRequest{Task: task, NodeIds: nodes}))
		if err != nil { return nil, err }
		return resp.Msg, nil
	}
	created, err := save(&probev1.ProbeTask{Kind: probev1.ProbeKind_PROBE_KIND_TCP, Target: "example.com:443", IntervalS: 30, TimeoutMs: 2000}, n2, n1, n1)
	if err != nil || created.Version != 1 || created.Task.Task.Id != 1 || !slices.Equal(created.Task.NodeIds, []int64{n1, n2}) { t.Fatalf("%+v %v", created, err) }
	list, _ := h.admin.ListProbeTasks(ctx, connect.NewRequest(&probev1.ListProbeTasksRequest{}))
	if list.Msg.Version != 1 || len(list.Msg.Tasks) != 1 { t.Fatalf("%+v", list.Msg) }
	// 整体替换：改目标、只留 n1。
	updated, err := save(&probev1.ProbeTask{Id: 1, Kind: probev1.ProbeKind_PROBE_KIND_ICMP, Target: "1.1.1.1", IntervalS: 10, TimeoutMs: 1000}, n1)
	if err != nil || updated.Version != 2 || updated.Task.Task.Target != "1.1.1.1" || !slices.Equal(updated.Task.NodeIds, []int64{n1}) { t.Fatalf("%+v %v", updated, err) }
	del, err := h.admin.DeleteProbeTask(ctx, connect.NewRequest(&probev1.DeleteProbeTaskRequest{Id: 1}))
	if err != nil || del.Msg.Version != 3 { t.Fatalf("%+v %v", del, err) }
	_, err = h.admin.DeleteProbeTask(ctx, connect.NewRequest(&probev1.DeleteProbeTaskRequest{Id: 1}))
	if codeOf(err) != connect.CodeNotFound || !strings.Contains(err.Error(), "probe task 1") { t.Fatalf("%v", err) }
}

func TestSaveProbeTaskErrorsNameTheFieldAndCode(t *testing.T) {
	// interval_s 1 → InvalidArgument 含 "interval_s must be between 5 and 3600"；
	// TCP 目标无端口 → InvalidArgument 含 "host:port"；节点 42 → NotFound 含 "node 42"；
	// 第 65 个任务 → ResourceExhausted 含 "maximum 64"。
}

func TestQueryProbesGroupsPerTaskAndOmitsEmptyPoints(t *testing.T) {
	// 直接经 store.WriteMinuteBatch 写：任务 7 三分钟（其中一分钟全丢包）、任务 9 一分钟；
	// QueryProbes(from=base, to=base+3600, maxPoints=100) → level "1m"、两条 series 按 task_id 升序；
	// 全丢包点 rttMeanUs == nil 且 lost == sent；有 rtt 的点三项都在；任务 9 只有 1 个样本（不凑 0 点）。
	// 校验错误：from ≥ to → InvalidArgument；不存在的节点 → NotFound（复用 queryWindow，与 QueryMetrics 同文案）。
}
```

`TestEveryAdminProcedureRejectsAnonymousCalls` 自动覆盖四个新方法；如它对请求体有按方法的构造表，补四项。

Run: `go test -count=1 ./internal/hub/api > /tmp/m3b-t4-api.log 2>&1; echo $?`

- [ ] **Step 4: e2e（hub 侧，agent 还没有 prober，本任务只验证接口与持久化）**

在 `ListNodes` 断言之后加：

```sh
node2=$(jq -r '.nodes[1].id' "$work/ListNodes.json")
icmp_body=$(jq -nc --arg a "$node1" --arg b "$node2" '{task: {kind: "PROBE_KIND_ICMP", target: "127.0.0.1", intervalS: 5, timeoutMs: 1000}, nodeIds: [$a, $b]}')
[ "$(rpc SaveProbeTask "$icmp_body")" = 200 ] || { echo "FAIL: SaveProbeTask icmp"; cat "$work/SaveProbeTask.json"; exit 1; }
icmp_task=$(jq -r '.task.task.id' "$work/SaveProbeTask.json")
tcp_body=$(jq -nc --arg a "$node1" --arg b "$node2" --arg target "host.docker.internal:$port" '{task: {kind: "PROBE_KIND_TCP", target: $target, intervalS: 5, timeoutMs: 2000}, nodeIds: [$a, $b]}')
[ "$(rpc SaveProbeTask "$tcp_body")" = 200 ] || { echo "FAIL: SaveProbeTask tcp"; cat "$work/SaveProbeTask.json"; exit 1; }
tcp_task=$(jq -r '.task.task.id' "$work/SaveProbeTask.json")
[ "$(rpc SaveProbeTask '{"task": {"kind": "PROBE_KIND_ICMP", "target": "127.0.0.1", "intervalS": 1, "timeoutMs": 1000}}')" = 400 ] || { echo "FAIL: interval below the minimum must be rejected"; exit 1; }
grep -q 'interval_s must be between 5 and 3600' "$work/SaveProbeTask.json" || { echo "FAIL: error must name the field"; cat "$work/SaveProbeTask.json"; exit 1; }
[ "$(rpc ListProbeTasks '{}')" = 200 ] || { echo "FAIL: ListProbeTasks"; exit 1; }
jq -e '.version == "2" and (.tasks | length) == 2 and all(.tasks[]; (.nodeIds | length) == 2)' "$work/ListProbeTasks.json" > /dev/null || { echo "FAIL: task list shape"; cat "$work/ListProbeTasks.json"; exit 1; }
```

重启后（登录之后）加：`[ "$(rpc ListProbeTasks '{}')" = 200 ]` 且 `jq -e '.version == "2" and (.tasks | length) == 2'`；`stats` 段加 `[ "$(get probe_task)" = 2 ]`、`[ "$(get probe_task_node)" = 4 ]`。探测结果的断言留到 Task 6。

Run: `make e2e > /tmp/m3b-t4-e2e.log 2>&1; echo $?`

- [ ] **Step 5: 门禁与提交**

Run: `make ci > /tmp/m3b-t4-ci.log 2>&1; echo $?`

```bash
git add proto gen web/src/gen internal/hub/api cmd/hub/serve.go scripts/e2e.sh internal/hub/probe
git commit -m "hub: 管理接口增删改查探测任务并按窗口查询探测历史"
```

---

### Task 5: agent prober——硬限制、有界队列、调度、ICMP 与 TCP 引擎

**Files:**
- Create: `internal/agent/prober/engine.go`（`Outcome`、`Engine`、`Multi`）、`queue.go`、`scheduler.go`、`resolve.go`、`icmp.go`、`tcp.go`，以及 `queue_test.go`、`scheduler_test.go`、`icmp_test.go`、`tcp_test.go`
- Modify: `go.mod`/`go.sum`（`go get golang.org/x/net@v0.57.0`，`go mod tidy`）

**Interfaces:**
- Consumes: `probelimit.CheckTask`、`probelimit.MaxTasksPerNode`、`probelimit.MaxResultAge`、`clock.Clock`。
- Produces：
  - `prober.Outcome{RttUs uint32; Timeout bool; Err string}`（三态互斥：`Err != ""` 为 error，否则 `Timeout` 为丢包，否则 rtt）
  - `prober.Engine interface { Probe(ctx context.Context, t *probev1.ProbeTask) Outcome }`；`prober.Multi{ICMP, TCP Engine}` 按 `kind` 分派
  - `prober.Result{TaskID uint64; Outcome Outcome; At time.Duration}`（`At` 为单调钟）
  - `prober.NewQueue(capacity int) *Queue`、`(*Queue).Push(Result)`、`(*Queue).Take(now, maxAge time.Duration) []Result`、`(*Queue).Requeue([]Result)`、`(*Queue).Dropped() uint64`、`prober.ToProto([]Result, now time.Duration) []*probev1.ProbeResult`、`prober.QueueCap = 4096`
  - `prober.NewScheduler(engine Engine, queue *Queue, clk clock.Clock, log *slog.Logger) *Scheduler`、`(*Scheduler).Version() uint64`、`(*Scheduler).Apply(*probev1.ProbeTasks)`、`(*Scheduler).Stop()`；测试注入 `Scheduler.Sleep func(context.Context, time.Duration) error`、`Scheduler.Rand func() float64`
  - `prober.NewICMP(clk clock.Clock, log *slog.Logger) *ICMP`、`(*ICMP).Available() bool`、`(*ICMP).InitErrors() []string`、`(*ICMP).Close()`；`prober.TCP{Clock clock.Clock}`

- [ ] **Step 1: 引擎接口与队列（`engine.go`、`queue.go`）**

```go
// Package prober 在 agent 侧执行 hub 下发的延迟探测任务并暂存结果。
//
// 结果的时间基准是单调钟：入队时记 At，上报时折算成 age_ms（§4.5），hub 用它反推测量时刻。
package prober

type Outcome struct {
	RttUs   uint32
	Timeout bool
	Err     string
}

type Engine interface {
	Probe(ctx context.Context, t *probev1.ProbeTask) Outcome
}

// Multi 按任务种类分派；未知种类是 hub 与 agent 版本偏斜的信号，按 error 回报而不是静默跳过。
type Multi struct{ ICMP, TCP Engine }

func (m Multi) Probe(ctx context.Context, t *probev1.ProbeTask) Outcome {
	switch t.GetKind() {
	case probev1.ProbeKind_PROBE_KIND_ICMP:
		return m.ICMP.Probe(ctx, t)
	case probev1.ProbeKind_PROBE_KIND_TCP:
		return m.TCP.Probe(ctx, t)
	}
	return Outcome{Err: fmt.Sprintf("unsupported probe kind %s", t.GetKind())}
}

// QueueCap 覆盖最坏情况：64 个任务 × 每 5 秒一次 × 120 秒迟到预算 = 1536 条，留出余量。
const QueueCap = 4096

type Result struct {
	TaskID  uint64
	Outcome Outcome
	At      time.Duration
}

// Queue 是有界的结果暂存：满时丢最旧并计数，探测协程永不阻塞（§8.2）。
type Queue struct {
	mu      sync.Mutex
	items   []Result
	cap     int
	dropped uint64
}

func NewQueue(capacity int) *Queue { return &Queue{cap: capacity} }

func (q *Queue) Push(r Result)

// Take 取走全部结果并丢弃超过 maxAge 的：hub 会拒收它们（§6.4 第 3 条），留着只会占上报体积。
func (q *Queue) Take(now, maxAge time.Duration) []Result

// Requeue 把上报失败的结果放回队头，保持时间顺序；仍受容量约束。
func (q *Queue) Requeue(rs []Result)

func (q *Queue) Dropped() uint64

func ToProto(rs []Result, now time.Duration) []*probev1.ProbeResult {
	out := make([]*probev1.ProbeResult, 0, len(rs))
	for _, r := range rs {
		p := &probev1.ProbeResult{TaskId: r.TaskID, AgeMs: uint32(min((now-r.At)/time.Millisecond, math.MaxUint32))}
		switch {
		case r.Outcome.Err != "":
			p.Outcome = &probev1.ProbeResult_Error{Error: &probev1.ProbeError{Message: r.Outcome.Err}}
		case r.Outcome.Timeout:
			p.Outcome = &probev1.ProbeResult_Timeout{Timeout: &probev1.Timeout{}}
		default:
			p.Outcome = &probev1.ProbeResult_RttUs{RttUs: r.Outcome.RttUs}
		}
		out = append(out, p)
	}
	return out
}
```

队列测试：容量 3 推 4 条 → 保留后 3 条、`Dropped() == 1`；`Take(now, 120s)` 丢弃 `At` 早于 120s 的并计数；`Requeue` 后再 `Take` 顺序为 旧→新；`ToProto` 的 `age_ms` 与三态映射。

- [ ] **Step 2: 调度器（`scheduler.go`）**

```go
type Scheduler struct {
	engine Engine
	queue  *Queue
	clk    clock.Clock
	log    *slog.Logger
	Sleep  func(context.Context, time.Duration) error
	Rand   func() float64

	mu      sync.Mutex
	version uint64
	running map[uint64]*runningTask
	wg      sync.WaitGroup
}

type runningTask struct {
	task   *probev1.ProbeTask
	cancel context.CancelFunc
}

// Apply 用整份清单替换当前任务集：消失的停掉、新增的启动、字段相同的不动（不重启计时），
// 版本采用清单的版本。超过每节点上限的任务（按 id 排序后靠后的）与字段非法的任务不启动，
// 各入队一条 error 说明原因——面板显示原因，而不是静默呈现为无数据（§8.2、§8.4）。
func (s *Scheduler) Apply(tasks *probev1.ProbeTasks) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version = tasks.GetVersion()
	want := map[uint64]*probev1.ProbeTask{}
	sorted := slices.SortedFunc(slices.Values(tasks.GetTasks()), func(a, b *probev1.ProbeTask) int { return cmp.Compare(a.GetId(), b.GetId()) })
	for i, t := range sorted {
		if i >= probelimit.MaxTasksPerNode {
			s.reject(t, fmt.Sprintf("more than %d tasks assigned; task dropped", probelimit.MaxTasksPerNode))
			continue
		}
		if err := probelimit.CheckTask(t); err != nil {
			s.reject(t, err.Error())
			continue
		}
		want[t.GetId()] = t
	}
	for id, r := range s.running {
		if t, ok := want[id]; !ok || !proto.Equal(t, r.task) {
			r.cancel()
			delete(s.running, id)
		}
	}
	for id, t := range want {
		if _, ok := s.running[id]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		s.running[id] = &runningTask{task: t, cancel: cancel}
		s.wg.Add(1)
		go s.run(ctx, t)
	}
}

func (s *Scheduler) reject(t *probev1.ProbeTask, why string) {
	s.queue.Push(Result{TaskID: t.GetId(), Outcome: Outcome{Err: why}, At: s.clk.Mono()})
	s.log.Warn("probe task rejected", "task", t.GetId(), "reason", why)
}

// run 是一个任务的定时循环：首次触发加 [0, interval) 的随机偏移（同一时刻不齐发），此后每 interval
// 一次；周期从本次触发算起，探测耗时不拉长周期（探测超时 ≤ interval 由硬限制保证，不会重叠）。
// 停止时半途的结果不入队。
func (s *Scheduler) run(ctx context.Context, t *probev1.ProbeTask) {
	defer s.wg.Done()
	interval := time.Duration(t.GetIntervalS()) * time.Second
	if err := s.Sleep(ctx, time.Duration(s.Rand()*float64(interval))); err != nil {
		return
	}
	for {
		next := s.clk.Mono() + interval
		out := s.engine.Probe(ctx, t)
		if ctx.Err() != nil {
			return
		}
		s.queue.Push(Result{TaskID: t.GetId(), Outcome: out, At: s.clk.Mono()})
		if err := s.Sleep(ctx, max(next-s.clk.Mono(), 0)); err != nil {
			return
		}
	}
}

// Stop 停掉全部任务并等待协程退出；之后 Apply 不再被调用。
func (s *Scheduler) Stop()
```

调度器测试用假引擎（记录被调用的任务与次数、可返回预设 Outcome）、假时钟与可控 `Sleep`（每次调用把时长记下并立即返回或阻塞到测试放行）：`TestApplyStartsNewStopsGoneKeepsUnchanged`（第二次 Apply 只换掉字段变化的任务，未变任务的探测计数连续、未重新经历随机偏移）；`TestApplyRejectsInvalidAndOverLimitWithErrorResults`（第 65 个任务与 interval 1 的任务各得一条 error 结果，且从未被引擎调用）；`TestRunKeepsPeriodDespiteProbeDuration`（引擎每次推进假时钟 2s，interval 5s → 相邻 Sleep 为 3s）；`TestStopDoesNotEnqueueInFlightResult`。

- [ ] **Step 3: 解析与 TCP（`resolve.go`、`tcp.go`）**

```go
// resolve 把目标变成一个地址：IP 字面量直接用；主机名在能用的地址族里取第一个，v4 优先。
// 解析预算就是探测超时——名字解析不出来时这一次按 error 记，不把等待算进 rtt。
func resolve(ctx context.Context, host string, v4, v6 bool) (netip.Addr, error)

// TCP 以连接建立耗时为 rtt（§8.2）。连不上——拒绝、超时、不可达——都计入丢包：对可达性监控
// 它们是同一个事实，这一次没有联通；只有名字解析失败与地址非法是 error。
type TCP struct{ Clock clock.Clock }

func (p TCP) Probe(ctx context.Context, t *probev1.ProbeTask) Outcome {
	timeout := time.Duration(t.GetTimeoutMs()) * time.Millisecond
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := p.Clock.Mono()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", t.GetTarget())
	if err != nil {
		var dnsErr *net.DNSError
		var addrErr *net.AddrError
		if errors.As(err, &dnsErr) || errors.As(err, &addrErr) {
			return Outcome{Err: err.Error()}
		}
		return Outcome{Timeout: true}
	}
	conn.Close()
	return Outcome{RttUs: uint32((p.Clock.Mono() - start) / time.Microsecond)}
}
```

TCP 测试：本机监听 → rtt > 0；关闭的本机端口 → `Timeout`；不存在的主机名 → `Err` 含主机名；`timeout_ms 100` 对黑洞地址 `192.0.2.1:9` → `Timeout` 且耗时不超过 1s。

- [ ] **Step 4: ICMP 引擎（`icmp.go`）**

```go
// ICMP 每个地址族一个共享 socket：优先非特权数据报 socket，不可用时退到 raw（§8.2）。
//
// 匹配只靠 payload（进程 nonce + task_id + seq），不看 ICMP ID：Linux 数据报 socket 的回包 ID 被内核
// 改成本地端口，macOS 的公网回包 ID 也会被改写。单读协程只接受 Echo Reply：raw socket 与 macOS 的
// udp6 会先读到自己发出的 Echo Request；macOS 同进程的数据报 socket 之间会互相收到对方的回包，
// 陌生 payload 一律忽略。x/net v0.57.0 在 Darwin 对 udp4 设了 IP_STRIPHDR，读到的字节不带 IP 头。
type ICMP struct {
	clk     clock.Clock
	log     *slog.Logger
	nonce   [8]byte
	seq     atomic.Uint32
	v4, v6  *icmpConn
	initErr []string
}

type icmpConn struct {
	pc      *icmp.PacketConn
	raw     bool
	proto   int
	mu      sync.Mutex
	pending map[pendingKey]chan time.Duration
}

type pendingKey struct {
	task uint64
	seq  uint32
}

func NewICMP(clk clock.Clock, log *slog.Logger) *ICMP {
	e := &ICMP{clk: clk, log: log}
	rand.Read(e.nonce[:])
	e.v4 = e.open("udp4", "ip4:icmp", "0.0.0.0", ipv4.ICMPTypeEchoReply.Protocol())
	e.v6 = e.open("udp6", "ip6:ipv6-icmp", "::", ipv6.ICMPTypeEchoReply.Protocol())
	for _, c := range []*icmpConn{e.v4, e.v6} {
		if c != nil {
			go e.read(c)
		}
	}
	return e
}

// open 记下两种 socket 各自的失败原因：都不可用时每次探测把原因原样回报（§8.2）。
func (e *ICMP) open(dgram, raw, addr string, proto int) *icmpConn {
	if pc, err := icmp.ListenPacket(dgram, addr); err == nil {
		return &icmpConn{pc: pc, proto: proto, pending: map[pendingKey]chan time.Duration{}}
	} else {
		e.initErr = append(e.initErr, dgram+": "+err.Error())
	}
	if pc, err := icmp.ListenPacket(raw, addr); err == nil {
		return &icmpConn{pc: pc, raw: true, proto: proto, pending: map[pendingKey]chan time.Duration{}}
	} else {
		e.initErr = append(e.initErr, raw+": "+err.Error())
	}
	return nil
}

// Available 供 Facts.icmp_available：任一地址族有 socket 即可用。
func (e *ICMP) Available() bool { return e.v4 != nil || e.v6 != nil }

func (e *ICMP) Close() // 关闭两个 socket；读协程因 net.ErrClosed 退出

const payloadLen = 32 // nonce 8 + task_id 8 + seq 4 + 零填充

func (e *ICMP) payload(task uint64, seq uint32) []byte
func (e *ICMP) parsePayload(b []byte) (pendingKey, bool) // 长度与 nonce 都对才算自己的

func (e *ICMP) Probe(ctx context.Context, t *probev1.ProbeTask) Outcome {
	timeout := time.Duration(t.GetTimeoutMs()) * time.Millisecond
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ip, err := resolve(ctx, t.GetTarget(), e.v4 != nil, e.v6 != nil)
	if err != nil {
		return Outcome{Err: err.Error()}
	}
	c, typ := e.v4, icmp.Type(ipv4.ICMPTypeEcho)
	if ip.Is6() {
		c, typ = e.v6, ipv6.ICMPTypeEcho
	}
	if c == nil {
		return Outcome{Err: "icmp unavailable: " + strings.Join(e.initErr, "; ")}
	}
	seq := e.seq.Add(1)
	key := pendingKey{task: t.GetId(), seq: seq}
	reply := make(chan time.Duration, 1)
	c.register(key, reply)
	defer c.unregister(key)
	msg := icmp.Message{Type: typ, Body: &icmp.Echo{ID: int(seq & 0xffff), Seq: int(seq & 0xffff), Data: e.payload(t.GetId(), seq)}}
	wire, err := msg.Marshal(nil)
	if err != nil {
		return Outcome{Err: err.Error()}
	}
	var dst net.Addr = &net.UDPAddr{IP: ip.AsSlice()}
	if c.raw {
		dst = &net.IPAddr{IP: ip.AsSlice()}
	}
	sent := e.clk.Mono()
	if _, err := c.pc.WriteTo(wire, dst); err != nil {
		return Outcome{Err: "send: " + err.Error()}
	}
	select {
	case at := <-reply:
		return Outcome{RttUs: uint32((at - sent) / time.Microsecond)}
	case <-ctx.Done():
		return Outcome{Timeout: true}
	}
}

func (e *ICMP) read(c *icmpConn) {
	buf := make([]byte, 1500)
	for {
		n, _, err := c.pc.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		at := e.clk.Mono()
		msg, err := icmp.ParseMessage(c.proto, buf[:n])
		if err != nil || (msg.Type != ipv4.ICMPTypeEchoReply && msg.Type != ipv6.ICMPTypeEchoReply) {
			continue
		}
		echo, ok := msg.Body.(*icmp.Echo)
		if !ok {
			continue
		}
		if key, ok := e.parsePayload(echo.Data); ok {
			c.deliver(key, at)
		}
	}
}
```

`register`/`unregister`/`deliver` 在 `icmpConn.mu` 下操作 `pending`；`deliver` 对已注销的键什么也不做，对通道用非阻塞发送。

ICMP 测试（`icmp_test.go`）：`NewICMP` 后若 `!Available()` 则 `t.Skip` 并打印 `initErr`（CI 机器缺权限时不假绿）；回环 `127.0.0.1`（timeout 1000）→ rtt > 0；两个任务并发各探 20 次回环 → 全部 rtt、没有一次超时（macOS 上交叉收包只能靠 payload 匹配才过）；`192.0.2.1` 超时 200ms → `Timeout` 且耗时 < 1s；`Close` 后 `Probe` 返回 `Err`；`parsePayload` 对长度不对 / nonce 不对的字节返回 false。注入：把 `parsePayload` 的 nonce 比较去掉 → 并发用例在 macOS 上把对方回包算给自己（rtt 仍有但 seq 不同——测试改为断言引擎内部 `reply` 只对同 key 触发：让假回包经 `deliver` 注入陌生 key，断言 pending 通道无消息）。

Run: `go test -count=1 -race ./internal/agent/prober > /tmp/m3b-t5-prober.log 2>&1; echo $?`
Expected: 0（ICMP 用例在本机应实际运行而不是 Skip：日志里不得出现 `SKIP`）

- [ ] **Step 5: 门禁与提交**

Run: `make ci > /tmp/m3b-t5-ci.log 2>&1; echo $?`（含 `GOOS=darwin go vet`，如 lint 目标里有）

```bash
git add go.mod go.sum internal/agent/prober
git commit -m "agent: 延迟探测引擎与调度，回包按 payload 匹配"
```

---

### Task 6: agent 接入——上报携带结果与版本、`Facts.icmp_available`、装配；端到端断言

**Files:**
- Modify: `internal/agent/client/runner.go`、`internal/agent/client/runner_test.go`、`internal/agent/collect/collect.go`、`cmd/agent/main.go`、`scripts/e2e.sh`

**Interfaces:**
- Consumes: `prober.Scheduler`、`prober.Queue`、`prober.ToProto`、`probelimit.MaxResultAge`。
- Produces：`client.Runner` 新字段 `Prober *prober.Scheduler`、`Results *prober.Queue`（nil 时不带结果、版本报 0）；`collect.Collector.IcmpAvailable bool`。

- [ ] **Step 1: 上报循环（`runner.go`）**

```go
		req := connect.NewRequest(&probev1.ReportRequest{Metrics: m})
		var taken []prober.Result
		if r.Results != nil {
			now := r.Clock.Mono()
			taken = r.Results.Take(now, probelimit.MaxResultAge)
			req.Msg.ProbeResults = prober.ToProto(taken, now)
		}
		if r.Prober != nil {
			req.Msg.TasksVersion = r.Prober.Version()
		}
		// …facts 不变…
		resp, err := r.Client.Report(ctx, req)
		if err != nil {
			// 结果放回队列：下一次上报再带；超过迟到预算的由 Take 丢弃（§4.7）。
			if r.Results != nil {
				r.Results.Requeue(taken)
			}
			// …退避不变…
		}
		// …
		if resp.Msg.Tasks != nil && r.Prober != nil {
			r.Prober.Apply(resp.Msg.Tasks)
		}
```

runner 测试（现有假 client 的模式）：上报带 `TasksVersion` 与队列里的结果；失败后结果回到队列、下一次再带；响应带 `Tasks` → `Prober.Apply` 被调用（用真 `Scheduler` + 假引擎，断言 `Version()` 变化）。

- [ ] **Step 2: Facts 与装配（`collect.go`、`main.go`）**

`Collector` 加字段 `IcmpAvailable bool`，`Facts()` 用它替代常量 `false`。`runRun`：

```go
	ic := prober.NewICMP(clk, log)
	defer ic.Close()
	col.IcmpAvailable = ic.Available()
	if !ic.Available() {
		log.Warn("icmp probing unavailable; icmp tasks will report errors", "reasons", ic.InitErrors())
	}
	queue := prober.NewQueue(prober.QueueCap)
	sched := prober.NewScheduler(prober.Multi{ICMP: ic, TCP: prober.TCP{Clock: clk}}, queue, clk, log)
	defer sched.Stop()
	r := &client.Runner{ /* 原字段 */ Prober: sched, Results: queue}
```

`ICMP.InitErrors() []string` 暴露 `initErr` 供日志。

- [ ] **Step 3: e2e 断言（agent 侧）**

在 Task 4 加的任务创建之后、`wait` 两个 agent 之前不加等待（agent 还要跑约 60s，足够触发多次探测）。`QueryMetrics` 断言之后加：

```sh
probe_body=$(jq -nc --arg nodeId "$node1" --argjson from "$((now - 3600))" --argjson to "$((now + 60))" '{nodeId: $nodeId, from: $from, to: $to, maxPoints: 100}')
[ "$(rpc QueryProbes "$probe_body")" = 200 ] || { echo "FAIL: QueryProbes"; cat "$work/QueryProbes.json"; exit 1; }
# 两个任务都有成功的探测：容器里 127.0.0.1 的 ICMP 走非特权数据报 socket，TCP 连的是宿主上的 hub。
jq -e --arg icmp "$icmp_task" --arg tcp "$tcp_task" '.level == "1m" and ([.series[].taskId] | sort) == ([$icmp, $tcp] | sort) and all(.series[]; any(.samples[]; .sent > 0 and .rttMeanUs != null and .errors == 0))' "$work/QueryProbes.json" > /dev/null || { echo "FAIL: probe results"; cat "$work/QueryProbes.json"; exit 1; }
jq -e 'all(.nodes[]; .facts.icmpAvailable == true)' "$work/ListNodes.json" > /dev/null || { echo "FAIL: icmp_available not reported"; cat "$work/ListNodes.json"; exit 1; }
```

重启后（`ListProbeTasks` 断言之后）加：删除 TCP 任务 → `ListProbeTasks` 剩 1 个、`version == "3"`；`QueryProbes` 仍返回两条 series（历史不随任务删除）；`stats` 段加 `[ "$(get probe_1m)" -ge 2 ]`、`probe_task = 1`、`probe_task_node = 2`。

Run: `make e2e > /tmp/m3b-t6-e2e.log 2>&1; echo $?`
Expected: 0

- [ ] **Step 4: 门禁与提交**

Run: `make ci > /tmp/m3b-t6-ci.log 2>&1; echo $?`

```bash
git add internal/agent cmd/agent/main.go scripts/e2e.sh
git commit -m "agent: 上报携带探测结果并按版本对账任务，端到端验证探测链路"
```

---

## 完成定义

- 六个任务各自提交，每个提交后 `make ci` 绿；Task 4 与 Task 6 后 `make e2e` 绿（两个架构的容器都上线、两类探测都有成功样本、重启后任务与历史仍在、删除任务不删历史）。
- `go test -race -count=1 ./internal/hub/... ./internal/agent/...` 绿。
- 探测族与指标族各自的水位、冻结检查、上卷、清理都有断言；`ingest` 的归属 / 超龄 / 结构三类准入各有正反测试；agent 侧硬限制与 hub 侧硬限制各有自己的测试。
- 生成物与工作树一致（`git status --porcelain -- gen web/src/gen` 为空）。
- spec 的三处修订（§6.3 可空 rtt 列、§8.2 共享 socket 与 payload 匹配、§8.3 `ProbeSample`）与 §13 第 2 项的确认随本计划提交。
