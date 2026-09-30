# Agentic 写入验证记录

## 代码与环境

- 实施计划：`2026-09-30-agentic-write.md`。以下凭据记录提交前的工作树；未推送或部署。
- 验证基点：`2834d920fd4f19364040b12902fd86d94455a742`，当时实现位于未提交工作树。
- 当前被测代码差异（排除计划文档）保存在 `build/agentic-validation/verified-code.diff`，SHA-256：`8188a033ae07a42d2fbeab33d53c584da7ff8bb89dc51746870651d281413ff5`。正式审查时的差异另存 `reviewed-code.diff`，SHA-256：`9767f784a1e424374c2f28c68c9e4fe988d5bb41222ef684b791177c567e5871`；其后只扩充两个测试文件，生产逻辑未变。用户新增的发布记录提交保留。
- macOS，Go 1.27.1，前端使用仓库现有 pnpm 环境。
- 运行中环境由 unrestricted 切换为 workspace-write，网络和本机监听受限，审批不可用。之后的 Go 命令使用 `GOCACHE=$PWD/build/go-cache`，避免写入不可写的系统缓存。
- 日志保存在忽略目录 `build/agentic-validation/`；不将大段日志写入源码。

## 已观察结果

| 命令或验证 | 实际结果 |
| --- | --- |
| `go test -count=1 ./cmd/hub` | 历史夹具完整撤回 schema 25 后整包通过，`cmd-hub.log`。 |
| `go test -count=1 ./internal/hub/api -run '^TestAgentic'` | 在原始 httptest 监听方式、收紧环境之前通过，包含凭据预览、并发重试、数据库补全与动态选择器测试，`api-extra.log`。后续审查修复不在这次证据内。 |
| `pnpm test -- src/pages/ApiTokens.test.tsx` | 当前脚本实际运行全套，66 文件、750 测试通过，`web-final.log`；与发现回执重复 key 时使用同一命令。 |
| `pnpm run build` | 成功，存在大于 500 kB 的 bundle 提示，`web-build-final.log`。 |
| `go vet ./...` | 通过，`vet-final.log`。 |
| `GOOS=linux GOARCH=amd64 go vet ./cmd/hub ./internal/hub/...` | 最后回执标识增量后通过，`vet-linux.log`；原生 vet 同时重新通过。 |
| `buf generate`、`buf lint` | 回执标识协议增量重新生成 Go/TS 代码，两个命令均退出 0。 |
| `go test -count=1 ./internal/hub/store -run 'TestAgentic\|TestChangeRechecks\|TestQueuedUpdate'` | 通过，`store-replay.log`；覆盖撤销、注册范围、更新派发复核、重启/恢复/清理和删除后重放。 |
| 独立副本 Handler 验证 | 将测试 transport 改为直接调用原始 HTTP Handler，不绑定端口；生产文件保持与被测工作树一致。基线与修复后全部 `TestAgentic` 通过。此方式覆盖 Connect 编解码、鉴权、服务、存储与内存发布，不代替 TCP 监听验收。 |
| 并发同键轮换 `-count=100` | 旧实现出现 `aborted: resource changed; preview again`；统一失败出口回查持久回执后 100 次通过，`concurrent-retry-stress.log`、`concurrent-retry-green.log`。 |
| 跨范围读取及全局告警 | 历史任务迁出范围后不泄露新目标；限定凭据修改计费/保存到期规则不裁剪其他节点状态；原 session 图例语义保留。修复前失败，修复后通过，`cross-surface-red.log`、`cross-surface-green.log`。 |
| 告警引用的读写范围 | `TestAgenticAlertScopeIncludesReferencedProbe` 对照指定节点、全站 token 与 session；列表和删除预览都纳入引用探测的范围，范围内允许、跨范围拒绝。修复前准确红，修复后绿，`alert-reference-scope-red.log`、`alert-reference-scope-green.log`。 |
| 存储与迁移最终回归 | `store-core-final.log` 退出 0，覆盖 Agentic、事务复核、queued update、迁移、告警 CRUD/引用/范围、节点和标签；不是完整 store 包通过。 |
| 告警扫描和更新管理器 | `go test -count=1 ./internal/hub/alert ./internal/hub/updates -run 'TestSweep\|TestRunExpiry\|TestExpiry\|TestEvaluate\|TestUpdate'` 退出 0，`alert-updates-final.log`。 |
| 最终 Handler 竞态检查 | 独立副本 `go test -race -count=1 ./internal/hub/api`，选择 Agentic、token/access、旧探测图例、告警 CRUD/动态范围/到期/状态相关测试，退出 0，41.620 秒，`handler-race-final.log`。包括成功启动/取消更新、原始 JSON、事务回执失败、回执标识、掩码清空/拒绝与 JSON 秘密测试。 |
| 最终并发重试压力 | 副本 `go test -count=100 ./internal/hub/api -run '^TestAgenticPreviewKeepsCredentialsAndConcurrentRetry$'` 退出 0，`concurrent-retry-final.log`。 |
| 恢复后数字 ID 复用 | `TestAgenticRestoreSeparatesReusedTokenIDs` 通过；不同身份且同数字 ID、同 requestId 的回执相互隔离，session 保留两份历史，`identity-restored-green.log`。 |
| 回执写入失败 | `TestAgenticReceiptFailureRollsBackBeforePublication` 用实际 SQLite trigger 阻止 operation INSERT，配置/轮换/探测/告警/更新均返回错误，资源版本和缓存不变；移除故障后同键首次提交、随后重放。`receipt-failure-green.log` 与最终 race 通过。 |
| 回执稳定标识 | 真实数据库主键派生 `operation.id`，恢复前后稳定且不同身份可区分；首发/重放/列表一致，界面不再以可能重合的 requestId 作为 key。`receipt-id-green.log`、最终 store/race/前端全套通过。 |
| 审查后存量迁移与真实提交失败 | `TestAgenticMigrationKeepsLegacyReadOnly` 增加存量窗口哈希/期限/剩余额度与更新 JSON/owner_id 保全断言；`TestAgenticDeferredCommitFailure` 先实证 INSERT 成功而 Commit 报延迟外键错误，再经真实 Store 变更路径验证回滚、状态清理与同键恢复/重放。原生核心测试通过，`remaining-store-green.log`、`store-core-final.log`。 |
| 审查后字段掩码与 JSON 秘密 | `TestAgenticFieldMaskClearsAndRejects` 覆盖空 tags、清 billing、显式 grace=0、保留未选字段，以及四种非法字段在预览/执行两端被拒且无副作用；`TestAgenticRawJSONOneTimeSecrets` 覆盖创建/轮换/注册窗口首次 Any 的秘密字段、预览/重放/回执不含秘密。`remaining-api-green.log`、最终 Handler race 通过。 |

