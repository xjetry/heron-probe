# Agent 统计口径与诊断实施记录

## 范围与基线

- 工作区 `/Users/xjetry/work/vibe/probe`，功能分支 `feat/agent-observability`，基线 `4839d22dc170d84494a5a80567ce02b4cfed436b`；开始时工作树干净。
- 交付：恢复正式旧版 Agent 兼容 CI；网卡集合变化时重建速率与流量基线；管理端只读、白名单化采集诊断。
- 不包含：远程配置修改、任意命令、凭据回传、计费或主题配置。
- `origin` 仍指向不存在的 `xjetry/probe.git`，`git fetch origin main` 返回 Repository not found；不修改远端配置，不推送现有历史。兼容产物从官方 `xjetry/heron-probe` 验证。

## 数据约束

- 网卡集合标识由实际计入合计的网卡名称规范排序后生成，与内核启动标识分开；计数与标识在同一条 Metrics 内到达。
- Agent 本地速率和 Hub 累计量都只对相同启动周期、相同集合的计数做差。集合切换的第一个样本只建基线，不伪造零速率；历史桶不接收该样本的跨集合增量。
- 标识随流量基线原子持久化；旧 Agent 的空标识仍可上报，切换到有标识或退回空标识时只重建一次基线。
- 诊断只含类型化白名单，采集失败仅为固定类别；不包含 token、Hub URL、命令行、任意配置或原始错误。诊断不进入公开协议及主题数据。
- Facts 摘要只随内容变化，不加入每次采样时间，避免稳定状态每次写库；页面明确显示最近保存的诊断而非实时健康保证。

## 验证范围

- 可控 Host 复现新增/移除网卡与过滤变化的假峰值，再验证枚举顺序和被排除网卡变化不重置。
- 从 Report HTTP 入口验证累计、管理/公开历史、重启恢复和旧协议边界。
- 诊断的上报、存储、恢复、管理读面与公开隔离，含有读数与失败/恢复两个方向。
- schema 升级与旧快照恢复；协议生成与兼容检查；Go/前端完整测试及目标平台静态检查。
- 新增断言缺陷注入，回读注入落地并记录真实退出码；撤回后相同命令重跑。
- 实际旧 Agent 通信 E2E、管理端桌面/移动浏览器检查。

## 验证证据

- 环境：Go 1.27.1、macOS arm64、Docker 29.4.0 / OrbStack aarch64；以下均为基线上未提交的实现，不代表远端 CI。
- 已观察实现前回归用例失败：新增网卡伪造速率；集合切换累计增加 9890/19780；诊断为空；非法集合摘要和诊断被入口接受。
- 实现后 `go test -count=1 ./internal/agent/collect ./internal/agent/client ./internal/hub/traffic` 通过；诊断入口与共享校验聚焦测试通过。
- `pnpm test` 首次执行退出 1：67 文件通过，`NodesScale.test.tsx` 的两个 100 节点交互用例超过 60 秒；758 项通过、2 项失败。不归因为既有 flake；停止重负载任务后同命令完整通过，结果见下。
- 存储、UI 与兼容下载替身的分项验证已完成。兼容下载的完整证据见 `docs/validation/2026-09-30-agent-compat.md`。
- 真实 HTTP 集成初跑与存储缺陷注入时间重叠，不作为最终凭据；后续同包完整验收与落盘注入恢复测试均重新覆盖。

### 主验收记录

