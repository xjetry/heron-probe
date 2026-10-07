# Web 改版：管理外壳、总览与节点 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地设计规范 §8 的第三、第四个里程碑：管理面板外壳与总览（抽屉与 ⋯ 菜单随首个消费者在这里落地），以及节点列表、编辑抽屉与节点详情。

**Architecture:** 管理端接入 `web/src/styles.css` 的共享 token，`admin.css` 只留布局与管理端专有的部件样式，不再有第二套调色板与字体。两个新基础件只在管理端用：`Drawer`（右侧 480px，原生 `<dialog>`，与 `Modal` 共用同一个焦点与背景 inert 的 hook）与 `RowMenu`（⋯ 行操作菜单，危险项在菜单内两段式确认）。管理端的节点状态必须把快照（在线、最近上报）与节点资料（维护中）合在一起判定，判定原语仍是 `lib/status.ts` 的 `nodeStatus`，合并逻辑只在 `lib/adminStatus.ts` 一处。「需要处理」四个数与节点列表的 URL 筛选都是纯函数（`lib/attention.ts`、`lib/nodeFilters.ts`），页面只负责渲染与路由。节点详情与公开节点页同结构，共用 `NowGrid`、`MetricCharts`、`ProbeTaskCharts`；低频内容进页内 tab。

**Tech Stack:** React 19 + react-router 8 + connect-query 2 + uPlot 1.6；vitest 5 + Testing Library（jsdom）；Playwright 1.63 e2e（`make web-e2e` 跑真实 hub）。

**Spec:** `docs/superpowers/specs/2026-10-07-web-redesign-design.md`（§2 设计系统、§4.1 外壳、§4.2 总览、§4.3 节点、§4.7 手机版、§6 数据边界）。前两个里程碑已由 `2026-10-07-web-redesign-public.md` 落地（共享 token、`StatusBadge`、`Bar`、`InfoTip`、`MultiSelect`、`ThemeToggle`、`Chart`、`MetricCharts` / `ProbeTaskCharts`、`lib/status.ts`、`lib/scheme.ts`）。

**不在本计划里：** 探测任务、告警（规则、事件页、静默、渠道）、系统页（在线更新、安全、API token、注册窗口、存储、外观、主题）与登录页——见 `2026-10-08-web-redesign-admin-probes-system.md`。本计划里凡是链到那些页面的筛选（「触发中告警」卡链到 `/alerts?state=firing`）在那份计划落地前只是落到未筛选的页面，链接本身不坏。

## Global Constraints

- 颜色与字体（§2）：全部取 `styles.css :root` 的 token（`--bg/--card/--card-raised/--card-hover/--fg/--muted/--faint/--line/--line-strong/--accent/--status-*/--font-ui/--font-mono/--radius-control/--radius-card/--control-h/--shadow-float`）。管理端不得再在 `admin.css` 里定义 `--accent`、`--bg` 等同名变量或另一套 `font-family`；`adminStyles.test.ts`（Task 1）钉住这条。管理端正文 13px。
- 密度（§2）：侧栏 232px、顶栏 48px、表头 32px、行 36px（两行内容 52px）、控件 32px、抽屉 480px；控件圆角 4px、卡片 6px；全圆角只给状态点与胶囊；只有弹层与抽屉带阴影。
- 状态一律「色点 + 文字」，四态唯一判定是 `nodeStatus`（§6）。管理端快照 `NodeStatus` 没有 `maintenance`，节点资料 `Node` 才有：合并只在 `lib/adminStatus.ts` 的 `liveStatus`，任何页面不得自己拼 `online ? "在线" : "离线"`。快照里没有这个节点（快照未到、失败）时状态是「未知」，不是离线，也不计入「需要处理」。
- 不新增接口、不改协议口径（§1 非目标）：四个「需要处理」数只能从 `ListNodes`、`GetSnapshot`、`ListAlertRules.states` 已有字段算出；节点列表的状态 / 到期 / 落后筛选在前端做，标签筛选继续走 `ListNodesRequest.tags / untagged`。
- 抽屉与弹窗都是原生 `<dialog>`（`showModal`）：背景 inert、焦点约束、Escape 关闭、关闭后焦点回到触发元素。e2e 与单测按 `getByRole("dialog")` 找它们，这个契约不能变。
- 行操作收进 ⋯：菜单项的可访问名沿用旧按钮的写法 `动词 节点名（#id）`（`withId`），危险项在菜单内两段式：首击只武装成「确认删除 …」，再击才执行；菜单关闭即撤销武装。
- 列表页统一为 页头（`h1` + 主按钮）→ 筛选行（`.filter-row[role=group][aria-label=筛选]`）→ 表格；手机（≤900px）表格降级为卡片，用 `td[data-label]` 的既有写法。
- 动态集合（§2）：标签筛选是可搜索多选下拉（`MultiSelect`），已选以可移除胶囊显示。
- 轮询不覆盖草稿：抽屉持有打开时的快照，列表每 10s / 2s 的刷新不重置表单；保存期间抽屉不可关闭。这是既有不变式（`Nodes.test.tsx`「节点刷新失败保留编辑行与草稿」等），改成抽屉后同一组测试必须仍然成立。
- `web/src/public/importScan.test.ts` 钉住公开包不得带入 `admin_pb.ts`：本计划新建的 `Drawer`、`RowMenu`、`QuickSearch`、`lib/adminStatus.ts`、`lib/attention.ts`、`lib/nodeFilters.ts` 都 import 管理服务类型，公开页不得引用它们；抽出来给两端共用的 `NowGrid` 不得 import 任何生成代码。
- `web/src/test/setup.ts` 关掉了 vitest 的 `globals`，每个测试文件自己 import `expect/it/vi`；管理端测试用 `renderWithAdmin(impl, routes, path)`（`web/src/test/harness.tsx`），返回 `{ router, queryClient }`。
- 代码注释与 commit message 不写过程信息（任务编号、方案代号、轮次），只写 WHY 与不变式；注释里的因果论断要能推演证伪。
- 每条新断言做一次缺陷注入（构造它本该抓住的缺陷，确认红在正确的原因上）。判成败的命令不接管道：`cmd > log 2>&1; echo $?`。
- Bash 每条命令用 `cd /Users/xjetry/work/vibe/probe/web && …`（zsh 不分词、`cd` 失败会让 `&&` 短路，先看退出码）。vitest 单测文件：`pnpm vitest run src/path/file.test.tsx`。

## Review Focus

- 快照还没到或刷新失败时打开节点列表：状态列是「状态未知」而不是「离线」，「需要处理」的离线数不把这些节点算进去，落后标记沿用上次取得的绑定版本——Task 6、Task 7、Task 9。
- 行菜单打开着的时候列表轮询刷新（10s）：菜单不关闭、焦点不丢、武装中的「确认删除」保持武装；节点在刷新后消失时菜单随行一起卸载——Task 3、Task 9。
- URL 带非法筛选值（`/nodes?status=foo`、`?expiring=yes`）：忽略那一项，列表不为空，不显示一个删不掉的胶囊——Task 8。
- 节点总数为 0：四张「需要处理」卡都是 0 且仍可点，实时表位置给出去处；卡数为 0 时文案不变成空——Task 7。
- 节点详情从「Agent 诊断」tab 切回「概览」：不重发 24h 历史查询（react-query 缓存仍在），切窗按钮状态保留；`?tab=diagnostics` 深链直接落在诊断 tab——Task 12。

---

## 文件地图

共享（两端都会用）：

| 文件 | 职责 |
|---|---|
| `web/src/styles.css` | 追加 `.filter-row*`（从 public.css 搬来）、`.drawer*`、`.row-menu*`、`.page-header`、`.attention*`、`.chip-tag`、`.badge-attention`、`.now-grid*`（从 public.css 搬来）、`.tabs*` |
| `web/src/components/NowGrid.tsx` | 节点页首屏六个现值格（从 `public/NodePage.tsx` 抽出，公开页与管理详情共用） |
| `web/src/components/History.tsx` | `MetricCharts` 的覆盖率说明进 ⓘ；删掉不再有消费者的 `HistoryCharts` / `ProbePanels` |
| `web/src/lib/scheme.ts` | 加 `ADMIN_SCHEME_KEY`（值仍是 `heron-admin-scheme`，老用户的选择不丢） |

管理端：

| 文件 | 职责 |
|---|---|
| `web/src/admin.css` | 只剩布局（外壳、侧栏、顶栏、表格列宽、手机降级）与管理端专有部件；删掉自有调色板、字体、按钮 / 输入 / 卡片的尺寸覆盖 |
| `web/src/components/dialog.ts` | `useNativeDialog(opener)`：`showModal`、`data-autofocus`、关闭回焦（从 `Modal` 抽出） |
| `web/src/components/Modal.tsx` | `variant: "modal" \| "drawer"`；`Drawer` 是 `variant="drawer"` 的别名导出 |
| `web/src/components/RowMenu.tsx` | ⋯ 菜单：`role=menu` / `menuitem`、方向键、Escape、点外关闭、两段式危险项 |
| `web/src/components/QuickSearch.tsx` | 顶栏节点搜索（⌘K / Ctrl+K） |
| `web/src/components/Layout.tsx` | 外壳：侧栏 232、顶栏 48、面包屑、QuickSearch、「公开页 ↗」、ThemeToggle、登出、窄屏导航抽屉 |
| `web/src/components/PageHeader.tsx` | `h1` + 主按钮 + 可选说明 |
| `web/src/lib/adminStatus.ts` | `liveStatus(node, live)`：快照 + 维护中 → 四态或未知 |
| `web/src/lib/attention.ts` | 「需要处理」四个数与各自的去向 |
| `web/src/lib/nodeFilters.ts` | 节点列表筛选：URL 参数解析 / 回写、应用筛选 |
| `web/src/pages/Overview.tsx` | 需要处理四卡 + 实时节点表（手机卡片） |
| `web/src/pages/Nodes.tsx` | 筛选行、表格、批量条、⋯ 菜单、抽屉编排 |
| `web/src/pages/NodeEditor.tsx` | 编辑抽屉（基本 / 地区 / 费用 / 运行） |
| `web/src/components/NodeCreateDrawer.tsx` | 添加节点：填名称 → 同一抽屉里给安装命令 |
| `web/src/components/NodeCredentialsDrawer.tsx` | 换 token 后的凭据与安装命令（替代 `NodeInstallModal`） |
| `web/src/pages/BatchNodeTagsEditor.tsx` | 批量编辑标签改为抽屉 |
| `web/src/components/EventFeed.tsx` | 告警事件列表（从 `pages/AlertEvents.tsx` 抽出，节点详情 tab 复用） |
| `web/src/pages/NodeDetail.tsx` | 状态头、编辑入口、tab：概览 / 流量校正 / Agent 诊断 / 告警事件 |
| `web/src/components/NodeOrderControl.tsx` | 只剩排序四项；「移动到…」搬进行的 ⋯ 菜单 |
| `web/src/components/NodeAddresses.tsx` | 导出 `addressText`（详情状态头的一行元信息复用同一套状态文案） |

## 消费面清单

按判定原语与可访问名 grep 到的全部读者，派发前已列全；执行时只改期望不改语义，再发现的同类断言照此处理并在 result 里列出位置。

| 旧契约 | 读者 | 新契约 | 归属 |
|---|---|---|---|
| `combobox` 「后台配色」 | `components/Layout.test.tsx:17`；`e2e/admin-ui.spec.ts:65,133,154` | `button` 「明暗切换」循环 跟随系统 → 浅色 → 深色 | Task 4、Task 13 |
| `button` 「打开导航」+ `dialog` 「导航」 | `Layout.test.tsx:27`；`e2e/admin-ui.spec.ts:147` | 不变 | — |
| `styles.test.tsx:48-51` 正控制：面板导航链接「总览」必须命中 styles.css 里某条 nowrap 规则（`.panel-nav a`） | `web/src/styles.test.tsx` | 侧栏 `nav` 保留 `panel-nav` 类 | Task 4 |
| `button` 「编辑 X（#id）」 | `pages/Nodes.test.tsx`（多处）；`e2e/admin-ui.spec.ts:84,128` | 先开 `button` 「更多操作 X（#id）」，再点 `menuitem` 「编辑 X（#id）」 | Task 9、Task 13 |
| `button` 「计费 X（#id）」 | `Nodes.test.tsx:65,325`；`e2e/admin-ui.spec.ts:100,144` | 删除；费用在编辑抽屉的「费用」分组 | Task 10、Task 13 |
| `button` 「换 token X」「删除 X」→「确认删除 X」 | `Nodes.test.tsx`（删除、换 token 各组） | `menuitem` 同名；确认项也是 `menuitem` | Task 9 |
| `combobox` 「移动 X」的 `move` 选项（「移动到…」） | `Nodes.test.tsx:1382,1429,1443`；`components/NodeOrderControl.test.tsx`；`e2e/admin-ui.spec.ts:275` | `menuitem` 「移动到… X（#id）」；`combobox` 只剩上移 / 下移 / 置顶 / 置底（`selectOption('last')` 不变） | Task 9、Task 13 |
| `checkbox` 「按标签过滤 X」、`button` 「清除标签过滤」 | `Nodes.test.tsx:949-1245`（标签 describe） | `MultiSelect` 「标签」内 `checkbox` 名即标签名，已选胶囊 `移除 X`；`button` 「清除筛选」 | Task 9 |
| 名称格「仅管理端」「维护中」文字、状态列「离线」（从未上报）、「暂无读数」、无标签时「—」 | `Nodes.test.tsx`（grep 见 Task 9 Step 1） | 「公开」胶囊只在公开节点；维护中在状态列 `StatusBadge`；从未上报显示「从未上报」；`Missing`（名「无读数」）；无标签时不渲染标签列表 | Task 9 |
| `styles.test.tsx` / `importScan.test.ts` 正控制 | 全局 | 不变；Task 12 的 `NowGrid` 不 import 生成代码 | Task 12 |
| 总览四张 `definition` 指标卡 `["4","30%","1.0 KiB/s","2.0 KiB/s"]`、`role=img` 「在线 / 离线」色点、文字「从未」、`meter` 名「42%」 | `pages/Overview.test.tsx` 全文 | 「需要处理」四张链接卡；`status-dot[role=img]` 名取 `STATUS_LABEL`；`meter` 名「CPU 42%」 | Task 7 |
| `region` 「Agent 运行诊断」直接可见 | `e2e/agent-diagnostics.spec.ts:27`；`pages/NodeDetail.test.tsx:436-460` | 在「Agent 诊断」tab 内（先点 tab 或 `?tab=diagnostics`） | Task 12、Task 13 |
| 节点详情 `link` 「告警事件」→ `/events?node=7` | `NodeDetail.test.tsx:142` | 改为 tab「告警事件」，内容由 `EventFeed` 渲染 | Task 11、Task 12 |
| 节点详情两张探测面板「探测 · RTT 均值 / 丢包率」 | `NodeDetail.test.tsx:147,340,354,400` | 每任务一张 RTT 图（标题链到 `/probes/:id/compare`） | Task 12 |
| 「上报覆盖率 …」段落可见 | `components/History.test.tsx`（COVERAGE_TEXT 用例） | 文案进 ⓘ 的 `role=tooltip` 正文（DOM 里仍在，`getByText` 仍找得到） | Task 12 |
| 公开 `NodePage.tsx` 内联的六格 | `public/NodePage.test.tsx`（`group` 名 CPU / 内存 …） | 换成共享 `NowGrid`，DOM 不变 | Task 12 |
| `NodeInstallModal` | `Nodes.test.tsx:889-922`（轮换后 token 与安装命令） | `NodeCredentialsDrawer`，内容与可访问名不变 | Task 10 |
| `pages/AlertEvents.test.tsx` 全文 | 事件页 | DOM 不变（只是列表搬进 `EventFeed`） | Task 11 |

---

## 里程碑 A：管理外壳与总览

### Task 1: 管理端接入共享 token

**Files:**
- Modify: `web/src/admin.css`（删自有调色板、字体与尺寸覆盖）
- Modify: `web/src/styles.css`（追加 `.page-header`、`.filter-row*` 从 `public.css` 搬入）
- Modify: `web/src/public/public.css`（删掉 `.filter-row*` 段，公开页改用共享的）
- Create: `web/src/adminStyles.test.ts`

**Interfaces:**
- Consumes: `styles.css :root` 的 token（Task 1 of 公开页计划）。
- Produces: 管理端元素继承 `:root` 的 `--accent/--bg/--card/--font-ui`；`.page-header`（`display:flex; align-items:center; justify-content:space-between; gap:12px; margin:0 0 16px`，`h1` 20px / 28px）；`.filter-row` 两端共用。

- [ ] **Step 1: 写测试（红）**

`web/src/adminStyles.test.ts`：

