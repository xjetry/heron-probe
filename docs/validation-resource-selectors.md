# 资源告警与节点选择器验证记录

日期：2026-09-29。基点：`bffd1485fe2a0ea34da0b2900ff2b014b17de2d3`，验证对象为该基点上的未提交工作树；认证、备份等模块由其他协作者同时修改。本文件记录本模块实际观察，不宣称全仓验收通过。

## 实现口径

- 内存、磁盘使用率先由同一次上报的 used/total 计算，再按分钟求均值。缺任一项或 total=0 不产生比例读数；追加到指标描述表，原有指标下标保持不变。
- 资源告警触发阈值为 (0,100]，恢复阈值为 [0,触发阈值)，持续窗口 1-60 分钟。触发需要每个完整分钟均达到阈值，恢复需要同长度完整窗口每分钟均不高于恢复阈值；缺失分钟不能恢复。
- 全部节点、显式节点、非空标签交集互斥。读取时动态覆盖展开为 node_ids，保存动态条件时不携带展开节点。删除被任务或规则引用的标签失败，并列出引用。
- 任务和告警覆盖共用 store 的 coverageSQL；标签变更在同一事务检查每节点任务上限、推进任务版本、裁剪失效状态，并回读任务覆盖与规则快照。registry 只发布事务返回值，alert 发布不再额外访问数据库，也不重置启动时钟。
- 标签批量选择只保存当前选中的固定节点；动态选择器明确展示未来变化会影响覆盖，空选择不能提交。历史 API 自动包含新增比例指标；本次未增加额外历史图组。

## 已执行验证

工作目录均为 `/Users/xjetry/work/vibe/probe`，前端命令工作目录为其 `web` 子目录。Go 测试均使用 `-count=1`，判定使用执行工具返回的原始退出码。

1. `go test -count=1 ./internal/hub/metric ./internal/hub/probe ./internal/hub/alert`：修改后的三个包全量通过，退出码 0；该次运行时本地 HTTP 测试仍可监听。
2. `go test -count=1 ./internal/hub/api -run 'TestDynamicSelectorAPI|TestResourceRuleAPI|TestAlertRuleCRUD|TestAlertKindsMapEveryValue|TestDeletedAlertScope|TestListProbeTasksExpands|TestSaveAlertRuleExpiryKind|TestSaveAlertRuleRejectsFields'`：退出码 0，观察到 agent 上报进入分钟库后经历 pending、firing、firing、ok；标签移除后 agent 得到空任务清单，告警状态清除，新匹配节点没有重新开始离线计时。
3. `npm test -- src/pages/AlertRulesResource.test.tsx src/pages/ProbeTasksSelector.test.tsx src/pages/AlertRules.test.tsx src/pages/ProbeTasks.test.tsx src/lib/alerts.test.ts`：5 文件、80 条测试通过，退出码 0。提交请求断言区分静态 node_ids 和动态 selector_tags，资源规则发送恢复阈值且不夹带探测字段。
4. `npm run typecheck`：退出码 0。
5. 受限沙箱下切换 `GOCACHE=/private/tmp/probe-resource-selectors.10Ydh8/go-cache` 后，`go test -count=1 ./internal/hub/store -run TestNodeUpdateScopeReadFailureRollsBackAllWrites`：退出码 0。通过破坏规则回读依赖的列验证节点名称、标签行、任务版本均回滚。
6. 同一 GOCACHE 下 `go test -count=1 ./internal/hub/metric ./internal/hub/probe ./internal/hub/alert -run 'TestResource|TestDynamicSelector|TestSelectorModes|TestDeletedScope'`：三个包退出码 0。
7. 同一 GOCACHE 下 `go test -count=1 ./internal/hub/api -run '^$'`：退出码 0，只证明编译，不作为运行时通过。
8. `git diff --check`：退出码 0。

## 缺陷注入

每次先 apply_patch 注入，再用源码回读确认注入存在，然后运行所列测试；全部已恢复。下列失败均为断言失败且退出码 1，不是编译失败。