- 代码快照：`build/agent-observability/reviewed-code.patch`，SHA-256 `2915ce37767dd07db2b880b5bfa4fbe97ef05e7de91834bcab5aefc3910b42ac`；由基线到工作树的代码差异生成，排除文档。
- 最终代码快照：`build/agent-observability/final-code.patch`，SHA-256 `461262dd2f530481108e16db76af5f7e5f1ce61b8eee0d02e2bf1c188a86eb4e`；包含缩窗断言修正、schema 夹具修正、Runner 启动准入测试及统计边界说明，排除 `docs/`。最终全量与 race 启动时的代码摘要为 `83cb9701602424e645bc3cb6147110609c1eeef189908be84de4af56f0fdf47e`，随后仅修正文案，无执行逻辑变化。
- `make lint script-test build` 退出 0：Go 模块、buf、ShellCheck、三个目标系统的 vet、发布/下载替身和本机及 Linux/Darwin 双架构编译通过。日志 `build/agent-observability/static.log`。
- `buf breaking --against '.git#ref=4839d22dc170d84494a5a80567ce02b4cfed436b'` 退出 0。重新 `buf generate` 退出 0，4 个变更生成物重新生成前后的 SHA-256 一致。
- `make hub-binary` 退出 0；真实 Hub 监听成功并接受浏览器登录、Agent JSON 上报和管理查询。
- `pnpm exec playwright test agent-diagnostics.spec.ts`：补齐 UpdateNode 既有必填字段后，Chromium/Firefox 通过，WebKit 超时，退出 1。该轮与 Go 全量测试并发，尚未判定原因。截图已保存到 `build/agent-observability/diagnostics-screenshots/`；人工查看 Chromium 桌面及 375px 长页，诊断卡片可读、没有横向溢出。
- 首轮 `make test` 退出 2：仅 `cmd/hub/TestOfflineCommandsRejectV8` 的 5 个子例失败，夹具已撤回新增列但初始版本断言仍为 25。同步为 26 后重新执行同一 `make test` 退出 0，包含安装脚本包（192.094 秒），日志 `go-test-retry.log`。全库同形搜索剩余 25 均为冻结历史夹具或历史文档，不替换其历史事实。
- 原命令 `pnpm test` 复跑退出 0，68 个文件、760 项全部通过，8.15 秒。日志 `web-test-retry.log`。不将首轮超时声称为既有 flake。
- 浏览器复跑暴露了立即缩窗测宽的时序缺陷。原生 DOM 测量得到 Chromium/WebKit `476 -> 375`（相隔两帧），Firefox `375 -> 375`；生产图表使用 ResizeObserver 重排。仅将新测试改成 `expect.poll` 等待 375，移除测量日志，不更改生产 CSS。实验轮三个引擎全部通过，日志 `browser-resize-experiment.log`。
- 独立浏览器测试副本 `build/agent-observability/browser-mutation.spec.ts` 临时漏掉 Report 的 diagnostics，`pnpm exec playwright test --config ../build/agent-observability/playwright-mutation.config.ts` 退出 1，失败位于实际接口清单缺失而非总超时；恢复载荷后同命令退出 0。日志 `browser-mutation-red.log` / `browser-mutation-restored.log`，没有修改共享生产实现。
- `pnpm exec playwright test` 完整执行退出 0：28 项通过、2 项既有非 Chromium Passkey 测试跳过。新增诊断在 Chromium、Firefox、WebKit 均通过，覆盖真实 JSON 上报、失败恢复、公开隔离与 375px 布局。日志 `web-e2e.log`。
- `make e2e compat-e2e` 退出 0：当前源码 Agent 与已发布 v0.3.5 分别覆盖 Debian/Alpine × amd64/arm64，共 8 格。日志 `agent-e2e.log` 中有 4 次 `E2E OK`（每次双架构）及 `published agent compatibility OK: v0.3.5`；两份旧版发布包的 SHA-256 与 pin 一致。
- 最后一次 `make lint` 退出 0，日志 `lint-final.log`。
- 增加 Runner 启动入口测试后，最终 `make test` 再次完整退出 0（安装脚本包 327.217 秒）；无 overlay 的真实入口测试包含在本轮。日志 `go-test-final.log`。
- `go test -race -count=1 ./internal/agent/client ./internal/agent/collect ./internal/agentwire ./internal/hub/traffic ./internal/hub/ingest ./internal/hub/store ./internal/hub/api` 退出 0，7 包全部通过，日志 `go-race-final.log`。
- `git diff --check` 退出 0；没有生成物或构建占位文件的意外删除。

### 缺陷注入记录

全部先回读确认临时代码存在，再执行测试，撤回后用同一命令复跑；日志在 `build/agent-observability/`。