```ts
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { expect, it } from "vitest";

const admin = readFileSync(join(import.meta.dirname, "admin.css"), "utf8");
const shared = readFileSync(join(import.meta.dirname, "styles.css"), "utf8");
const publicCss = readFileSync(join(import.meta.dirname, "public/public.css"), "utf8");

// 设计 token 只有 styles.css :root 一处来源（设计 §2）；admin.css 若重定义同名变量，管理端会悄悄用上第二套配色。
it("admin.css 不重定义 styles.css 的 token，也不另设字体", () => {
  const tokens = [...shared.matchAll(/^\s+(--[a-z-]+):/gm)].map((m) => m[1]);
  expect(tokens.length).toBeGreaterThan(10);
  for (const token of tokens) expect(admin, token).not.toMatch(new RegExp(`${token}\\s*:`));
  expect(admin).not.toMatch(/font-family\s*:/);
});

// 筛选行是两端共用的部件：只在 styles.css 定义，public.css 不再有自己的一份。
it("筛选行样式只在 styles.css", () => {
  expect(shared).toMatch(/^\.filter-row \{/m);
  expect(publicCss).not.toMatch(/\.filter-row/);
});
```

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/adminStyles.test.ts > /tmp/a1-red.log 2>&1; echo $?`
Expected: 1；第一条红在 `--bg` / `--accent` / `font-family` 命中，第二条红在 `styles.css` 没有 `.filter-row`。

- [ ] **Step 3: 改 admin.css**

把文件开头的 `.admin-shell { … }` 块（含全部 `--*` 变量、`background: radial-gradient(…)`、`font-family`、`font-size: 14px`、`line-height`）替换为：

```css
/* 只由管理端入口加载。颜色、字体、圆角、控件高度都来自 styles.css :root 的 token（设计 §2）；这里只有布局与管理端专有部件。 */
.admin-shell { min-height: 100dvh; font-size: 13px; line-height: 1.5; -webkit-font-smoothing: antialiased; }
```

然后删除下列覆盖（它们把共享的 32px 控件、4px 圆角、18px 进度条改成了另一套）：
- `.admin-shell :is(button, input, …):focus-visible { … }`、`.admin-shell button { transition … }`、`.admin-shell button:not(:disabled):hover { … }`、`.admin-shell a:hover { … }`
- `.admin-shell input:not([type="checkbox"]):not([type="radio"]), .admin-shell select, .admin-shell textarea { padding … min-height: 40px; … border-radius: 8px; … }`
- `.admin-shell label { gap … font-size: .9rem; … }`、`.admin-shell button { min-height: 36px; padding … border-radius: 8px; }`、`.admin-shell button.link { … }`
- `.admin-shell .icon-button { … }`、`.admin-shell .icon-button:hover { … }`（styles.css 已有）
- `.admin-shell .bar { … 22px … }`、`.admin-shell .bar .fill { … }`、`.admin-shell .bar span { … }`
- `.admin-shell h1 { … 26px … }`、`.admin-shell h2 { 17px }`、`.admin-shell h3 { 13px }`
- `.admin-shell .card, .admin-shell fieldset:not(.bare):not(.picks) { … border-radius: 12px; padding: 22px; … }` → 改为 `{ padding: 16px; margin: 0 0 16px; }`（边框、背景、圆角由 styles.css 的 `.card` 给；fieldset 加 `border: 1px solid var(--line); border-radius: var(--radius-card); background: var(--card);`）
- `.admin-modal { … border-radius: 16px; … }` 的 `border-radius` 改为 `var(--radius-card)`；`.modal-header h2` 21px → 16px；`.modal-header`、`.modal-body`、`.modal-footer` 的 padding 改为 `16px 20px`
- `.eyebrow`、`.sidebar-footer`、`.admin-avatar`、`.workspace-label`、`.live-caption`、`.status-pill*`、`.stats-grid`、`.metric-card*`（总览与节点页重写后不再用；Task 4 / Task 7 删除对应 JSX，这里先删规则会让它们在 Task 4 前短暂没样式——因此这些规则留到各自的 Task 删）
- 表格：`.admin-shell table.nodes th { … padding: 13px 16px; }` → `{ height: 32px; padding: 0 12px; background: var(--card); color: var(--muted); font-weight: 500; font-size: 12px; }`；`table.nodes td { padding: 18px 16px; }` → `{ height: 36px; padding: 6px 12px; vertical-align: middle; }`
- 保留：`.admin-shell .skip-link*`、侧栏 / 顶栏 / 工作区布局（Task 4 再改尺寸）、`.table-scroll`、`table.node-management*`、`.node-order-control*`、拖拽高亮、`.form-section*`、`.form-grid*`、`.switch-field*`、`.billing-edit`、`.date-*`、`.tags-*`、`.address-*`、`.install-command*`、`.copyable-*`、`.navigation-drawer*`、媒体查询。

在 `styles.css` 末尾追加：

```css
/* 列表页页头（components/PageHeader.tsx）：标题 + 主按钮，两端共用。 */
.page-header { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 12px; margin: 0 0 16px; }
.page-header h1 { margin: 0; font-size: 20px; line-height: 28px; font-weight: 600; }
.page-header p { margin: 4px 0 0; color: var(--muted); font-size: 12px; }
.page-header-actions { display: flex; align-items: center; gap: 8px; }
.primary-button { background: var(--accent); color: var(--on-accent); border-color: var(--accent); font-weight: 500; }
```

把 `public.css` 里「筛选行（FilterRow.tsx）」整段（`.filter-row` … `.filter-row label.inline`）剪切到 `styles.css` 末尾，注释改为「筛选行（public/FilterRow.tsx 与管理端列表页共用）」。

- [ ] **Step 4: 跑测试确认绿，再跑全量**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/adminStyles.test.ts src/styles.test.tsx src/styles.test.ts > /tmp/a1-green.log 2>&1; echo $?`
Expected: 0。`styles.test.tsx` 的 nowrap 枚举多了 `.filter-row` 里没有 nowrap 的规则，不受影响。

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run > /tmp/a1-all.log 2>&1; echo $?` 与 `pnpm build > /tmp/a1-build.log 2>&1; echo $?`
Expected: 都是 0（纯样式变更不改 DOM）。

- [ ] **Step 5: 缺陷注入**

在 `admin.css` 临时加回 `.admin-shell { --accent: #176957; }`，跑 `adminStyles.test.ts`，必须红在 `--accent`；恢复后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/admin.css web/src/styles.css web/src/public/public.css web/src/adminStyles.test.ts && git commit -m "style(web): 管理端接入共享设计 token，去掉第二套调色板与字体"
```

### Task 2: 原生 dialog 的 hook 与 Drawer

**Files:**
- Create: `web/src/components/dialog.ts`
- Modify: `web/src/components/Modal.tsx`（用 hook；加 `variant`；导出 `Drawer`）
- Create: `web/src/components/Modal.test.tsx`
- Modify: `web/src/admin.css`（`.drawer*`）

**Interfaces:**
- Produces:
  ```ts
  export function useNativeDialog(opener: HTMLElement): RefObject<HTMLDialogElement | null>;  // 挂载 showModal + 聚焦 [data-autofocus]；卸载 close + opener 回焦
  export function Modal(props: { title: string; description?: string; busy?: boolean; onClose: () => void; children: ReactNode; className?: string; opener: HTMLElement; variant?: "modal" | "drawer" }): JSX.Element;
  export function Drawer(props: Omit<Parameters<typeof Modal>[0], "variant">): JSX.Element;   // = <Modal variant="drawer" …/>
  ```
  DOM：`dialog.admin-modal`（modal）或 `dialog.drawer`（drawer），`aria-labelledby` 指向 `h2`；关闭按钮名「关闭弹窗」/「关闭抽屉」；children 自带 `.modal-body` / `.modal-footer`（既有写法不变，抽屉里同名类由 `.drawer .modal-footer` 定位到底部）。

- [ ] **Step 1: 写测试（红）**

`web/src/components/Modal.test.tsx`：

```tsx
import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { Drawer, Modal } from "./Modal";

function opener() {
  const button = document.createElement("button");
  document.body.appendChild(button);
  button.focus();
  return button;
}

it("抽屉是原生 dialog：有标题名，Escape 关闭，关闭后焦点回到触发元素", () => {
  const trigger = opener();
  const onClose = vi.fn();
  const { unmount } = render(<Drawer title="编辑节点" opener={trigger} onClose={onClose}><div className="modal-body"><input data-autofocus aria-label="名称" /></div></Drawer>);
  const dialog = screen.getByRole("dialog", { name: "编辑节点" });
  expect(dialog).toHaveClass("drawer");
  expect(dialog).toHaveAttribute("open");
  expect(screen.getByLabelText("名称")).toHaveFocus();
  fireEvent.cancel(dialog);
  expect(onClose).toHaveBeenCalledTimes(1);
  expect(screen.getByRole("button", { name: "关闭抽屉" })).toBeInTheDocument();
  unmount();
  expect(trigger).toHaveFocus();
});

it("busy 时 Escape 与关闭按钮都不关", () => {
  const onClose = vi.fn();
  render(<Drawer title="保存中" busy opener={opener()} onClose={onClose}><p /></Drawer>);
  fireEvent.cancel(screen.getByRole("dialog"));
  expect(screen.getByRole("button", { name: "关闭抽屉" })).toBeDisabled();
  expect(onClose).not.toHaveBeenCalled();
});

it("Modal 默认仍是居中弹窗，关闭按钮名不变", () => {
  render(<Modal title="移动节点" opener={opener()} onClose={() => {}}><p /></Modal>);
  expect(screen.getByRole("dialog", { name: "移动节点" })).toHaveClass("admin-modal");
  expect(screen.getByRole("button", { name: "关闭弹窗" })).toBeInTheDocument();
});
```

（`web/src/test/setup.ts` 已给 jsdom 的 `HTMLDialogElement` 补了 `showModal/close`，`open` 属性可断言。）

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/components/Modal.test.tsx > /tmp/a2-red.log 2>&1; echo $?`
Expected: 1；红在 `Drawer` 不存在。

- [ ] **Step 3: 实现**

`web/src/components/dialog.ts`：

```ts
import { useEffect, useRef } from "react";

// 鼠标激活按钮不保证它获得焦点；调用方显式传入触发器，原生 dialog 负责背景 inert 与焦点约束。
// 弹窗与抽屉共用这一个 hook：两者只差布局，焦点与回焦的规则必须一样，否则 e2e 里的 Tab 循环与 Escape 回焦会分叉。
export function useNativeDialog(opener: HTMLElement) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const dialog = ref.current!;
    dialog.showModal();
    dialog.querySelector<HTMLElement>("[data-autofocus]")?.focus();
    return () => {
      dialog.close();
      if (opener.isConnected) opener.focus({ preventScroll: true });
    };
  }, [opener]);
  return ref;
}
```

`web/src/components/Modal.tsx` 改为：

```tsx
import { type ReactNode, useId } from "react";
import { useNativeDialog } from "./dialog";
import { Icon } from "./Icon";

type Variant = "modal" | "drawer";
const CLOSE_LABEL: Record<Variant, string> = { modal: "关闭弹窗", drawer: "关闭抽屉" };

export function Modal({ title, description, busy = false, onClose, children, className = "", opener, variant = "modal" }: {
  title: string; description?: string; busy?: boolean; onClose: () => void; children: ReactNode; className?: string; opener: HTMLElement; variant?: Variant;
}) {
  const ref = useNativeDialog(opener);
  const id = useId();
  return <dialog ref={ref} className={`${variant === "drawer" ? "drawer" : "admin-modal"} ${className}`} aria-labelledby={`${id}-title`} aria-describedby={description ? `${id}-description` : undefined}
    onCancel={(event) => { event.preventDefault(); if (!busy) onClose(); }}>
    <header className="modal-header">
      <div><h2 id={`${id}-title`}>{title}</h2>{description && <p id={`${id}-description`}>{description}</p>}</div>
      <button type="button" className="icon-button" aria-label={CLOSE_LABEL[variant]} disabled={busy} onClick={onClose}><Icon name="close" /></button>
    </header>
    {children}
  </dialog>;
}

// 新建与编辑一律在右侧抽屉（设计 §4.1）；确认类的小对话仍用居中弹窗。
export function Drawer(props: Omit<Parameters<typeof Modal>[0], "variant">) {
  return <Modal {...props} variant="drawer" />;
}
```

`admin.css` 追加：

```css
/* 抽屉：固定右侧 480px，整高；与 .admin-modal 同一个 dialog 原语，只差布局（设计 §2「抽屉固定右侧 480px」）。 */
.drawer { position: fixed; inset: 0 0 0 auto; margin: 0; width: min(480px, 100vw); height: 100dvh; max-height: 100dvh; display: flex; flex-direction: column; padding: 0; border: 0; border-left: 1px solid var(--line); border-radius: 0; color: var(--fg); background: var(--card); box-shadow: var(--shadow-float); }
.drawer::backdrop { background: #080b1094; }
.drawer > form { display: contents; }
.drawer .modal-body { flex: 1; overflow: auto; }
.drawer .modal-footer { position: static; }
@media (max-width: 560px) { .drawer { width: 100vw; border-left: 0; } }
```

- [ ] **Step 4: 跑测试确认绿，再跑全量**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/components/Modal.test.tsx src/pages/Nodes.test.tsx src/components/Layout.test.tsx > /tmp/a2-green.log 2>&1; echo $?`
Expected: 0（现有 Modal 消费者行为不变）。

- [ ] **Step 5: 缺陷注入**

把 `useNativeDialog` 清理函数里的 `opener.focus(...)` 删掉：第一条用例红在「`trigger` toHaveFocus」；恢复后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/components/dialog.ts web/src/components/Modal.tsx web/src/components/Modal.test.tsx web/src/admin.css && git commit -m "feat(web): 右侧抽屉与居中弹窗共用同一个原生 dialog 原语"
```

### Task 3: ⋯ 行操作菜单 RowMenu

**Files:**
- Create: `web/src/components/RowMenu.tsx`、`web/src/components/RowMenu.test.tsx`
- Modify: `web/src/components/Icon.tsx`（加 `more`：三个点）
- Modify: `web/src/styles.css`（`.row-menu*`）

**Interfaces:**
- Produces:
  ```ts
  export type RowMenuItem = {
    label: string;                            // 可见文字，如「编辑」
    onSelect?: (trigger: HTMLElement) => void; // 选中后回调；trigger 是 ⋯ 按钮，给抽屉 / 弹窗当 opener
    to?: string;                              // 有 to 时渲染为链接（react-router Link），不传 onSelect
    danger?: boolean;
    disabled?: boolean;
    confirm?: string;                         // 有值时两段式：首击把本项换成 confirm 文字并多出「取消」，再击才 onSelect
  };
  export function RowMenu({ label, items }: { label: string; items: readonly RowMenuItem[] }): JSX.Element;
  ```
  DOM：`button.icon-button.row-menu-trigger[aria-label="更多操作 {label}"][aria-haspopup=menu][aria-expanded]`；打开后 `div.row-menu-popup[role=menu][aria-label="{label} 的操作"]`，每项 `[role=menuitem][aria-label="{item.label} {label}"]`（链接项是 `a`，其余是 `button`），武装后的危险项名为 `"{confirm}"`（调用方传完整句，如「确认删除 web-01（#1）」），并多一项 `menuitem` 「取消」。键盘：ArrowDown / ArrowUp 在项间移动（循环），Home / End，Escape 关闭并回焦触发器；点外（pointerdown 不在根内）关闭；关闭即撤销武装。

- [ ] **Step 1: 写测试（红）**

`web/src/components/RowMenu.test.tsx`：

```tsx
import { fireEvent, render, screen, within } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { expect, it, vi } from "vitest";
import { RowMenu } from "./RowMenu";

function mount(items: Parameters<typeof RowMenu>[0]["items"]) {
  const router = createMemoryRouter([{ path: "/", element: <RowMenu label="web-01（#1）" items={items} /> }, { path: "/nodes/1", element: <p>详情页</p> }], { initialEntries: ["/"] });
  render(<RouterProvider router={router} />);
  return router;
}

it("触发器与菜单的可访问名沿用「动词 节点名（#id）」，链接项导航，普通项回调并关闭", () => {
  const onEdit = vi.fn();
  const router = mount([{ label: "编辑", onSelect: onEdit }, { label: "查看详情", to: "/nodes/1" }]);
  const trigger = screen.getByRole("button", { name: "更多操作 web-01（#1）" });
  expect(trigger).toHaveAttribute("aria-haspopup", "menu");
  expect(screen.queryByRole("menu")).toBeNull();
  fireEvent.click(trigger);
  const menu = screen.getByRole("menu", { name: "web-01（#1） 的操作" });
  expect(within(menu).getAllByRole("menuitem").map((el) => el.getAttribute("aria-label"))).toEqual(["编辑 web-01（#1）", "查看详情 web-01（#1）"]);
  fireEvent.click(within(menu).getByRole("menuitem", { name: "编辑 web-01（#1）" }));
  expect(onEdit).toHaveBeenCalledWith(trigger);
  expect(screen.queryByRole("menu")).toBeNull();
  fireEvent.click(trigger);
  fireEvent.click(screen.getByRole("menuitem", { name: "查看详情 web-01（#1）" }));
  expect(router.state.location.pathname).toBe("/nodes/1");
});

it("危险项两段式：首击只武装，再击才执行；Escape 关闭并撤销武装、焦点回到触发器", () => {
  const onDelete = vi.fn();
  mount([{ label: "删除", danger: true, confirm: "确认删除 web-01（#1）", onSelect: onDelete }]);
  const trigger = screen.getByRole("button", { name: "更多操作 web-01（#1）" });
  fireEvent.click(trigger);
  fireEvent.click(screen.getByRole("menuitem", { name: "删除 web-01（#1）" }));
  expect(onDelete).not.toHaveBeenCalled();
  expect(screen.getByRole("menuitem", { name: "确认删除 web-01（#1）" })).toBeInTheDocument();
  expect(screen.getByRole("menuitem", { name: "取消" })).toBeInTheDocument();
  fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
  expect(screen.queryByRole("menu")).toBeNull();
  expect(trigger).toHaveFocus();
  fireEvent.click(trigger);
  expect(screen.getByRole("menuitem", { name: "删除 web-01（#1）" })).toBeInTheDocument();
  fireEvent.click(screen.getByRole("menuitem", { name: "删除 web-01（#1）" }));
  fireEvent.click(screen.getByRole("menuitem", { name: "确认删除 web-01（#1）" }));
  expect(onDelete).toHaveBeenCalledTimes(1);
  expect(screen.queryByRole("menu")).toBeNull();
});

it("方向键在项间循环移动焦点，禁用项仍可到达但不可选", () => {
  const onRotate = vi.fn();
  mount([{ label: "编辑", onSelect: () => {} }, { label: "换 token", onSelect: onRotate, disabled: true }, { label: "删除", danger: true, confirm: "确认删除 web-01（#1）", onSelect: () => {} }]);
  fireEvent.click(screen.getByRole("button", { name: "更多操作 web-01（#1）" }));
  const menu = screen.getByRole("menu");
  const items = within(menu).getAllByRole("menuitem");
  expect(items[0]).toHaveFocus();
  fireEvent.keyDown(menu, { key: "ArrowDown" });
  expect(items[1]).toHaveFocus();
  fireEvent.click(items[1]);
  expect(onRotate).not.toHaveBeenCalled();
  fireEvent.keyDown(menu, { key: "ArrowUp" });
  fireEvent.keyDown(menu, { key: "ArrowUp" });
  expect(items[2]).toHaveFocus();
  fireEvent.keyDown(menu, { key: "End" });
  expect(items[2]).toHaveFocus();
  fireEvent.keyDown(menu, { key: "Home" });
  expect(items[0]).toHaveFocus();
});

it("点菜单外关闭", () => {
  mount([{ label: "编辑", onSelect: () => {} }]);
  fireEvent.click(screen.getByRole("button", { name: "更多操作 web-01（#1）" }));
  fireEvent.pointerDown(document.body);
  expect(screen.queryByRole("menu")).toBeNull();
});
```

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/components/RowMenu.test.tsx > /tmp/a3-red.log 2>&1; echo $?`
Expected: 1；红在模块不存在。

- [ ] **Step 3: 实现**

`Icon.tsx` 的 `paths` 加一项：`more: "M5 12h.01M12 12h.01M19 12h.01"`（与现有 path 同一种 24×24 描边写法；读一下文件里 `paths` 的形状照抄格式）。

`web/src/components/RowMenu.tsx`：

```tsx
import { type KeyboardEvent, useEffect, useRef, useState } from "react";
import { Link } from "react-router";
import { Icon } from "./Icon";

export type RowMenuItem = {
  label: string;
  onSelect?: (trigger: HTMLElement) => void;
  to?: string;
  danger?: boolean;
  disabled?: boolean;
  confirm?: string;
};

// 行操作收进 ⋯（设计 §4.1）。危险项的两段式确认与 ConfirmDelete 同一规则：首击只武装，再击才执行；
// 武装只活在这次打开的菜单里——菜单关闭（Escape、点外、选了别的项、所在行卸载）即撤销，不跨越别的操作继续有效。
export function RowMenu({ label, items }: { label: string; items: readonly RowMenuItem[] }) {
  const [open, setOpen] = useState(false);
  const [armed, setArmed] = useState<number | null>(null);
  const root = useRef<HTMLSpanElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const close = (refocus: boolean) => {
    setOpen(false);
    setArmed(null);
    if (refocus) trigger.current?.focus();
  };
  useEffect(() => {
    if (!open) return;
    menu.current?.querySelector<HTMLElement>("[role=menuitem]")?.focus();
    const away = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node)) close(false);
    };
    document.addEventListener("pointerdown", away);
    return () => document.removeEventListener("pointerdown", away);
  }, [open]);
  const onKeyDown = (event: KeyboardEvent) => {
    const focusable = Array.from(menu.current?.querySelectorAll<HTMLElement>("[role=menuitem]") ?? []);
    const index = focusable.indexOf(document.activeElement as HTMLElement);
    const go = (i: number) => focusable[(i + focusable.length) % focusable.length]?.focus();
    if (event.key === "Escape") { event.preventDefault(); close(true); }
    else if (event.key === "ArrowDown") { event.preventDefault(); go(index + 1); }
    else if (event.key === "ArrowUp") { event.preventDefault(); go(index - 1); }
    else if (event.key === "Home") { event.preventDefault(); go(0); }
    else if (event.key === "End") { event.preventDefault(); go(focusable.length - 1); }
  };
  const select = (item: RowMenuItem, index: number) => {
    if (item.disabled) return;
    if (item.confirm && armed !== index) { setArmed(index); return; }
    const button = trigger.current!;
    close(false);
    item.onSelect?.(button);
  };
  return (
    <span ref={root} className="row-menu">
      <button ref={trigger} type="button" className="icon-button row-menu-trigger" aria-label={`更多操作 ${label}`} aria-haspopup="menu" aria-expanded={open}
        onClick={() => (open ? close(false) : setOpen(true))}><Icon name="more" /></button>
      {open && (
        <div ref={menu} className="row-menu-popup" role="menu" aria-label={`${label} 的操作`} onKeyDown={onKeyDown}>
          {items.map((item, index) => {
            const name = armed === index && item.confirm ? item.confirm : `${item.label} ${label}`;
            const text = armed === index && item.confirm ? item.confirm : item.label;
            const className = item.danger ? "danger" : undefined;
            if (item.to && !item.disabled) {
              return <Link key={item.label} role="menuitem" aria-label={name} className={className} to={item.to} onClick={() => close(false)}>{text}</Link>;
            }
            return (
              <button key={item.label} type="button" role="menuitem" aria-label={name} className={className} aria-disabled={item.disabled || undefined} onClick={() => select(item, index)}>{text}</button>
            );
          })}
          {armed !== null && <button type="button" role="menuitem" aria-label="取消" onClick={() => setArmed(null)}>取消</button>}
        </div>
      )}
    </span>
  );
}
```

（禁用项用 `aria-disabled` 而不是 `disabled`：`disabled` 的按钮收不到焦点，方向键就跳不到它，读屏用户不知道这一项存在。）

`styles.css` 追加：

```css
/* 行操作菜单（components/RowMenu.tsx）。 */
.row-menu { position: relative; display: inline-flex; }
.row-menu-popup { position: absolute; z-index: 40; top: calc(100% + 4px); right: 0; min-width: 160px; padding: 4px; display: grid; background: var(--card-raised); border: 1px solid var(--line); border-radius: var(--radius-control); box-shadow: var(--shadow-float); }
.row-menu-popup [role="menuitem"] { display: block; width: 100%; min-height: 32px; padding: 0 10px; border: 0; border-radius: var(--radius-control); background: none; color: var(--fg); font: inherit; text-align: left; line-height: 32px; white-space: nowrap; cursor: pointer; }
.row-menu-popup [role="menuitem"]:hover, .row-menu-popup [role="menuitem"]:focus-visible { background: var(--card-hover); outline: none; }
.row-menu-popup [role="menuitem"].danger { color: var(--status-offline); }
.row-menu-popup [role="menuitem"][aria-disabled="true"] { color: var(--faint); cursor: default; }
```

- [ ] **Step 4: 跑测试确认绿**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/components/RowMenu.test.tsx src/styles.test.tsx > /tmp/a3-green.log 2>&1; echo $?`
Expected: 0（`.row-menu-popup [role="menuitem"]` 带 `white-space: nowrap`，它不命中公开页头部，nowrap 枚举测试仍绿）。

- [ ] **Step 5: 缺陷注入**

(a) `select` 里去掉 `armed !== index` 判断（首击就执行）：第二条用例红在 `onDelete not.toHaveBeenCalled`。(b) `close` 里去掉 `setArmed(null)`：Escape 后重开仍显示「确认删除」，第二条用例红在 `getByRole("menuitem", { name: "删除 web-01（#1）" })`。各自恢复后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/components/RowMenu.tsx web/src/components/RowMenu.test.tsx web/src/components/Icon.tsx web/src/styles.css && git commit -m "feat(web): 行操作菜单，危险项在菜单内两段式确认"
```

### Task 4: 外壳：侧栏 232、顶栏 48、面包屑、明暗切换、公开页链接

**Files:**
- Modify: `web/src/components/Layout.tsx`、`web/src/components/Layout.test.tsx`
- Create: `web/src/components/PageHeader.tsx`
- Modify: `web/src/lib/scheme.ts`（`ADMIN_SCHEME_KEY`）
- Modify: `web/src/admin.css`（外壳尺寸；删 `.sidebar-footer*`、`.admin-avatar`、`.workspace-label`、`.scheme-control*`、`.eyebrow`）

**Interfaces:**
- Consumes: `ThemeToggle`、`readSchemeChoice / writeSchemeChoice / nextSchemeChoice`、`Modal`。
- Produces:
  ```ts
  export const ADMIN_SCHEME_KEY = "heron-admin-scheme";   // lib/scheme.ts；与改版前同一个键，老用户的选择保留
  export function PageHeader({ title, description, actions }: { title: string; description?: string; actions?: ReactNode }): JSX.Element;  // <header class="page-header"><div><h1/>{description && <p/>}</div>{actions && <div class="page-header-actions"/>}</header>
  ```
  外壳 DOM：`aside.admin-sidebar`（`span.brand` Heron；`nav.panel-nav[aria-label=主导航]` 三组十四项不变）；`header.admin-topbar`：`button` 「打开导航」（窄屏）、`nav.admin-breadcrumb[aria-label=位置]`（「工作台 / 当前页」）、Task 5 的搜索按钮占位、`a.public-page-link` 「公开页 ↗」（`href="/" target=_blank`）、`ThemeToggle`、`button` 「登出」。没有侧栏页脚、没有「管理工作台」副标题、没有编造的头像。

- [ ] **Step 1: 改测试（红）**

`Layout.test.tsx` 第一个用例改为：

```tsx
it("明暗切换保存在本机并在离开布局时恢复原页面配色", async () => {
  document.documentElement.dataset.theme = "light";
  const { router } = renderWithAdmin({}, routes, "/");
  try {
    const toggle = screen.getByRole("button", { name: "明暗切换" });
    fireEvent.click(toggle);   // auto → light
    fireEvent.click(toggle);   // light → dark
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(localStorage.getItem("heron-admin-scheme")).toBe("dark");
    await act(() => router.navigate("/login"));
    expect(document.documentElement.dataset.theme).toBe("light");
  } finally { localStorage.removeItem("heron-admin-scheme"); delete document.documentElement.dataset.theme; }
});
```

追加：

```tsx
it("顶栏只有导航开关、面包屑、搜索、公开页链接、明暗切换与登出；侧栏没有页脚", () => {
  renderWithAdmin({}, [{ path: "/", Component: Layout, children: [{ index: true, element: <h1>home</h1> }] }], "/");
  const topbar = screen.getByRole("banner");
  expect(within(topbar).getByRole("navigation", { name: "位置" })).toHaveTextContent("工作台/总览");
  expect(within(topbar).getByRole("link", { name: "公开页 ↗" })).toHaveAttribute("href", "/");
  expect(within(topbar).getByRole("button", { name: "明暗切换" })).toBeInTheDocument();
  expect(within(topbar).getByRole("button", { name: "登出" })).toBeInTheDocument();
  expect(screen.queryByText("管理工作台")).toBeNull();
  expect(screen.queryByText("基础设施监控")).toBeNull();
  expect(screen.getByRole("navigation", { name: "主导航" })).toHaveClass("panel-nav");
});
```

（`header.admin-topbar` 要成为 `role=banner`，它必须不在 `aside/main/nav` 里——现在它在 `div.admin-workspace` 下，满足。）

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/components/Layout.test.tsx > /tmp/a4-red.log 2>&1; echo $?`
Expected: 1；红在「明暗切换」按钮不存在、「位置」导航不存在。

