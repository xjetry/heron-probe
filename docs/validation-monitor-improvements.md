# Monitor 改进验证记录

工作区：`/Users/xjetry/work/vibe/probe/.worktrees/monitor-improvements`。
分支：`feat/monitor-improvements`，基点：`f741ce15f2b8ec14b62a1126f030e11842a917a4`。
环境：2026-09-29，macOS、Go、pnpm、OrbStack、Chrome 154；所有命令在此 worktree 执行，未修改主工作区受控文件。代码尚未提交，以下凭据对应该基点上的开发工作区，不代表远端 CI 或生产部署。

## 已观察的结果

- 新增网络峰值测试后、生产实现前，`go test -count=1 ./internal/hub/metric ./internal/hub/store -run TestNetworkPeaks` 退出 1：分别报缺少 `net_rx_bps` 和网络速率列，确认断言能抓住旧实现。
- 加入速率 MeanMax 列及 schema 21 迁移后，`go test -count=1 ./internal/hub/metric ./internal/hub/store -run 'TestNetworkPeaks|TestMigrationFromV20|TestFrozenSchemas'` 退出 0：峰值、流量总和、缺失读数和升级保持旧数据检查通过。
- 新增五年测试后、生产实现前，`go test -count=1 ./internal/hub/alert ./internal/hub/api -run TestFiveYear` 退出 1：续期返回空值，HTTP API 拒绝枚举 7，均为预期失败。
- `buf generate` 退出 0，已生成 Go 和 TypeScript 协议代码。
- `pnpm install --offline --frozen-lockfile` 在 `web` 退出 0。

## 最终验证

各命令直接判定原始退出码。日志放在被 Git 忽略的 `bin/`，不将生成物或临时测试库纳入提交。

| 命令 | 退出码 | 观察 |
| --- | --- | --- |
| `make test > bin/verification-go.log 2>&1` | 0 | 全部 Go 包通过，包括 `cmd/hub`、API、迁移/恢复和独立 deploy 测试；deploy 实际完成耗时 109.208 秒。 |
| `make lint` | 0 | 依赖整洁检查、buf lint、兼容脚本 ShellCheck、Go vet 与目标平台检查通过。 |
| `make build > bin/verification-build.log 2>&1` | 0 | 本机及 Linux/Darwin 的 amd64/arm64 编译通过。 |
| `pnpm test > ../bin/verification-web-final.log 2>&1`，在 `web/` | 0 | 稳定列标识修复后重新执行全量：59 文件、631 测试通过。 |
| `make hub-binary > bin/verification-hub-final.log 2>&1` | 0 | 管理端、公开端 Vite 构建及嵌入前端的 hub 编译通过。 |
| `buf breaking --against '.git#ref=f741ce15f2b8ec14b62a1126f030e11842a917a4'` | 0 | 协议兼容检查通过。 |
| `make script-test` | 0 | 发布、镜像回读、固定旧版本下载检查通过。 |
| `git diff --check` | 0 | 无空白错误。 |

最终再次执行 `buf generate`，前后对 17 份 Go/TypeScript 生成文件分别计算 SHA256，两份清单均有 17 行，`cmp` 返回 0；生成物与当前 proto 一致。命令输出 Node 的 localStorage 实验性 warning，不影响这次生成结果。

生成物核对时主仓库 `git status --short --branch` 只有 main 分支摘要、没有修改项；main 和开发 worktree 的 HEAD 都为上述基点。最终收尾时主工作区已由另一项工作切到 `feat/heron-brand` 并出现 README 与前端品牌文件改动，本任务未触碰或回退它们。开发改动仅在本 worktree，尚未暂存、提交、合并或推送。

Vite 仍提示 chunk 超过 500 kB，jsdom 输出 `Could not parse CSS stylesheet`；这些命令通过不等于没有 warning。

完整 Go 测试首次失败于旧 schema 快照测试夹具：只撤回旧列却保留新增 v21 列，并固定断言新库版本为 20。已让夹具同时撤回配置排序列和三层速率列，更新新库版本断言，再用同一条 `make test` 全量命令得到上述通过结果。没有为使夹具通过而放宽产品迁移规则。

前端曾在缺陷注入尚未恢复时与全量运行重叠，产生失败；该次不作为最终凭据。恢复并结束所有注入后，重新执行原命令，最终结果如表所示。

