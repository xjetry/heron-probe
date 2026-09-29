# 大规模节点交互验收

## 数据与环境

基线为 `f741ce15f2b8ec14b62a1126f030e11842a917a4`；工作目录为
`/Users/xjetry/work/vibe/probe/.worktrees/monitor-improvements`。以下自动化观察来自
2026-09-29 本机执行。自动化断言与真实浏览器布局分别记录；未测量帧率、延迟或渲染性能。

`scripts/ui-scale-fixture` 仅使用正式 HTTP API：登录、创建节点、更新可编辑字段、
读取完整列表、持续上报。拒绝非回环地址和已有节点的 hub，不直接改库，不启动 hub。
默认生成 100 个节点，覆盖长名称、长备注、标签、国家、计费信息，并按 hub 下发的间隔
持续变化 CPU、内存、网络累计计数器和实时网速。

先给一个专用空数据库设置密码并启动当前构建的 hub，然后在独立终端执行：

```sh
PROBE_FIXTURE_PASSWORD='本次独立数据库的密码' go run ./scripts/ui-scale-fixture -hub http://127.0.0.1:18089 -nodes 100
```

`fixture ready: nodes=100` 表示创建后已回读确认节点数；`reports accepted=N` 只累计
收到成功响应的上报。`Ctrl-C` 结束上报，数据留在该独立库供检查；再次运行要换新的空库。
端口无需固定，但必须与本次启动的 hub 一致。

## 自动化观察

命令均直接执行并检查原始退出码，不经管道。

```sh
pnpm exec vitest run src/api/useOrder.test.tsx src/pages/Nodes.test.tsx src/pages/NodesScale.test.tsx
```

在 `web/` 执行：退出码 0，3 个测试文件、79 个用例通过。观察包括 100 行长文本均完整
存在、节点 1 连续下移两次后客户端及服务器都是 `[2,3,1]`、后台列表刷新不覆盖顺序或
未提交备注、写成功但读取失败后所有排序入口禁用，恢复后展示权威顺序且保留失败提示。
这不是布局测试，也不是性能测量。

```sh
go test -count=1 ./scripts/ui-scale-fixture
```

在仓库根执行：退出码 0，包可编译，输出 `[no test files]`。这条命令不证明夹具已连到真实 hub。

共享排序逻辑的缺陷注入均先以 `rg` 确认修改落地，再运行有针对性的用例；每次失败均退出码 1：

| 注入缺陷 | 命令过滤参数 | 实际失败 |
| --- | --- | --- |
| 每次从查询结果而非期望排列计算移动 | `-t '连续'`，包含 hook 与节点规模测试 | 第二次移动仍为 `[2,1,3]`，两处入口均失败 |
| 查询数组变化即丢弃乐观顺序 | `-t '连续'` | 同集合旧列表把顺序变回 `[1,2,3]`，hook 断言失败 |
| 去掉 `enabled` 守卫 | `-t '过滤'` | 子集禁用期间仍发送了保存 |
| 忽略完整列表成员变化 | `-t '完整列表成员'` | 删除成员后仍发出第二个旧集合排列 |
| 卸载后仍认为会话有效 | `-t '卸载'` | 卸载后仍发出第二个保存 |
| 去掉写失败及恢复失败的阻断 | `-t '失败'` | 保存失败、回读失败两个分支均在恢复失败后错误发送第二写 |
| 每次点击都启动提交循环 | `-t '连续移动\|最终回读'` | 写入及回读尚在途时已经发出第二写，两用例失败 |

以上注入均已恢复。最后一组注入恢复后应由主流程的完整 `pnpm test` 与构建结果作为合并前凭据，
不把和注入窗口重叠的并行测试当成最终代码的验证。

关键文件校验和（SHA-256，自动化验收时的最终实现）：

```text
ece8f9295c881438cc04600437197a8c87f5a0f45cd34a0ab115d39a1a454045  web/src/api/useOrder.ts
9fc6cc3ef71778fe29023ca2fed168b5d591c0130eb53d79b15ea2415af0f132  web/src/api/useOrder.test.tsx
02d96d76ef77a0318b095bccc17ef7d81b7a9b2105f5e88c6f1532569051eec6  web/src/pages/Nodes.tsx
762d85df338bd4ac79cf8bbea5651d914df49919e1ffb82156563890116e6caa  web/src/pages/NodesScale.test.tsx
cdfc80d1e705a51bff11b65445ebc76f5285532c8608043338c8cbb3cbaab643  web/src/styles.css
1cca1a3e88e1faceae39f30283775724668fa65069926ecab5a3d280587ebd7a  scripts/ui-scale-fixture/main.go
```