- [ ] **Step 3: 实现**

`lib/scheme.ts` 加：`export const ADMIN_SCHEME_KEY = "heron-admin-scheme";`

`components/PageHeader.tsx`：

```tsx
import type { ReactNode } from "react";

// 列表页统一为 页头 → 筛选行 → 表格（设计 §4.1）；页头只有标题、一句可选说明与主按钮。
export function PageHeader({ title, description, actions }: { title: string; description?: string; actions?: ReactNode }) {
  return (
    <header className="page-header">
      <div><h1>{title}</h1>{description && <p>{description}</p>}</div>
      {actions && <div className="page-header-actions">{actions}</div>}
    </header>
  );
}
```

`Layout.tsx`：删掉 `savedScheme`、`scheme-control` 的 `select`、侧栏页脚与 `workspace-label`；主题改为：

```tsx
const [choice, setChoice] = useState(() => readSchemeChoice(ADMIN_SCHEME_KEY));
useEffect(() => {
  const previous = document.documentElement.dataset.theme;
  if (choice === "auto") delete document.documentElement.dataset.theme;
  else document.documentElement.dataset.theme = choice;
  return () => {
    if (previous === undefined) delete document.documentElement.dataset.theme;
    else document.documentElement.dataset.theme = previous;
  };
}, [choice]);
```

顶栏：

```tsx
<header className="admin-topbar">
  <button type="button" className="icon-button mobile-menu" aria-label="打开导航" aria-expanded={menuOpener !== null} onClick={(event) => setMenuOpener(event.currentTarget)}><Icon name="menu" /></button>
  <nav className="admin-breadcrumb" aria-label="位置"><span>工作台</span><span aria-hidden="true">/</span><strong>{current}</strong></nav>
  <div className="topbar-actions">
    {/* Task 5 在这里放 QuickSearch */}
    <a href="/" target="_blank" rel="noreferrer" className="public-page-link"><Icon name="external" /><span>公开页 ↗</span></a>
    <ThemeToggle choice={choice} onChange={(next) => { writeSchemeChoice(ADMIN_SCHEME_KEY, next); setChoice(next); }} />
    <button type="button" className="icon-button" title="登出" aria-label="登出" onClick={() => logout.mutate({})} disabled={logout.isPending}><Icon name="logout" /></button>
  </div>
</header>
```

侧栏：

```tsx
<aside className="admin-sidebar">
  <div className="sidebar-brand"><span className="brand"><HeronMark />Heron</span></div>
  <Navigation />
</aside>
```

`admin.css`：`.admin-sidebar { width: 232px; … }`、`.admin-workspace { margin-left: 232px; }`、`.admin-topbar { height: 48px; padding: 0 16px; gap: 12px; }`、`.sidebar-brand { padding: 12px 16px; height: 48px; display: flex; align-items: center; border-bottom: 1px solid var(--line); }`、`.sidebar-brand .brand { font-size: 16px; }`、`.admin-shell .panel-nav a { min-height: 32px; padding: 0 10px; border-radius: var(--radius-control); font-size: 13px; }`、`.admin-main { padding: 16px 24px; max-width: 1680px; }`；1200px 媒体查询里的 196px 侧栏改为 200px；删除 `.sidebar-footer`、`.admin-avatar`、`.workspace-label`、`.scheme-control*`、`.eyebrow`、`.admin-breadcrumb` 的 `white-space: nowrap`（styles.test 的 nowrap 枚举只看 styles.css，但这条本来就不该有）。

- [ ] **Step 4: 跑测试确认绿，再跑全量与类型检查**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/components/Layout.test.tsx src/styles.test.tsx > /tmp/a4-green.log 2>&1; echo $?` → 0
Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm typecheck > /tmp/a4-tsc.log 2>&1; echo $?` → 0

- [ ] **Step 5: 缺陷注入**

把 `writeSchemeChoice(ADMIN_SCHEME_KEY, next)` 去掉：第一条用例红在 `localStorage.getItem` 为 null；恢复后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/components/Layout.tsx web/src/components/Layout.test.tsx web/src/components/PageHeader.tsx web/src/lib/scheme.ts web/src/admin.css && git commit -m "feat(web): 管理外壳按设计重排：232px 侧栏、48px 顶栏、明暗切换与公开页链接"
```

### Task 5: 节点快速搜索（⌘K）

**Files:**
- Create: `web/src/components/QuickSearch.tsx`、`web/src/components/QuickSearch.test.tsx`
- Modify: `web/src/components/Layout.tsx`（顶栏放入）、`web/src/admin.css`（`.quick-search*`）

**Interfaces:**
- Consumes: `Modal`、`filterNodes`（`lib/nodeSearch.ts`）、`AdminService.method.listNodes`。
- Produces: `export function QuickSearch(): JSX.Element` —— 顶栏按钮 `button.quick-search-trigger[aria-label="搜索节点"]`（可见文字「搜索节点」+ `kbd` ⌘K）；打开 `dialog` 「搜索节点」，内有 `searchbox` 「名称、IP、地区、备注或主机名」，结果 `ul[role=listbox]` 至多 8 项 `li[role=option]`（含链接 `/nodes/:id`），ArrowDown / ArrowUp 选项、Enter 进入选中项、Escape 关闭；全局 `⌘K` / `Ctrl+K` 打开（已打开时不重复）；`listNodes` 只在打开时请求。

- [ ] **Step 1: 写测试（红）**

```tsx
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { renderWithAdmin } from "../test/harness";
import { QuickSearch } from "./QuickSearch";

const nodes = [{ id: 1n, name: "web-01", note: "customer" }, { id: 2n, name: "db-01", facts: { hostname: "db.internal" } }, { id: 3n, name: "edge" }];
const routes = [{ path: "/", element: <QuickSearch /> }, { path: "/nodes/:id", element: <p>详情</p> }];

it("⌘K 打开，输入过滤，方向键 + Enter 进入节点详情；关闭前不请求节点列表", async () => {
  const listNodes = vi.fn(async () => ({ nodes }));
  const { router } = renderWithAdmin({ listNodes }, routes, "/");
  expect(listNodes).not.toHaveBeenCalled();
  fireEvent.keyDown(window, { key: "k", metaKey: true });
  const dialog = await screen.findByRole("dialog", { name: "搜索节点" });
  const box = within(dialog).getByRole("searchbox");
  expect(box).toHaveFocus();
  await waitFor(() => expect(listNodes).toHaveBeenCalledTimes(1));
  fireEvent.change(box, { target: { value: "db" } });
  await waitFor(() => expect(within(dialog).getAllByRole("option").map((o) => o.textContent)).toEqual(["db-01"]));
  fireEvent.change(box, { target: { value: "" } });
  await waitFor(() => expect(within(dialog).getAllByRole("option")).toHaveLength(3));
  fireEvent.keyDown(box, { key: "ArrowDown" });
  fireEvent.keyDown(box, { key: "ArrowDown" });
  expect(within(dialog).getAllByRole("option")[1]).toHaveAttribute("aria-selected", "true");
  fireEvent.keyDown(box, { key: "Enter" });
  await waitFor(() => expect(router.state.location.pathname).toBe("/nodes/2"));
  expect(screen.queryByRole("dialog")).toBeNull();
});

it("点击顶栏按钮也能打开；没有匹配时说明；Escape 关闭并回焦", async () => {
  renderWithAdmin({ listNodes: async () => ({ nodes }) }, routes, "/");
  const trigger = screen.getByRole("button", { name: "搜索节点" });
  fireEvent.click(trigger);
  const box = await screen.findByRole("searchbox");
  fireEvent.change(box, { target: { value: "nothing" } });
  expect(await screen.findByText("没有匹配的节点。")).toBeInTheDocument();
  fireEvent.cancel(screen.getByRole("dialog"));
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(trigger).toHaveFocus();
});

it("列表请求失败时在对话框内显示错误，输入框仍在", async () => {
  renderWithAdmin({ listNodes: async () => { throw new Error("nodes down"); } }, routes, "/", { retry: false });
  fireEvent.click(screen.getByRole("button", { name: "搜索节点" }));
  expect(await screen.findByRole("alert")).toHaveTextContent(/nodes down/);
  expect(screen.getByRole("searchbox")).toBeInTheDocument();
});
```

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/components/QuickSearch.test.tsx > /tmp/a5-red.log 2>&1; echo $?` → 1（模块不存在）。

- [ ] **Step 3: 实现**

```tsx
import { useQuery } from "@connectrpc/connect-query";
import { skipToken } from "@tanstack/react-query";
import { type KeyboardEvent, useEffect, useState } from "react";
import { useNavigate } from "react-router";
import { errorBanner } from "../api/queryGate";
import { AdminService } from "../gen/heron/v1/admin_pb";
import { filterNodes } from "../lib/nodeSearch";
import { Icon } from "./Icon";
import { Modal } from "./Modal";

const LIMIT = 8;

// 顶栏节点搜索（设计 §4.1）：结果只给前 8 个，多了用方向键也翻不完，输入得更具体才是正路。
// 节点列表只在对话框打开时取：顶栏常驻在每一页，不能让每一页都多一条轮询。
export function QuickSearch() {
  const navigate = useNavigate();
  const [opener, setOpener] = useState<HTMLElement | null>(null);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const nodes = useQuery(AdminService.method.listNodes, opener ? {} : skipToken);
  useEffect(() => {
    const onKey = (event: globalThis.KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        setOpener((current) => current ?? document.querySelector<HTMLElement>(".quick-search-trigger") ?? document.body);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
  const close = () => { setOpener(null); setQuery(""); setActive(0); };
  const matches = nodes.data ? filterNodes(nodes.data.nodes, query).slice(0, LIMIT) : [];
  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "ArrowDown") { event.preventDefault(); setActive((i) => Math.min(i + 1, matches.length - 1)); }
    else if (event.key === "ArrowUp") { event.preventDefault(); setActive((i) => Math.max(i - 1, 0)); }
    else if (event.key === "Enter" && matches[active]) { event.preventDefault(); const id = matches[active].id; close(); void navigate(`/nodes/${id}`); }
  };
  return (
    <>
      <button type="button" className="quick-search-trigger" aria-label="搜索节点" onClick={(event) => setOpener(event.currentTarget)}>
        <Icon name="search" /><span>搜索节点</span><kbd>⌘K</kbd>
      </button>
      {opener && (
        <Modal title="搜索节点" opener={opener} onClose={close} className="quick-search">
          <div className="modal-body">
            <input data-autofocus type="search" aria-label="名称、IP、地区、备注或主机名" placeholder="名称、IP、地区、备注或主机名" value={query}
              onChange={(event) => { setQuery(event.target.value); setActive(0); }} onKeyDown={onKeyDown} />
            {errorBanner(nodes.error)}
            {nodes.data && matches.length === 0 && <p className="muted">没有匹配的节点。</p>}
            {matches.length > 0 && (
              <ul role="listbox" aria-label="匹配的节点">
                {matches.map((node, i) => (
                  <li key={String(node.id)} role="option" aria-selected={i === active} onPointerEnter={() => setActive(i)}>
                    <a href={`/admin/nodes/${node.id}`} onClick={(event) => { event.preventDefault(); close(); void navigate(`/nodes/${node.id}`); }}>{node.name}</a>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </Modal>
      )}
    </>
  );
}
```

`Layout.tsx` 的 `topbar-actions` 开头放 `<QuickSearch />`。`admin.css` 追加：

```css
.quick-search-trigger { display: inline-flex; align-items: center; gap: 8px; height: var(--control-h); padding: 0 10px; color: var(--muted); background: var(--bg); min-width: 220px; }
.quick-search-trigger kbd { margin-left: auto; font: inherit; font-size: 11px; color: var(--faint); border: 1px solid var(--line); border-radius: 3px; padding: 0 4px; }
.admin-modal.quick-search { width: min(560px, calc(100vw - 32px)); }
.quick-search input[type="search"] { width: 100%; }
.quick-search ul { list-style: none; margin: 8px 0 0; padding: 0; }
.quick-search li a { display: block; padding: 0 10px; line-height: 36px; border-radius: var(--radius-control); color: var(--fg); }
.quick-search li[aria-selected="true"] a { background: var(--card-hover); }
@media (max-width: 900px) { .quick-search-trigger { min-width: 0; } .quick-search-trigger span, .quick-search-trigger kbd { display: none; } }
```

- [ ] **Step 4: 跑测试确认绿**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/components/QuickSearch.test.tsx src/components/Layout.test.tsx > /tmp/a5-green.log 2>&1; echo $?` → 0

- [ ] **Step 5: 缺陷注入**

把 `skipToken` 换成恒 `{}`：第一条用例红在 `listNodes not.toHaveBeenCalled`；恢复后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/components/QuickSearch.tsx web/src/components/QuickSearch.test.tsx web/src/components/Layout.tsx web/src/admin.css && git commit -m "feat(web): 顶栏节点快速搜索，⌘K 打开，只在打开时取节点列表"
```

### Task 6: 管理端四态合并与「需要处理」的纯函数

**Files:**
- Create: `web/src/lib/adminStatus.ts`、`web/src/lib/adminStatus.test.ts`
- Create: `web/src/lib/attention.ts`、`web/src/lib/attention.test.ts`

**Interfaces:**
- Consumes: `nodeStatus / expiryLevel`（`lib/status.ts`）、`olderThan`（`lib/version.ts`）。
- Produces:
  ```ts
  // adminStatus.ts
  export function liveById(live: readonly LiveNode[] | undefined): ReadonlyMap<bigint, LiveNode>;   // LiveNode = admin_pb 的 NodeStatus（快照条目）
  export function liveStatus(node: Pick<Node, "maintenance">, live: Pick<LiveNode, "online" | "lastSeenAt"> | undefined): NodeStatus | undefined;
  //   维护中的节点不需要快照就是「维护中」；其余节点快照缺席时返回 undefined（未知），不是离线。
  // attention.ts
  export type AttentionCard = { key: "offline" | "expiring" | "lagging" | "firing"; label: string; count: number | null; note?: string; to: string };
  export const EXPIRING_DAYS = 30;
  export function attentionCards(input: { nodes: readonly Node[]; live: ReadonlyMap<bigint, LiveNode>; boundAgentVersion: string | undefined; states: readonly AlertStateEntry[] | undefined }): AttentionCard[];
  //   offline：liveStatus 为 offline 的数，note「从未上报 N」；expiring：expiryLevel(daysLeft) !== "neutral"（≤30 天，含已过期），note「已过期 N」；
  //   lagging：boundAgentVersion 未知时 count 为 null、note「无法取得 hub 绑定的 agent 版本」，否则 olderThan(agentVersion, bound) 的数、note「低于 vX」；
  //   firing：states 未到时 null，否则 state === "firing" 的条数（规则 × 节点）。
  //   去向：/nodes?status=offline、/nodes?expiring=1、/nodes?lagging=1、/alerts?state=firing。
  ```

- [ ] **Step 1: 写测试（红）**

`web/src/lib/adminStatus.test.ts`：

```ts
import { expect, it } from "vitest";
import { liveById, liveStatus } from "./adminStatus";

const live = liveById([{ id: 1n, name: "a", online: true, lastSeenAt: 100n }, { id: 2n, name: "b", online: false, lastSeenAt: 50n }, { id: 3n, name: "c", online: false }] as never);

it("快照 + 维护中合成四态；维护中压过在线；从未上报看快照的 lastSeenAt", () => {
  expect(liveStatus({ maintenance: false }, live.get(1n))).toBe("online");
  expect(liveStatus({ maintenance: true }, live.get(1n))).toBe("maintenance");
  expect(liveStatus({ maintenance: false }, live.get(2n))).toBe("offline");
  expect(liveStatus({ maintenance: false }, live.get(3n))).toBe("never");
});

it("快照里没有这个节点：维护中仍是维护中，其余是未知而不是离线", () => {
  expect(liveStatus({ maintenance: true }, undefined)).toBe("maintenance");
  expect(liveStatus({ maintenance: false }, undefined)).toBeUndefined();
});
```

`web/src/lib/attention.test.ts`：

```ts
import { expect, it } from "vitest";
import { liveById } from "./adminStatus";
import { attentionCards } from "./attention";

const nodes = [
  { id: 1n, name: "on", maintenance: false, facts: { agentVersion: "v0.8.0" }, billing: { daysLeft: 12 } },
  { id: 2n, name: "off", maintenance: false, facts: { agentVersion: "v0.7.0" }, billing: { daysLeft: -3 } },
  { id: 3n, name: "never", maintenance: false },
  { id: 4n, name: "maint", maintenance: true, facts: { agentVersion: "dev" }, billing: { daysLeft: 400 } },
  { id: 5n, name: "unknown", maintenance: false, facts: { agentVersion: "v0.6.0" } },
] as never[];
const live = liveById([{ id: 1n, online: true, lastSeenAt: 9n }, { id: 2n, online: false, lastSeenAt: 1n }, { id: 3n, online: false }, { id: 4n, online: true, lastSeenAt: 9n }] as never);
const states = [{ ruleId: 1n, nodeId: 1n, state: "firing" }, { ruleId: 1n, nodeId: 2n, state: "pending" }, { ruleId: 2n, nodeId: 1n, state: "firing" }] as never[];

it("四个数各按自己的谓词：离线不含未知与维护中，到期含已过期，落后只比绑定版本，触发按状态条数", () => {
  const cards = attentionCards({ nodes, live, boundAgentVersion: "v0.8.0", states });
  expect(cards.map((c) => [c.key, c.count, c.note, c.to])).toEqual([
    ["offline", 1, "从未上报 1", "/nodes?status=offline"],
    ["expiring", 2, "已过期 1", "/nodes?expiring=1"],
    ["lagging", 2, "低于 v0.8.0", "/nodes?lagging=1"],
    ["firing", 2, undefined, "/alerts?state=firing"],
  ]);
});

it("绑定版本未知时落后数是 null 并说明原因；告警状态未到时触发数是 null；没有节点时四个数都是 0", () => {
  const unknown = attentionCards({ nodes, live, boundAgentVersion: undefined, states: undefined });
  expect(unknown[2]).toMatchObject({ count: null, note: "无法取得 hub 绑定的 agent 版本" });
  expect(unknown[3].count).toBeNull();
  expect(attentionCards({ nodes: [], live: new Map(), boundAgentVersion: "v0.8.0", states: [] }).map((c) => c.count)).toEqual([0, 0, 0, 0]);
});
```

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/lib/adminStatus.test.ts src/lib/attention.test.ts > /tmp/a6-red.log 2>&1; echo $?` → 1（模块不存在）。

- [ ] **Step 3: 实现**

`web/src/lib/adminStatus.ts`：

```ts
import type { Node, NodeStatus as LiveNode } from "../gen/heron/v1/admin_pb";
import { nodeStatus, type NodeStatus } from "./status";

export function liveById(live: readonly LiveNode[] | undefined): ReadonlyMap<bigint, LiveNode> {
  return new Map((live ?? []).map((n) => [n.id, n]));
}

// 四态判定只有 nodeStatus 一处；管理端的输入分两个来源：快照条目带 hub 裁决的 online 与 lastSeenAt，节点资料才带 maintenance。
// 快照里没有这个节点（快照未到、刷新失败、刚创建还没进快照）时不能编一个 online=false 交给判定——那会把它读成离线；
// 维护中例外：它由站长设置，不依赖快照。
export function liveStatus(node: Pick<Node, "maintenance">, live: Pick<LiveNode, "online" | "lastSeenAt"> | undefined): NodeStatus | undefined {
  if (!live && !node.maintenance) return undefined;
  return nodeStatus({ online: live?.online ?? false, maintenance: node.maintenance, lastSeenAt: live?.lastSeenAt });
}
```

`web/src/lib/attention.ts`：

```ts
import type { AlertStateEntry, Node, NodeStatus as LiveNode } from "../gen/heron/v1/admin_pb";
import { liveStatus } from "./adminStatus";
import { expiryLevel } from "./status";
import { olderThan } from "./version";

export type AttentionCard = { key: "offline" | "expiring" | "lagging" | "firing"; label: string; count: number | null; note?: string; to: string };

// 与 expiryLevel 的「需关注」边界同一个数：卡片的计数与节点列表里到期列的琥珀色必须指同一批节点。
export const EXPIRING_DAYS = 30;