| 临时缺陷 | 命令与实际观察 | 恢复后 |
|---|---|---|
| 取消 Agent/Hub 集合守卫；固定间隔为 10000；旁路共享诊断校验 | `go test -count=1 ./internal/agent/collect ./internal/agent/client ./internal/hub/traffic ./internal/agentwire ./internal/hub/ingest -run 'TestNetworkScope\|TestDiagnostics'` 退出 1。分别出现新增网卡伪速率、9890/19780 错增量、间隔不再对账、17 类非法诊断及入口准入断言失败。`inject-guards.log` | 同命令退出 0，`inject-guards-restored.log` |
| 不记录采集失败；仅对展示的 128 接口做摘要；流量落盘漏 epoch | `go test -count=1 ./internal/agent/collect ./internal/hub/traffic ./internal/hub/api -run 'TestCollectionDiagnostics\|TestDiagnosticsTruncation\|TestNetworkScopeBaseline\|TestAgentScopeAndDiagnostics'` 退出 1。失败类别消失、展示外接口改名不改变摘要、重载基线丢失；真实 HTTP 路径也检出落盘 epoch 为空。`inject-persist.log` | 同命令退出 0，`inject-persist-restored.log` |
| 枚举顺序不规范化；成功采集仍保留上一轮失败 | `go test -count=1 ./internal/agent/collect -run 'TestNetworkScope\|TestCollectionDiagnostics'` 退出 1。仅重排就丢掉 30/60 正常速率；磁盘恢复后仍显示失败。`inject-order-recovery.log` | 同命令退出 0，`inject-order-recovery-restored.log` |
| Runner 绕过启动参数校验 | `go test -overlay build/agent-observability/runner-overlay.json -count=1 ./internal/agent/client -run '^TestDiagnosticsInvalidFiltersStopBeforeReporting$'` 退出 1。隔离 overlay 移除 Validate 后，非法 include 与 exclude 均到达 Report，两个子例在首请求断言处失败。`runner-validation-red.log` | 恢复 overlay 后同命令退出 0，`runner-validation-restored.log` |

存储实现单元还验证了写/读/恢复三侧拒绝、原子 upsert、空对象与未知区分、未知 JSON 字段、实时与快照迁移、非 JSON 空白。原完整存储包退出 0（166.739 秒），最后空白收口后聚焦同命令退出 0，最终全量由主验收覆盖；证据在 `/tmp/heron-store-observability.n3lJd0/`。管理 UI 单元已对未知、失败、截断、单位、时间、轮询、刷新失败保留和禁止编辑分别验红后恢复，聚焦 43 项通过。

## 边界与发布

- 网卡集合变化时，舍弃相对旧基线的整个区间，而不只是切换之后的流量。例如 t0 建基线、t5 增加网卡、t10 上报，集合比较会重建基线，不能拆出 t0 至 t10 内原有网卡的有效增量。频繁增删计入网卡会持续少计；可用包含规则限定稳定上行接口，不保证无损计费。同名接口在两次采样之间替换仍不可识别。
- 诊断严格白名单是一项准入合同，未来字段或失败类别扩展需要同步 Hub 或引入明确版本协商；没有通过静默丢弃非法诊断来继续接收请求。页面时间表示最近保存，不是每轮采样时间，稳定状态不会刷新该时间。
- 本次真实兼容矩阵固定 v0.3.5，不声称覆盖全部历史 Agent 或远端 Actions。schema 26 已验证实时升级和快照恢复；部署前仍需一致性备份，不能单换旧 Hub 二进制降级。
- 简化阶段因代理容量限制由主代理按复用、质量、效率三视角内联检查，应用 `netFilters` 去重；不声称完成三路独立简化。
- 未推送、发布或部署；失效 origin 和分支上的既有本地历史未擅自处理。

## 独立审查

- `ce-code-review` 回执 `status: complete`，结论 `Ready to merge`，待修复发现为 0。运行标识 `20260930-observability.Yt6HAUbx`，完整工件位于 `/tmp/compound-engineering-501/ce-code-review/20260930-observability.Yt6HAUbx/review.json`。
- 覆盖正确性、项目规范、测试、可维护性、安全、协议合同、数据迁移、可靠性、前端时序，以及独立跨模型对抗检查。审查没有替代主验收执行，测试凭据以本文件的实际命令和日志为准。
- 跨模型关于频繁网卡变化少计、未来诊断扩展和 Facts 保存时间的候选，经当前实现与显式合同核对后没有保留为缺陷；相关限制已在上述边界说明中公开，不以静默放宽准入解决。
- 保留一条非阻断覆盖建议：增加非空集合标识下 Collector → Report → Book 的成功、网络读取失败、同集合或不同集合恢复序列。现有缺失计数账本测试、采集失败测试与 HTTP 集合切换测试分别覆盖相关分支，但没有把这几个状态组合为一条入口测试；这不是已证实的生产缺陷，本次未追加。