| 注入缺陷 | 测试与实际失败 |
| --- | --- |
| 比例直接返回 used | `TestResourceRatiosUsePairedReadings`：内存均值 50，预期 70 |
| 恢复只看最后一分钟 | `TestResourceContinuousWindowsAndMissingReadings`：两个指标均提前变为 ok，预期 firing |
| 恢复忽略 Present | 同上：缺失分钟被当作 0 后提前恢复 |
| 全部节点跳过模式互斥 | `TestSelectorModesRejectAmbiguityAndEmptyTag`：接受 all=true 与 nodes=[1,2] |
| 跳过任务数检查 | `TestDynamicSelectorLimitRollsBackNodeAndVersion`：超限修改没有返回错误 |
| registry 不移除旧覆盖 | `TestDynamicSelectorUpdatesAllReadersAndRejectsReferencedTags`：列表残留 [1,2]，预期 [2] |
| alert 不发布事务返回的规则覆盖 | `TestDynamicSelectorAPIRefreshesAgentAndAlertScopes`：标签移除后旧节点仍处于 firing |
| 资源评估跳过启用的规则 | `TestResourceRuleAPIFromAgentSampleToRecovery`：首分钟状态为空，预期 pending |
| 规则快照回读错误被吞掉 | `TestNodeUpdateScopeReadFailureRollsBackAllWrites`：损坏的规则读取被接受 |
| 前端不发送动态标签 | `ProbeTasksSelector.test.tsx`：请求 selectorTags=[]，预期 [west] |
| 前端恢复阈值强制写 0 | `AlertRulesResource.test.tsx`：请求 recoveryThreshold=0，预期 77 |

任务数检查首次注入因未使用 import 而编译失败，该次不算验红；随后保留有效引用重新注入，观察到上表对应的断言失败。

## 最终重跑限制

后续权限环境变为受限沙箱。直接 Go 命令无法写默认缓存，报 `Library/Caches/go-build/... operation not permitted`；切换任务私有缓存后 metric/probe 全量通过，但 alert/api/store 全量遇到 `httptest: failed to listen ... bind: operation not permitted` 并退出 1。日志位于 `/private/tmp/probe-resource-selectors.10Ydh8/packages.log` 与 `store.log`。这些运行未完成，不记为通过；没有为绕过监听限制修改产品代码或测试。

未执行本模块的真实浏览器桌面/移动端检查。前端证据限于组件测试、TypeScript 检查和代码布局检查。主代理仍须在允许监听的环境完成全仓运行时验收。

## 内容标识

归档时主要生产文件 SHA-256：

```text
6928615d9bc63b14e7c919430e100389b93bea25343f87886638b6584bc28c1e  internal/hub/store/selector.go
cf875a621af24dfcdf1aec875c58b339866bfaf414561a8f165e567a9211118d  internal/hub/store/node.go
a4b644169af91ed03ec8ef495e51ad0c353be3dfc59796f5e7be2c8fb4599b4f  internal/hub/alert/resource.go
b789ff4d2a47177aacd122810eb884df51e4f3065d7c8660a5da14400e0b310a  internal/hub/alert/engine.go
4798070c5bdfd87bf2698b42f291a184e157910a0780e849d1e1c6e134240d9d  internal/hub/probe/registry.go
335f2d6c909f487fa27dd2b0c1a750fa98a7c4cae7a48e20d3e7d6a37bbd992b  internal/hub/metric/metric.go
ee74af5b9c0dcc2727f60dafd8bf427f8efa56eb8b0ae5ce07eaa0248001e74c  web/src/components/NodeSelector.tsx
b5710030594f1432d701001bfb635c246867c5427db2c25d904edc25a4906e44  web/src/pages/AlertRules.tsx
cb402e2a69d9f9b4b1aae096ec763f48fb7d0581c5e018ba391586ba69cdf78d  web/src/pages/ProbeTasks.tsx
```