// 总览首行「需要处理」（设计 §4.2）：每张卡一个待办谓词，点进去是同一谓词的筛选结果。
// count 为 null 表示依赖的数据没拿到（绑定版本、告警状态），显示为「—」；0 才是真的没有。
export function attentionCards({ nodes, live, boundAgentVersion, states }: {
  nodes: readonly Node[]; live: ReadonlyMap<bigint, LiveNode>; boundAgentVersion: string | undefined; states: readonly AlertStateEntry[] | undefined;
}): AttentionCard[] {
  const statuses = nodes.map((n) => liveStatus(n, live.get(n.id)));
  const offline = statuses.filter((s) => s === "offline").length;
  const never = statuses.filter((s) => s === "never").length;
  const expiring = nodes.filter((n) => expiryLevel(n.billing?.daysLeft) !== "neutral");
  const expired = expiring.filter((n) => (n.billing?.daysLeft ?? 0) < 0).length;
  const lagging = boundAgentVersion === undefined ? null : nodes.filter((n) => olderThan(n.facts?.agentVersion, boundAgentVersion)).length;
  return [
    { key: "offline", label: "离线", count: offline, note: `从未上报 ${never}`, to: "/nodes?status=offline" },
    { key: "expiring", label: `${EXPIRING_DAYS} 天内到期`, count: expiring.length, note: `已过期 ${expired}`, to: "/nodes?expiring=1" },
    { key: "lagging", label: "agent 版本落后", count: lagging, note: boundAgentVersion === undefined ? "无法取得 hub 绑定的 agent 版本" : `低于 ${boundAgentVersion}`, to: "/nodes?lagging=1" },
    { key: "firing", label: "触发中告警", count: states === undefined ? null : states.filter((s) => s.state === "firing").length, to: "/alerts?state=firing" },
  ];
}
```

- [ ] **Step 4: 跑测试确认绿**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/lib/adminStatus.test.ts src/lib/attention.test.ts > /tmp/a6-green.log 2>&1; echo $?` → 0

- [ ] **Step 5: 缺陷注入**

(a) `liveStatus` 对快照缺席返回 `nodeStatus({ online: false, … })`：第二条用例红在 `toBeUndefined`。(b) `attentionCards` 的 offline 改用 `!live.get(n.id)?.online`：第一条用例红在 offline 期望 1 实际 3（未知与维护中被算进去）。各自恢复后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/lib/adminStatus.ts web/src/lib/adminStatus.test.ts web/src/lib/attention.ts web/src/lib/attention.test.ts && git commit -m "feat(web): 管理端四态合并与「需要处理」四个数的纯函数"
```

### Task 7: 总览：需要处理四张卡与实时节点表

**Files:**
- Modify: `web/src/pages/Overview.tsx`（重写）、`web/src/pages/Overview.test.tsx`（重写）
- Modify: `web/src/admin.css`（删 `.stats-grid`、`.metric-card*`、`.status-pill*`、`.live-caption`、`.section-heading*`、`.node-search*`（总览用）；追加 `.attention*`、`.overview-table*` 与手机卡片）

**Interfaces:**
- Consumes: `attentionCards`、`liveById / liveStatus`、`StatusBadge`、`Bar thin`、`filterNodes`、`PageHeader`、`STATUS_LABEL`。
- Produces: DOM —— `PageHeader` 「总览」；`ul.attention[aria-label=需要处理]` 四个 `li > a[href]`（`strong.num` 数字或「—」、`span` 标签、`small` 说明）；`div.filter-row[role=group][aria-label=筛选]` 内 `searchbox` 「搜索节点」；`table.nodes.overview-table` 列：状态、节点、CPU、内存、磁盘、负载、网络、本周期、最近上报；每行 `tr[data-status]`，状态格 `span.status-dot[role=img][aria-label=STATUS_LABEL 或 "状态未知"]`，CPU / 内存 / 磁盘格 `Bar thin`（名「CPU 42%」「内存 50%」「磁盘 10%」）+ `span.num` 百分比，负载 / 网络 / 本周期 `span.num`，最近上报 `ago` 或「从未上报」。页面以 `getSnapshot`（2s）与 `listNodes`（10s）同为门控；`listAlertRules`（10s）只喂「触发中告警」卡。

- [ ] **Step 1: 重写测试（红）**

`web/src/pages/Overview.test.tsx`：

```tsx
import { Code, ConnectError } from "@connectrpc/connect";
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { renderWithAdmin } from "../test/harness";
import { POLL_MS } from "../lib/poll";
import { Overview } from "./Overview";

const snapshot = {
  now: 1_000_000n, reportIntervalMs: 10_000, hubVersion: "v0.9.0", boundAgentVersion: "v0.8.0",
  nodes: [
    { id: 1n, name: "web-01", online: true, lastSeenAt: 999_990n,
      traffic: { periodRx: 1024n ** 3n, periodTx: 512n * 1024n ** 2n },
      metrics: { cpuPct: 42, memUsed: 512n * 1024n ** 2n, memTotal: 1024n ** 3n, diskUsed: 1024n ** 3n, diskTotal: 10n * 1024n ** 3n, load1: 0.5, load5: 0.4, load15: 0.3, netRxBps: 1024n, netTxBps: 2048n } },
    { id: 2n, name: "never", online: false },
    { id: 3n, name: "gone", online: false, lastSeenAt: 900_000n },
  ],
};
const nodes = [
  { id: 1n, name: "web-01", note: "customer", maintenance: false, facts: { hostname: "hostname.internal", agentVersion: "v0.8.0" }, billing: { daysLeft: 20 } },
  { id: 2n, name: "never", maintenance: false },
  { id: 3n, name: "gone", maintenance: true, facts: { agentVersion: "v0.7.0" } },
  { id: 4n, name: "fresh", maintenance: false },
];
const rules = { rules: [], states: [{ ruleId: 1n, nodeId: 3n, state: "firing" }, { ruleId: 1n, nodeId: 1n, state: "pending" }] };
const impl = { getSnapshot: async () => snapshot, listNodes: async () => ({ nodes }), listAlertRules: async () => rules };
const render = (over: Partial<typeof impl> = {}) => renderWithAdmin({ ...impl, ...over }, [{ path: "/", Component: Overview }], "/");

afterEach(() => vi.useRealTimers());

it("需要处理四张卡按同一判定计数并链到对应筛选", async () => {
  render();
  const cards = within(await screen.findByRole("list", { name: "需要处理" })).getAllByRole("link");
  expect(cards.map((a) => [a.textContent, a.getAttribute("href")])).toEqual([
    ["0离线从未上报 1", "/nodes?status=offline"],      // gone 维护中不算离线；never 从未上报
    ["130 天内到期已过期 0", "/nodes?expiring=1"],
    ["1agent 版本落后低于 v0.8.0", "/nodes?lagging=1"],
    ["1触发中告警", "/alerts?state=firing"],
  ]);
});

it("实时表每行一个状态点与细条；维护中压过在线，不在快照里的节点是状态未知", async () => {
  render();
  const web = within(await screen.findByRole("row", { name: /web-01/ }));
  expect(web.getByRole("img", { name: "在线" })).toBeInTheDocument();
  expect(web.getByRole("meter", { name: "CPU 42%" })).toHaveAttribute("aria-valuenow", "42");
  expect(web.getByRole("meter", { name: "内存 50%" })).toBeInTheDocument();
  expect(web.getByRole("meter", { name: "磁盘 10%" })).toBeInTheDocument();
  expect(web.getByText("0.50 / 0.40 / 0.30")).toBeInTheDocument();
  expect(web.getByText("↓ 1.0 KiB/s ↑ 2.0 KiB/s")).toBeInTheDocument();
  expect(web.getByText("↓ 1.0 GiB ↑ 512 MiB")).toBeInTheDocument();
  expect(web.getByText("10 秒前")).toBeInTheDocument();
  expect(web.getByRole("link", { name: "web-01（#1）" })).toHaveAttribute("href", "/nodes/1");
  const never = within(screen.getByRole("row", { name: /never/ }));
  expect(never.getByRole("img", { name: "从未上报" })).toBeInTheDocument();
  expect(never.getAllByLabelText("无读数")).toHaveLength(6);
  expect(never.getByText("从未上报", { selector: "td" })).toBeInTheDocument();
  expect(within(screen.getByRole("row", { name: /gone/ })).getByRole("img", { name: "维护中" })).toBeInTheDocument();
  expect(within(screen.getByRole("row", { name: /fresh/ })).getByRole("img", { name: "状态未知" })).toBeInTheDocument();
});

it("告警状态未到或失败时触发卡显示「—」而不是 0，页面其余照常", async () => {
  render({ listAlertRules: async () => { throw new ConnectError("rules down", Code.Unavailable); } });
  const cards = within(await screen.findByRole("list", { name: "需要处理" })).getAllByRole("link");
  expect(cards[3]).toHaveTextContent("—触发中告警");
  expect(await screen.findByRole("alert")).toHaveTextContent("rules down");
  expect(screen.getByRole("table")).toBeInTheDocument();
});

it.each(["WEB", "CUSTOMER", "HOSTNAME"])("搜索 %s 过滤实时表，清空恢复", async (search) => {
  render();
  await screen.findByRole("row", { name: /never/ });
  const input = screen.getByRole("searchbox", { name: "搜索节点" });
  fireEvent.change(input, { target: { value: search } });
  expect(screen.getAllByRole("row").slice(1).map((row) => row.getAttribute("aria-label") ?? within(row).getByRole("link").textContent)).toEqual(["web-01"]);
  fireEvent.change(input, { target: { value: "absent" } });
  expect(screen.getByRole("status")).toHaveTextContent("没有匹配的节点。");
  fireEvent.change(input, { target: { value: "" } });
  expect(screen.getAllByRole("row")).toHaveLength(5);
});

it("已有快照时刷新失败仍保留表格并显示错误", async () => {
  const getSnapshot = vi.fn().mockResolvedValueOnce(snapshot).mockRejectedValue(new ConnectError("snapshot unavailable", Code.Unavailable));
  const { queryClient } = render({ getSnapshot });
  await screen.findByRole("row", { name: /web-01/ });
  await act(async () => { await queryClient.refetchQueries(); });
  expect(await screen.findByRole("alert")).toHaveTextContent(/^snapshot unavailable$/);
  expect(screen.getByRole("table")).toBeInTheDocument();
});

it("按 POLL_MS 轮询快照", async () => {
  expect(POLL_MS).toBe(2000);
  vi.useFakeTimers({ shouldAdvanceTime: true });
  const getSnapshot = vi.fn(async () => snapshot);
  render({ getSnapshot });
  await waitFor(() => expect(getSnapshot).toHaveBeenCalledTimes(1));
  await screen.findByRole("row", { name: /web-01/ });
  await act(async () => vi.advanceTimersByTimeAsync(POLL_MS + 100));
  await waitFor(() => expect(getSnapshot.mock.calls.length).toBeGreaterThanOrEqual(2));
});

it("没有节点时四张卡都是 0 且可点，表格位置给出去处", async () => {
  render({ getSnapshot: async () => ({ ...snapshot, nodes: [] }), listNodes: async () => ({ nodes: [] }), listAlertRules: async () => ({ rules: [], states: [] }) });
  const cards = within(await screen.findByRole("list", { name: "需要处理" })).getAllByRole("link");
  expect(cards.map((a) => a.querySelector("strong")?.textContent)).toEqual(["0", "0", "0", "0"]);
  expect(screen.getByText(/还没有节点/)).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "注册窗口" })).toHaveAttribute("href", "/register");
});

it("请求首次失败时显示 hub 的错误正文", async () => {
  render({ getSnapshot: async () => { throw new ConnectError("snapshot unavailable", Code.Unavailable); } });
  expect(await screen.findByRole("alert")).toHaveTextContent(/^snapshot unavailable$/);
});
```

每行给 `aria-label={node.name}`（`tr`），`getByRole("row", { name })` 才稳定。

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/Overview.test.tsx > /tmp/a7-red.log 2>&1; echo $?` → 1（「需要处理」列表不存在）。

- [ ] **Step 3: 实现**

`web/src/pages/Overview.tsx`：

```tsx
import { useQuery } from "@connectrpc/connect-query";
import { useState } from "react";
import { Link } from "react-router";
import { errorBanner, queryGate, queryGateAll } from "../api/queryGate";
import { Bar, Missing, ratio } from "../components/Bar";
import { PageHeader } from "../components/PageHeader";
import { AdminService, type Node, type NodeStatus as LiveNode } from "../gen/heron/v1/admin_pb";
import { liveById, liveStatus } from "../lib/adminStatus";
import { attentionCards } from "../lib/attention";
import { ago, bytes, percent } from "../lib/format";
import { withId } from "../lib/ids";
import { filterNodes } from "../lib/nodeSearch";
import { POLL_MS } from "../lib/poll";
import { STATUS_LABEL } from "../lib/status";

// 节点资料每 10s：维护中、到期、agent 版本都在这里；快照每 2s：在线与读数。两者同为门控——没有资料就判不出维护中，
// 把维护中的节点画成在线比晚两秒出表格更糟。
const DETAILS_MS = 10_000;

export function Overview() {
  const [search, setSearch] = useState("");
  const snap = useQuery(AdminService.method.getSnapshot, {}, { refetchInterval: POLL_MS });
  const details = useQuery(AdminService.method.listNodes, {}, { refetchInterval: DETAILS_MS });
  const rules = useQuery(AdminService.method.listAlertRules, {}, { refetchInterval: DETAILS_MS });
  const gate = queryGateAll(snap, details);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const [snapshot, listed] = gate.data;
  const now = Number(snapshot.now);
  const live = liveById(snapshot.nodes);
  const cards = attentionCards({ nodes: listed.nodes, live, boundAgentVersion: snapshot.boundAgentVersion, states: rules.data?.states });
  const rows = filterNodes(listed.nodes, search);
  return (
    <section>
      <PageHeader title="总览" />
      {gate.banner}
      {errorBanner(rules.error)}
      <ul className="attention" aria-label="需要处理">
        {cards.map((card) => (
          <li key={card.key} data-key={card.key} data-zero={card.count === 0 || undefined}>
            <Link to={card.to}><strong className="num">{card.count === null ? "—" : card.count}</strong><span>{card.label}</span>{card.note && <small className="muted">{card.note}</small>}</Link>
          </li>
        ))}
      </ul>
      <div className="filter-row" role="group" aria-label="筛选">
        <input type="search" aria-label="搜索节点" placeholder="名称、IP、地区、备注或主机名" value={search} onChange={(event) => setSearch(event.target.value)} />
        <span className="muted">实时 · 每 {POLL_MS / 1000} 秒</span>
      </div>
      {listed.nodes.length === 0 && <p className="muted">还没有节点。去 <Link to="/nodes">节点</Link> 页创建，或开一个 <Link to="/register">注册窗口</Link>。</p>}
      {listed.nodes.length > 0 && rows.length === 0 && <p className="muted" role="status">没有匹配的节点。</p>}
      {rows.length > 0 && (
        <div className="table-scroll" role="region" aria-label="节点实时读数" tabIndex={0}>
          <table className="nodes overview-table">
            <thead><tr><th>状态</th><th>节点</th><th>CPU</th><th>内存</th><th>磁盘</th><th>负载</th><th>网络</th><th>本周期</th><th>最近上报</th></tr></thead>
            <tbody>{rows.map((node) => <NodeRow key={String(node.id)} node={node} live={live.get(node.id)} now={now} />)}</tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function Meter({ label, value }: { label: string; value: number | undefined }) {
  if (value === undefined) return <Missing />;
  return <><Bar thin value={value} label={`${label} ${percent(value)}`} /><span className="num">{percent(value)}</span></>;
}

function NodeRow({ node, live, now }: { node: Node; live: LiveNode | undefined; now: number }) {
  const status = liveStatus(node, live);
  const m = live?.metrics;
  return (
    <tr aria-label={node.name} data-status={status ?? "unknown"}>
      <td data-label="状态"><span className="status-dot" data-status={status} role="img" aria-label={status ? STATUS_LABEL[status] : "状态未知"} /></td>
      <td data-label="节点"><Link to={`/nodes/${node.id}`} aria-label={withId(node.name, node.id)}>{node.name}</Link></td>
      <td data-label="CPU"><Meter label="CPU" value={m?.cpuPct} /></td>
      <td data-label="内存"><Meter label="内存" value={m?.memUsed !== undefined && m.memTotal ? ratio(m.memUsed, m.memTotal) : undefined} /></td>
      <td data-label="磁盘"><Meter label="磁盘" value={m?.diskUsed !== undefined && m.diskTotal ? ratio(m.diskUsed, m.diskTotal) : undefined} /></td>
      <td data-label="负载" className="num">{m?.load1 !== undefined && m.load5 !== undefined && m.load15 !== undefined ? `${m.load1.toFixed(2)} / ${m.load5.toFixed(2)} / ${m.load15.toFixed(2)}` : <Missing />}</td>
      <td data-label="网络" className="num">{m?.netRxBps !== undefined && m.netTxBps !== undefined ? `↓ ${bytes(m.netRxBps)}/s ↑ ${bytes(m.netTxBps)}/s` : <Missing />}</td>
      <td data-label="本周期" className="num">{live?.traffic ? `↓ ${bytes(live.traffic.periodRx)} ↑ ${bytes(live.traffic.periodTx)}` : <Missing />}</td>
      <td data-label="最近上报" className="muted num">{live?.lastSeenAt !== undefined ? ago(live.lastSeenAt, now) : STATUS_LABEL.never}</td>
    </tr>
  );
}
```

（`Meter` 对缺失返回一个 `Missing`，所以从未上报的行恰好 6 个「无读数」：CPU、内存、磁盘、负载、网络、本周期。）

`admin.css`：删 `.stats-grid`、`.metric-card*`、`.status-pill*`、`.live-caption`、`.section-heading*`、`.admin-shell .node-search*`、`.admin-shell .node-filters*`（Task 9 的节点页改用 `.filter-row`），追加：

```css
/* 总览首行「需要处理」（pages/Overview.tsx）：四张可点卡，数字大、标签小；手机 2×2（设计 §4.2、§4.7）。 */
.attention { list-style: none; margin: 0 0 16px; padding: 0; display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; }
.attention a { display: grid; gap: 2px; padding: 12px 14px; border: 1px solid var(--line); border-radius: var(--radius-card); background: var(--card); color: var(--fg); }
.attention a:hover { border-color: var(--line-strong); text-decoration: none; }
.attention strong { font-size: 24px; line-height: 28px; font-weight: 600; }
.attention li:not([data-zero]) strong { color: var(--status-attention); }
.attention li[data-key="offline"]:not([data-zero]) strong, .attention li[data-key="firing"]:not([data-zero]) strong { color: var(--status-offline); }
.attention span { font-size: 13px; }
.attention small { font-size: 12px; }
.overview-table .bar.thin { width: 72px; display: inline-block; vertical-align: middle; margin-right: 6px; }
.overview-table td { white-space: nowrap; }
@media (max-width: 900px) {
  .attention { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .admin-shell .table-scroll:has(.overview-table) { border: 0; background: none; overflow: visible; }
  .admin-shell table.overview-table, .overview-table tbody { display: block; background: none; border: 0; }
  .overview-table thead { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
  .overview-table tr { display: grid; grid-template-columns: auto 1fr 1fr; gap: 6px 12px; align-items: center; margin-bottom: 8px; padding: 10px 12px; border: 1px solid var(--line); border-radius: var(--radius-card); background: var(--card); }
  .admin-shell table.overview-table td { height: auto; padding: 0; border: 0; white-space: normal; }
  .overview-table td[data-label="节点"] { grid-column: 2 / -1; font-weight: 600; }
  .overview-table td[data-label="负载"], .overview-table td[data-label="本周期"] { display: none; }
  .overview-table td[data-label="最近上报"] { grid-column: 1 / -1; font-size: 12px; }
}
```

（手机卡片按设计 §4.7 只留三个关键数字：CPU、内存、磁盘，加网络与最近上报；负载与本周期藏起来。）

- [ ] **Step 4: 跑测试确认绿，再跑全量**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/Overview.test.tsx > /tmp/a7-green.log 2>&1; echo $?` → 0
Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run > /tmp/a7-all.log 2>&1; echo $?` → 0；`pnpm typecheck` → 0。

- [ ] **Step 5: 缺陷注入**

(a) `NodeRow` 里把 `liveStatus(node, live)` 换成 `live?.online ? "online" : "offline"`：第二条用例红在 `gone` 行找不到「维护中」与 `fresh` 行找不到「状态未知」。(b) 卡片数据把 `rules.data?.states` 换成 `rules.data?.states ?? []`：第三条用例红在期望「—」实际「0」。各自恢复后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/pages/Overview.tsx web/src/pages/Overview.test.tsx web/src/admin.css && git commit -m "feat(web): 管理总览首行改为「需要处理」四张卡，实时表用状态点与细条"
```

---

## 里程碑 B：节点列表与抽屉

### Task 8: 节点列表的筛选状态

**Files:**
- Create: `web/src/lib/nodeFilters.ts`、`web/src/lib/nodeFilters.test.ts`

**Interfaces:**
- Consumes: `liveStatus`、`expiryLevel`、`olderThan`、`filterNodes`、`sameTag`（`lib/tags.ts`）。
- Produces:
  ```ts
  export type ScopeFilters = { status: NodeStatus | null; expiring: boolean; lagging: boolean };   // 来自 URL：status=online|offline|never|maintenance、expiring=1、lagging=1；非法值忽略
  export function scopeFromParams(params: URLSearchParams): ScopeFilters;
  export function paramsWithScope(params: URLSearchParams, scope: ScopeFilters): URLSearchParams;  // 回写；为空的键删掉，其它键原样保留
  export const STATUS_OPTIONS: readonly { value: NodeStatus; label: string }[];                    // 四态，顺序 STATUS_ORDER
  export function applyScope(nodes: readonly Node[], live: ReadonlyMap<bigint, LiveNode>, boundAgentVersion: string | undefined, scope: ScopeFilters, search: string): Node[];
  //   先 filterNodes(search)，再按 status（liveStatus 相等；未知状态只在 status=null 时出现）、expiring（expiryLevel !== "neutral"）、lagging（绑定版本未知时过滤结果为空并由调用方说明原因）
  export const isScoped = (scope: ScopeFilters): boolean;
  ```
  标签筛选不在这里：它继续走 `ListNodesRequest.tags / untagged`（服务端），由 `Nodes.tsx` 的 `TagFilterState` 持有。

- [ ] **Step 1: 写测试（红）**

```ts
import { expect, it } from "vitest";
import { liveById } from "./adminStatus";
import { applyScope, isScoped, paramsWithScope, scopeFromParams } from "./nodeFilters";

