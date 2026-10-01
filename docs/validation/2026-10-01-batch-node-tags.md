# 节点批量标签验证

## 代码与环境

- 基点：`65af4186dd62480d681a6ecdeb7b9673e124ad50`；分支：`feat/batch-node-tags`。
- 实现快照：`git diff HEAD -- gen internal proto web`，SHA256 `e1018c5a2a5c941365a1084c36b53486ea143075524a9665555ac4728af43148`，不包含本记录与架构说明。
- 本机 macOS，Node `v26.10.0`；浏览器为真实 Chrome，经本机 HTTPS 测试代理访问临时数据库，没有操作线上服务。
- 接口、存储、注册表、节点页及对应测试均在本次范围内；没有数据库迁移或部署。

## 自动化结果

以下命令直接读取退出码，均为 0：

| 工作目录 | 命令 | 实际观察 |
| --- | --- | --- |
| 仓库根 | `go test -count=1 ./internal/hub/...` | Hub 全部包通过，包含批量事务、鉴权、探测、告警和公开读取路径 |
| 仓库根 | `go test -race -count=1 ./internal/hub/api ./internal/hub/probe ./internal/hub/store` | 三包通过，无竞态报告 |
| 仓库根 | `go test -race -count=1 ./internal/hub/api -run TestBatchNodeTags` | 加入未选中第三节点和任务列表反向索引断言后，再次通过 |
| `web` | `pnpm exec vitest run` | 69 文件、767 项测试通过 |
| `web` | `pnpm run build` | TypeScript 及管理端、公开端产物均构建成功 |
| 仓库根 | `buf lint` | 协议检查通过 |
| 仓库根 | `go vet ./...` | 本机静态检查通过 |
| 仓库根 | `GOOS=linux go vet ./internal/hub/...` | Linux Hub 静态检查通过 |
| 仓库根 | `git diff --check` | 无空白错误 |

Vitest 输出一条 `Could not parse CSS stylesheet` 提示，仍执行并通过全部 767 项；Vite 对两个入口均提示产物超过 500 kB，没有构建失败。

## 验红凭据

每次先确认临时修改已落地，再运行对应测试；以下缺陷均触发退出码 1，随后恢复实现并回绿。

| 注入缺陷 | 断言实际失败处 |
| --- | --- |
| 把部分选中算成全部选中 | `toBePartiallyChecked` 收到 `aria-checked=true` |
| 标签解除 SQL 增加恒假条件 | 节点仍有 `DB`，而期望仅有 `kept/new/家宽` |
| 把标签容量上限放宽到 1000 | 17 个标签被接受，且整批节点状态发生变化 |
| 不发布新的节点任务映射 | 第二台 agent 没收到动态探测任务 |
| 丢弃事务返回的告警规则集合 | 应覆盖两台的规则只产生一台状态 |
| 注册表更新时删除任务的全部旧节点，而非只删本批目标 | `Registry.List` 返回 `[1,2]`，缺少未选中的第三台节点 |
| 保留“写入成功但回读失败”的旧草稿 | 新测试看到弹窗仍存在；改为成功写入即丢弃旧基线后通过 |

测试还覆盖空节点集合、非正 id、重复 id、大小写折叠、增删冲突、非法标签、节点不存在、标签容量和探测配额导致的整批回滚，以及移除超过 16 项标签的并集。

## 浏览器观察

以 `pnpm run build` 和 `go build -o bin/heron-hub ./cmd/hub` 的产物启动 `web/e2e/server.mjs`，确认真实 Hub 监听成功；最终浏览器加载的管理脚本为 `index-BakMoyqX.js`。

- 创建 5 台节点，其中 3 台有“家宽”，全部另有“保留”标签和“保留备注”。全选后弹窗显示“家宽”半选、`3/5 个节点`，DOM 的 `indeterminate=true`。
- 桌面切为全部添加并保存，弹窗关闭；经真实 `ListNodes` 回读，5 台均有“家宽”和“保留”，备注未变。
- 真实 375px 宽窗口中，页面 `scrollWidth=375`；标签行、输入框和保存按钮未发生横向溢出。
- 移动窗口切为全部移除并保存，弹窗关闭；回读 5 台都只剩“保留”标签且备注未变；`ListTags` 仍保留“家宽”，节点数为零。
- 截图保存在本机忽略目录 `build/batch-node-tags-evidence/desktop.png`、`mobile.png`，不随源码提交。

未运行 Firefox、WebKit、容器安装矩阵或部署验收；这些结果不由本记录推断。

## 审阅范围

独立本地审阅检查了正确性、对抗场景、测试、接口契约、安全、性能、项目规范、维护性、可靠性及前端竞态。发现的“已提交后继续编辑旧草稿”问题已经修复，并由新增用例先验红再回绿。最终没有保留的代码缺陷；尚未单独组合测试“弹窗打开时轮询变化、保存延迟、回读延迟”三种时序，不将分散用例视为该组合已验证。
