# 历史峰值 UI 验证记录

## 代码与环境

- 日期：2026-09-29。
- 工作树：`/Users/xjetry/work/vibe/probe/.worktrees/monitor-improvements`，分支 `feat/monitor-improvements`。
- 基线：`f741ce15f2b8ec14b62a1126f030e11842a917a4`；以下结果针对该基线上未提交的历史 UI 改动。
- 环境：Darwin arm64、Node v26.9.0、Vitest v5.0.1，jsdom 与内存 Connect 服务；测试不依赖出网或真实采集时钟。
- 本单元只修改 `web/src/lib/series.ts`、`web/src/lib/series.test.ts`、`web/src/components/History.tsx`、`web/src/components/History.test.tsx` 和本记录。

最终文件 SHA-256：

```text
fcbb2300c396b5b6bfa13e073607545fd5cf577dd79ee09610add7bdfe572fe4  web/src/lib/series.ts
3ad9d02bf3bfa2baa584981a43dc5392084e79dd12b1b98ece391fc2699495dd  web/src/lib/series.test.ts
c56e9232a3595c69522a48d87fade3661f26e59404bad37d1e114064c9ad33d2  web/src/components/History.tsx
6f91d4e204202e9f9cd95654b44a71c0d619cfa8104d06b426dac27d1d24cfc1  web/src/components/History.test.tsx
```

## 行为与覆盖范围

`toAligned` 的所有消费点改用 `SeriesSelection`，显式指定指标名、`mean` / `sum-rate` / `max` 和图例。没有按网络字段名分支，也没有保留旧签名兼容层。

CPU 显示均值和峰值；内存显示内存均值、内存峰值与交换均值；网络显示累计字节增量除以桶宽所得的上下行均值，以及 `net_rx_bps` / `net_tx_bps` 的上下行实测峰值。缺桶、缺系列、`n=0`、缺所选统计量均为空洞；有样本的零值仍为零。文案明确最大值来自已采集样本，不能解释为采样间隔内的瞬时最高值。

既有覆盖已检查：`series.test.ts`、`Chart.test.tsx`、`NodeDetail.test.tsx`、`NodePage.test.tsx`。新增 History 测试从管理和公开两个真实路由入口经过内存服务、查询 hook 和共用 History，检查交给 Chart 的图例、单位和数据。绘图组件在该测试中替换为数据呈现器，不宣称验证了真实 Canvas 绘制或浏览器布局。

## 命令与实际观察

下列命令的工作目录均为工作树下的 `web`，直接检查工具返回的退出码，未通过管道截断命令结果。

| 命令或代码状态 | 退出码 | 观察 |
| --- | --- | --- |
| 仅先加入测试，生产代码未改；`pnpm test src/lib/series.test.ts src/components/History.test.tsx` | 1 | 13 项中 12 项失败；转换仍不接受选择描述，两个页面仍只有 `cpu` 旧图例 |
| 实现选择描述与面板后，同一命令 | 0 | 13 项通过 |
| 增加管理/公开均不含网络峰值系列的旧历史对照，同一命令 | 0 | 15 项通过；平均数据不消失，两个峰值列保持全空 |
| `pnpm test src/lib/series.test.ts src/components/History.test.tsx src/components/Chart.test.tsx src/public/NodePage.test.tsx` | 0 | 最终 4 个文件、22 项通过；包含所有缺陷注入恢复后的代码 |
| `pnpm exec tsc --noEmit --project tsconfig.app.json --incremental false` 首次 | 2 | 当时另一并行单元的 `src/api/useOrder.test.tsx:11` 报未使用的 `ids`；历史 UI 文件无诊断，未修改该单元 |
| 同一类型检查命令重跑 | 0 | 未输出诊断；前一次真实诊断已验证该入口会报告错误 |

## 缺陷注入

每次均通过 `apply_patch` 注入，在运行前以 `sed` 读回确认实际改动；运行同一条 `pnpm test src/lib/series.test.ts src/components/History.test.tsx`，直接取得退出码 1，再恢复。每项失败原因都来自被注入的行为，不是语法或环境错误。

| 注入行为 | 实际失败 |
| --- | --- |
| `max` 错读 `mean` | 峰值 92 变为 25，缺峰值点错变 50；均值/峰值独立列和两个入口断言失败 |
| `mean` 错读 `max` | 均值 25 变为 92，只有均值的点丢失；字段选择断言失败 |
| `sum-rate` 漏除桶宽 | 网络均值 2/3 错变 120/180；自定义指标 3 错变 180 |
| 移除 `n=0` 守卫 | 无样本点错误画出 12/99；三种选择均失败 |
| 缺失 `mean` / `max` 补零 | 预期空洞点变成零；字段缺失断言及页面断言失败 |
| 以 `|| null` 处理统计量 | 有效零均值、零峰值被丢弃；零值断言失败 |
| 缺失 `sum` 补零 | `sum-rate` 缺失点错误变为零；对应断言失败 |
| 缺桶或缺系列补零 | 旧历史的两条峰值线全零；四个页面对照与缺桶断言失败 |
| 网络面板错选 `mean` 而非 `max` | 实测峰值 11/17 错变 4/9，缺峰值点错变 6；两个有网络峰值系列的页面用例失败，两个旧历史用例仍通过 |
| 中文图例退回原始指标名 | 三个面板图例均发生预期差异，四个页面用例失败 |
| 忽略速率图单位覆盖 | 网络单位从 `bytes/s` 错变为 `bytes`，四个页面用例失败 |
| 说明文案将留空改为补零 | 四个页面均找不到约定的采样峰值说明 |

## 交接边界

- 未运行完整前端测试、前端构建、Go 测试或真实浏览器。统一全量、后端持久化/上卷与桌面/手机验收由主执行单元负责。
- `NodeDetail.test.tsx` 的旧英文图例断言属于另一文件所有者；已明确交接为 `下行均值,上行均值,下行峰值,上行峰值` 和 `内存均值,内存峰值,交换均值`。本单元的 History 测试已从同一个管理页面入口验证新值。
- 没有暂存、提交、构建产物、安装或修改生成文件。