it("URL 参数解析：合法值进入筛选，非法值被忽略而不是变成空列表", () => {
  expect(scopeFromParams(new URLSearchParams("status=offline&expiring=1&lagging=1"))).toEqual({ status: "offline", expiring: true, lagging: true });
  expect(scopeFromParams(new URLSearchParams("status=foo&expiring=yes&lagging=0"))).toEqual({ status: null, expiring: false, lagging: false });
  expect(isScoped(scopeFromParams(new URLSearchParams("")))).toBe(false);
});

it("回写只动自己的三个键，其余参数保留；空值删键", () => {
  const next = paramsWithScope(new URLSearchParams("tab=x&status=offline"), { status: null, expiring: true, lagging: false });
  expect(next.toString()).toBe("tab=x&expiring=1");
});

const nodes = [
  { id: 1n, name: "on", maintenance: false, facts: { agentVersion: "v0.7.0" }, billing: { daysLeft: 5 } },
  { id: 2n, name: "off", maintenance: false, facts: { agentVersion: "v0.8.0" } },
  { id: 3n, name: "unknown", maintenance: false },
  { id: 4n, name: "maint", maintenance: true },
] as never[];
const live = liveById([{ id: 1n, online: true, lastSeenAt: 1n }, { id: 2n, online: false, lastSeenAt: 1n }, { id: 4n, online: true, lastSeenAt: 1n }] as never);

it("按状态 / 到期 / 落后过滤；未知状态只在不按状态筛时出现；筛选可叠加搜索", () => {
  const names = (scope: Parameters<typeof applyScope>[3], search = "") => applyScope(nodes, live, "v0.8.0", scope, search).map((n: { name: string }) => n.name);
  expect(names({ status: null, expiring: false, lagging: false })).toEqual(["on", "off", "unknown", "maint"]);
  expect(names({ status: "offline", expiring: false, lagging: false })).toEqual(["off"]);
  expect(names({ status: "maintenance", expiring: false, lagging: false })).toEqual(["maint"]);
  expect(names({ status: null, expiring: true, lagging: false })).toEqual(["on"]);
  expect(names({ status: null, expiring: false, lagging: true })).toEqual(["on"]);
  expect(names({ status: "online", expiring: true, lagging: true }, "on")).toEqual(["on"]);
  expect(applyScope(nodes, live, undefined, { status: null, expiring: false, lagging: true }, "")).toEqual([]);
});
```

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/lib/nodeFilters.test.ts > /tmp/b8-red.log 2>&1; echo $?` → 1

- [ ] **Step 3: 实现**

```ts
import type { Node, NodeStatus as LiveNode } from "../gen/heron/v1/admin_pb";
import { liveStatus } from "./adminStatus";
import { filterNodes } from "./nodeSearch";
import { expiryLevel, STATUS_LABEL, STATUS_ORDER, type NodeStatus } from "./status";
import { olderThan } from "./version";

export type ScopeFilters = { status: NodeStatus | null; expiring: boolean; lagging: boolean };

export const STATUS_OPTIONS: readonly { value: NodeStatus; label: string }[] = STATUS_ORDER.map((value) => ({ value, label: STATUS_LABEL[value] }));

// 「需要处理」的卡链到这里（lib/attention.ts 的 to）：谓词必须与那边逐个相同，否则卡上的数与列表行数对不上。
// URL 是外部输入：不认识的值当没写，列表不能因为一个错字变空且无法清除。
export function scopeFromParams(params: URLSearchParams): ScopeFilters {
  const status = params.get("status");
  return {
    status: STATUS_ORDER.includes(status as NodeStatus) ? (status as NodeStatus) : null,
    expiring: params.get("expiring") === "1",
    lagging: params.get("lagging") === "1",
  };
}

export function paramsWithScope(params: URLSearchParams, scope: ScopeFilters): URLSearchParams {
  const next = new URLSearchParams(params);
  if (scope.status) next.set("status", scope.status); else next.delete("status");
  if (scope.expiring) next.set("expiring", "1"); else next.delete("expiring");
  if (scope.lagging) next.set("lagging", "1"); else next.delete("lagging");
  return next;
}

export const isScoped = (scope: ScopeFilters): boolean => scope.status !== null || scope.expiring || scope.lagging;

export function applyScope(nodes: readonly Node[], live: ReadonlyMap<bigint, LiveNode>, boundAgentVersion: string | undefined, scope: ScopeFilters, search: string): Node[] {
  return filterNodes(nodes, search).filter((node) => {
    if (scope.status && liveStatus(node, live.get(node.id)) !== scope.status) return false;
    if (scope.expiring && expiryLevel(node.billing?.daysLeft) === "neutral") return false;
    // 绑定版本未知时谁都判不出落后：结果为空，调用方说明原因（与「需要处理」卡上的「—」同一含义）。
    if (scope.lagging && (boundAgentVersion === undefined || !olderThan(node.facts?.agentVersion, boundAgentVersion))) return false;
    return true;
  });
}
```

- [ ] **Step 4: 跑测试确认绿**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/lib/nodeFilters.test.ts > /tmp/b8-green.log 2>&1; echo $?` → 0

- [ ] **Step 5: 缺陷注入**

`scopeFromParams` 的 status 不校验（直接 `as NodeStatus`）：第一条用例红在 `status: "foo"`。恢复后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/lib/nodeFilters.ts web/src/lib/nodeFilters.test.ts && git commit -m "feat(web): 节点列表的状态、到期、落后筛选与 URL 参数的解析回写"
```

### Task 9: 节点列表：筛选行、表格、批量条与 ⋯ 菜单

**Files:**
- Modify: `web/src/pages/Nodes.tsx`（筛选行、表头、`NodeRow`、批量条、菜单；状态与 mutation 逻辑不动）
- Modify: `web/src/components/NodeOrderControl.tsx`、`web/src/components/NodeOrderControl.test.tsx`（去掉「移动到…」选项与 `moveDisabled / onMoveTo`，它搬进 ⋯ 菜单）
- Modify: `web/src/pages/Nodes.test.tsx`、`web/src/pages/NodesBatchTags.test.tsx`、`web/src/pages/NodesScale.test.tsx`（只改期望，不改语义；见下表）
- Create: `web/src/pages/NodesFilters.test.tsx`
- Modify: `web/src/admin.css`（删 `.node-filters*`、`.tag-filter*`、`.node-batch-toolbar`、`.status-pill*`、`.node-actions`、`.node-traffic`、`.node-price`；追加 `.batch-toolbar`、`.node-management` 列宽与手机卡片）

**Interfaces:**
- Consumes: `RowMenu`（Task 3）、`PageHeader`（Task 4）、`MultiSelect`、`StatusBadge`、`Missing`、`liveById / liveStatus`（Task 6）、`scopeFromParams / paramsWithScope / applyScope / isScoped / STATUS_OPTIONS`（Task 8）、`expiryLevel`。
- Produces: DOM 契约（后续 Task 10、13 依赖）——
  - 页头：`PageHeader title="节点"`，主按钮 `button.primary-button` 「添加节点」。
  - 筛选行 `div.filter-row[role=group][aria-label=筛选]`：`input[type=search][aria-label=搜索节点]`；`MultiSelect label="标签"`（触发器 `button` 「标签」，选项 `checkbox` 名即标签名，带计数；已选胶囊 `移除 {tag}`）；`checkbox` 「只看没有标签的节点」；`select[aria-label=状态]`（`""` 全部状态 + 四态）；`checkbox` 「只看 30 天内到期」「只看 agent 版本落后」；任一筛选生效时 `button` 「清除筛选」；`span.num` 「{显示数} / {总数}」。
  - 表头列：`th[data-column=select]`（`MixedCheckbox` 「选择当前结果全部节点」）、order（sr-only 「排序」）、name 「节点」、addresses 「IPv4 / IPv6」、status 「状态」、「本周期」、「费用」、「到期」、actions（sr-only 「操作」）。
  - 行 `tr[data-status=online|offline|never|maintenance|unknown]`；名称格：`a[aria-label=withId]`、`NodeCountry`、`span.chip.chip-public` 「公开」（仅公开节点）、`span.badge-attention` 「agent 低于 vX」（仅落后）、`ul.tag-chips[aria-label="标签 {withId}"]`（没有标签时不渲染）、`p.node-note`；状态格：`StatusBadge` 或 `span.muted` 「状态未知」；本周期：`↓ a ↑ b` 或 `Missing`；费用：`priceText || "—"` + 「 · 自动续期」；到期：`span[data-level]`；操作格：`RowMenu label=withId` 项目依次 编辑、查看详情、移动到…、换 token、删除（`confirm: 确认删除 {withId}`）。
  - 批量条：只在已选 > 0 时渲染 `div.batch-toolbar[role=toolbar][aria-label=批量操作]`：「已选择 N 个节点」、`button` 「批量编辑标签」「移动到…」「清除选择」，总数未就绪 / 失败的说明照旧放在条内。
  - `NodeOrderControl` 新签名：`{ label, index, count, position, reorderDisabled, onMove, onDragStart, onDragEnd }`，`select` 「移动 X」只剩 上移一位 / 下移一位 / 置顶 / 置底。

- [ ] **Step 1: 列出要改期望的既有断言**

Run: `cd /Users/xjetry/work/vibe/probe/web && grep -n "按标签过滤 \|清除标签过滤\|name: \`编辑 \|name: \`计费 \|name: \`换 token\|name: \`删除 \|确认删除\|取消删除\|\"move\"\|selectOption\|暂无读数\|仅管理端\|维护中\|状态未知\|\"离线\"\|\"在线\"\|标签 a\|没有标签是\|选择当前结果" src/pages/Nodes.test.tsx src/pages/NodesBatchTags.test.tsx src/pages/NodesScale.test.tsx > /tmp/b9-consumers.log 2>&1; echo $?; wc -l /tmp/b9-consumers.log`

逐条按下表改；grep 出的每一处在 result 里列位置。

| 旧断言 | 新断言 |
|---|---|
| `getByRole("button", { name: \`编辑 ${label}\` })` 点击 | `openRowAction(label, "编辑")`（下方 helper） |
| `getByRole("button", { name: \`计费 ${label}\` })` 点击 | `openRowAction(label, "编辑")`，字段在同一抽屉的「费用」分组（Task 10 完成前此组用例仍红，Task 9 的 result 里标出） |
| `getByRole("button", { name: \`换 token ${label}\` })` | `openRowAction(label, "换 token")` |
| `getByRole("button", { name: \`删除 ${label}\` })` → `button` 「确认删除 …」 | `openRowAction(label, "删除")` → `getByRole("menuitem", { name: \`确认删除 ${label}\` })`；「取消删除 X」→ `menuitem` 「取消」 |
| `combobox` 「移动 X」`selectOption("move")` / `{ target: { value: "move" } }` | `openRowAction(label, "移动到…")` |
| `checkbox` 「按标签过滤 X」 | 先 `fireEvent.click(getByRole("button", { name: /^标签/ }))` 打开，再 `getByRole("checkbox", { name: "X" })`；已选胶囊 `button` 「移除 X」 |
| `button` 「清除标签过滤」 | `button` 「清除筛选」（同时清空搜索、标签、状态筛选） |
| 名称旁「维护中」文字 | 状态列 `StatusBadge` 文字「维护中」（`within(row).getByText("维护中")`） |
| 状态列「离线」（从未上报的节点） | 「从未上报」；有 lastSeenAt 且 online=false 才是「离线」 |
| 「暂无读数」 | `getByLabelText("无读数")`（`Missing`） |
| 「仅管理端」 | 删掉该断言；改断言非公开节点行里 `queryByText("公开")` 为 null |
| 「标签列显示节点的标签，没有标签是 —」 | 有标签：`within(row).getByRole("list", { name: \`标签 ${label}\` })` 列出；无标签：`queryByRole("list", { name })` 为 null |
| 「移动到…」入口在批量条里总是可见 / 「全部节点总数读取失败时入口禁用并说明原因」 | 先勾选一个节点让批量条出现，再断言 `toolbar` 内按钮禁用与说明；行菜单项 `menuitem` 「移动到… X」`aria-disabled="true"` |

在 `Nodes.test.tsx` 顶部加 helper（`NodesBatchTags.test.tsx` 与 `NodesScale.test.tsx` 各自复制一份同名 helper，测试文件之间不互相 import）：

```ts
// 行操作收进 ⋯ 菜单：先开本行菜单，再点同名菜单项。菜单项的可访问名沿用旧按钮的写法「动词 节点名（#id）」。
const openRowAction = (label: string, action: string) => {
  fireEvent.click(screen.getByRole("button", { name: `更多操作 ${label}` }));
  fireEvent.click(screen.getByRole("menuitem", { name: `${action} ${label}` }));
};
```

- [ ] **Step 2: 写新测试（红）**

`web/src/pages/NodesFilters.test.tsx`：

```tsx
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { renderWithAdmin, type AdminImpl } from "../test/harness";
import { Nodes } from "./Nodes";

afterEach(() => vi.useRealTimers());

const nodes = [
  { id: 1n, name: "on", public: true, maintenance: false, tags: ["prod"], trafficResetDay: 1, facts: { agentVersion: "v0.8.0" }, billing: { daysLeft: 12 } },
  { id: 2n, name: "off", public: false, maintenance: false, tags: [], trafficResetDay: 1, facts: { agentVersion: "v0.7.0" } },
  { id: 3n, name: "maint", public: false, maintenance: true, tags: [], trafficResetDay: 1 },
  { id: 4n, name: "fresh", public: false, maintenance: false, tags: [], trafficResetDay: 1 },
];
const snapshot = { now: 1_000n, reportIntervalMs: 4000, hubVersion: "v0.9.0", boundAgentVersion: "v0.8.0",
  nodes: [{ id: 1n, name: "on", online: true, lastSeenAt: 990n, traffic: { periodRx: 1024n, periodTx: 2048n } }, { id: 2n, name: "off", online: false, lastSeenAt: 1n }, { id: 3n, name: "maint", online: true, lastSeenAt: 990n }] };
const impl = { listNodes: async () => ({ nodes }), getSnapshot: async () => snapshot, listTags: async () => ({ tags: [{ name: "prod", nodeCount: 1 }] }) } satisfies AdminImpl;
const render = (path = "/nodes", over: AdminImpl = {}) => renderWithAdmin({ ...impl, ...over }, [{ path: "/nodes", Component: Nodes }], path);
const rowNames = () => screen.getAllByRole("row").slice(1).map((row) => within(row).getByRole("link").textContent);

it("状态列合并快照与维护中：维护中压过在线，不在快照里的节点是状态未知；离线数与总览同一判定", async () => {
  render();
  await screen.findByRole("row", { name: /fresh/ });
  const status = (name: string) => within(screen.getByRole("row", { name: new RegExp(name) })).getAllByRole("cell")[4].textContent;
  expect([status("on"), status("off"), status("maint"), status("fresh")]).toEqual(["在线", "离线", "维护中", "状态未知"]);
  expect(screen.getByRole("row", { name: /fresh/ })).toHaveAttribute("data-status", "unknown");
});

it("URL 里的筛选落到控件并过滤列表；非法值被忽略；控件改动回写 URL 并清空选择", async () => {
  const { router } = render("/nodes?status=offline&expiring=yes");
  await screen.findByRole("row", { name: /off/ });
  expect(rowNames()).toEqual(["off"]);
  expect(screen.getByRole("combobox", { name: "状态" })).toHaveValue("offline");
  expect(screen.getByRole("checkbox", { name: "只看 30 天内到期" })).not.toBeChecked();
  fireEvent.click(screen.getByRole("checkbox", { name: `选择 off（#2）` }));
  expect(screen.getByRole("toolbar", { name: "批量操作" })).toHaveTextContent("已选择 1 个节点");
  fireEvent.change(screen.getByRole("combobox", { name: "状态" }), { target: { value: "" } });
  fireEvent.click(screen.getByRole("checkbox", { name: "只看 30 天内到期" }));
  expect(router.state.location.search).toBe("?expiring=1");
  expect(rowNames()).toEqual(["on"]);
  expect(screen.queryByRole("toolbar", { name: "批量操作" })).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "清除筛选" }));
  expect(router.state.location.search).toBe("");
  expect(rowNames()).toEqual(["on", "off", "maint", "fresh"]);
});

it("落后筛选：绑定版本已知时只列落后节点；未知时结果为空并说明原因", async () => {
  render("/nodes?lagging=1");
  await screen.findByRole("row", { name: /off/ });
  expect(rowNames()).toEqual(["off"]);
  const { unmount } = render("/nodes?lagging=1", { getSnapshot: async () => ({ ...snapshot, boundAgentVersion: "" }) });
  expect(await screen.findByRole("status")).toHaveTextContent("无法取得 hub 绑定的 agent 版本");
  unmount();
});

it("名称格：公开胶囊只在公开节点出现，落后徽章一行，标签列表只在有标签时渲染", async () => {
  render();
  const on = within(await screen.findByRole("row", { name: /^on/ }));
  expect(on.getByText("公开")).toBeInTheDocument();
  expect(on.getByRole("list", { name: "标签 on（#1）" })).toHaveTextContent("prod");
  const off = within(screen.getByRole("row", { name: /off/ }));
  expect(off.queryByText("公开")).not.toBeInTheDocument();
  expect(off.getByText("agent 低于 v0.8.0")).toHaveClass("badge-attention");
  expect(off.queryByRole("list", { name: /^标签/ })).not.toBeInTheDocument();
});

it("批量条只在有选择时出现，表头全选作用于当前结果", async () => {
  render();
  await screen.findByRole("row", { name: /fresh/ });
  expect(screen.queryByRole("toolbar", { name: "批量操作" })).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("checkbox", { name: "选择当前结果全部节点" }));
  expect(screen.getByRole("toolbar", { name: "批量操作" })).toHaveTextContent("已选择 4 个节点");
  fireEvent.click(within(screen.getByRole("toolbar", { name: "批量操作" })).getByRole("button", { name: "清除选择" }));
  expect(screen.queryByRole("toolbar", { name: "批量操作" })).not.toBeInTheDocument();
});

it("行菜单打开且武装了删除时列表轮询刷新：菜单不关、武装保持；节点消失则菜单随行卸载", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  let gone = false;
  const listNodes = vi.fn(async () => ({ nodes: gone ? nodes.slice(1) : nodes }));
  render("/nodes", { listNodes });
  await screen.findByRole("row", { name: /^on/ });
  fireEvent.click(screen.getByRole("button", { name: "更多操作 on（#1）" }));
  fireEvent.click(screen.getByRole("menuitem", { name: "删除 on（#1）" }));
  expect(screen.getByRole("menuitem", { name: "确认删除 on（#1）" })).toBeInTheDocument();
  await act(async () => { await vi.advanceTimersByTimeAsync(10_000 + 100); });
  await waitFor(() => expect(listNodes.mock.calls.length).toBeGreaterThanOrEqual(2));
  expect(screen.getByRole("menuitem", { name: "确认删除 on（#1）" })).toBeInTheDocument();
  gone = true;
  await act(async () => { await vi.advanceTimersByTimeAsync(10_000 + 100); });
  await waitFor(() => expect(screen.queryByRole("row", { name: /^on/ })).not.toBeInTheDocument());
  expect(screen.queryByRole("menu")).not.toBeInTheDocument();
});

it("行菜单「移动到…」作用于单个节点；总数读取失败时该项禁用", async () => {
  render("/nodes", { listNodes: async (req: { tags?: string[] }) => { if (!req.tags || req.tags.length === 0) return { nodes }; return { nodes }; } });
  await screen.findByRole("row", { name: /^on/ });
  fireEvent.click(screen.getByRole("button", { name: "更多操作 on（#1）" }));
  fireEvent.click(screen.getByRole("menuitem", { name: "移动到… on（#1）" }));
  const dialog = await screen.findByRole("dialog");
  expect(dialog).toHaveTextContent("共 4 个节点，将移动其中的 1 个");
});
```

每行给 `aria-label={node.name}`（`tr`），与总览一致，`getByRole("row", { name })` 才稳。

- [ ] **Step 3: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/NodesFilters.test.tsx > /tmp/b9-red.log 2>&1; echo $?` → 1（没有「状态」下拉与批量条）。

- [ ] **Step 4: 实现**

`web/src/components/NodeOrderControl.tsx`：删 `moveDisabled`、`onMoveTo`、`menuLocked`、`<option value="move">` 与 `if (value === "move")` 分支；`select` 的 `disabled={locked}`。注释改为：「排序四项都走 ReorderNodes 的完整排列；按名次移动的『移动到…』是另一种操作，放在行的 ⋯ 菜单里。」`NodeOrderControl.test.tsx` 去掉 `onMoveTo / moveDisabled` 与 `"move"` 相关断言。

`web/src/pages/Nodes.tsx` —— 顶部 import 增删：

```tsx
import { useSearchParams } from "react-router";                // 与 Link 同一行
import { Missing } from "../components/Bar";
import { MultiSelect } from "../components/MultiSelect";
import { PageHeader } from "../components/PageHeader";
import { RowMenu } from "../components/RowMenu";
import { StatusBadge } from "../components/StatusBadge";
import { liveById, liveStatus } from "../lib/adminStatus";
import { applyScope, isScoped, paramsWithScope, scopeFromParams, STATUS_OPTIONS, type ScopeFilters } from "../lib/nodeFilters";
import { expiryLevel } from "../lib/status";
// 删：ConfirmDelete、Icon（页头按钮改用 PageHeader 的 actions，若别处仍用则保留）、filterNodes、NodeInstallModal（Task 10 换成 NodeCredentialsDrawer，本任务先保留 import）
```

组件内（状态与 mutation 之后、`return` 之前）替换这几行：