## 真实浏览器观察

专用 hub 在 `127.0.0.1:18089` 使用 `bin/ui-scale.db`，通过上述正式夹具累计观察到 `reports accepted=9500`；总览实际显示 100/100 在线。夹具停止后节点按正常离线规则变化，没有修改产品时钟或在线判定。浏览器为 Chrome 154，窄屏使用隔离 headless 实例的 mobile viewport，不是实体手机。

- 桌面 1728px、1440px 与窄屏 390px 均展示 100 行；1440px 的 `innerWidth/scrollWidth=1440/1440`，390px 为 `390/390`，窄屏行的 computed display 为 `grid`。
- 查看列表和编辑截图，长名称、备注与标签在节点卡片内换行；编辑、保存、取消、换 token、删除均有入口。未在浏览器实际执行换 token 或删除，避免改变这份百节点夹具；相关行为仍由既有前端和 API 测试约束。
- 节点 1 连续下移两次后，页面与真实 `ListNodes` 都为 `[2,3,1]`，没有错误横幅。
- 计费选择实际包含无周期以及月、季、半年、年、两年、三年、五年；保存五年后 `ListNodes` 回读 `BILLING_CYCLE_QUINQUENNIAL`，页面显示 `USD 12.50 / 五年`。
- 管理端和公开端长名称详情初次观测到 `scrollWidth=469`。在共享标题样式增加最大宽度和任意位置换行后，两端重新打开均为 `innerWidth/scrollWidth=390/390`；各 7 张图，CPU、内存和网络峰值图例及采样峰值说明均存在。
- 浏览器在应用加载前代理 fetch，先确认注入已安装，再让 `ListNodes` 失败：实际记录 1 次排序写入、6 次失败读取，全部排序按钮禁用。撤去故障后点击“重新读取排序”，入口恢复且仍只有 1 次写入，没有以重试读取为由重复写。
- 真实任务页面从 `[1,2,3]` 连续上移第三项两次，显示为 `[3,1,2]`，无错误横幅；最终产物复测继续把末项移到首位，页面及 `ListProbeTasks` 均为 `[2,3,1]`。完整 HTTP 生命周期与版本不变另由 API 测试覆盖。
- 布局选择器改用稳定 `data-column`，`data-label` 仅用于显示标题。只读、编辑两态分别将六个标签改成 `Changed label`，computed grid 行列完全不变；将名称列标识改成不存在的值，宽列从 `1 / -1` 变成 `auto`，恢复标识后恢复原布局。这一对照确认检查确实能观察到布局选择器失效。

临时浏览器脚本此前把 fetch 代理装在应用启动后，未拦截到 transport 请求；另一次缺少 CDP `Page.enable`，新文档脚本也未执行。两次超时都不作为产品失败或成功证据。最终先检查注入标志，再以真实请求计数和恢复后的服务端列表判定。

列表草稿保护、过滤禁用及失败后阻断另由上面的自动化用例验证；没有把未执行的键盘操作或持续实时动画观察补写为浏览器已通过。

截图保存在忽略目录：`bin/nodes-1440.png`、`bin/nodes-390.png`、`bin/nodes-edit-390.png`、`bin/history-390.png`、`bin/public-history-390.png`。

最终浏览器命令 `env PROBE_FIXTURE_PASSWORD='<专用测试密码>' node bin/check-ui.mjs > bin/verification-browser-final.log 2>&1` 退出码 0。该次通过前先用新产物重启 hub，并观察到 `hub listening`；最终产物与关键源文件 SHA256：

```text
5ada7719f0a8465546e0e98a91ebae0619c8fa4719c9ede7f1808a889ce8112e  bin/probe-hub
e33183a478a2a4008699e2a30913b693af2d90f82e567f55adaa3bb3851ccc6a  web/src/api/useOrder.ts
c18e6566d7af20a0603515b67852403b4b09f0289aca98f5bdd53d87d904b984  web/src/pages/Nodes.tsx
09ab1ce55aead61d577e8cb1fe72f01b43a4f1ddf5244235909a388649102322  web/src/styles.css
```

这组摘要区别于前面独立自动化验收时的源文件摘要；后续变化包括 Set 成员比较、移除重复复制、长标题换行与稳定布局列标识。最终 `pnpm test` 返回 0（59 文件、631 用例），`make hub-binary` 返回 0。