## 主流程缺陷注入

以下均先用 `rg` 确认改动落地，再执行对应断言；原始退出码均为 1，错误来自目标缺陷，而非编译或环境。每项验证后立即还原，最终工作区不含注入代码。

| 注入 | 实际失败 |
| --- | --- |
| 反转历史任务排序 rank | 历史顺序为 `[4 2 1 3]`，而非 `[3 1 2 4]`。 |
| 展示重排推进任务版本 | 端到端断言发现 agent 的任务清单/版本改变。 |
| 新任务取最小序号减一 | 新任务错误插在开头而非追加末尾。 |
| 删除完整排列中的重复 ID 检查 | 非法排列 `[3,3,2]` 被接受。 |
| API 将网络 max 改用 mean | 峰值从 800 变成 306.666…，历史断言失败。 |
| 关闭恢复路径中的 v21 指标迁移 | 旧快照恢复后无法读取新增速率列。 |
| 五年周期从 60 月改为 36 月 | 续期从 2029 年变为 2027 年，告警层与 HTTP API 双端断言失败。 |

恢复后执行 `go test -count=1 ./internal/hub/api ./internal/hub/store ./internal/hub/metric ./internal/hub/alert -run 'TestProbeDisplayOrder|TestNetworkPeaks|TestRestoreV20|TestFiveYear'` 返回 0。后续完整 `make test` 同样返回 0。

`TestProbeDisplayOrderAcrossAPIStorageAndAgent` 从真实 HTTP 入口覆盖保存、编辑、新建、重排、registry 重载、删除后的历史顺序、非法完整排列和 agent 清单/版本不变。网络测试同时约束流量总和、均值、采样峰值、旧数据缺峰值，以及管理端和公开端读取的一致性。五年周期覆盖枚举写入、公开快照、闰日 2024-02-29 到 2029-02-28 和跨多个周期续期。

## 独立验证记录

- 历史峰值前端：`validation-history-peaks.md`。
- 跨版本 CI：`validation-compat-ci.md`。
- 百节点交互与真实浏览器：`validation-scale-ui.md`。

固定旧 agent 与当前 agent 各自通过 Debian/Alpine、amd64/arm64，共 8 个组合。旧基线是已发布预发布版 `v0.1.0-rc.1`，不是稳定正式版；GitHub Actions 没有实际触发。E2E 使用的冻结 hub 摘要见独立记录；后来重建的浏览器验收产物只包含前端布局与说明文字等更新，不将两个二进制摘要混为同一份产物。

## 代码审查

`ce-code-review` 回执状态为 `complete`，结论 `Ready to merge`，无剩余 finding、风险或测试缺口；run ID 为 `20260929-monitor-NMUzn4UW`，回执文件为 `/tmp/compound-engineering-501/ce-code-review/20260929-monitor-NMUzn4UW/review.json`。审查范围包括已跟踪 diff 及新增实现/测试。

审查发现的文案与布局选择器耦合已改为稳定列标识，并在只读与编辑态实际验证；先前缺失的窄屏浏览器证据已补齐。Claude 审查在工具策略拒绝后达到 max_turns，未产出有效结果；本地 adversarial 替代复核完成，但没有取得跨模型独立佐证。同一线程串行的审查视角也不表述为相互独立的佐证。

## 升级与运行回读

数据库从 schema 20 升到 21，新增探测展示排序和三层网络速率聚合列；历史流量总量列保持原语义。升级前保留一致性备份，不能将旧二进制直接对已升级库运行作为回滚。若升级出现启动/迁移失败，停止写入并使用备份及对应旧版本恢复；不因新增峰值缺席就判为迁移损坏，旧数据本来没有采样速率。

未来部署的维护者应在首个上报周期、首个分钟落盘后回读节点在线、管理/公开历史、重排后清单及五年计费信息，观察迁移错误、上报拒绝和持续读写失败日志。本次没有部署到生产，也没有指定生产监控负责人；这些是交付验收路径，不声称已在生产观察。

浏览器验收完成后已停止本次专用 hub、独立 headless Chrome，并关闭自己创建的两个普通浏览器标签页；保留用户原有标签页及 CDP proxy。临时数据库和截图留在 worktree 的忽略目录 `bin/`。