```tsx
  const [params, setParams] = useSearchParams();
  const scope = scopeFromParams(params);
  // 筛选是 URL 的一部分：「需要处理」的卡链到这里，刷新、分享、后退都要落在同一组筛选上；改筛选清空选择，
  // 否则批量操作会作用于看不见的节点。
  const setScope = (next: ScopeFilters) => { setParams(paramsWithScope(params, next), { replace: true }); setSelected([]); };
  const clearFilters = () => { setSearch(""); setTagFilter({ kind: "tags", names: [] }); setScope({ status: null, expiring: false, lagging: false }); };
  const live = liveById(snapshot.data?.nodes);
  const filtered = search !== "" || tagFilter.kind === "untagged" || tagFilter.names.length > 0 || isScoped(scope);
  // …order 的定义不变…
  const list = applyScope(order.items, live, boundAgentVersion, scope, search);
  const openEditor = (node: Node, opener: HTMLElement) => { if (editing) return; update.reset(); setEditor({ node, opener }); };
```

（`editor` 状态去掉 `mode`：`useState<{ node: Node; opener: HTMLElement } | null>(null)`；`NodeEditor` 在 Task 10 前仍要 `mode`，本任务先传 `mode="general"`。）

JSX 整体替换为：

```tsx
  const tagOptions = [...(tags.data?.tags ?? []).map((tag) => ({ value: tag.name, label: tag.name, count: tag.nodeCount })),
    // 已选却从清单消失的标签仍须可取消，否则用户会困在无法清空的过滤条件里。
    ...(tagFilter.kind === "tags" ? tagFilter.names : []).filter((name) => !(tags.data?.tags ?? []).some((tag) => sameTag(tag.name, name))).map((name) => ({ value: name, label: name }))];
  const untagged = tagFilter.kind === "untagged";
  return <section>
    <PageHeader title="节点" actions={<button type="button" className="primary-button" disabled={editing} onClick={(event) => { lastOpener.current = event.currentTarget; create.reset(); setCreating(event.currentTarget); }}>添加节点</button>} />
    {!editor && errorBanner(nodes.error)}
    {snapshot.error != null && <p role="alert" className="error">{boundAgentVersion === undefined ? "无法取得 hub 绑定的 agent 版本，落后标记不可用" : `刷新失败，落后标记按上次取得的绑定版本 ${boundAgentVersion || "空"} 判断`}；在线状态与流量可能不是最新值：{errorText(snapshot.error)}</p>}
    {secret && <NodeInstallModal secretLabel={secret.label} token={secret.value} hubVersion={hubVersion} boundAgentVersion={boundAgentVersion} error={snapshot.error} reRegister={secret.reRegister} opener={secret.opener} onClose={() => setSecret(null)} />}
    <div className="filter-row" role="group" aria-label="筛选">
      <input type="search" aria-label="搜索节点" placeholder="名称、IP、地区、备注或主机名" value={search} onChange={(event) => { setSearch(event.target.value); setSelected([]); }} />
      <MultiSelect label="标签" searchable options={tagOptions} selected={tagFilter.kind === "tags" ? tagFilter.names : []} onChange={(names) => { setTagFilter({ kind: "tags", names }); setSelected([]); }} />
      {/* "无标签"不依赖 ListTags：清单加载中、为空或失败都照常可勾。它的可访问名称与标签项分开命名——用户可能真的建一个叫"无标签"的标签。 */}
      <label className="check"><input type="checkbox" aria-label="只看没有标签的节点" checked={untagged} onChange={() => { setTagFilter(untagged ? { kind: "tags", names: [] } : { kind: "untagged" }); setSelected([]); }} />无标签</label>
      <select aria-label="状态" value={scope.status ?? ""} onChange={(event) => setScope({ ...scope, status: (event.target.value || null) as ScopeFilters["status"] })}>
        <option value="">全部状态</option>{STATUS_OPTIONS.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
      </select>
      <label className="check"><input type="checkbox" aria-label="只看 30 天内到期" checked={scope.expiring} onChange={() => setScope({ ...scope, expiring: !scope.expiring })} />30 天内到期</label>
      <label className="check"><input type="checkbox" aria-label="只看 agent 版本落后" checked={scope.lagging} onChange={() => setScope({ ...scope, lagging: !scope.lagging })} />agent 版本落后</label>
      {filtered && <button type="button" className="link" onClick={clearFilters}>清除筛选</button>}
      {tags.error != null && <span role="alert" className="error">无法取得标签清单：{errorText(tags.error)}</span>}
      <span className="muted num">{list.length} / {order.items.length}</span>
    </div>
    {!editing && errorBanner(error)}
    {!batchEditor && errorBanner(batchUpdate.error)}
    {!batchEditor && batchUpdate.isPending && <p role="status" className="muted">标签已保存，正在重新读取…</p>}
    {order.error != null && <p role="alert" className="error">排序未完成：{errorText(order.error)}</p>}
    {order.pending && <p role="status" className="muted">正在保存并确认排序…</p>}
    {order.confirmed && <p className="order-saved" aria-live="polite">顺序已保存</p>}
    {order.blocked && <button type="button" onClick={order.recover} disabled={order.pending}>重新读取排序</button>}
    {filtered && <p className="node-subtext">筛选时不能用拖动或上下移（它们保存完整排列）；可用行菜单的「移动到…」按全序名次移动，或清除筛选后再调整。</p>}
    {!filtered && nodes.stale && <p className="node-subtext">列表还不是当前条件下的结果，暂时无法排序。</p>}
    {gate.ready ? <>
      {selectedNodes.length > 0 && <div className="batch-toolbar" role="toolbar" aria-label="批量操作">
        <span>已选择 {selectedNodes.length} 个节点</span>
        <button type="button" disabled={editing || nodes.stale || nodes.error != null || remove.isPending || tags.data === undefined || tags.error != null} onClick={(event) => { batchUpdate.reset(); setBatchEditor({ nodes: selectedNodes, tags: tags.data?.tags ?? [], opener: event.currentTarget }); }}>批量编辑标签</button>
        <button type="button" disabled={moveLocked} onClick={(event) => { moveNodes.reset(); setMoveTarget({ nodes: selectedNodes, opener: event.currentTarget }); }}>移动到…</button>
        {!moveReady && allNodes.error == null && <span className="muted">正在读取节点总数…</span>}
        {allNodes.error != null && <span className="error">无法取得节点总数，「移动到…」不可用：{errorText(allNodes.error)}</span>}
        <button type="button" className="link" disabled={editing} onClick={() => setSelected([])}>清除选择</button>
      </div>}
      <p className="node-subtext order-help" id="node-order-help">拖动手柄调整顺序，松开后自动保存。也可使用移动菜单，或聚焦手柄后按方向键、Home / End。</p>
      {list.length === 0 && (scope.lagging && boundAgentVersion === undefined
        ? <p className="node-empty" role="status">无法取得 hub 绑定的 agent 版本，「agent 版本落后」筛选暂时没有结果。</p>
        : <p className="node-empty" role="status">{narrowed ? "没有匹配的节点。" : "还没有节点，添加节点后安装 agent 即可开始监控。"}</p>)}
      <div className="table-scroll" role="region" aria-label="节点管理" tabIndex={0}>
        <table className="nodes node-management"><thead><tr>
          <th data-column="select"><MixedCheckbox label="选择当前结果全部节点" checked={selectedNodes.length === 0 ? false : selectedNodes.length === list.length ? true : "mixed"} disabled={editing || nodes.stale || list.length === 0} onChange={() => setSelected(selectedNodes.length === list.length ? [] : list.map((node) => node.id))} /></th>
          <th data-column="order"><span className="sr-only">排序</span></th><th data-column="name">节点</th><th data-column="addresses">IPv4 / IPv6</th><th data-column="status">状态</th><th>本周期</th><th>费用</th><th>到期</th><th data-column="actions"><span className="sr-only">操作</span></th>
        </tr></thead>
          <tbody>{list.map((node, index) => {
            const label = withId(node.name, node.id);
            return <NodeRow key={String(node.id)} node={node} live={live.get(node.id)} boundAgentVersion={boundAgentVersion}
              selection={<MixedCheckbox label={`选择 ${label}`} checked={selectedIds.has(node.id)} disabled={editing || nodes.stale} onChange={() => setSelected((current) => current.includes(node.id) ? current.filter((id) => id !== node.id) : [...current, node.id])} />}
              orderClass={dragging === node.id ? "is-dragging" : dragging !== null && drop?.target === node.id ? `drop-${drop.edge}` : undefined}
              onDragOver={(event) => { if (dragging === null || dragging === node.id) return; event.preventDefault(); event.dataTransfer.dropEffect = "move"; setDrop(dropPosition(event, node.id)); }}
              onDrop={(event) => { if (dragging !== null && dragging !== node.id) { event.preventDefault(); moveNode(dragging, dropPosition(event, node.id)); } endDrag(); }}
              orderControl={<NodeOrderControl label={label} index={index} count={list.length} position={narrowed ? node.position : index + 1} reorderDisabled={!sortable}
                onMove={(move) => moveNode(node.id, move)} onDragEnd={endDrag}
                onDragStart={(event) => { if (!sortable) { event.preventDefault(); return; } event.dataTransfer.effectAllowed = "move"; event.dataTransfer.setData("text/plain", String(node.id)); setDrag({ id: node.id, members }); setDrop(null); }} />}
              menu={<RowMenu label={label} items={[
                { label: "编辑", disabled: editing, onSelect: (trigger) => openEditor(node, trigger) },
                { label: "查看详情", to: `/nodes/${node.id}` },
                { label: "移动到…", disabled: moveLocked, onSelect: (trigger) => { moveNodes.reset(); setMoveTarget({ nodes: [node], opener: trigger }); } },
                { label: "换 token", disabled: rotate.isPending || editing, onSelect: (trigger) => { lastOpener.current = trigger; rotate.mutate({ id: node.id }); } },
                { label: "删除", danger: true, confirm: `确认删除 ${label}`, disabled: remove.isPending || editing, onSelect: () => remove.mutate({ id: node.id }) },
              ]} />} />;
          })}</tbody>
        </table>
      </div>
    </> : gate.loading}
    <TagManager tags={tags.data?.tags} pending={removeTag.isPending} onDelete={(name) => removeTag.mutate({ name })} />
    {/* batchEditor / moveTarget / editor / creating 四段原样保留；editor 段去掉 mode（Task 10 前传 mode="general"） */}
  </section>;
```

`NodeRow` 整体替换：

```tsx
function NodeRow({ node, live, boundAgentVersion, selection, orderControl, orderClass, onDragOver, onDrop, menu }: {
  node: Node; live: NodeStatus | undefined; boundAgentVersion?: string; selection: ReactNode; orderControl: ReactNode; menu: ReactNode;
  orderClass?: string; onDragOver: (event: DragEvent<HTMLTableRowElement>) => void; onDrop: (event: DragEvent<HTMLTableRowElement>) => void;
}) {
  const label = withId(node.name, node.id);
  const status = liveStatus(node, live);
  const lagging = boundAgentVersion !== undefined && olderThan(node.facts?.agentVersion, boundAgentVersion);
  return <tr className={orderClass} aria-label={node.name} data-status={status ?? "unknown"} onDragOver={onDragOver} onDrop={onDrop}>
    <td data-column="select">{selection}</td>
    <td data-column="order" data-label="排序">{orderControl}</td>
    <td data-column="name" data-label="节点">
      <div className="node-name-line"><Link to={`/nodes/${node.id}`} aria-label={label}>{node.name}</Link><NodeCountry node={node} />
        {node.public && <span className="chip chip-public">公开</span>}
        {lagging && <span className="badge-attention" title={`低于 hub 绑定的 agent 版本 ${boundAgentVersion}`}>agent 低于 {boundAgentVersion}</span>}</div>
      {node.tags.length > 0 && <ul className="tag-chips" aria-label={`标签 ${label}`}>{node.tags.map((tag) => <li key={tag} className="chip">{tag}</li>)}</ul>}
      {node.note && <p className="node-note muted" title={node.note}>{node.note}</p>}
    </td>
    <td data-column="addresses" data-label="IPv4 / IPv6"><NodeAddresses network={node.facts?.network} /></td>
    <td data-column="status" data-label="状态">{status ? <StatusBadge status={status} /> : <span className="muted">状态未知</span>}</td>
    <td data-column="traffic" data-label="本周期" className="num">{live?.traffic ? `↓ ${bytes(live.traffic.periodRx)} ↑ ${bytes(live.traffic.periodTx)}` : <Missing />}</td>
    <td data-column="billing" data-label="费用"><span className="num">{priceText(node.billing) || "—"}</span>{node.billing?.autoRenew && <span className="muted"> · 自动续期</span>}</td>
    <td data-column="expiry" data-label="到期" className="num"><span data-level={expiryLevel(node.billing?.daysLeft)}>{expiryText(node.billing) || "—"}</span></td>
    <td data-column="actions">{menu}</td>
  </tr>;
}
```

删除旧的 `TagFilter` 函数；`TagManager` 不动。`admin.css` 追加：

```css
/* 节点管理表（pages/Nodes.tsx）：行高 52px（设计 §4.3），名称格两行以内，操作列只有 ⋯。 */
.node-management td { height: 52px; }
.node-management th[data-column="select"], .node-management td[data-column="select"] { width: 36px; padding-right: 0; }
.node-management th[data-column="order"], .node-management td[data-column="order"] { width: 88px; }
.node-management th[data-column="actions"], .node-management td[data-column="actions"] { width: 44px; text-align: right; }
.node-name-line { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
.chip-public { background: var(--card-raised); }
.badge-attention { display: inline-block; padding: 0 6px; border-radius: 999px; font-size: 12px; line-height: 18px; white-space: nowrap; color: var(--status-attention); background: color-mix(in srgb, var(--status-attention) 14%, transparent); }
.node-note { margin: 2px 0 0; font-size: 12px; max-width: 32ch; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.batch-toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; margin: 0 0 12px; padding: 8px 12px; border: 1px solid var(--line); border-radius: var(--radius-card); background: var(--card-raised); }
.filter-row .check { display: inline-flex; align-items: center; gap: 6px; white-space: nowrap; }
@media (max-width: 900px) {
  /* 手机卡片（设计 §4.7）：状态 + 名称 + ⋯ 一行，三个数字（本周期 / 费用 / 到期）一行；地址与排序藏起来。 */
  .admin-shell .table-scroll:has(.node-management) { border: 0; background: none; overflow: visible; }
  .admin-shell table.node-management, .node-management tbody { display: block; background: none; border: 0; }
  .node-management thead { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
  .node-management tr { display: grid; grid-template-columns: auto 1fr auto; gap: 6px 10px; align-items: center; margin-bottom: 8px; padding: 10px 12px; border: 1px solid var(--line); border-radius: var(--radius-card); background: var(--card); }
  .admin-shell table.node-management td { height: auto; padding: 0; border: 0; }
  .node-management td[data-column="status"] { grid-row: 1; grid-column: 1; }
  .node-management td[data-column="name"] { grid-row: 1; grid-column: 2; }
  .node-management td[data-column="actions"] { grid-row: 1; grid-column: 3; }
  .node-management td[data-column="select"] { grid-row: 2; grid-column: 1; }
  .node-management td[data-column="traffic"], .node-management td[data-column="billing"], .node-management td[data-column="expiry"] { grid-row: 2; font-size: 12px; }
  .node-management td[data-column="traffic"] { grid-column: 2; }
  .node-management td[data-column="billing"] { grid-column: 3; }
  .node-management td[data-column="expiry"] { grid-row: 3; grid-column: 2 / -1; }
  .node-management td[data-column="order"], .node-management td[data-column="addresses"] { display: none; }
}
```

（`.node-management td[data-column="addresses"]` 在手机上隐藏会让 e2e 的 `复制 IPv4` 在 375px 下不可见——Task 13 把那两条断言移到 1440px 视口下；手机卡片按设计只留三个数字。）

- [ ] **Step 5: 跑测试确认绿，再跑全量**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/NodesFilters.test.tsx src/pages/Nodes.test.tsx src/pages/NodesBatchTags.test.tsx src/pages/NodesScale.test.tsx src/components/NodeOrderControl.test.tsx > /tmp/b9-green.log 2>&1; echo $?` → 0（「计费」入口那组用例在 Task 10 前允许红，result 里逐条列出标题）。
Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm typecheck > /tmp/b9-tsc.log 2>&1; echo $?` → 0

- [ ] **Step 6: 缺陷注入**

(a) `applyScope` 调用处改回 `filterNodes(order.items, search)`：URL 用例红在 `rowNames()` 期望 `["off"]`。(b) `setScope` 不调 `setSelected([])`：URL 用例红在 `toolbar` 仍在。(c) 批量条去掉 `selectedNodes.length > 0 &&`：批量条用例红在首个 `queryByRole("toolbar")`。各自恢复后绿。

- [ ] **Step 7: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/pages/Nodes.tsx web/src/pages/Nodes.test.tsx web/src/pages/NodesFilters.test.tsx web/src/pages/NodesBatchTags.test.tsx web/src/pages/NodesScale.test.tsx web/src/components/NodeOrderControl.tsx web/src/components/NodeOrderControl.test.tsx web/src/admin.css && git commit -m "feat(web): 节点列表按设计重排：筛选行、52px 表格、按需出现的批量条、行操作收进 ⋯ 菜单"
```

### Task 10: 节点抽屉：编辑四分组、添加、凭据、批量标签

**Files:**
- Modify: `web/src/pages/NodeEditor.tsx`（`Drawer`、四分组、去掉 `mode`）
- Create: `web/src/components/NodeCreateDrawer.tsx`、`web/src/components/NodeCredentialsDrawer.tsx`
- Delete: `web/src/components/NodeInstallModal.tsx`
- Modify: `web/src/pages/BatchNodeTagsEditor.tsx`（`Modal` → `Drawer`）
- Modify: `web/src/pages/Nodes.tsx`（创建态与凭据态改用两个抽屉组件；`editor` 不再有 `mode`）
- Modify: `web/src/pages/Nodes.test.tsx`（「计费」组用例改走编辑抽屉的「费用」分组；「关闭弹窗」→「关闭抽屉」）
- Modify: `web/src/admin.css`（`.form-section` 在抽屉里的间距；删 `.modal-body .form-grid` 两列，抽屉里单列）

**Interfaces:**
- Consumes: `Drawer`（Task 2）、`BillingEditor`、`InstallCommands`、`Secret`、`NodeAddresses detailed`、`NodeCountry / lookupText`。
- Produces:
  ```ts
  export function NodeEditor(props: { node: Node; knownTags: readonly Tag[]; saving: boolean; error: unknown; listError: unknown; onClose: () => void; onSave: (patch: NodePatch) => void; opener: HTMLElement }): JSX.Element;
  //   Drawer 标题「编辑节点 · {withId}」；四个 <section className="form-section" aria-labelledby> 标题依次「基本」「地区」「费用」「运行」；字段的 aria-label 不变（名称 X、备注 X、公开备注 X、公开 X、新标签 X、手动指定国家 / 地区 X、价格 X…、重置日 X、离线宽限期（秒） X、维护 X）。
  export function NodeCreateDrawer(props: { opener: HTMLElement; pending: boolean; error: unknown; onClose: () => void; onCreate: (request: { name: string; billing?: BillingDraft }) => void }): JSX.Element;
  //   标题「添加节点」；字段「新节点名称」（data-autofocus）、BillingEditor label="新节点"（选填）；提交按钮「创建」。
  export function NodeCredentialsDrawer(props: { title: string; secretLabel: string; token: string; hubVersion?: string; boundAgentVersion?: string; error?: unknown; reRegister?: boolean; opener: HTMLElement; onClose: () => void }): JSX.Element;
  //   title 由调用方给：创建后「节点已创建」，换 token 后「节点凭据」；正文与 NodeInstallModal 完全相同（Secret、说明、InstallCommands、「完成」按钮）。
  ```
  `Nodes.tsx` 的 `secret` 状态加 `title: string`；创建成功 `setCreating(null)` 后立刻 `setSecret({ …, title: "节点已创建" })`，两个抽屉前后相继，焦点落在凭据抽屉里，没有多余一次点击（设计 §4.3「填名称后直接给安装命令」）。

- [ ] **Step 1: 改既有测试的期望（红）**

Run: `cd /Users/xjetry/work/vibe/probe/web && grep -n "计费\|关闭弹窗\|计费设置\|添加节点\|节点凭据" src/pages/Nodes.test.tsx > /tmp/b10-consumers.log 2>&1; echo $?`

| 旧 | 新 |
|---|---|
| `openRowAction(label, "计费")`（Task 9 临时写法） | `openRowAction(label, "编辑")`，再在 `within(screen.getByRole("dialog"))` 里找「费用」分组：`within(dialog.getByRole("region", { name: "费用" }))` |
| `it.each(["编辑", "计费"])("%s保存成功但回读失败时保留草稿并说明已经保存")` | 去掉 `each`，只保留编辑一条 |
| 标题「计费设置 · X」 | 不再存在；断言 `heading` 「编辑节点 · X」 |
| `button` 「关闭弹窗」（编辑 / 批量 / 添加 / 凭据抽屉内） | `button` 「关闭抽屉」；「移动节点」仍是弹窗，保持「关闭弹窗」 |
| 创建后 `dialog` 标题「节点凭据」 | 「节点已创建」；换 token 后仍是「节点凭据」 |

新增一条用例到 `Nodes.test.tsx`：

```tsx
it("编辑抽屉四个分组按设计顺序，费用分组与原计费入口同一套字段", async () => {
  renderNodes({ listNodes: async () => ({ nodes: two }), updateNode: async () => ({}) });
  await screen.findByRole("link", { name: "a（#1）" });
  openRowAction("a（#1）", "编辑");
  const dialog = screen.getByRole("dialog");
  expect(dialog).toHaveClass("drawer");
  expect(within(dialog).getAllByRole("region").map((section) => section.getAttribute("aria-label") ?? within(section).getByRole("heading").textContent)).toEqual(["基本", "地区", "费用", "运行"]);
  expect(within(within(dialog).getByRole("region", { name: "费用" })).getByLabelText("价格 a（#1）")).toBeInTheDocument();
  expect(within(within(dialog).getByRole("region", { name: "运行" })).getByLabelText("维护 a（#1）")).toBeInTheDocument();
});
```

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/Nodes.test.tsx > /tmp/b10-red.log 2>&1; echo $?` → 1