## 缺陷注入

注入只发生在 `build/agentic-mutations/` 独立副本，先回读确认补丁落地，再直接检查 Go 命令退出码；原工作树未承载注入代码。

| 注入的缺陷 | 测试看见的失败 |
| --- | --- |
| 跳过事务内授权复核 | `revoked grant committed: <nil>`。 |
| 跳过 queued 到 dispatched 的授权复核 | `revoked update dispatched`。 |
| 反转详细审计清理时间条件 | `details retained beyond retention`。 |
| 恢复时覆盖操作回执而非合并 | `restore erased existing receipts`。 |
| 预览不回滚 | 数据库备注改变、新节点被创建、旧凭据失效、探测缓存被发布，分别被断言捕获。 |
| 放宽操作/范围准入 | 全站、动态标签、跨范围探测预览和共享对象删除错误放行；Hub 更新未返回权限拒绝。 |
| 字段补全退回缓存 | `patch lost durable fields or selector`，旧 interval 覆盖已提交数据库值。 |
| UI 未实现权限表单 | 先前实施时准确失败于找不到权限勾选项及 grant 未提交；实现后通过。 |
| 删除重放前先检查节点范围 | `committed retry rejected after scope removal`，`mutation-delete-replay-red.log`。 |
| 回执身份退回数字 token ID | 恢复后读到另一身份同键回执，`identity-red.log`。 |
| JSON 字段掩码忽略 trafficResetDay | `JSON field mask lost fields`，`json-red.log`。 |
| 引用错误不隐藏越界规则 | 错误带出 hidden-rule-name，`reference-errors-red.log`；修复后 `reference-errors-green.log` 通过。 |
| 更新预览错误发布内存 | 启动和取消都在 `update preview published state` 处红，数据库不变但内存改变，`update-preview-cache-red.log`。 |
| 更新提交不发布内存 | 启动和取消都在 `update commit diverged` 处红，数据库已改变但内存未改变，`update-commit-cache-red.log`。 |
| 事务失败不清零 CommittedAt | 五类操作均在 `receipt failure reported success` 处红，`receipt-failure-red.log`；恢复后最终 race 绿。 |
| 回执标识忽略主体身份、API 遗漏标识、界面仍用 requestId | 分别在 `restored receipts lack distinct stable IDs`、`receipt ID changed on replay` 和 React duplicate key 处红；日志为 `receipt-id-red.log`、`receipt-id-api-red.log`、`ui-receipt-id-red.log`。 |
| 迁移污染存量窗口/更新归属与内容 | `legacy window changed`、`legacy update changed`，分别记录于 `remaining-store-red.log`、`migration-update-owner-red.log`。首次修改列默认值的尝试被旧 schema 一致性检查拦住（`migration-update-schema-red.log`），不算新数据断言的凭据；改成只污染存量数据后才准确验红。 |
| 真实 Commit 失败保留成功时间 | `failed commit reported applied change`，`remaining-store-red.log`；延迟约束使 SQLite 在提交点失败，而不是回执 INSERT 失败。 |
| 掩码忽略清空/零值或放行非法字段 | 分别在 `field mask clear lost semantics` 和每种路径的 `invalid mask accepted`、资源/回执变化处红，`mask-clear-red.log`、`mask-invalid-red.log`。 |
| JSON 首发丢弃秘密、重放缓存秘密 | 创建、轮换、注册窗口三种操作分别在 Any 首发缺失和 `JSON retry exposed secret` 处红，`json-secret-first-red.log`、`json-secret-replay-red.log`；只使用隔离测试生成的临时凭据。 |

