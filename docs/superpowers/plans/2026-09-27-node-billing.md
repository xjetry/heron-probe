# 节点计费与到期 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 节点记下价格、币种、计费周期、到期日与自动续期，只作展示与提醒。面板可以编辑；公开页显示除自动续期之外的几项。新增"到期"告警种类：到期日距今不超过提前天数（含已过期）即触发，天按 hub 的 `--timezone` 计。开着自动续期的节点过了到期日，hub 按周期把到期日推后。

**Architecture:**
- **协议：** `types.proto` 新增 `BillingCycle` 与 `Billing` 子消息（五项加 hub 算好的 `days_left`）。`Node.billing` 与 `UpdateNodeRequest.billing` 都用它，后者整体替换、缺失即全清。`PublicNode.billing` 是 `PublicBilling`，由既有的投影机制从 `Billing` 生成，自动续期连名带号 `reserved`；投影为此扩展到枚举字段，两侧必须引用同一个枚举。`AlertKind` 加 `EXPIRY`，`AlertRule` 加 `days_before`。
- **存储：** schema v9：`node` 加五列，`alert_rule` 加 `days_before`，`alert_state` 加 `fired_expires_on`（到期规则进入 firing 时节点的到期日，`RecordTransition` 随状态写入）。`store.UpdateNode` 改收 `NodeEdit`，在同一个写事务里报告计费是否变化。新增 `RenewExpiry`：只在该行仍是推后所依据的取值时写入。种类与专用字段的组合由 `store.CheckKindFields` 一处裁决，`SaveAlertRule` 对非法组合报错，不再静默清零。
- **告警：** 日期工具与到期扫描在 `internal/hub/alert/expiry.go`。一次扫描在 `writeMu` 下先推后自动续期的到期日，再评估全部启用的到期规则。扫描时机有四个：hub 启动、hub 时区的每个日界、`UpdateNode` 改了计费、保存启用的到期规则。一条规则对一个节点只提醒一次；恢复文案按离开窗口的原因三选一，"日期没变"靠状态里记下的触发时到期日判断，重启之后同样成立。`CheckRule` 经 `store.CheckKindFields` 让探测四项只属于探测规则、`days_before` 只属于到期规则，其余一律 `InvalidArgument`。`alert.Config` 带 hub 时区。
- **服务层：** `UpdateNode` 校验计费；计费有变化就在放开 `nodeMu` 之后、返回之前同步扫描一次，响应是扫描之后的值。`days_left` 在管理端与公开端都按 hub 时区的今天计算，出自同一个 `billingProto`。`SaveAlertRule` 接受到期种类与 `days_before`。
- **前端：** `web/src/lib/billing.ts` 只依赖 `types_pb`，函数接收 `Billing` 与 `PublicBilling` 共有的字段，面板与公开页共用。节点页加计费列与编辑；告警规则页加到期种类；公开页的节点卡片与节点页各加费用、到期两行。

**Tech Stack:**
- 后端：Go 1.27.1、connect-go v1.21.0、modernc SQLite 1.59.0
- 生成：buf 1.50.0（protoc-gen-go、protoc-gen-connect-go、protoc-gen-es）
- 前端：React 19、connect-query、vitest 5.0.1（jsdom）
- e2e：POSIX sh + curl + jq（jq-1.7.1-apple）

**Spec:**
- `docs/superpowers/specs/2026-09-17-probe-architecture-design.md`，以 main 10a7409 的版本为准（§9.4 由 d0c9c32 写入；171e14a 按控制端的裁决改了 §9.1、§9.2、§9.4、§10；3072a7c 补了 §9.2 的日界、只提醒一次、三种恢复文案与几条评估细节；af62cf9 写明 `alert_state` 带触发时的到期日、评估时机四处与"日期没变"的比较基准；bfbab2e 把节点计费收进 §1 与 §15，写明 §10 只填周期时费用行的显示；10a7409 把 §9.2 零点不存在时的日界只定义为新一天的第一个时刻，`time.Date` 的归一方向按 UTC 偏移正负说明）；涉及 §1、§5.4、§6.6、§9.1、§9.2、§9.4、§10、§12、§15、§16。
- 方针：`docs/guidelines/agent-first.md`。
- `FEATURES.md` 第 51 行：节点费用与到期日是提醒用的展示值，不触发 Litestream 的重新考虑条件。本计划不改它。

工作树 `/Users/xjetry/work/vibe/probe-billing`，分支 `billing`，基点 af62cf9（写计划时 main 也在这里）。

## Global Constraints

**字段与校验（§9.4）**
- 五项与 `days_left` 收在 `types.proto` 的 `Billing` 子消息里，`Node.billing` 与 `UpdateNodeRequest.billing` 都用它。
- 价格：TEXT，十进制文本，`^\d{1,9}(\.\d{1,2})?$`；空表示未填。
- 币种：`^[A-Z]{3}$`（ISO 4217）；价格非空时必填，币种可以单独填。
- 周期：`BillingCycle`，月、季、半年、年、两年、三年（1/3/6/12/24/36 个月）；未指定表示没有周期。
- 到期日：`YYYY-MM-DD`，必须是存在的日子；空表示没有到期日。
- 自动续期：默认关；开着时周期与到期日都必须非空。
- 校验只在 `UpdateNode` 一处（`billingOf`），与 `traffic_reset_day`、`offline_grace_s` 同一个入口、同一格式的错误：`<请求路径>: <约束>; got <收到的值>`，路径写到子消息里的字段（`billing.price`）。任一项不合格，整次更新不写入。
- 整体替换：`billing` 缺失等于五项全清；空串、未指定与 `false` 也是清除，没有"不改"的取值。请求里的 `days_left` 不读。
- 回显：五项都没填时 `Node.billing` 与 `PublicNode.billing` 都缺失。
- 这些是展示值：hub 不汇总、不换算，也不拿价格做任何计算。

**日期与时区**
- 天的边界取 hub 的 `--timezone`，与流量周期同一个 `loc`。
- `days_left` = 到期日 − 今天，按日历日计，由 hub 算好下发；没有到期日时缺失，负数是已过期天数。面板与公开页只显示它，不在浏览器里再算。
- "合法日期"只有一个口径：`alert.ParseDate`。写侧校验、扫描与 `days_left` 都调它。

**自动续期**
- 到期日早于今天、开着自动续期、周期非空：`while 到期日 < 今天: 到期日 += 周期月数`。
- 日号超过目标月的天数时钳到月末；钳过之后日号停在钳后的值，不回到原来的日号。
- 推后的日期落库，并记一行 `node expiry renewed` 日志（`node_id`、`node`、`cycle`、`from`、`to`）。
- 写回经 `store.RenewExpiry` 的条件更新：该行的自动续期、周期与到期日仍是推后所依据的取值才写。

**到期规则**
- `days_before` 取 1–365。到期日减今天不超过它（含负数）即 `firing`，没有 `pending`；没有到期日按恢复处理。
- 种类专用字段只属于自己的种类：探测四项（任务、指标、阈值、持续分钟）对离线与到期规则必须为零，`days_before` 对离线与探测规则必须为零，一律 `InvalidArgument`。由 `store.CheckKindFields` 一处裁决：`store.SaveAlertRule` 对非法组合报错，`alert.CheckRule` 在保存与载入时调它并转成协议层的字段错误。离线规则带探测字段原来被存储层静默清零，从此改为拒绝（§9.1）。
- 面板对非探测种类下发的探测字段与提前天数都是零值，否则面板自己的保存会被拒。
- `days_before` 不是规则身份，与 `threshold`、`for_minutes` 同类：改它保留状态。
- 扫描时机：hub 启动一次；hub 时区的每个日界一次（零点不存在的日子取新一天的第一个时刻）；`UpdateNode` 改了任一计费字段后同步一次；保存启用的到期规则后同步一次。
- 同一次扫描先推后，再评估；文案按 §9.2 原文。
- 一条规则对一个节点只提醒一次：进入窗口时按当时的剩余天数写"将于"或"已于"，之后不再发第二条；到期当天写"剩 0 天"。
- 恢复文案按离开窗口的原因：清空写"已清除到期日"；到期日与触发时不同写"到期日已更新为"；相同（提前天数调小）写"已不在提醒窗口内"。触发时的到期日存在 `alert_state.fired_expires_on`，不从事件里读回（设计决定 18）。
- 事件的 `value` 是剩余天数，因清除到期日而恢复时为 0。

**公开**
- `PublicNode.billing` 是 `PublicBilling`：价格、币种、周期、到期日与 `days_left`，与 `Billing` 同号、同名、同类型、同 presence；自动续期的号 5 与名 `auto_renew` 都在 `reserved` 里。
- 它由 `NewPublic` 构造的投影生成，与 `PublicFacts`、`PublicMetrics` 同一机制，对不齐在构造期就 panic。投影扩展到枚举字段：两侧必须引用同一个枚举类型，否则同一处 panic。
- 公开入口经 `lib/billing.ts` 只引用 `types_pb`，import 扫描测试保持绿。

**存储**
- schema v9。迁移只引用冻结的语句（`migrationV9` 的七条 `ALTER`）；从 v8 迁来的库与新建库逐表逐列一致，列序也一致。
- `alert_state.fired_expires_on` 每次状态写都整行给出：触发时是节点当时的到期日，其余写入是空串，上一个状态的日期不会留到下一个状态。

**代码与提交规范**
- 代码注释与提交信息不写过程信息：任务、步骤、轮次编号，方案代号，审阅引用，"按上一轮"。
- 注释写 WHY 与不变式，并指明前提由谁保证。
- 不打补丁：根因在哪层就在哪层修，不在调用方加特判；同形状的缺陷全库一次改齐；不加兼容层。
- 不变式靠显式检查承载，不靠隐式机制兜底。

**验证规范**
- 每条新断言做一次缺陷注入，并确认它红在正确原因上。
- 注入在该任务提交之后进行，这样 `git checkout -- <文件>` 还原到的是已提交的实现。
- 注入前用 `git diff --stat` 非空确认注入确实落地。
- 判成败的命令写成 `cmd > /tmp/<名>.log 2>&1; echo $?`，不接管道。
- 缺陷注入表格里的 `\|` 是 Markdown 表格对竖线的转义，照抄命令时写成 `|`。`-run` 的正则里它表示"或"，留着反斜杠会变成匹配字面竖线，一个测试都不跑，却退出 0。
- vitest 的注入命令逐个写出测试文件。把文件名放进 shell 变量再展开时，zsh 不按空格拆分，两个文件会变成一个不存在的路径：vitest 报 `No test files found` 并退出 1，看起来像红了。
- Go 测试一律 `go test -count=1`。
- 测试里的正向等待只用 `internal/testwait.Bound` / `Until`。
- 生成物（`gen/`、`web/src/gen/`）只由 `make gen` 产生，不手改。`make ci` 在最后核对生成物已入库，所以每个任务的 `make ci` 放在提交之后跑。

**执行约束**
- 实现者不改 `docs/`；不派子代理。
- 每条命令以 `cd /Users/xjetry/work/vibe/probe-billing && ` 开头。
- 18080/18081 是 e2e 的固定默认端口，只有 e2e 任务能用；本地实验避开 18079–18092。`scripts/e2e.sh` 的两个端口可由 `E2E_HUB_PORT`、`E2E_HOOK_PORT` 覆盖（Task 8），本计划的命令都用默认端口。

## Review Focus

1. **夏令时从零点开始的时区。** America/Santiago 2026-09-06、America/Havana 2026-03-08 没有 00:00。
   - 下一个日界必须是 01:00（新偏移），严格晚于现在，本地日期是明天，再早 1 纳秒还是今天。
   - 夏令时不在零点的时区（America/New_York）与夏令时结束的那天，日界仍是 00:00。

   - 循环在日界到点时确实再扫描一次：启动扫描之后，墙钟跨过东八区零点，今天到期、开着自动续期的节点被推后。
   - 一轮扫描跨过零点时，新一天的扫描不被跳过：循环在扫描之前读钟定下一次触发，扫描拖过日界就立即重扫。

   测试在 Task 3 Step 1（`TestNextDayStartIsTheFirstInstantOfTheNextLocalDay`、`TestRunExpirySweepSweepsAgainAtTheDayBoundary`、`TestRunExpirySweepDoesNotSkipADayBoundaryCrossedDuringASweep`）。注入 a 去掉修正后，两个时区都红在"next day starts …T23:00:00, want …T01:00:00"；注入 ag 让循环只扫启动那一次，后两个测试红；第三个测试按读钟次序切换零点前后，零点前读 1 次与 2 次各一例：注入 aj 把读钟挪回扫描之后，只有读 1 次那例红；注入 ak 把负时长改成等再下一个日界，只有读 2 次那例红。

2. **极端日期。** `ParseDate` 接受 `0000-01-01` 与 `9999-12-31`。
   - 从 2026-09-27 算，`days_left` 分别是 −740251 与 2912173，不因 `time.Duration` 饱和变成 ±106751。
   - 自动续期从 0 年的 1 月 31 日按月推后照常工作。
   - 拒绝五位年、带符号的年、全角数字、前后空白与带时刻的写法。

   测试在 Task 3 Step 1（`TestDaysLeftCountsCalendarDays`、`TestRenewedExpiry`、`TestParseDateAcceptsOnlyRealYYYYMMDD`）与 Task 4 Step 1（`TestUpdateNodeAcceptsBoundaryBillingAndIgnoresDaysLeft` 保存 `9999-12-31`）。

3. **月末钳位与闰年。**
   - 1 月 31 日按月续到 4 月：依次 2 月 28 日、3 月 28 日、4 月 28 日，不回到 31 日。
   - 2028-01-31 按月推后得 2028-02-29；2024-02-29 按年推后到 2026-02-28。
   - 到期日恰是今天不推后；季度推后恰落在今天即停。

   测试在 Task 3 Step 1（`TestRenewedExpiry`）。

4. **陈旧的编辑与续期的竞争。**
   - 扫描读快照之后，管理员改了周期、到期日或关掉自动续期：按旧快照算出的日期不写回。
   - 管理员的表单带着推后之前的旧到期日提交：库里已是推后的日期，两者不同即算计费变化，随即重新扫描、再推后一次。
   - 只改名称不触发扫描；只改币种、只改周期都算计费变化。
   - 续期写回没有落定的节点，本轮不按过期的快照评估：不触发、不推后、保留状态。条件更新落空（快照之后计费被改过，或节点已被删除）是一种，写回出错是另一种；后者的错误随扫描返回，下一轮重试。

   测试在 Task 2 Step 1（`TestRenewExpiryWritesOnlyOverTheValuesItWasComputedFrom`、`TestUpdateNodeReplacesBillingAndReportsChange`）、Task 3 Step 1（`TestSweepExpirySkipsANodeWhoseSnapshotIsStale`、`TestSweepExpirySkipsANodeWhoseRenewalFailed`）与 Task 4 Step 1（`TestUpdateNodeSweepsExpiryWhenBillingChanges`）。

5. **hub 时区、UTC 与访客时区三者不同。**
   - hub 在东八区、时钟为 UTC 16:30 时，今天已是次日：`days_left` 比按 UTC 算少 1。
   - 管理端、公开端与启动扫描是同一个口径。
   - 公开快照的 `now` 与 `days_left` 出自同一次读钟：给公开服务一个每读一次就前进一天的钟，响应里的两者仍然相符。
   - 前端只显示 hub 下发的 `days_left`。

   测试在 Task 3 Step 1（`TestTodayTakesTheCalendarDayInTheZone`、`TestSweepExpiryCountsDaysInTheHubZone`、`TestServeRenewsExpiryAtStartupInTheHubZone`）与 Task 4 Step 1（`TestDaysLeftUsesTheHubZone`、`TestServeReportsDaysLeftInTheHubZone`、`TestPublicSnapshotReadsTheClockOnce`）。Task 4 注入 v 让 `GetSnapshot` 分两次读钟，只有最后这个测试红：`clock.Fake` 两次读到的是同一个值。

6. **畸形的计费输入。** 以下每一项都返回 `InvalidArgument`，错误以 `billing.<字段>` 写明路径、约束与收到的值，库里的计费不变：
   - 价格 `12.345`、`1234567890`、`-1`、`12.`、`.5`、`1e3`、` 12`、全角 `１２`
   - 有价格没币种
   - 币种 `usd`、`USDT`
   - 周期取表外的 99
   - 到期日 `2026-02-29`、`2026-1-05`、`2026-10-01T00:00:00Z`
   - 自动续期只带到期日、只带周期

   测试在 Task 4 Step 1（`TestUpdateNodeRejectsMalformedBilling`）。

7. **规则带着别的种类的字段，包括从库里载入的。**
   - 离线规则带任一探测字段（含 NaN 阈值）或 `days_before` 被拒，协议层什么也不保存。
   - 到期规则带探测字段、`days_before` 为 0 或 366 被拒；探测规则带 `days_before` 被拒。
   - 绕过引擎直接写进库的越界到期规则，载入时跳过并记一行 Warn，合法的照常载入。
   - 存储层同样拒绝非法组合（新建与修改都不写入），不再静默清零；换种类时同时清掉专用字段才是合法组合。
   - 面板新建离线规则、把探测或到期规则改成离线时，下发的探测字段与提前天数都是零值。

   测试在 Task 2 Step 1（store 的 `TestSaveAlertRuleRejectsFieldsOfOtherKinds`、`TestAlertRuleDaysBeforeBelongsToExpiryRules`）、Task 3 Step 1（`TestCheckRule` 的新用例、`TestLoadChecksExpiryRules`）、Task 4 Step 1（api 的 `TestSaveAlertRuleRejectsFieldsOfOtherKinds`、`TestSaveAlertRuleExpiryKind`）与 Task 6 Step 1（`AlertRules.test.tsx` 的离线与切换用例）。

8. **库里读不懂的到期日**（只有手改库会产生）。
   - 扫描跳过该节点：不触发，也不把已触发的告警当作恢复发出去。
   - 开着自动续期也不推后。
   - 每次扫描对每个这样的节点记一行 Warn，不去重：扫描两次就是两行。
   - `days_left` 缺失而不是 0，到期日原样下发。

   测试在 Task 3 Step 1（`TestSweepExpirySkipsUnreadableDates` 核对前三条并做两次扫描，`TestRenewedExpiry` 的 unreadable date 用例）与 Task 4 Step 1（`TestUnreadableExpiresOnHasNoDaysLeft`）。注入 ac 删掉那行 Warn、ai 让它每个节点只记一次，都红。

9. **公开的计费与 `Billing` 对不齐。** 以下每一种都让投影在构造期 panic，报出哪一侧哪个字段：
   - `PublicBilling` 的字段号与 `Billing` 不同
   - 删掉 `reserved 5`（自动续期的号）
   - 一侧 optional、一侧不是
   - 周期换成别的枚举类型
   - 源里有未公开、也未 reserve 的枚举字段

   同一枚举的字段照常投影，按编号复制。测试在 Task 1 Step 1（`TestNewProjectionRejectsMisalignedFields` 的枚举用例、`TestPublicBillingProjectsFromBilling`）。

10. **请求里的 `days_left` 与缺失的 `billing`。**
    - 请求里的 `days_left` 不影响保存与回显，回显是 hub 算的值。
    - 不带 `billing` 与空的 `billing` 都是五项全清，回显里 `billing` 缺失。
    - 五项都没填的节点在公开快照里没有 `billing`。

    测试在 Task 4 Step 1（`TestUpdateNodeAcceptsBoundaryBillingAndIgnoresDaysLeft`、`TestPublicSnapshotCarriesBillingWithoutAutoRenew`）；e2e 的请求体带 `daysLeft: 999`。

11. **恢复文案按原因，触发日期经得起重启。**
    - 日期没变、只是提前天数调小：`节点 X 已不在提醒窗口内（规则 R）`，value 是剩余天数。hub 在触发之后重启过也是这一句，触发日期从 `alert_state` 读回。
    - firing 期间改过日期（仍在窗口内，不发事件），再调小提前天数：日期与触发时不同，写"到期日已更新为"新日期。
    - 留在 firing 的扫描不改记下的日期；恢复与不带事件的写都把它清空。

    测试在 Task 2 Step 1（`TestAlertStateFiredExpiresOnFollowsEachWrite`、迁移测试里旧状态行取空串）与 Task 3 Step 1（`TestSweepExpiryRecoverySummaryFollowsTheReason`）。注入 x、y、z 分别去掉库读回、内存发布与 `apply` 的"状态不变直接返回"：x 与 z 红在第一步（重启后、以及保存规则前那次扫描之后），y 红在第三步（没有重启、日期来自内存）。

12. **只提醒一次。** 从"将于"走到到期当天、再走到"已于"，四天四次扫描只有进入窗口时那一条；到期当天写"剩 0 天"。`NextExpiry` 与 `apply` 各自都挡住第二条，两道都去掉才重复（Task 3 注入 ab、ad、ae）。测试在 Task 3 Step 1（`TestSweepExpiryNotifiesOncePerEntry`）。

## 实验与读码结论

凡由实验得出的结论，都注明了版本；换版本要重跑，不能直接改数字。

1. **go1.27.1 darwin/arm64：`time.Date` 对不存在的零点的归一方向随时区而异。**
   - 做法：对 `time.Date(y, m, d, 0, 0, 0, 0, loc)` 在几个时区的夏令时切换日取值，另取该时刻 `ZoneBounds()` 的 `end`。
   - 结果（原文）：

     ```
     America/Santiago 2026-09-06 00:00 -> 2026-09-05 23:00:00 -0400 -04
        ... next midnight 2026-09-05 23:00:00 -0400 -04 nextLocalDate=2026-09-05 zoneBoundsEnd=2026-09-06 01:00:00 -0300 -03
     America/Havana 2026-03-08 00:00 -> 2026-03-07 23:00:00 -0500 CST
        ... next midnight 2026-03-07 23:00:00 -0500 CST nextLocalDate=2026-03-07 zoneBoundsEnd=2026-03-08 01:00:00 -0400 CDT
     America/New_York 2026-03-08 00:00 -> 2026-03-08 00:00:00 -0500 EST
     Asia/Beirut 2026-03-29 00:00 -> 2026-03-29 01:00:00 +0300 EEST
     Africa/Cairo 2026-04-24 00:00 -> 2026-04-24 01:00:00 +0300 EEST
     ```

   - 结论：归一的方向随 UTC 偏移的正负而异（`time.Date` 先把墙钟读数当作 UTC 去查偏移，再按换算结果是否越出该时段复核）。偏移为负的 Santiago 与 Havana 往回给前一天 23:00（旧偏移），本地日期没变；偏移为正的 Cairo 与 Beirut 往前给新一天的 01:00（新偏移），本地日期已变，它本身就是新一天的第一个时刻。拿 23:00 定时，会在旧的一天里触发，之后每一轮算出的仍是这个已过去的时刻，定时器立即触发，空转一小时。spec §9.2 原先写"由 `time.Date` 归一化"，3072a7c 已据此改写，10a7409 又把归一方向按偏移正负写全。
   - 日期没变时取 `ZoneBounds()` 的 `end`，恰是新的一天的第一个时刻（01:00，新偏移）；日期已变时 `time.Date` 的结果本身就是答案。Task 3 的 `nextDayStart` 这样做，判断只看本地日期有没有变、不看时分：按"时分不为零就取 `ZoneBounds` 的 end"去判，偏移为正的那两例会取夏令时时段的结束处，日界定到几个月后夏令时结束的那次切换（Task 3 注入 an）。测试覆盖往回的两个时区与往前的 Cairo、Beirut，另以 UTC、固定的东八区、New York 的夏令时开始日与两个时区的夏令时结束日作对照。

2. **go1.27.1：`time.Time.Sub` 在约 292 年外饱和。**
   - 从 2026-09-27 到 9999-12-31：`Sub` 折合 106751.99 天，Unix 秒相减得 2912173 天；到 0000-01-01：−106751.99 对 −740251。
   - `time.Parse(time.DateOnly, …)` 接受 `0000-01-01` 与 `9999-12-31`，所以天数必须用 Unix 秒相减（`daysBetween`）。2912173 在 `int32` 范围内，`days_left` 用 `int32` 不溢出。

3. **go1.27.1：`time.Parse(time.DateOnly, s)` 就是"合法日期"的口径。**
   - 拒绝（原文节选）：`2026-02-30: day out of range`、`2026-02-29: day out of range`、`2026-13-01: month out of range`、`2026-1-05 … cannot parse "1-05" as "01"`、`10000-01-01 … cannot parse "0-01-01" as "-"`、`+2026-01-01`、前导或尾随空格（`extra text: " "`）、全角数字。
   - 接受：`2028-02-29`、`0000-01-01`、`9999-12-31`，三者 `Format(time.DateOnly)` 都还原成原串。布局要求恰好四位年、两位月日且不带多余字符，接受的串由年月日唯一确定，所以 `ParseDate` 不另做往返比较。

4. **jq-1.7.1-apple：`strftime`、`strptime`、`mktime` 一律按 UTC，不看 `TZ`。**
   - `TZ=UTC`、`TZ=Asia/Shanghai`、`TZ=America/Santiago` 下，`1790267400 | strftime("%Y-%m-%d %H:%M")` 都是 `2026-09-24 16:30`，`"2026-09-30" | strptime("%Y-%m-%d") | mktime` 都是 `1790726400`。
   - e2e 的 hub 以 `--timezone UTC` 运行，jq 算出的"今天"与 hub 一致，与跑 e2e 的机器时区无关。

5. **读码：基点上的投影只接受单值标量，`PublicNode` 不经投影。**
   - `internal/hub/api/public.go` 里 `newProjection` 只用于 `PublicFacts` 与 `PublicMetrics`；`PublicNode` 在 `GetSnapshot` 里逐字段构造。
   - `newProjection` 的 `projectable` 拒绝消息、枚举、列表与 map，并要求源消息里没公开的字段全部在公开消息里 reserved。`PublicNode` 相对 `Node` 满足不了后者（`public`、`note` 都不在 `PublicNode` 里）。
   - 所以计费的公开部分做成子消息 `PublicBilling`，从 `Billing` 投影；周期是枚举，投影要扩展到枚举字段。枚举按编号复制，编号的含义取决于枚举类型，所以两侧必须引用同一个枚举，这条与现有的号、类型、presence 核对放在同一处。

6. **实测：基点上离线规则带探测字段被接受并静默清零。**
   - 做法：在 3d7bb0a 的副本里（它到 af62cf9 之间只改了 `docs/`），对 `offline()` 规则设任务 1、丢包指标、阈值 20、持续 3 分钟，调 `CheckRule` 与 `Engine.SaveRule`。
   - 结果：`CheckRule = <nil>`；`SaveRule err=<nil> saved task=0 metric="" threshold=0 for=0`。存储层按种类把非探测规则的四项写成零值，没有校验。
   - Task 4 的注入 t 在协议层复现了同一行为：去掉离线分支的检查后，四条带探测字段的离线规则都被保存，列表里看不到那些字段。§9.1（171e14a）已改为显式拒绝。

7. **读码：protobuf-es v2 的生成结果。**
   - `make gen` 之后，`Node.billing`、`UpdateNodeRequest.billing` 生成 `billing?: Billing | undefined`，`PublicNode.billing` 生成 `billing?: PublicBilling | undefined`，两者的 `days_left` 都是 `daysLeft?: number | undefined`；`uint32 days_before` 生成 `daysBefore: number`。
   - 前端据此以 `node.billing?.…` 读取，`billing` 缺失即"什么都没填"。

8. **buf 1.50.0：加消息、字段与枚举值不破坏兼容。**
   - Task 1 的 proto 改动上，`buf lint` 退出 0；`buf breaking --against ".git#ref=af62cf9"` 退出 0。Task 1 Step 4 写的是 `$(git merge-base main HEAD)`，在工作树里它就是 af62cf9。
   - 冒烟：把基点上已有的 `Traffic.reset_day` 从 `uint32` 改成 `string`，同一条 `buf breaking` 退出 100 并指出该字段，说明它确实在比对。只改新加的 `Billing` 的字段类型，同一条命令仍退出 0：基点上没有这个消息，比对不到。
   - 在 git worktree 里（`.git` 是文件）`.git#ref=…` 同样可用：干净时退出 0，同一冒烟退出 100。

9. **读码：`describe()` 按 `PRAGMA table_info` 的顺序比列。**
   - 迁移测试比较的是逐列的名字、类型、非空、默认值与主键，列序也在内。
   - 所以 `schema.go` 里新列必须按迁移 9 的 `ADD COLUMN` 顺序写在表尾。Task 2 注入 d（对调两条 `ADD COLUMN`）因此红在 `migrated schema differs from fresh schema`。

10. **读码与实跑：锁序。**
    - 到期扫描取 `alert.Engine.writeMu`；`nodeMu` 只在 `api` 包里。
    - 不成环的保证是：`alert` 包不 import `api`，且引擎经 `SetSender` 注入的实现（当前是 `cmd/hub/serve.go` 装配的 `alert.Queue`）也不在 `api` 里，任何持 `writeMu` 的路径都取不到 `nodeMu`。只看 import 方向不够：注入的实现若来自 `api`，也能在 `writeMu` 下取到 `nodeMu`。两条都成立时，`api` 里持 `nodeMu` 再取 `writeMu` 也不成环。
    - 在 Task 1–8 做完的树上核对过：`go list -deps ./internal/hub/alert` 列出的项目内包只有 `gen/probe/v1`、`clock`、`hub/metric`、`hub/live`、`probelimit`、`hub/store` 与 `alert` 自己，没有 `hub/api`；同一命令对 `./cmd/hub` 列出 `hub/api`（对照）。`nodeMu` 只出现在 `api` 包的 `nodes.go` 与 `service.go`。取 `Engine.writeMu` 的是 `Load`、`SaveRule`、`DeleteRule`、`SaveChannel`、`DeleteChannel`、`Forget`、`SweepOffline`、`EvaluateProbes`、`SweepExpiry` 九个方法，`apply` 与 `Queue.Enqueue` 在它们之下被调用。
    - 本计划的 `UpdateNode` 并不持着 `nodeMu` 扫描（设计决定 6），两把锁在这里不嵌套。`DeleteNode` 在锁外调 `alerts.Forget` 是为了不挡住别的编辑，与成环无关。

11. **实测：用字面量构造公开消息的测试，挡住了构造期核对。**
    - 投影测试原先用 `&probev1.PublicBilling{Price: …}` 写期望值。把 `PublicBilling.price` 改成 optional、或把周期换成别的枚举之后，这个测试先编译失败，红在编译错误上，没有走到投影的 panic。
    - 改为从 JSON 读入期望值、用反射取 `days_left` 之后，同样两处改动都红在构造投影的 panic 上（Task 1 注入 c、d）。

12. **读码：告警事件按保留期删除；SQLite 的 `ADD COLUMN` 把列排在最后。**
    - `store` 的维护任务按保留期执行 `DELETE FROM alert_event WHERE at < ?`，而一个过期节点可以一直处在 firing。恢复时要知道触发时的到期日，不能指望那条触发事件还在；计划把它随状态存（设计决定 18）。
    - `alert_state` 的建表语句以 `PRIMARY KEY (rule_id, node_id)` 结尾。`ALTER TABLE … ADD COLUMN` 之后，`PRAGMA table_info` 里新列是第五列；新建库把 `fired_expires_on` 写在 `since_at` 之后、主键约束之前，也是第五列。迁移测试逐列比对二者（实验 9），Task 2 注入 o（迁移漏掉这一列）红在这里。

13. **实测：`NextExpiry` 与 `apply` 各自都挡住第二条提醒。**
    - 只去掉 `NextExpiry` 对留在 firing 的那一道，或只去掉 `apply` 的"状态不变直接返回"，`TestSweepExpiryNotifiesOncePerEntry` 都照常通过；两道都去掉，四次扫描对两个节点每次都发，共八条（Task 3 注入 ad、ae、ab）。
    - 所以 `expirySummary` 的注释把两处并列写，不说哪一处是关键。`apply` 那一道另有一个作用：它让留在 firing 的扫描不写库，触发日期因此保留（注入 z）。

14. **实测：SQLite 触发器 `RAISE(IGNORE)` 让条件更新落空。**
    - 在 `node` 上建 `BEFORE UPDATE OF expires_on` 的触发器、体内 `SELECT RAISE(IGNORE)`，`RenewExpiry` 的 `UPDATE` 不报错，`RowsAffected` 为 0，返回 false。这与"扫描读快照之后、写回之前有人改了计费"对 `RenewExpiry` 的效果相同。
    - `TestSweepExpirySkipsANodeWhoseSnapshotIsStale` 用它确定地造出过期快照；Task 3 注入 af（续期落空时照常评估）红在"节点被按过期的快照触发"上，说明触发器确实让写回落空。
    - 体内换成 `SELECT RAISE(ABORT, 'renewal write failed')`，`RenewExpiry` 返回错误。`TestSweepExpirySkipsANodeWhoseRenewalFailed` 用它造出写回出错：`SweepExpiry` 返回的错误含这句原文，Task 3 注入 al（出错时照常评估）红在"节点被按未推后的快照触发"上。

15. **实测：日界循环在扫描之后才读钟，会跳过扫描期间跨过的日界。**
    - 先扫描、再读钟、再按 `nextDayStart` 定时的写法里，扫描读钟时还在 D 日，扫描结束后循环读钟已过零点，`nextDayStart` 算出 D+2 零点，定一个约 24 小时的定时器。D+1 的日界扫描整个丢掉，这一天的续期与提醒晚一整天。循环里在零点前开始的扫描有两种：启动那一次，以及墙钟被往回调、定时器提前触发的那一次；hub 恰在零点前启动、节点多或写库慢时就会落进这个窗口。
    - 在 billing 分支的 60ac1dc 上施加 Task 3，用一次性测试实跑（跑完删除）。夹具另加 3000 个到期日落在提醒窗口里的节点，到期规则绕过引擎直接存进库（保存时不扫描），启动扫描要写 3000 次触发。钟用 `tickingClock`，起点在东八区 9 月 25 日零点前若干毫秒；看 9 月 24 日到期、开着自动续期的节点 5 秒内推后没有。

      | 零点前余量 | 扫描之后读钟 | 扫描之前读钟 |
      |---|---|---|
      | 500 ms | 启动后 513 ms 推后 | 启动后 511 ms 推后 |
      | 100 ms | 5 秒内没推后，墙钟已过零点 4.9 秒 | 启动后 194 ms 推后 |
      | 20 ms | 5 秒内没推后，墙钟已过零点 4.99 秒 | 启动后 194 ms 推后 |

    - 测试里用 `clock.Fake`、看到启动扫描的效果后 `SetWall` 到次日，暴露的是同一个机制：拨钟若赶在循环读钟之前，循环就按次日算出下一个日界。在扫描之后读钟的写法上实跑过：忙等看到效果后立刻拨钟，30 遍红 3 遍，循环里临时打印的定时时长是 `23h59m59.8s`。
    - 所以改的是循环（设计决定 21），不是测试。`tickingClock` 用例走"定时器等到日界再扫"这条路。另一个用例用 `scriptedClock`，按读钟次序而不是真实时间切换零点前后，零点前读 1 次与 2 次各一例。读 1 次时循环读到 24 日、扫描读到 25 日；读 2 次时循环与扫描都读到 24 日，扫描结束已过零点，定时器时长为负、立即重扫。Task 3 注入 aj（扫描之后才读钟）只让前一例红，注入 ak（负时长改成等再下一个日界）只让后一例红。

## 设计决定（spec 未定，由本计划定）

1. **日期工具放在 `alert` 包（`expiry.go`），不新开包。**
   - `api` 已依赖 `alert`，写侧校验（`billingOf`）与 `days_left` 直接调 `alert.ParseDate`、`alert.Today`、`alert.DaysLeft`；读侧扫描在同一个文件里。"合法日期"与"今天"各只有一个实现。

2. **日期表示为该日 UTC 零点的 `time.Time`；天数用 Unix 秒相减。**
   - 解析、"今天"与相减都在这种值上做，两个日期相差几天与任何时区的夏令时无关。hub 时区只在 `Today` 里出现一次。
   - Unix 秒相减的理由见实验 2。

3. **下一个日界用 `nextDayStart`：`time.Date` 给出的时刻本地日期没变时，取该时刻所在时段的结束处。** 理由见实验 1；spec §9.2 写的就是这个做法。

4. **时区经 `alert.Config.Location`、`api.Config.Location`、`api.PublicConfig.Location` 传入，三个构造函数遇 nil 即 panic。**
   - `serve` 把同一个 `loc`（`--timezone` 解析的结果，流量周期也用它）交给三者。
   - nil 在运行中才会在 `time.Time.In` 里 panic；装配错误应当在启动时暴露。

5. **自动续期的写回是条件更新；计费是否变化在同一个写事务里算出。**
   - `RenewExpiry(id, cycle, from, to)` 只在 `auto_renew = 1 AND billing_cycle = cycle AND expires_on = from` 时写。扫描按它读到的快照推后，快照之后 `UpdateNode` 改了计费，按旧快照写回会盖掉管理员刚保存的值；条件不成立时不写，由那次 `UpdateNode` 触发的扫描按新值重算。
   - `store.UpdateNode` 在写事务里先读旧的五项，再写新值，返回二者是否不同。`RenewExpiry` 也经单写协程，读与写之间插不进一次推后，所以比较的对象就是这次写入实际覆盖的值。表单带着推后之前的旧日期提交时，二者不同，调用方随即扫描、再推后一次。

6. **`UpdateNode` 先放开 `nodeMu`，再同步扫描，最后读回响应。**
   - `nodeMu` 只罩住库写入与 `SetResetDay`，二者不分叉的理由不变。
   - 扫描要等 `writeMu`（离线巡检、探测评估、日界扫描都可能正持有），再做一整轮续期写回与状态写；持着 `nodeMu` 等它，一次计费编辑就会挡住其它节点的编辑与删除。这与 `DeleteNode` 把清理放在锁外同一个理由。
   - 扫描之后才 `GetNode` 回读，响应里的到期日与 `days_left` 已是扫描之后的值，续费后立刻看到恢复。
   - 放锁之后节点可能被并发的 `DeleteNode` 删掉，回读得到不存在时按 `NotFound` 应答。这条路径没有测试：要让删除恰好落在扫描与回读之间，得在生产代码里加钩子，不值。
   - 扫描失败只记日志：修改已提交，下一次扫描会再评估。扫描用 `context.WithoutCancel`，客户端断开不打断已开始的扫描。
   - 锁序见实验 10。锁的范围由代码结构保证，没有测试钉住"扫描时不持 `nodeMu`"。

7. **保存启用的到期规则后立即评估一次（§9.2 的第四处时机）。** 停用的规则保存后没有状态（`publishRule` 按既有规则裁剪），扫描跳过停用的规则。

8. **库里读不懂的到期日：跳过评估、保留状态、记一行 Warn。**
   - 写入口都校验过日期，只有手改库会产生这种值。按"没有到期日"处理会把已触发的告警当作恢复发出去；按"已过期"处理会凭空触发。跳过两者都不做。
   - 这种节点不推后，`days_left` 缺失（不是 0：0 会显示成今天到期）。Warn 每次扫描对每个这样的节点记一行，不去重：它一直在，日志就一直提醒。

9. **种类专用字段由 `store.CheckKindFields` 一处裁决，存储层与 `CheckRule` 都用它（§9.1）。**
   - 谓词在 `store`：`alert` 引用 `store`，反过来不行，所以唯一的实现只能放在 `store`。它返回 `store.KindFieldError`（字段名与约束），`alert.CheckRule` 在已知种类上调它，转成 `FieldError`，协议层的错误文案因此不变。
   - `store.SaveAlertRule` 对非法组合报错，不再改写：原来它把非探测规则的探测四项静默清零（本计划又加了 `days_before`），调用方发了什么、存下的却是零值，无从察觉。按种类选列、别的列写 NULL 仍然保留，那是落列，不是校验。
   - 保存（`Engine.SaveRule`）与载入（`Engine.Load`）都经过 `CheckRule`，手改过的库也载入不了这种规则。
   - 这改变了离线规则的既有行为：`SaveAlertRule` 对带探测字段的离线规则原来返回成功并回显零值（实验 6），现在返回 `InvalidArgument`，错误写明字段（如 `rule.threshold must be 0 unless kind is probe`）。
   - Task 2 与 Task 3 之间有一个中间状态：存储层已拒绝，`CheckRule` 还没调谓词，协议层会把这种拒绝报成内部错误。只在两个提交之间存在，Task 3 提交后消失；中间提交的 `make ci` 照常为 0。
   - 面板不会因此被拒：`toRule` 只发当前种类的字段，其余在协议里是零值。Task 6 把"新建离线规则"的用例补上阈值、持续分钟与提前天数的零值断言（草稿里持续分钟默认 3、提前天数默认 7），与既有的"改成离线"用例一起钉住；"探测规则改成到期"的用例从带着任务与阈值的探测规则出发，钉住到期分支不带探测字段。
   - e2e 不发这种载荷。既有测试里有两处依赖原来的行为：store 的 `TestSaveAlertRuleClearsStatesOnIdentityChange` 把探测规则改成离线时留着探测字段，Task 2 改为同时清掉它们；`rule_test.go` 的一条合法用例留着阈值 20，Task 3 把它换成完整的 `offline()`。

10. **`days_before` 不是规则身份。** 与 `threshold`、`for_minutes` 同类：改它保留状态，下一次扫描按新值判断是否恢复。种类、任务或指标变化才清状态（`identityChanged` 不变）。

11. **`Billing` 子消息与由投影生成的 `PublicBilling`（§9.4、§10）。**
    - 字段号：`Billing` 1–6（`price`、`currency`、`billing_cycle`、`expires_on`、`auto_renew`、`days_left`）；`Node.billing = 12`，`UpdateNodeRequest.billing = 7`，`PublicNode.billing = 9`（各自消息里的下一个空号）；`PublicBilling` 沿用 `Billing` 的号，`reserved 5` 与 `reserved "auto_renew"`。
    - 投影：`projectable` 接受枚举；`newProjection` 在号、类型、presence 核对的同一个 `switch` 里加一条"两侧枚举类型相同"，不同即 panic。`NewPublic` 构造 `billing` 投影，与 `facts`、`metrics` 并列，所以对不齐时 hub 起不来。
    - 投影测试的期望值从 JSON 读入（实验 11），让 proto 对不齐时测试红在构造期的 panic 上，而不是编译错误。
    - `billingProto(store.Billing, today)` 是唯一的来源：`Node.billing` 直接用它，`PublicNode.billing` 是它的投影。五项都没填时返回 nil，两端的 `billing` 都缺失，公开快照里不出现空对象。

12. **错误路径写到子消息里的字段。** `billing.price: must match …`、`billing.currency: required when billing.price is set`、`billing.auto_renew: requires billing.billing_cycle and billing.expires_on to be set`。调用方据路径就能定位请求里的字段。

13. **请求里的 `days_left` 不读。** `billingOf` 只取五项；回显由 `billingProto` 按 hub 的今天重算。与 `AlertRule.created_at` 同一做法，proto 注释写明。

14. **文案与事件值的细节（§9.2）。**
    - 到期当天写"剩 0 天"：它仍在窗口内，没有过期。
    - 一条规则对一个节点只提醒一次：从"将于…到期"走到"已于…到期"不再发第二条（实验 13）。
    - 事件的 `value` 是剩余天数，因清除到期日而恢复时为 0。
    - 恢复文案三种，按离开窗口的原因选，判断方式见设计决定 18。

15. **公开快照只读一次钟。** `now` 与每个节点的 `days_left` 出自同一时刻，调用方拿 `now` 核对 `days_left` 不会差一天；e2e 就这样核对。`clock.Fake` 两次读到同一个值，测不出读了几次，所以 `TestPublicSnapshotReadsTheClockOnce` 给公开服务一个每读一次就前进一天的钟。

16. **前端。**
    - `lib/billing.ts` 的函数接收 `BillingView`：`Billing` 与 `PublicBilling` 共有的五个字段，也接受 `undefined`（节点没有 `billing`）。它只 import `types_pb`，公开入口复用它不会带进管理服务的生成代码。
    - 节点页的草稿带一个 `billing` 子对象，随整行整体提交；节点没有 `billing` 时从空值开始。页面不重复 hub 的校验，只把币种输入转成大写：ISO 4217 代码都是大写，这一步不改变任何合法取值。到期日用 `<input type="date">`，浏览器给出的就是 `YYYY-MM-DD`。
    - 公开节点页的静态信息卡：有主机信息、费用或到期任一项就显示；三者都没有时不画这张卡。

17. **e2e 的 `wait_alert` 改为带节点参数。** 离线告警等 node2，到期告警等 node1；同一个函数、同一个预算口径。

18. **"日期没变"靠 `alert_state.fired_expires_on` 判断。**
    - 含义：到期规则进入 firing 时节点的到期日。恢复时与节点当前的到期日比：相同写"已不在提醒窗口内"，不同写"到期日已更新为"新日期，清空写"已清除到期日"。
    - 比的是触发时的日期，不是上一次扫描时的：firing 期间改过日期（仍在窗口内，不发事件）再调小提前天数，写"到期日已更新为"。它对照的是管理员收到的那条通知，而 spec 只在"日期没变"时用第三种。
    - 存在状态行里，随 `RecordTransition` 与状态同事务写入，由 `Load` 读回：告警事件按保留期删除（实验 12），hub 重启不能丢。
    - 必须加列，不加列的两种做法都不成立：
      - `alert_state` 现有的列只有 `rule_id`、`node_id`、`state`、`since_at`，没有可以借用的值字段。
      - 从事件表读回触发事件（它的 `value` 是触发时的剩余天数、`at` 是触发时刻）：事件按 `--retention-alert-events` 清理，默认 90 天、下限 24 小时（spec §6.5），而一个过期节点可以一直处在 firing，触发事件被清掉之后就判断不了；由 `value` 与 `at` 反推日期还要假定 hub 时区没换过。
    - 每次状态写整行给出这一列：触发时是当时的到期日，恢复与 `SetAlertState` 写空串。留在 firing 时 `apply` 不写库，日期保持触发那一刻的值。
    - 不按"这次扫描由谁触发"来判断（`SaveRule` 触发的恢复就算提前天数调小）：那次扫描失败、或在保存与扫描之间重启，下一次由别的时机扫到的恢复就会写错；同时并发的 `UpdateNode` 改了日期也会被算成提前天数。
    - 今天往回挪（hub 换了 `--timezone`、墙钟被往回调）也可能让日期没变的节点离开窗口，同样写"已不在提醒窗口内"，这句照样成立。
    - 引擎只经触发转换进入 firing 并总是记下日期。`fired_expires_on` 为空的 firing 只可能来自绕过引擎写的库，当前日期非空、与它不等，按到期日改过写。
    - 旧库迁上来的状态行（离线与探测规则）取空串，这一列对它们没有意义。

19. **续期写回没有落定的节点，本轮不评估。** 两种情形都跳过它、保留已有状态，与读不懂的到期日同一条路径（`nodeExpiry.valid` 为假）。
    - `RenewExpiry` 返回 false：快照之后这一行变了。可能是计费被改过，改它的 `UpdateNode` 提交后自己会再扫描一次、按新值收敛；也可能是节点已被删除（`api.DeleteNode` 在 `nodeMu` 下提交删除，之后才经 `alerts.Forget` 取 `writeMu`），没有要评估的对象。按过期的快照评估，只会多发一对触发与恢复。
    - `RenewExpiry` 返回错误：本轮没有推后之后的日期。按未推后的快照评估，开着自动续期的节点会先收到"已过期"，续期成功的下一轮再收到"到期日已更新"，这一对通知都是假的。错误照常随扫描返回，下一轮重试续期。

20. **e2e 的端口可覆盖，默认不变。** `scripts/e2e.sh` 读 `E2E_HUB_PORT`、`E2E_HOOK_PORT`，默认 18080/18081；webhook 接收器的端口作为参数传给 python。与 `install-accept.sh` 的 `HUB_PORT`、`DIST_PORT` 同形。控制端保证同一时刻只有 e2e 任务用这两个默认端口；写计划时的实跑覆盖成 18193/18194（执行约束要求本地实验避开 18079–18092）。

21. **日界循环在扫描之前读钟。** `RunExpirySweep` 每轮先读钟，算出这次读数之后的第一个日界 `next`，再扫描，扫描之后按当时的墙钟定到 `next` 的时长。
    - 下一次触发时刻由扫描之前的读数算出，所以扫描期间跨过的日界至多引起一次立即的重扫（时长为负），不会被跳过。
    - 重扫若只是重复评估同一天，续期已经写回，`apply` 在状态没变时直接返回，不写库。
    - 时长按扫描之后的墙钟算，不按扫描之前的读数：后者会让触发比日界晚一整轮扫描的时间。
    - 先扫描再读钟会在扫描跨过零点时定到再下一个日界，丢掉一整天（实验 15）。
    - 两种跳过各由一例钉住：扫描之后才读钟由"零点前读 1 次"那例接住，负时长被改成等再下一个日界由"零点前读 2 次"那例接住。

## 文件结构

**proto 与生成**
- `proto/probe/v1/types.proto`：`BillingCycle`、`Billing`。
- `proto/probe/v1/admin.proto`：`Node.billing`、`UpdateNodeRequest.billing`；`ALERT_KIND_EXPIRY`；`AlertRule.days_before`；相关注释。
- `proto/probe/v1/public.proto`：`PublicNode.billing` 与 `PublicBilling`。
- 生成物（`make gen`）：`gen/probe/v1/{types,admin,public}.pb.go`、`gen/probe/v1/probev1connect/admin.connect.go`、`web/src/gen/probe/v1/{types,admin,public}_pb.ts`。

**投影**（`internal/hub/api/`）
- `projection.go`：枚举字段可投影，两侧枚举类型必须相同。
- `public.go`：`NewPublic` 构造 `billing` 投影。
- `projection_test.go`、`public_test.go`。

**store**（`internal/hub/store/`）
- `schema.go`、`migrations.go`、`store.go`：v9。
- `node.go`：`BillingCycle`、`Billing`、`NodeEdit`；`UpdateNode` 新签名；`RenewExpiry`。
- `alert.go`：`KindExpiry`、`AlertRule.DaysBefore`；`CheckKindFields` 与 `KindFieldError`，`SaveAlertRule` 对非法组合报错；`StateRow.FiredExpiresOn`，`RecordTransition` 多一个 `firedExpiresOn` 参数。
- `billing_test.go`（新）。
- `RecordTransition` 的测试调用点补空串：十个测试文件（`store` 3、`alert` 4、`api` 2、`cmd/hub` 1）。`alert_state_test.go` 里换种类的用例改成合法组合。

**alert**（`internal/hub/alert/`）
- `expiry.go`（新）：日期工具、自动续期、到期扫描与日界循环。
- `state.go`：`ExpiryObservation`、`NextExpiry`。
- `rule.go`：`CheckRule` 的种类专用字段检查。
- `engine.go`：`Config.Location`；`stateEntry.firedExpiresOn` 与 `entry`，`Load`、`States`、`apply` 带上触发日期；`SaveRule` 之后评估到期规则。
- `expiry_test.go`（新）；`engine_test.go`、`rule_test.go` 的夹具与用例。

**api**（`internal/hub/api/`）
- `nodes.go`：`billingCycles`、`billingOf`、`billingProto`、`nodeProto(n, today)`；`UpdateNode` 的校验与扫描。
- `service.go`：`Config.Location`、`today()`。
- `public.go`：`PublicConfig.Location`；`GetSnapshot` 经投影填 `billing`。
- `alerts.go`：到期种类与 `days_before`。
- `billing_test.go`（新）；`api_test.go`（`newZonedHarness`）、`alert_scope_reload_test.go`。

**cmd/hub**
- `serve.go`：三个 `Location`、`RunExpirySweep` 循环、`--timezone` 的帮助。
- `serve_alert_test.go`、`mux_test.go`。

**前端**（`web/src/`）
- `lib/billing.ts`（新）及其测试。
- `pages/Nodes.tsx`、`styles.css`：计费列与编辑。
- `lib/alerts.ts`、`pages/AlertRules.tsx`：到期种类。
- `public/Overview.tsx`、`public/NodePage.tsx`：费用与到期两行；`public/importScan.test.ts` 的冒烟清单。

**其他**
- `scripts/e2e.sh`、`README.md`、`proto/SKILL.md`（入口卡片）。

## 开工

- [ ] 确认工作树并跑基线

工作树 `/Users/xjetry/work/vibe/probe-billing` 已由控制端建好，本计划不新建工作树。先确认它在 `billing` 分支上、含 af62cf9（本计划依据的 spec 版本），再跑基线。

```bash
cd /Users/xjetry/work/vibe/probe-billing && git rev-parse --abbrev-ref HEAD > /tmp/billing-setup-branch.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && git merge-base --is-ancestor af62cf9 HEAD > /tmp/billing-setup-ancestor.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && git log --oneline -1 > /tmp/billing-setup-head.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && git log --oneline HEAD..main > /tmp/billing-setup-behind.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && git diff --stat HEAD main -- . ':(exclude)docs' > /tmp/billing-setup-behind-code.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && make ci > /tmp/billing-setup-ci.log 2>&1; echo $?
```

Expected：
- 六条都是 0。第二条退出 1 说明工作树早于 af62cf9，先找控制端。
- `billing-setup-branch.log` 为 `billing`；`billing-setup-head.log` 为 `af62cf9 docs: 到期状态随 alert_state 带触发时的到期日；评估时机为四处；"日期没变"的比较基准与成因`。
- `billing-setup-behind.log` 列出 main 上领先本分支的提交，写计划时为空。`billing-setup-behind-code.log` 必须为空：main 若已前移，领先的提交只能改 `docs/`，计划里的原文才与工作树一致；不为空先找控制端。
- 写计划时在 af62cf9 的副本上跑同一条 `make ci`：退出 0，vitest `Tests  369 passed (369)`。

这份基线日志是之后判定"既有 flake"的对照：同一条命令在基线上不红，才谈得上既有。

---

### Task 1: proto：计费子消息 Billing、公开的 PublicBilling 与到期告警种类；投影支持枚举

**Files:**
- Modify: `proto/probe/v1/types.proto`（`BillingCycle` 与 `Billing`）
- Modify: `proto/probe/v1/admin.proto`（`UpdateNode` 的注释；`Node.billing = 12`；`UpdateNodeRequest.billing = 7`；`ALERT_KIND_EXPIRY`；`AlertRule.days_before`；`AlertEvent.value` 的注释）
- Modify: `proto/probe/v1/public.proto`（`PublicNode.billing = 9`；`PublicBilling`，`reserved 5` 与 `reserved "auto_renew"`）
- Modify: `internal/hub/api/projection.go`（枚举字段可投影；两侧枚举类型必须相同）
- Modify: `internal/hub/api/public.go`（`Public.billing` 投影，由 `NewPublic` 构造）
- Generated（`make gen`）：
  - Go：`gen/probe/v1/types.pb.go`、`admin.pb.go`、`public.pb.go`、`probev1connect/admin.connect.go`（只有 `UpdateNode` 的注释变了）
  - TS：`web/src/gen/probe/v1/types_pb.ts`、`admin_pb.ts`、`public_pb.ts`
- Test: `internal/hub/api/projection_test.go`、`internal/hub/api/public_test.go`

**Interfaces:**
- Produces（proto）：
  - `enum BillingCycle`：`UNSPECIFIED = 0`、`MONTHLY = 1`、`QUARTERLY = 2`、`SEMIANNUAL = 3`、`YEARLY = 4`、`BIENNIAL = 5`、`TRIENNIAL = 6`。
  - `message Billing`：`string price = 1`、`string currency = 2`、`BillingCycle billing_cycle = 3`、`string expires_on = 4`、`bool auto_renew = 5`、`optional int32 days_left = 6`。
  - `Node.billing = 12`、`UpdateNodeRequest.billing = 7`，类型都是 `Billing`。
  - `PublicNode.billing = 9`，类型 `PublicBilling`：与 `Billing` 同号的 `price`、`currency`、`billing_cycle`、`expires_on`、`days_left`；`reserved 5; reserved "auto_renew";`。
  - `AlertKind.ALERT_KIND_EXPIRY = 3`；`AlertRule.uint32 days_before = 13`。
- Produces（生成）：Go 的 `probev1.BillingCycle`、`probev1.Billing`（`DaysLeft *int32`）、`probev1.PublicBilling`、`AlertRule.DaysBefore uint32`；TS 的 `BillingCycle`、`Billing`（`types_pb`）、`PublicBilling`（`public_pb`）、`daysLeft?: number | undefined`、`daysBefore: number`。
- Produces（`internal/hub/api`）：`newProjection` 接受枚举字段；`Public` 多一个 `billing projection`，Task 4 的 `GetSnapshot` 用它。

- [ ] **Step 1: 写失败测试**

`projection_test.go` 给投影补枚举字段的一正一反：
- 测试夹具加枚举 `Shade` 与消息 `OtherEnumField`、`Painted`、`PaintedPublic`、`PaintedUnreserved`。
- 同一枚举的字段照常投影（`EnumField`、`PaintedPublic`）；两侧枚举不同时 panic 并点名两个枚举；源里有未公开、也未 reserve 的枚举字段时 panic。原来"枚举不可投影"那条用例删掉。
- `TestPublicBillingProjectsFromBilling` 从 `Billing` 投影出 `PublicBilling`：五项照抄、自动续期不出现，`days_left` 的有无随源。期望值从 JSON 读入、`days_left` 经反射取（实验 11）。

`public_test.go` 的字段允许列表：`PublicNode` 加 `billing`，新增 `PublicBilling` 一项。

`internal/hub/api/projection_test.go` 原文（1/8）：

```go
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
```

替换为：

```go
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
```

`internal/hub/api/projection_test.go` 原文（2/8）：

```go
)

// projectionFixtures 造几组消息：Src 是 int32 a = 1，Pair 是 a = 1、b = 2；其余各在一处与它们不对齐，
// 或（PairPublic、MsgField、EnumField 之外）按 newProjection 的某一条约束写错。
func projectionFixtures(t *testing.T) protoreflect.FileDescriptor {
	t.Helper()
```

替换为：

```go
)

// projectionFixtures 造几组消息：Src 是 int32 a = 1，Pair 是 a = 1、b = 2，Painted 是 a = 1 与枚举 c = 2；其余各在一处
// 与它们不对齐（OtherEnumField 是与 EnumField 不对齐），或（PairPublic、PaintedPublic、MsgField、EnumField 之外）按 newProjection
// 的某一条约束写错。
func projectionFixtures(t *testing.T) protoreflect.FileDescriptor {
	t.Helper()
```

`internal/hub/api/projection_test.go` 原文（3/8）：

```go
	}
	a := func() *descriptorpb.FieldDescriptorProto { return field("a", 1, i32, single) }
	// proto3 optional 由一个合成 oneof 承载 presence。
	optional := msg("WithPresence", a())
```

替换为：

```go
	}
	a := func() *descriptorpb.FieldDescriptorProto { return field("a", 1, i32, single) }
	colorC := field("c", 2, descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(), single)
	colorC.TypeName = proto.String(".projfix.Color")
	// proto3 optional 由一个合成 oneof 承载 presence。
	optional := msg("WithPresence", a())
```

`internal/hub/api/projection_test.go` 原文（4/8）：

```go
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name: proto.String("projection_fixtures.proto"), Package: proto.String("projfix"), Syntax: proto.String("proto3"),
		EnumType: []*descriptorpb.EnumDescriptorProto{{Name: proto.String("Color"), Value: []*descriptorpb.EnumValueDescriptorProto{{Name: proto.String("COLOR_UNSPECIFIED"), Number: proto.Int32(0)}}}},
		MessageType: []*descriptorpb.DescriptorProto{
			msg("Src", a()),
```

替换为：

```go
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name: proto.String("projection_fixtures.proto"), Package: proto.String("projfix"), Syntax: proto.String("proto3"),
		EnumType: []*descriptorpb.EnumDescriptorProto{
			{Name: proto.String("Color"), Value: []*descriptorpb.EnumValueDescriptorProto{{Name: proto.String("COLOR_UNSPECIFIED"), Number: proto.Int32(0)}}},
			{Name: proto.String("Shade"), Value: []*descriptorpb.EnumValueDescriptorProto{{Name: proto.String("SHADE_UNSPECIFIED"), Number: proto.Int32(0)}}},
		},
		MessageType: []*descriptorpb.DescriptorProto{
			msg("Src", a()),
```

`internal/hub/api/projection_test.go` 原文（5/8）：

```go
			msg("MsgField", typed("a", descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), ".projfix.Src")),
			msg("EnumField", typed("a", descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(), ".projfix.Color")),
			msg("Pair", a(), field("b", 2, i32, single)),
			reserve(msg("PairPublic", a()), [][2]int32{{2, 2}}, "b"),
```

替换为：

```go
			msg("MsgField", typed("a", descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), ".projfix.Src")),
			msg("EnumField", typed("a", descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(), ".projfix.Color")),
			msg("OtherEnumField", typed("a", descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(), ".projfix.Shade")),
			msg("Painted", a(), colorC),
			reserve(msg("PaintedPublic", a()), [][2]int32{{2, 2}}, "c"),
			msg("PaintedUnreserved", a()),
			msg("Pair", a(), field("b", 2, i32, single)),
			reserve(msg("PairPublic", a()), [][2]int32{{2, 2}}, "b"),
```

`internal/hub/api/projection_test.go` 原文（6/8）：

```go
		{"Src", "Repeated", "only singular scalar fields"},
		{"MsgField", "MsgField", "only singular scalar fields"},
		{"EnumField", "EnumField", "only singular scalar fields"},
		{"PairUnreserved", "Pair", fmt.Sprintf(hideB, "PairUnreserved")},
		{"PairNumberOnly", "Pair", fmt.Sprintf(hideB, "PairNumberOnly")},
```

替换为：

```go
		{"Src", "Repeated", "only singular scalar fields"},
		{"MsgField", "MsgField", "only singular scalar fields"},
		{"OtherEnumField", "EnumField", "projfix.OtherEnumField.a uses enum projfix.Shade but projfix.EnumField.a uses enum projfix.Color"},
		{"PaintedUnreserved", "Painted", `projfix.Painted.c is not public, so projfix.PaintedUnreserved must reserve both its number 2 and its name "c"`},
		{"PairUnreserved", "Pair", fmt.Sprintf(hideB, "PairUnreserved")},
		{"PairNumberOnly", "Pair", fmt.Sprintf(hideB, "PairNumberOnly")},
```

`internal/hub/api/projection_test.go` 原文（7/8）：

```go
	newProjection(dynamicpb.NewMessageType(desc("Src")), desc("Src"))
	newProjection(dynamicpb.NewMessageType(desc("PairPublic")), desc("Pair"))
}

```

替换为：

```go
	newProjection(dynamicpb.NewMessageType(desc("Src")), desc("Src"))
	newProjection(dynamicpb.NewMessageType(desc("PairPublic")), desc("Pair"))
	newProjection(dynamicpb.NewMessageType(desc("EnumField")), desc("EnumField"))
	newProjection(dynamicpb.NewMessageType(desc("PaintedPublic")), desc("Painted"))
}

```

`internal/hub/api/projection_test.go` 原文（8/8）：

```go
	}
}
```

替换为：

```go
	}
}

// PublicBilling 由 Billing 投影：周期按编号原样复制，自动续期不出现，days_left 的缺失与 0 各自保留。期望值从 JSON 读入、
// days_left 经反射取：两个消息对不齐时本测试照常编译，红在构造投影的 panic 上。
func TestPublicBillingProjectsFromBilling(t *testing.T) {
	p := newProjection((&probev1.PublicBilling{}).ProtoReflect().Type(), (&probev1.Billing{}).ProtoReflect().Descriptor())
	got := p.apply(&probev1.Billing{Price: "12.50", Currency: "USD", BillingCycle: probev1.BillingCycle_BILLING_CYCLE_YEARLY,
		ExpiresOn: "2026-10-01", AutoRenew: true, DaysLeft: proto.Int32(0)})
	want := &probev1.PublicBilling{}
	if err := protojson.Unmarshal([]byte(`{"price": "12.50", "currency": "USD", "billingCycle": "BILLING_CYCLE_YEARLY", "expiresOn": "2026-10-01", "daysLeft": 0}`), want); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(got, want) {
		t.Fatalf("projected = %v, want %v", got, want)
	}
	noDate := p.apply(&probev1.Billing{Price: "5", Currency: "EUR"}).ProtoReflect()
	if noDate.Has(noDate.Descriptor().Fields().ByName("days_left")) {
		t.Fatalf("no expiry date projected days_left: %v", noDate.Interface())
	}
}
```

`internal/hub/api/public_test.go` 原文：

```go
	"probe.v1.PublicSite":     {"title", "theme", "accent_color", "logo", "custom_css"},
	"probe.v1.PublicSnapshot": {"now", "report_interval_ms", "nodes"},
	"probe.v1.PublicNode":     {"id", "name", "online", "last_seen_at", "sort_order", "facts", "metrics", "traffic"},
	"probe.v1.PublicFacts":    {"os", "arch", "virtualization", "cpu_model", "cpu_cores"},
	"probe.v1.PublicMetrics": {"cpu_pct", "load1", "load5", "load15", "mem_total", "mem_used", "swap_total", "swap_used",
		"disk_total", "disk_used", "net_rx_total", "net_tx_total", "net_rx_bps", "net_tx_bps", "tcp_conns", "udp_conns", "procs", "uptime_s"},
```

替换为：

```go
	"probe.v1.PublicSite":     {"title", "theme", "accent_color", "logo", "custom_css"},
	"probe.v1.PublicSnapshot": {"now", "report_interval_ms", "nodes"},
	"probe.v1.PublicNode":     {"id", "name", "online", "last_seen_at", "sort_order", "facts", "metrics", "traffic", "billing"},
	"probe.v1.PublicFacts":    {"os", "arch", "virtualization", "cpu_model", "cpu_cores"},
	"probe.v1.PublicBilling":  {"price", "currency", "billing_cycle", "expires_on", "days_left"},
	"probe.v1.PublicMetrics": {"cpu_pct", "load1", "load5", "load15", "mem_total", "mem_used", "swap_total", "swap_used",
		"disk_total", "disk_used", "net_rx_total", "net_tx_total", "net_rx_bps", "net_tx_bps", "tcp_conns", "udp_conns", "procs", "uptime_s"},
```

- [ ] **Step 2: 跑红**

```bash
cd /Users/xjetry/work/vibe/probe-billing && go test -count=1 -run 'TestNewProjection|TestPublicBillingProjectsFromBilling|TestPublicResponsesExposeOnlyAllowlistedFields' ./internal/hub/api/ > /tmp/billing-t1-red.log 2>&1; echo $?
```

Expected：1，包编译失败：生成代码里还没有这些类型。原文节选：
- `internal/hub/api/projection_test.go:130:31: undefined: probev1.PublicBilling`
- `internal/hub/api/projection_test.go:130:81: undefined: probev1.Billing`
- `internal/hub/api/projection_test.go:131:89: undefined: probev1.BillingCycle_BILLING_CYCLE_YEARLY`

- [ ] **Step 3: 实现**

三个 proto 文件与投影两处的改动如下。proto 改完跑 `make gen`，`public.go` 用到的 `probev1.PublicBilling` 由它生成。

`proto/probe/v1/types.proto` 原文：

```proto
  uint32 reset_day = 7;
}
```

替换为：

```proto
  uint32 reset_day = 7;
}

// 节点的计费周期（§9.4）。管理与公开两端共用，所以与 Billing 一起定义在这里：public.proto 不能 import admin.proto。
// 未指定表示没有周期（一次性付费或未填）；自动续期要求非未指定。
enum BillingCycle {
  BILLING_CYCLE_UNSPECIFIED = 0;
  // 1 个月。
  BILLING_CYCLE_MONTHLY = 1;
  // 3 个月。
  BILLING_CYCLE_QUARTERLY = 2;
  // 6 个月。
  BILLING_CYCLE_SEMIANNUAL = 3;
  // 12 个月。
  BILLING_CYCLE_YEARLY = 4;
  // 24 个月。
  BILLING_CYCLE_BIENNIAL = 5;
  // 36 个月。
  BILLING_CYCLE_TRIENNIAL = 6;
}

// 节点的计费与到期（§9.4）。价格与币种是提醒用的展示值：hub 不汇总、不换算，也不拿它们做任何计算；到期日与周期
// 驱动 days_left、自动续期与到期规则。Node 与 UpdateNodeRequest 都以它承载；公开端的 PublicBilling 由它按字段名投影生成，两者对不齐时 hub 构造公开服务就 panic。
message Billing {
  // 价格，十进制文本（如 12.50）；空表示未填。保存时须为空或匹配 ^[0-9]{1,9}(\.[0-9]{1,2})?$。
  string price = 1;
  // ISO 4217 币种代码（三个大写字母）；价格非空时必填，币种可以单独填。
  string currency = 2;
  BillingCycle billing_cycle = 3;
  // 到期日 YYYY-MM-DD；空表示没有到期日。保存时须为空或存在的日期。
  string expires_on = 4;
  // 开着时，到期日早于今天（hub 时区）即按周期推后到不早于今天。hub 启动、每个日界（零点不存在的日子取新一天的
  // 第一个时刻）、计费字段变化与保存启用的到期规则时检查。保存时要求 billing_cycle 与 expires_on 都非空。
  bool auto_renew = 5;
  // 到期日减去今天的天数，今天按 hub 的 --timezone 取日历日；负数是已过期的天数。没有到期日、或库里的到期日无法解析时缺失。只由 hub 填写：
  // 保存请求里的值忽略，与 AlertRule.created_at 同一做法。
  optional int32 days_left = 6;
}
```

`proto/probe/v1/admin.proto` 原文（1/8）：

```proto
    option (probe.v1.access) = ACCESS_SESSION;
  }
  // 整体替换可编辑字段（名称、是否公开、备注、周期重置日、离线宽限期）。
  rpc UpdateNode(UpdateNodeRequest) returns (UpdateNodeResponse) {
    option (probe.v1.access) = ACCESS_SESSION;
```

替换为：

```proto
    option (probe.v1.access) = ACCESS_SESSION;
  }
  // 整体替换可编辑字段（名称、是否公开、备注、周期重置日、离线宽限期、计费与到期）。计费字段有变化时，
  // 返回之前按新值做一次到期扫描（自动续期推后、到期规则评估），响应里的到期日与 days_left 是扫描之后的值。
  rpc UpdateNode(UpdateNodeRequest) returns (UpdateNodeResponse) {
    option (probe.v1.access) = ACCESS_SESSION;
```

`proto/probe/v1/admin.proto` 原文（2/8）：

```proto
  // PROBE_OFFLINE_AFTER 调高后已存的更小值仍会回显，引擎按下限取值，下次编辑须改成不小于下限的值。
  optional uint32 offline_grace_s = 11;
}

```

替换为：

```proto
  // PROBE_OFFLINE_AFTER 调高后已存的更小值仍会回显，引擎按下限取值，下次编辑须改成不小于下限的值。
  optional uint32 offline_grace_s = 11;
  // 计费与到期（§9.4）；五项都没填时缺失。days_left 由 hub 按 --timezone 的今天算出。
  Billing billing = 12;
}

```

`proto/probe/v1/admin.proto` 原文（3/8）：

```proto
  // 必填，缺失拒绝；0 表示清除（取 PROBE_OFFLINE_AFTER），非 0 须 ≥ PROBE_OFFLINE_AFTER 的秒数。
  optional uint32 offline_grace_s = 6;
}
message UpdateNodeResponse {
```

替换为：

```proto
  // 必填，缺失拒绝；0 表示清除（取 PROBE_OFFLINE_AFTER），非 0 须 ≥ PROBE_OFFLINE_AFTER 的秒数。
  optional uint32 offline_grace_s = 6;
  // 计费与到期，整体替换：缺失等于五项全清，空串、未指定与 false 也是清除，没有"不改"的取值。
  // 取值约束见 Billing 各字段；days_left 由 hub 计算，这里的值忽略。
  Billing billing = 7;
}
message UpdateNodeResponse {
```

`proto/probe/v1/admin.proto` 原文（4/8）：

```proto
  ALERT_KIND_OFFLINE = 1;
  ALERT_KIND_PROBE = 2;
}
enum ProbeMetric {
```

替换为：

```proto
  ALERT_KIND_OFFLINE = 1;
  ALERT_KIND_PROBE = 2;
  // 节点到期日距今不超过 days_before 天（含已过期）即触发，数据源是节点的到期日。
  ALERT_KIND_EXPIRY = 3;
}
enum ProbeMetric {
```

`proto/probe/v1/admin.proto` 原文（5/8）：

```proto
  // 1–64 个字符。
  string name = 2;
  // 必须为离线或探测，未指定值不允许保存。
  AlertKind kind = 3;
  bool enabled = 4;
```

替换为：

```proto
  // 1–64 个字符。
  string name = 2;
  // 必须为离线、探测或到期，未指定值不允许保存。
  AlertKind kind = 3;
  bool enabled = 4;
```

`proto/probe/v1/admin.proto` 原文（6/8）：

```proto
  // 已保存渠道的 id，升序去重；空表示只记事件不投递。
  repeated int64 channel_ids = 6;
  // 以下字段仅用于探测规则，task_id 必须是已存在的非零任务 id。
  uint64 task_id = 7;
  // 探测规则必须选择丢包百分比或往返毫秒数。
```

替换为：

```proto
  // 已保存渠道的 id，升序去重；空表示只记事件不投递。
  repeated int64 channel_ids = 6;
  // 以下四个字段仅用于探测规则，task_id 必须是已存在的非零任务 id；离线与到期规则四项都必须为零值。
  uint64 task_id = 7;
  // 探测规则必须选择丢包百分比或往返毫秒数。
```

`proto/probe/v1/admin.proto` 原文（7/8）：

```proto
  // 为假时 node_ids 是显式作用域，删除节点后空集不等于全部。
  bool all_nodes = 12;
}
message ListAlertRulesRequest {}
```

替换为：

```proto
  // 为假时 node_ids 是显式作用域，删除节点后空集不等于全部。
  bool all_nodes = 12;
  // 仅用于到期规则：1–365，节点到期日减今天（hub 时区）不超过它即触发，已过期的也算；其余种类必须为 0。
  // 保存启用的到期规则后立即评估一次，此后在 hub 启动、每个日界（零点不存在的日子取新一天的第一个时刻）与节点
  // 计费字段变化时评估。
  uint32 days_before = 13;
}
message ListAlertRulesRequest {}
```

`proto/probe/v1/admin.proto` 原文（8/8）：

```proto
  int64 at = 5;
  string summary = 6;
  // 触发或恢复时的观测值：离线为未上报秒数；探测同规则 threshold 的单位（丢包百分比 loss_pct 或往返毫秒 rtt_ms）。
  double value = 7;
  repeated AlertDelivery deliveries = 8;
```

替换为：

```proto
  int64 at = 5;
  string summary = 6;
  // 触发或恢复时的观测值：离线为未上报秒数；探测同规则 threshold 的单位（丢包百分比 loss_pct 或往返毫秒 rtt_ms）；
  // 到期为剩余天数（负数是已过期天数），因清除到期日而恢复时为 0。
  double value = 7;
  repeated AlertDelivery deliveries = 8;
```

`proto/probe/v1/public.proto` 原文：

```proto
  // hub 侧累计的流量；每个节点都有，从未上报的节点为零用量。
  Traffic traffic = 8;
}

```

替换为：

```proto
  // hub 侧累计的流量；每个节点都有，从未上报的节点为零用量。
  Traffic traffic = 8;
  // 计费与到期的公开部分（§9.4）；五项都没填时缺失。
  PublicBilling billing = 9;
}

// Billing 的公开部分，字段号与 Billing 相同，由投影按字段名生成（与 PublicFacts 同一机制）。自动续期是运维开关，
// 不公开，号与名保留：要公开必须先删掉 reserved，而不是随手加一个字段。
message PublicBilling {
  reserved 5;
  reserved "auto_renew";
  // 价格，十进制文本（如 12.50）；空表示未填。
  string price = 1;
  // ISO 4217 币种代码；价格非空时必有。
  string currency = 2;
  BillingCycle billing_cycle = 3;
  // 到期日 YYYY-MM-DD；空表示没有到期日。
  string expires_on = 4;
  // 到期日减去今天的天数，今天按 hub 的 --timezone 取日历日；负数是已过期的天数。没有到期日、或库里的到期日无法解析时缺失。
  optional int32 days_left = 6;
}

```

`internal/hub/api/projection.go` 原文（1/4）：

```go
//
// newProjection 是唯一把公开消息与源消息配对的地方，spec §10 对这对消息的约束都在这里核对，任一不符即 panic：
//   - 每个公开字段在源里有同名字段，号、类型、基数与 presence 都相同；
//   - 源里没公开的字段，号与名都在公开消息的 reserved 里；每个 reserved 的号与名都对应一个没公开的源字段。
//
```

替换为：

```go
//
// newProjection 是唯一把公开消息与源消息配对的地方，spec §10 对这对消息的约束都在这里核对，任一不符即 panic：
//   - 每个公开字段在源里有同名字段，号、类型、基数与 presence 都相同，枚举字段两侧引用同一个枚举类型；
//   - 源里没公开的字段，号与名都在公开消息的 reserved 里；每个 reserved 的号与名都对应一个没公开的源字段。
//
```

`internal/hub/api/projection.go` 原文（2/4）：

```go
			panic(fmt.Sprintf("%s (%v, presence %v) does not match %s (%v, presence %v)",
				d.FullName(), d.Kind(), d.HasPresence(), s.FullName(), s.Kind(), s.HasPresence()))
		}
		p.fields = append(p.fields, [2]protoreflect.FieldDescriptor{d, s})
```

替换为：

```go
			panic(fmt.Sprintf("%s (%v, presence %v) does not match %s (%v, presence %v)",
				d.FullName(), d.Kind(), d.HasPresence(), s.FullName(), s.Kind(), s.HasPresence()))
		case d.Kind() == protoreflect.EnumKind && d.Enum().FullName() != s.Enum().FullName():
			panic(fmt.Sprintf("%s uses enum %s but %s uses enum %s; enums are copied by number, so both sides must use the same enum",
				d.FullName(), d.Enum().FullName(), s.FullName(), s.Enum().FullName()))
		}
		p.fields = append(p.fields, [2]protoreflect.FieldDescriptor{d, s})
```

`internal/hub/api/projection.go` 原文（3/4）：

```go
}

// projectable 限于单值标量：消息、枚举、列表与 map 的逐项语义各不相同，公开消息目前只需要标量。
func projectable(f protoreflect.FieldDescriptor) bool {
	if f.Cardinality() == protoreflect.Repeated {
```

替换为：

```go
}

// projectable 限于单值标量与枚举：消息、列表与 map 的逐项语义各不相同，公开消息目前不需要。枚举按编号复制，编号的
// 含义由枚举类型决定，所以 newProjection 另要求两侧引用同一个枚举类型。
func projectable(f protoreflect.FieldDescriptor) bool {
	if f.Cardinality() == protoreflect.Repeated {
```

`internal/hub/api/projection.go` 原文（4/4）：

```go
	}
	switch f.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind, protoreflect.EnumKind:
		return false
	}
```

替换为：

```go
	}
	switch f.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return false
	}
```

`internal/hub/api/public.go` 原文（1/2）：

```go
	facts   projection
	metrics projection

	// limit 按来源计数，覆盖挂载点收到的每个请求。
```

替换为：

```go
	facts   projection
	metrics projection
	billing projection

	// limit 按来源计数，覆盖挂载点收到的每个请求。
```

`internal/hub/api/public.go` 原文（2/2）：

```go
		facts:   newProjection((&probev1.PublicFacts{}).ProtoReflect().Type(), (&probev1.Facts{}).ProtoReflect().Descriptor()),
		metrics: newProjection((&probev1.PublicMetrics{}).ProtoReflect().Type(), (&probev1.Metrics{}).ProtoReflect().Descriptor()),
		limit:   ratelimit.New[netip.Addr](publicBurst, publicRefill),
		maxAge:  cachePolicy(probeServices()),
```

替换为：

```go
		facts:   newProjection((&probev1.PublicFacts{}).ProtoReflect().Type(), (&probev1.Facts{}).ProtoReflect().Descriptor()),
		metrics: newProjection((&probev1.PublicMetrics{}).ProtoReflect().Type(), (&probev1.Metrics{}).ProtoReflect().Descriptor()),
		billing: newProjection((&probev1.PublicBilling{}).ProtoReflect().Type(), (&probev1.Billing{}).ProtoReflect().Descriptor()),
		limit:   ratelimit.New[netip.Addr](publicBurst, publicRefill),
		maxAge:  cachePolicy(probeServices()),
```

生成：

```bash
cd /Users/xjetry/work/vibe/probe-billing && make gen > /tmp/billing-t1-gen.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && git status --short > /tmp/billing-t1-gen-status.log 2>&1; echo $?
```

Expected：两条都是 0。`billing-t1-gen-status.log` 恰好列出三个 proto、`projection.go`、`public.go`、两个测试文件与下列七个生成物（都是 ` M`）：`gen/probe/v1/admin.pb.go`、`gen/probe/v1/probev1connect/admin.connect.go`、`gen/probe/v1/public.pb.go`、`gen/probe/v1/types.pb.go`、`web/src/gen/probe/v1/admin_pb.ts`、`web/src/gen/probe/v1/public_pb.ts`、`web/src/gen/probe/v1/types_pb.ts`。

- [ ] **Step 4: 跑绿**

```bash
cd /Users/xjetry/work/vibe/probe-billing && go test -count=1 -run 'TestNewProjection|TestPublicBillingProjectsFromBilling|TestPublicResponsesExposeOnlyAllowlistedFields' ./internal/hub/api/ > /tmp/billing-t1-green-t1.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && buf lint > /tmp/billing-t1-lint.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && buf breaking --against ".git#ref=$(git merge-base main HEAD)" > /tmp/billing-t1-breaking.log 2>&1; echo $?
```

Expected：三条都是 0。`buf breaking` 在基点上比对（实验 8）：加消息、字段与枚举值不破坏兼容。

- [ ] **Step 5: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-billing && git add internal/hub/api/projection.go internal/hub/api/projection_test.go internal/hub/api/public.go internal/hub/api/public_test.go proto/probe/v1/admin.proto proto/probe/v1/public.proto proto/probe/v1/types.proto gen/probe/v1/admin.pb.go gen/probe/v1/probev1connect/admin.connect.go gen/probe/v1/public.pb.go gen/probe/v1/types.pb.go web/src/gen/probe/v1/admin_pb.ts web/src/gen/probe/v1/public_pb.ts web/src/gen/probe/v1/types_pb.ts > /tmp/billing-t1-add.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && git commit -m "proto: 计费子消息 Billing、公开的 PublicBilling 与到期告警种类；投影支持枚举" -m "计费五项与 days_left 收在 types.proto 的 Billing 里，Node 与 UpdateNodeRequest 都以它承载：公开与管理两端都用它与 BillingCycle，而 public.proto 不能 import admin.proto。PublicBilling 由投影从 Billing 生成，自动续期连名带号 reserved；公开服务构造时就核对两者对齐，对不齐 hub 起不来。投影扩展到枚举字段：枚举按编号复制，编号的含义取决于枚举类型，所以两侧必须引用同一个枚举，否则构造期 panic。days_left 用 optional：没有到期日时缺失，与剩 0 天区分；保存请求里的值由 hub 忽略。" > /tmp/billing-t1-commit.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && git status --porcelain > /tmp/billing-t1-status.log 2>&1; echo $?
```

Expected：三条都是 0；`billing-t1-status.log` 为空。

- [ ] **Step 6: make ci**

```bash
cd /Users/xjetry/work/vibe/probe-billing && make ci > /tmp/billing-t1-ci.log 2>&1; echo $?
```

Expected：0；vitest `Tests  369 passed (369)`。`make ci` 最后核对生成物已入库，所以放在提交之后。

- [ ] **Step 7: 缺陷注入**

| | 改动 | 命令 | 期望 |
|---|---|---|---|
| a | `public.proto` 里 `PublicBilling` 的 `days_left` 改用字段号 7 | `go test -count=1 -run 'TestNewProjection\|TestPublicBillingProjectsFromBilling\|TestPublicResponsesExposeOnlyAllowlistedFields' ./internal/hub/api/ > /tmp/billing-t1-inj-a.log 2>&1; echo $?` | 1，`TestPublicBillingProjectsFromBilling` 在构造投影时 panic：`probe.v1.PublicBilling.days_left is field 7 but probe.v1.Billing.days_left is field 6; public fields keep the source numbers` |
| b | `public.proto` 的 `PublicBilling` 删掉 `reserved 5;` 一行（名字仍保留） | `go test -count=1 -run 'TestNewProjection\|TestPublicBillingProjectsFromBilling\|TestPublicResponsesExposeOnlyAllowlistedFields' ./internal/hub/api/ > /tmp/billing-t1-inj-b.log 2>&1; echo $?` | 1，同一测试 panic：`probe.v1.Billing.auto_renew is not public, so probe.v1.PublicBilling must reserve both its number 5 and its name "auto_renew"` |
| c | `PublicBilling` 的 `string price = 1;` 改成 `optional string price = 1;` | `go test -count=1 -run 'TestNewProjection\|TestPublicBillingProjectsFromBilling\|TestPublicResponsesExposeOnlyAllowlistedFields' ./internal/hub/api/ > /tmp/billing-t1-inj-c.log 2>&1; echo $?` | 1，同一测试 panic：`probe.v1.PublicBilling.price (string, presence true) does not match probe.v1.Billing.price (string, presence false)` |
| d | `PublicBilling` 的 `BillingCycle billing_cycle = 3;` 改成 `ProbeKind billing_cycle = 3;` | `go test -count=1 -run 'TestNewProjection\|TestPublicBillingProjectsFromBilling\|TestPublicResponsesExposeOnlyAllowlistedFields' ./internal/hub/api/ > /tmp/billing-t1-inj-d.log 2>&1; echo $?` | 1，同一测试 panic：`probe.v1.PublicBilling.billing_cycle uses enum probe.v1.ProbeKind but probe.v1.Billing.billing_cycle uses enum probe.v1.BillingCycle; enums are copied by number, so both sides must use the same enum` |
| e | `public_test.go` 的允许列表里 `PublicBilling` 漏掉 `days_left` | `go test -count=1 -run 'TestNewProjection\|TestPublicBillingProjectsFromBilling\|TestPublicResponsesExposeOnlyAllowlistedFields' ./internal/hub/api/ > /tmp/billing-t1-inj-e.log 2>&1; echo $?` | 1，`public_test.go:270: fields of probe.v1.PublicBilling = [billing_cycle currency days_left expires_on price], allowlist has [billing_cycle currency expires_on price]` |
| f | `projectable` 仍把枚举排除在外（`case` 里加回 `protoreflect.EnumKind`） | `go test -count=1 -run 'TestNewProjection\|TestPublicBillingProjectsFromBilling\|TestPublicResponsesExposeOnlyAllowlistedFields' ./internal/hub/api/ > /tmp/billing-t1-inj-f.log 2>&1; echo $?` | 1，`TestNewProjectionRejectsMisalignedFields/OtherEnumField<-EnumField`：`panic "projfix.OtherEnumField.a: only singular scalar fields can be projected" does not mention "projfix.OtherEnumField.a uses enum projfix.Shade but projfix.EnumField.a uses enum projfix.Color"`；同一枚举的正向用例也 panic |
| g | `newProjection` 删掉枚举类型一致的那条检查 | `go test -count=1 -run 'TestNewProjection\|TestPublicBillingProjectsFromBilling\|TestPublicResponsesExposeOnlyAllowlistedFields' ./internal/hub/api/ > /tmp/billing-t1-inj-g.log 2>&1; echo $?` | 1，同一子测试：`no panic; want one mentioning "projfix.OtherEnumField.a uses enum projfix.Shade but projfix.EnumField.a uses enum projfix.Color"` |

a–d 改的是 proto：改动 → `git diff --stat` 非空 → `make gen > /tmp/billing-t1-inj-gen.log 2>&1; echo $?`（0）→ 跑命令 → 核对原因 → `git checkout -- proto gen web/src/gen`。e–g 改的是测试或投影代码：改动 → `git diff --stat` 非空 → 跑命令 → 核对 → `git checkout -- internal/hub/api`。

---

### Task 2: store：计费五列、到期规则的提前天数与到期状态的触发日期（schema v9）

**Files:**
- Modify: `internal/hub/store/schema.go`（`ddlNode` 表尾五列；`ddlAlertRule` 表尾 `days_before`；`ddlAlertState` 的 `fired_expires_on`）
- Modify: `internal/hub/store/migrations.go`（迁移 9 与冻结的 `migrationV9`）
- Modify: `internal/hub/store/store.go`（`schemaVersion = 9`）
- Modify: `internal/hub/store/node.go`（`BillingCycle`、`Billing`、`NodeEdit`；`Node.Billing`；`selectNodes`、`scanNodes`；`UpdateNode` 新签名；`RenewExpiry`）
- Modify: `internal/hub/store/alert.go`（`KindExpiry`、`AlertRule.DaysBefore`；`KindFieldError`、`CheckKindFields`，`SaveAlertRule` 对非法组合报错；`ListAlertRules`、`SaveAlertRule` 读写 `days_before`；`StateRow.FiredExpiresOn`；`ListAlertStates`、`setAlertState`、`SetAlertState`、`RecordTransition` 读写 `fired_expires_on`）
- Modify（调用点随签名改）：
  - `UpdateNode` 改用 `NodeEdit`：`internal/hub/api/nodes.go`、`internal/hub/store/store_test.go`（四处）、`internal/hub/store/alert_test.go`、`internal/hub/alert/engine_test.go`（`grace`）
  - `RecordTransition` 多一个参数：`internal/hub/alert/engine.go`（传空串，Task 3 换成真实的值），以及十个测试文件 `internal/hub/store/{alert_test,alert_prune_test,maintenance_safety_test}.go`、`internal/hub/alert/{queue_test,queue_prune_test,queue_refill_test,queue_storage_test}.go`、`internal/hub/api/{alerts_test,delivery_failure_test}.go`、`cmd/hub/serve_alert_test.go`
  - `internal/hub/store/alert_test.go` 里一处 `StateRow` 的无键字面量改成带键
  - `internal/hub/store/alert_state_test.go`：`TestSaveAlertRuleClearsStatesOnIdentityChange` 把探测规则换成离线时同时清掉探测字段（带着它们是非法组合）
- Create: `internal/hub/store/billing_test.go`

**Interfaces:**
- Produces（`internal/hub/store`）：
  - `type BillingCycle string`；常量 `CycleNone`（空串）、`CycleMonthly`、`CycleQuarterly`、`CycleSemiannual`、`CycleYearly`、`CycleBiennial`、`CycleTriennial`
  - `func BillingCycles() []BillingCycle`（全部非空周期，按月数升序）
  - `type Billing struct { Price, Currency string; Cycle BillingCycle; ExpiresOn string; AutoRenew bool }`
  - `type NodeEdit struct { Name string; Public bool; Note string; TrafficResetDay int; OfflineGraceS int; Billing Billing }`
  - `Node.Billing Billing`
  - `func (s *Store) UpdateNode(ctx context.Context, id int64, e NodeEdit) (billingChanged bool, err error)`，替换原来的 `UpdateNode(ctx, id, name, public, note, resetDay, offlineGraceS) error`
  - `func (s *Store) RenewExpiry(ctx context.Context, id int64, cycle BillingCycle, from, to string) (bool, error)`
  - `const KindExpiry AlertKind = "expiry"`；`AlertRule.DaysBefore int`
  - `StateRow.FiredExpiresOn string`
  - `type KindFieldError struct { Field, Constraint string }`；`func CheckKindFields(r AlertRule) error`：探测四项只属于探测规则、`days_before` 只属于到期规则，别的种类上必须为零值。`SaveAlertRule` 先调它，非法组合原样返回这个错误，不写入
  - `func (s *Store) RecordTransition(ctx context.Context, ruleID, nodeID int64, state AlertState, firedExpiresOn string, ev AlertEvent, channelIDs []int64) (AlertEvent, error)`：`firedExpiresOn` 随状态写入。`SetAlertState` 签名不变，写空串。
- 迁移：`schemaVersion = 9`；`migrations[9] = execAll(migrationV9)`（七条 `ALTER`）。

- [ ] **Step 1: 写失败测试**

新测试文件 `billing_test.go` 钉住六件事：
- 从 v8 迁来的库与新建库逐列一致；旧的节点、规则与状态行取默认值，迁移后能写新列（`schemaV8` 是 v7 加 `setting` 表的完整 DDL）。
- 计费五项随 `UpdateNode` 整体替换；`billingChanged` 只看五项与库内原值是否不同，别的字段变不算；只改币种、只改周期都算。
- `RenewExpiry` 只在到期日、周期与自动续期都仍是推后依据的取值时写入。
- `days_before` 只随到期规则落库，改它保留状态；换成离线（同时清掉 `days_before`，组合合法）时列变回 NULL、状态清除。
- 种类与专用字段的非法组合被 `SaveAlertRule` 拒绝，新建与修改都不写入，报错写明字段（`TestSaveAlertRuleRejectsFieldsOfOtherKinds`）。
- `fired_expires_on` 由每一次状态写给出：触发时写进去，恢复与 `SetAlertState` 都把它清空；重新读状态拿到的是库里的值。

既有调用点随签名改为 `NodeEdit`，`RecordTransition` 的调用补上空串；`alert_state_test.go` 换种类的用例改成合法组合。

新建 `internal/hub/store/billing_test.go`（整份）：

```go
package store

import (
	"database/sql"
	"errors"
	"math"
	"reflect"
	"slices"
	"testing"
	"time"
)

// v8 的完整 DDL：v7 加上 setting 表。
var schemaV8 = append(slices.Clone(schemaV7), "CREATE TABLE setting (\n  key TEXT PRIMARY KEY,\n  value TEXT NOT NULL\n)")

// 旧库里已有的节点、规则与状态升级后取列默认值：没有计费信息，规则没有提前天数，状态没有触发时的到期日；
// 升级后的库能照常写入新列。
func TestMigrationFromV8MatchesFreshSchemaAndKeepsRows(t *testing.T) {
	migrated, fresh := migrateFrom(t, schemaV8, 8, func(t *testing.T, db *sql.DB) {
		seedMinuteRow(t, db)
		for _, stmt := range []string{
			"INSERT INTO alert_rule (id, name, kind, enabled, all_nodes, created_at) VALUES (3, 'r', 'offline', 1, 1, 1)",
			"INSERT INTO alert_state (rule_id, node_id, state, since_at) VALUES (3, 7, 'firing', 1)",
		} {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatal(err)
			}
		}
	})
	if got, want := describe(t, migrated.r), describe(t, fresh.r); !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated schema differs from fresh schema:\n got: %+v\nwant: %+v", got, want)
	}
	if v := userVersion(t, migrated.r); v != schemaVersion {
		t.Fatalf("user_version = %d, want %d", v, schemaVersion)
	}
	n, err := migrated.GetNode(t.Context(), 7)
	if err != nil || n.Name != "kept" || n.Billing != (Billing{}) {
		t.Fatalf("node after migration: %+v %v", n, err)
	}
	rules, err := migrated.ListAlertRules(t.Context())
	if err != nil || len(rules) != 1 || rules[0].ID != 3 || rules[0].DaysBefore != 0 {
		t.Fatalf("rules after migration: %+v %v", rules, err)
	}
	wantStates := []StateRow{{RuleID: 3, NodeID: 7, State: StateFiring, SinceAt: time.Unix(1, 0).UTC()}}
	if states, err := migrated.ListAlertStates(t.Context()); err != nil || !reflect.DeepEqual(states, wantStates) {
		t.Fatalf("states after migration: %+v %v", states, err)
	}
	b := Billing{Price: "5", Currency: "EUR", Cycle: CycleYearly, ExpiresOn: "2027-01-01", AutoRenew: true}
	if _, err := migrated.UpdateNode(t.Context(), 7, NodeEdit{Name: "kept", TrafficResetDay: 1, Billing: b}); err != nil {
		t.Fatal(err)
	}
	if n, err := migrated.GetNode(t.Context(), 7); err != nil || n.Billing != b {
		t.Fatalf("billing on a migrated database: %+v %v", n, err)
	}
}

// 计费五项随 UpdateNode 整体替换：零值即清除。billingChanged 只看这五项与库内原值是否不同，别的字段变不算。
func TestUpdateNodeReplacesBillingAndReportsChange(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	id, _ := s.CreateNode(ctx, "n", hash(1))
	edit := NodeEdit{Name: "n", TrafficResetDay: 1}
	update := func(e NodeEdit) bool {
		t.Helper()
		changed, err := s.UpdateNode(ctx, id, e)
		if err != nil {
			t.Fatal(err)
		}
		return changed
	}
	if update(edit) {
		t.Fatal("empty billing over empty billing reported a change")
	}
	full := Billing{Price: "12.50", Currency: "USD", Cycle: CycleMonthly, ExpiresOn: "2026-10-01", AutoRenew: true}
	edit.Billing = full
	if !update(edit) {
		t.Fatal("setting billing reported no change")
	}
	for _, read := range []func() ([]Node, error){
		func() ([]Node, error) { n, err := s.GetNode(ctx, id); return []Node{n}, err },
		func() ([]Node, error) { return s.ListNodes(ctx) },
	} {
		nodes, err := read()
		if err != nil || len(nodes) != 1 || nodes[0].Billing != full {
			t.Fatalf("read back %+v %v, want %+v", nodes, err, full)
		}
	}
	edit.Name, edit.Public = "renamed", true
	if update(edit) {
		t.Fatal("changing only name and public reported a billing change")
	}
	if nodes, err := s.ListPublicNodes(ctx); err != nil || len(nodes) != 1 || nodes[0].Billing != full {
		t.Fatalf("public nodes %+v %v", nodes, err)
	}
	edit.Billing.AutoRenew = false
	if !update(edit) {
		t.Fatal("turning off auto renew reported no change")
	}
	edit.Billing.Currency = "EUR"
	if !update(edit) {
		t.Fatal("changing only the currency reported no change")
	}
	edit.Billing.Cycle = CycleYearly
	if !update(edit) {
		t.Fatal("changing only the cycle reported no change")
	}
	edit.Billing.ExpiresOn = "2026-11-01"
	if !update(edit) {
		t.Fatal("changing only the expiry date reported no change")
	}
	edit.Billing.Price = "13.00"
	if !update(edit) {
		t.Fatal("changing only the price reported no change")
	}
	edit.Billing = Billing{}
	if !update(edit) {
		t.Fatal("clearing billing reported no change")
	}
	if n, err := s.GetNode(ctx, id); err != nil || n.Billing != (Billing{}) {
		t.Fatalf("cleared billing read back %+v %v", n.Billing, err)
	}
	if changed, err := s.UpdateNode(ctx, 999, edit); !errors.Is(err, ErrNotFound) || changed {
		t.Fatalf("unknown id: %v %v, want ErrNotFound and no change", changed, err)
	}
}

// RenewExpiry 只在该行仍是推后所依据的取值时写入：到期日、周期或自动续期任一已被改过都不写。
func TestRenewExpiryWritesOnlyOverTheValuesItWasComputedFrom(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	id, _ := s.CreateNode(ctx, "n", hash(1))
	set := func(b Billing) {
		t.Helper()
		if _, err := s.UpdateNode(ctx, id, NodeEdit{Name: "n", TrafficResetDay: 1, Billing: b}); err != nil {
			t.Fatal(err)
		}
	}
	expiresOn := func() string {
		t.Helper()
		n, err := s.GetNode(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return n.Billing.ExpiresOn
	}
	monthly := Billing{Cycle: CycleMonthly, ExpiresOn: "2026-01-31", AutoRenew: true}
	for _, c := range []struct {
		name string
		row  Billing
		from string
	}{
		{"stale date", monthly, "2026-01-30"},
		{"cycle changed", Billing{Cycle: CycleYearly, ExpiresOn: "2026-01-31", AutoRenew: true}, "2026-01-31"},
		{"auto renew off", Billing{Cycle: CycleMonthly, ExpiresOn: "2026-01-31"}, "2026-01-31"},
	} {
		set(c.row)
		renewed, err := s.RenewExpiry(ctx, id, CycleMonthly, c.from, "2026-02-28")
		if err != nil || renewed || expiresOn() != "2026-01-31" {
			t.Fatalf("%s: renewed=%v err=%v expires_on=%s", c.name, renewed, err, expiresOn())
		}
	}
	set(monthly)
	renewed, err := s.RenewExpiry(ctx, id, CycleMonthly, "2026-01-31", "2026-02-28")
	if err != nil || !renewed || expiresOn() != "2026-02-28" {
		t.Fatalf("renew: renewed=%v err=%v expires_on=%s", renewed, err, expiresOn())
	}
	if renewed, err := s.RenewExpiry(ctx, 999, CycleMonthly, "2026-01-31", "2026-02-28"); err != nil || renewed {
		t.Fatalf("unknown node: renewed=%v err=%v", renewed, err)
	}
}

// days_before 只随到期规则落库；改它不改规则身份，状态保留；换成别的种类（同时清掉 days_before，组合合法）时，
// 列变回 NULL，状态随身份一起清除。
func TestAlertRuleDaysBeforeBelongsToExpiryRules(t *testing.T) {
	s, ids, _, _ := alertFixture(t)
	ctx := t.Context()
	r := saveRule(t, s, AlertRule{Name: "到期", Kind: KindExpiry, Enabled: true, AllNodes: true, DaysBefore: 7})
	column := func() sql.NullInt64 {
		t.Helper()
		var v sql.NullInt64
		if err := s.r.QueryRow("SELECT days_before FROM alert_rule WHERE id = ?", r.ID).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if got := column(); r.DaysBefore != 7 || got != (sql.NullInt64{Int64: 7, Valid: true}) {
		t.Fatalf("saved %d, column %v", r.DaysBefore, got)
	}
	if rules, err := s.ListAlertRules(ctx); err != nil || len(rules) != 1 || rules[0].DaysBefore != 7 || rules[0].Kind != KindExpiry {
		t.Fatalf("listed %+v %v", rules, err)
	}
	if err := s.SetAlertState(ctx, r.ID, ids[0], StateFiring, s.clk.Now()); err != nil {
		t.Fatal(err)
	}
	r.DaysBefore = 30
	r = saveRule(t, s, r)
	assertAlertRows(t, s, "alert_state", "rule_id = 1", 1)
	r.Kind, r.DaysBefore = KindOffline, 0
	r = saveRule(t, s, r)
	if got := column(); got.Valid {
		t.Fatalf("offline rule kept the days_before column: %v", got)
	}
	assertAlertRows(t, s, "alert_state", "rule_id = 1", 0)
}

// 种类与专用字段的非法组合在存储层就被拒绝，新建与修改都不写入，已有的行保持原样；报错写明字段与约束。
func TestSaveAlertRuleRejectsFieldsOfOtherKinds(t *testing.T) {
	s, _, _, task := alertFixture(t)
	existing := saveRule(t, s, AlertRule{Name: "离线", Kind: KindOffline, Enabled: true, AllNodes: true})
	for _, c := range []struct {
		name  string
		rule  AlertRule
		field string
	}{
		{"offline with task", AlertRule{Kind: KindOffline, TaskID: task}, "task_id"},
		{"offline with metric", AlertRule{Kind: KindOffline, Metric: MetricLossPct}, "metric"},
		{"offline with NaN threshold", AlertRule{Kind: KindOffline, Threshold: math.NaN()}, "threshold"},
		{"offline with for_minutes", AlertRule{Kind: KindOffline, ForMinutes: 3}, "for_minutes"},
		{"offline with days_before", AlertRule{Kind: KindOffline, DaysBefore: 7}, "days_before"},
		{"expiry with threshold", AlertRule{Kind: KindExpiry, DaysBefore: 7, Threshold: 5}, "threshold"},
		{"probe with days_before", AlertRule{Kind: KindProbe, TaskID: task, Metric: MetricLossPct, Threshold: 5, ForMinutes: 3, DaysBefore: 7}, "days_before"},
	} {
		for _, id := range []int64{0, existing.ID} {
			r := c.rule
			r.ID, r.Name, r.Enabled, r.AllNodes = id, "非法", true, true
			var kf KindFieldError
			if _, err := s.SaveAlertRule(t.Context(), r); !errors.As(err, &kf) || kf.Field != c.field {
				t.Fatalf("%s (id %d): err = %v, want KindFieldError on %s", c.name, id, err, c.field)
			}
		}
	}
	rules, err := s.ListAlertRules(t.Context())
	if err != nil || len(rules) != 1 || rules[0].Name != "离线" || rules[0].Kind != KindOffline {
		t.Fatalf("rules after rejected saves: %+v %v", rules, err)
	}
}

// 触发时的到期日只由写入它的那一次给出：每次写都整行替换，恢复与不带事件的写都把它清空，状态行不会带着上一次
// 触发的日期进入下一个状态。它存在库里，重新读状态（hub 重启走的就是这条路）拿到的是同一个值。
func TestAlertStateFiredExpiresOnFollowsEachWrite(t *testing.T) {
	s, ids, _, _ := alertFixture(t)
	ctx := t.Context()
	r := saveRule(t, s, AlertRule{Name: "到期", Kind: KindExpiry, Enabled: true, AllNodes: true, DaysBefore: 7})
	fired := func() string {
		t.Helper()
		states, err := s.ListAlertStates(ctx)
		if err != nil || len(states) != 1 {
			t.Fatalf("states = %+v %v", states, err)
		}
		return states[0].FiredExpiresOn
	}
	record := func(state AlertState, firedExpiresOn string, tr Transition) {
		t.Helper()
		if _, err := s.RecordTransition(ctx, r.ID, ids[0], state, firedExpiresOn, AlertEvent{Transition: tr, At: s.clk.Now()}, nil); err != nil {
			t.Fatal(err)
		}
	}
	record(StateFiring, "2026-10-01", TransitionFiring)
	if got := fired(); got != "2026-10-01" {
		t.Fatalf("after firing: fired_expires_on = %q, want 2026-10-01", got)
	}
	record(StateOK, "", TransitionRecovered)
	if got := fired(); got != "" {
		t.Fatalf("after recovery: fired_expires_on = %q, want empty", got)
	}
	record(StateFiring, "2026-10-01", TransitionFiring)
	if err := s.SetAlertState(ctx, r.ID, ids[0], StateOK, s.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if got := fired(); got != "" {
		t.Fatalf("after SetAlertState: fired_expires_on = %q, want empty", got)
	}
}
```

`internal/hub/store/store_test.go` 原文（1/4）：

```go
	ctx := context.Background()
	id, _ := s.CreateNode(ctx, "old", hash(1))
	if err := s.UpdateNode(ctx, id, "new", true, "note", 1, 0); err != nil {
		t.Fatal(err)
	}
```

替换为：

```go
	ctx := context.Background()
	id, _ := s.CreateNode(ctx, "old", hash(1))
	if _, err := s.UpdateNode(ctx, id, NodeEdit{Name: "new", Public: true, Note: "note", TrafficResetDay: 1}); err != nil {
		t.Fatal(err)
	}
```

`internal/hub/store/store_test.go` 原文（2/4）：

```go
		t.Fatalf("GetNode = %+v, %v", n, err)
	}
	if err := s.UpdateNode(ctx, 999, "x", false, "", 1, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v, want ErrNotFound", err)
	}
```

替换为：

```go
		t.Fatalf("GetNode = %+v, %v", n, err)
	}
	if _, err := s.UpdateNode(ctx, 999, NodeEdit{Name: "x", TrafficResetDay: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v, want ErrNotFound", err)
	}
```

`internal/hub/store/store_test.go` 原文（3/4）：

```go
		t.Fatalf("default reset day = %d, want 1", n.TrafficResetDay)
	}
	if err := s.UpdateNode(ctx, id, "n", false, "", 15, 0); err != nil {
		t.Fatal(err)
	}
```

替换为：

```go
		t.Fatalf("default reset day = %d, want 1", n.TrafficResetDay)
	}
	if _, err := s.UpdateNode(ctx, id, NodeEdit{Name: "n", TrafficResetDay: 15}); err != nil {
		t.Fatal(err)
	}
```

`internal/hub/store/store_test.go` 原文（4/4）：

```go
	}
	for _, i := range []int{0, 2} {
		if err := s.UpdateNode(ctx, ids[i], []string{"a", "b", "c"}[i], true, "", 1, 0); err != nil {
			t.Fatal(err)
		}
```

替换为：

```go
	}
	for _, i := range []int{0, 2} {
		if _, err := s.UpdateNode(ctx, ids[i], NodeEdit{Name: []string{"a", "b", "c"}[i], Public: true, TrafficResetDay: 1}); err != nil {
			t.Fatal(err)
		}
```

`internal/hub/store/alert_test.go` 原文（1/5）：

```go
func recordEvent(t *testing.T, s *Store, rule, node int64, channels []int64) AlertEvent {
	t.Helper()
	ev, err := s.RecordTransition(t.Context(), rule, node, StateFiring, AlertEvent{
		Transition: TransitionFiring, At: s.clk.Now(), Summary: "offline", Value: 42,
	}, channels)
```

替换为：

```go
func recordEvent(t *testing.T, s *Store, rule, node int64, channels []int64) AlertEvent {
	t.Helper()
	ev, err := s.RecordTransition(t.Context(), rule, node, StateFiring, "", AlertEvent{
		Transition: TransitionFiring, At: s.clk.Now(), Summary: "offline", Value: 42,
	}, channels)
```

`internal/hub/store/alert_test.go` 原文（2/5）：

```go
		t.Fatal(err)
	}
	_, err := s.RecordTransition(t.Context(), r.ID, ids[0], StateFiring, AlertEvent{At: s.clk.Now()}, []int64{cs[0].ID, cs[1].ID})
	if err == nil {
		t.Fatal("delivery failure was accepted")
```

替换为：

```go
		t.Fatal(err)
	}
	_, err := s.RecordTransition(t.Context(), r.ID, ids[0], StateFiring, "", AlertEvent{At: s.clk.Now()}, []int64{cs[0].ID, cs[1].ID})
	if err == nil {
		t.Fatal("delivery failure was accepted")
```

`internal/hub/store/alert_test.go` 原文（3/5）：

```go
	s, ids, _, _ := alertFixture(t)
	for _, grace := range []int{90, 0} {
		if err := s.UpdateNode(t.Context(), ids[0], "n", false, "", 1, grace); err != nil {
			t.Fatal(err)
		}
```

替换为：

```go
	s, ids, _, _ := alertFixture(t)
	for _, grace := range []int{90, 0} {
		if _, err := s.UpdateNode(t.Context(), ids[0], NodeEdit{Name: "n", TrafficResetDay: 1, OfflineGraceS: grace}); err != nil {
			t.Fatal(err)
		}
```

`internal/hub/store/alert_test.go` 原文（4/5）：

```go
	a.NodeIDs = []int64{ids[1], ids[1]}
	a = saveRule(t, s, a)
	want := []StateRow{{a.ID, ids[1], StatePending, s.clk.Now().Add(time.Second)}, {b.ID, ids[0], StatePending, s.clk.Now().Add(time.Second)}, {b.ID, ids[1], StatePending, s.clk.Now().Add(time.Second)}}
	got, err := s.ListAlertStates(t.Context())
	if err != nil || !reflect.DeepEqual(got, want) {
```

替换为：

```go
	a.NodeIDs = []int64{ids[1], ids[1]}
	a = saveRule(t, s, a)
	pending := func(rule, node int64) StateRow {
		return StateRow{RuleID: rule, NodeID: node, State: StatePending, SinceAt: s.clk.Now().Add(time.Second)}
	}
	want := []StateRow{pending(a.ID, ids[1]), pending(b.ID, ids[0]), pending(b.ID, ids[1])}
	got, err := s.ListAlertStates(t.Context())
	if err != nil || !reflect.DeepEqual(got, want) {
```

`internal/hub/store/alert_test.go` 原文（5/5）：

```go
			}
			t.Run("transition", func(t *testing.T) {
				_, err := s.RecordTransition(t.Context(), r.ID, ids[0], StateFiring, AlertEvent{At: s.clk.Now()}, []int64{cs[0].ID, cs[1].ID})
				assertAlertNotFound(t, err, missing, id)
			})
```

替换为：

```go
			}
			t.Run("transition", func(t *testing.T) {
				_, err := s.RecordTransition(t.Context(), r.ID, ids[0], StateFiring, "", AlertEvent{At: s.clk.Now()}, []int64{cs[0].ID, cs[1].ID})
				assertAlertNotFound(t, err, missing, id)
			})
```

`internal/hub/store/alert_state_test.go` 原文：

```go
			switch change {
			case "kind":
				r.Kind = KindOffline
			case "task":
				p, _, err := s.SaveProbeTask(t.Context(), taskForTest(), nil)
```

替换为：

```go
			switch change {
			case "kind":
				// 换成离线要同时清掉探测字段：带着它们的离线规则是非法组合，存储层拒绝。
				r = AlertRule{ID: r.ID, Name: r.Name, Kind: KindOffline, AllNodes: true, Enabled: true}
			case "task":
				p, _, err := s.SaveProbeTask(t.Context(), taskForTest(), nil)
```

`internal/hub/store/alert_prune_test.go` 原文：

```go
	var events []AlertEvent
	for _, at := range []time.Time{before.Add(-24 * time.Hour), before, before.Add(24 * time.Hour)} {
		ev, err := s.RecordTransition(t.Context(), r.ID, ids[0], StateFiring, AlertEvent{At: at, Transition: TransitionFiring}, []int64{cs[0].ID, cs[1].ID})
		if err != nil {
			t.Fatal(err)
```

替换为：

```go
	var events []AlertEvent
	for _, at := range []time.Time{before.Add(-24 * time.Hour), before, before.Add(24 * time.Hour)} {
		ev, err := s.RecordTransition(t.Context(), r.ID, ids[0], StateFiring, "", AlertEvent{At: at, Transition: TransitionFiring}, []int64{cs[0].ID, cs[1].ID})
		if err != nil {
			t.Fatal(err)
```

`internal/hub/store/maintenance_safety_test.go` 原文：

```go
	}
	r := saveRule(t, s, AlertRule{Name: "offline", Kind: KindOffline, AllNodes: true})
	if _, err := s.RecordTransition(ctx, r.ID, id, StateFiring, AlertEvent{At: clk.Now().Add(-91 * 24 * time.Hour), Transition: TransitionFiring}, nil); err != nil {
		t.Fatal(err)
	}
```

替换为：

```go
	}
	r := saveRule(t, s, AlertRule{Name: "offline", Kind: KindOffline, AllNodes: true})
	if _, err := s.RecordTransition(ctx, r.ID, id, StateFiring, "", AlertEvent{At: clk.Now().Add(-91 * 24 * time.Hour), Transition: TransitionFiring}, nil); err != nil {
		t.Fatal(err)
	}
```

`internal/hub/alert/engine_test.go` 原文：

```go
func (f *fixture) grace(t *testing.T, id int64, seconds int) {
	t.Helper()
	must(t, f.st.UpdateNode(t.Context(), id, fmt.Sprintf("node%d", id), false, "", 1, seconds))
}
func (f *fixture) sweep(t *testing.T) { t.Helper(); must(t, f.e.SweepOffline(t.Context())) }
```

替换为：

```go
func (f *fixture) grace(t *testing.T, id int64, seconds int) {
	t.Helper()
	_, err := f.st.UpdateNode(t.Context(), id, store.NodeEdit{Name: fmt.Sprintf("node%d", id), TrafficResetDay: 1, OfflineGraceS: seconds})
	must(t, err)
}
func (f *fixture) sweep(t *testing.T) { t.Helper(); must(t, f.e.SweepOffline(t.Context())) }
```

`internal/hub/alert/queue_test.go` 原文：

```go
		ids = append(ids, c.ID)
	}
	ev, err := f.st.RecordTransition(t.Context(), r.ID, f.ids[0], store.StateFiring, store.AlertEvent{At: f.clk.Now(), Summary: "persisted summary", Value: 5, Transition: store.TransitionFiring}, ids)
	must(t, err)
	return ev
```

替换为：

```go
		ids = append(ids, c.ID)
	}
	ev, err := f.st.RecordTransition(t.Context(), r.ID, f.ids[0], store.StateFiring, "", store.AlertEvent{At: f.clk.Now(), Summary: "persisted summary", Value: 5, Transition: store.TransitionFiring}, ids)
	must(t, err)
	return ev
```

`internal/hub/alert/queue_prune_test.go` 原文：

```go
			c := queueChannel(t, f, srv.URL)
			rule := f.rule(t, offline())
			old, err := f.st.RecordTransition(t.Context(), rule.ID, f.ids[0], store.StateFiring,
				store.AlertEvent{At: f.clk.Now().Add(-91 * 24 * time.Hour), Transition: store.TransitionFiring}, []int64{c.ID})
			must(t, err)
```

替换为：

```go
			c := queueChannel(t, f, srv.URL)
			rule := f.rule(t, offline())
			old, err := f.st.RecordTransition(t.Context(), rule.ID, f.ids[0], store.StateFiring, "",
				store.AlertEvent{At: f.clk.Now().Add(-91 * 24 * time.Hour), Transition: store.TransitionFiring}, []int64{c.ID})
			must(t, err)
```

`internal/hub/alert/queue_refill_test.go` 原文：

```go
			events := make([]store.AlertEvent, count)
			for i := range events {
				ev, err := f.st.RecordTransition(t.Context(), rule.ID, f.ids[0], store.StateFiring,
					store.AlertEvent{At: f.clk.Now(), Transition: store.TransitionFiring, Summary: fmt.Sprint(i)}, []int64{c.ID})
				must(t, err)
```

替换为：

```go
			events := make([]store.AlertEvent, count)
			for i := range events {
				ev, err := f.st.RecordTransition(t.Context(), rule.ID, f.ids[0], store.StateFiring, "",
					store.AlertEvent{At: f.clk.Now(), Transition: store.TransitionFiring, Summary: fmt.Sprint(i)}, []int64{c.ID})
				must(t, err)
```

`internal/hub/alert/queue_storage_test.go` 原文：

```go
	rule := f.rule(t, offline())
	for i := range QueueCap + 1 {
		_, err := f.st.RecordTransition(t.Context(), rule.ID, f.ids[0], store.StateFiring, store.AlertEvent{
			At: f.clk.Now(), Transition: store.TransitionFiring, Summary: fmt.Sprint(i),
		}, []int64{c.ID})
```

替换为：

```go
	rule := f.rule(t, offline())
	for i := range QueueCap + 1 {
		_, err := f.st.RecordTransition(t.Context(), rule.ID, f.ids[0], store.StateFiring, "", store.AlertEvent{
			At: f.clk.Now(), Transition: store.TransitionFiring, Summary: fmt.Sprint(i),
		}, []int64{c.ID})
```

`internal/hub/api/alerts_test.go` 原文：

```go
			id = n2
		}
		ev, err := h.store.RecordTransition(t.Context(), r.Id, id, store.StateFiring, store.AlertEvent{Transition: store.TransitionFiring, At: h.clk.Now(), Summary: fmt.Sprint(i), Value: float64(i)}, []int64{c.Id})
		if err != nil {
			t.Fatal(err)
```

替换为：

```go
			id = n2
		}
		ev, err := h.store.RecordTransition(t.Context(), r.Id, id, store.StateFiring, "", store.AlertEvent{Transition: store.TransitionFiring, At: h.clk.Now(), Summary: fmt.Sprint(i), Value: float64(i)}, []int64{c.Id})
		if err != nil {
			t.Fatal(err)
```

`internal/hub/api/delivery_failure_test.go` 原文（1/2）：

```go
	r := saveRule(t, h, offlineRule())
	c := saveChannel(t, h, webhook("http://127.0.0.1"))
	ev, err := h.store.RecordTransition(t.Context(), r.Id, n, store.StateFiring, store.AlertEvent{Transition: store.TransitionFiring, At: h.clk.Now(), Summary: "down"}, []int64{c.Id})
	if err != nil {
		t.Fatal(err)
```

替换为：

```go
	r := saveRule(t, h, offlineRule())
	c := saveChannel(t, h, webhook("http://127.0.0.1"))
	ev, err := h.store.RecordTransition(t.Context(), r.Id, n, store.StateFiring, "", store.AlertEvent{Transition: store.TransitionFiring, At: h.clk.Now(), Summary: "down"}, []int64{c.Id})
	if err != nil {
		t.Fatal(err)
```

`internal/hub/api/delivery_failure_test.go` 原文（2/2）：

```go
		channels[i] = c.Id
	}
	ev, err := h.store.RecordTransition(t.Context(), r.Id, n, store.StateFiring, store.AlertEvent{Transition: store.TransitionFiring, At: h.clk.Now()}, channels)
	if err != nil {
		t.Fatal(err)
```

替换为：

```go
		channels[i] = c.Id
	}
	ev, err := h.store.RecordTransition(t.Context(), r.Id, n, store.StateFiring, "", store.AlertEvent{Transition: store.TransitionFiring, At: h.clk.Now()}, channels)
	if err != nil {
		t.Fatal(err)
```

`cmd/hub/serve_alert_test.go` 原文（1/2）：

```go
			t.Fatal(err)
		}
		_, err = st.RecordTransition(t.Context(), r.ID, id, store.StateFiring, store.AlertEvent{Transition: store.TransitionFiring, At: time.Now()}, []int64{channel})
		if err != nil {
			t.Fatal(err)
```

替换为：

```go
			t.Fatal(err)
		}
		_, err = st.RecordTransition(t.Context(), r.ID, id, store.StateFiring, "", store.AlertEvent{Transition: store.TransitionFiring, At: time.Now()}, []int64{channel})
		if err != nil {
			t.Fatal(err)
```

`cmd/hub/serve_alert_test.go` 原文（2/2）：

```go
				}
				for _, age := range []time.Duration{tc.retention + 24*time.Hour, tc.retention - 24*time.Hour} {
					ev, err := st.RecordTransition(t.Context(), r.ID, id, store.StateFiring, store.AlertEvent{Transition: store.TransitionFiring, At: clk.Now().Add(-age)}, []int64{channel})
					if err != nil {
						t.Fatal(err)
```

替换为：

```go
				}
				for _, age := range []time.Duration{tc.retention + 24*time.Hour, tc.retention - 24*time.Hour} {
					ev, err := st.RecordTransition(t.Context(), r.ID, id, store.StateFiring, "", store.AlertEvent{Transition: store.TransitionFiring, At: clk.Now().Add(-age)}, []int64{channel})
					if err != nil {
						t.Fatal(err)
```

- [ ] **Step 2: 跑红**

```bash
cd /Users/xjetry/work/vibe/probe-billing && go test -count=1 ./internal/hub/store/ ./internal/hub/alert/ ./internal/hub/api/ ./cmd/hub/ > /tmp/billing-t2-red.log 2>&1; echo $?
```

Expected：1，四个包都编译失败。原文节选：
- `internal/hub/store/alert_prune_test.go:20:127: too many arguments in call to s.RecordTransition`
- `internal/hub/store/alert_test.go:348:16: assignment mismatch: 2 variables but s.UpdateNode returns 1 value`
- `internal/hub/store/billing_test.go:37:41: n.Billing undefined (type Node has no field or method Billing)`
- `internal/hub/alert/engine_test.go:73:51: undefined: store.NodeEdit`
- `internal/hub/api/delivery_failure_test.go:74:171: too many arguments in call to h.store.RecordTransition`
- `cmd/hub/serve_alert_test.go:148:148: too many arguments in call to st.RecordTransition`

- [ ] **Step 3: 实现**

`internal/hub/store/schema.go` 原文（1/3）：

```go
  created_at INTEGER NOT NULL,
  -- 墙钟，只供展示与告警文案，不参与离线时长计算。
  last_seen_at INTEGER
)`

```

替换为：

```go
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

```

`internal/hub/store/schema.go` 原文（2/3）：

```go
  threshold REAL,
  for_minutes INTEGER,
  created_at INTEGER NOT NULL
)`
const ddlAlertRuleNode = `CREATE TABLE alert_rule_node (
```

替换为：

```go
  threshold REAL,
  for_minutes INTEGER,
  created_at INTEGER NOT NULL,
  -- 非到期规则写 NULL：SaveAlertRule 只为到期规则落这一列，CheckKindFields 拒绝别的种类带非零值。到期规则的
  -- 1–365 由 alert.CheckRule 在保存与载入时裁决，存储层不查。列序与迁移 9 的 ADD COLUMN 结果一致。
  days_before INTEGER
)`
const ddlAlertRuleNode = `CREATE TABLE alert_rule_node (
```

`internal/hub/store/schema.go` 原文（3/3）：

```go
  state TEXT NOT NULL,
  since_at INTEGER NOT NULL,
  PRIMARY KEY (rule_id, node_id)
)`
```

替换为：

```go
  state TEXT NOT NULL,
  since_at INTEGER NOT NULL,
  -- 引擎只在到期规则进入 firing 时写入非空值：当时节点的到期日（见 StateRow.FiredExpiresOn）。
  -- 列序与迁移 9 的 ADD COLUMN 结果一致：ADD COLUMN 把列排在最后，与写在 PRIMARY KEY 约束之前的这一行同为第五列。
  fired_expires_on TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (rule_id, node_id)
)`
```

`internal/hub/store/migrations.go` 原文（1/2）：

```go
	7: migrateDeliveryFailure,
	8: execAll([]string{ddlSettingV8}),
}

```

替换为：

```go
	7: migrateDeliveryFailure,
	8: execAll([]string{ddlSettingV8}),
	9: execAll(migrationV9),
}

```

`internal/hub/store/migrations.go` 原文（2/2）：

```go
  value TEXT NOT NULL
)`
```

替换为：

```go
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
```

`internal/hub/store/store.go` 原文：

```go
}

const schemaVersion = 8

func migrate(db *sql.DB) error {
```

替换为：

```go
}

const schemaVersion = 9

func migrate(db *sql.DB) error {
```

`internal/hub/store/node.go` 原文（1/4）：

```go

var ErrBadOrder = errors.New("ids must list every node exactly once")

type Node struct {
```

替换为：

```go

var ErrBadOrder = errors.New("ids must list every node exactly once")

// BillingCycle 按 TEXT 落库，空串表示没有周期。与协议枚举的对应在 api 的 billingCycles 表，周期的月数在
// alert 的 cycleMonths；两处的测试都按 BillingCycles 核对一一对应。
type BillingCycle string

const (
	CycleNone       BillingCycle = ""
	CycleMonthly    BillingCycle = "monthly"
	CycleQuarterly  BillingCycle = "quarterly"
	CycleSemiannual BillingCycle = "semiannual"
	CycleYearly     BillingCycle = "yearly"
	CycleBiennial   BillingCycle = "biennial"
	CycleTriennial  BillingCycle = "triennial"
)

// BillingCycles 列出全部非空周期。
func BillingCycles() []BillingCycle {
	return []BillingCycle{CycleMonthly, CycleQuarterly, CycleSemiannual, CycleYearly, CycleBiennial, CycleTriennial}
}

// Billing 是节点的计费与到期（§9.4），随 UpdateNode 整体替换，零值即"都没填"。存储不校验取值：写入口有两个，
// api 的 UpdateNode 按 §9.4 校验整组取值，RenewExpiry 只写 alert 推后得到的日期。
type Billing struct {
	Price     string
	Currency  string
	Cycle     BillingCycle
	ExpiresOn string // YYYY-MM-DD；空表示没有到期日
	AutoRenew bool
}

// NodeEdit 是 UpdateNode 整体替换的可编辑字段；调用方已做校验与清洗。
type NodeEdit struct {
	Name            string
	Public          bool
	Note            string
	TrafficResetDay int
	OfflineGraceS   int // 0 写 NULL，读侧取 TTL
	Billing         Billing
}

type Node struct {
```

`internal/hub/store/node.go` 原文（2/4）：

```go
	Facts          *probev1.Facts
	FactsUpdatedAt time.Time
}

const selectNodes = `SELECT n.id, n.name, n.public, n.note, n.sort_order, n.created_at, n.last_seen_at, n.traffic_reset_day, n.offline_grace_s,
	f.hostname, f.os, f.kernel, f.arch, f.virtualization, f.cpu_model, f.cpu_cores, f.agent_version, f.icmp_available, f.updated_at
	FROM node n LEFT JOIN node_facts f ON f.node_id = n.id`
```

替换为：

```go
	Facts          *probev1.Facts
	FactsUpdatedAt time.Time
	Billing        Billing
}

const selectNodes = `SELECT n.id, n.name, n.public, n.note, n.sort_order, n.created_at, n.last_seen_at, n.traffic_reset_day, n.offline_grace_s,
	n.price, n.currency, n.billing_cycle, n.expires_on, n.auto_renew,
	f.hostname, f.os, f.kernel, f.arch, f.virtualization, f.cpu_model, f.cpu_cores, f.agent_version, f.icmp_available, f.updated_at
	FROM node n LEFT JOIN node_facts f ON f.node_id = n.id`
```

`internal/hub/store/node.go` 原文（3/4）：

```go
		var hostname, os, kernel, arch, virt, cpuModel, agentVersion sql.NullString
		var cores, icmp, factsUpdated sql.NullInt64
		if err := rows.Scan(&n.ID, &n.Name, &n.Public, &n.Note, &n.SortOrder, &created, &seen, &n.TrafficResetDay, &grace,
			&hostname, &os, &kernel, &arch, &virt, &cpuModel, &cores, &agentVersion, &icmp, &factsUpdated); err != nil {
			return nil, err
```

替换为：

```go
		var hostname, os, kernel, arch, virt, cpuModel, agentVersion sql.NullString
		var cores, icmp, factsUpdated sql.NullInt64
		b := &n.Billing
		if err := rows.Scan(&n.ID, &n.Name, &n.Public, &n.Note, &n.SortOrder, &created, &seen, &n.TrafficResetDay, &grace,
			&b.Price, &b.Currency, &b.Cycle, &b.ExpiresOn, &b.AutoRenew,
			&hostname, &os, &kernel, &arch, &virt, &cpuModel, &cores, &agentVersion, &icmp, &factsUpdated); err != nil {
			return nil, err
```

`internal/hub/store/node.go` 原文（4/4）：

```go
}

// UpdateNode 整体替换可编辑字段；调用方已做校验与清洗。重置日与其他字段一起整体替换，
// 不存在"不改"的取值。
func (s *Store) UpdateNode(ctx context.Context, id int64, name string, public bool, note string, resetDay int, offlineGraceS int) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec("UPDATE node SET name = ?, public = ?, note = ?, traffic_reset_day = ?, offline_grace_s = NULLIF(?, 0) WHERE id = ?", name, public, note, resetDay, offlineGraceS, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

```

替换为：

```go
}

// UpdateNode 整体替换可编辑字段，不存在"不改"的取值。billingChanged 报告计费五项与写入前的库内值是否不同，
// 调用方据它决定是否立即做一次到期扫描。库内值在同一个写事务里读出，RenewExpiry 也经单写协程，读与写之间插不进
// 一次推后，所以它就是这次写入实际覆盖掉的值。表单若带着推后之前的到期日提交，库内值已是推后的日期，两者不同，
// 调用方随即重新扫描、再推后一次。
func (s *Store) UpdateNode(ctx context.Context, id int64, e NodeEdit) (billingChanged bool, err error) {
	err = s.write(ctx, func(tx *sql.Tx) error {
		var old Billing
		err := tx.QueryRow("SELECT price, currency, billing_cycle, expires_on, auto_renew FROM node WHERE id = ?", id).
			Scan(&old.Price, &old.Currency, &old.Cycle, &old.ExpiresOn, &old.AutoRenew)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		b := e.Billing
		if _, err := tx.Exec(`UPDATE node SET name = ?, public = ?, note = ?, traffic_reset_day = ?, offline_grace_s = NULLIF(?, 0),
			price = ?, currency = ?, billing_cycle = ?, expires_on = ?, auto_renew = ? WHERE id = ?`,
			e.Name, e.Public, e.Note, e.TrafficResetDay, e.OfflineGraceS, b.Price, b.Currency, b.Cycle, b.ExpiresOn, b.AutoRenew, id); err != nil {
			return err
		}
		billingChanged = old != b
		return nil
	})
	if err != nil {
		return false, err
	}
	return billingChanged, nil
}

// RenewExpiry 把自动续期推后的到期日写回，前提是该行此刻仍是推后所依据的那组取值（开着自动续期、周期与
// 旧到期日都没变）：推后的日期由到期扫描从它读出的快照算出，快照之后 UpdateNode 若改了计费字段，按旧快照写回
// 就会盖掉管理员刚保存的值。条件不成立时不写、返回 false：计费被 UpdateNode 改过时，由那次 UpdateNode 触发的扫描
// 按新值重算；节点已被删除时，没有要重算的对象。
func (s *Store) RenewExpiry(ctx context.Context, id int64, cycle BillingCycle, from, to string) (bool, error) {
	var renewed bool
	err := s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec("UPDATE node SET expires_on = ? WHERE id = ? AND auto_renew = 1 AND billing_cycle = ? AND expires_on = ?", to, id, cycle, from)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		renewed = n == 1
		return err
	})
	return renewed, err
}

```

`internal/hub/store/alert.go` 原文（1/16）：

```go
	KindOffline AlertKind = "offline"
	KindProbe   AlertKind = "probe"
)

```

替换为：

```go
	KindOffline AlertKind = "offline"
	KindProbe   AlertKind = "probe"
	KindExpiry  AlertKind = "expiry"
)

```

`internal/hub/store/alert.go` 原文（2/16）：

```go
	Threshold  float64
	ForMinutes int
	CreatedAt  time.Time
}
```

替换为：

```go
	Threshold  float64
	ForMinutes int
	// DaysBefore 只属于到期规则。它与 Threshold、ForMinutes 一样不是规则身份：改它保留状态（见 SaveAlertRule）。
	DaysBefore int
	CreatedAt  time.Time
}
```

`internal/hub/store/alert.go` 原文（3/16）：

```go
	State          AlertState
	SinceAt        time.Time
}

```

替换为：

```go
	State          AlertState
	SinceAt        time.Time
	// FiredExpiresOn 只属于到期规则：进入 firing 时节点的到期日。恢复时拿它与节点当前的到期日比较，相等说明日期没变，
	// 恢复文案写"已不在提醒窗口内"而不是"到期日已更新"（§9.2）。这个日期随状态存，而不是从触发事件里读回：告警事件
	// 按保留期清理，一个过期节点却可以一直处在 firing。其余种类的状态、以及不经 RecordTransition 写入的状态，这一项为空。
	FiredExpiresOn string
}

```

`internal/hub/store/alert.go` 原文（4/16）：

```go
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id, name, kind, enabled, all_nodes, task_id, metric, threshold, for_minutes, created_at FROM alert_rule ORDER BY id`)
	if err != nil {
		return nil, err
```

替换为：

```go
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id, name, kind, enabled, all_nodes, task_id, metric, threshold, for_minutes, days_before, created_at FROM alert_rule ORDER BY id`)
	if err != nil {
		return nil, err
```

`internal/hub/store/alert.go` 原文（5/16）：

```go
	for rows.Next() {
		var r AlertRule
		var task, minutes sql.NullInt64
		var metric sql.NullString
		var threshold sql.NullFloat64
		var created int64
		if err := rows.Scan(&r.ID, &r.Name, &r.Kind, &r.Enabled, &r.AllNodes, &task, &metric, &threshold, &minutes, &created); err != nil {
			return nil, err
		}
		r.TaskID, r.Metric, r.Threshold, r.ForMinutes = uint64(task.Int64), ProbeMetric(metric.String), threshold.Float64, int(minutes.Int64)
		r.CreatedAt = time.Unix(created, 0).UTC()
		index[r.ID] = len(out)
```

替换为：

```go
	for rows.Next() {
		var r AlertRule
		var task, minutes, daysBefore sql.NullInt64
		var metric sql.NullString
		var threshold sql.NullFloat64
		var created int64
		if err := rows.Scan(&r.ID, &r.Name, &r.Kind, &r.Enabled, &r.AllNodes, &task, &metric, &threshold, &minutes, &daysBefore, &created); err != nil {
			return nil, err
		}
		r.TaskID, r.Metric, r.Threshold, r.ForMinutes = uint64(task.Int64), ProbeMetric(metric.String), threshold.Float64, int(minutes.Int64)
		r.DaysBefore = int(daysBefore.Int64)
		r.CreatedAt = time.Unix(created, 0).UTC()
		index[r.ID] = len(out)
```

`internal/hub/store/alert.go` 原文（6/16）：

```go

// 引用检查与保存同在单写事务，删除不能插入两者之间造成孤儿引用。
func (s *Store) SaveAlertRule(ctx context.Context, r AlertRule) (AlertRule, error) {
	r.NodeIDs, r.ChannelIDs = sortedAlertIDs(r.NodeIDs), sortedAlertIDs(r.ChannelIDs)
	if r.AllNodes {
```

替换为：

```go

// KindFieldError 是规则带着不属于它种类的专用字段。Field 是协议里的字段名，Constraint 是违反的约束。
type KindFieldError struct {
	Field, Constraint string
}

func (e KindFieldError) Error() string { return e.Field + " " + e.Constraint }

// CheckKindFields 裁决种类与专用字段的组合：探测四项（任务、指标、阈值、持续分钟）只属于探测规则，days_before
// 只属于到期规则，别的种类上必须是零值。它是这条规则唯一的实现：SaveAlertRule 对非法组合报错而不改写，
// alert.CheckRule 在保存与载入时调它，协议层经 CheckRule 得到同样的字段与约束（§9.1）。阈值用 != 0 判：NaN 与任何数
// 都不等，也被拒绝。种类本身是否合法不在这里判断。
func CheckKindFields(r AlertRule) error {
	if r.Kind != KindProbe {
		switch {
		case r.TaskID != 0:
			return KindFieldError{"task_id", "must be 0 unless kind is probe"}
		case r.Metric != "":
			return KindFieldError{"metric", "must be unspecified unless kind is probe"}
		case r.Threshold != 0:
			return KindFieldError{"threshold", "must be 0 unless kind is probe"}
		case r.ForMinutes != 0:
			return KindFieldError{"for_minutes", "must be 0 unless kind is probe"}
		}
	}
	if r.Kind != KindExpiry && r.DaysBefore != 0 {
		return KindFieldError{"days_before", "must be 0 unless kind is expiry"}
	}
	return nil
}

// 引用检查与保存同在单写事务，删除不能插入两者之间造成孤儿引用。
func (s *Store) SaveAlertRule(ctx context.Context, r AlertRule) (AlertRule, error) {
	// 非法组合报错而不是清零：静默清零是放宽方向，调用方发了什么、存下的却是零值，无从察觉（§9.1）。
	if err := CheckKindFields(r); err != nil {
		return AlertRule{}, err
	}
	r.NodeIDs, r.ChannelIDs = sortedAlertIDs(r.NodeIDs), sortedAlertIDs(r.ChannelIDs)
	if r.AllNodes {
```

`internal/hub/store/alert.go` 原文（7/16）：

```go
			}
		}
		var task, metric, threshold, minutes any
		if r.Kind == KindProbe {
			if err := requireAlertReference(tx, "probe_task", ObjectProbeTask, int64(r.TaskID)); err != nil {
```

替换为：

```go
			}
		}
		// 每种规则只落自己的专用列，其余列写 NULL。CheckKindFields 已保证别的种类的专用字段都是零值，NULL 与回显的
		// 零值一致。
		var task, metric, threshold, minutes, daysBefore any
		if r.Kind == KindProbe {
			if err := requireAlertReference(tx, "probe_task", ObjectProbeTask, int64(r.TaskID)); err != nil {
```

`internal/hub/store/alert.go` 原文（8/16）：

```go
			}
			task, metric, threshold, minutes = int64(r.TaskID), r.Metric, r.Threshold, r.ForMinutes
		} else {
			r.TaskID, r.Metric, r.Threshold, r.ForMinutes = 0, "", 0, 0
		}
		var created int64
```

替换为：

```go
			}
			task, metric, threshold, minutes = int64(r.TaskID), r.Metric, r.Threshold, r.ForMinutes
		}
		if r.Kind == KindExpiry {
			daysBefore = r.DaysBefore
		}
		var created int64
```

`internal/hub/store/alert.go` 原文（9/16）：

```go
		if r.ID == 0 {
			created = s.clk.Now().Unix()
			err := tx.QueryRow(`INSERT INTO alert_rule (name, kind, enabled, all_nodes, task_id, metric, threshold, for_minutes, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`, r.Name, r.Kind, r.Enabled, r.AllNodes, task, metric, threshold, minutes, created).Scan(&r.ID)
			if err != nil {
				return err
```

替换为：

```go
		if r.ID == 0 {
			created = s.clk.Now().Unix()
			err := tx.QueryRow(`INSERT INTO alert_rule (name, kind, enabled, all_nodes, task_id, metric, threshold, for_minutes, days_before, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`, r.Name, r.Kind, r.Enabled, r.AllNodes, task, metric, threshold, minutes, daysBefore, created).Scan(&r.ID)
			if err != nil {
				return err
```

`internal/hub/store/alert.go` 原文（10/16）：

```go
				return err
			}
			err = tx.QueryRow(`UPDATE alert_rule SET name = ?, kind = ?, enabled = ?, all_nodes = ?, task_id = ?, metric = ?, threshold = ?, for_minutes = ?
				WHERE id = ? RETURNING created_at`, r.Name, r.Kind, r.Enabled, r.AllNodes, task, metric, threshold, minutes, r.ID).Scan(&created)
			if errors.Is(err, sql.ErrNoRows) {
				return NotFoundError{Kind: ObjectAlertRule, ID: r.ID}
```

替换为：

```go
				return err
			}
			err = tx.QueryRow(`UPDATE alert_rule SET name = ?, kind = ?, enabled = ?, all_nodes = ?, task_id = ?, metric = ?, threshold = ?, for_minutes = ?, days_before = ?
				WHERE id = ? RETURNING created_at`, r.Name, r.Kind, r.Enabled, r.AllNodes, task, metric, threshold, minutes, daysBefore, r.ID).Scan(&created)
			if errors.Is(err, sql.ErrNoRows) {
				return NotFoundError{Kind: ObjectAlertRule, ID: r.ID}
```

`internal/hub/store/alert.go` 原文（11/16）：

```go
		}
		// 规则与状态由本次写事务一起提交；种类、任务或指标变化后，旧观测不再描述当前规则。
		// threshold 与 for_minutes 不是身份：同一个量换阈值时保留状态，下一轮按新阈值判断是否恢复。
		// 禁用与作用域收缩也在这里裁剪，重启不能重新载入已不适用的 firing。
		if identityChanged || !r.Enabled || !r.AllNodes {
```

替换为：

```go
		}
		// 规则与状态由本次写事务一起提交；种类、任务或指标变化后，旧观测不再描述当前规则。
		// threshold、for_minutes 与 days_before 不是身份：同一个量换阈值时保留状态，下一轮按新阈值判断是否恢复。
		// 禁用与作用域收缩也在这里裁剪，重启不能重新载入已不适用的 firing。
		if identityChanged || !r.Enabled || !r.AllNodes {
```

`internal/hub/store/alert.go` 原文（12/16）：

```go

func (s *Store) ListAlertStates(ctx context.Context) ([]StateRow, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT rule_id, node_id, state, since_at FROM alert_state ORDER BY rule_id, node_id")
	if err != nil {
		return nil, err
```

替换为：

```go

func (s *Store) ListAlertStates(ctx context.Context) ([]StateRow, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT rule_id, node_id, state, since_at, fired_expires_on FROM alert_state ORDER BY rule_id, node_id")
	if err != nil {
		return nil, err
```

`internal/hub/store/alert.go` 原文（13/16）：

```go
		var r StateRow
		var since int64
		if err := rows.Scan(&r.RuleID, &r.NodeID, &r.State, &since); err != nil {
			return nil, err
		}
```

替换为：

```go
		var r StateRow
		var since int64
		if err := rows.Scan(&r.RuleID, &r.NodeID, &r.State, &since, &r.FiredExpiresOn); err != nil {
			return nil, err
		}
```

`internal/hub/store/alert.go` 原文（14/16）：

```go
}

// 两个状态写入口共用事务内准入，单写协程保证删除之后排队的写不能重建孤儿状态。
func setAlertState(tx *sql.Tx, ruleID, nodeID int64, state AlertState, since time.Time) error {
	if err := requireAlertReference(tx, "alert_rule", ObjectAlertRule, ruleID); err != nil {
		return err
```

替换为：

```go
}

// 两个状态写入口共用事务内准入，单写协程保证删除之后排队的写不能重建孤儿状态。整行替换：每次写都给出
// fired_expires_on，上一个状态记的日期不会留到下一个状态。
func setAlertState(tx *sql.Tx, ruleID, nodeID int64, state AlertState, since time.Time, firedExpiresOn string) error {
	if err := requireAlertReference(tx, "alert_rule", ObjectAlertRule, ruleID); err != nil {
		return err
```

`internal/hub/store/alert.go` 原文（15/16）：

```go
		return NotFoundError{Kind: ObjectNode, ID: nodeID}
	}
	_, err = tx.Exec("INSERT OR REPLACE INTO alert_state (rule_id, node_id, state, since_at) VALUES (?, ?, ?, ?)", ruleID, nodeID, state, since.Unix())
	return err
}

func (s *Store) SetAlertState(ctx context.Context, ruleID, nodeID int64, state AlertState, since time.Time) error {
	return s.write(ctx, func(tx *sql.Tx) error { return setAlertState(tx, ruleID, nodeID, state, since) })
}

```

替换为：

```go
		return NotFoundError{Kind: ObjectNode, ID: nodeID}
	}
	_, err = tx.Exec("INSERT OR REPLACE INTO alert_state (rule_id, node_id, state, since_at, fired_expires_on) VALUES (?, ?, ?, ?, ?)", ruleID, nodeID, state, since.Unix(), firedExpiresOn)
	return err
}

// SetAlertState 写不带事件的状态变化。alert 的状态机只经触发转换进入 firing（那一步走 RecordTransition），所以这里
// 写的状态没有触发时的到期日。
func (s *Store) SetAlertState(ctx context.Context, ruleID, nodeID int64, state AlertState, since time.Time) error {
	return s.write(ctx, func(tx *sql.Tx) error { return setAlertState(tx, ruleID, nodeID, state, since, "") })
}

```

`internal/hub/store/alert.go` 原文（16/16）：

```go
}

// 状态、事件与续投队列同事务提交，崩溃不能留下已转换但没有通知记录的状态。
func (s *Store) RecordTransition(ctx context.Context, ruleID, nodeID int64, state AlertState, ev AlertEvent, channelIDs []int64) (AlertEvent, error) {
	ev.ID, ev.RuleID, ev.NodeID, ev.Deliveries = 0, ruleID, nodeID, nil
	ev.At = time.Unix(ev.At.Unix(), 0).UTC()
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := setAlertState(tx, ruleID, nodeID, state, ev.At); err != nil {
			return err
		}
```

替换为：

```go
}

// 状态、事件与续投队列同事务提交，崩溃不能留下已转换但没有通知记录的状态。firedExpiresOn 随状态写入，
// 含义见 StateRow.FiredExpiresOn。
func (s *Store) RecordTransition(ctx context.Context, ruleID, nodeID int64, state AlertState, firedExpiresOn string, ev AlertEvent, channelIDs []int64) (AlertEvent, error) {
	ev.ID, ev.RuleID, ev.NodeID, ev.Deliveries = 0, ruleID, nodeID, nil
	ev.At = time.Unix(ev.At.Unix(), 0).UTC()
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := setAlertState(tx, ruleID, nodeID, state, ev.At, firedExpiresOn); err != nil {
			return err
		}
```

`internal/hub/api/nodes.go` 原文：

```go
	s.nodeMu.Lock()
	defer s.nodeMu.Unlock()
	err = s.store.UpdateNode(ctx, req.Msg.GetId(), name, req.Msg.GetPublic(), note, day, int(grace))
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound(req.Msg.GetId())
```

替换为：

```go
	s.nodeMu.Lock()
	defer s.nodeMu.Unlock()
	_, err = s.store.UpdateNode(ctx, req.Msg.GetId(), store.NodeEdit{Name: name, Public: req.Msg.GetPublic(), Note: note, TrafficResetDay: day, OfflineGraceS: int(grace)})
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound(req.Msg.GetId())
```

`internal/hub/alert/engine.go` 原文：

```go
	var err error
	if tr != nil {
		ev, err = e.st.RecordTransition(ctx, r.ID, nodeID, next, store.AlertEvent{Transition: *tr, At: now, Summary: summary, Value: value}, r.ChannelIDs)
	} else {
		err = e.st.SetAlertState(ctx, r.ID, nodeID, next, now)
```

替换为：

```go
	var err error
	if tr != nil {
		ev, err = e.st.RecordTransition(ctx, r.ID, nodeID, next, "", store.AlertEvent{Transition: *tr, At: now, Summary: summary, Value: value}, r.ChannelIDs)
	} else {
		err = e.st.SetAlertState(ctx, r.ID, nodeID, next, now)
```

- [ ] **Step 4: 跑绿**

```bash
cd /Users/xjetry/work/vibe/probe-billing && go test -count=1 ./internal/hub/store/ ./internal/hub/alert/ ./internal/hub/api/ ./cmd/hub/ > /tmp/billing-t2-green-t2.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && go vet ./... > /tmp/billing-t2-vet-t2.log 2>&1; echo $?
```

Expected：两条都是 0。以下既有测试照常通过：
- `TestMigrationsReferenceOnlyFrozenDDL`：迁移 9 只引用 `migrationV9`，没有引用 `ddlNode`、`ddlAlertRule` 或 `ddlAlertState`。
- 各版本的迁移测试：从 v1–v8 迁到 v9 都与新建库一致。

- [ ] **Step 5: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-billing && git add cmd/hub/serve_alert_test.go internal/hub/alert/engine.go internal/hub/alert/engine_test.go internal/hub/alert/queue_prune_test.go internal/hub/alert/queue_refill_test.go internal/hub/alert/queue_storage_test.go internal/hub/alert/queue_test.go internal/hub/api/alerts_test.go internal/hub/api/delivery_failure_test.go internal/hub/api/nodes.go internal/hub/store/alert.go internal/hub/store/alert_prune_test.go internal/hub/store/alert_state_test.go internal/hub/store/alert_test.go internal/hub/store/billing_test.go internal/hub/store/maintenance_safety_test.go internal/hub/store/migrations.go internal/hub/store/node.go internal/hub/store/schema.go internal/hub/store/store.go internal/hub/store/store_test.go > /tmp/billing-t2-add.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && git commit -m "store: 节点计费五列、到期规则的提前天数与到期状态的触发日期（schema v9）" -m "迁移 9 只引用冻结的 ALTER 语句；新列在新建库里排在各表的最后，由 v8 迁来的库与新建库逐列相同。UpdateNode 改收 NodeEdit，在同一个写事务里读出旧计费并报告五项是否变化，调用方据此决定是否立即做到期扫描。RenewExpiry 以自动续期、周期与旧到期日为条件写回：推后的日期由扫描按它读到的快照算出，快照之后管理员若改了计费，按旧快照写回会盖掉新值。days_before 只随到期规则落库，它不是规则身份，改它保留状态。种类与专用字段的组合由 CheckKindFields 一处裁决，SaveAlertRule 对非法组合报错：原来离线规则带着的探测字段会被静默清零，调用方无从察觉（§9.1）。alert_state 加 fired_expires_on，记到期规则进入 firing 时节点的到期日：恢复文案要区分到期日改了与只是提前天数调小，触发事件会按保留期清理而过期节点可以一直 firing，这个日期随状态存、由 Load 读回，不从触发事件里找。每次状态写都整行替换，它不会留到下一个状态。" > /tmp/billing-t2-commit.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && git status --porcelain > /tmp/billing-t2-status.log 2>&1; echo $?
```

Expected：三条都是 0；`billing-t2-status.log` 为空。

- [ ] **Step 6: make ci**

```bash
cd /Users/xjetry/work/vibe/probe-billing && make ci > /tmp/billing-t2-ci.log 2>&1; echo $?
```

Expected：0；vitest `Tests  369 passed (369)`。`make ci` 最后核对生成物已入库，所以放在提交之后。

- [ ] **Step 7: 缺陷注入**

| | 改动 | 命令 | 期望 |
|---|---|---|---|
| a | 迁移 9 改成 `execAll([]string{ddlNode})`（引用当前 DDL） | `go test -count=1 -run 'TestMigrationsReferenceOnlyFrozenDDL' ./internal/hub/store/ > /tmp/billing-t2-inj-a.log 2>&1; echo $?` | 1，`migrate_test.go:651: migrations.go:30:22: migrations reference current DDL ddlNode` |
| b | 删掉迁移 9 那一行 | `go test -count=1 -run 'TestMigrationFromV8' ./internal/hub/store/ > /tmp/billing-t2-inj-b.log 2>&1; echo $?` | 1，`billing_test.go:19: open database …/old.db: no migration to schema version 9` |
| c | 迁移 9 里 `auto_renew` 的默认值写成 `DEFAULT 1` | `go test -count=1 -run 'TestMigrationFromV8' ./internal/hub/store/ > /tmp/billing-t2-inj-c.log 2>&1; echo $?` | 1，`billing_test.go:31: migrated schema differs from fresh schema`；got 与 want 只在 `node.auto_renew` 的默认值上不同（`1` 对 `0`） |
| d | 迁移 9 里 `currency` 与 `billing_cycle` 两条 `ADD COLUMN` 对调（列序与新建库不同） | `go test -count=1 -run 'TestMigrationFromV8' ./internal/hub/store/ > /tmp/billing-t2-inj-d.log 2>&1; echo $?` | 1，同一句；got 与 want 在 `node` 的两列上对调（`billing_cycle` 与 `currency`） |
| e | `UpdateNode` 的 `UPDATE` 不写计费五列 | `go test -count=1 -run 'TestUpdateNodeReplacesBilling' ./internal/hub/store/ > /tmp/billing-t2-inj-e.log 2>&1; echo $?` | 1，`billing_test.go:85: read back [… Billing:{Price: Currency: Cycle: ExpiresOn: AutoRenew:false}}] <nil>, want {Price:12.50 Currency:USD Cycle:monthly ExpiresOn:2026-10-01 AutoRenew:true}` |
| f | `billingChanged` 只比到期日与价格 | `go test -count=1 -run 'TestUpdateNodeReplacesBilling' ./internal/hub/store/ > /tmp/billing-t2-inj-f.log 2>&1; echo $?` | 1，`billing_test.go:97: turning off auto renew reported no change` |
| g | `billingChanged` 恒为真 | `go test -count=1 -run 'TestUpdateNodeReplacesBilling' ./internal/hub/store/ > /tmp/billing-t2-inj-g.log 2>&1; echo $?` | 1，`billing_test.go:72: empty billing over empty billing reported a change` |
| h | `RenewExpiry` 的条件去掉 `billing_cycle = ?` | `go test -count=1 -run 'TestRenewExpiry' ./internal/hub/store/ > /tmp/billing-t2-inj-h.log 2>&1; echo $?` | 1，`billing_test.go:159: cycle changed: renewed=true err=<nil> expires_on=2026-02-28` |
| i | `RenewExpiry` 的条件去掉 `expires_on = ?` | `go test -count=1 -run 'TestRenewExpiry' ./internal/hub/store/ > /tmp/billing-t2-inj-i.log 2>&1; echo $?` | 1，`billing_test.go:159: stale date: renewed=true err=<nil> expires_on=2026-02-28` |
| j | `RenewExpiry` 的条件去掉 `auto_renew = 1` | `go test -count=1 -run 'TestRenewExpiry' ./internal/hub/store/ > /tmp/billing-t2-inj-j.log 2>&1; echo $?` | 1，`billing_test.go:159: auto renew off: renewed=true err=<nil> expires_on=2026-02-28` |
| k | `SaveAlertRule` 对任何种类都写 `days_before` 列（换成离线时写进 0 而不是 NULL） | `go test -count=1 -run 'TestAlertRuleDaysBefore' ./internal/hub/store/ > /tmp/billing-t2-inj-k.log 2>&1; echo $?` | 1，`billing_test.go:201: offline rule kept the days_before column: {0 true}`：列里是 0 而不是 NULL |
| l | `ListAlertRules` 不把 `days_before` 读回结构体 | `go test -count=1 -run 'TestAlertRuleDaysBefore' ./internal/hub/store/ > /tmp/billing-t2-inj-l.log 2>&1; echo $?` | 1，`billing_test.go:190: listed [{… Kind:expiry … DaysBefore:0 …}] <nil>` |
| m | 身份比较加上 `days_before`（改提前天数也清状态） | `go test -count=1 -run 'TestAlertRuleDaysBefore' ./internal/hub/store/ > /tmp/billing-t2-inj-m.log 2>&1; echo $?` | 1，`billing_test.go:197: alert_state WHERE rule_id = 1 count=0 err=<nil>, want 1` |
| n | `scanNodes` 把价格与币种读反 | `go test -count=1 -run 'TestUpdateNodeReplacesBilling' ./internal/hub/store/ > /tmp/billing-t2-inj-n.log 2>&1; echo $?` | 1，`billing_test.go:85: read back [… Billing:{Price:USD Currency:12.50 …}] …` |
| o | 迁移 9 漏掉 `alert_state` 的 `ADD COLUMN` | `go test -count=1 -run 'TestMigrationFromV8' ./internal/hub/store/ > /tmp/billing-t2-inj-o.log 2>&1; echo $?` | 1，`billing_test.go:31: migrated schema differs from fresh schema`；got 的 `alert_state` 少了 `fired_expires_on` 一列 |
| p | `setAlertState` 的 `INSERT` 不写 `fired_expires_on`（取列默认值） | `go test -count=1 -run 'TestAlertStateFiredExpiresOn' ./internal/hub/store/ > /tmp/billing-t2-inj-p.log 2>&1; echo $?` | 1，`billing_test.go:260: after firing: fired_expires_on = "", want 2026-10-01` |
| q | `ListAlertStates` 不把 `fired_expires_on` 读回结构体 | `go test -count=1 -run 'TestAlertStateFiredExpiresOn\|TestMigrationFromV8' ./internal/hub/store/ > /tmp/billing-t2-inj-q.log 2>&1; echo $?` | 1，同一句（库里有值，读回时丢了） |
| r | 状态写入改成 upsert，空值不覆盖上一次记下的日期 | `go test -count=1 -run 'TestAlertStateFiredExpiresOn' ./internal/hub/store/ > /tmp/billing-t2-inj-r.log 2>&1; echo $?` | 1，`billing_test.go:264: after recovery: fired_expires_on = "2026-10-01", want empty` |
| s | `SaveAlertRule` 不调 `CheckKindFields`（非法组合照常写入） | `go test -count=1 -run 'TestSaveAlertRuleRejectsFieldsOfOtherKinds' ./internal/hub/store/ > /tmp/billing-t2-inj-s.log 2>&1; echo $?` | 1，`billing_test.go:228: offline with task (id 0): err = <nil>, want KindFieldError on task_id`：`t.Fatalf`，停在第一例 |
| t | `CheckKindFields` 不查 `days_before` | `go test -count=1 -run 'TestSaveAlertRuleRejectsFieldsOfOtherKinds' ./internal/hub/store/ > /tmp/billing-t2-inj-t.log 2>&1; echo $?` | 1，`billing_test.go:228: offline with days_before (id 0): err = <nil>, want KindFieldError on days_before`：前四例照常被拒，停在这一例 |
| u | `billingChanged` 不比币种与周期 | `go test -count=1 -run 'TestUpdateNodeReplacesBilling' ./internal/hub/store/ > /tmp/billing-t2-inj-u.log 2>&1; echo $?` | 1，`billing_test.go:101: changing only the currency reported no change`：`t.Fatal`，停在只改币种这一步 |

每项：改动 → `git diff --stat` 非空 → 跑命令 → 核对原因 → `git checkout -- internal/hub/store`。

---

### Task 3: alert：到期规则、自动续期与按 hub 时区日界的到期扫描

**Files:**
- Create: `internal/hub/alert/expiry.go`、`internal/hub/alert/expiry_test.go`
- Modify: `internal/hub/alert/state.go`（`ExpiryObservation`、`NextExpiry`）
- Modify: `internal/hub/alert/rule.go`（`CheckRule` 在已知种类上经 `checkKindFields` 调 `store.CheckKindFields`）
- Modify: `internal/hub/alert/engine.go`（`Config.Location` 与 `New` 的检查；`stateEntry.firedExpiresOn`，`Load`、`States`、`apply` 带上它；`entry`；`SaveRule` 之后评估到期规则；`publishRule`，注释写明 `days_before` 也不是身份）
- Modify: `cmd/hub/serve.go`（`--timezone` 的帮助；`alert.Config` 带 `loc`；`RunExpirySweep` 循环）
- Test（夹具与新用例）：
  - `internal/hub/alert/engine_test.go`（夹具的时区为东八区）
  - `internal/hub/alert/rule_test.go`
  - `cmd/hub/serve_alert_test.go`（`TestServeRenewsExpiryAtStartupInTheHubZone`）
  - `alert.New` 的其余调用点加 `Location: time.UTC`：`cmd/hub/mux_test.go`、`internal/hub/api/api_test.go`、`internal/hub/api/alert_scope_reload_test.go`

**Interfaces:**
- Consumes：Task 2 的 `store.Billing`、`BillingCycles`、`NodeEdit`、`RenewExpiry`、`KindExpiry`、`AlertRule.DaysBefore`、`CheckKindFields`、`KindFieldError`、`StateRow.FiredExpiresOn`、`RecordTransition` 的 `firedExpiresOn`。
- Produces（`internal/hub/alert`）：
  - `type Config struct { TTL time.Duration; Location *time.Location }`；`New` 在 `Location == nil` 时 panic `alert.Config.Location must be set`
  - `func ParseDate(s string) (time.Time, error)`
  - `func Today(now time.Time, loc *time.Location) time.Time`
  - `func DaysLeft(expiresOn string, today time.Time) (int, bool)`
  - `type ExpiryObservation struct { HasExpiry bool; DaysLeft int; DaysBefore int }`
  - `func NextExpiry(cur store.AlertState, o ExpiryObservation) (store.AlertState, *store.Transition)`
  - `func (e *Engine) SweepExpiry(ctx context.Context) error`
  - `func (e *Engine) RunExpirySweep(ctx context.Context)`
  - `States()` 返回的 `StateRow` 带 `FiredExpiresOn`
  - 包内：`daysBetween`、`cycleMonths`、`addMonths`、`renewedExpiry`、`nextDayStart`、`sweepExpiry`（调用方持 `writeMu`）、`expirySummary(n, r, o, tr, firedOn)`、`entry`、`apply(ctx, r, nodeID, next, firedExpiresOn, tr, summary, value)`、`publishRule`、`checkKindFields`
- 行为：`CheckRule` 拒绝离线规则上的探测字段（设计决定 9）；恢复文案按离开窗口的原因三选一（设计决定 14、18）；续期写回落空或出错的节点本轮不评估（设计决定 19）。

- [ ] **Step 1: 写失败测试**

`expiry_test.go` 覆盖日期工具（合法日期、今天、剩余天数、周期月数、推后、下一个日界）、`NextExpiry` 的状态表，以及扫描：
- 文案与 `value` 按 §9.2 原文；按 hub 时区跨零点。
- 恢复文案按原因：日期没变、只是提前天数调小写"已不在提醒窗口内"，重启前后都认得出；firing 期间改过日期再调小提前天数写"到期日已更新为"。每一步保存规则之前先照常扫描一次，留在 firing 的扫描不改记下的日期。
- 只提醒一次：从"将于"走到到期当天、再走到"已于"都不发第二条；到期当天写"剩 0 天"。
- 先推后再评估，并记一行日志；续期写回落空（快照已过期）或出错的节点本轮不评估，写回成功之后照常续期；出错时错误随扫描返回（用 SQLite 触发器造出落空与出错，实验 14）。
- 没有可用周期不推后；读不懂的日期跳过评估、保留状态、不推后，两次扫描各对每个节点记一行 Warn。
- 作用域、停用与被别处删掉的节点。
- 保存规则即评估；循环启动即扫描，日界到点再扫描一次（随真实时间走的测试钟）；一轮扫描跨过零点时新一天不被跳过（按读钟次序切换零点前后的钟，零点前读 1 次与 2 次各一例，实验 15）；载入经 `CheckRule`，跳过非法规则时记一行 Warn；`New` 要求时区。

夹具改为东八区：夹具时钟的 UTC 12:00 是当地 20:00，UTC 16:00 起当地已是次日，测试据此区分"按哪个时区取今天"。`rule_test.go` 加上三种规则带别的种类字段的用例，合法用例改用完整的 `offline()`：原来那条离线用例留着阈值 20，现在是非法的。`serve_alert_test.go` 从 `serve` 的入口证明启动扫描按 `--timezone` 取今天。

新建 `internal/hub/alert/expiry_test.go`（整份）：

```go
package alert

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	_ "time/tzdata" // 夏令时用例要真实时区库；嵌入的库让结论不随测试机的系统时区库变化。

	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/testwait"
)

func expiryRule() store.AlertRule {
	return store.AlertRule{Name: "到期", Kind: store.KindExpiry, Enabled: true, AllNodes: true, DaysBefore: 7}
}

func date(s string) time.Time {
	d, err := ParseDate(s)
	if err != nil {
		panic(err)
	}
	return d
}

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// "合法日期"的口径：四位年、两位月日、日子真实存在，前后不带任何字符。
func TestParseDateAcceptsOnlyRealYYYYMMDD(t *testing.T) {
	for _, ok := range []string{"2026-02-28", "2028-02-29", "0000-01-01", "9999-12-31"} {
		if _, err := ParseDate(ok); err != nil {
			t.Errorf("ParseDate(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "2026-02-29", "2026-02-30", "2026-13-01", "2026-1-05", "2026-01-5", "20260105", "2026/01/05",
		" 2026-01-05", "2026-01-05 ", "2026-01-05T00:00:00Z", "10000-01-01", "２０２６-01-05"} {
		if _, err := ParseDate(bad); err == nil {
			t.Errorf("ParseDate(%q) accepted", bad)
		}
	}
}

// 同一时刻在不同时区是不同的日历日：UTC 16:30 在东八区已是次日。
func TestTodayTakesTheCalendarDayInTheZone(t *testing.T) {
	now := time.Date(2026, 9, 24, 16, 30, 0, 0, time.UTC)
	for _, c := range []struct {
		loc  *time.Location
		want string
	}{
		{time.UTC, "2026-09-24"},
		{time.FixedZone("UTC+8", 8*3600), "2026-09-25"},
		{zone(t, "America/Los_Angeles"), "2026-09-24"},
	} {
		if got := Today(now, c.loc); !got.Equal(date(c.want)) {
			t.Errorf("Today in %s = %s, want %s", c.loc, got.Format(time.DateOnly), c.want)
		}
	}
}

// 剩余天数是日历日之差：跨夏令时不差一天，跨几千年也不因 time.Duration 饱和而算错。
func TestDaysLeftCountsCalendarDays(t *testing.T) {
	today := date("2026-09-27")
	for _, c := range []struct {
		expiresOn string
		want      int
	}{
		{"2026-10-01", 4},
		{"2026-09-27", 0},
		{"2026-09-20", -7},
		{"2027-03-28", 182}, // 跨过北半球夏令时的开始与结束
		{"9999-12-31", 2912173},
		{"0000-01-01", -740251},
	} {
		got, ok := DaysLeft(c.expiresOn, today)
		if !ok || got != c.want {
			t.Errorf("DaysLeft(%s) = %d %v, want %d", c.expiresOn, got, ok, c.want)
		}
	}
	for _, none := range []string{"", "2026-02-30"} {
		if got, ok := DaysLeft(none, today); ok {
			t.Errorf("DaysLeft(%q) = %d, want none", none, got)
		}
	}
}

func TestCycleMonthsCoversEveryStoredCycle(t *testing.T) {
	want := map[store.BillingCycle]int{store.CycleMonthly: 1, store.CycleQuarterly: 3, store.CycleSemiannual: 6,
		store.CycleYearly: 12, store.CycleBiennial: 24, store.CycleTriennial: 36}
	cycles := store.BillingCycles()
	if len(cycles) != len(want) {
		t.Fatalf("store.BillingCycles() = %v, want the %d cycles of §9.4", cycles, len(want))
	}
	for _, c := range cycles {
		if got, ok := cycleMonths(c); !ok || got != want[c] {
			t.Errorf("cycleMonths(%s) = %d %v, want %d", c, got, ok, want[c])
		}
	}
	for _, c := range []store.BillingCycle{store.CycleNone, "weekly"} {
		if got, ok := cycleMonths(c); ok {
			t.Errorf("cycleMonths(%q) = %d, want no renewal", c, got)
		}
	}
}

// 推后到不早于今天为止；每一步从上一步的结果推后，钳到月末之后不回弹。
func TestRenewedExpiry(t *testing.T) {
	monthly := store.Billing{Cycle: store.CycleMonthly, AutoRenew: true}
	with := func(b store.Billing, expiresOn string) store.Billing { b.ExpiresOn = expiresOn; return b }
	for _, c := range []struct {
		name  string
		b     store.Billing
		today string
		want  string
	}{
		{"one month", with(monthly, "2026-09-10"), "2026-09-24", "2026-10-10"},
		{"several months, clamped then drifting", with(monthly, "2026-01-31"), "2026-04-01", "2026-04-28"},
		{"leap February", with(monthly, "2028-01-31"), "2028-02-15", "2028-02-29"},
		{"yearly from February 29", store.Billing{Cycle: store.CycleYearly, AutoRenew: true, ExpiresOn: "2024-02-29"}, "2025-03-01", "2026-02-28"},
		{"quarterly lands on today", store.Billing{Cycle: store.CycleQuarterly, AutoRenew: true, ExpiresOn: "2026-06-24"}, "2026-09-24", "2026-09-24"},
		{"triennial", store.Billing{Cycle: store.CycleTriennial, AutoRenew: true, ExpiresOn: "2020-01-15"}, "2026-09-24", "2029-01-15"},
		{"from year 0", with(monthly, "0000-01-31"), "0000-03-01", "0000-03-29"},
	} {
		got, ok := renewedExpiry(c.b, date(c.today))
		if !ok || got != c.want {
			t.Errorf("%s: renewedExpiry = %q %v, want %q", c.name, got, ok, c.want)
		}
	}
	for _, c := range []struct {
		name string
		b    store.Billing
	}{
		{"auto renew off", store.Billing{Cycle: store.CycleMonthly, ExpiresOn: "2026-09-10"}},
		{"no cycle", store.Billing{AutoRenew: true, ExpiresOn: "2026-09-10"}},
		{"unknown cycle", store.Billing{Cycle: "weekly", AutoRenew: true, ExpiresOn: "2026-09-10"}},
		{"no date", with(monthly, "")},
		{"unreadable date", with(monthly, "2026-09-31")},
		{"due today", with(monthly, "2026-09-24")},
		{"in the future", with(monthly, "2026-10-01")},
	} {
		if got, ok := renewedExpiry(c.b, date("2026-09-24")); ok {
			t.Errorf("%s: renewed to %s", c.name, got)
		}
	}
}

// 下一个日界是本地日期变化的第一个时刻：严格晚于 now，本地日期是明天，再早 1 纳秒还是今天。
// 夏令时在零点开始的时区里零点不存在，新的一天从 01:00 开始。go1.27.1 的 time.Date 对不存在的零点给出的时刻两个方向
// 都有：Santiago、Havana 往回给前一天的 23:00，本地日期没变，要取 ZoneBounds 的 end；Cairo、Beirut 往前给新一天的
// 01:00，本地日期已变，它本身就是答案。两类各有用例，只认其中一类的写法会在另一类上定错日界。
func TestNextDayStartIsTheFirstInstantOfTheNextLocalDay(t *testing.T) {
	for _, c := range []struct {
		loc  *time.Location
		now  string // loc 里的本地时刻
		want string // loc 里的本地时刻与偏移
	}{
		{time.UTC, "2026-09-27T00:00:00", "2026-09-28T00:00:00Z"},
		{time.FixedZone("UTC+8", 8*3600), "2026-09-24T20:00:00", "2026-09-25T00:00:00+08:00"},
		{zone(t, "America/New_York"), "2026-03-07T22:00:00", "2026-03-08T00:00:00-05:00"},
		{zone(t, "America/Santiago"), "2026-09-05T22:00:00", "2026-09-06T01:00:00-03:00"},
		{zone(t, "America/Santiago"), "2026-04-04T22:00:00", "2026-04-05T00:00:00-04:00"},
		{zone(t, "America/Havana"), "2026-03-07T22:00:00", "2026-03-08T01:00:00-04:00"},
		{zone(t, "America/Havana"), "2026-10-31T22:00:00", "2026-11-01T00:00:00-04:00"},
		{zone(t, "Africa/Cairo"), "2026-04-23T22:00:00", "2026-04-24T01:00:00+03:00"},
		{zone(t, "Asia/Beirut"), "2026-03-28T22:00:00", "2026-03-29T01:00:00+03:00"},
	} {
		now, err := time.ParseInLocation("2006-01-02T15:04:05", c.now, c.loc)
		if err != nil {
			t.Fatal(err)
		}
		got := nextDayStart(now, c.loc)
		if got.Format(time.RFC3339) != c.want {
			t.Errorf("%s from %s: next day starts %s, want %s", c.loc, c.now, got.Format(time.RFC3339), c.want)
		}
		today, tomorrow := Today(now, c.loc), Today(now, c.loc).AddDate(0, 0, 1)
		if !got.After(now) || !Today(got, c.loc).Equal(tomorrow) || !Today(got.Add(-time.Nanosecond), c.loc).Equal(today) {
			t.Errorf("%s from %s: %s is not the first instant of the next local day", c.loc, c.now, got.Format(time.RFC3339Nano))
		}
	}
}

func TestNextExpiry(t *testing.T) {
	firing, recoveredTr := store.TransitionFiring, store.TransitionRecovered
	for _, c := range []struct {
		name   string
		cur    store.AlertState
		o      ExpiryObservation
		want   store.AlertState
		wantTr *store.Transition
	}{
		{"no date, never fired", store.StateOK, ExpiryObservation{DaysBefore: 7}, store.StateOK, nil},
		{"date cleared while firing", store.StateFiring, ExpiryObservation{DaysBefore: 7}, store.StateOK, &recoveredTr},
		{"outside the window", store.StateOK, ExpiryObservation{HasExpiry: true, DaysLeft: 8, DaysBefore: 7}, store.StateOK, nil},
		{"enters the window on its edge", store.StateOK, ExpiryObservation{HasExpiry: true, DaysLeft: 7, DaysBefore: 7}, store.StateFiring, &firing},
		{"already expired", store.StateOK, ExpiryObservation{HasExpiry: true, DaysLeft: -3, DaysBefore: 7}, store.StateFiring, &firing},
		{"stays firing", store.StateFiring, ExpiryObservation{HasExpiry: true, DaysLeft: -1, DaysBefore: 7}, store.StateFiring, nil},
		{"renewed out of the window", store.StateFiring, ExpiryObservation{HasExpiry: true, DaysLeft: 8, DaysBefore: 7}, store.StateOK, &recoveredTr},
		{"no pending stage", store.StatePending, ExpiryObservation{HasExpiry: true, DaysLeft: 1, DaysBefore: 7}, store.StateFiring, &firing},
	} {
		got, tr := NextExpiry(c.cur, c.o)
		if got != c.want || (tr == nil) != (c.wantTr == nil) || (tr != nil && *tr != *c.wantTr) {
			t.Errorf("%s: NextExpiry = %s %v, want %s %v", c.name, got, tr, c.want, c.wantTr)
		}
	}
}

func (f *fixture) billing(t *testing.T, id int64, b store.Billing) {
	t.Helper()
	_, err := f.st.UpdateNode(t.Context(), id, store.NodeEdit{Name: fmt.Sprintf("node%d", id), TrafficResetDay: 1, Billing: b})
	must(t, err)
}

func (f *fixture) expiresOn(t *testing.T, id int64) string {
	t.Helper()
	n, err := f.st.GetNode(t.Context(), id)
	must(t, err)
	return n.Billing.ExpiresOn
}

func (f *fixture) sweepExpiry(t *testing.T) { t.Helper(); must(t, f.e.SweepExpiry(t.Context())) }

// 夹具的今天是东八区的 2026-09-24。触发与恢复的文案按 §9.2 原文，没有到期日的节点不产生状态。
func TestSweepExpiryFiresAndRecoversWithSpecSummaries(t *testing.T) {
	f := newFixture(t)
	node1, node2 := f.ids[0], f.ids[1]
	r := f.rule(t, expiryRule())
	for _, step := range []struct {
		expiresOn string
		state     store.AlertState
		summary   string
		value     float64
	}{
		{"2026-09-28", store.StateFiring, "节点 node1 将于 2026-09-28 到期（剩 4 天，规则 到期）", 4},
		{"2026-11-01", store.StateOK, "节点 node1 到期日已更新为 2026-11-01（规则 到期）", 38},
		{"2026-09-17", store.StateFiring, "节点 node1 已于 2026-09-17 到期（已过期 7 天，规则 到期）", -7},
		{"", store.StateOK, "节点 node1 已清除到期日（规则 到期）", 0},
	} {
		f.billing(t, node1, store.Billing{ExpiresOn: step.expiresOn})
		before := len(f.events(t))
		f.sweepExpiry(t)
		f.sweepExpiry(t)
		events := f.events(t)
		if len(events) != before+1 {
			t.Fatalf("expires_on %q: %d new events, want exactly 1 across two sweeps", step.expiresOn, len(events)-before)
		}
		if ev := events[0]; ev.NodeID != node1 || ev.Summary != step.summary || ev.Value != step.value {
			t.Fatalf("expires_on %q: event %+v, want summary %q value %v", step.expiresOn, ev, step.summary, step.value)
		}
		wantState(t, f.e, r.ID, node1, step.state)
		wantState(t, f.e, r.ID, node2, "")
	}
}

// 今天取 hub 时区的日历日：UTC 15:59 与 16:00 之间东八区跨过零点，剩余天数从 8 变成 7，规则在这一刻触发。
func TestSweepExpiryCountsDaysInTheHubZone(t *testing.T) {
	f := newFixture(t)
	r := f.rule(t, expiryRule())
	f.billing(t, f.ids[0], store.Billing{ExpiresOn: "2026-10-02"})
	f.clk.SetWall(time.Date(2026, 9, 24, 15, 59, 0, 0, time.UTC))
	f.sweepExpiry(t)
	wantState(t, f.e, r.ID, f.ids[0], "")
	f.clk.SetWall(time.Date(2026, 9, 24, 16, 0, 0, 0, time.UTC))
	f.sweepExpiry(t)
	wantState(t, f.e, r.ID, f.ids[0], store.StateFiring)
}

// 同一次扫描里先推后、再评估：已触发的告警随续期直接恢复，推后的日期落库并记一行日志。
func TestSweepExpiryRenewsBeforeEvaluating(t *testing.T) {
	f := newFixture(t)
	var logs bytes.Buffer
	f.log = slog.New(slog.NewTextHandler(&logs, nil))
	f.restart(t)
	r := f.rule(t, expiryRule())
	f.billing(t, f.ids[0], store.Billing{Cycle: store.CycleMonthly, ExpiresOn: "2026-09-20"})
	f.sweepExpiry(t)
	wantState(t, f.e, r.ID, f.ids[0], store.StateFiring)
	f.billing(t, f.ids[0], store.Billing{Cycle: store.CycleMonthly, ExpiresOn: "2026-09-20", AutoRenew: true})
	f.sweepExpiry(t)
	if got := f.expiresOn(t, f.ids[0]); got != "2026-10-20" {
		t.Fatalf("expires_on after renewal = %s, want 2026-10-20", got)
	}
	events := f.events(t)
	if len(events) != 2 || events[0].Transition != store.TransitionRecovered || events[0].Summary != "节点 node1 到期日已更新为 2026-10-20（规则 到期）" {
		t.Fatalf("events after renewal: %+v", events)
	}
	wantState(t, f.e, r.ID, f.ids[0], store.StateOK)
	if !strings.Contains(logs.String(), `msg="node expiry renewed" node_id=1 node=node1 cycle=monthly from=2026-09-20 to=2026-10-20`) {
		t.Fatalf("renewal log line missing:\n%s", logs.String())
	}
}

// 周期为空或读不懂时扫描自己不推后，不依赖保存入口拦下这种组合。
func TestSweepExpiryDoesNotRenewWithoutAUsableCycle(t *testing.T) {
	f := newFixture(t)
	for _, cycle := range []store.BillingCycle{store.CycleNone, "weekly"} {
		f.billing(t, f.ids[0], store.Billing{Cycle: cycle, ExpiresOn: "2026-08-31", AutoRenew: true})
		f.sweepExpiry(t)
		if got := f.expiresOn(t, f.ids[0]); got != "2026-08-31" {
			t.Fatalf("cycle %q renewed to %s", cycle, got)
		}
	}
}

// 库里读不懂的到期日（只有手改会产生）既不触发也不恢复：已触发的状态原样保留，开着自动续期也不推后，每次扫描对
// 每个这样的节点记一行 Warn，不去重：扫描两次就是两行。
func TestSweepExpirySkipsUnreadableDates(t *testing.T) {
	f := newFixture(t)
	var logs bytes.Buffer
	f.log = slog.New(slog.NewTextHandler(&logs, nil))
	f.restart(t)
	r := f.rule(t, expiryRule())
	f.billing(t, f.ids[0], store.Billing{ExpiresOn: "2026-09-25"})
	f.sweepExpiry(t)
	wantState(t, f.e, r.ID, f.ids[0], store.StateFiring)
	f.billing(t, f.ids[0], store.Billing{Cycle: store.CycleMonthly, AutoRenew: true, ExpiresOn: "2026-09-31"})
	f.billing(t, f.ids[1], store.Billing{ExpiresOn: "2026/09/25"})
	f.sweepExpiry(t)
	f.sweepExpiry(t)
	wantState(t, f.e, r.ID, f.ids[0], store.StateFiring)
	wantState(t, f.e, r.ID, f.ids[1], "")
	if events := f.events(t); len(events) != 1 {
		t.Fatalf("events = %+v, want only the first firing", events)
	}
	if got := f.expiresOn(t, f.ids[0]); got != "2026-09-31" {
		t.Fatalf("unreadable date renewed to %s", got)
	}
	for _, want := range []string{"node_id=1 expires_on=2026-09-31", "node_id=2 expires_on=2026/09/25"} {
		line := `level=WARN msg="node expires_on is not a YYYY-MM-DD date; expiry rules skip this node" ` + want
		if n := strings.Count(logs.String(), line); n != 2 {
			t.Fatalf("%d lines of %q, want 2:\n%s", n, line, logs.String())
		}
	}
}

// 作用域外的节点不评估；停用的规则不评估。
func TestSweepExpiryHonoursScopeAndEnabled(t *testing.T) {
	f := newFixture(t)
	scoped := expiryRule()
	scoped.AllNodes, scoped.NodeIDs = false, []int64{f.ids[1]}
	scoped = f.rule(t, scoped)
	disabled := expiryRule()
	disabled.Enabled = false
	disabled = f.rule(t, disabled)
	f.billing(t, f.ids[0], store.Billing{ExpiresOn: "2026-09-25"})
	f.sweepExpiry(t)
	wantState(t, f.e, scoped.ID, f.ids[0], "")
	wantState(t, f.e, disabled.ID, f.ids[0], "")
	if events := f.events(t); len(events) != 0 {
		t.Fatalf("events = %+v", events)
	}
}

// 另一个进程删掉的节点（probe-hub node delete 删了库里的状态行，没经过 Forget）不再是候选，内存里的状态随扫描清掉。
func TestSweepExpiryDropsStatesOfNodesDeletedElsewhere(t *testing.T) {
	f := newFixture(t)
	r := f.rule(t, expiryRule())
	f.billing(t, f.ids[0], store.Billing{ExpiresOn: "2026-09-25"})
	f.sweepExpiry(t)
	wantState(t, f.e, r.ID, f.ids[0], store.StateFiring)
	must(t, f.st.DeleteNode(t.Context(), f.ids[0]))
	f.sweepExpiry(t)
	wantState(t, f.e, r.ID, f.ids[0], "")
}

// 保存启用的到期规则即评估一次：不等下一个日界。停用的规则保存后没有状态。
func TestSaveRuleEvaluatesEnabledExpiryRules(t *testing.T) {
	f := newFixture(t)
	f.billing(t, f.ids[0], store.Billing{ExpiresOn: "2026-09-27"})
	disabled := expiryRule()
	disabled.Enabled = false
	disabled = f.rule(t, disabled)
	wantState(t, f.e, disabled.ID, f.ids[0], "")
	enabled := f.rule(t, expiryRule())
	wantState(t, f.e, enabled.ID, f.ids[0], store.StateFiring)
	enabled.DaysBefore = 2
	f.rule(t, enabled)
	wantState(t, f.e, enabled.ID, f.ids[0], store.StateOK)
}

// 恢复文案按离开窗口的原因。日期没变、只是提前天数调小：已不在提醒窗口内，第一次恢复前重启过一次（日期从库里读回），
// 第二次没有（日期来自内存）。firing 期间改过日期再调小提前天数：日期与触发时不同，写到期日已更新。每一步保存规则
// 之前先照常扫描一次：留在 firing 的扫描不改记下的日期。事件从这次扫描之前数起，所以重启之后的第一次扫描也在断言
// 之内：状态由 Load 从库里读回，留在 firing 不再发第二条触发（§9.2）。
func TestSweepExpiryRecoverySummaryFollowsTheReason(t *testing.T) {
	f := newFixture(t)
	node1 := f.ids[0]
	f.billing(t, node1, store.Billing{ExpiresOn: "2026-09-27"})
	r := f.rule(t, expiryRule())
	f.restart(t)
	for _, step := range []struct {
		expiresOn  string
		daysBefore int
		state      store.AlertState
		summary    string
		value      float64
	}{
		{"2026-09-27", 2, store.StateOK, "节点 node1 已不在提醒窗口内（规则 到期）", 3},
		{"2026-09-27", 7, store.StateFiring, "节点 node1 将于 2026-09-27 到期（剩 3 天，规则 到期）", 3},
		{"2026-09-27", 2, store.StateOK, "节点 node1 已不在提醒窗口内（规则 到期）", 3},
		{"2026-09-27", 7, store.StateFiring, "节点 node1 将于 2026-09-27 到期（剩 3 天，规则 到期）", 3},
		{"2026-09-29", 7, store.StateFiring, "", 0},
		{"2026-09-29", 4, store.StateOK, "节点 node1 到期日已更新为 2026-09-29（规则 到期）", 5},
	} {
		before := len(f.events(t))
		f.billing(t, node1, store.Billing{ExpiresOn: step.expiresOn})
		f.sweepExpiry(t)
		r.DaysBefore = step.daysBefore
		r = f.rule(t, r)
		events := f.events(t)
		switch {
		case step.summary == "" && len(events) != before:
			t.Fatalf("%s with days_before %d: %d new events, want none", step.expiresOn, step.daysBefore, len(events)-before)
		case step.summary != "" && (len(events) != before+1 || events[0].Summary != step.summary || events[0].Value != step.value):
			t.Fatalf("%s with days_before %d: events %+v, want one new event %q value %v", step.expiresOn, step.daysBefore, events, step.summary, step.value)
		}
		wantState(t, f.e, r.ID, node1, step.state)
	}
}

// 一条规则对一个节点只提醒一次：从"将于"走到到期当天、再走到"已于"都不发第二条。到期当天写"剩 0 天"。
func TestSweepExpiryNotifiesOncePerEntry(t *testing.T) {
	f := newFixture(t)
	f.rule(t, expiryRule())
	f.billing(t, f.ids[0], store.Billing{ExpiresOn: "2026-09-25"})
	f.billing(t, f.ids[1], store.Billing{ExpiresOn: "2026-09-24"})
	for day := range 4 {
		f.clk.SetWall(time.Date(2026, 9, 24+day, 12, 0, 0, 0, time.UTC))
		f.sweepExpiry(t)
	}
	var got []string
	for _, ev := range f.events(t) {
		got = append(got, ev.Summary)
	}
	slices.Sort(got)
	want := []string{"节点 node1 将于 2026-09-25 到期（剩 1 天，规则 到期）", "节点 node2 将于 2026-09-24 到期（剩 0 天，规则 到期）"}
	if !slices.Equal(got, want) {
		t.Fatalf("summaries over four days = %q, want %q", got, want)
	}
}

// 循环一启动就扫描一次：停机跨过的日界由它补上；取消后返回。
func TestRunExpirySweepSweepsOnStartAndStops(t *testing.T) {
	f := newFixture(t)
	f.billing(t, f.ids[0], store.Billing{Cycle: store.CycleMonthly, ExpiresOn: "2026-09-01", AutoRenew: true})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); f.e.RunExpirySweep(ctx) }()
	testwait.Until(t, 10*time.Millisecond, func() bool { return f.expiresOn(t, f.ids[0]) == "2026-10-01" },
		"expires_on = %s, want 2026-10-01 after the startup sweep", testwait.When(func() string { return f.expiresOn(t, f.ids[0]) }))
	cancel()
	select {
	case <-done:
	case <-time.After(testwait.Bound):
		t.Fatal("RunExpirySweep did not return after cancel")
	}
}

// 续期的条件更新没有写入，说明快照之后这一行变了（计费被改过，或节点被删除）：本轮不按过期的快照评估它，不触发
// 也不推后；库里的值与快照一致之后照常续期。触发器让续期的 UPDATE 落空，对 RenewExpiry 的效果与"扫描读快照之后、写回之前有人改了计费"
// 相同。
func TestSweepExpirySkipsANodeWhoseSnapshotIsStale(t *testing.T) {
	f := newFixture(t)
	r := f.rule(t, expiryRule())
	f.billing(t, f.ids[0], store.Billing{Cycle: store.CycleMonthly, ExpiresOn: "2026-09-20", AutoRenew: true})
	db, err := sql.Open("sqlite", f.path)
	must(t, err)
	defer db.Close()
	_, err = db.ExecContext(t.Context(), "CREATE TRIGGER stale BEFORE UPDATE OF expires_on ON node BEGIN SELECT RAISE(IGNORE); END")
	must(t, err)
	f.sweepExpiry(t)
	if events := f.events(t); len(events) != 0 {
		t.Fatalf("a node with a stale snapshot was evaluated: %+v", events)
	}
	wantState(t, f.e, r.ID, f.ids[0], "")
	_, err = db.ExecContext(t.Context(), "DROP TRIGGER stale")
	must(t, err)
	f.sweepExpiry(t)
	if got := f.expiresOn(t, f.ids[0]); got != "2026-10-20" {
		t.Fatalf("expires_on = %s, want 2026-10-20 once the snapshot matches", got)
	}
	if events := f.events(t); len(events) != 0 {
		t.Fatalf("renewed node produced events: %+v", events)
	}
}

// 续期写回出错时，这个节点在库里的有效到期日未定：本轮不评估它，错误随扫描返回，下一轮重试续期。若按未推后的快照
// 评估，开着自动续期的节点会先收到"已过期"，续期成功的下一轮再收到"到期日已更新"，这一对通知都是假的。
func TestSweepExpirySkipsANodeWhoseRenewalFailed(t *testing.T) {
	f := newFixture(t)
	r := f.rule(t, expiryRule())
	f.billing(t, f.ids[0], store.Billing{Cycle: store.CycleMonthly, ExpiresOn: "2026-09-20", AutoRenew: true})
	db, err := sql.Open("sqlite", f.path)
	must(t, err)
	defer db.Close()
	_, err = db.ExecContext(t.Context(), "CREATE TRIGGER renew_fails BEFORE UPDATE OF expires_on ON node BEGIN SELECT RAISE(ABORT, 'renewal write failed'); END")
	must(t, err)
	if err := f.e.SweepExpiry(t.Context()); err == nil || !strings.Contains(err.Error(), "renewal write failed") {
		t.Fatalf("SweepExpiry error = %v, want the renewal write error", err)
	}
	if events := f.events(t); len(events) != 0 {
		t.Fatalf("a node whose renewal failed was evaluated: %+v", events)
	}
	wantState(t, f.e, r.ID, f.ids[0], "")
	_, err = db.ExecContext(t.Context(), "DROP TRIGGER renew_fails")
	must(t, err)
	f.sweepExpiry(t)
	if got := f.expiresOn(t, f.ids[0]); got != "2026-10-20" {
		t.Fatalf("expires_on = %s, want 2026-10-20 once the renewal write succeeds", got)
	}
	if events := f.events(t); len(events) != 0 {
		t.Fatalf("renewed node produced events: %+v", events)
	}
}

// tickingClock 的墙钟从 base 起随真实时间前进。RunExpirySweep 的定时器按真实时间走，墙钟随它一起跨过日界，用例走的
// 是"定时器等到日界再扫"这条路。
type tickingClock struct{ base, start time.Time }

func (c tickingClock) Now() time.Time      { return c.base.Add(time.Since(c.start)) }
func (c tickingClock) Mono() time.Duration { return time.Since(c.start) }

// 启动扫描之后，循环在 hub 时区的日界到点时再扫描一次：墙钟从东八区 2026-09-24 23:59:59.5 起走，约半秒后进入次日，
// 今天到期、开着自动续期的节点由零点之后的扫描推后。启动扫描读到的若还是 24 日，不推后它（到期日不早于今天）；循环
// 在启动扫描之前读钟，定时器定在 25 日零点，启动扫描再慢也不会把下一次推到 26 日。
func TestRunExpirySweepSweepsAgainAtTheDayBoundary(t *testing.T) {
	f := newFixture(t)
	f.billing(t, f.ids[0], store.Billing{Cycle: store.CycleMonthly, ExpiresOn: "2026-09-24", AutoRenew: true})
	clk := tickingClock{base: time.Date(2026, 9, 24, 23, 59, 59, 500_000_000, f.loc), start: time.Now()}
	e := New(Config{TTL: 30 * time.Second, Location: f.loc}, f.st, f.l, clk, f.log)
	must(t, e.Load(t.Context()))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); e.RunExpirySweep(ctx) }()
	testwait.Until(t, 10*time.Millisecond, func() bool { return f.expiresOn(t, f.ids[0]) == "2026-10-24" },
		"expires_on = %s, want 2026-10-24 after the sweep at the day boundary", testwait.When(func() string { return f.expiresOn(t, f.ids[0]) }))
	cancel()
	select {
	case <-done:
	case <-time.After(testwait.Bound):
		t.Fatal("RunExpirySweep did not return after cancel")
	}
}

// scriptedClock 在 arm 之前一律返回 before；arm 之后前 beforeReads 次读仍返回 before，此后一律返回 after。它按读钟的
// 次序而不是真实时间切换，用例借此逐次指定循环与扫描各自读到零点前还是零点后。
type scriptedClock struct {
	mu            sync.Mutex
	before, after time.Time
	armed         bool
	beforeReads   int
}

func (c *scriptedClock) arm() { c.mu.Lock(); c.armed = true; c.mu.Unlock() }

func (c *scriptedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.armed {
		return c.before
	}
	if c.beforeReads > 0 {
		c.beforeReads--
		return c.before
	}
	return c.after
}

func (c *scriptedClock) Mono() time.Duration { return 0 }

// 一轮扫描跨过零点时，新一天的扫描不被跳过。arm 之后每一轮先是循环读钟定下一次触发，再是扫描读钟取今天，扫描结束后
// 循环再读一次钟算定时器时长（夹具里没有规则，扫描不再读钟）。零点前是东八区 9 月 24 日，零点后是 25 日；节点 24 日
// 到期、开着按月自动续期，只有按 25 日扫描才推后到 10 月 24 日。两例各钉住一种跳过：
//   - 前 1 次读在零点前：循环读到 24 日、定到 25 日零点，扫描已读到 25 日并推后。循环若改成扫描之后才读钟，扫描读到
//     24 日不推后，循环读到 25 日而定到 26 日零点。
//   - 前 2 次读在零点前：循环与扫描都读到 24 日，扫描不推后；结束时已过零点，定时器时长为负、立即触发，重扫按 25 日
//     推后。负时长若被改成等到再下一个日界，25 日的扫描就被推到 26 日零点。
func TestRunExpirySweepDoesNotSkipADayBoundaryCrossedDuringASweep(t *testing.T) {
	for _, beforeReads := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d reads before midnight", beforeReads), func(t *testing.T) {
			f := newFixture(t)
			f.billing(t, f.ids[0], store.Billing{Cycle: store.CycleMonthly, ExpiresOn: "2026-09-24", AutoRenew: true})
			clk := &scriptedClock{before: time.Date(2026, 9, 24, 23, 59, 59, 900_000_000, f.loc), after: time.Date(2026, 9, 25, 0, 0, 0, 100_000_000, f.loc), beforeReads: beforeReads}
			e := New(Config{TTL: 30 * time.Second, Location: f.loc}, f.st, f.l, clk, f.log)
			must(t, e.Load(t.Context()))
			clk.arm()
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() { defer close(done); e.RunExpirySweep(ctx) }()
			testwait.Until(t, 10*time.Millisecond, func() bool { return f.expiresOn(t, f.ids[0]) == "2026-10-24" },
				"expires_on = %s, want 2026-10-24: the day boundary crossed during the sweep was skipped", testwait.When(func() string { return f.expiresOn(t, f.ids[0]) }))
			cancel()
			select {
			case <-done:
			case <-time.After(testwait.Bound):
				t.Fatal("RunExpirySweep did not return after cancel")
			}
		})
	}
}

// 载入与保存经同一个 CheckRule：绕过 Engine 直接写进库的越界到期规则载入时跳过并记一行 Warn，合法的照常载入并带着
// 提前天数。
func TestLoadChecksExpiryRules(t *testing.T) {
	f := newFixture(t)
	var logs bytes.Buffer
	f.log = slog.New(slog.NewTextHandler(&logs, nil))
	bad := expiryRule()
	bad.DaysBefore = 400
	_, err := f.st.SaveAlertRule(t.Context(), bad)
	must(t, err)
	good, err := f.st.SaveAlertRule(t.Context(), expiryRule())
	must(t, err)
	f.restart(t)
	if rules := f.e.Rules(); len(rules) != 1 || rules[0].ID != good.ID || rules[0].DaysBefore != 7 {
		t.Fatalf("loaded rules = %+v, want only rule %d with days_before 7", rules, good.ID)
	}
	if want := `level=WARN msg="invalid alert rule skipped" rule_id=1 err="invalid: days_before must be between 1 and 365"`; strings.Count(logs.String(), want) != 1 {
		t.Fatalf("want one line %q in:\n%s", want, logs.String())
	}
}

func TestNewRequiresLocation(t *testing.T) {
	defer func() {
		if r := recover(); r != "alert.Config.Location must be set" {
			t.Fatalf("panic = %v", r)
		}
	}()
	New(Config{TTL: time.Second}, nil, nil, nil, nil)
}
```

`internal/hub/alert/engine_test.go` 原文（1/3）：

```go
	st   *store.Store
	clk  *clock.Fake
	l    *live.Live
	e    *Engine
```

替换为：

```go
	st   *store.Store
	clk  *clock.Fake
	loc  *time.Location
	l    *live.Live
	e    *Engine
```

`internal/hub/alert/engine_test.go` 原文（2/3）：

```go
func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{clk: clock.NewFake(time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	var err error
	f.path = filepath.Join(t.TempDir(), "hub.db")
```

替换为：

```go
func newFixture(t *testing.T) *fixture {
	t.Helper()
	// 时区东八区：夹具时钟的 UTC 12:00 是当地 20:00，UTC 16:00 起当地已是次日，到期测试据此区分"按哪个时区取今天"。
	f := &fixture{clk: clock.NewFake(time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)), loc: time.FixedZone("UTC+8", 8*3600), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	var err error
	f.path = filepath.Join(t.TempDir(), "hub.db")
```

`internal/hub/alert/engine_test.go` 原文（3/3）：

```go
	t.Helper()
	f.l = live.New(f.clk, 30*time.Second)
	f.e = New(Config{TTL: 30 * time.Second}, f.st, f.l, f.clk, f.log)
	must(t, f.e.Load(t.Context()))
}
```

替换为：

```go
	t.Helper()
	f.l = live.New(f.clk, 30*time.Second)
	f.e = New(Config{TTL: 30 * time.Second, Location: f.loc}, f.st, f.l, f.clk, f.log)
	must(t, f.e.Load(t.Context()))
}
```

`internal/hub/alert/rule_test.go` 原文（1/2）：

```go
		{"minutes_low", func(r *store.AlertRule) { r.ForMinutes = 0 }, "for_minutes"},
		{"minutes_high", func(r *store.AlertRule) { r.ForMinutes = 61 }, "for_minutes"},
	}
	for _, c := range cases {
```

替换为：

```go
		{"minutes_low", func(r *store.AlertRule) { r.ForMinutes = 0 }, "for_minutes"},
		{"minutes_high", func(r *store.AlertRule) { r.ForMinutes = 61 }, "for_minutes"},
		{"probe_days_before", func(r *store.AlertRule) { r.DaysBefore = 7 }, "days_before"},
		{"offline_task", func(r *store.AlertRule) { *r = offline(); r.TaskID = 1 }, "task_id"},
		{"offline_metric", func(r *store.AlertRule) { *r = offline(); r.Metric = store.MetricLossPct }, "metric"},
		{"offline_threshold", func(r *store.AlertRule) { *r = offline(); r.Threshold = 20 }, "threshold"},
		{"offline_nan", func(r *store.AlertRule) { *r = offline(); r.Threshold = math.NaN() }, "threshold"},
		{"offline_minutes", func(r *store.AlertRule) { *r = offline(); r.ForMinutes = 3 }, "for_minutes"},
		{"offline_days_before", func(r *store.AlertRule) { *r = offline(); r.DaysBefore = 7 }, "days_before"},
		{"expiry_days_low", func(r *store.AlertRule) { *r = expiryRule(); r.DaysBefore = 0 }, "days_before"},
		{"expiry_days_high", func(r *store.AlertRule) { *r = expiryRule(); r.DaysBefore = 366 }, "days_before"},
		{"expiry_task", func(r *store.AlertRule) { *r = expiryRule(); r.TaskID = 1 }, "task_id"},
		{"expiry_metric", func(r *store.AlertRule) { *r = expiryRule(); r.Metric = store.MetricRttMs }, "metric"},
		{"expiry_threshold", func(r *store.AlertRule) { *r = expiryRule(); r.Threshold = 1 }, "threshold"},
		{"expiry_minutes", func(r *store.AlertRule) { *r = expiryRule(); r.ForMinutes = 1 }, "for_minutes"},
	}
	for _, c := range cases {
```

`internal/hub/alert/rule_test.go` 原文（2/2）：

```go
		func(r *store.AlertRule) { r.AllNodes = false; r.NodeIDs = []int64{1} },
		func(r *store.AlertRule) { r.AllNodes = false },
		func(r *store.AlertRule) { r.Kind = store.KindOffline; r.TaskID = 0; r.Metric = ""; r.ForMinutes = 0 },
	} {
		r := base
```

替换为：

```go
		func(r *store.AlertRule) { r.AllNodes = false; r.NodeIDs = []int64{1} },
		func(r *store.AlertRule) { r.AllNodes = false },
		func(r *store.AlertRule) { *r = offline() },
		func(r *store.AlertRule) { *r = expiryRule(); r.DaysBefore = 1 },
		func(r *store.AlertRule) { *r = expiryRule(); r.DaysBefore = 365 },
	} {
		r := base
```

`cmd/hub/serve_alert_test.go` 原文：

```go
		})
	}
}
```

替换为：

```go
		})
	}
}

// hub 启动即做一次到期扫描，天界取 --timezone：UTC 16:30 在上海已是 9 月 25 日，9 月 24 日到期、开着按月自动续期的
// 节点推后到 10 月 24 日；按 UTC 算今天仍是 9 月 24 日，不会推后。"node expiry renewed" 只在推后的日期落库之后记。
func TestServeRenewsExpiryAtStartupInTheHubZone(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 9, 24, 16, 30, 0, 0, time.UTC))
	_, events, _ := startAlertHub(t, clk, func(st *store.Store) {
		id, err := st.CreateNode(t.Context(), "renewing", make([]byte, 32))
		if err != nil {
			t.Fatal(err)
		}
		b := store.Billing{Cycle: store.CycleMonthly, ExpiresOn: "2026-09-24", AutoRenew: true}
		if _, err := st.UpdateNode(t.Context(), id, store.NodeEdit{Name: "renewing", TrafficResetDay: 1, Billing: b}); err != nil {
			t.Fatal(err)
		}
	}, "--timezone", "Asia/Shanghai")
	deadline := time.NewTimer(testwait.Bound)
	defer deadline.Stop()
	for {
		select {
		case event := <-events:
			if string(event["msg"]) != `"node expiry renewed"` {
				continue
			}
			if from, to := string(event["from"]), string(event["to"]); from != `"2026-09-24"` || to != `"2026-10-24"` {
				t.Fatalf("renewed from %s to %s, want 2026-09-24 to 2026-10-24", from, to)
			}
			return
		case <-deadline.C:
			t.Fatal("the startup expiry sweep did not renew the node")
		}
	}
}
```

`cmd/hub/mux_test.go` 原文：

```go
	book := traffic.New(st, clk, time.UTC, slog.Default())
	reg := probe.New(st, slog.Default())
	alerts := alert.New(alert.Config{TTL: 30 * time.Second}, st, l, clk, slog.Default())
	notifier := alert.NewQueue(st, alerts.Channels, alert.NewHTTPClient(), "", clk, nil, slog.Default())
	alerts.SetSender(notifier)
```

替换为：

```go
	book := traffic.New(st, clk, time.UTC, slog.Default())
	reg := probe.New(st, slog.Default())
	alerts := alert.New(alert.Config{TTL: 30 * time.Second, Location: time.UTC}, st, l, clk, slog.Default())
	notifier := alert.NewQueue(st, alerts.Channels, alert.NewHTTPClient(), "", clk, nil, slog.Default())
	alerts.SetSender(notifier)
```

`internal/hub/api/api_test.go` 原文：

```go
	book := traffic.New(st, clk, time.UTC, slog.Default())
	reg := probe.New(st, slog.Default())
	alerts := alert.New(alert.Config{TTL: 30 * time.Second}, st, l, clk, slog.Default())
	notifier := alert.NewQueue(st, alerts.Channels, alert.NewHTTPClient(), "", clk, nil, slog.Default())
	in, err := ingest.New(ingest.Config{TTL: 30 * time.Second, TrustedProxies: prefixes}, l, st, a, book, reg, clk, slog.Default())
```

替换为：

```go
	book := traffic.New(st, clk, time.UTC, slog.Default())
	reg := probe.New(st, slog.Default())
	alerts := alert.New(alert.Config{TTL: 30 * time.Second, Location: time.UTC}, st, l, clk, slog.Default())
	notifier := alert.NewQueue(st, alerts.Channels, alert.NewHTTPClient(), "", clk, nil, slog.Default())
	in, err := ingest.New(ingest.Config{TTL: 30 * time.Second, TrustedProxies: prefixes}, l, st, a, book, reg, clk, slog.Default())
```

`internal/hub/api/alert_scope_reload_test.go` 原文：

```go
		t.Fatal(err)
	}
	e := alert.New(alert.Config{TTL: 30 * time.Second}, h.store, h.live, h.clk, slog.Default())
	if err := e.Load(t.Context()); err != nil {
		t.Fatal(err)
```

替换为：

```go
		t.Fatal(err)
	}
	e := alert.New(alert.Config{TTL: 30 * time.Second, Location: time.UTC}, h.store, h.live, h.clk, slog.Default())
	if err := e.Load(t.Context()); err != nil {
		t.Fatal(err)
```

- [ ] **Step 2: 跑红**

```bash
cd /Users/xjetry/work/vibe/probe-billing && go test -count=1 ./internal/hub/alert/ ./internal/hub/api/ ./cmd/hub/ > /tmp/billing-t3-red.log 2>&1; echo $?
```

Expected：1，三个包都编译失败。原文节选：
- `internal/hub/alert/engine_test.go:61:42: unknown field Location in struct literal of type Config`
- `internal/hub/alert/expiry_test.go:25:12: undefined: ParseDate`；`undefined: Today`、`undefined: DaysLeft`、`undefined: cycleMonths`、`undefined: renewedExpiry` 同样
- `cmd/hub/mux_test.go:50:58: unknown field Location in struct literal of type alert.Config`
- `internal/hub/api/alert_scope_reload_test.go:26:53: unknown field Location in struct literal of type alert.Config`

- [ ] **Step 3: 实现**

新建 `internal/hub/alert/expiry.go`（整份）：

```go
package alert

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xjetry/probe/internal/hub/store"
)

// 到期日按日历日计（§9.4）。日期一律表示为该日 UTC 零点的 time.Time：解析、"今天"与相减都在这种值上做，两个日期
// 相差几天与任何时区的夏令时无关。日期值里 hub 时区（--timezone）只经 Today 进入：把一个时刻换成那里的日历日；
// 下一个日界的时刻由 nextDayStart 按同一时区算（RunExpirySweep 传入 cfg.Location）。

// ParseDate 解析 YYYY-MM-DD。写侧（api 的 UpdateNode）与读侧（到期扫描、days_left）都用它，"合法日期"只有这一个
// 口径：time.Parse 按 time.DateOnly 要求四位年、两位月日，并拒绝不存在的日子（2026-02-30、非闰年的 02-29）。
func ParseDate(s string) (time.Time, error) {
	return time.Parse(time.DateOnly, s)
}

// Today 是 now 在 loc 里的日历日。
func Today(now time.Time, loc *time.Location) time.Time {
	y, m, d := now.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// daysBetween 用 Unix 秒相减：time.Time.Sub 相差约 292 年以上就饱和，ParseDate 接受的 0000–9999 年会算错。
func daysBetween(from, to time.Time) int {
	return int((to.Unix() - from.Unix()) / 86400)
}

// DaysLeft 是到期日减 today 的天数，负数是已过期天数；expiresOn 为空或不是合法日期时 ok 为假。
func DaysLeft(expiresOn string, today time.Time) (int, bool) {
	if expiresOn == "" {
		return 0, false
	}
	d, err := ParseDate(expiresOn)
	if err != nil {
		return 0, false
	}
	return daysBetween(today, d), true
}

// cycleMonths 是自动续期每次推后的月数。store.CycleNone 与表外的值（写入口会拒绝，只可能来自手改的库）不推后：
// 扫描自己守这一条，不依赖保存入口。
func cycleMonths(c store.BillingCycle) (int, bool) {
	switch c {
	case store.CycleMonthly:
		return 1, true
	case store.CycleQuarterly:
		return 3, true
	case store.CycleSemiannual:
		return 6, true
	case store.CycleYearly:
		return 12, true
	case store.CycleBiennial:
		return 24, true
	case store.CycleTriennial:
		return 36, true
	}
	return 0, false
}

// addMonths 把日期推后 n 个月；日号超过目标月的天数时钳到月末（1 月 31 日 + 1 个月 = 2 月 28 或 29 日）。
func addMonths(d time.Time, n int) time.Time {
	first := time.Date(d.Year(), d.Month()+time.Month(n), 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	return time.Date(first.Year(), first.Month(), min(d.Day(), last), 0, 0, 0, 0, time.UTC)
}

// renewedExpiry 是自动续期推后的到期日：到期日早于 today 就加一个周期，直到不早于 today。每一步从上一步的结果
// 推后，钳到月末之后日号停在钳后的值（1 月 31 日按月推后：2 月 28 日、3 月 28 日……），这是 §9.4 接受的漂移。
// ok 为假表示不推后：没开自动续期、没有可用的周期、没有到期日或不是合法日期、到期日不早于 today。
func renewedExpiry(b store.Billing, today time.Time) (string, bool) {
	if !b.AutoRenew {
		return "", false
	}
	months, ok := cycleMonths(b.Cycle)
	if !ok {
		return "", false
	}
	d, err := ParseDate(b.ExpiresOn)
	if err != nil || !d.Before(today) {
		return "", false
	}
	for d.Before(today) {
		d = addMonths(d, months)
	}
	return d.Format(time.DateOnly), true
}

// nextDayStart 是 now 之后 loc 里下一个日历日开始的时刻。一般就是 time.Date(y, m, d+1, 0, 0, 0, 0, loc)。
// 夏令时在零点开始的时区当天的零点不存在，go1.27.1 的 time.Date 对它的归一方向随 UTC 偏移的正负而异（它先把墙钟
// 读数当作 UTC 去查偏移，再按换算结果是否越出该时段复核）：
// 偏移为负的时区（America/Santiago、America/Havana）往回给前一天的 23:00（仍按旧偏移），本地日期没变；拿它定时会在旧的一天里
// 触发，之后每一轮算出的都是这个已经过去的时刻，定时器立即触发，循环空转到夏令时生效为止。这时新的一天从夏令时
// 生效的那一刻开始，也就是该时刻所在时段的结束处（ZoneBounds 的 end）。
// 偏移为正的时区（Africa/Cairo、Asia/Beirut）往前给新一天的 01:00（新偏移），本地日期已变，它本身就是新一天的第一个时刻，
// 走普通分支直接返回。判断只看本地日期有没有变，不看时分：按"不存在的零点会变成非零点的时刻"去判，会把偏移为正的
// 时区的日界定到几个月后夏令时结束的那次切换。
func nextDayStart(now time.Time, loc *time.Location) time.Time {
	y, m, d := now.In(loc).Date()
	next := time.Date(y, m, d+1, 0, 0, 0, 0, loc)
	if ny, nm, nd := next.Date(); ny == y && nm == m && nd == d {
		_, end := next.ZoneBounds()
		return end
	}
	return next
}

// nodeExpiry 是一个节点在本次扫描里的到期观测；valid 为假表示本轮不评估这个节点，已有状态原样保留：库里的到期日
// 读不懂，或者续期写回没有落定（写回出错，或条件更新发现快照之后这一行变了）。
type nodeExpiry struct {
	hasExpiry bool
	daysLeft  int
	valid     bool
}

// SweepExpiry 在 writeMu 下做一次到期扫描（sweepExpiry）。评估时机共四处（§9.2）：hub 启动与每个日历日开始
// （RunExpirySweep）、节点计费字段变化时调用它；保存启用的到期规则时，SaveRule 已持 writeMu，直接调 sweepExpiry。
// 一次扫描先把开着自动续期且早于今天的到期日推后并落库，再按推后之后的日期评估全部启用的到期规则。两步在同一次
// writeMu 下完成，续期带来的恢复与续期本身在同一次扫描里发生，不会先发一条"已过期"再发恢复。
func (e *Engine) SweepExpiry(ctx context.Context) error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	return e.sweepExpiry(ctx)
}

// sweepExpiry 的调用方持 writeMu。节点在开头读一次：之后才提交的计费修改（UpdateNode 不经 writeMu 写库）不在本次
// 快照里，本次可能按旧值多发一对转换；那次 UpdateNode 提交后自己调用 SweepExpiry，排在本次之后，按新值收敛。
// 续期写回经 RenewExpiry 的条件更新，不会盖掉快照之后的修改。
func (e *Engine) sweepExpiry(ctx context.Context) error {
	today := Today(e.clk.Now(), e.cfg.Location)
	nodes, err := e.st.ListNodes(ctx)
	if err != nil {
		return err
	}
	var errs []error
	// 续期写回没有落定的节点本轮不评估，见下面两处 skip 的说明。
	skip := map[int64]bool{}
	for i := range nodes {
		n := &nodes[i]
		to, ok := renewedExpiry(n.Billing, today)
		if !ok {
			continue
		}
		renewed, err := e.st.RenewExpiry(ctx, n.ID, n.Billing.Cycle, n.Billing.ExpiresOn, to)
		if err != nil {
			// 写回出错，本轮没有得到推后之后的日期。按未推后的快照评估，开着自动续期的节点会收到"已过期"，续期成功的
			// 下一轮又收到"到期日已更新"，这一对通知都是假的；所以跳过它，错误随扫描返回，下一轮重试续期。
			errs = append(errs, err)
			skip[n.ID] = true
			continue
		}
		if !renewed {
			// 条件更新没有写入，说明快照之后这一行变了：计费被改过，改它的 UpdateNode 提交后自己会再扫描一次、
			// 按新值收敛；或者节点已被删除，没有要评估的对象。两种情形都不该按过期的快照评估。
			skip[n.ID] = true
			continue
		}
		e.log.Info("node expiry renewed", "node_id", n.ID, "node", n.Name, "cycle", string(n.Billing.Cycle), "from", n.Billing.ExpiresOn, "to", to)
		n.Billing.ExpiresOn = to
	}
	observed := make(map[int64]nodeExpiry, len(nodes))
	for _, n := range nodes {
		o := nodeExpiry{valid: !skip[n.ID]}
		if o.valid && n.Billing.ExpiresOn != "" {
			d, err := ParseDate(n.Billing.ExpiresOn)
			if err != nil {
				// 写入口都校验过日期，只有手改的库会走到这里。跳过而不是按"没有到期日"处理：读不懂的值既不该触发，
				// 也不该把已触发的告警当作恢复发出去。
				e.log.Warn("node expires_on is not a YYYY-MM-DD date; expiry rules skip this node", "node_id", n.ID, "expires_on", n.Billing.ExpiresOn)
				o.valid = false
			} else {
				o.hasExpiry, o.daysLeft = true, daysBetween(today, d)
			}
		}
		observed[n.ID] = o
	}
	for _, r := range e.Rules() {
		if !r.Enabled || r.Kind != store.KindExpiry {
			continue
		}
		candidates := map[int64]bool{}
		for _, n := range nodes {
			if !inScope(r, n.ID) {
				continue
			}
			candidates[n.ID] = true
			ob := observed[n.ID]
			if !ob.valid {
				continue
			}
			o := ExpiryObservation{HasExpiry: ob.hasExpiry, DaysLeft: ob.daysLeft, DaysBefore: r.DaysBefore}
			cur := e.entry(stateKey{r.ID, n.ID})
			next, tr := NextExpiry(cur.state, o)
			summary, value := expirySummary(n, r, o, tr, cur.firedExpiresOn)
			// 进入 firing 的观测一定有到期日（NextExpiry 对没有到期日的观测只给 ok），记下的日期非空。
			fired := ""
			if next == store.StateFiring {
				fired = n.Billing.ExpiresOn
			}
			if err := e.apply(ctx, r, n.ID, next, fired, tr, summary, value); err != nil {
				errs = append(errs, err)
			}
		}
		if err := e.pruneCandidates(ctx, r.ID, candidates); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// expirySummary 是 §9.2 的到期文案；value 是剩余天数，因清除到期日而恢复时为 0。一条规则对一个节点只提醒一次：
// 触发文案按进入窗口时的剩余天数写"将于"或"已于"。此后留在 firing，NextExpiry 不给转换，apply 也跳过状态不变的
// 写，两处各自都挡住第二条。
// 恢复文案按离开窗口的原因。firedOn 是恢复之前那个 firing 状态记下的触发时到期日：当前到期日与它相同，日期没变，
// 写"已不在提醒窗口内"（规则的提前天数调小了；今天往回挪——hub 换了 --timezone、墙钟被往回调——也会走到这里，
// 这句同样成立）；
// 不同就是到期日改过，手动修改与自动续期都算。引擎只经触发转换进入 firing 并总是记下日期，firedOn 为空的 firing
// 只可能来自绕过引擎写的库，这时当前日期非空、与它不等，按到期日改过写。
func expirySummary(n store.Node, r store.AlertRule, o ExpiryObservation, tr *store.Transition, firedOn string) (string, float64) {
	recovered := tr != nil && *tr == store.TransitionRecovered
	switch {
	case recovered && !o.HasExpiry:
		return fmt.Sprintf("节点 %s 已清除到期日（规则 %s）", n.Name, r.Name), 0
	case recovered && n.Billing.ExpiresOn == firedOn:
		return fmt.Sprintf("节点 %s 已不在提醒窗口内（规则 %s）", n.Name, r.Name), float64(o.DaysLeft)
	case recovered:
		return fmt.Sprintf("节点 %s 到期日已更新为 %s（规则 %s）", n.Name, n.Billing.ExpiresOn, r.Name), float64(o.DaysLeft)
	case o.DaysLeft < 0:
		return fmt.Sprintf("节点 %s 已于 %s 到期（已过期 %d 天，规则 %s）", n.Name, n.Billing.ExpiresOn, -o.DaysLeft, r.Name), float64(o.DaysLeft)
	}
	return fmt.Sprintf("节点 %s 将于 %s 到期（剩 %d 天，规则 %s）", n.Name, n.Billing.ExpiresOn, o.DaysLeft, r.Name), float64(o.DaysLeft)
}

// RunExpirySweep 先扫描一次（hub 停机跨过的零点由这一次补上），此后在 hub 时区每个日历日开始时扫描一次。
//
// 下一次触发时刻由本轮扫描之前读到的钟算出，所以扫描期间跨过的日界至多引起一次立即的重扫，不会被跳过：next 是这次
// 读钟之后的第一个日界。扫描在它之前结束，定时器在它到来时触发；扫描拖过了它，定时器时长为负、立即触发，重扫按新的
// 一天评估。扫描自己若已按新的一天评估过，重扫只是重复：续期已经写回，状态没变时 apply 直接返回、不写库。反过来，
// 扫描之后才读钟，拖过日界的那一轮会从新的一天算出再下一个日界，新一天的续期与提醒就晚一整天。
//
// 定时器按单调钟走，时长在扫描之后按当时的墙钟算：墙钟被调整只影响当轮，不累积；提前触发只是多扫一次（按的仍是
// 前一天），下一轮再按真正的日界定时。
func (e *Engine) RunExpirySweep(ctx context.Context) {
	for {
		next := nextDayStart(e.clk.Now(), e.cfg.Location)
		if err := e.SweepExpiry(ctx); err != nil {
			e.log.Error("expiry sweep failed", "err", err)
		}
		timer := time.NewTimer(next.Sub(e.clk.Now()))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
```

`internal/hub/alert/state.go` 原文：

```go
	return store.StateFiring, &tr
}
```

替换为：

```go
	return store.StateFiring, &tr
}

// ExpiryObservation 是一个节点在一次到期扫描里的观测。HasExpiry 为假时 DaysLeft 无意义。
type ExpiryObservation struct {
	HasExpiry  bool
	DaysLeft   int // 到期日减今天（hub 时区的日历日），负数是已过期天数
	DaysBefore int // 规则的提前天数，1–365
}

// NextExpiry 没有 pending：到期是日历事件，进入窗口即触发，不存在"持续多久才算"。没有到期日按恢复处理——
// 清除到期日是结束提醒的正当方式，不能让已触发的告警卡在 firing。
func NextExpiry(cur store.AlertState, o ExpiryObservation) (store.AlertState, *store.Transition) {
	if !o.HasExpiry || o.DaysLeft > o.DaysBefore {
		return recovered(cur)
	}
	if cur == store.StateFiring {
		return cur, nil
	}
	tr := store.TransitionFiring
	return store.StateFiring, &tr
}
```

`internal/hub/alert/rule.go` 原文（1/3）：

```go
// CheckRule 只校验持久化结构；DeleteNode 可把显式作用域删空，空集仍是不覆盖节点的合法规则。
// Load 若丢弃这种规则，列表会不可见，而存储的渠道引用仍阻止删除，库与内存就会不一致。
func CheckRule(r store.AlertRule) error {
	if err := checkName(r.Name); err != nil {
```

替换为：

```go
// CheckRule 只校验持久化结构；DeleteNode 可把显式作用域删空，空集仍是不覆盖节点的合法规则。
// Load 若丢弃这种规则，列表会不可见，而存储的渠道引用仍阻止删除，库与内存就会不一致。
//
// 种类专用的字段只属于自己的种类，由 store.CheckKindFields 一处裁决；这里在已知种类上调它，把它的结果转成协议层
// 认得的字段错误。保存（Engine.SaveRule）与载入（Engine.Load）都经这里，所以"离线或到期规则带着探测字段"既存不
// 进去，也不会从手改过的库里载入生效；store.SaveAlertRule 自己也用同一个谓词拒绝。
func CheckRule(r store.AlertRule) error {
	if err := checkName(r.Name); err != nil {
```

`internal/hub/alert/rule.go` 原文（2/3）：

```go
	switch r.Kind {
	case store.KindOffline:
		return nil
	case store.KindProbe:
		if r.TaskID == 0 {
			return invalid("task_id", "must not be 0")
```

替换为：

```go
	switch r.Kind {
	case store.KindOffline:
		return checkKindFields(r)
	case store.KindExpiry:
		if r.DaysBefore < 1 || r.DaysBefore > 365 {
			return invalid("days_before", "must be between 1 and 365")
		}
		return checkKindFields(r)
	case store.KindProbe:
		if err := checkKindFields(r); err != nil {
			return err
		}
		if r.TaskID == 0 {
			return invalid("task_id", "must not be 0")
```

`internal/hub/alert/rule.go` 原文（3/3）：

```go
		return nil
	default:
		return oneOf("kind", string(r.Kind), string(store.KindOffline), string(store.KindProbe))
	}
}

```

替换为：

```go
		return nil
	default:
		return oneOf("kind", string(r.Kind), string(store.KindOffline), string(store.KindProbe), string(store.KindExpiry))
	}
}

// checkKindFields 把 store.CheckKindFields 的结果转成 FieldError，字段与约束原样沿用。
func checkKindFields(r store.AlertRule) error {
	var kf store.KindFieldError
	if err := store.CheckKindFields(r); errors.As(err, &kf) {
		return FieldError{Path: kf.Field, Constraint: kf.Constraint}
	} else if err != nil {
		return err
	}
	return nil
}

```

`internal/hub/alert/engine.go` 原文（1/11）：

```go
// 读方法只取 mu，apply 调用前已释放 mu，因此可以读取 Channels 等快照。
type Sender interface{ Enqueue(ev store.AlertEvent) }
type Config struct{ TTL time.Duration }
type stateKey struct{ rule, node int64 }
type stateEntry struct {
	state   store.AlertState
	sinceAt time.Time
}

```

替换为：

```go
// 读方法只取 mu，apply 调用前已释放 mu，因此可以读取 Channels 等快照。
type Sender interface{ Enqueue(ev store.AlertEvent) }
type Config struct {
	TTL time.Duration
	// Location 是 hub 的 --timezone：到期日按它的日历日计（§9.4），New 要求非 nil。
	Location *time.Location
}
type stateKey struct{ rule, node int64 }
type stateEntry struct {
	state   store.AlertState
	sinceAt time.Time
	// firedExpiresOn 与库里的 alert_state.fired_expires_on 同值（含义见 store.StateRow.FiredExpiresOn）：Load 从库读入，
	// apply 在写库成功后随状态一起发布。
	firedExpiresOn string
}

```

`internal/hub/alert/engine.go` 原文（2/11）：

```go
}

func New(cfg Config, st *store.Store, l *live.Live, clk clock.Clock, log *slog.Logger) *Engine {
	return &Engine{cfg: cfg, st: st, live: l, clk: clk, log: log, rules: map[int64]store.AlertRule{}, channels: map[int64]store.NotifyChannel{}, states: map[stateKey]stateEntry{}}
}
```

替换为：

```go
}

// New 对缺时区的 Config panic：到期扫描对 nil 时区调用 time.Time.In 会在运行中 panic，装配错误应当在启动时暴露。
func New(cfg Config, st *store.Store, l *live.Live, clk clock.Clock, log *slog.Logger) *Engine {
	if cfg.Location == nil {
		panic("alert.Config.Location must be set")
	}
	return &Engine{cfg: cfg, st: st, live: l, clk: clk, log: log, rules: map[int64]store.AlertRule{}, channels: map[int64]store.NotifyChannel{}, states: map[stateKey]stateEntry{}}
}
```

`internal/hub/alert/engine.go` 原文（3/11）：

```go
	for _, s := range states {
		if _, ok := validRules[s.RuleID]; ok {
			e.states[stateKey{s.RuleID, s.NodeID}] = stateEntry{s.State, s.SinceAt}
		}
	}
```

替换为：

```go
	for _, s := range states {
		if _, ok := validRules[s.RuleID]; ok {
			e.states[stateKey{s.RuleID, s.NodeID}] = stateEntry{s.State, s.SinceAt, s.FiredExpiresOn}
		}
	}
```

`internal/hub/alert/engine.go` 原文（4/11）：

```go
	var out []store.StateRow
	for k, s := range e.states {
		out = append(out, store.StateRow{RuleID: k.rule, NodeID: k.node, State: s.state, SinceAt: s.sinceAt})
	}
	sort.Slice(out, func(i, j int) bool {
```

替换为：

```go
	var out []store.StateRow
	for k, s := range e.states {
		out = append(out, store.StateRow{RuleID: k.rule, NodeID: k.node, State: s.state, SinceAt: s.sinceAt, FiredExpiresOn: s.firedExpiresOn})
	}
	sort.Slice(out, func(i, j int) bool {
```

`internal/hub/alert/engine.go` 原文（5/11）：

```go
		return store.AlertRule{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	previous := e.rules[saved.ID]
	// 种类、任务或指标变化后，旧观测不再描述当前规则，与 SaveAlertRule 的状态裁剪一致。
	// threshold 与 for_minutes 不改变身份，状态沿用，下一轮按新阈值判断是否恢复。
	identityChanged := previous.Kind != saved.Kind || previous.TaskID != saved.TaskID || previous.Metric != saved.Metric
	e.rules[saved.ID] = cloneRule(saved)
```

替换为：

```go
		return store.AlertRule{}, err
	}
	e.publishRule(saved)
	// 除此之外，到期规则只在启动、日界与计费变化时评估：若不在这里评估一次，新建、启用或改了提前天数的规则要等到
	// 下一个日界才有状态。规则已提交，扫描失败只记日志：保存本身成功了，下一次扫描会再评估。
	if saved.Enabled && saved.Kind == store.KindExpiry {
		if err := e.sweepExpiry(context.WithoutCancel(ctx)); err != nil {
			e.log.Error("expiry sweep after saving rule failed", "rule_id", saved.ID, "err", err)
		}
	}
	return saved, nil
}

// publishRule 在 SaveAlertRule 提交之后把规则与状态裁剪同步到内存；调用方持 writeMu。
func (e *Engine) publishRule(saved store.AlertRule) {
	e.mu.Lock()
	defer e.mu.Unlock()
	previous := e.rules[saved.ID]
	// 种类、任务或指标变化后，旧观测不再描述当前规则，与 SaveAlertRule 的状态裁剪一致。
	// threshold、for_minutes 与 days_before 不改变身份，状态沿用，下一轮按新值判断是否恢复。
	identityChanged := previous.Kind != saved.Kind || previous.TaskID != saved.TaskID || previous.Metric != saved.Metric
	e.rules[saved.ID] = cloneRule(saved)
```

`internal/hub/alert/engine.go` 原文（6/11）：

```go
		}
	}
	return saved, nil
}
func (e *Engine) DeleteRule(ctx context.Context, id int64) error {
```

替换为：

```go
		}
	}
}
func (e *Engine) DeleteRule(ctx context.Context, id int64) error {
```

`internal/hub/alert/engine.go` 原文（7/11）：

```go
	}
}
func (e *Engine) current(k stateKey) store.AlertState {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if s, ok := e.states[k]; ok {
		return s.state
	}
	return store.StateOK
}

// 调用方持 writeMu；状态、事件与投递先由 store 原子提交，再发布内存并通知 Sender。
func (e *Engine) apply(ctx context.Context, r store.AlertRule, nodeID int64, next store.AlertState, tr *store.Transition, summary string, value float64) error {
	k := stateKey{r.ID, nodeID}
	if e.current(k) == next {
```

替换为：

```go
	}
}
func (e *Engine) current(k stateKey) store.AlertState { return e.entry(k).state }

// entry 是 k 的内存状态；没有状态的一对规则与节点按 ok 处理。
func (e *Engine) entry(k stateKey) stateEntry {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if s, ok := e.states[k]; ok {
		return s
	}
	return stateEntry{state: store.StateOK}
}

// 调用方持 writeMu；状态、事件与投递先由 store 原子提交，再发布内存并通知 Sender。
// firedExpiresOn 只由到期扫描在进入 firing 时给出，其余调用传空。状态不变时 apply 直接返回、不写库，所以一个 firing
// 状态记的始终是它进入 firing 那一刻的到期日。
func (e *Engine) apply(ctx context.Context, r store.AlertRule, nodeID int64, next store.AlertState, firedExpiresOn string, tr *store.Transition, summary string, value float64) error {
	k := stateKey{r.ID, nodeID}
	if e.current(k) == next {
```

`internal/hub/alert/engine.go` 原文（8/11）：

```go
	var err error
	if tr != nil {
		ev, err = e.st.RecordTransition(ctx, r.ID, nodeID, next, "", store.AlertEvent{Transition: *tr, At: now, Summary: summary, Value: value}, r.ChannelIDs)
	} else {
		err = e.st.SetAlertState(ctx, r.ID, nodeID, next, now)
	}
```

替换为：

```go
	var err error
	if tr != nil {
		ev, err = e.st.RecordTransition(ctx, r.ID, nodeID, next, firedExpiresOn, store.AlertEvent{Transition: *tr, At: now, Summary: summary, Value: value}, r.ChannelIDs)
	} else {
		// SetAlertState 不写触发日期，内存与库记同一个值。
		firedExpiresOn = ""
		err = e.st.SetAlertState(ctx, r.ID, nodeID, next, now)
	}
```

`internal/hub/alert/engine.go` 原文（9/11）：

```go
	}
	e.mu.Lock()
	e.states[k] = stateEntry{next, time.Unix(now.Unix(), 0).UTC()}
	sender := e.sender
	e.mu.Unlock()
```

替换为：

```go
	}
	e.mu.Lock()
	e.states[k] = stateEntry{next, time.Unix(now.Unix(), 0).UTC(), firedExpiresOn}
	sender := e.sender
	e.mu.Unlock()
```

`internal/hub/alert/engine.go` 原文（10/11）：

```go
				summary = fmt.Sprintf("节点 %s 已恢复上报（规则 %s）", node.Name, r.Name)
			}
			if err := e.apply(ctx, r, node.ID, next, tr, summary, unseen.Seconds()); err != nil {
				errs = append(errs, err)
			}
```

替换为：

```go
				summary = fmt.Sprintf("节点 %s 已恢复上报（规则 %s）", node.Name, r.Name)
			}
			if err := e.apply(ctx, r, node.ID, next, "", tr, summary, unseen.Seconds()); err != nil {
				errs = append(errs, err)
			}
```

`internal/hub/alert/engine.go` 原文（11/11）：

```go
			value := samples[len(samples)-1].Value
			summary := fmt.Sprintf("节点 %s 规则 %s：%s %.1f", node.Name, r.Name, r.Metric, value)
			if err := e.apply(ctx, r, node.ID, next, tr, summary, value); err != nil {
				errs = append(errs, err)
			}
```

替换为：

```go
			value := samples[len(samples)-1].Value
			summary := fmt.Sprintf("节点 %s 规则 %s：%s %.1f", node.Name, r.Name, r.Metric, value)
			if err := e.apply(ctx, r, node.ID, next, "", tr, summary, value); err != nil {
				errs = append(errs, err)
			}
```

`cmd/hub/serve.go` 原文（1/3）：

```go
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	db := fs.String("db", "probe.db", "SQLite database path")
	tz := fs.String("timezone", "", "IANA time zone for traffic period boundaries (default: the host's zone, resolved from TZ or /etc/localtime; UTC if neither resolves); already-persisted period starts are interpreted in the new zone; usage of the current period may be reset at the next read, report or flush")
	listen := fs.String("listen", "127.0.0.1:8080", "listen address")
	proxies := fs.String("trusted-proxies", "", "comma-separated CIDRs whose X-Forwarded-For / X-Forwarded-Proto are trusted; empty trusts none. Behind a reverse proxy, list the proxy here: the public page and agent registration are rate-limited per source (one IPv4 address, or one IPv6 /64), and failed logins are locked out per source, so without it every visitor shares the proxy address's single bucket and lockout")
```

替换为：

```go
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	db := fs.String("db", "probe.db", "SQLite database path")
	tz := fs.String("timezone", "", "IANA time zone for traffic period boundaries and node expiry days (default: the host's zone, resolved from TZ or /etc/localtime; UTC if neither resolves); already-persisted period starts are interpreted in the new zone; usage of the current period may be reset at the next read, report or flush")
	listen := fs.String("listen", "127.0.0.1:8080", "listen address")
	proxies := fs.String("trusted-proxies", "", "comma-separated CIDRs whose X-Forwarded-For / X-Forwarded-Proto are trusted; empty trusts none. Behind a reverse proxy, list the proxy here: the public page and agent registration are rate-limited per source (one IPv4 address, or one IPv6 /64), and failed logins are locked out per source, so without it every visitor shares the proxy address's single bucket and lockout")
```

`cmd/hub/serve.go` 原文（2/3）：

```go
	book := traffic.New(st, clk, loc, log)
	reg := probe.New(st, log)
	alerts := alert.New(alert.Config{TTL: ttl}, st, l, clk, log)
	notifier := alert.NewQueue(st, alerts.Channels, alert.NewHTTPClient(), "", clk, nil, log)
	alerts.SetSender(notifier)
```

替换为：

```go
	book := traffic.New(st, clk, loc, log)
	reg := probe.New(st, log)
	alerts := alert.New(alert.Config{TTL: ttl, Location: loc}, st, l, clk, log)
	notifier := alert.NewQueue(st, alerts.Channels, alert.NewHTTPClient(), "", clk, nil, log)
	alerts.SetSender(notifier)
```

`cmd/hub/serve.go` 原文（3/3）：

```go
	stopSweep := startLoop(alerts.RunOfflineSweep)
	defer startLoop(alerts.RunProbeEvaluation)()
	defer startLoop(notifier.Run)()

```

替换为：

```go
	stopSweep := startLoop(alerts.RunOfflineSweep)
	defer startLoop(alerts.RunProbeEvaluation)()
	defer startLoop(alerts.RunExpirySweep)()
	defer startLoop(notifier.Run)()

```

- [ ] **Step 4: 跑绿**

```bash
cd /Users/xjetry/work/vibe/probe-billing && go test -count=1 ./internal/hub/alert/ ./internal/hub/api/ ./cmd/hub/ > /tmp/billing-t3-green-t3.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && go vet ./... > /tmp/billing-t3-vet-t3.log 2>&1; echo $?
```

Expected：两条都是 0。`TestServeRenewsExpiryAtStartupInTheHubZone` 从 `runServeWith` 起 hub：时钟 UTC 2026-09-24 16:30、`--timezone Asia/Shanghai`，日志里有 `from=2026-09-24 to=2026-10-24` 的续期行。

- [ ] **Step 5: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-billing && git add cmd/hub/mux_test.go cmd/hub/serve.go cmd/hub/serve_alert_test.go internal/hub/alert/engine.go internal/hub/alert/engine_test.go internal/hub/alert/expiry.go internal/hub/alert/expiry_test.go internal/hub/alert/rule.go internal/hub/alert/rule_test.go internal/hub/alert/state.go internal/hub/api/alert_scope_reload_test.go internal/hub/api/api_test.go > /tmp/billing-t3-add.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && git commit -m "alert: 到期规则、自动续期与按 hub 时区日界的到期扫描" -m "日期一律表示为该日 UTC 零点的 time.Time，天数用 Unix 秒相减：time.Time.Sub 在约 292 年外饱和，而合法日期覆盖 0000 到 9999 年。hub 时区只在取今天时出现一次。夏令时从零点开始的时区里，下一个日界取该时段的结束处：go1.27.1 的 time.Date 对不存在的零点给出前一天 23:00，拿它定时会在旧的一天里空转到夏令时生效。一次扫描在 writeMu 下先推后自动续期、再评估全部启用的到期规则，续期带来的恢复与续期本身在同一次扫描里；续期写回没有落定的节点本轮不评估：条件更新没有写入说明快照之后这一行变了，计费被改过时由那次 UpdateNode 自己的扫描收敛，节点被删除时没有要评估的对象；写回出错时错误随扫描返回，下一轮重试。时机是启动、日界、计费变化与保存启用的到期规则，最后这一处让新规则不必等到零点才有状态。日界循环在扫描之前读钟、按这次读数定下一次触发，扫描拖过零点时立即再扫一次，新一天的扫描不会被推到次日零点。CheckRule 经 store.CheckKindFields 让探测四项只属于探测规则、days_before 只属于到期规则；离线规则带探测字段从此被拒绝，不再由存储静默清零。库里读不懂的到期日跳过评估、保留已有状态。一条规则对一个节点只提醒一次，留在 firing 不再发第二条。恢复文案按离开窗口的原因选：清空、到期日改过、日期没变（通常是提前天数调小了）三种；判断日期改没改靠状态里记下的触发时到期日，它随状态由 Load 读回，重启之后同样认得出。" > /tmp/billing-t3-commit.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && git status --porcelain > /tmp/billing-t3-status.log 2>&1; echo $?
```

Expected：三条都是 0；`billing-t3-status.log` 为空。

- [ ] **Step 6: make ci**

```bash
cd /Users/xjetry/work/vibe/probe-billing && make ci > /tmp/billing-t3-ci.log 2>&1; echo $?
```

Expected：0；vitest `Tests  369 passed (369)`。`make ci` 最后核对生成物已入库，所以放在提交之后。

- [ ] **Step 7: 缺陷注入**

| | 改动 | 命令 | 期望 |
|---|---|---|---|
| a | `nextDayStart` 去掉"日期没变就取 `ZoneBounds` 的 end"那段 | `go test -count=1 -run 'TestNextDayStart' ./internal/hub/alert/ > /tmp/billing-t3-inj-a.log 2>&1; echo $?` | 1，`expiry_test.go:185: America/Santiago from 2026-09-05T22:00:00: next day starts 2026-09-05T23:00:00-04:00, want 2026-09-06T01:00:00-03:00`；Havana 同样，两个时区各另有一行 `is not the first instant of the next local day` |
| b | `daysBetween` 改用 `to.Sub(from) / (24 * time.Hour)` | `go test -count=1 -run 'TestDaysLeft' ./internal/hub/alert/ > /tmp/billing-t3-inj-b.log 2>&1; echo $?` | 1，`expiry_test.go:89: DaysLeft(9999-12-31) = 106751 true, want 2912173` 与 `DaysLeft(0000-01-01) = -106751 true, want -740251` |
| c | `renewedExpiry` 每步从原到期日加 k 个周期（不累积钳位漂移） | `go test -count=1 -run 'TestRenewedExpiry' ./internal/hub/alert/ > /tmp/billing-t3-inj-c.log 2>&1; echo $?` | 1，`expiry_test.go:138: several months, clamped then drifting: renewedExpiry = "2026-04-30" true, want "2026-04-28"`；from year 0 同样 |
| d | `addMonths` 不钳到月末（`min(d.Day(), last)` 改成 `d.Day()+0*last`） | `go test -count=1 -run 'TestRenewedExpiry' ./internal/hub/alert/ > /tmp/billing-t3-inj-d.log 2>&1; echo $?` | 1，`expiry_test.go:138: several months, clamped then drifting: renewedExpiry = "2026-04-03" …`、`leap February: … "2028-03-02" … want "2028-02-29"`、`yearly from February 29: … "2025-03-01" …` |
| e | `renewedExpiry` 去掉 `!b.AutoRenew` 的守卫 | `go test -count=1 -run 'TestRenewedExpiry' ./internal/hub/alert/ > /tmp/billing-t3-inj-e.log 2>&1; echo $?` | 1，`expiry_test.go:154: auto renew off: renewed to 2026-10-10` |
| f | `cycleMonths` 的季度返回 4 | `go test -count=1 -run 'TestCycleMonths' ./internal/hub/alert/ > /tmp/billing-t3-inj-f.log 2>&1; echo $?` | 1，`expiry_test.go:108: cycleMonths(quarterly) = 4 true, want 3` |
| g | `Today` 取 UTC 的日历日（`now.In(loc)` 改成 `now.UTC()`） | `go test -count=1 -run 'TestToday\|TestSweepExpiryCountsDaysInTheHubZone' ./internal/hub/alert/ > /tmp/billing-t3-inj-g.log 2>&1; echo $?` | 1，`expiry_test.go:68: Today in UTC+8 = 2026-09-24, want 2026-09-25` 与 `expiry_test.go:276: node 1 state="" want "firing"` |
| h | `NextExpiry` 的 `o.DaysLeft > o.DaysBefore` 改成 `>=` | `go test -count=1 -run 'TestNextExpiry' ./internal/hub/alert/ > /tmp/billing-t3-inj-h.log 2>&1; echo $?` | 1，`expiry_test.go:214: enters the window on its edge: NextExpiry = ok <nil>, want firing …` |
| i | 续期落库之后不更新快照里的到期日（删掉 `n.Billing.ExpiresOn = to`） | `go test -count=1 -run 'TestSweepExpiryRenewsBeforeEvaluating' ./internal/hub/alert/ > /tmp/billing-t3-inj-i.log 2>&1; echo $?` | 1，`expiry_test.go:296: events after renewal: [{… Transition:firing … Summary:节点 node1 已于 2026-09-20 到期（已过期 4 天，规则 到期） …}]`：只有触发，没有随续期的恢复 |
| j | `SaveRule` 不做保存后的到期扫描 | `go test -count=1 -run 'TestSaveRuleEvaluatesEnabledExpiryRules' ./internal/hub/alert/ > /tmp/billing-t3-inj-j.log 2>&1; echo $?` | 1，`expiry_test.go:386: node 1 state="" want "firing"` |
| k | 读不懂的到期日不跳过（删掉 `if !ob.valid { continue }`） | `go test -count=1 -run 'TestSweepExpirySkipsUnreadableDates' ./internal/hub/alert/ > /tmp/billing-t3-inj-k.log 2>&1; echo $?` | 1，`expiry_test.go:331: node 1 state="ok" want "firing"` |
| l | `sweepExpiry` 不调 `pruneCandidates` | `go test -count=1 -run 'TestSweepExpiryDropsStatesOfNodesDeletedElsewhere' ./internal/hub/alert/ > /tmp/billing-t3-inj-l.log 2>&1; echo $?` | 1，`expiry_test.go:374: node 1 state="firing" want ""` |
| m | `CheckRule` 的离线分支不调 `checkKindFields`（直接 `return nil`） | `go test -count=1 -run 'TestCheckRule' ./internal/hub/alert/ > /tmp/billing-t3-inj-m.log 2>&1; echo $?` | 1，`rule_test.go:55: error=<nil> want ErrInvalid and task_id`，metric、threshold（两条，含 NaN）、for_minutes、days_before（`TestCheckRule/offline_days_before`）各一行 |
| n | 到期规则的下限从 1 放到 0 | `go test -count=1 -run 'TestCheckRule' ./internal/hub/alert/ > /tmp/billing-t3-inj-n.log 2>&1; echo $?` | 1，`rule_test.go:55: error=<nil> want ErrInvalid and days_before`（`expiry_days_low`） |
| o | `CheckRule` 的探测分支不调 `checkKindFields` | `go test -count=1 -run 'TestCheckRule' ./internal/hub/alert/ > /tmp/billing-t3-inj-o.log 2>&1; echo $?` | 1，同一句（`probe_days_before`） |
| p | 扫描不跳过停用的规则 | `go test -count=1 -run 'TestSweepExpiryHonoursScopeAndEnabled' ./internal/hub/alert/ > /tmp/billing-t3-inj-p.log 2>&1; echo $?` | 1，`expiry_test.go:359: node 1 state="firing" want ""` |
| q | `alert.New` 不检查 `Location` | `go test -count=1 -run 'TestNewRequiresLocation' ./internal/hub/alert/ > /tmp/billing-t3-inj-q.log 2>&1; echo $?` | 1，`expiry_test.go:637: panic = <nil>` |
| r | 清除到期日的恢复文案用"到期日已更新为"那句 | `go test -count=1 -run 'TestSweepExpiryFiresAndRecoversWithSpecSummaries' ./internal/hub/alert/ > /tmp/billing-t3-inj-r.log 2>&1; echo $?` | 1，`expiry_test.go:259: expires_on "": event {… Summary:节点 node1 到期日已更新为 （规则 到期） Value:0 …}, want summary "节点 node1 已清除到期日（规则 到期）" value 0` |
| s | `RunExpirySweep` 先等到日界再扫（启动时不扫） | `go test -count=1 -run 'TestRunExpirySweepSweepsOnStartAndStops' ./internal/hub/alert/ > /tmp/billing-t3-inj-s.log 2>&1; echo $?` | 1，`expiry_test.go:460: expires_on = 2026-09-01, want 2026-10-01 after the startup sweep` |
| t | `serve.go` 不启动 `RunExpirySweep` | `go test -count=1 -run 'TestServeRenewsExpiryAtStartupInTheHubZone' ./cmd/hub/ > /tmp/billing-t3-inj-t.log 2>&1; echo $?` | 1，`serve_alert_test.go:257: the startup expiry sweep did not renew the node` |
| u | `serve.go` 给告警引擎传 `time.UTC` 而不是 `--timezone` | `go test -count=1 -run 'TestServeRenewsExpiryAtStartupInTheHubZone' ./cmd/hub/ > /tmp/billing-t3-inj-u.log 2>&1; echo $?` | 1，同一句：按 UTC 今天还是 2026-09-24，到期日不早于今天，不推后 |
| v | `Load` 不经 `CheckRule`（删掉跳过非法规则那段） | `go test -count=1 -run 'TestLoadChecksExpiryRules' ./internal/hub/alert/ > /tmp/billing-t3-inj-v.log 2>&1; echo $?` | 1，`expiry_test.go:627: loaded rules = [{… DaysBefore:400 …} {… DaysBefore:7 …}], want only rule 2 with days_before 7` |
| w | `expirySummary` 删掉"日期没变"那一支 | `go test -count=1 -run 'TestSweepExpiryRecoverySummaryFollowsTheReason' ./internal/hub/alert/ > /tmp/billing-t3-inj-w.log 2>&1; echo $?` | 1，`expiry_test.go:426: 2026-09-27 with days_before 2: events [{… Transition:recovered … Summary:节点 node1 到期日已更新为 2026-09-27（规则 到期） Value:3 …} …], want one new event "节点 node1 已不在提醒窗口内（规则 到期）" value 3`：第一步就红 |
| x | `Load` 不读回触发日期（`s.FiredExpiresOn` 换成 `""`） | `go test -count=1 -run 'TestSweepExpiryRecoverySummaryFollowsTheReason' ./internal/hub/alert/ > /tmp/billing-t3-inj-x.log 2>&1; echo $?` | 1，同一句，红在第一步：那一步之前重启过，触发日期要从库里读回 |
| y | `apply` 发布到内存时丢掉触发日期（库里照写） | `go test -count=1 -run 'TestSweepExpiryRecoverySummaryFollowsTheReason' ./internal/hub/alert/ > /tmp/billing-t3-inj-y.log 2>&1; echo $?` | 1，同一句，红在第三步（事件 ID 3、4）：那一步没有重启，触发日期来自内存；第一步从库里读回，照常通过 |
| z | `apply` 去掉"状态不变直接返回"（留在 firing 的扫描经 `SetAlertState` 重写状态行） | `go test -count=1 -run 'TestSweepExpiryRecoverySummaryFollowsTheReason' ./internal/hub/alert/ > /tmp/billing-t3-inj-z.log 2>&1; echo $?` | 1，同一句，红在第一步：保存规则之前那次留在 firing 的扫描经 `SetAlertState` 重写了状态行，触发日期被清空 |
| aa | 到期当天写成"已于"（`o.DaysLeft < 0` 改成 `<= 0`） | `go test -count=1 -run 'TestSweepExpiryNotifiesOncePerEntry' ./internal/hub/alert/ > /tmp/billing-t3-inj-aa.log 2>&1; echo $?` | 1，`expiry_test.go:449: summaries over four days = [… "节点 node2 已于 2026-09-24 到期（已过期 0 天，规则 到期）"], want [… "节点 node2 将于 2026-09-24 到期（剩 0 天，规则 到期）"]` |
| ab | `NextExpiry` 对留在 firing 也报触发转换，且 `apply` 去掉"状态不变直接返回" | `go test -count=1 -run 'TestSweepExpiryNotifiesOncePerEntry' ./internal/hub/alert/ > /tmp/billing-t3-inj-ab.log 2>&1; echo $?` | 1，`expiry_test.go:449: summaries over four days = [ …（剩 0 天… …（剩 1 天… …（已过期 1 天… …（已过期 2 天… …]`：每次扫描都发一条 |
| ac | 读不懂的到期日不记 Warn（删掉那行 `e.log.Warn`） | `go test -count=1 -run 'TestSweepExpirySkipsUnreadableDates' ./internal/hub/alert/ > /tmp/billing-t3-inj-ac.log 2>&1; echo $?` | 1，`expiry_test.go:342: 0 lines of "level=WARN msg=\"node expires_on is not a YYYY-MM-DD date; expiry rules skip this node\" node_id=1 expires_on=2026-09-31", want 2`：两次扫描一行都没有 |
| ad | 只去掉 `NextExpiry` 对留在 firing 的那一道（`apply` 的一道仍在） | `go test -count=1 -run 'TestSweepExpiryNotifiesOncePerEntry' ./internal/hub/alert/ > /tmp/billing-t3-inj-ad.log 2>&1; echo $?` | 0（预期不红，见下） |
| ae | 与 z 同一改动（`apply` 去掉"状态不变直接返回"），只跑只提醒一次的用例 | `go test -count=1 -run 'TestSweepExpiryNotifiesOncePerEntry' ./internal/hub/alert/ > /tmp/billing-t3-inj-ae.log 2>&1; echo $?` | 0（预期不红，见下） |
| af | 条件更新落空时照常评估（删掉 `if !renewed` 分支里的 `skip[n.ID] = true`） | `go test -count=1 -run 'TestSweepExpirySkipsANodeWhoseSnapshotIsStale' ./internal/hub/alert/ > /tmp/billing-t3-inj-af.log 2>&1; echo $?` | 1，`expiry_test.go:484: a node with a stale snapshot was evaluated: [{… Transition:firing … Summary:节点 node1 已于 2026-09-20 到期（已过期 4 天，规则 到期） …}]` |
| ag | `RunExpirySweep` 只在启动时扫一次，之后只等 `ctx` 结束 | `go test -count=1 -run 'TestRunExpirySweep' ./internal/hub/alert/ > /tmp/billing-t3-inj-ag.log 2>&1; echo $?` | 1，两个日界用例都红：`expiry_test.go:546: expires_on = 2026-09-24, want 2026-10-24 after the sweep at the day boundary`；跨越用例的两例（零点前读 1 次、2 次）都是 `expiry_test.go:601: expires_on = 2026-09-24, want 2026-10-24: the day boundary crossed during the sweep was skipped`（每处都等满 testwait 的时限后才红） |
| ah | `Load` 跳过非法规则时不记 Warn（删掉那行 `e.log.Warn`） | `go test -count=1 -run 'TestLoadChecksExpiryRules' ./internal/hub/alert/ > /tmp/billing-t3-inj-ah.log 2>&1; echo $?` | 1，`expiry_test.go:630: want one line "level=WARN msg=\"invalid alert rule skipped\" rule_id=1 err=\"invalid: days_before must be between 1 and 365\"" in:` |
| ai | 读不懂的到期日每个节点只记一次 Warn（包级的 `sync.Map` 去重） | `go test -count=1 -run 'TestSweepExpirySkipsUnreadableDates' ./internal/hub/alert/ > /tmp/billing-t3-inj-ai.log 2>&1; echo $?` | 1，`expiry_test.go:342: 1 lines of "level=WARN msg=\"node expires_on is not a YYYY-MM-DD date; expiry rules skip this node\" node_id=1 expires_on=2026-09-31", want 2` |
| aj | `RunExpirySweep` 扫描之后才读钟（改回先 `SweepExpiry`、再 `now := e.clk.Now()`、按 `nextDayStart(now, …).Sub(now)` 定时） | `go test -count=1 -run 'TestRunExpirySweep' ./internal/hub/alert/ > /tmp/billing-t3-inj-aj.log 2>&1; echo $?` | 1，只有 `TestRunExpirySweepDoesNotSkipADayBoundaryCrossedDuringASweep/1_reads_before_midnight` 红：`expiry_test.go:601: expires_on = 2026-09-24, want 2026-10-24: the day boundary crossed during the sweep was skipped`（等满 testwait 的时限后才红）。扫描拿到零点前的读数、不推后，循环拿到零点后的读数、定到 26 日零点。`2_reads_before_midnight` 照常通过：循环与扫描都在零点前，扫描之后的读数在零点后，按它定到 25 日零点、立即重扫。另两个循环用例也照常通过：`tickingClock` 用例的启动扫描在零点前就结束，读钟顺序对它没有影响 |
| ak | 定时器时长为负时不立即触发，改等再下一个日界（`d < 0` 时 `d = nextDayStart(now, …).Sub(now)`） | `go test -count=1 -run 'TestRunExpirySweep' ./internal/hub/alert/ > /tmp/billing-t3-inj-ak.log 2>&1; echo $?` | 1，只有 `TestRunExpirySweepDoesNotSkipADayBoundaryCrossedDuringASweep/2_reads_before_midnight` 红：`expiry_test.go:601: expires_on = 2026-09-24, want 2026-10-24: the day boundary crossed during the sweep was skipped`（等满 testwait 的时限后才红）。扫描按 24 日评估不推后，结束时已过零点，负时长被改成等到 26 日零点。`1_reads_before_midnight` 照常通过：扫描自己读到 25 日就推后了 |
| al | 续期写回出错时照常评估（删掉 `err != nil` 分支里的 `skip[n.ID] = true`） | `go test -count=1 -run 'TestSweepExpirySkipsANodeWhoseRenewalFailed' ./internal/hub/alert/ > /tmp/billing-t3-inj-al.log 2>&1; echo $?` | 1，`expiry_test.go:513: a node whose renewal failed was evaluated: [{… Transition:firing … Summary:节点 node1 已于 2026-09-20 到期（已过期 4 天，规则 到期） …}]` |
| am | `Load` 不载入到期规则的状态（`if _, ok := validRules[s.RuleID]; ok {` 改成 `if r, ok := validRules[s.RuleID]; ok && r.Kind != store.KindExpiry {`） | `go test -count=1 -run 'TestSweepExpiryRecoverySummaryFollowsTheReason' ./internal/hub/alert/ > /tmp/billing-t3-inj-am.log 2>&1; echo $?` | 1，`expiry_test.go:426: 2026-09-27 with days_before 2: events [{ID:3 … 已不在提醒窗口内 …} {ID:2 … Transition:firing … 将于 2026-09-27 到期（剩 3 天，规则 到期） …} {ID:1 …}], want one new event "节点 node1 已不在提醒窗口内（规则 到期）" value 3`：红在第一步。重启后没有读回 firing，扫描把节点当成刚进窗口，又发了一条触发（事件 2） |
| an | `nextDayStart` 按时分判断：`if ny, nm, nd := next.Date(); ny == y && nm == m && nd == d {` 改成 `if next.Hour() != 0 \|\| next.Minute() != 0 {` | `go test -count=1 -run 'TestNextDayStart' ./internal/hub/alert/ > /tmp/billing-t3-inj-an.log 2>&1; echo $?` | 1，只有偏移为正的两行红：`expiry_test.go:185: Africa/Cairo from 2026-04-23T22:00:00: next day starts 2026-10-29T23:00:00+02:00, want 2026-04-24T01:00:00+03:00`，Beirut 同样（`2026-10-24T23:00:00+02:00`），两个时区各另有一行 `is not the first instant of the next local day`。01:00 的时分不为零，取了夏令时时段的结束处，日界定到几个月后夏令时结束的那次切换；Santiago、Havana 的 23:00 同样不为零，照旧取对 |

每项：改动 → `git diff --stat` 非空 → 跑命令 → 核对原因 → `git checkout -- internal/hub/alert cmd/hub`。s、ag、aj、ak 等满 `testwait.Bound`（30 秒）才红，ag 有三处各等一次。

ad 与 ae 预期不红，列在这里是为核对 `expirySummary` 注释里"两处各自都挡住第二条"：只去掉 `NextExpiry` 那一道（ad）时，`apply` 的"状态不变直接返回"仍拦住；只去掉 `apply` 那一道（ae）时，`NextExpiry` 对留在 firing 不给转换，仍然只有一条。两道都去掉（ab）才重复提醒。ae 与 z 是同一改动：它不让提醒重复，却让留在 firing 的扫描经 `SetAlertState` 清掉触发日期，z 红在的就是这一点。

---

### Task 4: api：UpdateNode 校验并保存计费子消息，days_left 按 hub 时区下发，到期规则经协议保存

**Files:**
- Modify: `internal/hub/api/nodes.go`（`billingCycles`、`pricePattern`、`currencyPattern`、`billingOf`、`billingProto`、`nodeProto(n, today)`；`ListNodes`、`CreateNode`、`UpdateNode`：`nodeMu` 只罩住库写入与 `SetResetDay`，放锁之后扫描、再回读）
- Modify: `internal/hub/api/service.go`（`Config.Location` 与 `New` 的检查；`today()`）
- Modify: `internal/hub/api/public.go`（`PublicConfig.Location` 与 `NewPublic` 的检查；`GetSnapshot` 经投影填 `billing`）
- Modify: `internal/hub/api/alerts.go`（`alertKinds` 加到期；`ruleProto`、`SaveAlertRule` 带 `days_before`；指标一段的注释）
- Modify: `cmd/hub/serve.go`（`api.Config`、`api.PublicConfig` 带 `loc`）
- Create: `internal/hub/api/billing_test.go`
- Modify: `internal/hub/api/alerts_test.go`（`TestAlertKindsMapEveryValue`：`alertKinds` 按协议枚举全集核对，映射出的每个存储种类都被 `alert.CheckRule` 接受）
- Test（夹具与调用点）：`internal/hub/api/api_test.go`（`newZonedHarness`）、`internal/hub/api/alert_scope_reload_test.go`、`cmd/hub/mux_test.go`、`cmd/hub/serve_alert_test.go`（`TestServeReportsDaysLeftInTheHubZone`）

**Interfaces:**
- Consumes：Task 1 的生成代码与 `Public.billing` 投影；Task 2 的 `store.Billing`、`BillingCycles`、`NodeEdit`、`UpdateNode` 的 `billingChanged`；Task 3 的 `alert.ParseDate`、`Today`、`DaysLeft`、`(*Engine).SweepExpiry`。
- Produces（`internal/hub/api`）：
  - `Config.Location *time.Location`（`New` 遇 nil panic `api.Config.Location must be set`）；`PublicConfig.Location`（`NewPublic` 遇 nil panic `api.PublicConfig.Location must be set`）
  - `var billingCycles map[probev1.BillingCycle]store.BillingCycle`
  - `func billingOf(m *probev1.Billing) (store.Billing, error)`：nil 即五项全清，`days_left` 不读
  - `func billingProto(b store.Billing, today time.Time) *probev1.Billing`：五项都没填时返回 nil
  - `func nodeProto(n store.Node, today time.Time) *probev1.Node`
  - `func (s *Service) today() time.Time`
- Produces（测试辅助）：`newZonedHarness(t, trusted string, loc *time.Location) *harness`；`newHarness` 调它并传 `time.UTC`。

- [ ] **Step 1: 写失败测试**

`billing_test.go` 覆盖：
- 周期表与协议枚举一一对应。
- 畸形输入逐项拒绝，错误以 `billing.<字段>` 逐字比对，库不变；边界上的合法值照常保存。
- 请求里的 `days_left` 被忽略，回显是 hub 算的值；不带 `billing` 与空的 `billing` 都是五项全清，回显里 `billing` 缺失。
- `days_left` 在管理端、公开端都取 hub 时区的今天。
- 公开快照带四项与 `days_left`、不带自动续期；没有到期日的节点没有 `days_left`，什么都没填的节点没有 `billing`。
- 库里读不懂的到期日照原样下发，`days_left` 缺失而不是 0。
- 公开快照的 `now` 与 `days_left` 出自同一次读钟：公开服务用每读一次就前进一天的钟构造，经真实的 Connect 处理器取快照（`TestPublicSnapshotReadsTheClockOnce`）。
- 计费变化才扫描，响应是扫描之后的值；到期告警随 `UpdateNode` 同步转换。
- 到期规则经协议保存；离线规则带任一探测字段或提前天数、探测规则带提前天数都被拒，什么也不保存。
- 两个构造函数要求时区。

`serve_alert_test.go` 从 `serve` 的入口证明两端的 `days_left` 按 `--timezone` 计。`billed()` 把节点名写成 `n<id>`，几个节点在失败输出里分得开。

新建 `internal/hub/api/billing_test.go`（整份）：

```go
package api

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/hub/alert"
	"github.com/xjetry/probe/internal/hub/store"
)

// billed 是一次 UpdateNode 请求：名称 n<id>（几个节点在失败输出里分得开）、重置日 1、宽限期取默认，计费取 b
// （nil 即不带 billing）。
func billed(id int64, b *probev1.Billing) *probev1.UpdateNodeRequest {
	return &probev1.UpdateNodeRequest{Id: id, Name: fmt.Sprintf("n%d", id), TrafficResetDay: 1, OfflineGraceS: proto.Uint32(0), Billing: b}
}

func (h *harness) update(t *testing.T, req *probev1.UpdateNodeRequest) *probev1.Node {
	t.Helper()
	resp, err := h.admin.UpdateNode(t.Context(), connect.NewRequest(req))
	if err != nil {
		t.Fatal(err)
	}
	return resp.Msg.GetNode()
}

func TestBillingCyclesMapEveryValue(t *testing.T) {
	values := probev1.BillingCycle(0).Descriptor().Values()
	var stored []store.BillingCycle
	for i := 0; i < values.Len(); i++ {
		v := probev1.BillingCycle(values.Get(i).Number())
		c, ok := billingCycles[v]
		if !ok {
			t.Fatalf("%s has no stored form", v)
		}
		if enumFor(billingCycles, c) != v {
			t.Errorf("%s does not round-trip through %q", v, c)
		}
		stored = append(stored, c)
	}
	want := append([]store.BillingCycle{store.CycleNone}, store.BillingCycles()...)
	if len(billingCycles) != values.Len() || !slices.Equal(stored, want) {
		t.Fatalf("stored forms %q, want %q", stored, want)
	}
}

// 每种不合格的取值都被拒绝，错误以请求路径写明字段、约束与收到的值，库里的计费不变。
func TestUpdateNodeRejectsMalformedBilling(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	id, _ := h.createNode(t, "n")
	kept := h.update(t, billed(id, &probev1.Billing{Price: "5", Currency: "EUR", ExpiresOn: "2026-06-01"}))
	const price = `billing.price: must match ^\d{1,9}(\.\d{1,2})?$, e.g. 12.50; got `
	const date = `billing.expires_on: must be an existing date in YYYY-MM-DD form; got `
	for _, c := range []struct {
		b    *probev1.Billing
		want string
	}{
		{&probev1.Billing{Price: "12.345", Currency: "USD"}, price + `"12.345"`},
		{&probev1.Billing{Price: "1234567890", Currency: "USD"}, price + `"1234567890"`},
		{&probev1.Billing{Price: "-1", Currency: "USD"}, price + `"-1"`},
		{&probev1.Billing{Price: "12.", Currency: "USD"}, price + `"12."`},
		{&probev1.Billing{Price: ".5", Currency: "USD"}, price + `".5"`},
		{&probev1.Billing{Price: "1e3", Currency: "USD"}, price + `"1e3"`},
		{&probev1.Billing{Price: " 12", Currency: "USD"}, price + `" 12"`},
		{&probev1.Billing{Price: "１２", Currency: "USD"}, price + `"１２"`},
		{&probev1.Billing{Price: "12"}, "billing.currency: required when billing.price is set"},
		{&probev1.Billing{Currency: "usd"}, `billing.currency: must be three uppercase letters (ISO 4217), e.g. USD; got "usd"`},
		{&probev1.Billing{Currency: "USDT"}, `billing.currency: must be three uppercase letters (ISO 4217), e.g. USD; got "USDT"`},
		{&probev1.Billing{BillingCycle: 99}, "billing.billing_cycle: must be one of BILLING_CYCLE_UNSPECIFIED, BILLING_CYCLE_MONTHLY, BILLING_CYCLE_QUARTERLY, BILLING_CYCLE_SEMIANNUAL, BILLING_CYCLE_YEARLY, BILLING_CYCLE_BIENNIAL, BILLING_CYCLE_TRIENNIAL; got 99"},
		{&probev1.Billing{ExpiresOn: "2026-02-29"}, date + `"2026-02-29"`},
		{&probev1.Billing{ExpiresOn: "2026-1-05"}, date + `"2026-1-05"`},
		{&probev1.Billing{ExpiresOn: "2026-10-01T00:00:00Z"}, date + `"2026-10-01T00:00:00Z"`},
		{&probev1.Billing{AutoRenew: true, ExpiresOn: "2026-10-01"}, "billing.auto_renew: requires billing.billing_cycle and billing.expires_on to be set"},
		{&probev1.Billing{AutoRenew: true, BillingCycle: probev1.BillingCycle_BILLING_CYCLE_MONTHLY}, "billing.auto_renew: requires billing.billing_cycle and billing.expires_on to be set"},
	} {
		_, err := h.admin.UpdateNode(t.Context(), connect.NewRequest(billed(id, c.b)))
		if codeOf(err) != connect.CodeInvalidArgument || err.Error() != "invalid_argument: "+c.want {
			t.Errorf("%v: err = %v, want %s", c.b, err, c.want)
		}
	}
	list, err := h.admin.ListNodes(t.Context(), connect.NewRequest(&probev1.ListNodesRequest{}))
	if err != nil || !proto.Equal(list.Msg.GetNodes()[0], kept) {
		t.Fatalf("rejected updates changed the node: %v %v, want %v", list, err, kept)
	}
}

// 边界上的合法取值照常保存；币种可以单独填。days_left 由 hub 算出，请求里的值不读；空的 billing 与不带 billing
// 都是五项全清，回显里 billing 缺失。
func TestUpdateNodeAcceptsBoundaryBillingAndIgnoresDaysLeft(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	id, _ := h.createNode(t, "n")
	for _, b := range []*probev1.Billing{
		{Price: "0", Currency: "USD"},
		{Price: "999999999.99", Currency: "JPY", BillingCycle: probev1.BillingCycle_BILLING_CYCLE_TRIENNIAL},
		{Price: "12.5", Currency: "CNY", BillingCycle: probev1.BillingCycle_BILLING_CYCLE_MONTHLY, ExpiresOn: "2028-02-29", AutoRenew: true},
		{Currency: "EUR"},
		{ExpiresOn: "9999-12-31"},
	} {
		got := h.update(t, billed(id, b)).GetBilling()
		if got.GetPrice() != b.GetPrice() || got.GetCurrency() != b.GetCurrency() || got.GetBillingCycle() != b.GetBillingCycle() ||
			got.GetExpiresOn() != b.GetExpiresOn() || got.GetAutoRenew() != b.GetAutoRenew() || (got.DaysLeft != nil) != (b.GetExpiresOn() != "") {
			t.Errorf("saved %v, echoed %v", b, got)
		}
	}
	// 时钟是 2026-01-01（UTC），2026-01-10 剩 9 天；请求里的 999 不起作用。
	if got := h.update(t, billed(id, &probev1.Billing{ExpiresOn: "2026-01-10", DaysLeft: proto.Int32(999)})).GetBilling(); got.GetDaysLeft() != 9 {
		t.Fatalf("days_left = %d, want 9 computed by the hub", got.GetDaysLeft())
	}
	for _, b := range []*probev1.Billing{{}, nil} {
		h.update(t, billed(id, &probev1.Billing{Price: "5", Currency: "EUR", ExpiresOn: "2026-06-01"}))
		if got := h.update(t, billed(id, b)); got.Billing != nil {
			t.Fatalf("billing %v did not clear: %v", b, got)
		}
		list, err := h.admin.ListNodes(t.Context(), connect.NewRequest(&probev1.ListNodesRequest{}))
		if err != nil || list.Msg.GetNodes()[0].Billing != nil {
			t.Fatalf("billing %v: ListNodes = %v %v", b, list, err)
		}
	}
}

// days_left 取 hub 时区的今天：UTC 16:30 在东八区已是次日，比按 UTC 算少一天。管理端、公开端，以及公开快照的
// now 与 days_left 都是同一个口径。
func TestDaysLeftUsesTheHubZone(t *testing.T) {
	h := newZonedHarness(t, "", time.FixedZone("UTC+8", 8*3600))
	h.login(t)
	id, _ := h.createNode(t, "n")
	h.clk.SetWall(time.Date(2026, 1, 1, 16, 30, 0, 0, time.UTC))
	req := billed(id, &probev1.Billing{ExpiresOn: "2026-01-10"})
	req.Public = true
	if got := h.update(t, req).GetBilling().GetDaysLeft(); got != 8 {
		t.Fatalf("UpdateNode days_left = %d, want 8", got)
	}
	list, err := h.admin.ListNodes(t.Context(), connect.NewRequest(&probev1.ListNodesRequest{}))
	if err != nil || list.Msg.GetNodes()[0].GetBilling().GetDaysLeft() != 8 {
		t.Fatalf("ListNodes = %v %v", list, err)
	}
	snap, err := h.publicClient().GetSnapshot(t.Context(), connect.NewRequest(&probev1.PublicServiceGetSnapshotRequest{}))
	if err != nil || snap.Msg.GetNow() != h.clk.Now().Unix() || snap.Msg.GetNodes()[0].GetBilling().GetDaysLeft() != 8 {
		t.Fatalf("public snapshot = %v %v", snap, err)
	}
}

// 公开快照带价格、币种、周期、到期日与 days_left，不带自动续期；没有到期日的节点 days_left 缺失，什么都没填的
// 节点 billing 缺失。
func TestPublicSnapshotCarriesBillingWithoutAutoRenew(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	a, _ := h.createNode(t, "a")
	b, _ := h.createNode(t, "b")
	c, _ := h.createNode(t, "c")
	for _, req := range []*probev1.UpdateNodeRequest{
		billed(a, &probev1.Billing{Price: "12.50", Currency: "USD", BillingCycle: probev1.BillingCycle_BILLING_CYCLE_YEARLY, ExpiresOn: "2025-12-29"}),
		billed(b, &probev1.Billing{Price: "3", Currency: "EUR", BillingCycle: probev1.BillingCycle_BILLING_CYCLE_MONTHLY, ExpiresOn: "2026-03-01", AutoRenew: true}),
		billed(c, nil),
	} {
		req.Public = true
		h.update(t, req)
	}
	snap, err := h.publicClient().GetSnapshot(t.Context(), connect.NewRequest(&probev1.PublicServiceGetSnapshotRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	want := &probev1.PublicBilling{Price: "12.50", Currency: "USD", BillingCycle: probev1.BillingCycle_BILLING_CYCLE_YEARLY, ExpiresOn: "2025-12-29", DaysLeft: proto.Int32(-3)}
	if got := snap.Msg.GetNodes()[0].GetBilling(); !proto.Equal(got, want) {
		t.Fatalf("public billing of a = %v, want %v", got, want)
	}
	if got := snap.Msg.GetNodes()[2]; got.Billing != nil {
		t.Fatalf("node without billing = %v", got)
	}
	upd := billed(b, &probev1.Billing{Price: "3", Currency: "EUR"})
	upd.Public = true
	h.update(t, upd)
	h.clk.Advance(2 * time.Second) // 越过快照缓存的 1 秒窗口
	raw := pubGet(t, h, "GetSnapshot", jsonQuery("{}"), nil)
	if raw.status != 200 || bytes.Contains(raw.body, []byte("autoRenew")) || bytes.Contains(raw.body, []byte("auto_renew")) {
		t.Fatalf("snapshot JSON = %d %s", raw.status, raw.body)
	}
	if !bytes.Contains(raw.body, []byte(`"daysLeft":-3`)) || strings.Count(string(raw.body), "daysLeft") != 1 || strings.Count(string(raw.body), `"billing"`) != 2 {
		t.Fatalf("days_left must appear only for the node with an expiry date, billing only for the nodes that have one: %s", raw.body)
	}
}

// 库里读不懂的到期日只可能来自绕过 UpdateNode 的写库。它照原样下发，days_left 缺失而不是 0：0 会显示成"今天到期"。
func TestUnreadableExpiresOnHasNoDaysLeft(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	id, _ := h.createNode(t, "n")
	if _, err := h.store.UpdateNode(t.Context(), id, store.NodeEdit{Name: "n", TrafficResetDay: 1, Billing: store.Billing{ExpiresOn: "2026-02-30"}}); err != nil {
		t.Fatal(err)
	}
	list, err := h.admin.ListNodes(t.Context(), connect.NewRequest(&probev1.ListNodesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if b := list.Msg.GetNodes()[0].GetBilling(); b.GetExpiresOn() != "2026-02-30" || b.DaysLeft != nil {
		t.Fatalf("billing = %v, want expires_on kept and days_left missing", b)
	}
}

// steppingClock 每读一次墙钟前进一天。GetSnapshot 若分两次读钟，now 与 days_left 就落在不同的日历日上。
type steppingClock struct {
	mu   sync.Mutex
	next time.Time
}

func (c *steppingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.next
	c.next = t.Add(24 * time.Hour)
	return t
}
func (c *steppingClock) Mono() time.Duration { return 0 }

// 公开快照的 now 与 days_left 出自同一次读钟：第三方主题拿 now 核对 days_left，不会差一天。快照经真实的 Connect
// 处理器取得，公开服务用每读一次就前进一天的钟构造。
func TestPublicSnapshotReadsTheClockOnce(t *testing.T) {
	h := newZonedHarness(t, "", time.FixedZone("UTC+8", 8*3600))
	h.login(t)
	id, _ := h.createNode(t, "n")
	req := billed(id, &probev1.Billing{ExpiresOn: "2026-03-01"})
	req.Public = true
	h.update(t, req)
	loc := time.FixedZone("UTC+8", 8*3600)
	clk := &steppingClock{next: time.Date(2026, 1, 1, 23, 59, 59, 0, loc)}
	pub := NewPublic(PublicConfig{ReportInterval: 10 * time.Second, Location: loc}, h.store, h.live, h.book, h.reg, clk, slog.Default())
	path, handler := probev1connect.NewPublicServiceHandler(pub)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := probev1connect.NewPublicServiceClient(srv.Client(), srv.URL)
	for range 3 {
		snap, err := client.GetSnapshot(t.Context(), connect.NewRequest(&probev1.PublicServiceGetSnapshotRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		want, _ := alert.DaysLeft("2026-03-01", alert.Today(time.Unix(snap.Msg.GetNow(), 0), loc))
		if got := snap.Msg.GetNodes()[0].GetBilling().GetDaysLeft(); got != int32(want) {
			t.Fatalf("now %d is %s in the hub zone, so days_left should be %d; got %d", snap.Msg.GetNow(), time.Unix(snap.Msg.GetNow(), 0).In(loc).Format(time.DateOnly), want, got)
		}
	}
}

// 计费字段变了才扫描：只改名称时已过期的自动续期不推后；计费一变，响应里就是推后之后的日期。表单带着推后之前的
// 旧到期日提交也算变了（与库里推后的日期比），扫描随即再推后一次。
func TestUpdateNodeSweepsExpiryWhenBillingChanges(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	id, _ := h.createNode(t, "n")
	renewing := func() *probev1.Billing {
		return &probev1.Billing{BillingCycle: probev1.BillingCycle_BILLING_CYCLE_MONTHLY, ExpiresOn: "2026-01-15", AutoRenew: true}
	}
	if got := h.update(t, billed(id, renewing())).GetBilling().GetExpiresOn(); got != "2026-01-15" {
		t.Fatalf("expires_on = %s", got)
	}
	h.clk.SetWall(time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC))
	h.login(t) // 墙钟跳过了会话的有效期
	renamed := billed(id, renewing())
	renamed.Name = "renamed"
	if got := h.update(t, renamed); got.GetBilling().GetExpiresOn() != "2026-01-15" || got.GetBilling().GetDaysLeft() != -5 {
		t.Fatalf("a rename swept expiry: %v", got)
	}
	priced := renewing()
	priced.Price, priced.Currency = "9", "USD"
	if got := h.update(t, billed(id, priced)); got.GetBilling().GetExpiresOn() != "2026-02-15" || got.GetBilling().GetDaysLeft() != 26 {
		t.Fatalf("a billing change did not renew: %v", got)
	}
	if got := h.update(t, billed(id, priced)); got.GetBilling().GetExpiresOn() != "2026-02-15" {
		t.Fatalf("stale form: %v", got)
	}
}

// 到期告警跟着 UpdateNode 同步转换：响应返回时事件已经落库，续费之后不等到零点。
func TestUpdateNodeFiresAndRecoversExpiryAlerts(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	id, _ := h.createNode(t, "n")
	saveRule(t, h, expiryRuleProto())
	events := func() []*probev1.AlertEvent {
		t.Helper()
		resp, err := h.admin.ListAlertEvents(t.Context(), connect.NewRequest(&probev1.ListAlertEventsRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg.GetEvents()
	}
	h.update(t, billed(id, &probev1.Billing{ExpiresOn: "2026-01-04"}))
	if ev := events(); len(ev) != 1 || ev[0].GetTransition() != "firing" || ev[0].GetSummary() != "节点 n1 将于 2026-01-04 到期（剩 3 天，规则 到期）" || ev[0].GetValue() != 3 {
		t.Fatalf("events after setting a close date: %v", ev)
	}
	h.update(t, billed(id, &probev1.Billing{ExpiresOn: "2027-01-04"}))
	if ev := events(); len(ev) != 2 || ev[0].GetTransition() != "recovered" || ev[0].GetSummary() != "节点 n1 到期日已更新为 2027-01-04（规则 到期）" {
		t.Fatalf("events after renewing: %v", ev)
	}
}

func expiryRuleProto() *probev1.AlertRule {
	return &probev1.AlertRule{Name: "到期", Kind: probev1.AlertKind_ALERT_KIND_EXPIRY, Enabled: true, AllNodes: true, DaysBefore: 7}
}

// 到期规则经协议保存并回显提前天数；提前天数越界或带着探测字段时，错误以请求路径写明字段与约束。
func TestSaveAlertRuleExpiryKind(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	saved := saveRule(t, h, expiryRuleProto())
	if saved.GetKind() != probev1.AlertKind_ALERT_KIND_EXPIRY || saved.GetDaysBefore() != 7 {
		t.Fatalf("saved %v", saved)
	}
	list, err := h.admin.ListAlertRules(t.Context(), connect.NewRequest(&probev1.ListAlertRulesRequest{}))
	if err != nil || len(list.Msg.GetRules()) != 1 || list.Msg.GetRules()[0].GetDaysBefore() != 7 {
		t.Fatalf("listed %v %v", list, err)
	}
	for _, c := range []struct {
		change func(*probev1.AlertRule)
		want   string
	}{
		{func(r *probev1.AlertRule) { r.DaysBefore = 0 }, "rule.days_before must be between 1 and 365"},
		{func(r *probev1.AlertRule) { r.DaysBefore = 366 }, "rule.days_before must be between 1 and 365"},
		{func(r *probev1.AlertRule) { r.TaskId = 1 }, "rule.task_id must be 0 unless kind is probe"},
		{func(r *probev1.AlertRule) { r.Metric = probev1.ProbeMetric_PROBE_METRIC_LOSS_PCT }, "rule.metric must be unspecified unless kind is probe"},
		{func(r *probev1.AlertRule) { r.Threshold = 1 }, "rule.threshold must be 0 unless kind is probe"},
		{func(r *probev1.AlertRule) { r.ForMinutes = 1 }, "rule.for_minutes must be 0 unless kind is probe"},
	} {
		r := expiryRuleProto()
		c.change(r)
		_, err := h.admin.SaveAlertRule(t.Context(), connect.NewRequest(&probev1.SaveAlertRuleRequest{Rule: r}))
		if codeOf(err) != connect.CodeInvalidArgument || err.Error() != "invalid_argument: "+c.want {
			t.Errorf("%v: err = %v, want %s", r, err, c.want)
		}
	}
}

// 离线规则带任一探测字段或提前天数、探测规则带提前天数，都被拒绝，什么也不保存。离线规则带探测字段原来会被存储层
// 静默清零，调用方发了什么、存下的却是零值，无从察觉。
func TestSaveAlertRuleRejectsFieldsOfOtherKinds(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	offline := func(change func(*probev1.AlertRule)) *probev1.AlertRule { r := offlineRule(); change(r); return r }
	probeWithDays := &probev1.AlertRule{Name: "丢包", Kind: probev1.AlertKind_ALERT_KIND_PROBE, Enabled: true, AllNodes: true,
		TaskId: 1, Metric: probev1.ProbeMetric_PROBE_METRIC_LOSS_PCT, Threshold: 20, ForMinutes: 3, DaysBefore: 7}
	for _, c := range []struct {
		rule *probev1.AlertRule
		want string
	}{
		{offline(func(r *probev1.AlertRule) { r.TaskId = 1 }), "rule.task_id must be 0 unless kind is probe"},
		{offline(func(r *probev1.AlertRule) { r.Metric = probev1.ProbeMetric_PROBE_METRIC_RTT_MS }), "rule.metric must be unspecified unless kind is probe"},
		{offline(func(r *probev1.AlertRule) { r.Threshold = 5 }), "rule.threshold must be 0 unless kind is probe"},
		{offline(func(r *probev1.AlertRule) { r.ForMinutes = 3 }), "rule.for_minutes must be 0 unless kind is probe"},
		{offline(func(r *probev1.AlertRule) { r.DaysBefore = 7 }), "rule.days_before must be 0 unless kind is expiry"},
		{probeWithDays, "rule.days_before must be 0 unless kind is expiry"},
	} {
		_, err := h.admin.SaveAlertRule(t.Context(), connect.NewRequest(&probev1.SaveAlertRuleRequest{Rule: c.rule}))
		if codeOf(err) != connect.CodeInvalidArgument || err.Error() != "invalid_argument: "+c.want {
			t.Errorf("%v: err = %v, want %s", c.rule, err, c.want)
		}
	}
	list, err := h.admin.ListAlertRules(t.Context(), connect.NewRequest(&probev1.ListAlertRulesRequest{}))
	if err != nil || len(list.Msg.GetRules()) != 0 {
		t.Fatalf("rejected rules were saved: %v %v", list, err)
	}
}

func TestConstructorsRequireLocation(t *testing.T) {
	for _, c := range []struct {
		want string
		call func()
	}{
		{"api.Config.Location must be set", func() { New(Config{TTL: time.Second}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil) }},
		{"api.PublicConfig.Location must be set", func() { NewPublic(PublicConfig{}, nil, nil, nil, nil, nil, nil) }},
	} {
		func() {
			defer func() {
				if r := recover(); r != c.want {
					t.Errorf("panic = %v, want %s", r, c.want)
				}
			}()
			c.call()
		}()
	}
}
```

`internal/hub/api/api_test.go` 原文（1/3）：

```go
func newHarness(t *testing.T, trusted string) *harness {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), clk, slog.Default())
```

替换为：

```go
func newHarness(t *testing.T, trusted string) *harness {
	t.Helper()
	return newZonedHarness(t, trusted, time.UTC)
}

// newZonedHarness 的 loc 是 hub 的 --timezone：流量周期、到期扫描与 days_left 用同一个时区，与 serve 的装配一致。
func newZonedHarness(t *testing.T, trusted string, loc *time.Location) *harness {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), clk, slog.Default())
```

`internal/hub/api/api_test.go` 原文（2/3）：

```go
	a := auth.New(st, clk, slog.Default())
	l := live.New(clk, 30*time.Second)
	book := traffic.New(st, clk, time.UTC, slog.Default())
	reg := probe.New(st, slog.Default())
	alerts := alert.New(alert.Config{TTL: 30 * time.Second, Location: time.UTC}, st, l, clk, slog.Default())
	notifier := alert.NewQueue(st, alerts.Channels, alert.NewHTTPClient(), "", clk, nil, slog.Default())
	in, err := ingest.New(ingest.Config{TTL: 30 * time.Second, TrustedProxies: prefixes}, l, st, a, book, reg, clk, slog.Default())
```

替换为：

```go
	a := auth.New(st, clk, slog.Default())
	l := live.New(clk, 30*time.Second)
	book := traffic.New(st, clk, loc, slog.Default())
	reg := probe.New(st, slog.Default())
	alerts := alert.New(alert.Config{TTL: 30 * time.Second, Location: loc}, st, l, clk, slog.Default())
	notifier := alert.NewQueue(st, alerts.Channels, alert.NewHTTPClient(), "", clk, nil, slog.Default())
	in, err := ingest.New(ingest.Config{TTL: 30 * time.Second, TrustedProxies: prefixes}, l, st, a, book, reg, clk, slog.Default())
```

`internal/hub/api/api_test.go` 原文（3/3）：

```go
		t.Fatal(err)
	}
	svc := New(Config{TTL: 30 * time.Second, ReportInterval: 10 * time.Second, TrustedProxies: prefixes, HubVersion: "test-hub-version"}, st, a, l, in, book, reg, alerts, notifier, clk, slog.Default())
	pub := NewPublic(PublicConfig{ReportInterval: 10 * time.Second, TrustedProxies: prefixes}, st, l, book, reg, clk, slog.Default())
	mux := http.NewServeMux()
	mux.Handle(in.Handler())
```

替换为：

```go
		t.Fatal(err)
	}
	svc := New(Config{TTL: 30 * time.Second, ReportInterval: 10 * time.Second, TrustedProxies: prefixes, HubVersion: "test-hub-version", Location: loc}, st, a, l, in, book, reg, alerts, notifier, clk, slog.Default())
	pub := NewPublic(PublicConfig{ReportInterval: 10 * time.Second, TrustedProxies: prefixes, Location: loc}, st, l, book, reg, clk, slog.Default())
	mux := http.NewServeMux()
	mux.Handle(in.Handler())
```

`internal/hub/api/alert_scope_reload_test.go` 原文：

```go
		t.Fatal(err)
	}
	svc := New(Config{TTL: 30 * time.Second}, h.store, h.auth, h.live, h.ingest, h.book, h.reg, e, nil, h.clk, slog.Default())
	mux := http.NewServeMux()
	mux.Handle(svc.Handler())
```

替换为：

```go
		t.Fatal(err)
	}
	svc := New(Config{TTL: 30 * time.Second, Location: time.UTC}, h.store, h.auth, h.live, h.ingest, h.book, h.reg, e, nil, h.clk, slog.Default())
	mux := http.NewServeMux()
	mux.Handle(svc.Handler())
```

`cmd/hub/mux_test.go` 原文：

```go
		t.Fatal(err)
	}
	admin := api.New(api.Config{TTL: 30 * time.Second, ReportInterval: 10 * time.Second}, st, a, l, svc, book, reg, alerts, notifier, clk, slog.Default())
	pub := api.NewPublic(api.PublicConfig{ReportInterval: 10 * time.Second}, st, l, book, reg, clk, slog.Default())
	return newMux(mountOf(svc.Handler()), mountOf(admin.Handler()), mountOf(pub.Handler()), mountOf(web.Prefix, web.Handler()), mountOf("/", web.PublicHandler()))
}
```

替换为：

```go
		t.Fatal(err)
	}
	admin := api.New(api.Config{TTL: 30 * time.Second, ReportInterval: 10 * time.Second, Location: time.UTC}, st, a, l, svc, book, reg, alerts, notifier, clk, slog.Default())
	pub := api.NewPublic(api.PublicConfig{ReportInterval: 10 * time.Second, Location: time.UTC}, st, l, book, reg, clk, slog.Default())
	return newMux(mountOf(svc.Handler()), mountOf(admin.Handler()), mountOf(pub.Handler()), mountOf(web.Prefix, web.Handler()), mountOf("/", web.PublicHandler()))
}
```

`cmd/hub/serve_alert_test.go` 原文：

```go
			t.Fatal("the startup expiry sweep did not renew the node")
		}
	}
}
```

替换为：

```go
			t.Fatal("the startup expiry sweep did not renew the node")
		}
	}
}

// serve 把 --timezone 交给管理端与公开端：UTC 16:30 在上海已是 9 月 25 日，9 月 30 日到期还剩 5 天（按 UTC 是 6 天）。
func TestServeReportsDaysLeftInTheHubZone(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 9, 24, 16, 30, 0, 0, time.UTC))
	db := filepath.Join(t.TempDir(), "hub.db")
	password := "days left sufficiently long password"
	if err := runPasswdWith([]string{"--db", db}, pipeWith(t, password+"\n"), io.Discard); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(db, clk, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateNode(t.Context(), "zoned", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateNode(t.Context(), id, store.NodeEdit{Name: "zoned", Public: true, TrafficResetDay: 1, Billing: store.Billing{ExpiresOn: "2026-09-30"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	url, _, _ := startTestHub(t, db, clk, "--timezone", "Asia/Shanghai")
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	admin := probev1connect.NewAdminServiceClient(&http.Client{Jar: jar, Timeout: testwait.Bound}, url)
	if _, err := admin.Login(t.Context(), connect.NewRequest(&probev1.LoginRequest{Password: password})); err != nil {
		t.Fatal(err)
	}
	nodes, err := admin.ListNodes(t.Context(), connect.NewRequest(&probev1.ListNodesRequest{}))
	if err != nil || len(nodes.Msg.GetNodes()) != 1 || nodes.Msg.GetNodes()[0].GetBilling().GetDaysLeft() != 5 {
		t.Fatalf("admin ListNodes = %v %v, want days_left 5", nodes, err)
	}
	public := probev1connect.NewPublicServiceClient(&http.Client{Timeout: testwait.Bound}, url)
	snap, err := public.GetSnapshot(t.Context(), connect.NewRequest(&probev1.PublicServiceGetSnapshotRequest{}))
	if err != nil || len(snap.Msg.GetNodes()) != 1 || snap.Msg.GetNodes()[0].GetBilling().GetDaysLeft() != 5 {
		t.Fatalf("public GetSnapshot = %v %v, want days_left 5", snap, err)
	}
}
```

`internal/hub/api/alerts_test.go` 原文：

```go
		t.Fatalf("deleted node left alert cache=%v", after.Msg)
	}
}
```

替换为：

```go
		t.Fatalf("deleted node left alert cache=%v", after.Msg)
	}
}

// alertKinds 是协议种类与存储种类之间的翻译表。协议加了种类而这张表漏配时，编译照过；新种类若还没有协议层的用例，
// 别的用例也不会红，功能却静默失效：
// SaveAlertRule 在 parseEnum 处以 rule.kind 拒绝这个种类，列表回显时 enumFor 找不到它而给出 UNSPECIFIED。这里按协议
// 枚举的全集核对：UNSPECIFIED 之外的每个值都有映射且往返一致，表里没有多出的项；反过来，映射出的每个存储种类都要被
// alert.CheckRule 接受，免得表配上了、校验却不认这个种类。
func TestAlertKindsMapEveryValue(t *testing.T) {
	values := probev1.AlertKind(0).Descriptor().Values()
	for i := 0; i < values.Len(); i++ {
		v := probev1.AlertKind(values.Get(i).Number())
		if v == probev1.AlertKind_ALERT_KIND_UNSPECIFIED {
			continue
		}
		k, ok := alertKinds[v]
		if !ok {
			t.Errorf("%s has no stored kind in alertKinds", v)
			continue
		}
		if enumFor(alertKinds, k) != v {
			t.Errorf("%s does not round-trip through %q", v, k)
		}
	}
	if len(alertKinds) != values.Len()-1 {
		t.Errorf("alertKinds has %d entries, want one per protocol kind except UNSPECIFIED (%d)", len(alertKinds), values.Len()-1)
	}
	// 每个存储种类一条最小的合法规则：离线不带专用字段，探测带任务、指标、阈值与持续分钟，到期带提前天数。
	minimal := map[store.AlertKind]store.AlertRule{
		store.KindOffline: {Name: "离线", Kind: store.KindOffline, AllNodes: true},
		store.KindProbe:   {Name: "探测", Kind: store.KindProbe, AllNodes: true, TaskID: 1, Metric: store.MetricLossPct, Threshold: 10, ForMinutes: 3},
		store.KindExpiry:  {Name: "到期", Kind: store.KindExpiry, AllNodes: true, DaysBefore: 7},
	}
	for v, k := range alertKinds {
		r, ok := minimal[k]
		if !ok {
			t.Errorf("no minimal valid rule for stored kind %q (%s); add one so CheckRule is checked for it", k, v)
			continue
		}
		if err := alert.CheckRule(r); err != nil {
			t.Errorf("alert.CheckRule rejects the stored kind %q of %s: %v", k, v, err)
		}
	}
}
```

- [ ] **Step 2: 跑红**

```bash
cd /Users/xjetry/work/vibe/probe-billing && go test -count=1 ./internal/hub/api/ ./cmd/hub/ > /tmp/billing-t4-red.log 2>&1; echo $?
```

Expected：1，两个包都编译失败。原文节选：
- `cmd/hub/mux_test.go:64:87: unknown field Location in struct literal of type api.Config`
- `cmd/hub/mux_test.go:65:74: unknown field Location in struct literal of type api.PublicConfig`
- `internal/hub/api/api_test.go:88:135: unknown field Location in struct literal of type Config`
- `internal/hub/api/billing_test.go:44:12: undefined: billingCycles`

- [ ] **Step 3: 实现**

`internal/hub/api/nodes.go` 原文（1/7）：

```go
	"context"
	"errors"
	"time"
	"unicode/utf8"
```

替换为：

```go
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
```

`internal/hub/api/nodes.go` 原文（2/7）：

```go

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/sanitize"
	"github.com/xjetry/probe/internal/hub/store"
```

替换为：

```go

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/alert"
	"github.com/xjetry/probe/internal/hub/sanitize"
	"github.com/xjetry/probe/internal/hub/store"
```

`internal/hub/api/nodes.go` 原文（3/7）：

```go
)

func nodeProto(n store.Node) *probev1.Node {
	out := &probev1.Node{Id: n.ID, Name: n.Name, Public: n.Public, Note: n.Note, SortOrder: n.SortOrder, CreatedAt: n.CreatedAt.Unix(), Facts: n.Facts, TrafficResetDay: uint32(n.TrafficResetDay)}
	if !n.LastSeenAt.IsZero() {
		out.LastSeenAt = proto.Int64(n.LastSeenAt.Unix())
```

替换为：

```go
)

// billingCycles 是协议枚举与库里周期文本的一一对应，未指定对应"没有周期"；TestBillingCyclesMapEveryValue 按两侧全集核对。
var billingCycles = map[probev1.BillingCycle]store.BillingCycle{
	probev1.BillingCycle_BILLING_CYCLE_UNSPECIFIED: store.CycleNone,
	probev1.BillingCycle_BILLING_CYCLE_MONTHLY:     store.CycleMonthly,
	probev1.BillingCycle_BILLING_CYCLE_QUARTERLY:   store.CycleQuarterly,
	probev1.BillingCycle_BILLING_CYCLE_SEMIANNUAL:  store.CycleSemiannual,
	probev1.BillingCycle_BILLING_CYCLE_YEARLY:      store.CycleYearly,
	probev1.BillingCycle_BILLING_CYCLE_BIENNIAL:    store.CycleBiennial,
	probev1.BillingCycle_BILLING_CYCLE_TRIENNIAL:   store.CycleTriennial,
}

// 价格与币种的形状按 §9.4。RE2 的 \d 只匹配 ASCII 数字，全角数字不算。
var (
	pricePattern    = regexp.MustCompile(`^\d{1,9}(\.\d{1,2})?$`)
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
)

// billingOf 是计费字段唯一的校验，与 traffic_reset_day、offline_grace_s 同在 UpdateNode 入口裁决（§9.4）。m 为 nil
// （请求没带 billing）时各项取零值，即五项全清。days_left 由 hub 计算，请求里的值不读。到期日与到期扫描、days_left
// 共用 alert.ParseDate，写进去的日期读侧一定读得懂。自动续期要求周期与到期日都非空：推后需要这两项。扫描对缺周期
// 另有守卫（alert.cycleMonths），不依赖这里。
func billingOf(m *probev1.Billing) (store.Billing, error) {
	b := store.Billing{Price: m.GetPrice(), Currency: m.GetCurrency(), ExpiresOn: m.GetExpiresOn(), AutoRenew: m.GetAutoRenew()}
	if b.Price != "" && !pricePattern.MatchString(b.Price) {
		return store.Billing{}, invalid("billing.price: must match %s, e.g. 12.50; got %q", pricePattern, b.Price)
	}
	if b.Currency != "" && !currencyPattern.MatchString(b.Currency) {
		return store.Billing{}, invalid("billing.currency: must be three uppercase letters (ISO 4217), e.g. USD; got %q", b.Currency)
	}
	if b.Price != "" && b.Currency == "" {
		return store.Billing{}, invalid("billing.currency: required when billing.price is set")
	}
	cycle, ok := billingCycles[m.GetBillingCycle()]
	if !ok {
		values := probev1.BillingCycle(0).Descriptor().Values()
		names := make([]string, values.Len())
		for i := range names {
			names[i] = string(values.Get(i).Name())
		}
		return store.Billing{}, invalid("billing.billing_cycle: must be one of %s; got %s", strings.Join(names, ", "), m.GetBillingCycle())
	}
	b.Cycle = cycle
	if b.ExpiresOn != "" {
		if _, err := alert.ParseDate(b.ExpiresOn); err != nil {
			return store.Billing{}, invalid("billing.expires_on: must be an existing date in YYYY-MM-DD form; got %q", b.ExpiresOn)
		}
	}
	if b.AutoRenew && (b.Cycle == store.CycleNone || b.ExpiresOn == "") {
		return store.Billing{}, invalid("billing.auto_renew: requires billing.billing_cycle and billing.expires_on to be set")
	}
	return b, nil
}

// billingProto 是 Node.billing，也是公开端 PublicBilling 的投影来源。五项都没填时为 nil，字段缺失。today 是 hub 时区的
// 今天（alert.Today）；没有到期日或库里的值读不懂时 days_left 缺失。
func billingProto(b store.Billing, today time.Time) *probev1.Billing {
	if b == (store.Billing{}) {
		return nil
	}
	out := &probev1.Billing{Price: b.Price, Currency: b.Currency, BillingCycle: enumFor(billingCycles, b.Cycle), ExpiresOn: b.ExpiresOn, AutoRenew: b.AutoRenew}
	if d, ok := alert.DaysLeft(b.ExpiresOn, today); ok {
		out.DaysLeft = proto.Int32(int32(d))
	}
	return out
}

// nodeProto 的 today 是 hub 时区的今天（alert.Today）。
func nodeProto(n store.Node, today time.Time) *probev1.Node {
	out := &probev1.Node{Id: n.ID, Name: n.Name, Public: n.Public, Note: n.Note, SortOrder: n.SortOrder, CreatedAt: n.CreatedAt.Unix(), Facts: n.Facts, TrafficResetDay: uint32(n.TrafficResetDay),
		Billing: billingProto(n.Billing, today)}
	if !n.LastSeenAt.IsZero() {
		out.LastSeenAt = proto.Int64(n.LastSeenAt.Unix())
```

`internal/hub/api/nodes.go` 原文（4/7）：

```go
	}
	out := make([]*probev1.Node, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, nodeProto(n))
	}
	return connect.NewResponse(&probev1.ListNodesResponse{Nodes: out}), nil
```

替换为：

```go
	}
	out := make([]*probev1.Node, 0, len(nodes))
	today := s.today()
	for _, n := range nodes {
		out = append(out, nodeProto(n, today))
	}
	return connect.NewResponse(&probev1.ListNodesResponse{Nodes: out}), nil
```

`internal/hub/api/nodes.go` 原文（5/7）：

```go
	}
	s.log.Info("node created", "node", id, "name", name)
	return connect.NewResponse(&probev1.CreateNodeResponse{Node: nodeProto(n), Token: tok}), nil
}

```

替换为：

```go
	}
	s.log.Info("node created", "node", id, "name", name)
	return connect.NewResponse(&probev1.CreateNodeResponse{Node: nodeProto(n, s.today()), Token: tok}), nil
}

```

`internal/hub/api/nodes.go` 原文（6/7）：

```go
		return nil, invalid("offline_grace_s: must be 0 or at least %d seconds (PROBE_OFFLINE_AFTER); got %d", (s.cfg.TTL+time.Second-1)/time.Second, grace)
	}
	s.nodeMu.Lock()
	defer s.nodeMu.Unlock()
	_, err = s.store.UpdateNode(ctx, req.Msg.GetId(), store.NodeEdit{Name: name, Public: req.Msg.GetPublic(), Note: note, TrafficResetDay: day, OfflineGraceS: int(grace)})
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound(req.Msg.GetId())
```

替换为：

```go
		return nil, invalid("offline_grace_s: must be 0 or at least %d seconds (PROBE_OFFLINE_AFTER); got %d", (s.cfg.TTL+time.Second-1)/time.Second, grace)
	}
	billing, err := billingOf(req.Msg.GetBilling())
	if err != nil {
		return nil, err
	}
	edit := store.NodeEdit{Name: name, Public: req.Msg.GetPublic(), Note: note, TrafficResetDay: day, OfflineGraceS: int(grace), Billing: billing}
	s.nodeMu.Lock()
	billingChanged, err := s.store.UpdateNode(ctx, req.Msg.GetId(), edit)
	if err == nil {
		// 只有库提交成功才改内存；nodeMu 跨越两次写入并与删除共用，失败或并发请求都不能使两者分叉。
		s.traffic.SetResetDay(req.Msg.GetId(), day)
	}
	s.nodeMu.Unlock()
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound(req.Msg.GetId())
```

`internal/hub/api/nodes.go` 原文（7/7）：

```go
		return nil, internalError("updating node failed")
	}
	// 只有库提交成功才改内存；nodeMu 跨越两次写入并与删除共用，失败或并发请求都不能使两者分叉。
	s.traffic.SetResetDay(req.Msg.GetId(), day)
	n, err := s.store.GetNode(ctx, req.Msg.GetId())
	if err != nil {
		s.log.Error("reading updated node failed", "err", err)
		return nil, internalError("reading updated node failed")
	}
	return connect.NewResponse(&probev1.UpdateNodeResponse{Node: nodeProto(n)}), nil
}

```

替换为：

```go
		return nil, internalError("updating node failed")
	}
	// 计费字段变了就立刻按新值扫描一次（§9.2）：续费之后不等到零点才恢复。修改已提交，扫描失败只记日志，下一次扫描
	// 会再评估。扫描放在 nodeMu 之外：它要等 writeMu（离线巡检、探测评估、日界扫描都可能正持有），再做一整轮续期
	// 写回与状态写，持 nodeMu 等它只会挡住其它节点的编辑与删除，与 DeleteNode 把清理放在锁外同一个理由。持锁调用
	// 也不会成环：alert 包不 import api，且引擎经 SetSender 注入的实现（当前是 alert.Queue）也不在 api 里，
	// 任何持 writeMu 的路径都取不到 nodeMu。
	if billingChanged {
		if err := s.alerts.SweepExpiry(context.WithoutCancel(ctx)); err != nil {
			s.log.Error("expiry sweep after node update failed", "node", req.Msg.GetId(), "err", err)
		}
	}
	// 扫描之后才回读，响应里的到期日与 days_left 已是推后之后的值。放锁之后节点可能已被并发的 DeleteNode 删掉，
	// 这时按不存在应答。
	n, err := s.store.GetNode(ctx, req.Msg.GetId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound(req.Msg.GetId())
	}
	if err != nil {
		s.log.Error("reading updated node failed", "err", err)
		return nil, internalError("reading updated node failed")
	}
	return connect.NewResponse(&probev1.UpdateNodeResponse{Node: nodeProto(n, s.today())}), nil
}

```

`internal/hub/api/service.go` 原文（1/3）：

```go
	// 被面板当作非正式版本：给出 latest 安装命令、不标落后节点。
	HubVersion string
}

```

替换为：

```go
	// 被面板当作非正式版本：给出 latest 安装命令、不标落后节点。
	HubVersion string
	// Location 是 hub 的 --timezone，days_left 按它的日历日算；New 要求非 nil。
	Location *time.Location
}

```

`internal/hub/api/service.go` 原文（2/3）：

```go
		panic("api.Config.TTL must be positive")
	}
	return &Service{
		cfg: cfg, store: st, auth: a, live: l, nodes: nodes, traffic: book, probes: probes, alerts: alerts, notifier: notifier, clk: clk, log: log,
```

替换为：

```go
		panic("api.Config.TTL must be positive")
	}
	if cfg.Location == nil {
		panic("api.Config.Location must be set")
	}
	return &Service{
		cfg: cfg, store: st, auth: a, live: l, nodes: nodes, traffic: book, probes: probes, alerts: alerts, notifier: notifier, clk: clk, log: log,
```

`internal/hub/api/service.go` 原文（3/3）：

```go
	}
}

func (s *Service) Handler() (string, http.Handler) {
```

替换为：

```go
	}
}

// today 是 hub 时区（--timezone）的今天，days_left 以它为基准。
func (s *Service) today() time.Time { return alert.Today(s.clk.Now(), s.cfg.Location) }

func (s *Service) Handler() (string, http.Handler) {
```

`internal/hub/api/public.go` 原文（1/4）：

```go
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/live"
	"github.com/xjetry/probe/internal/hub/probe"
```

替换为：

```go
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/alert"
	"github.com/xjetry/probe/internal/hub/live"
	"github.com/xjetry/probe/internal/hub/probe"
```

`internal/hub/api/public.go` 原文（2/4）：

```go
	// TrustedProxies 决定限流按哪个来源地址计：只有来自这些对端的 X-Forwarded-For 才被采信；空表示一个都不信。
	TrustedProxies []netip.Prefix
}

```

替换为：

```go
	// TrustedProxies 决定限流按哪个来源地址计：只有来自这些对端的 X-Forwarded-For 才被采信；空表示一个都不信。
	TrustedProxies []netip.Prefix
	// Location 是 hub 的 --timezone，days_left 按它的日历日算；NewPublic 要求非 nil。
	Location *time.Location
}

```

`internal/hub/api/public.go` 原文（3/4）：

```go

func NewPublic(cfg PublicConfig, st *store.Store, l *live.Live, book *traffic.Book, probes *probe.Registry, clk clock.Clock, log *slog.Logger) *Public {
	return &Public{
		cfg: cfg, store: st, live: l, traffic: book, probes: probes, clk: clk, log: log,
```

替换为：

```go

func NewPublic(cfg PublicConfig, st *store.Store, l *live.Live, book *traffic.Book, probes *probe.Registry, clk clock.Clock, log *slog.Logger) *Public {
	if cfg.Location == nil {
		panic("api.PublicConfig.Location must be set")
	}
	return &Public{
		cfg: cfg, store: st, live: l, traffic: book, probes: probes, clk: clk, log: log,
```

`internal/hub/api/public.go` 原文（4/4）：

```go
		return nil, internalError("listing nodes failed")
	}
	out := &probev1.PublicSnapshot{Now: p.clk.Now().Unix(), ReportIntervalMs: uint32(p.cfg.ReportInterval / time.Millisecond)}
	for _, n := range nodes {
		online, seen, m := liveState(p.live, n)
		pn := &probev1.PublicNode{Id: n.ID, Name: n.Name, Online: online, LastSeenAt: seen, SortOrder: n.SortOrder, Traffic: trafficProto(p.traffic.View(n.ID))}
		if n.Facts != nil {
			pn.Facts = p.facts.apply(n.Facts).(*probev1.PublicFacts)
```

替换为：

```go
		return nil, internalError("listing nodes failed")
	}
	// now 只读一次：快照的 now 与每个节点的 days_left 出自同一时刻，调用方拿 now 核对 days_left 不会差一天。
	now := p.clk.Now()
	today := alert.Today(now, p.cfg.Location)
	out := &probev1.PublicSnapshot{Now: now.Unix(), ReportIntervalMs: uint32(p.cfg.ReportInterval / time.Millisecond)}
	for _, n := range nodes {
		online, seen, m := liveState(p.live, n)
		pn := &probev1.PublicNode{Id: n.ID, Name: n.Name, Online: online, LastSeenAt: seen, SortOrder: n.SortOrder, Traffic: trafficProto(p.traffic.View(n.ID))}
		// 计费经投影公开：PublicBilling 没有 auto_renew（reserved），它与 Billing 的对齐由 NewPublic 构造投影时核对。
		if b := billingProto(n.Billing, today); b != nil {
			pn.Billing = p.billing.apply(b).(*probev1.PublicBilling)
		}
		if n.Facts != nil {
			pn.Facts = p.facts.apply(n.Facts).(*probev1.PublicFacts)
```

`internal/hub/api/alerts.go` 原文（1/4）：

```go
	probev1.AlertKind_ALERT_KIND_OFFLINE: store.KindOffline,
	probev1.AlertKind_ALERT_KIND_PROBE:   store.KindProbe,
}
var probeMetrics = map[probev1.ProbeMetric]store.ProbeMetric{
```

替换为：

```go
	probev1.AlertKind_ALERT_KIND_OFFLINE: store.KindOffline,
	probev1.AlertKind_ALERT_KIND_PROBE:   store.KindProbe,
	probev1.AlertKind_ALERT_KIND_EXPIRY:  store.KindExpiry,
}
var probeMetrics = map[probev1.ProbeMetric]store.ProbeMetric{
```

`internal/hub/api/alerts.go` 原文（2/4）：

```go

func ruleProto(r store.AlertRule) *probev1.AlertRule {
	return &probev1.AlertRule{Id: r.ID, Name: r.Name, Kind: enumFor(alertKinds, r.Kind), Enabled: r.Enabled, AllNodes: r.AllNodes, NodeIds: r.NodeIDs, ChannelIds: r.ChannelIDs, TaskId: r.TaskID, Metric: enumFor(probeMetrics, r.Metric), Threshold: r.Threshold, ForMinutes: uint32(r.ForMinutes), CreatedAt: r.CreatedAt.Unix()}
}

```

替换为：

```go

func ruleProto(r store.AlertRule) *probev1.AlertRule {
	return &probev1.AlertRule{Id: r.ID, Name: r.Name, Kind: enumFor(alertKinds, r.Kind), Enabled: r.Enabled, AllNodes: r.AllNodes, NodeIds: r.NodeIDs, ChannelIds: r.ChannelIDs, TaskId: r.TaskID, Metric: enumFor(probeMetrics, r.Metric), Threshold: r.Threshold, ForMinutes: uint32(r.ForMinutes), DaysBefore: uint32(r.DaysBefore), CreatedAt: r.CreatedAt.Unix()}
}

```

`internal/hub/api/alerts.go` 原文（3/4）：

```go
	}
	var metric store.ProbeMetric
	// 离线规则不使用探测指标；显式传入的指标仍须属于协议枚举。
	if kind == store.KindProbe || r.GetMetric() != probev1.ProbeMetric_PROBE_METRIC_UNSPECIFIED {
		metric, err = parseEnum(probeMetrics, r.GetMetric(), "rule", "metric")
```

替换为：

```go
	}
	var metric store.ProbeMetric
	// 只有探测规则使用指标。其余种类显式传入的指标也先按协议枚举解析，表外值在这里以协议词汇报错；表内值由
	// alert.CheckRule 以"非探测规则必须不指定"拒绝。
	if kind == store.KindProbe || r.GetMetric() != probev1.ProbeMetric_PROBE_METRIC_UNSPECIFIED {
		metric, err = parseEnum(probeMetrics, r.GetMetric(), "rule", "metric")
```

`internal/hub/api/alerts.go` 原文（4/4）：

```go
		}
	}
	saved, err := s.alerts.SaveRule(ctx, store.AlertRule{ID: r.GetId(), Name: r.GetName(), Kind: kind, Enabled: r.GetEnabled(), AllNodes: r.GetAllNodes(), NodeIDs: r.GetNodeIds(), ChannelIDs: r.GetChannelIds(), TaskID: r.GetTaskId(), Metric: metric, Threshold: r.GetThreshold(), ForMinutes: int(r.GetForMinutes())})
	if err != nil {
		return nil, s.operationError(err, "rule", "saving alert rule failed")
```

替换为：

```go
		}
	}
	saved, err := s.alerts.SaveRule(ctx, store.AlertRule{ID: r.GetId(), Name: r.GetName(), Kind: kind, Enabled: r.GetEnabled(), AllNodes: r.GetAllNodes(), NodeIDs: r.GetNodeIds(), ChannelIDs: r.GetChannelIds(), TaskID: r.GetTaskId(), Metric: metric, Threshold: r.GetThreshold(), ForMinutes: int(r.GetForMinutes()), DaysBefore: int(r.GetDaysBefore())})
	if err != nil {
		return nil, s.operationError(err, "rule", "saving alert rule failed")
```

`cmd/hub/serve.go` 原文：

```go
		return err
	}
	admin := api.New(api.Config{TTL: ttl, ReportInterval: svc.Interval(), TrustedProxies: trusted, HubVersion: version}, st, a, l, svc, book, reg, alerts, notifier, clk, log)
	pub := api.NewPublic(api.PublicConfig{ReportInterval: svc.Interval(), TrustedProxies: trusted}, st, l, book, reg, clk, log)

	mux := newMux(mountOf(svc.Handler()), mountOf(admin.Handler()), mountOf(pub.Handler()), mountOf(web.Prefix, web.Handler()), mountOf("/", public))
```

替换为：

```go
		return err
	}
	admin := api.New(api.Config{TTL: ttl, ReportInterval: svc.Interval(), TrustedProxies: trusted, HubVersion: version, Location: loc}, st, a, l, svc, book, reg, alerts, notifier, clk, log)
	pub := api.NewPublic(api.PublicConfig{ReportInterval: svc.Interval(), TrustedProxies: trusted, Location: loc}, st, l, book, reg, clk, log)

	mux := newMux(mountOf(svc.Handler()), mountOf(admin.Handler()), mountOf(pub.Handler()), mountOf(web.Prefix, web.Handler()), mountOf("/", public))
```

- [ ] **Step 4: 跑绿**

```bash
cd /Users/xjetry/work/vibe/probe-billing && go test -count=1 ./internal/hub/api/ ./cmd/hub/ > /tmp/billing-t4-green-t4.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && go vet ./... > /tmp/billing-t4-vet-t4.log 2>&1; echo $?
```

Expected：两条都是 0。

- [ ] **Step 5: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-billing && git add cmd/hub/mux_test.go cmd/hub/serve.go cmd/hub/serve_alert_test.go internal/hub/api/alert_scope_reload_test.go internal/hub/api/alerts.go internal/hub/api/alerts_test.go internal/hub/api/api_test.go internal/hub/api/billing_test.go internal/hub/api/nodes.go internal/hub/api/public.go internal/hub/api/service.go > /tmp/billing-t4-add.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && git commit -m "api: UpdateNode 校验并保存计费子消息，days_left 按 hub 时区下发，到期规则经协议保存" -m "计费在 UpdateNode 入口一处校验，错误以请求路径写明字段、约束与收到的值；billing 缺失即五项全清，其中的 days_left 不读。到期日与扫描共用 alert.ParseDate，写进去的日期读侧一定读得懂。计费有变化就在返回前同步扫描一次，扫描之后才回读，响应里的到期日与 days_left 已是推后之后的值；扫描在 nodeMu 之外进行，一次计费编辑等 writeMu 时不会挡住其它节点的编辑与删除。days_left 由 hub 按 --timezone 的今天算出，库里读不懂的到期日照原样下发、days_left 缺失（0 会被读成今天到期）；Node.billing 与经投影生成的 PublicBilling 同一来源；公开快照的 now 与 days_left 出自同一次读钟。离线规则带探测字段、探测规则带提前天数都以 InvalidArgument 拒绝。api.Config 与 api.PublicConfig 缺时区即 panic，装配错误在启动时暴露。" > /tmp/billing-t4-commit.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && git status --porcelain > /tmp/billing-t4-status.log 2>&1; echo $?
```

Expected：三条都是 0；`billing-t4-status.log` 为空。

- [ ] **Step 6: make ci**

```bash
cd /Users/xjetry/work/vibe/probe-billing && make ci > /tmp/billing-t4-ci.log 2>&1; echo $?
```

Expected：0；vitest `Tests  369 passed (369)`。`make ci` 最后核对生成物已入库，所以放在提交之后。

- [ ] **Step 7: 缺陷注入**

| | 改动 | 命令 | 期望 |
|---|---|---|---|
| a | 价格正则去掉 `^` 与 `$` | `go test -count=1 -run 'TestUpdateNodeRejectsMalformedBilling' ./internal/hub/api/ > /tmp/billing-t4-inj-a.log 2>&1; echo $?` | 1，`billing_test.go:91: price:"12.345" currency:"USD": err = <nil>, want billing.price: must match ^\d{1,9}(\.\d{1,2})?$, e.g. 12.50; got "12.345"`，`1234567890`、`-1`、`12.`、`.5`、`1e3`、`" 12"` 各一行。全角 `１２` 那一行也红：去掉锚点后它照样被拒（RE2 的 `\d` 只认 ASCII），红是因为错误原文里插入的是改动后的正则。另有 `billing_test.go:96: rejected updates changed the node` 一行：最后一个被放行的 `" 12"` 存下了 |
| b | 删掉"价格非空时币种必填" | `go test -count=1 -run 'TestUpdateNodeRejectsMalformedBilling' ./internal/hub/api/ > /tmp/billing-t4-inj-b.log 2>&1; echo $?` | 1，`billing_test.go:91: price:"12": err = <nil>, want billing.currency: required when billing.price is set` 与 `billing_test.go:96: rejected updates changed the node` |
| c | 币种正则加 `(?i)`（接受小写） | `go test -count=1 -run 'TestUpdateNodeRejectsMalformedBilling' ./internal/hub/api/ > /tmp/billing-t4-inj-c.log 2>&1; echo $?` | 1，`… currency:"usd": err = <nil>, want billing.currency: must be three uppercase letters (ISO 4217), e.g. USD; got "usd"` 与 `rejected updates changed the node` |
| d | 表外的周期值不拒绝（`if !ok` 改成 `if ok = true; !ok`） | `go test -count=1 -run 'TestUpdateNodeRejectsMalformedBilling' ./internal/hub/api/ > /tmp/billing-t4-inj-d.log 2>&1; echo $?` | 1，`… billing_cycle:99: err = <nil>, want billing.billing_cycle: must be one of BILLING_CYCLE_UNSPECIFIED, …; got 99` 与 `rejected updates changed the node` |
| e | 到期日不校验（`ParseDate` 的错误条件后加 `&& false`） | `go test -count=1 -run 'TestUpdateNodeRejectsMalformedBilling' ./internal/hub/api/ > /tmp/billing-t4-inj-e.log 2>&1; echo $?` | 1，`… expires_on:"2026-02-29": err = <nil>, want billing.expires_on: must be an existing date in YYYY-MM-DD form; got "2026-02-29"`，另两种写法各一行 |
| f | 自动续期只在周期与到期日都缺时才拒绝（`\|\|` 改成 `&&`） | `go test -count=1 -run 'TestUpdateNodeRejectsMalformedBilling' ./internal/hub/api/ > /tmp/billing-t4-inj-f.log 2>&1; echo $?` | 1，`… expires_on:"2026-10-01" auto_renew:true: err = <nil>, want billing.auto_renew: requires billing.billing_cycle and billing.expires_on to be set`，只带周期的一行同样 |
| g | `Service.today` 用 `time.UTC` | `go test -count=1 -run 'TestDaysLeftUsesTheHubZone' ./internal/hub/api/ > /tmp/billing-t4-inj-g.log 2>&1; echo $?` | 1，`billing_test.go:145: UpdateNode days_left = 9, want 8` |
| h | `GetSnapshot` 不填 `PublicNode.billing`：投影那段的条件改成 `b != nil && false`（整段删掉会让 `today` 未使用，编译失败） | `go test -count=1 -run 'TestPublicSnapshotCarriesBilling\|TestDaysLeftUsesTheHubZone' ./internal/hub/api/ > /tmp/billing-t4-inj-h.log 2>&1; echo $?` | 1，`billing_test.go:153: public snapshot = …`（节点没有 `billing`）与 `billing_test.go:179: public billing of a = <nil>, want price:"12.50" … days_left:-3` |
| i | 不论计费是否变化都扫描（`if billingChanged {` 改成 `if billingChanged \|\| true {`） | `go test -count=1 -run 'TestUpdateNodeSweepsExpiryWhenBillingChanges' ./internal/hub/api/ > /tmp/billing-t4-inj-i.log 2>&1; echo $?` | 1，`billing_test.go:276: a rename swept expiry: … billing:{… expires_on:"2026-02-15" auto_renew:true days_left:26}` |
| j | 计费变化也不扫描（`if billingChanged && false {`） | `go test -count=1 -run 'TestUpdateNodeSweepsExpiryWhenBillingChanges\|TestUpdateNodeFiresAndRecovers' ./internal/hub/api/ > /tmp/billing-t4-inj-j.log 2>&1; echo $?` | 1，`billing_test.go:281: a billing change did not renew: … expires_on:"2026-01-15" … days_left:-5}` 与 `billing_test.go:304: events after setting a close date: []` |
| k | 先 `GetNode` 读回响应、后扫描（响应不含推后的日期） | `go test -count=1 -run 'TestUpdateNodeSweepsExpiryWhenBillingChanges' ./internal/hub/api/ > /tmp/billing-t4-inj-k.log 2>&1; echo $?` | 1，`billing_test.go:281: a billing change did not renew: …`：库里已推后，响应是推后之前读出的 |
| l | `alertKinds` 删掉 `EXPIRY` 一行 | `go test -count=1 -run 'TestSaveAlertRuleExpiryKind' ./internal/hub/api/ > /tmp/billing-t4-inj-l.log 2>&1; echo $?` | 1，`billing_test.go:320: invalid_argument: rule.kind must be one of ALERT_KIND_OFFLINE, ALERT_KIND_PROBE; got "ALERT_KIND_EXPIRY"` |
| m | `ruleProto` 不填 `DaysBefore` | `go test -count=1 -run 'TestSaveAlertRuleExpiryKind' ./internal/hub/api/ > /tmp/billing-t4-inj-m.log 2>&1; echo $?` | 1，`billing_test.go:322: saved id:1 name:"到期" kind:ALERT_KIND_EXPIRY enabled:true created_at:1767225600 all_nodes:true`：回显没有 `days_before` |
| n | `SaveAlertRule` 不把 `days_before` 传给引擎 | `go test -count=1 -run 'TestSaveAlertRuleExpiryKind' ./internal/hub/api/ > /tmp/billing-t4-inj-n.log 2>&1; echo $?` | 1，`billing_test.go:320: invalid_argument: rule.days_before must be between 1 and 365` |
| o | `api.New` 不检查 `Location` | `go test -count=1 -run 'TestConstructorsRequireLocation' ./internal/hub/api/ > /tmp/billing-t4-inj-o.log 2>&1; echo $?` | 1，`billing_test.go:389: panic = <nil>, want api.Config.Location must be set` |
| p | `serve.go` 给 `api.New` 传 `time.UTC` | `go test -count=1 -run 'TestServeReportsDaysLeftInTheHubZone' ./cmd/hub/ > /tmp/billing-t4-inj-p.log 2>&1; echo $?` | 1，`serve_alert_test.go:295: admin ListNodes = … billing:{expires_on:"2026-09-30" days_left:6} …, want days_left 5` |
| q | `serve.go` 给 `api.NewPublic` 传 `time.UTC` | `go test -count=1 -run 'TestServeReportsDaysLeftInTheHubZone' ./cmd/hub/ > /tmp/billing-t4-inj-q.log 2>&1; echo $?` | 1，`serve_alert_test.go:300: public GetSnapshot = … billing:{expires_on:"2026-09-30" days_left:6} …, want days_left 5` |
| r | `UpdateNode` 的响应沿用请求里的 `days_left`：最后的 `return` 拆成 `out := nodeProto(n, s.today())`，接着 `if b := req.Msg.GetBilling(); b != nil && b.DaysLeft != nil && out.Billing != nil { out.Billing.DaysLeft = b.DaysLeft }`，再返回 `Node: out`。必须判 nil：e2e 里改重置日的那次 `UpdateNode` 不带 `billing`，写成 `req.Msg.GetBilling().DaysLeft` 会解引用 nil 指针，hub 的处理协程 panic，e2e 红在 `FAIL: UpdateNode reset day` 而不是表里那一行（写计划时实跑过） | `go test -count=1 -run 'TestUpdateNodeAcceptsBoundaryBillingAndIgnoresDaysLeft' ./internal/hub/api/ > /tmp/billing-t4-inj-r.log 2>&1; echo $?` | 1，`billing_test.go:121: days_left = 999, want 9 computed by the hub` |
| s | `billingProto` 对五项全空的计费也返回非 nil（删掉开头的 `return nil`） | `go test -count=1 -run 'TestUpdateNodeAcceptsBoundaryBillingAndIgnoresDaysLeft\|TestPublicSnapshotCarriesBilling' ./internal/hub/api/ > /tmp/billing-t4-inj-s.log 2>&1; echo $?` | 1，`billing_test.go:126: billing  did not clear: … billing:{}` 与 `billing_test.go:182: node without billing = … billing:{}` |
| t | `CheckRule` 的离线分支不调 `checkKindFields`（与 Task 3 注入 m 同一处，这里跑协议层的测试） | `go test -count=1 -run 'TestSaveAlertRuleRejectsFieldsOfOtherKinds' ./internal/hub/api/ > /tmp/billing-t4-inj-t.log 2>&1; echo $?` | 1，`billing_test.go:369: … kind:ALERT_KIND_OFFLINE … task_id:1 …: err = internal: saving alert rule failed, want rule.task_id must be 0 unless kind is probe`，metric、threshold、for_minutes、days_before 各一行：store 的判定挡住了写入，但错误没转成字段错误，协议层只见 internal；没有 `rejected rules were saved` |
| u | `billingProto` 只对空串不给 `days_left`，读不懂的日期给 0 | `go test -count=1 -run 'TestUnreadableExpiresOnHasNoDaysLeft' ./internal/hub/api/ > /tmp/billing-t4-inj-u.log 2>&1; echo $?` | 1，`billing_test.go:210: billing = expires_on:"2026-02-30" days_left:0, want expires_on kept and days_left missing` |
| v | `GetSnapshot` 分两次读钟：先为 `today` 读一次，再读 `now` | `go test -count=1 -run 'TestPublicSnapshotReadsTheClockOnce\|TestDaysLeftUsesTheHubZone\|TestPublicSnapshotCarriesBilling' ./internal/hub/api/ > /tmp/billing-t4-inj-v.log 2>&1; echo $?` | 1，只有 `TestPublicSnapshotReadsTheClockOnce` 红：`billing_test.go:254: now 1767369599 is 2026-01-02 in the hub zone, so days_left should be 58; got 59`；另两个用例的钟不走，照常通过。反过来的顺序（先读 `now`）红在同一条断言，数字对调：`now 1767283199 is 2026-01-01 in the hub zone, so days_left should be 59; got 58` |
| w | `alertKinds` 删掉 `EXPIRY` 一行（同注入 l，这里跑全集用例） | `go test -count=1 -run 'TestAlertKindsMapEveryValue' ./internal/hub/api/ > /tmp/billing-t4-inj-w.log 2>&1; echo $?` | 1，`alerts_test.go:499: ALERT_KIND_EXPIRY has no stored kind in alertKinds` 与 `alerts_test.go:507: alertKinds has 2 entries, want one per protocol kind except UNSPECIFIED (3)` |
| x | `CheckRule` 不认到期种类（`case store.KindExpiry:` 改成 `case "expiry_disabled":`，到期规则落到 `default` 被拒） | `go test -count=1 -run 'TestAlertKindsMapEveryValue' ./internal/hub/api/ > /tmp/billing-t4-inj-x.log 2>&1; echo $?` | 1，`alerts_test.go:522: alert.CheckRule rejects the stored kind "expiry" of ALERT_KIND_EXPIRY: invalid: kind must be one of offline, probe, expiry; got "expiry"`：翻译表配上了，校验却不认这个种类 |

每项：改动 → `git diff --stat` 非空 → 跑命令 → 核对原因 → `git checkout -- internal/hub cmd/hub`（t 改的是 `internal/hub/alert/rule.go`）。

---

### Task 5: web：共用的计费显示与节点页的计费列

**Files:**
- Create: `web/src/lib/billing.ts`、`web/src/lib/billing.test.ts`
- Modify: `web/src/pages/Nodes.tsx`（计费列；编辑草稿带 `billing` 子对象；`BillingSummary`、`BillingEditor`）
- Modify: `web/src/styles.css`（`.billing-edit`）
- Test: `web/src/pages/Nodes.test.tsx`

**Interfaces:**
- Consumes：Task 1 的 TS 生成代码（`BillingCycle`、`Node.billing`）；Task 4 的 `UpdateNode` 行为。
- Produces（`web/src/lib/billing.ts`，只 import `types_pb`）：
  - `type BillingView = { price: string; currency: string; billingCycle: BillingCycle; expiresOn: string; daysLeft?: number }`：`Billing` 与 `PublicBilling` 共有的字段
  - `BILLING_CYCLES`（六项，标签月、季、半年、年、两年、三年）
  - `cycleLabel(c)`；`priceText(b)`、`expiryText(b)`、`expired(b)`，三者都接受 `BillingView | undefined`
- Produces（`Nodes.tsx`）：草稿的 `billing` 子对象（`type BillingDraft = Draft["billing"]`），节点没有 `billing` 时各项从空值开始。

- [ ] **Step 1: 写失败测试**

`billing.test.ts` 钉住：
- 周期表覆盖协议枚举里除未指定之外的每个值；未指定是没有周期，表外值显示编号。
- 价格、周期与到期的合成文案，剩 0 天不算过期。
- 没有 `billing` 的节点：三个函数都当作什么都没填。
- 全部用例把 Date 钉在 2030-06-15（`beforeEach` 里 `vi.useFakeTimers({ toFake: ["Date"] })` 与 `vi.setSystemTime("2030-06-15T12:00:00Z")`，`afterEach` 里 `vi.useRealTimers()`）。夹具的 `daysLeft` 是 hub 下发的值，与浏览器今天无关：按本地日期重算的实现在任何时区都与夹具不同而红；不钉时夹具恰好等于某一天的日历差，那一天照样全绿。只 fake Date，计时器保持真实。

`Nodes.test.tsx` 的计费用例放进 `describe("计费")`，同样把 Date 钉在 2030-06-15（react-query 与 `waitFor` 用的计时器保持真实）；节点夹具的计费写成嵌套的 `billing`：
- 计费列的合成与标红，全空时是破折号。
- 编辑随整行提交，币种即时转大写；提交体里的 `billing` 用 `expect.objectContaining` 比对（实际对象带 `$typeName`）。
- 编辑从当前值开始，清空后提交空值。
- hub 以 `InvalidArgument` 拒绝计费取值时，列表上方显示错误原文，编辑行与五项草稿保留。

既有的"编辑回传全部字段"改用带计费的节点，只改名称时断言提交体带着当前五项：`UpdateNode` 整体替换，`billing` 缺失即五项全清。

新建 `web/src/lib/billing.test.ts`（整份）：

```ts
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { BillingCycle, BillingCycleSchema } from "../gen/probe/v1/types_pb";
import { BILLING_CYCLES, cycleLabel, expired, expiryText, priceText, type BillingView } from "./billing";

const none: BillingView = { price: "", currency: "", billingCycle: BillingCycle.UNSPECIFIED, expiresOn: "" };

// 夹具的 daysLeft 是 hub 下发的值，与"到期日减去浏览器今天"无关。时钟钉在离夹具几年之外的日期：在任何时区里，
// 按本地日期重算出的天数都与夹具不同，用 Date 自己算的实现一定红；不钉时，夹具恰好等于某一天的日历差，那一天
// 本地重算照样全绿。只 fake Date，计时器保持真实。
beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime("2030-06-15T12:00:00Z");
});
afterEach(() => { vi.useRealTimers(); });

describe("cycleLabel", () => {
  it("周期表覆盖协议枚举里除未指定之外的每个值", () => {
    for (const { number, name } of BillingCycleSchema.values) {
      if (number === BillingCycle.UNSPECIFIED) continue;
      expect(cycleLabel(number), name).not.toMatch(/^(|未知（\d+）)$/);
    }
    expect(BILLING_CYCLES.map((e) => e.label)).toEqual(["月", "季", "半年", "年", "两年", "三年"]);
  });
  it("未指定是没有周期，表外值显示编号", () => {
    expect([cycleLabel(BillingCycle.UNSPECIFIED), cycleLabel(9 as BillingCycle)]).toEqual(["", "未知（9）"]);
  });
});

describe("priceText", () => {
  it.each([
    [{ price: "12.50", currency: "USD", billingCycle: BillingCycle.MONTHLY }, "USD 12.50 / 月"],
    [{ price: "99", currency: "EUR", billingCycle: BillingCycle.TRIENNIAL }, "EUR 99 / 三年"],
    [{ price: "30", currency: "CNY" }, "CNY 30"],
    [{ billingCycle: BillingCycle.YEARLY }, "每年"],
    [{ currency: "USD" }, ""],
    [{}, ""],
  ])("%o → %s", (b, want) => {
    expect(priceText({ ...none, ...b })).toBe(want);
  });
});

describe("expiryText", () => {
  it.each([
    [{ expiresOn: "2026-10-01", daysLeft: 4 }, "2026-10-01（剩 4 天）", false],
    [{ expiresOn: "2026-09-27", daysLeft: 0 }, "2026-09-27（剩 0 天）", false],
    [{ expiresOn: "2026-09-24", daysLeft: -3 }, "2026-09-24（已过期 3 天）", true],
    [{ expiresOn: "2026-10-01" }, "2026-10-01", false],
    [{}, "", false],
  ])("%o → %s", (b, want, isExpired) => {
    expect(expiryText({ ...none, ...b })).toBe(want);
    expect(expired({ ...none, ...b })).toBe(isExpired);
  });
});

describe("没有 billing 的节点", () => {
  it("三个函数都当作什么都没填", () => {
    expect([priceText(undefined), expiryText(undefined), expired(undefined)]).toEqual(["", "", false]);
  });
});
```

`web/src/pages/Nodes.test.tsx` 原文（1/4）：

```tsx
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { renderWithAdmin, type AdminImpl } from "../test/harness";
```

替换为：

```tsx
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithAdmin, type AdminImpl } from "../test/harness";
```

`web/src/pages/Nodes.test.tsx` 原文（2/4）：

```tsx
import { AdminService, GetSnapshotResponseSchema, GetRegisterWindowResponseSchema } from "../gen/probe/v1/admin_pb";

```

替换为：

```tsx
import { AdminService, GetSnapshotResponseSchema, GetRegisterWindowResponseSchema } from "../gen/probe/v1/admin_pb";
import { BillingCycle } from "../gen/probe/v1/types_pb";

```

`web/src/pages/Nodes.test.tsx` 原文（3/4）：

```tsx

  it("编辑回传全部字段", async () => {
    const updateNode = vi.fn(async () => ({ node: two[0] }));
    renderNodes({ listNodes: async () => ({ nodes: two }), updateNode });
    await screen.findByRole("link", { name: "a（#1）" });
```

替换为：

```tsx

  it("编辑回传全部字段，没碰的计费也按当前值回传", async () => {
    const updateNode = vi.fn(async () => ({ node: two[0] }));
    // UpdateNode 整体替换，billing 缺失等于五项全清：只改名称时提交体里的计费必须是节点当前的五项。
    const billed = { ...two[0], billing: { price: "9", currency: "EUR", billingCycle: BillingCycle.QUARTERLY, expiresOn: "2026-12-01", daysLeft: 60, autoRenew: true } };
    renderNodes({ listNodes: async () => ({ nodes: [billed, two[1]] }), updateNode });
    await screen.findByRole("link", { name: "a（#1）" });
```

`web/src/pages/Nodes.test.tsx` 原文（4/4）：

```tsx
    fireEvent.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(updateNode).toHaveBeenCalledWith(expect.objectContaining({ id: 1n, name: "a2", public: true, note: "changed note", trafficResetDay: 15 }), expect.anything()));
  });
```

替换为：

```tsx
    fireEvent.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(updateNode).toHaveBeenCalledWith(expect.objectContaining({
      id: 1n, name: "a2", public: true, note: "changed note", trafficResetDay: 15,
      billing: expect.objectContaining({ price: "9", currency: "EUR", billingCycle: BillingCycle.QUARTERLY, expiresOn: "2026-12-01", autoRenew: true }),
    }), expect.anything()));
  });

  describe("计费", () => {
    // 夹具的 daysLeft 是 hub 下发的值。时钟钉在离夹具几年之外的日期，按浏览器本地日期重算的实现在任何时区都与夹具
    // 不同而红；不钉时夹具恰好等于某一天的日历差，那一天本地重算照样全绿。只 fake Date，react-query 与 waitFor
    // 用的计时器保持真实。
    beforeEach(() => {
      vi.useFakeTimers({ toFake: ["Date"] });
      vi.setSystemTime("2030-06-15T12:00:00Z");
    });
    afterEach(() => { vi.useRealTimers(); });

    it("计费列合成价格、周期、到期与自动续期，已过期的那段标红，全空是破折号", async () => {
      renderNodes({ listNodes: async () => ({ nodes: [
        { ...two[0], billing: { price: "12.50", currency: "USD", billingCycle: BillingCycle.MONTHLY, expiresOn: "2026-10-01", daysLeft: 4, autoRenew: true } },
        { ...two[1], billing: { price: "", currency: "", billingCycle: BillingCycle.UNSPECIFIED, expiresOn: "2026-09-24", daysLeft: -3, autoRenew: false } },
        { ...two[0], id: 3n, name: "c" },
      ] }) });
      const row = async (name: string) => within((await screen.findByRole("link", { name })).closest("tr")!);
      expect((await row("a（#1）")).getByRole("cell", { name: "USD 12.50 / 月 · 2026-10-01（剩 4 天） · 自动续期" })).toBeInTheDocument();
      expect((await row("a（#1）")).getByText("2026-10-01（剩 4 天）")).not.toHaveClass("error");
      expect((await row("b（#2）")).getByText("2026-09-24（已过期 3 天）")).toHaveClass("error");
      expect((await row("c（#3）")).getByRole("cell", { name: "—" })).toBeInTheDocument();
    });

    it("编辑计费随整行整体提交，币种输入即转大写", async () => {
      const updateNode = vi.fn(async () => ({}));
      renderNodes({ listNodes: async () => ({ nodes: two }), updateNode });
      await screen.findByRole("link", { name: "a（#1）" });
      fireEvent.click(screen.getByRole("button", { name: "编辑 a（#1）" }));
      fireEvent.change(screen.getByLabelText("价格 a（#1）"), { target: { value: "12.50" } });
      fireEvent.change(screen.getByLabelText("币种 a（#1）"), { target: { value: "usd" } });
      fireEvent.change(screen.getByLabelText("周期 a（#1）"), { target: { value: String(BillingCycle.YEARLY) } });
      fireEvent.change(screen.getByLabelText("到期日 a（#1）"), { target: { value: "2027-01-31" } });
      fireEvent.click(screen.getByLabelText("自动续期 a（#1）"));
      expect(screen.getByLabelText("币种 a（#1）")).toHaveValue("USD");
      fireEvent.change(screen.getByLabelText("名称 a（#1）"), { target: { value: "a2" } });
      fireEvent.click(screen.getByRole("button", { name: "保存" }));
      await waitFor(() => expect(updateNode).toHaveBeenCalledWith(expect.objectContaining({
        id: 1n, name: "a2", billing: expect.objectContaining({ price: "12.50", currency: "USD", billingCycle: BillingCycle.YEARLY, expiresOn: "2027-01-31", autoRenew: true }),
      }), expect.anything()));
    });

    it("编辑从节点当前的计费开始，清空之后提交的是空值", async () => {
      const updateNode = vi.fn(async () => ({}));
      const current = { ...two[0], billing: { price: "9", currency: "EUR", billingCycle: BillingCycle.QUARTERLY, expiresOn: "2026-12-01", daysLeft: 60, autoRenew: true } };
      renderNodes({ listNodes: async () => ({ nodes: [current] }), updateNode });
      await screen.findByRole("link", { name: "a（#1）" });
      fireEvent.click(screen.getByRole("button", { name: "编辑 a（#1）" }));
      expect({
        price: (screen.getByLabelText("价格 a（#1）") as HTMLInputElement).value,
        currency: (screen.getByLabelText("币种 a（#1）") as HTMLInputElement).value,
        cycle: (screen.getByLabelText("周期 a（#1）") as HTMLSelectElement).value,
        expiresOn: (screen.getByLabelText("到期日 a（#1）") as HTMLInputElement).value,
        autoRenew: (screen.getByLabelText("自动续期 a（#1）") as HTMLInputElement).checked,
      }).toEqual({ price: "9", currency: "EUR", cycle: String(BillingCycle.QUARTERLY), expiresOn: "2026-12-01", autoRenew: true });
      fireEvent.change(screen.getByLabelText("价格 a（#1）"), { target: { value: "" } });
      fireEvent.change(screen.getByLabelText("币种 a（#1）"), { target: { value: "" } });
      fireEvent.change(screen.getByLabelText("周期 a（#1）"), { target: { value: String(BillingCycle.UNSPECIFIED) } });
      fireEvent.change(screen.getByLabelText("到期日 a（#1）"), { target: { value: "" } });
      fireEvent.click(screen.getByLabelText("自动续期 a（#1）"));
      fireEvent.click(screen.getByRole("button", { name: "保存" }));
      await waitFor(() => expect(updateNode).toHaveBeenCalledWith(expect.objectContaining({
        id: 1n, billing: expect.objectContaining({ price: "", currency: "", billingCycle: BillingCycle.UNSPECIFIED, expiresOn: "", autoRenew: false }),
      }), expect.anything()));
    });

    it("hub 拒绝计费取值时显示错误原文，编辑行与五项草稿保留", async () => {
      const message = "billing.currency: must be three uppercase letters (ISO 4217); got \"US\"";
      const updateNode = vi.fn(async () => { throw new ConnectError(message, Code.InvalidArgument); });
      renderNodes({ listNodes: async () => ({ nodes: two }), updateNode });
      await screen.findByRole("link", { name: "a（#1）" });
      fireEvent.click(screen.getByRole("button", { name: "编辑 a（#1）" }));
      fireEvent.change(screen.getByLabelText("价格 a（#1）"), { target: { value: "12.50" } });
      fireEvent.change(screen.getByLabelText("币种 a（#1）"), { target: { value: "us" } });
      fireEvent.change(screen.getByLabelText("周期 a（#1）"), { target: { value: String(BillingCycle.MONTHLY) } });
      fireEvent.change(screen.getByLabelText("到期日 a（#1）"), { target: { value: "2030-07-01" } });
      fireEvent.click(screen.getByLabelText("自动续期 a（#1）"));
      fireEvent.click(screen.getByRole("button", { name: "保存" }));
      expect(await screen.findByRole("alert")).toHaveTextContent(message);
      expect(updateNode).toHaveBeenCalledTimes(1);
      expect({
        price: (screen.getByLabelText("价格 a（#1）") as HTMLInputElement).value,
        currency: (screen.getByLabelText("币种 a（#1）") as HTMLInputElement).value,
        cycle: (screen.getByLabelText("周期 a（#1）") as HTMLSelectElement).value,
        expiresOn: (screen.getByLabelText("到期日 a（#1）") as HTMLInputElement).value,
        autoRenew: (screen.getByLabelText("自动续期 a（#1）") as HTMLInputElement).checked,
      }).toEqual({ price: "12.50", currency: "US", cycle: String(BillingCycle.MONTHLY), expiresOn: "2030-07-01", autoRenew: true });
    });
  });
```

- [ ] **Step 2: 跑红**

```bash
cd /Users/xjetry/work/vibe/probe-billing && pnpm --dir web exec vitest run src/lib/billing.test.ts src/pages/Nodes.test.tsx > /tmp/billing-t5-red.log 2>&1; echo $?
```

Expected：1。原文节选：
- `FAIL  src/lib/billing.test.ts`：`Error: Failed to resolve import "./billing" from "src/lib/billing.test.ts". Does the file exist?`
- `FAIL  src/pages/Nodes.test.tsx > Nodes > 计费 > 计费列合成价格、周期、到期与自动续期，已过期的那段标红，全空是破折号`：`Unable to find an accessible element with the role "cell" and name "USD 12.50 / 月 · 2026-10-01（剩 4 天） · 自动续期"`
- 另三个计费用例：`Unable to find a label with the text of: 价格 a（#1）`
- `Nodes > 编辑回传全部字段，没碰的计费也按当前值回传`：`AssertionError: expected "vi.fn()" to be called with arguments: [ ObjectContaining{…}, Anything ]`（实现之前的编辑不回传计费）
- `Tests  5 failed | 46 passed (51)`

- [ ] **Step 3: 实现**

新建 `web/src/lib/billing.ts`（整份）：

```ts
import { BillingCycle } from "../gen/probe/v1/types_pb";

// 面板的 Billing（types_pb）与公开页的 PublicBilling（public_pb）共有的字段（§9.4）；自动续期只在管理端有，这里不用。
// 节点五项都没填时 hub 不下发 billing，所以下面的函数都接受 undefined。
// 本文件只 import types_pb：公开入口也引用它，不能经它带进管理服务的生成代码（importScan.test.ts 钉住）。
export type BillingView = {
  price: string;
  currency: string;
  billingCycle: BillingCycle;
  expiresOn: string;
  // hub 按 --timezone 算好的剩余天数；负数是已过期天数，没有到期日时缺失。浏览器不自己再算：访客的时区与 hub 不同时，按本地日期算可能差一天。
  daysLeft?: number;
};

export const BILLING_CYCLES: readonly { value: BillingCycle; label: string }[] = [
  { value: BillingCycle.MONTHLY, label: "月" },
  { value: BillingCycle.QUARTERLY, label: "季" },
  { value: BillingCycle.SEMIANNUAL, label: "半年" },
  { value: BillingCycle.YEARLY, label: "年" },
  { value: BillingCycle.BIENNIAL, label: "两年" },
  { value: BillingCycle.TRIENNIAL, label: "三年" },
];

// 未指定是"没有周期"，给空串；表外的值（hub 比页面新）显示编号而不是空，免得读成没有周期。
export function cycleLabel(c: BillingCycle): string {
  if (c === BillingCycle.UNSPECIFIED) return "";
  return BILLING_CYCLES.find((e) => e.value === c)?.label ?? `未知（${c}）`;
}

// "USD 12.50 / 月"；没有价格但有周期时是"每月"；都没有时是空串。
export function priceText(b: BillingView | undefined): string {
  if (!b) return "";
  const cycle = cycleLabel(b.billingCycle);
  if (b.price === "") return cycle && `每${cycle}`;
  const amount = `${b.currency} ${b.price}`;
  return cycle ? `${amount} / ${cycle}` : amount;
}

// "2026-10-01（剩 4 天）" 或 "2026-09-24（已过期 3 天）"；没有到期日时是空串。
export function expiryText(b: BillingView | undefined): string {
  if (!b || b.expiresOn === "") return "";
  if (b.daysLeft === undefined) return b.expiresOn;
  return b.daysLeft < 0 ? `${b.expiresOn}（已过期 ${-b.daysLeft} 天）` : `${b.expiresOn}（剩 ${b.daysLeft} 天）`;
}

export const expired = (b: BillingView | undefined): boolean => b?.daysLeft !== undefined && b.daysLeft < 0;
```

`web/src/pages/Nodes.tsx` 原文（1/8）：

```tsx
import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { Link } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
```

替换为：

```tsx
import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, Fragment, type ReactNode, useState } from "react";
import { Link } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
```

`web/src/pages/Nodes.tsx` 原文（2/8）：

```tsx
import { Secret } from "../components/Secret";
import { AdminService, type Node } from "../gen/probe/v1/admin_pb";
import { errorText } from "../api/auth";
import { useLatestError } from "../api/useLatestError";
import { graceText } from "../lib/alerts";
import { withId } from "../lib/ids";
import { lagsHub } from "../lib/version";
```

替换为：

```tsx
import { Secret } from "../components/Secret";
import { AdminService, type Node } from "../gen/probe/v1/admin_pb";
import { BillingCycle } from "../gen/probe/v1/types_pb";
import { errorText } from "../api/auth";
import { useLatestError } from "../api/useLatestError";
import { graceText } from "../lib/alerts";
import { BILLING_CYCLES, expired, expiryText, priceText } from "../lib/billing";
import { withId } from "../lib/ids";
import { lagsHub } from "../lib/version";
```

`web/src/pages/Nodes.tsx` 原文（3/8）：

```tsx
      <div className="table-scroll" role="region" aria-label="节点管理" tabIndex={0}>
        <table className="nodes">
          <thead><tr><th>排序</th><th>名称</th><th>公开</th><th>备注</th><th>重置日</th><th>离线宽限期</th><th>创建于</th><th>操作</th></tr></thead>
          <tbody>
            {list.map((n, i) => (
```

替换为：

```tsx
      <div className="table-scroll" role="region" aria-label="节点管理" tabIndex={0}>
        <table className="nodes">
          <thead><tr><th>排序</th><th>名称</th><th>公开</th><th>备注</th><th>重置日</th><th>离线宽限期</th><th>计费</th><th>创建于</th><th>操作</th></tr></thead>
          <tbody>
            {list.map((n, i) => (
```

`web/src/pages/Nodes.tsx` 原文（4/8）：

```tsx
const validResetDay = (day: number) => Number.isInteger(day) && day >= 1 && day <= 28;

// 宽限期以字符串编辑，0 表示清除（取 hub 的 PROBE_OFFLINE_AFTER）。
const draftOf = (node: Node) => ({
  name: node.name, public: node.public, note: node.note, trafficResetDay: node.trafficResetDay,
  offlineGraceS: String(node.offlineGraceS ?? 0),
});
const validGrace = (s: string) => /^\d+$/.test(s);

```

替换为：

```tsx
const validResetDay = (day: number) => Number.isInteger(day) && day >= 1 && day <= 28;

// 宽限期以字符串编辑，0 表示清除（取 hub 的 PROBE_OFFLINE_AFTER）。计费五项随整行整体提交（UpdateNode 整体替换），
// 节点没有 billing 时从空值开始；取值约束由 hub 裁决并把错误原文显示在列表上方，页面不另抄一份规则。
const draftOf = (node: Node) => ({
  name: node.name, public: node.public, note: node.note, trafficResetDay: node.trafficResetDay,
  offlineGraceS: String(node.offlineGraceS ?? 0),
  billing: {
    price: node.billing?.price ?? "", currency: node.billing?.currency ?? "", billingCycle: node.billing?.billingCycle ?? BillingCycle.UNSPECIFIED,
    expiresOn: node.billing?.expiresOn ?? "", autoRenew: node.billing?.autoRenew ?? false,
  },
});
type Draft = ReturnType<typeof draftOf>;
type BillingDraft = Draft["billing"];
const validGrace = (s: string) => /^\d+$/.test(s);

```

`web/src/pages/Nodes.tsx` 原文（5/8）：

```tsx
  saving: boolean; deleting: boolean; rotating: boolean;
  onMoveUp: () => void; onMoveDown: () => void;
  onSave: (patch: { name: string; public: boolean; note: string; trafficResetDay: number; offlineGraceS: number }, onSuccess: () => void) => void;
  onDelete: () => void; onRotate: () => void;
}) {
```

替换为：

```tsx
  saving: boolean; deleting: boolean; rotating: boolean;
  onMoveUp: () => void; onMoveDown: () => void;
  onSave: (patch: Omit<Draft, "offlineGraceS"> & { offlineGraceS: number }, onSuccess: () => void) => void;
  onDelete: () => void; onRotate: () => void;
}) {
```

`web/src/pages/Nodes.tsx` 原文（6/8）：

```tsx
        <td><input type="number" min={1} max={28} aria-label={`重置日 ${withId(node.name, node.id)}`} value={draft.trafficResetDay} onChange={(e) => setDraft({ ...draft, trafficResetDay: Number(e.target.value) })} /><p className="muted">若从本周期起点算起新的重置日已经过去，本周期用量会立即清零。</p></td>
        <td><input type="number" min={0} aria-label={`离线宽限期（秒） ${withId(node.name, node.id)}`} aria-describedby={`grace-hint-${node.id}`} value={draft.offlineGraceS} onChange={(e) => setDraft({ ...draft, offlineGraceS: e.target.value })} /><p className="muted" id={`grace-hint-${node.id}`}>0 表示取 hub 的 PROBE_OFFLINE_AFTER；非 0 不能小于它。</p></td>
        <td />
        <td>
```

替换为：

```tsx
        <td><input type="number" min={1} max={28} aria-label={`重置日 ${withId(node.name, node.id)}`} value={draft.trafficResetDay} onChange={(e) => setDraft({ ...draft, trafficResetDay: Number(e.target.value) })} /><p className="muted">若从本周期起点算起新的重置日已经过去，本周期用量会立即清零。</p></td>
        <td><input type="number" min={0} aria-label={`离线宽限期（秒） ${withId(node.name, node.id)}`} aria-describedby={`grace-hint-${node.id}`} value={draft.offlineGraceS} onChange={(e) => setDraft({ ...draft, offlineGraceS: e.target.value })} /><p className="muted" id={`grace-hint-${node.id}`}>0 表示取 hub 的 PROBE_OFFLINE_AFTER；非 0 不能小于它。</p></td>
        <td><BillingEditor label={withId(node.name, node.id)} draft={draft.billing} onChange={(patch) => setDraft({ ...draft, billing: { ...draft.billing, ...patch } })} /></td>
        <td />
        <td>
```

`web/src/pages/Nodes.tsx` 原文（7/8）：

```tsx
      <td>每月 {node.trafficResetDay} 日</td>
      <td>{graceText(node.offlineGraceS)}</td>
      <td className="muted">{new Date(Number(node.createdAt) * 1000).toLocaleDateString()}</td>
      <td>
```

替换为：

```tsx
      <td>每月 {node.trafficResetDay} 日</td>
      <td>{graceText(node.offlineGraceS)}</td>
      <td><BillingSummary node={node} /></td>
      <td className="muted">{new Date(Number(node.createdAt) * 1000).toLocaleDateString()}</td>
      <td>
```

`web/src/pages/Nodes.tsx` 原文（8/8）：

```tsx
    </tr>
  );
}
```

替换为：

```tsx
    </tr>
  );
}

// "USD 12.50 / 月 · 2026-10-01（剩 4 天） · 自动续期"；已过期的那一段用告警红；全没填是"—"。
function BillingSummary({ node }: { node: Node }) {
  const parts: ReactNode[] = [];
  const price = priceText(node.billing);
  if (price) parts.push(price);
  const expiry = expiryText(node.billing);
  if (expiry) parts.push(<span className={expired(node.billing) ? "error" : undefined}>{expiry}</span>);
  if (node.billing?.autoRenew) parts.push("自动续期");
  if (parts.length === 0) return <>—</>;
  return <>{parts.map((p, i) => <Fragment key={i}>{i > 0 && " · "}{p}</Fragment>)}</>;
}

function BillingEditor({ label, draft, onChange }: { label: string; draft: BillingDraft; onChange: (patch: Partial<BillingDraft>) => void }) {
  return (
    <div className="billing-edit">
      <input aria-label={`价格 ${label}`} inputMode="decimal" placeholder="12.50" value={draft.price} onChange={(e) => onChange({ price: e.target.value })} />
      {/* ISO 4217 代码都是大写，输入时就转成大写；其余取值原样交给 hub 校验。 */}
      <input aria-label={`币种 ${label}`} placeholder="USD" value={draft.currency} onChange={(e) => onChange({ currency: e.target.value.toUpperCase() })} />
      <select aria-label={`周期 ${label}`} value={draft.billingCycle} onChange={(e) => onChange({ billingCycle: Number(e.target.value) as BillingCycle })}>
        <option value={BillingCycle.UNSPECIFIED}>无周期</option>
        {BILLING_CYCLES.map(({ value, label: cycle }) => <option key={value} value={value}>每{cycle}</option>)}
      </select>
      <input type="date" aria-label={`到期日 ${label}`} value={draft.expiresOn} onChange={(e) => onChange({ expiresOn: e.target.value })} />
      <label className="inline"><input type="checkbox" aria-label={`自动续期 ${label}`} checked={draft.autoRenew} onChange={(e) => onChange({ autoRenew: e.target.checked })} />自动续期</label>
      <p className="muted">只用于展示与到期提醒。开着自动续期时，到期日过了 hub 按周期推后；需要周期与到期日。</p>
    </div>
  );
}
```

`web/src/styles.css` 原文：

```css
.edit-form textarea { width: 100%; min-height: 6rem; font-family: ui-monospace, monospace; }
.logo-preview { height: 32px; width: auto; }
.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(360px, 100%), 1fr)); gap: 1rem; }
.secret { font-family: ui-monospace, monospace; word-break: break-all; white-space: pre-wrap; background: var(--bg); padding: 0.5rem; border-radius: 6px; }
```

替换为：

```css
.edit-form textarea { width: 100%; min-height: 6rem; font-family: ui-monospace, monospace; }
.logo-preview { height: 32px; width: auto; }
/* 节点表格编辑态的计费格：五个输入两列排开，说明占整行；表格单元格默认不换行，这里要换。 */
.billing-edit { display: grid; grid-template-columns: repeat(2, minmax(6rem, 1fr)); gap: 0.25rem 0.5rem; white-space: normal; min-width: 16rem; }
.billing-edit p { grid-column: 1 / -1; margin: 0; }
.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(360px, 100%), 1fr)); gap: 1rem; }
.secret { font-family: ui-monospace, monospace; word-break: break-all; white-space: pre-wrap; background: var(--bg); padding: 0.5rem; border-radius: 6px; }
```

- [ ] **Step 4: 跑绿**

```bash
cd /Users/xjetry/work/vibe/probe-billing && pnpm --dir web exec vitest run src/lib/billing.test.ts src/pages/Nodes.test.tsx > /tmp/billing-t5-green-t5.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && pnpm --dir web exec tsc -b > /tmp/billing-t5-tsc-t5.log 2>&1; echo $?
```

Expected：两条都是 0；第一条 `Test Files  2 passed (2)`、`Tests  65 passed (65)`。

- [ ] **Step 5: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-billing && git add web/src/lib/billing.test.ts web/src/lib/billing.ts web/src/pages/Nodes.test.tsx web/src/pages/Nodes.tsx web/src/styles.css > /tmp/billing-t5-add.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && git commit -m "web: 节点页的计费列" -m "lib/billing.ts 只 import types_pb，函数接收 Billing 与 PublicBilling 共有的字段，节点没有 billing 时按什么都没填处理；公开入口复用它也不会带进管理服务的生成代码。计费五项作为 billing 子对象随整行整体提交，取值约束只在 hub 校验，页面显示 hub 的错误原文；只把币种输入转成大写。剩余天数只显示 hub 下发的 days_left，不按浏览器时区再算；计费测试把 Date 钉在 2030-06-15，按浏览器本地日期重算的实现在任何时区都会红。只改名称时提交体带着当前五项计费：UpdateNode 整体替换，billing 缺失即五项全清。" > /tmp/billing-t5-commit.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && git status --porcelain > /tmp/billing-t5-status.log 2>&1; echo $?
```

Expected：三条都是 0；`billing-t5-status.log` 为空。

- [ ] **Step 6: make ci**

```bash
cd /Users/xjetry/work/vibe/probe-billing && make ci > /tmp/billing-t5-ci.log 2>&1; echo $?
```

Expected：0；vitest `Tests  387 passed (387)`。`make ci` 最后核对生成物已入库，所以放在提交之后。

- [ ] **Step 7: 缺陷注入**

| | 改动 | 命令 | 期望 |
|---|---|---|---|
| a | `priceText` 不拼周期 | `pnpm --dir web exec vitest run src/lib/billing.test.ts > /tmp/billing-t5-inj-a.log 2>&1; echo $?` | 1，`priceText > { price: '12.50', …(2) } → USD 12.50 / 月`：`expected 'USD 12.50' to be 'USD 12.50 / 月'`；`EUR 99 / 三年` 一例同样 |
| b | 已过期天数不取反 | `pnpm --dir web exec vitest run src/lib/billing.test.ts > /tmp/billing-t5-inj-b.log 2>&1; echo $?` | 1，`expected '2026-09-24（已过期 -3 天）' to be '2026-09-24（已过期 3 天）'` |
| c | `expired` 把剩 0 天也算过期（`< 0` 改成 `<= 0`） | `pnpm --dir web exec vitest run src/lib/billing.test.ts > /tmp/billing-t5-inj-c.log 2>&1; echo $?` | 1，`expiryText > { expiresOn: '2026-09-27', daysLeft: 0 } → 2026-09-27（剩 0 天）`：`expected true to be false` |
| d | 周期表删掉"三年" | `pnpm --dir web exec vitest run src/lib/billing.test.ts > /tmp/billing-t5-inj-d.log 2>&1; echo $?` | 1，`cycleLabel > 周期表覆盖协议枚举里除未指定之外的每个值`：`BILLING_CYCLE_TRIENNIAL: expected '未知（6）' not to match …`；`EUR 99 / 三年` 一例同样红 |
| e | `priceText` 不处理缺失的 billing（删掉 `if (!b) return "";`） | `pnpm --dir web exec vitest run src/lib/billing.test.ts > /tmp/billing-t5-inj-e.log 2>&1; echo $?` | 1，`没有 billing 的节点 > 三个函数都当作什么都没填`：`TypeError: Cannot read properties of undefined (reading 'billingCycle')` |
| f | 计费列的到期段不标红 | `pnpm --dir web exec vitest run src/pages/Nodes.test.tsx > /tmp/billing-t5-inj-f.log 2>&1; echo $?` | 1，`Nodes > 计费 > 计费列合成价格、周期、到期与自动续期，已过期的那段标红，全空是破折号`：`expect(element).toHaveClass("error")` 失败 |
| g | 币种输入不转大写 | `pnpm --dir web exec vitest run src/pages/Nodes.test.tsx > /tmp/billing-t5-inj-g.log 2>&1; echo $?` | 1，两个用例红：`Nodes > 计费 > 编辑计费随整行整体提交，币种输入即转大写`：`expect(element).toHaveValue(USD)` 失败；`Nodes > 计费 > hub 拒绝计费取值时显示错误原文，编辑行与五项草稿保留` 核对保留的草稿，币种应是转大写之后的 `US` |
| h | 编辑草稿的价格与币种从空串开始 | `pnpm --dir web exec vitest run src/pages/Nodes.test.tsx > /tmp/billing-t5-inj-h.log 2>&1; echo $?` | 1，两个用例红：`Nodes > 编辑回传全部字段，没碰的计费也按当前值回传`：`expected "vi.fn()" to be called with arguments: [ ObjectContaining{…}, Anything ]`（只改名称时提交的价格与币种成了空串）；`Nodes > 计费 > 编辑从节点当前的计费开始，清空之后提交的是空值`：`expected { price: '', currency: '', …(3) } to deeply equal { price: '9', currency: 'EUR', …(3) }` |
| i | 计费列不写"自动续期" | `pnpm --dir web exec vitest run src/pages/Nodes.test.tsx > /tmp/billing-t5-inj-i.log 2>&1; echo $?` | 1，同 f 的用例：`Unable to find an accessible element with the role "cell" and name "USD 12.50 / 月 · 2026-10-01（剩 4 天） · 自动续期"` |
| j | 全空时不画破折号 | `pnpm --dir web exec vitest run src/pages/Nodes.test.tsx > /tmp/billing-t5-inj-j.log 2>&1; echo $?` | 1，同 f 的用例：`Unable to find an accessible element with the role "cell" and name "—"` |
| k | `expiryText` 与 `expired` 改用浏览器本地日期重算天数（不读 `daysLeft` 的值） | `pnpm --dir web exec vitest run src/lib/billing.test.ts src/pages/Nodes.test.tsx > /tmp/billing-t5-inj-k.log 2>&1; echo $?` | 1，四个用例红：`expiryText > { expiresOn: '2026-10-01', daysLeft: 4 } → 2026-10-01（剩 4 天）`：`expected '2026-10-01（已过期 1353 天）' to be '2026-10-01（剩 4 天）'`；剩 0 天、已过期 3 天两例同样；`Nodes > 计费 > 计费列合成价格、周期、到期与自动续期，已过期的那段标红，全空是破折号`：`Unable to find an accessible element with the role "cell" and name "USD 12.50 / 月 · 2026-10-01（剩 4 天） · 自动续期"`。时钟钉在 2030-06-15，本地重算与夹具差出几年 |

每项：改动 → `git diff --stat` 非空 → 跑命令 → 核对原因 → `git checkout -- web/src`。

---

### Task 6: web：告警规则页的到期种类

**Files:**
- Modify: `web/src/lib/alerts.ts`（`ALERT_KINDS` 加到期；`ruleCondition`）
- Modify: `web/src/pages/AlertRules.tsx`（`Draft.daysBefore`；`toRule` 只发当前种类的专用字段；到期表单）
- Test: `web/src/lib/alerts.test.ts`、`web/src/pages/AlertRules.test.tsx`

**Interfaces:**
- Consumes：Task 1 的 `AlertKind.EXPIRY`、`AlertRule.daysBefore`；Task 3、4 的 `CheckRule` 与 `SaveAlertRule`。
- Produces：`ALERT_KINDS` 含 `{ value: AlertKind.EXPIRY, label: "到期" }`；到期规则的条件文案 `到期日距今不超过 N 天（含已过期）`。

- [ ] **Step 1: 写失败测试**

`alerts.test.ts`：类型表覆盖协议枚举里除未指定之外的每个种类；到期规则的条件文案。

`AlertRules.test.tsx`：
- 新建到期规则只发提前天数，默认 7。
- 超出 1–365 时表单不提交。
- 列表写出条件，编辑时带回提前天数。
- 到期规则改成离线后不再带提前天数。
- 探测规则改成到期时只带提前天数：从带着任务与阈值的探测规则切过来，`taskId`、`metric`、`threshold`、`forMinutes` 都是零值。新建草稿的任务与阈值是空串，换算后本来就是零值，只靠新建用例钉不住到期分支。
- 既有的"新建离线规则"用例改名为"……别的种类的字段都是零值"，补上阈值、持续分钟与提前天数为 0 的断言：hub 拒绝带着别的种类字段的规则，面板自己的提交不能被拒（设计决定 9）。

`web/src/lib/alerts.test.ts` 原文（1/3）：

```ts
import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { AlertDeliverySchema, AlertKind, AlertRuleSchema, AlertStateEntrySchema, ChannelKind, DeliveryFailure, DeliveryFailureSchema, ListProbeTasksResponseSchema, NotifyChannelSchema, ProbeMetric, ProbeTaskDetailSchema } from "../gen/probe/v1/admin_pb";
import { ProbeKind } from "../gen/probe/v1/types_pb";
import { CHANNEL_KINDS, channelTarget, deliveryText, failureText, graceText, labelOf, ruleCondition, statesOf, taskLabel, taskLabels, transitionLabel } from "./alerts";
import { PROBE_KINDS } from "./probes";

```

替换为：

```ts
import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { AlertDeliverySchema, AlertKind, AlertKindSchema, AlertRuleSchema, AlertStateEntrySchema, ChannelKind, DeliveryFailure, DeliveryFailureSchema, ListProbeTasksResponseSchema, NotifyChannelSchema, ProbeMetric, ProbeTaskDetailSchema } from "../gen/probe/v1/admin_pb";
import { ProbeKind } from "../gen/probe/v1/types_pb";
import { ALERT_KINDS, CHANNEL_KINDS, channelTarget, deliveryText, failureText, graceText, labelOf, ruleCondition, statesOf, taskLabel, taskLabels, transitionLabel } from "./alerts";
import { PROBE_KINDS } from "./probes";

```

`web/src/lib/alerts.test.ts` 原文（2/3）：

```ts
    const r = create(AlertRuleSchema, { kind: AlertKind.PROBE, taskId: 3n, metric: ProbeMetric.LOSS_PCT, threshold: 50, forMinutes: 3 });
    expect(ruleCondition(r, tasks)).toBe("TCP 1.1.1.1:443 丢包率 ≥ 50%，连续 3 分钟");
  });
  it("RTT 规则带 ms，已删除任务用编号", () => {
```

替换为：

```ts
    const r = create(AlertRuleSchema, { kind: AlertKind.PROBE, taskId: 3n, metric: ProbeMetric.LOSS_PCT, threshold: 50, forMinutes: 3 });
    expect(ruleCondition(r, tasks)).toBe("TCP 1.1.1.1:443 丢包率 ≥ 50%，连续 3 分钟");
  });
  it("到期规则写出提前天数", () => {
    expect(ruleCondition(create(AlertRuleSchema, { kind: AlertKind.EXPIRY, daysBefore: 14 }), tasks)).toBe("到期日距今不超过 14 天（含已过期）");
  });
  it("RTT 规则带 ms，已删除任务用编号", () => {
```

`web/src/lib/alerts.test.ts` 原文（3/3）：

```ts
});

it("transitionLabel 认识两种变化，未知值原样显示", () => {
  expect([transitionLabel("firing"), transitionLabel("recovered"), transitionLabel("x")]).toEqual(["触发", "恢复", "x"]);
```

替换为：

```ts
});

it("类型表覆盖协议枚举里除未指定之外的每个种类", () => {
  for (const { number, name } of AlertKindSchema.values) {
    if (number === AlertKind.UNSPECIFIED) continue;
    expect(labelOf(ALERT_KINDS, number), name).not.toMatch(/^未知/);
  }
});

it("transitionLabel 认识两种变化，未知值原样显示", () => {
  expect([transitionLabel("firing"), transitionLabel("recovered"), transitionLabel("x")]).toEqual(["触发", "恢复", "x"]);
```

`web/src/pages/AlertRules.test.tsx` 原文（1/3）：

```tsx

it("新建离线规则覆盖全部节点时不带节点列表", async () => {
  const saved: SaveAlertRuleRequest[] = [];
```

替换为：

```tsx

// 离线规则的探测字段与提前天数、到期规则的探测字段都必须是零值：hub 拒绝带着别的种类字段的规则（alert.CheckRule）。
// 新建草稿里连续分钟默认 3、提前天数默认 7，所以这里这两项的断言不是空转；任务与阈值在新建草稿里是空串，
// 由"主动切换离线"那例从探测规则出发钉住。
it("新建离线规则覆盖全部节点时不带节点列表，别的种类的字段都是零值", async () => {
  const saved: SaveAlertRuleRequest[] = [];
```

`web/src/pages/AlertRules.test.tsx` 原文（2/3）：

```tsx
  const r = saved[0].rule!;
  expect({ id: r.id, name: r.name, kind: r.kind, enabled: r.enabled, allNodes: r.allNodes, nodeIds: r.nodeIds, channelIds: r.channelIds, taskId: r.taskId, metric: r.metric }).toEqual(
    { id: 0n, name: "全网离线", kind: AlertKind.OFFLINE, enabled: true, allNodes: true, nodeIds: [], channelIds: [5n], taskId: 0n, metric: ProbeMetric.UNSPECIFIED });
  await waitFor(() => expect(within(screen.getByRole("form", { name: "新建告警规则" })).getByLabelText("名称")).toHaveValue(""));
```

替换为：

```tsx
  const r = saved[0].rule!;
  expect({ id: r.id, name: r.name, kind: r.kind, enabled: r.enabled, allNodes: r.allNodes, nodeIds: r.nodeIds, channelIds: r.channelIds,
    taskId: r.taskId, metric: r.metric, threshold: r.threshold, forMinutes: r.forMinutes, daysBefore: r.daysBefore }).toEqual(
    { id: 0n, name: "全网离线", kind: AlertKind.OFFLINE, enabled: true, allNodes: true, nodeIds: [], channelIds: [5n],
      taskId: 0n, metric: ProbeMetric.UNSPECIFIED, threshold: 0, forMinutes: 0, daysBefore: 0 });
  await waitFor(() => expect(within(screen.getByRole("form", { name: "新建告警规则" })).getByLabelText("名称")).toHaveValue(""));
```

`web/src/pages/AlertRules.test.tsx` 原文（3/3）：

```tsx
  expect(within(screen.getByRole("form", { name: "新建告警规则" })).getByLabelText("名称")).toHaveValue("全网离线");
});
```

替换为：

```tsx
  expect(within(screen.getByRole("form", { name: "新建告警规则" })).getByLabelText("名称")).toHaveValue("全网离线");
});

const expiryRules = create(ListAlertRulesResponseSchema, {
  rules: [{ id: 12n, name: "续费", kind: AlertKind.EXPIRY, enabled: true, allNodes: true, channelIds: [5n], daysBefore: 14 }],
  states: [{ ruleId: 12n, nodeId: 2n, state: "firing" }],
});

it("新建到期规则只发提前天数，默认 7 天", async () => {
  const saved: SaveAlertRuleRequest[] = [];
  render({ saveAlertRule: async (req) => { saved.push(req); return {}; } });
  const form = await screen.findByRole("form", { name: "新建告警规则" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "续费提醒" } });
  fireEvent.change(within(form).getByLabelText("类型"), { target: { value: String(AlertKind.EXPIRY) } });
  expect(within(form).getByLabelText("提前天数")).toHaveValue(7);
  expect(within(form).queryByLabelText("探测任务")).toBeNull();
  fireEvent.change(within(form).getByLabelText("提前天数"), { target: { value: "30" } });
  fireEvent.click(within(form).getByRole("button", { name: "创建" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  const r = saved[0].rule!;
  expect({ kind: r.kind, daysBefore: r.daysBefore, taskId: r.taskId, metric: r.metric, threshold: r.threshold, forMinutes: r.forMinutes }).toEqual(
    { kind: AlertKind.EXPIRY, daysBefore: 30, taskId: 0n, metric: ProbeMetric.UNSPECIFIED, threshold: 0, forMinutes: 0 });
});

it("提前天数超出 1–365 时表单不提交", async () => {
  const saved: SaveAlertRuleRequest[] = [];
  render({ saveAlertRule: async (req) => { saved.push(req); return {}; } });
  const form = await screen.findByRole("form", { name: "新建告警规则" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "越界" } });
  fireEvent.change(within(form).getByLabelText("类型"), { target: { value: String(AlertKind.EXPIRY) } });
  for (const value of ["0", "366", ""]) {
    fireEvent.change(within(form).getByLabelText("提前天数"), { target: { value } });
    fireEvent.click(within(form).getByRole("button", { name: "创建" }));
  }
  await act(async () => {});
  expect(saved).toHaveLength(0);
});

it("列表写出到期规则的条件与状态，编辑时带回提前天数", async () => {
  const saved: SaveAlertRuleRequest[] = [];
  render({ listAlertRules: async () => expiryRules, saveAlertRule: async (req) => { saved.push(req); return {}; } });
  const row = within((await screen.findByRole("cell", { name: "到期日距今不超过 14 天（含已过期）" })).closest("tr")!);
  expect(row.getByRole("cell", { name: "到期" })).toBeInTheDocument();
  expect(row.getByRole("cell", { name: "触发：法兰克福" })).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "编辑 续费（#12）" }));
  const form = screen.getByRole("form", { name: "编辑 续费（#12）" });
  expect(within(form).getByLabelText("提前天数")).toHaveValue(14);
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "续费提醒" } });
  fireEvent.click(within(form).getByRole("button", { name: "保存" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  expect(saved[0].rule).toEqual({ ...expiryRules.rules[0], name: "续费提醒" });
});

it("到期规则改成离线时不再带提前天数", async () => {
  const saved: SaveAlertRuleRequest[] = [];
  render({ listAlertRules: async () => expiryRules, saveAlertRule: async (req) => { saved.push(req); return {}; } });
  fireEvent.click(await screen.findByRole("button", { name: "编辑 续费（#12）" }));
  const form = screen.getByRole("form", { name: "编辑 续费（#12）" });
  fireEvent.change(within(form).getByLabelText("类型"), { target: { value: String(AlertKind.OFFLINE) } });
  fireEvent.click(within(form).getByRole("button", { name: "保存" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  expect({ kind: saved[0].rule!.kind, daysBefore: saved[0].rule!.daysBefore }).toEqual({ kind: AlertKind.OFFLINE, daysBefore: 0 });
});

// 新建草稿的探测任务与阈值是空串，换算后本来就是零值；从带着任务与阈值的探测规则切过来，这两项的断言才不是空转。
it("探测规则改成到期时只带提前天数", async () => {
  const saved: SaveAlertRuleRequest[] = [];
  render({ listAlertRules: async () => rttRules, listProbeTasks: async () => rttTasks,
    saveAlertRule: async (req) => { saved.push(req); return {}; },
  });
  fireEvent.click(await screen.findByRole("button", { name: "编辑 延迟（#10）" }));
  const form = screen.getByRole("form", { name: "编辑 延迟（#10）" });
  fireEvent.change(within(form).getByLabelText("类型"), { target: { value: String(AlertKind.EXPIRY) } });
  fireEvent.click(within(form).getByRole("button", { name: "保存" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  expect(saved[0].rule).toEqual({ ...rttRules.rules[0], channelIds: [5n], kind: AlertKind.EXPIRY,
    taskId: 0n, metric: ProbeMetric.UNSPECIFIED, threshold: 0, forMinutes: 0, daysBefore: 7 });
});
```

- [ ] **Step 2: 跑红**

```bash
cd /Users/xjetry/work/vibe/probe-billing && pnpm --dir web exec vitest run src/lib/alerts.test.ts src/pages/AlertRules.test.tsx > /tmp/billing-t6-red.log 2>&1; echo $?
```

Expected：1。原文节选：
- `FAIL  src/lib/alerts.test.ts > ruleCondition > 到期规则写出提前天数`：`expected '任务 #0 未知（0） ≥ 0，连续 0 分钟' to be '到期日距今不超过 14 天（含已过期）'`
- `FAIL  src/lib/alerts.test.ts > 类型表覆盖协议枚举里除未指定之外的每个种类`：`ALERT_KIND_EXPIRY: expected '未知（3）' not to match /^未知/`
- 页面的三个到期用例：`Unable to find a label with the text of: 提前天数`，或找不到条件单元格
- `探测规则改成到期时只带提前天数`：`expected { …(14) } to deeply equal { …(14) }`
- `Tests  6 failed | 46 passed (52)`

"新建离线规则……别的种类的字段都是零值"与"到期规则改成离线时不再带提前天数"在实现之前就是绿的：原来的 `toRule` 本来就只发当前种类的字段。它们钉住的是实现之后不回退，注入 c、g 证明它们会红。

- [ ] **Step 3: 实现**

`web/src/lib/alerts.ts` 原文（1/2）：

```ts
  { value: AlertKind.OFFLINE, label: "离线" },
  { value: AlertKind.PROBE, label: "探测" },
];
// unit 与 formatUnit 的单位名一致：丢包阈值是百分数，RTT 阈值是毫秒（proto AlertRule.threshold）。
```

替换为：

```ts
  { value: AlertKind.OFFLINE, label: "离线" },
  { value: AlertKind.PROBE, label: "探测" },
  { value: AlertKind.EXPIRY, label: "到期" },
];
// unit 与 formatUnit 的单位名一致：丢包阈值是百分数，RTT 阈值是毫秒（proto AlertRule.threshold）。
```

`web/src/lib/alerts.ts` 原文（2/2）：

```ts
export function ruleCondition(rule: AlertRule, tasks: ProbeTaskDetail[] | undefined): string {
  if (rule.kind === AlertKind.OFFLINE) return "超过宽限期未上报";
  const metric = PROBE_METRICS.find((m) => m.value === rule.metric);
  const threshold = metric ? formatUnit(rule.threshold, metric.unit) : String(rule.threshold);
```

替换为：

```ts
export function ruleCondition(rule: AlertRule, tasks: ProbeTaskDetail[] | undefined): string {
  if (rule.kind === AlertKind.OFFLINE) return "超过宽限期未上报";
  if (rule.kind === AlertKind.EXPIRY) return `到期日距今不超过 ${rule.daysBefore} 天（含已过期）`;
  const metric = PROBE_METRICS.find((m) => m.value === rule.metric);
  const threshold = metric ? formatUnit(rule.threshold, metric.unit) : String(rule.threshold);
```

`web/src/pages/AlertRules.tsx` 原文（1/4）：

```tsx
  name: string; kind: AlertKind; enabled: boolean; allNodes: boolean; nodeIds: Set<bigint>; channelIds: Set<bigint>;
  taskId: string; metric: ProbeMetric; threshold: string; forMinutes: string;
};

const emptyDraft = (): Draft => ({
  name: "", kind: AlertKind.OFFLINE, enabled: true, allNodes: true, nodeIds: new Set(), channelIds: new Set(),
  taskId: "", metric: ProbeMetric.LOSS_PCT, threshold: "", forMinutes: "3",
});
```

替换为：

```tsx
  name: string; kind: AlertKind; enabled: boolean; allNodes: boolean; nodeIds: Set<bigint>; channelIds: Set<bigint>;
  taskId: string; metric: ProbeMetric; threshold: string; forMinutes: string; daysBefore: string;
};

// 种类专用字段里有默认值的是指标（丢包率）、连续分钟 3 与提前天数 7；探测任务与阈值留空，切到探测时由表单的 required
// 挡住提交，要用户自己选填。种类、启用与"全部节点"另有各自的默认值。只有当前类型的那几项随保存发出（toRule）。
const emptyDraft = (): Draft => ({
  name: "", kind: AlertKind.OFFLINE, enabled: true, allNodes: true, nodeIds: new Set(), channelIds: new Set(),
  taskId: "", metric: ProbeMetric.LOSS_PCT, threshold: "", forMinutes: "3", daysBefore: "7",
});
```

`web/src/pages/AlertRules.tsx` 原文（2/4）：

```tsx
    threshold: probe ? String(r.threshold) : "", forMinutes: probe ? String(r.forMinutes) : "3",
  };
```

替换为：

```tsx
    threshold: probe ? String(r.threshold) : "", forMinutes: probe ? String(r.forMinutes) : "3",
    daysBefore: r.kind === AlertKind.EXPIRY ? String(r.daysBefore) : "7",
  };
```

`web/src/pages/AlertRules.tsx` 原文（3/4）：

```tsx
// 显式作用域因此变空时由 hub 拒绝（spec §6.6：空集不等于全部节点），不会悄悄放宽成全部。
function toRule(id: bigint, d: Draft, nodes: Node[], channels: NotifyChannel[]) {
  const live = (ids: ReadonlySet<bigint>, items: { id: bigint }[]) => ascending(items.filter((it) => ids.has(it.id)).map((it) => it.id));
  const probe = d.kind === AlertKind.PROBE
    ? { taskId: BigInt(d.taskId), metric: d.metric, threshold: Number(d.threshold), forMinutes: Number(d.forMinutes) }
    : {};
  return {
    id, name: d.name.trim(), kind: d.kind, enabled: d.enabled, allNodes: d.allNodes,
    nodeIds: d.allNodes ? [] : live(d.nodeIds, nodes), channelIds: live(d.channelIds, channels), ...probe,
  };
```

替换为：

```tsx
// 显式作用域因此变空时由 hub 拒绝（spec §6.6：空集不等于全部节点），不会悄悄放宽成全部。
// 种类专用字段只发当前类型的：hub 拒绝带着别的种类字段的规则（alert.CheckRule）。
function toRule(id: bigint, d: Draft, nodes: Node[], channels: NotifyChannel[]) {
  const live = (ids: ReadonlySet<bigint>, items: { id: bigint }[]) => ascending(items.filter((it) => ids.has(it.id)).map((it) => it.id));
  const own = d.kind === AlertKind.PROBE
    ? { taskId: BigInt(d.taskId), metric: d.metric, threshold: Number(d.threshold), forMinutes: Number(d.forMinutes) }
    : d.kind === AlertKind.EXPIRY ? { daysBefore: Number(d.daysBefore) } : {};
  return {
    id, name: d.name.trim(), kind: d.kind, enabled: d.enabled, allNodes: d.allNodes,
    nodeIds: d.allNodes ? [] : live(d.nodeIds, nodes), channelIds: live(d.channelIds, channels), ...own,
  };
```

`web/src/pages/AlertRules.tsx` 原文（4/4）：

```tsx
      </div>
      {probe ? (
        <div className="row">
```

替换为：

```tsx
      </div>
      {draft.kind === AlertKind.EXPIRY ? (
        <>
          <div className="row">
            <label>提前天数<input type="number" required min={1} max={365} step={1} value={draft.daysBefore} onChange={(e) => set({ daysBefore: e.target.value })} /></label>
          </div>
          <p className="muted">节点到期日距今不超过提前天数即触发（已过期的也算）；到期日改到这个范围之外、清除到期日，或调小提前天数使它落到范围之外，即恢复。续期后的到期日仍在范围内时不恢复。到期日在节点页设置。保存后立即评估，此后在 hub 启动时、hub 时区的每个日界（零点不存在的日子取新一天的第一个时刻）与修改节点计费时评估。</p>
        </>
      ) : probe ? (
        <div className="row">
```

- [ ] **Step 4: 跑绿**

```bash
cd /Users/xjetry/work/vibe/probe-billing && pnpm --dir web exec vitest run src/lib/alerts.test.ts src/pages/AlertRules.test.tsx > /tmp/billing-t6-green-t6.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && pnpm --dir web exec tsc -b > /tmp/billing-t6-tsc-t6.log 2>&1; echo $?
```

Expected：两条都是 0；第一条 `Test Files  2 passed (2)`、`Tests  52 passed (52)`。

- [ ] **Step 5: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-billing && git add web/src/lib/alerts.test.ts web/src/lib/alerts.ts web/src/pages/AlertRules.test.tsx web/src/pages/AlertRules.tsx > /tmp/billing-t6-add.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && git commit -m "web: 告警规则页的到期种类" -m "到期规则的表单只有提前天数，取 1 到 365，默认 7。保存时只发当前种类的专用字段，别的种类的字段都是零值：hub 拒绝带着其他种类字段的规则，而草稿里持续分钟与提前天数各有默认值。说明写明保存后立即评估，之后在 hub 启动时、hub 时区的每个日界与修改节点计费时评估。恢复条件按窗口口径说明：到期日离开窗口、清除到期日或调小提前天数使它离开窗口才恢复，续期后仍在窗口内的不恢复。" > /tmp/billing-t6-commit.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && git status --porcelain > /tmp/billing-t6-status.log 2>&1; echo $?
```

Expected：三条都是 0；`billing-t6-status.log` 为空。

- [ ] **Step 6: make ci**

```bash
cd /Users/xjetry/work/vibe/probe-billing && make ci > /tmp/billing-t6-ci.log 2>&1; echo $?
```

Expected：0；vitest `Tests  394 passed (394)`。`make ci` 最后核对生成物已入库，所以放在提交之后。

- [ ] **Step 7: 缺陷注入**

| | 改动 | 命令 | 期望 |
|---|---|---|---|
| a | `ALERT_KINDS` 删掉"到期" | `pnpm --dir web exec vitest run src/lib/alerts.test.ts src/pages/AlertRules.test.tsx > /tmp/billing-t6-inj-a.log 2>&1; echo $?` | 1，五个用例红：`类型表覆盖协议枚举里除未指定之外的每个种类`（`ALERT_KIND_EXPIRY: expected '未知（3）' not to match /^未知/`）与页面的四个到期用例（含 `探测规则改成到期时只带提前天数`） |
| b | 到期规则的条件写成"到期前 N 天" | `pnpm --dir web exec vitest run src/lib/alerts.test.ts src/pages/AlertRules.test.tsx > /tmp/billing-t6-inj-b.log 2>&1; echo $?` | 1，`ruleCondition > 到期规则写出提前天数`：`expected '到期前 14 天' to be '到期日距今不超过 14 天（含已过期）'`；列表用例同样红 |
| c | `toRule` 对任何种类都带 `daysBefore` | `pnpm --dir web exec vitest run src/lib/alerts.test.ts src/pages/AlertRules.test.tsx > /tmp/billing-t6-inj-c.log 2>&1; echo $?` | 1，六个用例红：`RTT 规则只改名称保留完整载荷…`、"主动切换指标…"、`新建离线规则…别的种类的字段都是零值`、`到期规则改成离线时不再带提前天数` 等，都是载荷多出 `daysBefore` |
| d | 编辑到期规则时提前天数不从规则带回（一律 `"7"`） | `pnpm --dir web exec vitest run src/lib/alerts.test.ts src/pages/AlertRules.test.tsx > /tmp/billing-t6-inj-d.log 2>&1; echo $?` | 1，`列表写出到期规则的条件与状态，编辑时带回提前天数`：`expect(element).toHaveValue(14)` 失败 |
| e | 提前天数输入框去掉 `min={1}` | `pnpm --dir web exec vitest run src/lib/alerts.test.ts src/pages/AlertRules.test.tsx > /tmp/billing-t6-inj-e.log 2>&1; echo $?` | 1，`提前天数超出 1–365 时表单不提交`：`expected [ { …(2) } ] to have a length of +0 but got 1` |
| f | 新建草稿的提前天数默认空串 | `pnpm --dir web exec vitest run src/lib/alerts.test.ts src/pages/AlertRules.test.tsx > /tmp/billing-t6-inj-f.log 2>&1; echo $?` | 1，`新建到期规则只发提前天数，默认 7 天`：`expect(element).toHaveValue(7)` 失败 |
| g | `toRule` 对任何种类都带 `forMinutes` | `pnpm --dir web exec vitest run src/lib/alerts.test.ts src/pages/AlertRules.test.tsx > /tmp/billing-t6-inj-g.log 2>&1; echo $?` | 1，五个用例红，其中 `新建离线规则覆盖全部节点时不带节点列表，别的种类的字段都是零值`：`expected { id: 0n, name: '全网离线', kind: 1, …(9) } to deeply equal …`（载荷多出 `forMinutes: 3`） |
| h | `toRule` 的到期分支也带 `threshold` | `pnpm --dir web exec vitest run src/lib/alerts.test.ts src/pages/AlertRules.test.tsx > /tmp/billing-t6-inj-h.log 2>&1; echo $?` | 1，只有 `探测规则改成到期时只带提前天数` 红：`expected { …(14) } to deeply equal { …(14) }`，差异是 `threshold` 应为 0、实为 75。新建到期规则的用例照常绿：新建草稿的阈值是空串，换算后本来就是 0 |

每项：改动 → `git diff --stat` 非空 → 跑命令 → 核对原因 → `git checkout -- web/src`。命令里两个文件逐个写出（见验证规范）。

---

### Task 7: web：公开页的节点卡片与节点页显示费用与到期

**Files:**
- Modify: `web/src/public/Overview.tsx`（卡片的费用、到期两行）
- Modify: `web/src/public/NodePage.tsx`（静态信息卡的显示条件与两行）
- Test: `web/src/public/Overview.test.tsx`、`web/src/public/NodePage.test.tsx`、`web/src/public/importScan.test.ts`（冒烟清单加 `lib/billing.ts`）

**Interfaces:**
- Consumes：Task 1 的 `PublicNode.billing`（`PublicBilling`）；Task 5 的 `priceText`、`expiryText`、`expired`，直接传 `node.billing`。

- [ ] **Step 1: 写失败测试**

- 总览：填了费用与到期的卡片多两行，已过期的到期标红，没填的不显示这两行。
- 节点页：静态信息卡带这两行；主机信息缺失时卡片照样显示它们；只填了到期日的节点也画卡片，只有到期一行且标红，没有费用行；三者都没有时不画卡片。
- 夹具的计费写成嵌套的 `billing`（`PublicBilling`）。
- 两处计费用例开头把 Date 钉在 2031-01-01（`vi.useFakeTimers({ toFake: ["Date"] })` 与 `vi.setSystemTime(new Date(2031, 0, 1))`），节点页补 `afterEach(() => vi.useRealTimers())`。夹具的剩余天数是 hub 下发的值；不钉时它恰好等于到期日减今天，按浏览器本地日期重算的实现照样通过。
- import 扫描的冒烟清单加上 `lib/billing.ts`，证明它确实进了公开包、且没带进管理服务的生成代码。

`web/src/public/Overview.test.tsx` 原文（1/2）：

```tsx
import { PublicService } from "../gen/probe/v1/public_pb";
import { POLL_MS } from "../lib/poll";
```

替换为：

```tsx
import { PublicService } from "../gen/probe/v1/public_pb";
import { BillingCycle } from "../gen/probe/v1/types_pb";
import { POLL_MS } from "../lib/poll";
```

`web/src/public/Overview.test.tsx` 原文（2/2）：

```tsx
  await waitFor(() => expect(getSnapshot.mock.calls.length).toBeGreaterThanOrEqual(2));
});
```

替换为：

```tsx
  await waitFor(() => expect(getSnapshot.mock.calls.length).toBeGreaterThanOrEqual(2));
});

it("填了费用与到期的节点卡片多两行，已过期的到期标红，没填的不显示这两行", async () => {
  // 时钟放在与夹具错开的日期：剩余天数只能来自 hub 下发的 daysLeft，页面按本地日期重算会得出另一个数。
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(new Date(2031, 0, 1));
  const billed = {
    now: 1_000n,
    nodes: [
      { id: 5n, name: "paid", online: true, sortOrder: 0, billing: { price: "12.50", currency: "USD", billingCycle: BillingCycle.MONTHLY, expiresOn: "2026-10-01", daysLeft: 4 } },
      { id: 6n, name: "lapsed", online: true, sortOrder: 1, billing: { expiresOn: "2026-09-24", daysLeft: -3 } },
      { id: 7n, name: "plain", online: true, sortOrder: 2 },
    ],
  };
  renderWithService(PublicService, { getSnapshot: async () => billed }, [{ path: "/", Component: PublicOverview }], "/");
  const paid = within(await screen.findByRole("article", { name: "paid" }));
  expect(paid.getByText("费用").nextElementSibling).toHaveTextContent(/^USD 12\.50 \/ 月$/);
  const due = paid.getByText("到期").nextElementSibling;
  expect(due).toHaveTextContent(/^2026-10-01（剩 4 天）$/);
  expect(due).not.toHaveClass("error");
  const lapsed = within(screen.getByRole("article", { name: "lapsed" }));
  expect(lapsed.queryByText("费用")).toBeNull();
  expect(lapsed.getByText("2026-09-24（已过期 3 天）")).toHaveClass("error");
  const plain = within(screen.getByRole("article", { name: "plain" }));
  expect([plain.queryByText("费用"), plain.queryByText("到期")]).toEqual([null, null]);
});
```

`web/src/public/NodePage.test.tsx` 原文（1/3）：

```tsx
import { create } from "@bufbuild/protobuf";
import { screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { PublicService } from "../gen/probe/v1/public_pb";
import { QueryProbesResponseSchema } from "../gen/probe/v1/query_pb";
import { ProbeKind } from "../gen/probe/v1/types_pb";
import { renderWithService } from "../test/harness";
```

替换为：

```tsx
import { create } from "@bufbuild/protobuf";
import { cleanup, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { PublicService } from "../gen/probe/v1/public_pb";
import { QueryProbesResponseSchema } from "../gen/probe/v1/query_pb";
import { BillingCycle, ProbeKind } from "../gen/probe/v1/types_pb";
import { renderWithService } from "../test/harness";
```

`web/src/public/NodePage.test.tsx` 原文（2/3）：

```tsx
}));

```

替换为：

```tsx
}));

afterEach(() => vi.useRealTimers());

```

`web/src/public/NodePage.test.tsx` 原文（3/3）：

```tsx
  expect(queryMetrics).not.toHaveBeenCalled();
});
```

替换为：

```tsx
  expect(queryMetrics).not.toHaveBeenCalled();
});

it("静态信息卡带费用与到期两行；主机信息缺失时卡片照样显示这两行，都没有时不画卡片", async () => {
  // 时钟放在与夹具错开的日期：剩余天数只能来自 hub 下发的 daysLeft，页面按本地日期重算会得出另一个数。
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(new Date(2031, 0, 1));
  const nodes = [
    { id: 7n, name: "edge-1", online: true, facts: { os: "Alpine 3.21", arch: "arm64" }, billing: { price: "5", currency: "EUR", billingCycle: BillingCycle.YEARLY, expiresOn: "2026-09-20", daysLeft: -7 } },
    { id: 8n, name: "fresh", online: false, billing: { price: "3", currency: "USD" } },
    { id: 9n, name: "bare", online: false },
    { id: 10n, name: "due", online: false, billing: { expiresOn: "2026-09-20", daysLeft: -7 } },
  ];
  const getSnapshot = async () => ({ now: 1_000n, nodes });
  const queryMetrics = async () => ({ level: "1m", stepS: 60, ts: [], series: [] });
  const queryProbes = async () => ({ level: "1m", stepS: 60, series: [] });
  const show = async (id: number) => {
    renderWithService(PublicService, { getSnapshot, queryMetrics, queryProbes }, [{ path: "/nodes/:id", Component: NodePage }], `/nodes/${id}`);
    await screen.findByRole("heading", { level: 1 });
  };
  await show(7);
  expect(screen.getByText("Alpine 3.21")).toBeInTheDocument();
  expect(screen.getByText("费用").nextElementSibling).toHaveTextContent(/^EUR 5 \/ 年$/);
  expect(screen.getByText("2026-09-20（已过期 7 天）")).toHaveClass("error");
  cleanup();
  await show(8);
  expect(screen.getByText("费用").nextElementSibling).toHaveTextContent(/^USD 3$/);
  expect(screen.queryByText("到期")).toBeNull();
  expect(screen.queryByText("系统")).toBeNull();
  cleanup();
  await show(9);
  expect(screen.queryByRole("definition")).toBeNull();
  cleanup();
  await show(10);
  expect(screen.getAllByRole("definition").map((d) => d.textContent)).toEqual(["2026-09-20（已过期 7 天）"]);
  expect(screen.getByText("2026-09-20（已过期 7 天）")).toHaveClass("error");
});
```

`web/src/public/importScan.test.ts` 原文：

```ts
it("公开包的模块图不含管理服务的生成代码", () => {
  // 冒烟：确实打进了公开服务的生成代码与共用组件，空图不能冒充通过。
  for (const f of ["gen/probe/v1/public_pb.ts", "gen/probe/v1/query_pb.ts", "components/History.tsx", "components/Chart.tsx"]) {
    expect(pub.ids.some((id) => id.endsWith(`/src/${f}`)), f).toBe(true);
  }
```

替换为：

```ts
it("公开包的模块图不含管理服务的生成代码", () => {
  // 冒烟：确实打进了公开服务的生成代码与共用组件，空图不能冒充通过。
  for (const f of ["gen/probe/v1/public_pb.ts", "gen/probe/v1/query_pb.ts", "components/History.tsx", "components/Chart.tsx", "lib/billing.ts"]) {
    expect(pub.ids.some((id) => id.endsWith(`/src/${f}`)), f).toBe(true);
  }
```

- [ ] **Step 2: 跑红**

```bash
cd /Users/xjetry/work/vibe/probe-billing && pnpm --dir web exec vitest run src/public/Overview.test.tsx src/public/NodePage.test.tsx src/public/importScan.test.ts > /tmp/billing-t7-red.log 2>&1; echo $?
```

Expected：1。原文节选：
- `FAIL  src/public/NodePage.test.tsx > 静态信息卡带费用与到期两行；主机信息缺失时卡片照样显示这两行，都没有时不画卡片`：`Unable to find an element with the text: 费用.`
- `FAIL  src/public/Overview.test.tsx > 填了费用与到期的节点卡片多两行，已过期的到期标红，没填的不显示这两行`：同上
- `FAIL  src/public/importScan.test.ts > 公开包的模块图不含管理服务的生成代码`：`lib/billing.ts: expected false to be true`
- `Tests  3 failed | 8 passed (11)`

- [ ] **Step 3: 实现**

`web/src/public/Overview.tsx` 原文（1/3）：

```tsx
import { Bar, Missing, ratio } from "../components/Bar";
import { PublicService, type PublicNode } from "../gen/probe/v1/public_pb";
import { ago, bytes, duration, percent } from "../lib/format";
import { POLL_MS } from "../lib/poll";
```

替换为：

```tsx
import { Bar, Missing, ratio } from "../components/Bar";
import { PublicService, type PublicNode } from "../gen/probe/v1/public_pb";
import { expired, expiryText, priceText } from "../lib/billing";
import { ago, bytes, duration, percent } from "../lib/format";
import { POLL_MS } from "../lib/poll";
```

`web/src/public/Overview.tsx` 原文（2/3）：

```tsx
}

// 卡片内容按 §10：名称、在线、系统与架构、CPU、内存、磁盘、网速、运行时长、本周期流量。
function NodeCard({ node, now }: { node: PublicNode; now: number }) {
  const m = node.metrics;
  const f = node.facts;
  return (
    <article className={`card node-card ${node.online ? "online" : "offline"}`} aria-label={node.name}>
```

替换为：

```tsx
}

// 卡片内容按 §10：名称、在线、系统与架构、CPU、内存、磁盘、网速、运行时长、本周期流量，以及填了才显示的费用与到期。
function NodeCard({ node, now }: { node: PublicNode; now: number }) {
  const m = node.metrics;
  const f = node.facts;
  const price = priceText(node.billing);
  const expiry = expiryText(node.billing);
  return (
    <article className={`card node-card ${node.online ? "online" : "offline"}`} aria-label={node.name}>
```

`web/src/public/Overview.tsx` 原文（3/3）：

```tsx
        <dt>本周期</dt>
        <dd>{node.traffic ? `↓ ${bytes(node.traffic.periodRx)} ↑ ${bytes(node.traffic.periodTx)}` : <Missing />}</dd>
      </dl>
      <p className="muted">{node.lastSeenAt !== undefined ? `最近上报 ${ago(node.lastSeenAt, now)}` : "从未上报"}</p>
```

替换为：

```tsx
        <dt>本周期</dt>
        <dd>{node.traffic ? `↓ ${bytes(node.traffic.periodRx)} ↑ ${bytes(node.traffic.periodTx)}` : <Missing />}</dd>
        {price && <><dt>费用</dt><dd>{price}</dd></>}
        {expiry && <><dt>到期</dt><dd className={expired(node.billing) ? "error" : undefined}>{expiry}</dd></>}
      </dl>
      <p className="muted">{node.lastSeenAt !== undefined ? `最近上报 ${ago(node.lastSeenAt, now)}` : "从未上报"}</p>
```

`web/src/public/NodePage.tsx` 原文（1/3）：

```tsx
import { HistoryCharts, RangePicker, useHistory, type HistoryMethods } from "../components/History";
import { PublicService } from "../gen/probe/v1/public_pb";
import { POLL_MS } from "../lib/poll";

```

替换为：

```tsx
import { HistoryCharts, RangePicker, useHistory, type HistoryMethods } from "../components/History";
import { PublicService } from "../gen/probe/v1/public_pb";
import { expired, expiryText, priceText } from "../lib/billing";
import { POLL_MS } from "../lib/poll";

```

`web/src/public/NodePage.tsx` 原文（2/3）：

```tsx
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  if (!node) return missing;
  return (
    <section>
```

替换为：

```tsx
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  if (!node) return missing;
  const price = priceText(node.billing);
  const expiry = expiryText(node.billing);
  return (
    <section>
```

`web/src/public/NodePage.tsx` 原文（3/3）：

```tsx
      </header>
      <HistoryCharts history={history} noProbes={<p className="muted">窗口内没有探测结果。</p>} />
      {node.facts && (
        <dl className="card facts">
          <dt>系统</dt><dd>{node.facts.os}</dd>
          <dt>架构</dt><dd>{node.facts.arch}</dd>
          <dt>CPU</dt><dd>{node.facts.cpuModel} × {node.facts.cpuCores}</dd>
          <dt>虚拟化</dt><dd>{node.facts.virtualization || "无 / 未知"}</dd>
        </dl>
      )}
```

替换为：

```tsx
      </header>
      <HistoryCharts history={history} noProbes={<p className="muted">窗口内没有探测结果。</p>} />
      {/* 静态信息卡：主机信息从未上报时缺失，费用与到期填了才显示（§10），三者都没有时不画这张卡。 */}
      {(node.facts || price || expiry) && (
        <dl className="card facts">
          {node.facts && (
            <>
              <dt>系统</dt><dd>{node.facts.os}</dd>
              <dt>架构</dt><dd>{node.facts.arch}</dd>
              <dt>CPU</dt><dd>{node.facts.cpuModel} × {node.facts.cpuCores}</dd>
              <dt>虚拟化</dt><dd>{node.facts.virtualization || "无 / 未知"}</dd>
            </>
          )}
          {price && <><dt>费用</dt><dd>{price}</dd></>}
          {expiry && <><dt>到期</dt><dd className={expired(node.billing) ? "error" : undefined}>{expiry}</dd></>}
        </dl>
      )}
```

- [ ] **Step 4: 跑绿**

```bash
cd /Users/xjetry/work/vibe/probe-billing && pnpm --dir web exec vitest run src/public/Overview.test.tsx src/public/NodePage.test.tsx src/public/importScan.test.ts > /tmp/billing-t7-green-t7.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && pnpm --dir web exec tsc -b > /tmp/billing-t7-tsc-t7.log 2>&1; echo $?
```

Expected：两条都是 0；第一条 `Test Files  3 passed (3)`、`Tests  11 passed (11)`。

- [ ] **Step 5: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-billing && git add web/src/public/NodePage.test.tsx web/src/public/NodePage.tsx web/src/public/Overview.test.tsx web/src/public/Overview.tsx web/src/public/importScan.test.ts > /tmp/billing-t7-add.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && git commit -m "web: 公开页卡片与节点页显示费用与到期" -m "费用与到期取自 PublicBilling，填了才显示，已过期的到期标红；节点没有 billing 时两行都不出现。节点页的静态信息卡在主机信息缺失时也显示这两行。import 扫描的冒烟清单加上 lib/billing.ts，证明它确实进了公开包、且没带进管理服务的生成代码。两处计费用例把 Date 钉在 2031 年，按浏览器本地日期重算的实现会红；节点页补只填了到期日的节点，钉住静态信息卡显示条件里的到期一支。" > /tmp/billing-t7-commit.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && git status --porcelain > /tmp/billing-t7-status.log 2>&1; echo $?
```

Expected：三条都是 0；`billing-t7-status.log` 为空。

- [ ] **Step 6: make ci**

```bash
cd /Users/xjetry/work/vibe/probe-billing && make ci > /tmp/billing-t7-ci.log 2>&1; echo $?
```

Expected：0；vitest `Tests  396 passed (396)`。`make ci` 最后核对生成物已入库，所以放在提交之后。

- [ ] **Step 7: 缺陷注入**

| | 改动 | 命令 | 期望 |
|---|---|---|---|
| a | 总览卡片的到期不标红 | `pnpm --dir web exec vitest run src/public/Overview.test.tsx > /tmp/billing-t7-inj-a.log 2>&1; echo $?` | 1，`填了费用与到期的节点卡片多两行，已过期的到期标红，没填的不显示这两行`：`expect(element).toHaveClass("error")` 失败 |
| b | 总览卡片没填价格也画"费用"一行 | `pnpm --dir web exec vitest run src/public/Overview.test.tsx > /tmp/billing-t7-inj-b.log 2>&1; echo $?` | 1，同一用例：`expected <dt></dt> to be null` |
| c | 节点页只在有主机信息时画静态信息卡 | `pnpm --dir web exec vitest run src/public/NodePage.test.tsx > /tmp/billing-t7-inj-c.log 2>&1; echo $?` | 1，`静态信息卡带费用与到期两行；主机信息缺失时卡片照样显示这两行，都没有时不画卡片`：`Unable to find an element with the text: 费用.` |
| d | 节点页的到期不标红 | `pnpm --dir web exec vitest run src/public/NodePage.test.tsx > /tmp/billing-t7-inj-d.log 2>&1; echo $?` | 1，同一用例：`expect(element).toHaveClass("error")` 失败 |
| e | `billing.ts` 加 `import { AdminService } from "../gen/probe/v1/admin_pb";` 并导出一个用到它的值 `export const adminServiceName = AdminService.typeName;`。只加 import 或用 `import type` 会被打包消除，不红 | `pnpm --dir web exec vitest run src/public/importScan.test.ts > /tmp/billing-t7-inj-e.log 2>&1; echo $?` | 1，`公开包的模块图不含管理服务的生成代码`：`expected [ 'src/gen/probe/v1/admin_pb.ts' ] to deeply equal []`；`公开包的产物里没有 admin.proto 的描述符` 同样红 |
| f | `billing.ts` 的 `expiryText` 与 `expired` 改用浏览器本地日期重算天数（写法同 Task 5 注入 k） | `pnpm --dir web exec vitest run src/public/Overview.test.tsx src/public/NodePage.test.tsx > /tmp/billing-t7-inj-f.log 2>&1; echo $?` | 1，两个用例都红：总览 `Expected element to have text content: /^2026-10-01（剩 4 天）$/`，收到 `2026-10-01（已过期 1553 天）`；节点页 `Unable to find an element with the text: 2026-09-20（已过期 7 天）` |
| g | 节点页静态信息卡的显示条件去掉 `\|\| expiry`（`{(node.facts \|\| price \|\| expiry) && (` 改成 `{(node.facts \|\| price) && (`） | `pnpm --dir web exec vitest run src/public/NodePage.test.tsx > /tmp/billing-t7-inj-g.log 2>&1; echo $?` | 1，`静态信息卡带费用与到期两行；主机信息缺失时卡片照样显示这两行，都没有时不画卡片`：红在只填了到期日的节点（`show(10)` 之后）：`Unable to find an accessible element with the role "definition"` |

每项：改动 → `git diff --stat` 非空 → 跑命令 → 核对原因 → `git checkout -- web/src`。

---

### Task 8: e2e：计费字段、公开快照、到期告警与自动续期；README；入口卡片

**Files:**
- Modify: `scripts/e2e.sh`（两个端口可由环境变量覆盖；`wait_alert` 带节点参数；计费与到期一段；node1 在 firing 状态下重启，重启后的规则、事件、计费与续期恢复；统计的行数）
- Modify: `README.md`（"运行 hub"一节加一条；"时区"一节提到期日的天边界）
- Modify: `proto/SKILL.md`（入口卡片：`description` 加上费用与到期；"例子"一节在 CPU 那例之后加一个按 `daysLeft` 列节点的例子）

**Interfaces:**
- Consumes：Task 1–7 的全部对外行为。e2e 用 curl + jq 走 Connect 的 JSON；公开服务用既有的 `pubget`（匿名 GET）。
- 流程（放在离线告警恢复之后、重启之前）：
  1. 三位小数的价格被拒（400），错误写明 `billing.price`。
  2. node1 设为上月 1 日到期、按月、自动续期：`UpdateNode` 的响应已是推后的日期（某月 1 日，`daysLeft` 在 0–31），hub 日志有对应的 `node expiry renewed` 一行。
  3. node1 改为 5 天后到期、关掉自动续期：公开快照的 `billing` 带价格、币种、周期、到期日与 `daysLeft`，不带 `autoRenew`；`daysLeft` 与同一响应的 `now` 相符。
  4. `daysBefore: 0` 的到期规则被拒（400），错误写明 `rule.days_before`。
  5. 保存 node1 的到期规则（提前 10 天）：回显 `daysBefore`，触发事件送达，文案"将于…到期（剩 N 天…）"，webhook 载荷 `"kind":"expiry"`。
  6. node1 停在 firing 进入重启。重启之后：两条规则（到期那条带 `daysBefore: 10`），node1 的计费原样且 `daysLeft` 在 4–5；再保存一次同一条到期规则（一次扫描在响应之前同步做完），事件仍是三个（离线一对加到期的触发）且都已送达，不重复提醒。
  7. 重启之后续期到 60 天后：`daysLeft` 在 59–60，恢复事件送达，文案"到期日已更新为…"，事件共四个。统计里 `alert_rule`、`alert_rule_node` 为 2，`alert_event`、`alert_delivery` 为 4。
- 每个 `UpdateNode` 的请求体都在 `billing` 里带 `daysLeft: 999`：续期后的 0–31、公开快照里与 `now` 相符、重启后的 4–5、重启后续期的 59–60 四处断言都排除了 999。
- 端口：`port=${E2E_HUB_PORT:-18080}`、`hook_port=${E2E_HOOK_PORT:-18081}`，webhook 接收器的三处 18081 都换成 `$hook_port`，python 接收器从参数读端口。
- 入口卡片：hub 经 `GetApiReference` 下发的 `guide` 就是 `proto/SKILL.md`（`proto/embed.go` 的 `go:embed`；`reference_test.go` 核对两者逐字相同）。`run_card_examples` 从下发的卡片里抽出每个 `sh example` 块，用 API token 执行：空 hub 那一轮要求输出是 JSON，重启后有数据的那一轮另要求非空。新例子在空 hub 输出 `[]`，有数据时 node1 带着到期日，所以 `scripts/e2e.sh` 不用为它改。

- [ ] **Step 1: 改 e2e.sh、README 与入口卡片**

原文以 Task 7 提交后的文件为准，都只出现一次。

`scripts/e2e.sh` 原文（1/7）：

```sh
echo "E2E artifacts: $work"
db="$work/e2e.db"
port=18080
base="http://127.0.0.1:$port"
admin_pw="e2e admin password 2026"
```

替换为：

```sh
echo "E2E artifacts: $work"
db="$work/e2e.db"
# 端口默认 18080（hub）/18081（webhook 接收器），与 install-accept 的 18085/18086、macos-accept 的 18087/18088 错开；
# 本机上别的进程占着默认端口时，用环境变量 E2E_HUB_PORT、E2E_HOOK_PORT 覆盖。
port=${E2E_HUB_PORT:-18080}
hook_port=${E2E_HOOK_PORT:-18081}
base="http://127.0.0.1:$port"
admin_pw="e2e admin password 2026"
```

`scripts/e2e.sh` 原文（2/7）：

```sh

# 接收器与 hub 同在宿主回环；每次请求体落一行，退出时一并回收。
python3 - "$work/hooks.txt" <<'PY' > "$work/hookrecv.log" 2>&1 &
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer
```

替换为：

```sh

# 接收器与 hub 同在宿主回环；每次请求体落一行，退出时一并回收。
python3 - "$work/hooks.txt" "$hook_port" <<'PY' > "$work/hookrecv.log" 2>&1 &
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer
```

`scripts/e2e.sh` 原文（3/7）：

```sh
    def log_message(self, *a):
        pass
HTTPServer(("127.0.0.1", 18081), H).serve_forever()
PY
hookrecv=$!
i=0
until curl -sf -o /dev/null http://127.0.0.1:18081/; do
  i=$((i + 1)); [ "$i" -lt 30 ] || { echo "FAIL: webhook receiver did not listen"; cat "$work/hookrecv.log"; exit 1; }; sleep 0.2
done
[ "$(rpc SaveNotifyChannel "$(jq -nc '{channel: {name: "e2e hook", kind: "CHANNEL_KIND_WEBHOOK", webhook: {url: "http://127.0.0.1:18081/hook"}}}')")" = 200 ] || { echo "FAIL: SaveNotifyChannel"; cat "$work/SaveNotifyChannel.json"; exit 1; }
channel=$(jq -r '.channel.id' "$work/SaveNotifyChannel.json")
[ "$(rpc TestNotifyChannel "$(jq -nc --arg id "$channel" '{id: $id}')")" = 200 ] || { echo "FAIL: TestNotifyChannel"; cat "$work/TestNotifyChannel.json"; exit 1; }
```

替换为：

```sh
    def log_message(self, *a):
        pass
HTTPServer(("127.0.0.1", int(sys.argv[2])), H).serve_forever()
PY
hookrecv=$!
i=0
until curl -sf -o /dev/null "http://127.0.0.1:$hook_port/"; do
  i=$((i + 1)); [ "$i" -lt 30 ] || { echo "FAIL: webhook receiver did not listen"; cat "$work/hookrecv.log"; exit 1; }; sleep 0.2
done
[ "$(rpc SaveNotifyChannel "$(jq -nc --arg url "http://127.0.0.1:$hook_port/hook" '{channel: {name: "e2e hook", kind: "CHANNEL_KIND_WEBHOOK", webhook: {url: $url}}}')")" = 200 ] || { echo "FAIL: SaveNotifyChannel"; cat "$work/SaveNotifyChannel.json"; exit 1; }
channel=$(jq -r '.channel.id' "$work/SaveNotifyChannel.json")
[ "$(rpc TestNotifyChannel "$(jq -nc --arg id "$channel" '{id: $id}')")" = 200 ] || { echo "FAIL: TestNotifyChannel"; cat "$work/TestNotifyChannel.json"; exit 1; }
```

`scripts/e2e.sh` 原文（4/7）：

```sh
# 至多落在一次 sleep 里，累计值至多多算这一次，其余每一秒都是被等的系统醒着运行的时间。墙钟截止会把
# 整段休眠算进预算，醒来后第一次检查就失败，而那段时间里被等的系统根本没有运行。
wait_alert() {
  transition=$1
  alert_budget_s=$2
  alert_started=$(date +%s)
  alert_slept_s=0
  while :; do
    [ "$(rpc ListAlertEvents '{}')" = 200 ] || { echo "FAIL: ListAlertEvents"; cat "$work/ListAlertEvents.json"; exit 1; }
    if jq -e --arg n "$node2" --arg tr "$transition" '[.events[]? | select(.transition == $tr and .nodeId == $n and any(.deliveries[]; (.ok // false) == true))] | length == 1' "$work/ListAlertEvents.json" > /dev/null; then
      echo "alert $transition delivered after $(($(date +%s) - alert_started))s (${alert_slept_s}s of polling, budget ${alert_budget_s}s)"
      return
```

替换为：

```sh
# 至多落在一次 sleep 里，累计值至多多算这一次，其余每一秒都是被等的系统醒着运行的时间。墙钟截止会把
# 整段休眠算进预算，醒来后第一次检查就失败，而那段时间里被等的系统根本没有运行。
# wait_alert 节点 变化 预算秒数：等该节点恰有一条已送达的该变化事件。
wait_alert() {
  alert_node=$1
  transition=$2
  alert_budget_s=$3
  alert_started=$(date +%s)
  alert_slept_s=0
  while :; do
    [ "$(rpc ListAlertEvents '{}')" = 200 ] || { echo "FAIL: ListAlertEvents"; cat "$work/ListAlertEvents.json"; exit 1; }
    if jq -e --arg n "$alert_node" --arg tr "$transition" '[.events[]? | select(.transition == $tr and .nodeId == $n and any(.deliveries[]; (.ok // false) == true))] | length == 1' "$work/ListAlertEvents.json" > /dev/null; then
      echo "alert $transition delivered after $(($(date +%s) - alert_started))s (${alert_slept_s}s of polling, budget ${alert_budget_s}s)"
      return
```

`scripts/e2e.sh` 原文（5/7）：

```sh
docker kill "$(cat "$work/cid-arm64")" > /dev/null
wait "$arm64" || true
wait_alert firing "$wait_alert_firing_s"
grep -q '"transition":"firing"' "$work/hooks.txt" || { echo "FAIL: webhook body"; cat "$work/hooks.txt"; exit 1; }
docker start "$(cat "$work/cid-arm64")" > /dev/null
run_agent arm64 >> "$work/agent-arm64.log" 2>&1 &
arm64=$!
wait_alert recovered "$wait_alert_recovered_s"
# node1 的 agent 已退出；先推进到新重置日对应的周期，再保存停机前状态。
[ "$(rpc GetTraffic '{}')" = 200 ] || { echo "FAIL: GetTraffic before restart"; exit 1; }
```

替换为：

```sh
docker kill "$(cat "$work/cid-arm64")" > /dev/null
wait "$arm64" || true
wait_alert "$node2" firing "$wait_alert_firing_s"
grep -q '"transition":"firing"' "$work/hooks.txt" || { echo "FAIL: webhook body"; cat "$work/hooks.txt"; exit 1; }
docker start "$(cat "$work/cid-arm64")" > /dev/null
run_agent arm64 >> "$work/agent-arm64.log" 2>&1 &
arm64=$!
wait_alert "$node2" recovered "$wait_alert_recovered_s"

# 计费与到期（§9.4）。UpdateNode 整体替换，billing 与 node1 其余可编辑字段（公开、重置日 15）一起给全。
# hub 以 --timezone UTC 运行，jq 的 now 与 strftime 按 UTC 取日历日。
# node_body 到期日 自动续期：node1 的整份可编辑字段，12.50 USD 按月。billing 里的 daysLeft 由 hub 计算，请求里的 999
# 不起作用：续期后的 0–31、公开快照里与 now 相符、重启后的 4–5、重启后续期的 59–60 四处断言都排除了 999。
node_body() {
  jq -nc --arg id "$node1" --arg exp "$1" --argjson renew "$2" \
    '{id: $id, name: "e2e-amd64", public: true, note: "", trafficResetDay: 15, offlineGraceS: 0,
      billing: {price: "12.50", currency: "USD", billingCycle: "BILLING_CYCLE_MONTHLY", expiresOn: $exp, autoRenew: $renew, daysLeft: 999}}'
}
[ "$(rpc UpdateNode "$(jq -nc --arg id "$node1" '{id: $id, name: "e2e-amd64", public: true, trafficResetDay: 15, offlineGraceS: 0, billing: {price: "12.345", currency: "USD"}}')")" = 400 ] || { echo "FAIL: a three-decimal price was accepted"; cat "$work/UpdateNode.json"; exit 1; }
grep -q 'billing.price: must match' "$work/UpdateNode.json" || { echo "FAIL: price error must name the field"; cat "$work/UpdateNode.json"; exit 1; }
# 自动续期：上月 1 日到期、按月续期。保存触发的扫描当即推后到不早于今天的某月 1 日，响应里已是推后的日期；
# 1 日在任何月份都不钳，跑在月底零点前后也只是推后到这个月或下个月的 1 日。
last_month=$(jq -rn 'now | strftime("%Y %m") | split(" ") | map(tonumber) | if .[1] == 1 then [.[0] - 1, 12] else [.[0], .[1] - 1] end | "\(.[0])-\(if .[1] < 10 then "0" else "" end)\(.[1])-01"')
[ "$(rpc UpdateNode "$(node_body "$last_month" true)")" = 200 ] || { echo "FAIL: UpdateNode with auto renew"; cat "$work/UpdateNode.json"; exit 1; }
jq -e --arg from "$last_month" '.node.billing | .autoRenew == true and .expiresOn != $from and (.expiresOn | endswith("-01")) and .daysLeft >= 0 and .daysLeft <= 31' "$work/UpdateNode.json" > /dev/null || { echo "FAIL: auto renew did not push the expiry date past today"; cat "$work/UpdateNode.json"; exit 1; }
renewed_to=$(jq -r '.node.billing.expiresOn' "$work/UpdateNode.json")
grep -q "msg=\"node expiry renewed\" node_id=$node1 node=e2e-amd64 cycle=monthly from=$last_month to=$renewed_to\$" "$work/hub.log" || { echo "FAIL: renewal not logged"; grep 'expiry' "$work/hub.log"; exit 1; }
# 5 天后到期、关掉自动续期；公开快照带价格、币种、周期、到期日与 days_left，不带自动续期。快照缓存 1 秒。
soon=$(jq -rn 'now + 5 * 86400 | strftime("%Y-%m-%d")')
[ "$(rpc UpdateNode "$(node_body "$soon" false)")" = 200 ] || { echo "FAIL: UpdateNode billing"; cat "$work/UpdateNode.json"; exit 1; }
i=0
until [ "$(pubget billing GetSnapshot '{}')" = 200 ] && jq -e --arg exp "$soon" '.nodes[0].billing.expiresOn == $exp' "$work/pub-billing.json" > /dev/null; do
  i=$((i + 1)); [ "$i" -lt 10 ] || { echo "FAIL: public snapshot lacks the expiry date"; cat "$work/pub-billing.json"; exit 1; }; sleep 0.5
done
jq -e '.nodes[0].billing | .price == "12.50" and .currency == "USD" and .billingCycle == "BILLING_CYCLE_MONTHLY" and (has("autoRenew") | not)' "$work/pub-billing.json" > /dev/null || { echo "FAIL: public billing fields"; cat "$work/pub-billing.json"; exit 1; }
# days_left 与同一响应的 now 出自同一时刻：到期日的 UTC 零点减 now 所在日的 UTC 零点，按天计。
jq -e '(.now | tonumber) as $now | .nodes[0].billing | .daysLeft == (((.expiresOn | strptime("%Y-%m-%d") | mktime) - ($now - $now % 86400)) / 86400)' "$work/pub-billing.json" > /dev/null || { echo "FAIL: public days_left does not match now"; cat "$work/pub-billing.json"; exit 1; }
[ "$(rpc SaveAlertRule '{"rule": {"name": "bad expiry", "kind": "ALERT_KIND_EXPIRY", "enabled": true, "allNodes": true, "daysBefore": 0}}')" = 400 ] || { echo "FAIL: days_before 0 was accepted"; cat "$work/SaveAlertRule.json"; exit 1; }
grep -q 'rule.days_before must be between 1 and 365' "$work/SaveAlertRule.json" || { echo "FAIL: days_before error must name the field"; cat "$work/SaveAlertRule.json"; exit 1; }
# 到期规则在 SaveAlertRule 与 UpdateNode 的请求里同步评估：响应返回时事件已落库并交给投递队列，等待的只是投递。
# 预算 delivery_retry_wait，余量一个 offline_sweep（与上面两处同一余量口径）。
wait_expiry_s=$((retry_wait_s + sweep_s))
# expiry_rule_body 规则 ID：node1 的到期规则，提前 10 天；新建时 ID 为 0。重启后按同一份载荷再保存一次。
expiry_rule_body() {
  jq -nc --arg id "$1" --arg c "$channel" --arg n "$node1" '{rule: {id: $id, name: "e2e expiry", kind: "ALERT_KIND_EXPIRY", enabled: true, allNodes: false, nodeIds: [$n], channelIds: [$c], daysBefore: 10}}'
}
[ "$(rpc SaveAlertRule "$(expiry_rule_body 0)")" = 200 ] || { echo "FAIL: SaveAlertRule expiry"; cat "$work/SaveAlertRule.json"; exit 1; }
jq -e '.rule.kind == "ALERT_KIND_EXPIRY" and .rule.daysBefore == 10' "$work/SaveAlertRule.json" > /dev/null || { echo "FAIL: expiry rule echo"; cat "$work/SaveAlertRule.json"; exit 1; }
expiry_rule=$(jq -r '.rule.id' "$work/SaveAlertRule.json")
wait_alert "$node1" firing "$wait_expiry_s"
jq -e --arg n "$node1" --arg exp "$soon" '[.events[] | select(.nodeId == $n and .transition == "firing")][0].summary | startswith("节点 e2e-amd64 将于 " + $exp + " 到期（剩 ") and endswith("天，规则 e2e expiry）")' "$work/ListAlertEvents.json" > /dev/null || { echo "FAIL: expiry firing summary"; cat "$work/ListAlertEvents.json"; exit 1; }
grep -q '"kind":"expiry","transition":"firing"' "$work/hooks.txt" || { echo "FAIL: expiry webhook body"; cat "$work/hooks.txt"; exit 1; }
# node1 停在 firing 进入重启；续期与恢复放到重启之后。
# node1 的 agent 已退出；先推进到新重置日对应的周期，再保存停机前状态。
[ "$(rpc GetTraffic '{}')" = 200 ] || { echo "FAIL: GetTraffic before restart"; exit 1; }
```

`scripts/e2e.sh` 原文（6/7）：

```sh
[ "$(rpc Login "$login_body")" = 200 ] || { echo "FAIL: login after password change"; exit 1; }
[ "$(rpc ListAlertRules '{}')" = 200 ] || { echo "FAIL: ListAlertRules after restart"; exit 1; }
jq -e '(.rules | length) == 1' "$work/ListAlertRules.json" > /dev/null || { echo "FAIL: alert rule lost"; cat "$work/ListAlertRules.json"; exit 1; }
[ "$(rpc ListAlertEvents '{}')" = 200 ] || { echo "FAIL: ListAlertEvents after restart"; exit 1; }
jq -e '(.events | length) == 2 and all(.events[]; any(.deliveries[]; (.ok // false) == true))' "$work/ListAlertEvents.json" > /dev/null || { echo "FAIL: alert events lost"; cat "$work/ListAlertEvents.json"; exit 1; }
[ "$(rpc ListProbeTasks '{}')" = 200 ] || { echo "FAIL: ListProbeTasks after restart"; exit 1; }
jq -e --arg version "$task_version" --arg icmp "$icmp_task" --arg tcp "$tcp_task" '.version == $version and (.tasks | length) == 2 and all(.tasks[]; (.nodeIds | length) == 2) and ([.tasks[].task.id] | sort) == ([$icmp, $tcp] | sort)' "$work/ListProbeTasks.json" > /dev/null || { echo "FAIL: tasks lost across restart"; cat "$work/ListProbeTasks.json"; exit 1; }
```

替换为：

```sh
[ "$(rpc Login "$login_body")" = 200 ] || { echo "FAIL: login after password change"; exit 1; }
[ "$(rpc ListAlertRules '{}')" = 200 ] || { echo "FAIL: ListAlertRules after restart"; exit 1; }
jq -e '(.rules | length) == 2 and any(.rules[]; .kind == "ALERT_KIND_EXPIRY" and .daysBefore == 10)' "$work/ListAlertRules.json" > /dev/null || { echo "FAIL: alert rule lost"; cat "$work/ListAlertRules.json"; exit 1; }
[ "$(rpc ListNodes '{}')" = 200 ] || { echo "FAIL: ListNodes after restart"; exit 1; }
jq -e --arg id "$node1" --arg exp "$soon" '.nodes[] | select(.id == $id) | .billing | .price == "12.50" and .currency == "USD" and .billingCycle == "BILLING_CYCLE_MONTHLY" and .expiresOn == $exp and (.autoRenew // false) == false and .daysLeft >= 4 and .daysLeft <= 5' "$work/ListNodes.json" > /dev/null || { echo "FAIL: billing lost across restart"; cat "$work/ListNodes.json"; exit 1; }
# node1 在 firing 状态下重启：状态从 alert_state 读回，启动扫描不再发第二条触发。再保存一次同一条规则，让一次扫描
# 在响应之前同步做完，之后再数事件，不与启动扫描赛跑。离线一对加到期的触发，共三条，都已送达。
[ "$(rpc SaveAlertRule "$(expiry_rule_body "$expiry_rule")")" = 200 ] || { echo "FAIL: SaveAlertRule expiry after restart"; cat "$work/SaveAlertRule.json"; exit 1; }
[ "$(rpc ListAlertEvents '{}')" = 200 ] || { echo "FAIL: ListAlertEvents after restart"; exit 1; }
jq -e '(.events | length) == 3 and all(.events[]; any(.deliveries[]; (.ok // false) == true))' "$work/ListAlertEvents.json" > /dev/null || { echo "FAIL: restart changed the alert events (lost, undelivered or fired again)"; cat "$work/ListAlertEvents.json"; exit 1; }
# 重启之后续期：60 天后到期，恢复事件送达，文案写新日期。
later=$(jq -rn 'now + 60 * 86400 | strftime("%Y-%m-%d")')
[ "$(rpc UpdateNode "$(node_body "$later" false)")" = 200 ] || { echo "FAIL: UpdateNode renewal"; cat "$work/UpdateNode.json"; exit 1; }
jq -e '.node.billing | .daysLeft >= 59 and .daysLeft <= 60' "$work/UpdateNode.json" > /dev/null || { echo "FAIL: renewed days_left"; cat "$work/UpdateNode.json"; exit 1; }
wait_alert "$node1" recovered "$wait_expiry_s"
jq -e --arg n "$node1" --arg exp "$later" '[.events[] | select(.nodeId == $n and .transition == "recovered")][0].summary == "节点 e2e-amd64 到期日已更新为 " + $exp + "（规则 e2e expiry）"' "$work/ListAlertEvents.json" > /dev/null || { echo "FAIL: expiry recovered summary"; cat "$work/ListAlertEvents.json"; exit 1; }
jq -e '(.events | length) == 4 and all(.events[]; any(.deliveries[]; (.ok // false) == true))' "$work/ListAlertEvents.json" > /dev/null || { echo "FAIL: alert events after renewal"; cat "$work/ListAlertEvents.json"; exit 1; }
[ "$(rpc ListProbeTasks '{}')" = 200 ] || { echo "FAIL: ListProbeTasks after restart"; exit 1; }
jq -e --arg version "$task_version" --arg icmp "$icmp_task" --arg tcp "$tcp_task" '.version == $version and (.tasks | length) == 2 and all(.tasks[]; (.nodeIds | length) == 2) and ([.tasks[].task.id] | sort) == ([$icmp, $tcp] | sort)' "$work/ListProbeTasks.json" > /dev/null || { echo "FAIL: tasks lost across restart"; cat "$work/ListProbeTasks.json"; exit 1; }
```

`scripts/e2e.sh` 原文（7/7）：

```sh
[ "$(get setting)" = 5 ] || { echo "FAIL: setting rows"; exit 1; }
[ "$(get node)" = 2 ] || { echo "FAIL: node count"; exit 1; }
[ "$(get alert_rule)" = 1 ] || { echo "FAIL: alert rule count"; exit 1; }
[ "$(get alert_rule_node)" = 1 ] || { echo "FAIL: alert scope count"; exit 1; }
[ "$(get alert_event)" = 2 ] || { echo "FAIL: alert event count"; exit 1; }
[ "$(get alert_delivery)" = 2 ] || { echo "FAIL: alert delivery count"; exit 1; }
[ "$(get notify_channel)" = 1 ] || { echo "FAIL: channel count"; exit 1; }
[ "$(get probe_task)" = 1 ] || { echo "FAIL: task count"; exit 1; }
```

替换为：

```sh
[ "$(get setting)" = 5 ] || { echo "FAIL: setting rows"; exit 1; }
[ "$(get node)" = 2 ] || { echo "FAIL: node count"; exit 1; }
[ "$(get alert_rule)" = 2 ] || { echo "FAIL: alert rule count"; exit 1; }
[ "$(get alert_rule_node)" = 2 ] || { echo "FAIL: alert scope count"; exit 1; }
[ "$(get alert_event)" = 4 ] || { echo "FAIL: alert event count"; exit 1; }
[ "$(get alert_delivery)" = 4 ] || { echo "FAIL: alert delivery count"; exit 1; }
[ "$(get notify_channel)" = 1 ] || { echo "FAIL: channel count"; exit 1; }
[ "$(get probe_task)" = 1 ] || { echo "FAIL: task count"; exit 1; }
```

`README.md` 原文（1/2）：

```markdown
- 管理面板在 `/admin/`。离线判定的时限由环境变量 `PROBE_OFFLINE_AFTER` 设定（默认 30s，10s–180s）。
- 其余参数见 `probe-hub serve -h`；节点、注册窗口与 API token 也可在 hub 主机上用 `probe-hub node|window|token` 管理。

## 用 Docker 运行 hub
```

替换为：

```markdown
- 管理面板在 `/admin/`。离线判定的时限由环境变量 `PROBE_OFFLINE_AFTER` 设定（默认 30s，10s–180s）。
- 其余参数见 `probe-hub serve -h`；节点、注册窗口与 API token 也可在 hub 主机上用 `probe-hub node|window|token` 管理。
- 节点可在面板里记录价格、币种、计费周期与到期日：只用于展示与提醒，hub 不汇总、不换算。公开节点填了的这几项也显示在公开页，自动续期开关除外。建「到期」类型的告警规则可在到期前若干天提醒；开着自动续期的节点过了到期日，hub 按周期把到期日推后。到期日按天计，天的边界与流量周期一样取 `--timezone`。

## 用 Docker 运行 hub
```

`README.md` 原文（2/2）：

```markdown
### 时区

镜像里没有 `/etc/localtime`（时区数据已嵌入二进制）。不指定时区时，hub 按 UTC 判定流量周期的重置日，并在启动日志里告警 `host time zone could not be resolved; using UTC`。用 `-e TZ=Asia/Shanghai` 指定（不替换默认参数），或在写全的参数里加 `--timezone Asia/Shanghai`；两者都给时以 `--timezone` 为准。

### 环境变量
```

替换为：

```markdown
### 时区

镜像里没有 `/etc/localtime`（时区数据已嵌入二进制）。不指定时区时，hub 按 UTC 判定流量周期的重置日与节点到期日的天边界，并在启动日志里告警 `host time zone could not be resolved; using UTC`。用 `-e TZ=Asia/Shanghai` 指定（不替换默认参数），或在写全的参数里加 `--timezone Asia/Shanghai`；两者都给时以 `--timezone` 为准。

### 环境变量
```

`proto/SKILL.md` 原文（1/2）：

```markdown
description: 查询自托管探针 hub 的节点、实时状态、历史指标、流量、探测与告警事件。用户问起服务器在不在线、负载、流量、延迟或告警时使用。
```

替换为：

```markdown
description: 查询自托管探针 hub 的节点、实时状态、历史指标、流量、探测、费用与到期，以及告警事件。用户问起服务器在不在线、负载、流量、延迟、费用、何时到期或告警时使用。
```

`proto/SKILL.md` 原文（2/2）：

```markdown
最近 20 条告警事件：
```

替换为：

````markdown
节点的费用与到期。`daysLeft` 是到期日减去今天的天数，今天按 hub 的时区（`--timezone`）取日历日，负数是已过期的天数，由 hub 算好下发；没有到期日（或库里的到期日无法解析）时它缺失，这样的节点不列出。价格与币种是记录用的展示值，hub 不汇总、不换算：

```sh example
curl -fsS -H "Authorization: Bearer $PROBE_TOKEN" -H 'Content-Type: application/json' \
  --data '{}' "$PROBE_HUB/probe.v1.AdminService/ListNodes" | jq '[(.nodes // [])[] | select(.billing.daysLeft != null) | {name, price: .billing.price, currency: .billing.currency, expiresOn: .billing.expiresOn, daysLeft: .billing.daysLeft}]'
```

最近 20 条告警事件：
````

- [ ] **Step 2: 语法检查与卡片一致性**

```bash
cd /Users/xjetry/work/vibe/probe-billing && sh -n scripts/e2e.sh > /tmp/billing-t8-syntax.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && go test -count=1 -run 'Reference|Guide|Skill' -v ./internal/hub/api/ > /tmp/billing-t8-card.log 2>&1; echo $?
```

Expected：两条都是 0；第一条日志为空。第二条 `TestApiReferenceServesTheRepositoryProtoAndGuide` 与 `TestApiReferenceIsReachableWithAToken` 都 PASS，前者核对 hub 下发的卡片与 `proto/SKILL.md` 逐字相同。

- [ ] **Step 3: 跑 e2e**

```bash
cd /Users/xjetry/work/vibe/probe-billing && make e2e > /tmp/billing-t8-e2e.log 2>&1; echo $?
```

Expected：
- 0。`E2E_TIER1` 的两个镜像（Debian、Alpine）各跑一遍，日志里两次 `E2E OK`。
- 每一遍都有到期告警的两行：重启之前的 `alert firing delivered after 0s (0s of polling, budget 15s)`，重启之后的 `alert recovered delivered after 0s (0s of polling, budget 15s)`。评估在请求里同步完成，只等投递。写计划时两遍分别是 0s（触发与恢复，Debian 与 Alpine 相同）。
- hub 日志里有续期的一行，形如 `msg="node expiry renewed" node_id=2 node=e2e-amd64 cycle=monthly from=2026-08-01 to=2026-10-01`：`from` 是上月 1 日，`to` 是不早于今天的某月 1 日（写计划时在 2026-09-27 跑出的就是这一行）。
- `--- stats ---` 段有 `alert_rule: 2`、`alert_rule_node: 2`、`alert_event: 4`、`alert_delivery: 4`。
- 每一遍都有 `card examples ok (empty hub): 6` 与 `card examples ok: 6`：卡片的 `sh example` 由 5 个变成 6 个，新例子在两轮里都执行。
- 镜像冷拉取报 `toomanyrequests` 是 Docker Hub 的匿名限速，属于环境；换个时间重跑同一条命令，不改脚本。

- [ ] **Step 4: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-billing && git add scripts/e2e.sh README.md > /tmp/billing-t8-add.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && git commit -m "e2e: 计费字段、公开快照、到期告警与自动续期；README 记计费与到期" -m "e2e 从 HTTP 入口走一遍计费与到期：畸形价格被拒且错误写明 billing.price；自动续期在保存时即推后并记日志；请求里的 daysLeft 被忽略；公开快照带计费、不带自动续期，daysLeft 与同一响应的 now 相符；到期规则保存即触发并经 webhook 送达；节点停在 firing 进入重启，重启后再保存规则也不重复提醒，续期到窗口之外即恢复；重启后规则、事件与计费都在。两个端口可由 E2E_HUB_PORT、E2E_HOOK_PORT 覆盖，默认仍是 18080/18081。wait_alert 改为带节点参数，离线与到期告警共用。README 写明计费只作展示与提醒，到期日的天边界与流量周期一样取 --timezone。" > /tmp/billing-t8-commit.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && git add proto/SKILL.md > /tmp/billing-t8-add-card.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && git commit -m "proto: 入口卡片写明费用与到期，并给一个按剩余天数列节点的例子" -m 'agent 靠卡片的 description 判断什么时候用这个 API、靠例子学会怎么进门：描述里没有费用与到期，问到"哪台服务器快到期了"时 agent 不会想到这里；有了例子，它不必先读完 proto 才知道 daysLeft 由 hub 按 --timezone 算好、缺失表示没有到期日。例子与卡片里其它例子同样标成 sh example，e2e 在有数据的那一轮执行它并要求输出非空，select 用的字段名写错就红（投影里的字段名写错只会输出 null，仍非空）。' > /tmp/billing-t8-commit-card.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && git status --porcelain > /tmp/billing-t8-status.log 2>&1; echo $?
```

Expected：五条都是 0；`billing-t8-status.log` 为空。两个提交：e2e 与 README 一个，入口卡片一个。

- [ ] **Step 5: make ci**

```bash
cd /Users/xjetry/work/vibe/probe-billing && make ci > /tmp/billing-t8-ci.log 2>&1; echo $?
```

Expected：0；vitest `Tests  396 passed (396)`。

- [ ] **Step 6: 缺陷注入**

e2e 一遍要几分钟，注入只跑 Alpine 一个镜像。每项：改动 → `git diff --stat` 非空 → 重建 hub → 跑命令 → 核对首个 `FAIL` 行 → `git checkout -- internal proto` → 再重建 hub。卡片由 `go:embed` 编进 hub，改它同样要重建。

重建与命令：

```bash
cd /Users/xjetry/work/vibe/probe-billing && go build -o bin/probe-hub ./cmd/hub > /tmp/billing-t8-inj-build.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-billing && AGENT_IMAGE=alpine:3.21 EXPECT_OS=Alpine scripts/e2e.sh > /tmp/billing-t8-inj-<项>.log 2>&1; echo $?
```

| | 改动 | 期望 |
|---|---|---|
| a | `internal/hub/api/public.go` 的 `GetSnapshot` 不填 `PublicNode.billing`：投影那段的条件改成 `b != nil && false`，同 Task 4 注入 h | 1，`FAIL: public snapshot lacks the expiry date`（约 5 秒的轮询之后） |
| b | `internal/hub/api/nodes.go` 的 `UpdateNode` 不做计费变化后的扫描（`if billingChanged {` 改成 `if billingChanged && false {`） | 1，`FAIL: auto renew did not push the expiry date past today`：响应里的到期日仍是上月 1 日（写计划时 `expiresOn` 2026-08-01、`daysLeft` −57） |
| c | `internal/hub/alert/engine.go` 的 `SaveRule` 不做保存后的扫描（条件末尾加 `&& false`） | 1，`FAIL: firing alert not delivered after 15s of polling`：到期规则保存后没有评估，要等到零点 |
| d | `internal/hub/api/nodes.go` 的 `UpdateNode` 响应沿用请求里的 `days_left`，写法同 Task 4 注入 r（nil 安全：e2e 里改重置日的 `UpdateNode` 不带 `billing`） | 1，`FAIL: auto renew did not push the expiry date past today`：响应里的到期日已推后（2026-10-01），`daysLeft` 却是请求里的 999，续期后 0–31 的断言排除了它 |
| e | `internal/hub/store/alert.go` 的 `KindExpiry` 字面值改成 `"expiry_x"`（单测都引用常量，只有 webhook 载荷里的种类名跟着变） | 1，`FAIL: expiry webhook body`：webhook 收到的到期告警是 `"kind":"expiry_x","transition":"firing"`，同一文件里的测试通知与离线告警照旧 |
| f | `internal/hub/alert/engine.go` 的 `Load` 不读回状态（条件末尾加 `&& false`） | 1，`FAIL: restart changed the alert events (lost, undelivered or fired again)`：重启后状态没有读回，重启后再保存规则的那次扫描把同一节点当作刚进入提醒窗口，又发了一条同样文案的触发，事件变成 4 个 |
| g | `proto/SKILL.md` 新例子的 `select(.billing.daysLeft != null)` 改成 `select(.billing.days_left != null)`（JSON 里没有这个名字） | 1，空 hub 那一轮照常通过（`card examples ok (empty hub): 6`），重启后有数据的那一轮 `FAIL: card example …/card-example-4.sh printed empty JSON`：`select` 什么都选不出 |

e2e 的其余断言不逐条在 e2e 上注入，各自的缺陷由单测的注入覆盖：
- 三位小数的价格被拒、错误写明 `billing.price`：Task 4 注入 a。
- 续期落库、日志一行：Task 3 注入 i（扫描快照不随续期更新）、t（`serve` 不启动循环，启动扫描不续期）。日志的字段由 `TestSweepExpiryRenewsBeforeEvaluating` 逐字比对。
- 公开快照不带 `autoRenew`：Task 1 注入 b（`PublicBilling` 不 reserve 自动续期的号，投影构造即 panic）、e（允许列表）。`daysLeft` 与 `now` 相符：Task 4 注入 g、h。
- 请求里的 `daysLeft: 999` 不起作用：Task 4 注入 r，e2e 注入 d。
- `daysBefore: 0` 被拒：Task 3 注入 n。回显 `daysBefore`：Task 4 注入 m。
- 触发与恢复的文案：`TestSweepExpiryFiresAndRecoversWithSpecSummaries` 逐字比对四句，Task 3 注入 r；恢复文案按原因三选一由 Task 3 注入 w 钉住。改到期日即恢复：Task 4 注入 j。
- 重启后两条规则且带 `daysBefore`：Task 2 注入 l。计费原样：Task 2 注入 e、n。
- 重启时 node1 停在 firing，重启后再保存一次规则（同步扫描一轮）事件仍是 3 个：e2e 注入 f 在线上钉住；单测侧，触发日期落库与读回由 Task 2 注入 p、q 钉住，`Load` 读回它由 Task 3 注入 x 钉住，只提醒一次由 Task 3 注入 ab 钉住。
- 没有单独注入的：统计的行数。
- 钉不住的：卡片新例子投影里的字段名写错（如 `expiresOn: .billing.expires_on`）时输出带 `null`，仍非空，e2e 不红。卡片里其它例子也只要求非空，是同一口径。

---

## 执行顺序与并行

**本计划内**
- 依赖：
  - Task 1 最先：Task 4–8 用它的生成代码。
  - Task 2 改 store 及其调用点（`api/nodes.go`、`alert/engine.go` 与十三个既有测试文件），不依赖 Task 1；按序做即可。
  - Task 3 依赖 Task 2（`Billing`、`RenewExpiry`、`KindExpiry`、`StateRow.FiredExpiresOn`、`RecordTransition` 的新参数）。
  - Task 4 依赖 Task 1、2、3（生成代码与 `Public.billing` 投影、`NodeEdit`、`alert.ParseDate` 与 `SweepExpiry`）。
  - Task 5、6 依赖 Task 1 的 TS 生成代码；单测不依赖 Task 2–4。
  - Task 7 依赖 Task 5（`lib/billing.ts`）。
  - Task 8 最后。
- 推荐单工作树按 1→8 顺序做。要并行时，Task 5、6 可以在 Task 1 提交之后另开工作树，与 Task 2–4 同时进行，Task 7 开始前合回。Task 2–4 不改 `web/`，Task 5、6 不改生成物，预计不冲突。合回后在合并结果上重跑 `make ci`。
- e2e 只在 Task 8 跑。中间任务的边界上，按推理 e2e 也是绿的：计费字段都是新增，旧的 e2e 断言不涉及它们；离线规则的 e2e 载荷不带探测字段，Task 3 的新检查不影响它。这一条没有在中间边界实跑。
- 端口：e2e 默认用 18080/18081，只有 e2e 任务能用；`E2E_HUB_PORT`、`E2E_HOOK_PORT` 可以覆盖（设计决定 20）。

**合回主干前**
1. `git log --oneline $(git merge-base main billing)..main` 读标题，找同类实现。
2. `git merge-tree --write-tree --name-only main billing` 无副作用预演冲突。
3. 生成物（`gen/`、`web/src/gen/`）冲突不手解，改源文件后重跑 `make gen`。
4. 按当前主干重新枚举下面这些跨面清单。它们是横切改动：自动合并对缺口无感，不冲突、编译过、测试绿，功能却静默失效。
   - **迁移号：** 主干若也加了迁移 9，本分支改用下一个号；`schemaVersion`、`migrationV9` 的名字与 `billing_test.go` 的 `schemaV8` 夹具一并调整。主干若给 `alert_state` 加了列，`fired_expires_on` 在新建库里的位置随之调整，使列序与迁移结果一致。
   - **告警种类：** `AlertKind` 的枚举值在三处成表：`internal/hub/api/alerts.go` 的 `alertKinds`、`web/src/lib/alerts.ts` 的 `ALERT_KINDS`、`CheckRule` 的 `oneOf`。主干若加了新种类，它的专用字段也要进 `store.CheckKindFields`（存储层与 `CheckRule` 共用的唯一谓词），面板的 `toRule` 也只发它自己的字段。
   - **字段号：** `Billing` 1–6；`Node.billing = 12`、`UpdateNodeRequest.billing = 7`、`PublicNode.billing = 9`、`AlertRule.days_before = 13`。主干若占了其中的号，改用下一个空号；`PublicBilling` 跟随 `Billing` 的号。`public_test.go` 的允许列表随之核对。
   - **状态写入口：** 主干新增的 `RecordTransition` 调用要给 `firedExpiresOn`（编译会报）；新增的状态写路径若绕开 `setAlertState`，要同样整行写这一列。
   - **构造函数的调用点：** 主干新增的 `alert.New`、`api.New`、`api.NewPublic` 调用都要带 `Location`，否则在测试或启动时 panic。枚举命令如下，其中 `internal/hub/ingest` 的那行是另一个 `New`，与此无关：

     ```bash
     cd /Users/xjetry/work/vibe/probe-billing && git grep -nE 'New\((alert\.|api\.)?Config\{|NewPublic\((api\.)?PublicConfig\{' -- cmd internal > /tmp/billing-merge-ctors.log 2>&1; echo $?
     ```

   - **`store.UpdateNode` 的调用点：** 主干新增的调用要改用 `NodeEdit`；这一条编译会报。
   - **e2e 的计数：** 末尾的统计行数与重启后的规则数、事件数。主干若在 e2e 里新增规则或事件，数字要合起来算。

**与其他工作**
- 主工作树里有未提交的 `FEATURES.md` 改动，不涉及本计划改的文件。本计划不碰它。

## 自查记录

**spec 覆盖**

| spec | 落点 |
|---|---|
| §9.4 字段、存法、校验、空值含义；校验在 `UpdateNode` 一处；`Billing` 子消息，缺失即全清 | Task 1（协议）、Task 2（列与迁移）、Task 4（`billingOf`） |
| §9.4 日期按天、天边界取 `--timezone`；`days_left` 由 hub 下发，请求里的值忽略 | Task 3（`Today`、`DaysLeft`、`Config.Location`）、Task 4（管理与公开两端）、Task 5、7（只显示） |
| §9.4 自动续期：推后、钳位、落库与日志、周期为空不推后 | Task 2（`RenewExpiry`）、Task 3（`renewedExpiry`、`sweepExpiry`） |
| §9.4 公开：`PublicBilling` 由投影生成，投影扩展到枚举，自动续期 reserved | Task 1（`projection.go`、`public.go`、投影测试）、Task 4（`GetSnapshot`）、Task 7（页面） |
| §9.1 到期规则、`days_before` 1–365、种类专用字段显式拒绝，保存与载入共用 | Task 2（`store.CheckKindFields`，存储层拒绝写入）、Task 3（`CheckRule` 经它转成字段错误）、Task 4（`SaveAlertRule`）、Task 6（面板只发当前种类的字段） |
| §9.2 没有 `pending`、四处评估时机、日界、先推后再评估、只提醒一次、三种恢复文案、`value`、读不懂的日期、快照收敛 | Task 3；恢复原因的判断见设计决定 18 |
| §10 卡片与节点页的费用、到期两行，已过期标红 | Task 7 |
| §6.6 表结构 | Task 2（v9，冻结语句；含 `alert_state.fired_expires_on`） |
| §12 公开消息字段允许列表 | Task 1 |
| README（控制端要求） | Task 8 |
| 入口卡片写明费用与到期（`docs/guidelines/agent-first.md`：卡片写清 schema 在哪，例子由 e2e 执行） | Task 8 |

`FEATURES.md` 第 51 行：Litestream 的重新考虑条件不因本计划成立，不改。

**Review Focus 与测试的对应**：十二条各落在 Review Focus 一节写明的任务与测试里；e2e 另从线上复核了第 5 条的一半（公开快照的 `daysLeft` 与同一响应的 `now` 相符）与第 10 条的一半（请求里的 `daysLeft: 999` 不起作用）。

**占位扫描**：没有 TBD，没有"类似 Task N"。每个代码步骤给出整份新文件，或精确的原文与替换；原文在当时的文件里都只出现一次。

**类型与名字一致**：`Billing`、`PublicBilling`、`BillingCycle`、`NodeEdit`、`BillingCycles`、`RenewExpiry`、`CheckKindFields`、`KindFieldError`、`checkKindFields`、`StateRow.FiredExpiresOn`、`fired_expires_on`、`RecordTransition`、`ParseDate`、`Today`、`DaysLeft`、`NextExpiry`、`SweepExpiry`、`RunExpirySweep`、`expirySummary`、`entry`、`apply`、`billingOf`、`billingProto`、`nodeProto`、`newZonedHarness`、`BillingView`、`cycleLabel`、`priceText`、`expiryText`、`expired`、`BILLING_CYCLES`、`wait_alert`、`node_body`、`expiry_rule_body` 在定义处与全部使用处同名同签名；前端的周期表与种类表由测试对照协议枚举。

**写计划时实跑过的验证**（仓库之外的副本；go1.27.1 darwin/arm64、Node v26.9.0、pnpm 12.5.1、vitest 5.0.1、buf 1.50.0、jq-1.7.1-apple、Docker 29.4.0）：
- 副本在 af62cf9 上做完 Task 1–8，每个任务一个提交。本计划的代码块由这 8 个提交逐文件生成；反过来，把计划里的"新建"与"原文→替换为"按任务顺序应用到 af62cf9 的干净检出上，Task 1 之后再跑 `make gen`，每个任务之后的整棵树都与对应提交逐字节相同。
- 跑红：在每个任务的父提交上放入该任务改动的全部测试文件，跑 Step 2 的命令，退出码与原文如各任务所记。
- 跑绿：每个任务的提交上跑 Step 4 的命令，全部退出 0。Task 1 的 `buf breaking` 另做了冒烟（实验 8）。
- `make ci`：基点与 8 个提交上都跑过，全部退出 0，生成物干净；vitest 用例数依次为 369、369、369、369、369、387、394、396、396。
- 注入：Task 1–7 的 118 项在对应提交上逐项实跑。116 项退出 1，红在表中写的原因；Task 3 的 ad、ae 按预期退出 0（实验 13）。复跑脚本对每项先核对原文恰出现一次、`git diff --stat` 非空，跑完还原并确认工作树干净。
- 日界循环改为扫描之前读钟之后，Task 3 在 billing 分支的 60ac1dc（Task 1、2 已落地）上重做：计划里的 Task 3 逐条施加，锚点都唯一命中，整棵树与重做的提交逐字节相同；跑红与计划所记相同；跑绿两条都是 0；`make ci` 退出 0，vitest 369；Task 3 的 36 项注入全部重跑，34 项退出 1，ad、ae 退出 0。Task 4–8 的 71 处改动在新的 Task 3 之上逐条施加，都唯一命中，做完的树上 `make ci` 退出 0。
- 执行中的逐任务审阅改了 Task 1、2、3、5、6、7 的测试、注释与说明文字，计划按落地的提交同步。Task 1–8 按本计划在 af62cf9 上逐任务重建：Task 2 之后的整棵树与 billing 分支 30a5ef5 只差 `RenewExpiry` 的一段注释（billing-t3fix 分支 73222b3 改的），Task 3 之后的整棵树与 73222b3 逐字节相同；Task 7 之后的 `web/` 与 billing-web 分支 be7ab7a 逐字节相同。Task 1、2、5、6、7 重新跑红，Task 5–7 重新跑绿，Task 1–3、5–8 重新跑 `make ci`，Task 2、5、6、7 的注入全部重跑（21、11、8、7 项，全部退出 1）。同步 73222b3 之后，Task 3 重新跑红、跑绿与全部 39 项注入（37 项退出 1，ad、ae 退出 0），Task 2、3、8 重新跑 `make ci`。同步 d41ae4a、bf4aeb6、afda87e、3b174ae、e253156 与 59a033e 之后，Task 8 之后的整棵树与 59a033e 逐字节相同。Task 4 重跑注入 a、h、v（v 的两种读钟顺序各一次；h 另把投影那段整段删掉，编译失败在 `declared and not used: today`）。在与 afda87e 相同的那棵树上，Task 4、8 重新跑 `make ci`（vitest 369、396），Task 8 重跑语法检查与卡片一致性测试，实验 10 的依赖核对也在这棵树上重做。在与 59a033e 相同的树上：Task 3 的 alert 包重跑为 0，注入 a、al、am 重跑（`expiry_test.go` 的行号随新增的说明与两行用例后移），新增的注入 an 实跑；Task 4 重新跑红、跑绿与 vet，新增的注入 w、x 实跑；实验 1 在同一环境重跑，加了 Cairo；Task 8 之后的树上 `make ci` 退出 0，vitest 396。
- e2e：与 afda87e 相同的那棵 Task 8 树上先 `make binaries`，Debian 与 Alpine 各跑一遍，都是 `E2E OK`，两遍都有 `card examples ok (empty hub): 6` 与 `card examples ok: 6`。这两次运行跑的就是那棵树里的 `scripts/e2e.sh`，只用 `E2E_HUB_PORT=18193 E2E_HOOK_PORT=18194` 把端口挪开（18079–18092 留给别的工作），没有拷贝脚本。表中七项注入的 `FAIL` 行取自 billing 分支上同一棵树的两次逐项实跑（Alpine、默认端口，首个 `FAIL` 两次相同），本副本没有重跑。
- 实验 1–4、6、8、11、13–15 的原文与实验 10 的依赖核对来自同一环境。
- 副本在写完计划后删除。

**没有实跑的**：
- `make e2e` 本身，以及 18080/18081 上的 e2e。由 Task 8 Step 3 覆盖。
- 浏览器验收：页面改动由 vitest 覆盖，没有在真实浏览器里看过计费列与到期表单的排版。
- 中间任务边界上的 e2e（见"执行顺序与并行"）。

## 对 spec 的回写

以 main 10a7409 的 spec 逐条核对过：计划与 spec 一致，没有需要回写或裁决的条目。

**已一致**
- §1、§15：节点费用与到期、到期提醒在范围之内，里程碑是"M6 之后：节点计费"，即本计划。
- §6.6：`alert_state` 带进入 firing 时的到期日 `fired_expires_on`（Task 2，设计决定 18）。
- §9.1：到期规则、`days_before` 1–365；探测专用字段对离线与到期规则必须为零、`days_before` 只属于到期规则，由 `store.CheckKindFields` 一处裁决，存储层与 `CheckRule` 都用它，保存与载入共用。
- §9.2：没有 `pending`；评估时机四处；零点不存在的日子取新一天的第一个时刻，`time.Date` 的结果本地日期没变才取该时段的结束处（偏移为负往回、为正往前，实验 1）；先推后再评估；只提醒一次，到期当天"剩 0 天"；三种恢复文案，"日期没变"与进入 firing 时记在 `alert_state` 里的到期日比，成因通常是提前天数调小，换时区或墙钟回拨也会；`value`；`days_before` 不是身份；读不懂的到期日；扫描快照与并发编辑的收敛、续期写回的条件更新。
- §9.4：`Billing` 子消息与字段表、校验、空值含义；`days_left` 由 hub 按 `--timezone` 算好下发、保存请求里的值忽略；`billing` 缺失即五项全清；自动续期的推后、钳位与漂移；`PublicBilling` 由投影生成，投影扩展到枚举。
- §10：卡片与节点页的费用、到期两行，已过期标红；费用行在价格或周期任一填了时显示，只填周期时是"每年"这类周期字样（Task 5 的 `priceText`，只填币种时为空）；`PublicNode` 带 `PublicBilling`。
- §12：公开消息的字段允许列表随 `PublicBilling` 更新。

## 执行修正（执行与整分支审阅后记录；代码以分支为准）

**计划文字与实测不符（没改行为）**
- Task 3 注入 m：除 `task_id`、`metric`、`threshold`（两条）、`for_minutes` 外，`TestCheckRule/offline_days_before` 也红（`rule_test.go:55`）。离线分支里的 `checkKindFields` 同样拒绝 `days_before`，注入去掉的正是这一步。正文已补这一行。
- Task 4 注入 a：全角 `１２` 那一行也红。去掉锚点后它照样被拒（RE2 的 `\d` 只认 ASCII），红是因为错误原文里插入的是改动后的正则。另有 `billing_test.go:96: rejected updates changed the node` 一行。正文已补。
- Task 4 注入 v：原文没写两次读钟的先后，期望输出对应先为 `today` 读、再读 `now`。反过来的顺序红在同一条断言，数字对调（`now 1767283199 is 2026-01-01 in the hub zone, so days_left should be 59; got 58`）。正文已写明顺序与另一种顺序的输出。
- Task 4 注入 h 与 Task 8 注入 a：原文"删掉投影那段"会让 `today` 未使用，编译失败（`internal/hub/api/public.go:233:2: declared and not used: today`），单测与 e2e 都跑不起来。改为把投影那段的条件改成 `b != nil && false`，红在原来的期望行。
- vitest 用例数：Task 5–8 的 `make ci` 原文是 386、392、394、394，实测 387、394、396、396。Task 5 的修复补了计费错误回显一例，Task 6 的修复补了探测规则改成到期一例。正文已按实测。
- 几处注释与提交信息描述的是后续任务才落地的行为：在各自的提交上还不成立，合到一起后成立，所以分支只能整体合回。这些没有改：
  - Task 1 的 proto 注释（`UpdateNode` 的同步扫描、离线与到期规则带探测字段一律拒绝、`kind` 可取到期、`Node.billing` 的回显）与 Task 2 的提交信息与注释（`Load` 读回、`CheckKindFields` 由 `alert.CheckRule` 调用），到 Task 2–4 成立。
  - Task 3 的 `SweepExpiry`（节点计费字段变化时调用它）、`sweepExpiry`（那次 `UpdateNode` 提交后自己调用 `SweepExpiry`）、`ParseDate`（写侧 api 的 `UpdateNode`）、`cycleMonths`（写入口拒绝表外值）四处，到 Task 4 成立。
  - Task 5 `web/src/lib/billing.ts` 头注释的"公开入口也引用它……（importScan.test.ts 钉住）"，到 Task 7 公开页引用它之后成立。`importScan.test.ts` 按真实构建的模块图扫描，不需要往清单里补。

**源自计划原文、执行后审阅改掉的缺陷**
- Task 1 `proto/probe/v1/types.proto`：`Billing` 的消息注释"也不拿它做任何计算"与同一消息里驱动 `days_left`、自动续期与到期规则的到期日和周期相矛盾，收窄为价格与币种（spec §9.4 同步收窄）。`Billing.days_left` 与 `public.proto` 的 `PublicBilling.days_left` 的缺失情形补上"库里的到期日无法解析"。`internal/hub/api/projection_test.go` 的夹具注释写明 `OtherEnumField` 是与 `EnumField` 对不齐。
- Task 2 `internal/hub/store/alert.go`：原文的替换把 `KindFieldError` 与 `CheckKindFields` 插在 `SaveAlertRule` 与它的文档注释之间，那段注释移回 `SaveAlertRule` 正上方。`schema.go` 的 `ddlAlertRule` 里 `days_before` 列注释原把保证方写成 `alert.CheckRule`，改为逐条写：非到期规则写 NULL 由 `SaveAlertRule` 与 `CheckKindFields` 保证，1–365 由 `alert.CheckRule` 在保存与载入时裁决，存储层不查。`internal/hub/store/billing_test.go` 的 `TestUpdateNodeReplacesBillingAndReportsChange` 补了只改到期日、只改价格两步。
- Task 3 `internal/hub/alert/expiry.go` 的 `SweepExpiry` 文档注释：原文"唯一的评估入口……保存启用的到期规则时各调用一次"是排他句，也与代码不符：保存规则时是 `SaveRule` 在已持的 `writeMu` 下直接调 `sweepExpiry`。改为分别写两种调用。
- Task 3 `TestRunExpirySweepDoesNotSkipADayBoundaryCrossedDuringASweep`：原先只钉住"循环在扫描之前读钟"。扫描拖过零点、定时器时长为负而立即重扫的那条路径没有钉住：把负时长改成等到再下一个日界，`internal/hub/alert` 与 `cmd/hub` 全绿。改用前 N 次读钟返回零点前的钟，N = 1、2 各跑一遍；注入 aj 只红 N = 1，ak 只红 N = 2。
- Task 3 续期写回的条件更新落空：原文把原因只写成 `UpdateNode` 改了计费，漏了删除。api 的 `DeleteNode` 在 `nodeMu` 下提交删除，不经 `writeMu`，扫描读完快照之后节点可以被删掉。`expiry.go`、`expiry_test.go` 与 `internal/hub/store/node.go` 的 `RenewExpiry` 三处注释改为非排他的逐条陈述。
- Task 3 `RenewExpiry` 报错：原文照常按未推后的日期评估，开着自动续期的节点会发出一对假告警。改为与落空同样跳过该节点：本轮不评估、保留状态，错误照常返回。新用例 `TestSweepExpirySkipsANodeWhoseRenewalFailed` 用触发器让写回 `RAISE(ABORT)`，注入 al 钉住。
- Task 3 `TestSweepExpiryRecoverySummaryFollowsTheReason`：`before` 原在重启后的第一次扫描之后才取，§9.2 的"重启后不重复触发"没有到期专属的钉子。挪到 `f.billing` 之前，注入 am（`Load` 不载入到期规则的状态）红在第一步。
- Task 3 `internal/hub/alert/expiry.go` 的 `nextDayStart` 注释只写了 Santiago、Havana 往回给前一天 23:00 的情形，实验 1 的标题也写成"往回归一"。Cairo、Beirut 的 `time.Date` 往前给新一天的 01:00，本地日期已变，走普通分支；照原注释把判断改成"时分不为零就取 `ZoneBounds` 的 end"，这类时区的日界会定到几个月后夏令时结束的那次切换。方向随 UTC 偏移的正负而异：偏移为负往回，偏移为正往前。注释写清两个方向、成因与只看本地日期的判据，`TestNextDayStartIsTheFirstInstantOfTheNextLocalDay` 补 Cairo、Beirut 两行与说明，注入 an（按时分判断）只红这两行。spec §9.2 同口径。
- Task 3 `expiry.go` 的头注释"hub 时区（--timezone）只在 Today 里出现一次"是排他句，`nextDayStart` 也按同一时区算下一个日界。改为：日期值里 hub 时区只经 `Today` 进入，下一个日界的时刻由 `nextDayStart` 按同一时区算（`RunExpirySweep` 传入 `cfg.Location`）。
- Task 4 `internal/hub/api/nodes.go` 的 `UpdateNode`：锁序注释的括号里只列了各次扫描、`apply` 与 `Sender.Enqueue`，读着像持 `writeMu` 路径的全集，实际还有 `Load`、`SaveRule`、`DeleteRule`、`SaveChannel`、`DeleteChannel`、`Forget`。改为保证方本身：alert 包不 import api，且引擎经 `SetSender` 注入的实现（当前是 `alert.Queue`）也不在 api 里，任何持 writeMu 的路径都取不到 nodeMu。只写 import 方向不够：注入的实现若来自 api，也能在 writeMu 下取到 nodeMu。实验 10 同改。
- Task 4 `internal/hub/api/alerts_test.go`：`alertKinds` 把协议种类翻成存储种类，协议加了种类而表漏配时编译照过；新种类若还没有协议层的用例，别的用例也不会红，保存时 `parseEnum` 以 `rule.kind` 拒绝、列表回显给出 `UNSPECIFIED`。补 `TestAlertKindsMapEveryValue`：按协议枚举全集核对映射与往返，并要求映射出的每个存储种类都被 `alert.CheckRule` 接受，与 `TestBillingCyclesMapEveryValue` 同一写法；注入 w、x。
- Task 5 `web/src/lib/billing.test.ts` 与 `web/src/pages/Nodes.test.tsx`：夹具的 `daysLeft` 恰好等于按今天的本地日期算出的天数，按本地日期重算的注入全绿。两处把 `Date` 固定到 2030-06-15，只替换 `Date`。"编辑回传全部字段"改用带计费的节点，断言不碰计费时提交体仍按当前值带着五项计费。另补了计费错误回显一例。
- Task 6 `web/src/pages/AlertRules.tsx`：到期表单的说明"续期或清除到期日即恢复"与 §9.2 不符，改为按提醒范围写：到期日移出范围、清除到期日或调小提前天数使它落到范围之外才恢复，续期后仍在范围内不恢复。到期种类提交体里的 `threshold`、`taskId` 原先没有钉住，因为新草稿的这两项本来就是空的；补"探测规则改成到期时只带提前天数"一例，注入 h 红在它上面。`AlertRules.test.tsx` 的测试注释"非探测种类的探测字段与提前天数都必须是零值"对到期规则不成立，按种类分开写。`emptyDraft` 的注释原说每种规则的专用字段都有初值，改为写明哪几项有默认值、探测任务与阈值留空。
- Task 7 `web/src/public/Overview.test.tsx` 与 `NodePage.test.tsx`：夹具日期与今天吻合，"只显示 hub 下发的 `daysLeft`"没有钉住。两处把 `Date` 固定到本地 2031-01-01，`NodePage.test.tsx` 另在 `afterEach` 恢复真实计时器。`NodePage.tsx` 静态信息卡的显示条件 `node.facts || price || expiry` 里的 `expiry` 没有用例，补一个只有到期日的节点。
- Task 8 `proto/SKILL.md`：入口卡片的 `description` 与例子都没有费用与到期。用户问哪台快到期时 agent 可能不会加载这张卡片，加载了也不知道信息在 `ListNodes` 的 `billing` 里。`description` 补上费用与到期，"例子"一节加一个按 `daysLeft` 列节点的例子，由 e2e 执行；`card examples ok` 由 5 个变成 6 个。
- Task 8 `scripts/e2e.sh` 与提交信息：`wait_alert` 的说明原写"等该节点恰有一条该变化的事件且已送达"，而 jq 数的是已送达的该变化事件恰为一条，同一变化的未送达事件不计；改为"等该节点恰有一条已送达的该变化事件"。e2e 提交信息里的"续期即恢复"与 §9.2 不符：续期后仍在提醒范围内不恢复；改为"续期到窗口之外即恢复"。