- [ ] **Step 3: 实现**

`web/src/pages/NodeEditor.tsx`——签名去掉 `mode`，外层 `Modal` → `Drawer`，正文：

```tsx
  return <Drawer title={`编辑节点 · ${label}`} busy={saving} onClose={onClose} opener={opener}>
    <form onSubmit={submit}>
      <div className="modal-body">
        {errorBanner(error, listError)}
        <fieldset className="bare" disabled={saving}>
          <section className="form-section" aria-label="基本">
            <h3>基本</h3>
            <label>节点名称<input data-autofocus aria-label={`名称 ${label}`} value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} /></label>
            <label className="switch-field"><span>公开显示<small>关闭后仅在管理后台可见</small></span><input type="checkbox" aria-label={`公开 ${label}`} checked={draft.public} onChange={(e) => setDraft({ ...draft, public: e.target.checked })} /></label>
            <label>公开备注<input aria-label={`公开备注 ${label}`} placeholder="对访客可见的一行说明，如线路类型" value={draft.publicRemark} onChange={(e) => setDraft({ ...draft, publicRemark: e.target.value })} /><span className="muted">随公开页展示；至多 100 字、单行，留空不显示。</span></label>
            <label>备注<textarea aria-label={`备注 ${label}`} placeholder="商家、用途或其他内部备注" value={draft.note} onChange={(e) => setDraft({ ...draft, note: e.target.value })} /></label>
            <div><label htmlFor={`tag-input-${node.id}`}>节点标签</label><TagsEditor id={node.id} label={label} isPublic={draft.public} tags={draft.tags} known={knownTags} pending={pendingTag} onPending={setPendingTag} onChange={(tags) => setDraft({ ...draft, tags })} /></div>
          </section>
          <section className="form-section" aria-label="地区">
            <h3>地区</h3>
            {/* 查得值依赖 agent 探测到的出口地址，地址诊断与来源 IP 放在同一组：读者要判断"查得值为什么是这个"时，依据就在旁边。 */}
            <div className="address-diagnostics"><NodeAddresses network={node.facts?.network} detailed /><p className="muted">agent 探测出口，每 5 分钟更新。无可用地址或路由标记为不支持；超时与服务异常标记为探测失败。</p></div>
            <div><span className="field-label">上报来源 IP</span><p>{node.lastSource ? <code>{node.lastSource}</code> : <span className="muted">尚未记录来源</span>}</p><p className="muted">hub 实际观察到的来源，经过反代时依赖可信代理配置；与 agent 探测结果独立。</p></div>
            <label>手动指定国家 / 地区<input aria-label={`手动指定国家 / 地区 ${label}`} aria-describedby={`country-hint-${node.id}`} placeholder="例如 JP，留空自动查询" value={draft.countryPin} onChange={(e) => setDraft({ ...draft, countryPin: e.target.value.toUpperCase() })} /><span className="muted" id={`country-hint-${node.id}`}>两个字母（ISO 3166-1），优先于查得值；留空用查得值：{node.countryLookup ? lookupText(node) : "尚无查得值"}。</span></label>
            <p className="muted"><NodeCountry node={node} /> · 创建于 {new Date(Number(node.createdAt) * 1000).toLocaleDateString()}</p>
          </section>
          <section className="form-section" aria-label="费用">
            <h3>费用</h3>
            <BillingEditor label={label} draft={draft.billing} onChange={(patch) => setDraft({ ...draft, billing: { ...draft.billing, ...patch } })} />
          </section>
          <section className="form-section" aria-label="运行">
            <h3>运行</h3>
            <label>每月流量重置日<input type="number" min={1} max={28} aria-label={`重置日 ${label}`} value={draft.trafficResetDay} onChange={(e) => setDraft({ ...draft, trafficResetDay: Number(e.target.value) })} /><span className="muted">若从本周期起点算起新的重置日已经过去，本周期用量会立即清零。</span></label>
            <label>离线宽限期（秒）<input type="number" min={0} aria-label={`离线宽限期（秒） ${label}`} aria-describedby={`grace-hint-${node.id}`} value={draft.offlineGraceS} onChange={(e) => setDraft({ ...draft, offlineGraceS: e.target.value })} /><span className="muted" id={`grace-hint-${node.id}`}>0 表示取 hub 的 HERON_OFFLINE_AFTER；非 0 不能小于它。</span></label>
            <label className="switch-field"><span>维护中<small>告警事件照常记录但不投递，在线状态照实显示；到期的提醒不受影响</small></span><input type="checkbox" aria-label={`维护 ${label}`} checked={draft.maintenance} onChange={(e) => setDraft({ ...draft, maintenance: e.target.checked })} /></label>
          </section>
        </fieldset>
      </div>
      <footer className="modal-footer"><button type="button" onClick={onClose} disabled={saving}>取消</button><button type="submit" className="primary-button" disabled={saving || !valid} aria-busy={saving}>保存</button></footer>
    </form>
  </Drawer>;
```

（`<section aria-label>` 让 `getByRole("region", { name })` 可用；`draftOf / NodePatch / TagsEditor / valid / submit` 不变。原 `mode === "billing"` 时 `BillingEditor autoFocus` 的用法删掉。）

`web/src/components/NodeCredentialsDrawer.tsx`：把 `NodeInstallModal.tsx` 的内容搬来，`Modal` → `Drawer`，`title` 由 props 给，文件头注释保留原文（明文只由创建或换发响应提供…）。删除 `NodeInstallModal.tsx`，`grep -rn NodeInstallModal web/src` 必须为空。

`web/src/components/NodeCreateDrawer.tsx`：

```tsx
import { type FormEvent, useState } from "react";
import { errorBanner } from "../api/queryGate";
import { BillingEditor, type BillingDraft, billingDraftSet, emptyBillingDraft } from "./BillingEditor";
import { Drawer } from "./Modal";

// 添加节点只要名称；计费选填，没填时请求不带 billing（hub 把缺席当"未设置"，空对象会被当成"全空的计费"）。
// 草稿属于抽屉：关闭即丢弃，重新打开从空开始。
export function NodeCreateDrawer({ opener, pending, error, onClose, onCreate }: {
  opener: HTMLElement; pending: boolean; error: unknown; onClose: () => void; onCreate: (request: { name: string; billing?: BillingDraft }) => void;
}) {
  const [name, setName] = useState("");
  const [billing, setBilling] = useState(emptyBillingDraft);
  const submit = (event: FormEvent) => { event.preventDefault(); if (name.trim() && !pending) onCreate(billingDraftSet(billing) ? { name, billing } : { name }); };
  return <Drawer title="添加节点" description="创建后在这里直接给出安装命令；token 仅用于注册 agent，不能上报指标。计费可留空，稍后在编辑里补。" busy={pending} opener={opener} onClose={onClose}>
    <form onSubmit={submit}>
      <div className="modal-body">{errorBanner(error)}
        <label>新节点名称<input data-autofocus value={name} onChange={(event) => setName(event.target.value)} placeholder="例如 tokyo-01" disabled={pending} /></label>
        <section className="form-section" aria-label="计费（选填）"><h3>计费（选填）</h3><BillingEditor label="新节点" draft={billing} onChange={(patch) => setBilling({ ...billing, ...patch })} /></section>
      </div>
      <footer className="modal-footer"><button type="button" disabled={pending} onClick={onClose}>取消</button><button type="submit" className="primary-button" disabled={pending || name.trim() === ""}>创建</button></footer>
    </form>
  </Drawer>;
}
```

（`BillingDraft` 若 `BillingEditor.tsx` 未导出，则 `export type BillingDraft = ReturnType<typeof emptyBillingDraft>` 加到那里。）`Nodes.tsx`：删 `name / billing` 两个 state 与 `onCreate`，`creating && <NodeCreateDrawer opener={creating} pending={create.isPending} error={create.error} onClose={() => setCreating(null)} onCreate={(request) => create.mutate(request)} />`；`create.onSuccess` 里 `setSecret({ …, title: "节点已创建" })`，`rotate.onSuccess` 里 `title: "节点凭据"`；`secret && <NodeCredentialsDrawer title={secret.title} … />`。

`web/src/pages/BatchNodeTagsEditor.tsx`：`Modal` → `Drawer`，其余不变。

- [ ] **Step 4: 跑测试确认绿，再跑全量**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/Nodes.test.tsx src/pages/NodesBatchTags.test.tsx src/pages/NodesScale.test.tsx > /tmp/b10-green.log 2>&1; echo $?` → 0；全量 `pnpm vitest run` → 0；`pnpm typecheck` → 0。

- [ ] **Step 5: 缺陷注入**

(a) 「费用」分组改名「计费」：新用例红在分组名数组。(b) `NodeCreateDrawer` 的 `billingDraftSet(billing) ? … : { name }` 改为总是带 `billing`：既有用例「添加节点可选填计费，没填时请求不带 billing」红。各自恢复后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add -A web/src/pages/NodeEditor.tsx web/src/components/NodeCreateDrawer.tsx web/src/components/NodeCredentialsDrawer.tsx web/src/components/NodeInstallModal.tsx web/src/pages/BatchNodeTagsEditor.tsx web/src/pages/Nodes.tsx web/src/pages/Nodes.test.tsx web/src/components/BillingEditor.tsx web/src/admin.css && git commit -m "feat(web): 节点的编辑、添加、凭据与批量标签改为右侧抽屉，编辑分基本 / 地区 / 费用 / 运行四组"
```

### Task 11: 抽出 EventFeed，事件页 DOM 不变

**Files:**
- Create: `web/src/components/EventFeed.tsx`
- Modify: `web/src/pages/AlertEvents.tsx`（只剩节点筛选与查询编排）
- Test: `web/src/pages/AlertEvents.test.tsx` 全文不改，必须仍绿

**Interfaces:**
- Produces:
  ```ts
  export const EVENT_PAGE = 100;   // 与 hub 默认页长一致；不足一页即已到最早的事件
  export function useAlertEvents(nodeId: bigint | null): UseInfiniteQueryResult<InfiniteData<ListAlertEventsResponse>, ConnectError>;  // null → skipToken
  export function EventFeed(props: { events: ReturnType<typeof useAlertEvents>; nodeName: (id: bigint) => string; channelName: (id: bigint) => string }): JSX.Element;
  //   渲染 queryGate(events) 的 loading / 表格（region「告警事件」、列 时间 节点 变化 摘要 通知）/「加载更早的事件」/「没有告警事件。」；DeliveryItem 一并搬入。错误横幅由调用方汇总（同文去重要求所有查询错误进同一次 errorBanner）。
  ```

- [ ] **Step 1: 搬代码**

把 `AlertEvents.tsx` 里的 `PAGE`、`EventList`、`DeliveryItem` 搬到 `components/EventFeed.tsx`；`useAlertEvents`：

```ts
export function useAlertEvents(nodeId: bigint | null) {
  return useInfiniteQuery(AdminService.method.listAlertEvents, nodeId === null ? skipToken : { nodeId, beforeId: 0n, limit: EVENT_PAGE }, {
    pageParamKey: "beforeId",
    // 事件按 id 倒序；下一页从本页最小 id 之前开始。
    getNextPageParam: (last) => (last.events.length < EVENT_PAGE ? undefined : last.events[last.events.length - 1].id),
  });
}
export function EventFeed({ events, nodeName, channelName }: { … }) {
  const region = queryGate(events);
  if (!region.ready) return <>{region.loading}</>;
  return <EventList data={region.data} nodeName={nodeName} channelName={channelName} hasNextPage={events.hasNextPage} fetchingNext={events.isFetchingNextPage} onMore={() => void events.fetchNextPage()} />;
}
```

`AlertEvents.tsx`：`const events = useAlertEvents(valid ? nodeId : null);` … `<EventFeed events={events} nodeName={nodeName} channelName={channelName} />`；`errorBanner(nodes.error, events.error, channels.error)` 原位保留。

- [ ] **Step 2: 跑测试确认绿**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/AlertEvents.test.tsx > /tmp/b11.log 2>&1; echo $?` → 0；`pnpm typecheck` → 0。

- [ ] **Step 3: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/components/EventFeed.tsx web/src/pages/AlertEvents.tsx && git commit -m "refactor(web): 告警事件列表抽成 EventFeed，节点详情的事件 tab 复用它"
```

### Task 12: 节点详情：状态头、编辑入口、四个 tab

**Files:**
- Create: `web/src/components/NowGrid.tsx`
- Modify: `web/src/public/NodePage.tsx`（六格换成 `NowGrid`；`Now` 删掉）、`web/src/public/public.css`（`.now-grid*` 搬到 `styles.css`）
- Modify: `web/src/components/History.tsx`（`MetricCharts` 的覆盖率说明进 `InfoTip`；删 `HistoryCharts`、`ProbePanels`）、`web/src/components/History.test.tsx`
- Modify: `web/src/pages/NodeDetail.tsx`（重写）、`web/src/pages/NodeDetail.test.tsx`
- Modify: `web/src/styles.css`（`.now-grid*`、`.tabs*`、`.node-head-meta`）

**Interfaces:**
- Consumes: `NodeEditor`（Task 10）、`EventFeed / useAlertEvents`（Task 11）、`liveStatus`（Task 6）、`StatusBadge`、`CountryBadge`、`InfoTip`、`MetricCharts / ProbeTaskCharts / RangePicker / useHistory`、`AgentDiagnostics`、`ExecutionScope`、`NodeAddresses`。
- Produces:
  ```ts
  // NowGrid.tsx：只 import type（擦除后不进模块图），公开包与管理包都能用。
  export type NowMetrics = { cpuPct?: number; memUsed?: bigint; memTotal?: bigint; diskUsed?: bigint; diskTotal?: bigint; netRxBps?: bigint; netTxBps?: bigint; uptimeS?: bigint };
  export function NowGrid(props: { metrics: NowMetrics | undefined; daysLeft: number | undefined }): JSX.Element;   // dl.now-grid，六个 group：CPU 内存 磁盘 网络 运行时长 剩余天数（DOM 与原 NodePage 内联写法逐字相同）
  // History.tsx
  export function MetricCharts(props: { history: HistoryState; showCoverage?: boolean }): JSX.Element;  // showCoverage 时在网格上方渲染 <InfoTip label="上报覆盖率">…原文案…</InfoTip> 而不是整段 <p>
  // NodeDetail.tsx
  export type DetailTab = "overview" | "traffic" | "diagnostics" | "events";
  export const TRAFFIC_MS = 10_000;
  ```
  DOM：`header.node-head`（`h1` 节点名、`CountryBadge`、`StatusBadge detail="最近上报 …"`、`p.node-head-meta.num` 「IPv4 · IPv6 · 主机名 · 内核 · agent 版本」+ `.badge-attention`、右侧 `button` 「编辑」）→ `NowGrid` → `div.tabs[role=tablist][aria-label=详情]` 四个 `button[role=tab][aria-selected][aria-controls]`「概览」「流量校正」「Agent 诊断」「告警事件」→ `section[role=tabpanel][aria-labelledby]`。tab 状态在 `?tab=`（非法值当 overview），切换 `replace`。概览 = `RangePicker` + `MetricCharts showCoverage` + `ProbeTaskCharts titleLink → /probes/:id/compare`；流量校正 = `TrafficCard`（原样）；Agent 诊断 = `dl.card.facts`（主机名 + 来源、双栈出口 detailed、系统、内核、架构、CPU、虚拟化、agent、ICMP）+ `ExecutionScope` + `AgentDiagnostics`；告警事件 = `EventFeed`。`useHistory` 的 `enabled` 与 tab 无关（切回概览不重发：react-query 缓存），`listNodes` 10s 轮询不变。

- [ ] **Step 1: 改既有测试的期望并加新用例（红）**

`NodeDetail.test.tsx`：

| 旧 | 新 |
|---|---|
| 「头部链接到该节点的告警事件」`link` → `/events?node=7` | 改名「告警事件 tab 内嵌该节点的事件列表」：`listAlertEvents: vi.fn(async () => ({ events: [] }))`，点 `tab` 「告警事件」后 `expect(listAlertEvents).toHaveBeenCalledWith(expect.objectContaining({ nodeId: 7n }), expect.anything())`，`findByText("没有告警事件。")`；点之前 `listAlertEvents` 未被调用 |
| 「同窗口同名任务在两张探测图中带编号区分」「探测图每个任务一条线，已删除任务用编号」「切窗请求挂起时保留探测图与图例」中对两张面板（「探测 · RTT 均值」「丢包率」）的断言 | 改为每任务一张图：`heading` 为任务标题（同名带编号 `withId`），`data-testid=chart` 的数量 = 任务数；删掉任务的标题是「任务 #id」；标题链接 `href="/probes/5/compare"`（照 `public/NodePage.test.tsx:19` 的写法） |
| 「主机信息显示 ICMP…」「主机名一格带上…来源地址」「旧 Agent 的节点详情明确显示未提供诊断」「诊断每 10 秒更新…」 | 先 `fireEvent.click(await screen.findByRole("tab", { name: "Agent 诊断" }))` 再断言；其余不变 |
| 「上报覆盖率」段落 `getByText(/上报覆盖/)` | 仍 `getByText`（InfoTip 正文在 DOM 里）；另断 `getByRole("button", { name: "上报覆盖率" })` 存在 |

新增：

```tsx
it("状态头：四态徽章带最近上报，元信息一行，落后徽章只在落后时出现", async () => {
  renderWithAdmin({ ...defaultImpl, getSnapshot: async () => ({ now: 1_000n, reportIntervalMs: 4000, hubVersion: "v0.9.0", boundAgentVersion: "v0.8.0", nodes: [{ id: 7n, name: "db-01", online: true, lastSeenAt: 990n, metrics: { cpuPct: 42 } }] }),
    listNodes: async () => ({ nodes: [{ ...(await listNodes()).nodes[0], facts: { ...(await listNodes()).nodes[0].facts, agentVersion: "v0.7.0", network: { ipv4: { state: 1, address: "8.8.8.8" } } } }] }) },
    [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
  const head = within(await screen.findByRole("banner"));
  expect(head.getByText("在线 · 最近上报 10 秒前")).toBeInTheDocument();
  expect(head.getByText(/8\.8\.8\.8 · — · db-01\.internal · 6\.1 · v0\.7\.0/)).toBeInTheDocument();
  expect(head.getByText("agent 低于 v0.8.0")).toHaveClass("badge-attention");
  expect(screen.getByRole("group", { name: "CPU" })).toHaveTextContent("42%");
  fireEvent.click(head.getByRole("button", { name: "编辑" }));
  expect(await screen.findByRole("heading", { name: "编辑节点 · db-01（#7）" })).toBeInTheDocument();
});

it("tab 跟随 ?tab=，非法值回到概览；切到诊断再切回不重发历史查询", async () => {
  const queryMetrics = vi.fn(defaultImpl.queryMetrics);
  const { router } = renderWithAdmin({ ...defaultImpl, queryMetrics }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7?tab=bogus");
  expect(await screen.findByRole("tab", { name: "概览" })).toHaveAttribute("aria-selected", "true");
  await waitFor(() => expect(queryMetrics).toHaveBeenCalledTimes(1));
  fireEvent.click(screen.getByRole("tab", { name: "Agent 诊断" }));
  expect(router.state.location.search).toBe("?tab=diagnostics");
  expect(screen.getByRole("region", { name: "Agent 运行诊断" })).toBeInTheDocument();
  fireEvent.click(screen.getByRole("tab", { name: "概览" }));
  expect(router.state.location.search).toBe("");
  expect(queryMetrics).toHaveBeenCalledTimes(1);
  renderWithAdmin(defaultImpl, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7?tab=diagnostics");
  expect((await screen.findAllByRole("tab", { name: "Agent 诊断" })).at(-1)).toHaveAttribute("aria-selected", "true");
});
```

（`header.node-head` 用 `<header>` 且是 `section` 的直接子元素时没有 `banner` 角色——给它 `role="banner"` 不合语义；改为 `aria-label="节点状态"` 并用 `getByRole("group", { name: "节点状态" })`：`<header className="node-head" role="group" aria-label="节点状态">`。上面用例的 `findByRole("banner")` 据此写成 `findByRole("group", { name: "节点状态" })`。）

`History.test.tsx` 的 COVERAGE_TEXT 用例：段落断言改成 `getByRole("button", { name: "上报覆盖率" })` + `getByText(COVERAGE_TEXT)`。`public/NodePage.test.tsx` 不改（DOM 相同）。

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/NodeDetail.test.tsx src/components/History.test.tsx > /tmp/b12-red.log 2>&1; echo $?` → 1

- [ ] **Step 3: 实现**

`web/src/components/NowGrid.tsx`：

```tsx
import type { ReactNode } from "react";
import { Missing, ratio } from "./Bar";
import { bytes, duration, percent } from "../lib/format";
import { expiryLevel } from "../lib/status";

export type NowMetrics = { cpuPct?: number; memUsed?: bigint; memTotal?: bigint; diskUsed?: bigint; diskTotal?: bigint; netRxBps?: bigint; netTxBps?: bigint; uptimeS?: bigint };

function Now({ label, children }: { label: string; children: ReactNode }) {
  return <div className="now-cell" role="group" aria-label={label}><dt>{label}</dt><dd className="num">{children}</dd></div>;
}