最终通过 checksum dry-run 比对副本和原树的 Hub Go 生产文件，未发现文件内容差异（排除前端 build 产物）；注入用的 `store.go`、更新 `manager.go`、store/API `changes.go` 另用 `diff -q` 确认一致。测试 transport 的差异仅留在副本。

## 未通过与未覆盖

- 全量 `go test -count=1 ./...` 尚未通过。收紧环境前一次在 10 分钟上限超时，涉及 deploy 与 API；没有基线同命令证据，不能归为既有 flake。记录在 `go-all.log`。
- 收紧环境后以相同全量命令重跑失败于 `httptest: failed to listen ... bind: operation not permitted`，退出码 1，记录在 `go-final.log`。整包 store 也有依赖监听的既有集成测试，因此不能用核心测试通过冒充整包通过。
- 最终再次执行同一全量命令，退出码 1，`go-final-recheck.log`。本次 deploy 通过（266.360 秒），多个包仍失败于 TCP/UDP 绑定限制，Darwin 采集测试还受到 sysctl/平台信息读取权限限制；不能据此宣称全量通过或归为既有 flake。
- `go-final-recheck.log` 的全量重跑发生于最后 `operation.id` 增量之前。之后在全部补验测试落地的最终工作树再次运行同一 `go test -count=1 ./...`，原始退出码仍为 1，日志为 `go-final-current.log`；deploy 通过（230.942 秒），失败仍包括 TCP/UDP 绑定与 Darwin 平台读取权限。此次覆盖最终代码，但结果不是全绿。
- 扩大副本测试范围时，`TestDeletedAlertScopeRemainsListedAfterReload` 自建第二个 HTTP server，仍被监听限制阻断，`handler-race-expanded-bind-failure.log`。最终 race 的选择集合不包含这个测试，不把缩小集合的通过称作该命令已修复。
- 浏览器 CDP 连接需要用户授权，先前检查超时；未进行桌面/移动端真实浏览器验收。
- HTTP Handler 链路已注入回执 INSERT 失败；真实 SQLite Commit 失败已在 Store 入口用延迟约束验证。没有从 HTTP 入口直接制造 Commit I/O 故障，不把存储层覆盖描述成该场景的端到端验收。

## 正式审查

- `ce-code-review` receipt：`status: complete`，run ID `20260930-agentic-write-review`，产物目录 `/tmp/compound-engineering-501/ce-code-review/20260930-agentic-write-review`；`review.json` 另存于 `build/agentic-validation/review.json`。
- 12 个审查视角全部完成，当前 `findings` 和 `actionable_findings` 均为空。四条早期范围/告警问题由主代理修复，再由独立 validator 按最新代码与各自定向测试核销，不表示原问题不存在。
- `Operation.id` 最后增量由审查编排方单独核对 store/API/proto/UI 并跑定向测试，记录在 `incremental-coverage.json`；这部分不是原 12 个视角的独立覆盖。
- 最终 verdict 为 `Not ready`：功能实现与对应测试已交付，但全量及实际运行验收未完成。没有将监听/平台权限失败判成代码缺陷，也没有以定向通过替代全量通过。
- 未进行外部跨模型审查；使用本地 adversarial 视角。审查未修改生产代码、提交或部署。未解决的测试与部署风险已在本记录列明。
- 用户继续推进后，只在 store/API 的 `changes_test.go` 补充上述剩余可控场景，并做缺陷注入与回归；原始 `review.json` 保持不变，里面的旧 testing_gaps 以本记录的后续验证补充，不声称重新完成了另一轮正式审查。静态 native/Linux vet 在补验后均再次退出 0。
- 最后恢复副本后，迁移/真实提交失败测试再次退出 0（`remaining-store-restored-green.log`）；当前原生存储核心回归退出 0（3.171 秒），Handler 选择集合的 race 退出 0（41.620 秒）。全量仍失败，原审查 `Not ready` 的运行时验收边界没有因此解除。

## 实施进度

权限/范围、schema 25、类型化写入口、事务预览/并发检查/回执、注册隔离、更新派发复核、请求读范围、面板、CLI、协议生成物与入口文档均已落地。审查中已修复字段补全缓存滞后、并发同键回执竞争、历史任务目标泄露、受限请求污染全局告警扫描、引用错误名称泄露及告警列表/引用任务范围不一致。

告警可见性消费面已枚举并统一：API 列表经 `ListVisibleAlertRules`，写事务经 `Change.authorizeScope`，引用完整性错误经 `checkAlertReferences`，三处都调用存储层 `authorizeRule`。全局监控引擎仍读取完整配置，不用请求范围裁剪运行状态。

最后代码检查、缺陷注入与正式审查已完成；完整监听、桌面/移动端浏览器及部署验收不在已通过清单内。发布前应在有权限的隔离副本执行全量测试、实际 Hub 启动/HTTP 烟测和升级/恢复演练；schema 25 不能仅更换旧二进制降级。永久回执会持续增加，应监测数据库、备份体积和写延迟。