// 节点页首屏六个现值格（设计 §3.2 / §4.3）：公开页与管理详情同一份。入参是结构类型，两端的快照条目都满足，
// 本文件不 import 任何生成代码，公开包的模块图里不会因此多出管理服务。
export function NowGrid({ metrics: m, daysLeft }: { metrics: NowMetrics | undefined; daysLeft: number | undefined }) {
  return (
    <dl className="now-grid">
      <Now label="CPU">{m?.cpuPct !== undefined ? percent(m.cpuPct) : <Missing />}</Now>
      <Now label="内存">{m?.memUsed !== undefined && m.memTotal ? percent(ratio(m.memUsed, m.memTotal)) : <Missing />}</Now>
      <Now label="磁盘">{m?.diskUsed !== undefined && m.diskTotal ? percent(ratio(m.diskUsed, m.diskTotal)) : <Missing />}</Now>
      <Now label="网络">{m?.netRxBps !== undefined && m.netTxBps !== undefined ? `↓ ${bytes(m.netRxBps)}/s ↑ ${bytes(m.netTxBps)}/s` : <Missing />}</Now>
      <Now label="运行时长">{m?.uptimeS !== undefined ? duration(m.uptimeS) : <Missing />}</Now>
      <Now label="剩余天数">{daysLeft !== undefined ? <span data-level={expiryLevel(daysLeft)}>{daysLeft < 0 ? `已过期 ${-daysLeft} 天` : `${daysLeft} 天`}</span> : <Missing />}</Now>
    </dl>
  );
}
```

`public/NodePage.tsx`：删 `Now` 与 `<dl className="now-grid">…</dl>`，换 `<NowGrid metrics={m} daysLeft={daysLeft} />`；`.now-grid*` 规则从 `public.css` 剪到 `styles.css`（原样）。

`History.tsx`：`MetricCharts` 的 `{showCoverage && coverage.kind !== "absent" && <p className="muted">…</p>}` 改为

```tsx
      {showCoverage && coverage.kind !== "absent" && (
        <p className="muted coverage-note"><InfoTip label="上报覆盖率">
          {coverage.kind === "no-start" && "尚无覆盖记录"}{coverage.kind === "no-observed" && "无可观测区间"}
          {coverage.kind === "rate" && <>上报覆盖 {coverage.percent}%{coverage.unknown && <>，未知 {coverage.unknown}</>}</>}
          。这是 hub 观测到的分钟里节点有上报的比例，不是在线率；hub 未运行、超出保留期等无法观测的时段计为未知。
        </InfoTip></p>
      )}
```

删 `ProbePanels`、`HistoryCharts`（`grep -rn "HistoryCharts\|ProbePanels" web/src` 必须为空）。

`pages/NodeDetail.tsx` 重写（`TrafficCard` 原样保留）：

```tsx
import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
import { errorText } from "../api/auth";
import { AgentDiagnostics } from "../components/AgentDiagnostics";
import { CountryBadge } from "../components/CountryBadge";
import { EventFeed, useAlertEvents } from "../components/EventFeed";
import { ExecutionScope } from "../components/ExecutionScope";
import { MetricCharts, ProbeTaskCharts, RangePicker, useHistory, type HistoryMethods } from "../components/History";
import { NodeAddresses, addressText } from "../components/NodeAddresses";
import { NowGrid } from "../components/NowGrid";
import { StatusBadge } from "../components/StatusBadge";
import { AdminService, type GetTrafficResponse, type Node } from "../gen/heron/v1/admin_pb";
import { liveStatus } from "../lib/adminStatus";
import { ago, bytes } from "../lib/format";
import { withId } from "../lib/ids";
import { POLL_MS } from "../lib/poll";
import { olderThan } from "../lib/version";
import { NodeEditor } from "./NodeEditor";

const ADMIN_HISTORY: HistoryMethods = { queryMetrics: AdminService.method.queryMetrics, queryProbes: AdminService.method.queryProbes };
export const TRAFFIC_MS = 10_000;
export type DetailTab = "overview" | "traffic" | "diagnostics" | "events";
const TABS: readonly { id: DetailTab; label: string }[] = [{ id: "overview", label: "概览" }, { id: "traffic", label: "流量校正" }, { id: "diagnostics", label: "Agent 诊断" }, { id: "events", label: "告警事件" }];
// URL 是外部输入：不认识的 tab 当概览，不显示一个空面板。
const tabOf = (raw: string | null): DetailTab => (TABS.some((t) => t.id === raw) ? (raw as DetailTab) : "overview");

export function NodeDetail() {
  const { id } = useParams();
  const [params, setParams] = useSearchParams();
  const tab = tabOf(params.get("tab"));
  const validId = /^\d+$/.test(id ?? "");
  const nodeId = validId ? BigInt(id!) : 0n;
  const qc = useQueryClient();
  const nodes = useQuery(AdminService.method.listNodes, {}, { enabled: validId, refetchInterval: TRAFFIC_MS });
  const snap = useQuery(AdminService.method.getSnapshot, {}, { enabled: validId, refetchInterval: POLL_MS });
  const tags = useQuery(AdminService.method.listTags, {}, { enabled: validId });
  const channels = useQuery(AdminService.method.listNotifyChannels, {}, { enabled: validId && tab === "events" });
  // 历史与流量只依赖 URL 里的 id，不依赖 listNodes（它挂起或首次失败时它们可能已就绪）；是否"不存在"只有 listNodes 到达后才能判断。
  const history = useHistory(ADMIN_HISTORY, nodeId, validId);
  const traffic = useQuery(AdminService.method.getTraffic, {}, { enabled: validId, refetchInterval: TRAFFIC_MS });
  // 事件 tab 才取事件：它是低频内容，不该随每次打开详情多发一条请求。
  const events = useAlertEvents(validId && tab === "events" ? nodeId : null);
  const [editor, setEditor] = useState<HTMLElement | null>(null);
  const update = useMutation(AdminService.method.updateNode, {
    onSuccess: async () => {
      try { await qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listNodes, cardinality: "finite" }) }, { throwOnError: true }); }
      catch (error) { throw new Error(`已保存，但回读失败：${errorText(error)}`); }
    },
  });
  if (!validId) return <p role="alert" className="error">节点 {id} 不存在。<Link to="/">返回总览</Link></p>;
  const gate = queryGate(nodes);
  const node = gate.ready ? gate.data.nodes.find((n) => n.id === nodeId) : undefined;
  if (gate.ready && !node) return <p role="alert" className="error">节点 {id} 不存在。<Link to="/">返回总览</Link></p>;
  const live = snap.data?.nodes.find((n) => n.id === nodeId);
  const bound = snap.data?.boundAgentVersion;
  const status = node ? liveStatus(node, live) : undefined;
  const select = (next: DetailTab) => { const p = new URLSearchParams(params); if (next === "overview") p.delete("tab"); else p.set("tab", next); setParams(p, { replace: true }); };
  return (
    <section className="node-detail">
      {errorBanner(nodes.error, snap.error, history.metrics.error, history.probes.error, traffic.error, tab === "events" ? events.error : undefined, tab === "events" ? channels.error : undefined)}
      <header className="node-head" role="group" aria-label="节点状态">
        <div className="node-head-title">
          <h1>{node ? node.name : `节点 #${nodeId}`}</h1>
          {node?.country && <CountryBadge code={node.country} />}
          {status && <StatusBadge status={status} detail={live?.lastSeenAt !== undefined && snap.data ? `最近上报 ${ago(live.lastSeenAt, Number(snap.data.now))}` : undefined} />}
          {node && <button type="button" className="node-head-edit" disabled={editor !== null} onClick={(event) => { update.reset(); setEditor(event.currentTarget); }}>编辑</button>}
        </div>
        {node?.facts && <p className="node-head-meta num">
          {[addressText(node.facts.network?.ipv4), addressText(node.facts.network?.ipv6), node.facts.hostname || "—", node.facts.kernel || "—", node.facts.agentVersion || "—"].join(" · ")}
          {bound !== undefined && olderThan(node.facts.agentVersion, bound) && <> <span className="badge-attention" title={`低于 hub 绑定的 agent 版本 ${bound}`}>agent 低于 {bound}</span></>}
        </p>}
        <NowGrid metrics={live?.metrics} daysLeft={node?.billing?.daysLeft} />
      </header>
      <div className="tabs" role="tablist" aria-label="详情">
        {TABS.map((t) => <button key={t.id} id={`tab-${t.id}`} type="button" role="tab" aria-selected={tab === t.id} aria-controls={`panel-${t.id}`} onClick={() => select(t.id)}>{t.label}</button>)}
      </div>
      <section id={`panel-${tab}`} role="tabpanel" aria-labelledby={`tab-${tab}`}>
        {tab === "overview" && <>
          <header className="row detail-header"><RangePicker history={history} /></header>
          <MetricCharts history={history} showCoverage />
          <ProbeTaskCharts history={history} noProbes={<p className="muted">窗口内没有探测结果。<Link to="/probes">管理探测任务</Link></p>} titleLink={(taskId, title) => <Link to={`/probes/${taskId}/compare`}>{title}</Link>} />
        </>}
        {tab === "traffic" && <TrafficCard nodeId={nodeId} data={traffic.data} />}
        {tab === "diagnostics" && (gate.ready && node ? <>
          {node.facts && <dl className="card facts">
            {/* 来源地址是 hub 在上报上看到的对端，不是 agent 自报；只在管理端显示，公开页没有这个字段。 */}
            <dt>主机名</dt><dd>{node.facts.hostname}{node.lastSource && <span className="muted">（来源 {node.lastSource}）</span>}</dd>
            <dt>双栈出口</dt><dd><NodeAddresses network={node.facts.network} detailed /></dd>
            <dt>系统</dt><dd>{node.facts.os}</dd><dt>内核</dt><dd>{node.facts.kernel}</dd><dt>架构</dt><dd>{node.facts.arch}</dd>
            <dt>CPU</dt><dd>{node.facts.cpuModel} × {node.facts.cpuCores}</dd><dt>虚拟化</dt><dd>{node.facts.virtualization || "无 / 未知"}</dd>
            <dt>agent</dt><dd>{node.facts.agentVersion}</dd><dt>ICMP 探测</dt><dd>{node.facts.icmpAvailable ? "可用" : "不可用"}</dd>
          </dl>}
          <ExecutionScope execution={node.facts?.execution} updatedAt={node.factsUpdatedAt} />
          <AgentDiagnostics diagnostics={node.facts?.diagnostics} updatedAt={node.factsUpdatedAt} />
        </> : gate.loading)}
        {tab === "events" && node && <EventFeed events={events} nodeName={() => node.name} channelName={(cid) => channels.data?.channels.find((c) => c.id === cid)?.name ?? `渠道 #${cid}`} />}
      </section>
      {editor && node && <NodeEditor key={String(node.id)} node={node} opener={editor} knownTags={tags.data?.tags ?? []} saving={update.isPending} error={update.error} listError={nodes.error}
        onClose={() => setEditor(null)} onSave={(patch) => update.mutate({ id: node.id, ...patch }, { onSuccess: () => setEditor(null) })} />}
    </section>
  );
}
```

`addressText(detection)`：若 `NodeAddresses.tsx` 没有导出这样的纯函数，加一个：`export const addressText = (d: AddressDetection | undefined): string => d?.state === AddressDetectionState.AVAILABLE ? d.address : d?.state === AddressDetectionState.FAILED ? "探测失败" : d?.state === AddressDetectionState.UNSUPPORTED ? "不支持" : "—";`（与 `NodeAddresses` 已有的状态文案一致，若那里已有同义函数就复用，不写第二份）。

`styles.css` 追加：

```css
/* 页内 tab（节点详情；设计 §4.3）：下划线式，选中项用前景色 + 2px 底线；面板只渲染当前项。 */
.tabs { display: flex; gap: 4px; margin: 16px 0 12px; border-bottom: 1px solid var(--line); }
.tabs [role="tab"] { height: 36px; padding: 0 12px; border: 0; border-bottom: 2px solid transparent; border-radius: 0; background: none; color: var(--muted); }
.tabs [role="tab"][aria-selected="true"] { color: var(--fg); border-bottom-color: var(--fg); }
.node-head-meta { margin: 4px 0 12px; font-size: 13px; color: var(--muted); }
.node-head-title .node-head-edit { margin-left: auto; }
```

- [ ] **Step 4: 跑测试确认绿，再跑全量**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/NodeDetail.test.tsx src/components/History.test.tsx src/public/NodePage.test.tsx src/public/importScan.test.ts > /tmp/b12-green.log 2>&1; echo $?` → 0；全量 `pnpm vitest run` → 0；`pnpm typecheck` → 0。

- [ ] **Step 5: 缺陷注入**

(a) `tabOf` 直接 `raw as DetailTab`：tab 用例红在 `?tab=bogus` 下「概览」不是选中项。(b) `useHistory` 的 `enabled` 改成 `validId && tab === "overview"`：tab 用例红在 `queryMetrics` 被调两次。(c) `NowGrid` 里内存格去掉 `m.memTotal &&`：`public/NodePage.test.tsx` 「从未上报…六格都是破折号」仍绿（undefined 短路），改注入「内存」格固定输出 `percent(0)`：红在破折号用例。各自恢复后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/components/NowGrid.tsx web/src/public/NodePage.tsx web/src/public/public.css web/src/styles.css web/src/components/History.tsx web/src/components/History.test.tsx web/src/components/NodeAddresses.tsx web/src/pages/NodeDetail.tsx web/src/pages/NodeDetail.test.tsx && git commit -m "feat(web): 节点详情与公开节点页同结构：状态头、六格、编辑抽屉，低频内容进四个 tab"
```

---

## 验收

### Task 13: e2e 跟上新 DOM 并截图

**Files:**
- Modify: `web/e2e/admin-ui.spec.ts`、`web/e2e/agent-diagnostics.spec.ts`

e2e 需要真实 hub：`cd /Users/xjetry/work/vibe/probe && make web-e2e > /tmp/b13-e2e.log 2>&1; echo $?`（端口 18987 / 18988，一次只能跑一份）。

- [ ] **Step 1: 改 `admin-ui.spec.ts`**

| 行 | 旧 | 新 |
|---|---|---|
| 65, 133, 154 | `getByLabel('后台配色').selectOption('dark' / 'light')` | `button` 「明暗切换」：从跟随系统出发点一次=浅色、两次=深色。写一个 `const setScheme = async (page, want: 'light' \| 'dark') => { for (let i = 0; i < 3; i++) { if ((await page.evaluate(() => document.documentElement.dataset.theme)) === want) return; await page.getByRole('button', { name: '明暗切换' }).click(); } throw new Error('scheme not reached'); }`（`data-theme` 是 `forcedTheme` 写到 `html` 上的属性；若实际属性名不同按 `lib/scheme.ts` 改） |
| 84, 128 | `getByRole('button', { name: \`编辑 X（#id）\` })` | `await page.getByRole('button', { name: \`更多操作 X（#id）\` }).click(); await page.getByRole('menuitem', { name: \`编辑 X（#id）\` }).click();`；Escape 后焦点回到的元素是 ⋯ 触发器：`toBeFocused()` 改断 `更多操作 …` |
| 100, 144 | `button` 「计费 X」 | 同上走「编辑」，字段在 `dialog.getByRole('region', { name: '费用' })` 内 |
| 70–71, 87 | `复制 IPv4 8.8.8.8` 在列表与弹窗内可见 | 列表内断言保留在 1440px 视口；375px 下（原 137–139 行）地址列隐藏，把 `copy4 / copy6` 的手机断言删掉，改断 `page.getByRole('row', …)` 内 `更多操作` 可见 |
| 86 | `dialog` | 不变（抽屉也是 `dialog`）；追加 `await expect(dialog).toHaveClass(/drawer/)` |
| 148 | 导航抽屉里的「总览」链接 | 不变 |
| 153–155 | 总览截图 | 追加：`await expect(page.getByRole('list', { name: '需要处理' })).toBeVisible()`；截图名不变 |

追加一段（在 `nodes-light-desktop.png` 之前）：

```ts
    // 行菜单内两段式删除：首击只武装，Escape 撤销，节点仍在。
    await page.getByRole('button', { name: `更多操作 tokyo-renamed（#${ids[0]}）` }).click();
    await page.getByRole('menuitem', { name: `删除 tokyo-renamed（#${ids[0]}）` }).click();
    await expect(page.getByRole('menuitem', { name: `确认删除 tokyo-renamed（#${ids[0]}）` })).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath('nodes-row-menu-armed.png') });
    await page.keyboard.press('Escape');
    await expect(page.getByRole('menu')).toHaveCount(0);
    await expect(page.getByRole('link', { name: `tokyo-renamed（#${ids[0]}）` })).toBeVisible();
    // 需要处理 → 筛选结果：卡上的数与列表行数同一谓词。
    await page.goto('/admin/');
    const expiring = page.getByRole('list', { name: '需要处理' }).getByRole('link').nth(1);
    const expiringCount = Number(await expiring.locator('strong').textContent());
    await expiring.click();
    await expect(page).toHaveURL(/\/admin\/nodes\?expiring=1$/);
    await expect(page.getByRole('row')).toHaveCount(expiringCount + 1);
    await page.getByRole('button', { name: '清除筛选' }).click();
    await expect(page).toHaveURL(/\/admin\/nodes$/);
```

节点排序两条用例（173–296）：`combobox 移动 X` 的 `selectOption('last')` 不变；`selectOption('move')`（275）改为 `更多操作 X` → `menuitem` 「移动到… X」；「移动到…」批量按钮（249）先勾选再点，不变。

- [ ] **Step 2: 改 `agent-diagnostics.spec.ts`**

`await page.goto(\`/admin/nodes/${node.id}\`);` 后加 `await page.getByRole("tab", { name: "Agent 诊断" }).click();`；手机截图前后不变（tab 在 375px 也可见）。另加一行深链验证：诊断断言完成后 `await page.goto(\`/admin/nodes/${node.id}?tab=diagnostics\`); await expect(card).toBeVisible();`。

- [ ] **Step 3: 跑 e2e**

Run: `cd /Users/xjetry/work/vibe/probe && make web-e2e > /tmp/b13-e2e.log 2>&1; echo $?` → 0。看 `web/test-results/**/overview-desktop.png`、`nodes-light-desktop.png`、`nodes-row-menu-armed.png`、`node-editor-mobile.png`、`diagnostics-desktop.png`：四张卡一行、表格 52px 行、抽屉从右侧出、手机无横向滚动。截图里发现的问题按根因修在组件 / CSS（不改测试绕过），修完重跑同一条命令。

- [ ] **Step 4: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/e2e/admin-ui.spec.ts web/e2e/agent-diagnostics.spec.ts && git commit -m "test(web): 管理端 e2e 跟上外壳、行菜单、抽屉与详情 tab，并从需要处理卡点进筛选"
```

### Task 14: 全量验证、死样式与验收记录

- [ ] **Step 1: 全量**

```bash
cd /Users/xjetry/work/vibe/probe/web && pnpm typecheck > /tmp/b14-tsc.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe/web && pnpm lint > /tmp/b14-lint.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run > /tmp/b14-unit.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe && make web-e2e > /tmp/b14-e2e.log 2>&1; echo $?
```

四个都要 0。失败不改测试绕过；修根因后重跑同一条命令。

- [ ] **Step 2: admin.css 死样式**

```bash
cd /Users/xjetry/work/vibe/probe/web && grep -o "^\.[a-zA-Z][a-zA-Z0-9_-]*" src/admin.css | sort -u | while read -r cls; do n=${cls#.}; if ! grep -rq -- "$n" src --include='*.tsx' --include='*.ts' --exclude='*.css'; then echo "$cls"; fi; done > /tmp/b14-dead.log 2>&1; echo $?; cat /tmp/b14-dead.log
```

先用一个已知无引用的类名冒烟（临时在 admin.css 加 `.zz-unused{}` 跑一次，看它出现在输出里，再删掉）。输出里每个类名核对一遍（有的是由 JS 拼出来的，如 `drop-before`、`is-dragging`），确实无引用的删除。

- [ ] **Step 3: 验收记录**

在本计划末尾追加 `## 验收记录`：每个 Task 的 commit hash、全量四条命令的退出码、e2e 截图路径、死样式清理的类名列表、消费面清单之外新发现的断言位置。

- [ ] **Step 4: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/admin.css docs/superpowers/plans/2026-10-08-web-redesign-admin-shell-nodes.md && git commit -m "style(web): 清理管理端无引用样式；记录外壳、总览与节点改版的验收"
```

---

## 自审记录

- 设计 §4.1 外壳：Task 4（侧栏 232、顶栏 48、面包屑、公开页链接、明暗切换、登出）、Task 5（⌘K 快速搜索）；「列表页统一为 页头 → 筛选行 → 表格；新建与编辑一律在右侧抽屉；行操作收进 ⋯」在本计划内由 Task 7、9、10 落实到总览与节点，其余列表页在《探测告警 + 系统登录》计划。
- §4.2 总览：Task 6 四个数的谓词，Task 7 四卡 + 实时表（状态点、名称、三条细条 + 数字、负载、↓↑、本周期、最近上报）。「触发中的告警」按 `ListAlertRules.states` 的 `state === "firing"` 条数，而不是 §4.2 括号里提的 `AlertEvent.transition`——states 是当前态，事件是历史，卡要的是当前。
- §4.3 节点：Task 8 筛选、Task 9 表格 / 批量条 / ⋯、Task 10 四分组抽屉与添加 → 安装命令、Task 12 详情（同结构、状态头元信息、右上编辑、四 tab、覆盖率进 ⓘ 并写明不是在线率）。
- §4.7 手机版：侧栏抽屉（Task 4 保留既有）、需要处理 2×2（Task 7 CSS）、节点表变卡片（Task 9 CSS）。
- Review Focus 五条各有归属任务与用例：未知状态（NodesFilters 第 1 条、Overview 第 2 条）、菜单跨轮询（NodesFilters 第 6 条）、非法 URL（nodeFilters 第 1 条、NodesFilters 第 2 条）、零节点（Overview 第 7 条）、tab 不重发历史（NodeDetail 新用例）。
- 类型一致性：`liveStatus(node, live)` 在 Task 6 定义、Task 7 / 9 / 12 同签名调用；`RowMenuItem.onSelect(trigger)` 在 Task 3 定义、Task 9 以 trigger 为 opener；`Drawer` 的 props 与 `Modal` 相同减 `variant`；`NodeEditor` 去掉 `mode` 后 Task 12 的调用与 Task 10 一致；`EventFeed({ events, nodeName, channelName })` 在 Task 11 定义、Task 12 调用；`attentionCards` 的 `to` 与 Task 8 `scopeFromParams` 认的键一致（`status / expiring / lagging`）。
- 占位扫描：无 TBD / TODO；每个代码步骤都给了代码；「同 Task N」只出现在 helper 复制的说明里并要求复制而非引用。
- 自带约束：`scopeChips` 一度列入 Task 8 后发现没有消费者，已删；`NowGrid` 不 import 生成代码的要求改为「不 import 任何生成代码（结构类型入参）」，`importScan.test.ts` 本身只钉 `admin_pb`，两者都满足。
