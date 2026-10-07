# Web 改版：共享设计系统与公开页 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地设计规范 §8 的前两个里程碑：两端共用的设计 token 与基础组件（状态徽章、阈值进度条、迷你线、多选下拉、ⓘ、图表样式），以及按新设计重写的公开页（状态墙 + 详情侧栏、卡片视图、节点页、探测对比、关闭页、手机版）。

**Architecture:** token 只写在 `web/src/styles.css` 的 `:root` 一处，`admin.css` 在管理面板改版前继续用自己的覆盖，所以管理面板外观不受本计划影响；状态、阈值、筛选、分组、汇总都是 `web/src/lib/` 与 `web/src/public/filters.ts` 里的纯函数，组件只负责渲染。图表的横轴刻度、图例、浅线、填充带都由共享 `Chart` 承载，节点页与对比页不各自设置。公开页关闭时 hub 直接回静态说明页（`internal/hub/web/web.go` 的 `closedPage`），不经 SPA。

**Tech Stack:** React 19 + react-router 8 + connect-query 2 + uPlot 1.6；vitest 5 + Testing Library（jsdom）；Playwright 1.63 e2e（`make web-e2e` 跑真实 hub）；Go 1.x（关闭页）。

**Spec:** `docs/superpowers/specs/2026-10-07-web-redesign-design.md`（§2 设计系统、§3 公开页、§5 图表、§6 数据边界；§4 管理面板与 §8 其余里程碑另写计划）。设计 token 的唯一来源是 `docs/design/stitch/DESIGN.md` 的 frontmatter。

**不在本计划里：** 管理面板的外壳、总览、节点列表、探测任务（含「节点」列不换行的修复）、告警、系统页与登录；抽屉与 ⋯ 菜单组件（首个消费者在管理外壳，随那个计划一起做）。

## Global Constraints

- 颜色（§2）：主色 `light-dark(#2563eb, #5b9bf8)` 只用于链接、选中、焦点环、主按钮；站点可替换主色，含义不能只靠主色表达。状态色固定：在线 `#10b981`、需关注 `#f59e0b`、离线 `#f43f5e`、从未上报 `#6b7280`、维护中 `#a78bfa`；状态一律「色点 + 文字」。进度条阈值：<70% 中性、70–90% 琥珀、>90% 玫红。深色画布 `#0b0d12` / 卡片 `#12151c` / 边框 `#232836`；浅色 `#f6f7f9` / `#ffffff` / `#e4e7ec`。
- `vite.config.ts:29-34` 用正则 `--accent:\s*light-dark\(\s*(#[0-9a-fA-F]{6})\s*,` 从 `styles.css` 读内置浅色主色，写法变了构建失败。
- 字体（§2）：界面 Inter + 系统中文；JetBrains Mono 只给数字、单位、时间、IP、版本号并开 `tabular-nums`。不随产物打包字体、不加载远程字体（`docs/brand.md`；CSP `default-src 'self'`），按本机已安装字体回退。公开页正文 14px。
- 密度（§2）：表头 32px、行 36px、控件 32px；控件圆角 4px、卡片 6px；全圆角只给状态点与胶囊；只有弹层带阴影。
- 动态集合（§2）：标签与地区来自快照、数量不定，筛选器一律是多选下拉（标签可搜索），已选以可移除胶囊显示，放不下折叠「+N」。
- 数据边界（§6）：只用 `public.proto` / `query.proto` 已有字段。公开页禁止：IP、主机名、内核、agent 版本、可用率 / 在线率 / SLA、离线原因、同步延迟、拓扑、事件日志、告警、页脚、手动刷新、通知铃铛、额外导航。节点状态只有四种：在线、离线（附最后上报）、从未上报、维护中；维护中优先于其余三种，在线计数与分段条用同一判定。
- 图表（§5）：1px 细网格；没有方块图例；图例无悬停显示最新值、悬停显示该时刻值；均值实线、峰值同色浅线；窗口内没有数据时图区中央一行占位；系列色与五个状态色保持距离；横轴相邻刻度 ≥80px，手机刻度只留时分（7d / 30d 保留月-日）。
- 公开请求是不带凭据的 GET，按来源限流（桶 60、每秒补 10）：不得为每张卡片各发一次历史查询，所以卡片视图没有迷你线，只有详情面板为选中的一个节点查 1 小时速率。
- 公开入口的 import 卫生由 `web/src/public/importScan.test.ts` 钉住：公开包不得带入 `admin_pb.ts`；新组件若公开页要用，不得 import 任何管理服务的生成代码。
- `web/src/test/setup.ts` 关掉了 vitest 的 `globals`，每个测试文件自己 import `expect/it/vi`；公开页测试用 `renderWithService(PublicService, impl, routes, path)`（`web/src/test/harness.tsx`）。
- 代码注释与 commit message 不写过程信息（任务编号、方案代号、轮次），只写 WHY 与不变式；注释里的因果论断要能推演证伪。
- 每条新断言做一次缺陷注入（构造它本该抓住的缺陷，确认红在正确的原因上）。判成败的命令不接管道：`cmd > log 2>&1; echo $?`。
- Bash 每条命令用 `cd /Users/xjetry/work/vibe/probe/web && …`（zsh 不分词、`cd` 失败会让 `&&` 短路，先看退出码）。vitest 单测文件：`pnpm vitest run src/path/file.test.tsx`。

## Review Focus

- `online: true` 且 `maintenance: true` 的节点：状态是「维护中」，不计入「在线」计数与分段条的在线段，详情里仍显示「最近上报 刚刚」——Task 2、Task 13。
- 状态墙里被选中的节点在下一次轮询后从快照消失（被转私有）：详情面板退回到墙上第一个节点，不崩、不留空面板——Task 15。
- `localStorage` 被禁用（Safari 无痕、企业策略）时读写都抛：明暗切换当前页面仍生效，刷新后回到跟随站点——Task 4。
- 对比窗口里某节点每个样本都全部超时（没有 RTT）：表格行的均值 / 最小 / 最大是「–」、丢包率 100%，排序时「–」排最后——Task 18。
- 快照里所有节点都没有国家（全部归「未知」）或节点数为 0：分组只有一组「未知」、分段条不出现 NaN 宽度——Task 11、Task 13。

---

## 文件地图

共享（两端都会用）：

| 文件 | 职责 |
|---|---|
| `web/src/styles.css` | `:root` token、基础元素、`.num`、`.status-badge`、`.bar[data-level]`、`.multi-select`、`.info-tip`、`.chart-legend`、`.sparkline`、`.sr-only` |
| `web/src/lib/status.ts` | 四态判定 `nodeStatus`、标签与顺序、阈值 `usageLevel` / `expiryLevel` |
| `web/src/lib/scheme.ts` | 访客明暗选择的读写与「访客 > 站点 > 系统」的合成 |
| `web/src/lib/seriesPalette.ts` | 图表系列色与浅线取色 |
| `web/src/lib/useMediaQuery.ts` | 媒体查询 hook（宽屏 / 手机分支） |
| `web/src/components/StatusBadge.tsx`、`Bar.tsx`、`InfoTip.tsx`、`ThemeToggle.tsx`、`MultiSelect.tsx`、`Sparkline.tsx`、`Chart.tsx`、`History.tsx`、`ProbeComparison.tsx` | 组件 |

公开页：

| 文件 | 职责 |
|---|---|
| `web/src/public/filters.ts` | 搜索 / 筛选 / 排序 / 分组 / 汇总的纯函数 |
| `web/src/public/Layout.tsx`、`site.ts` | 48px 顶栏、站点设置与访客明暗的应用 |
| `web/src/public/Overview.tsx` | 状态、视图切换；`StatusSummary.tsx`、`FilterRow.tsx`、`StatusWall.tsx`、`DetailPanel.tsx`、`NodeCard.tsx`（含 `CardGrid`） |
| `web/src/public/NodePage.tsx`、`ProbeCompare.tsx` | 节点页、探测对比 |
| `web/src/public/public.css` | 公开页布局（顶栏、汇总条、筛选行、状态墙、详情面板、卡片、节点页、手机版） |
| `internal/hub/web/web.go` | 关闭页 |
| `web/e2e/public-overview.spec.ts`、`public-node.spec.ts`、`region-filter.spec.ts` | 从用户入口验收 |

删除：`web/src/public/FilterBar.tsx`、`FilterBar.test.tsx`、`ViewOptions.tsx`、`web/e2e/public-cards.spec.ts`（被新组件与新 e2e 取代）。

---

### Task 1: 设计 token 与基础样式

**Files:**
- Modify: `web/src/styles.css:1-12`（`:root` 块）、`:33`（控件）、`:25`（`.card`）、`:84-88`（`.dot`、`.bar`）
- Create: `web/src/styles.test.ts`

**Interfaces:**
- Produces: CSS 变量 `--bg --card --card-raised --card-hover --fg --muted --faint --line --line-strong --accent --on-accent --status-online --status-attention --status-offline --status-never --status-maintenance --font-ui --font-mono --radius-control --radius-card --control-h --shadow-float`；旧名 `--ok --bad --warn` 继续存在（指向状态色），管理面板改版时再删；工具类 `.num`、`.sr-only`、`.icon-button`。

- [ ] **Step 1: 写 token 核对测试（红）**

`web/src/styles.test.ts`：

```ts
// @vitest-environment node
// @ts-nocheck 读文件需要 node 类型，tsconfig.app 只给了 vite/client（与 importScan.test.ts 同一原因）。
import { readFileSync } from "node:fs";
import { expect, it } from "vitest";

const css = readFileSync(new URL("./styles.css", import.meta.url), "utf8");
const root = /:root\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
const token = (name) => new RegExp(`${name}:\\s*([^;]+);`).exec(root)?.[1].trim();

// 设计规范 §2 的取值只写在 styles.css 一处；这里逐个对照规范，改规范必须同时改 CSS 与这张表。
it.each([
  ["--bg", "light-dark(#f6f7f9, #0b0d12)"],
  ["--card", "light-dark(#ffffff, #12151c)"],
  ["--line", "light-dark(#e4e7ec, #232836)"],
  ["--fg", "light-dark(#111827, #e6e8ee)"],
  ["--accent", "light-dark(#2563eb, #5b9bf8)"],
  ["--status-online", "#10b981"],
  ["--status-attention", "#f59e0b"],
  ["--status-offline", "#f43f5e"],
  ["--status-never", "#6b7280"],
  ["--status-maintenance", "#a78bfa"],
  ["--ok", "var(--status-online)"],
  ["--bad", "var(--status-offline)"],
  ["--warn", "var(--status-attention)"],
])("%s = %s", (name, value) => {
  expect(token(name)).toBe(value);
});

// vite.config.ts 用同一个正则读内置浅色主色；写法变了构建才失败，这里先于构建钉住。
it("--accent 保持 vite.config 能读出的写法", () => {
  expect(/--accent:\s*light-dark\(\s*(#[0-9a-fA-F]{6})\s*,/.exec(css)?.[1]).toBe("#2563eb");
});

// 站点可替换主色（§2）：状态色是字面量，不引用 --accent，换主色不会连带改掉在线 / 离线的颜色。
it("五个状态色都是字面量", () => {
  for (const name of ["online", "attention", "offline", "never", "maintenance"]) expect(token(`--status-${name}`)).toMatch(/^#[0-9a-f]{6}$/);
});

it("等宽只经 .num 施加，正文字体栈以 Inter 开头、不引用远程字体", () => {
  expect(token("--font-ui")).toMatch(/^Inter,/);
  expect(token("--font-mono")).toMatch(/^"JetBrains Mono",/);
  expect(css).not.toMatch(/@font-face|@import|fonts\.googleapis/);
  expect(css).toMatch(/\.num\s*\{[^}]*font-family:\s*var\(--font-mono\)[^}]*font-variant-numeric:\s*tabular-nums/);
});
```

- [ ] **Step 2: 跑测试确认红在正确的原因上**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/styles.test.ts > /tmp/t1-red.log 2>&1; echo $?; grep -c '✓\|×' /tmp/t1-red.log; grep -m3 'AssertionError\|expected' /tmp/t1-red.log`
Expected: 退出码 1；失败原因是 `--bg` 等取值与旧值（`#0f1115`）不等、`--status-online` 为 undefined。不是「文件读不到」。

- [ ] **Step 3: 改 `:root` 与基础样式**

把 `web/src/styles.css` 第 1–12 行（注释到两条 `data-theme` 规则）替换为：

```css
/* 明暗只经 color-scheme 切换：light-dark() 按元素的 color-scheme 取值，默认跟随系统；公开页按站点设置与访客选择在 html 上
   写 data-theme，下面两条规则让它压过系统设置。不要再按系统明暗的媒体查询改写变量，那会绕过 data-theme。
   取值来自 docs/design/stitch/DESIGN.md 的 frontmatter（设计 §2），styles.test.ts 逐项对照。 */
:root {
  color-scheme: light dark;
  --bg: light-dark(#f6f7f9, #0b0d12);
  --card: light-dark(#ffffff, #12151c);
  --card-raised: light-dark(#f9fafb, #181c25);
  --card-hover: light-dark(#f3f4f6, #1c212c);
  --fg: light-dark(#111827, #e6e8ee);
  --muted: light-dark(#4b5563, #9aa3b5);
  --faint: light-dark(#9ca3af, #5f6878);
  --line: light-dark(#e4e7ec, #232836);
  --line-strong: light-dark(#d1d5db, #2f3646);
  /* 主色只用于链接、选中、焦点环、主按钮；站点设置可覆盖它（site.ts），所以含义不能只靠它表达。 */
  --accent: light-dark(#2563eb, #5b9bf8);
  --on-accent: light-dark(#ffffff, #0b0d12);
  /* 状态色固定、与主色无关：换主色不能连带改掉在线 / 离线。 */
  --status-online: #10b981;
  --status-attention: #f59e0b;
  --status-offline: #f43f5e;
  --status-never: #6b7280;
  --status-maintenance: #a78bfa;
  /* 旧名：管理面板（admin.css）仍按它们取色，面板改版后删除。 */
  --ok: var(--status-online);
  --bad: var(--status-offline);
  --warn: var(--status-attention);
  /* 不打包字体：本机装了 Inter / JetBrains Mono 就用，否则退到系统字体（docs/brand.md 不加载远程字体）。 */
  --font-ui: Inter, system-ui, -apple-system, "PingFang SC", "Hiragino Sans GB", "Noto Sans SC", "Microsoft YaHei", sans-serif;
  --font-mono: "JetBrains Mono", ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  --radius-control: 4px;
  --radius-card: 6px;
  --control-h: 32px;
  --shadow-float: 0 4px 16px rgba(0, 0, 0, 0.45);
  font-family: var(--font-ui); font-size: 14px; line-height: 1.45; color: var(--fg); background: var(--bg);
}
:root[data-theme="light"] { color-scheme: light; }
:root[data-theme="dark"] { color-scheme: dark; }
/* 等宽只给数字、单位、时间、IP、版本号（设计 §2）；中文与节点名一律不用。 */
.num { font-family: var(--font-mono); font-variant-numeric: tabular-nums; }
.sr-only { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0; }
.icon-button { display: inline-flex; align-items: center; justify-content: center; width: var(--control-h); height: var(--control-h); padding: 0; flex: none; color: var(--muted); border: 1px solid transparent; background: transparent; border-radius: var(--radius-control); }
.icon-button:hover { color: var(--fg); border-color: var(--line); }
:is(button, input, select, textarea, a, summary):focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
```

然后把第 25 行 `.card` 的 `border-radius: 8px` 改为 `border-radius: var(--radius-card)`；第 33 行控件规则改为 `input, select, button, textarea { font: inherit; min-height: var(--control-h); padding: 0 0.6rem; border: 1px solid var(--line); border-radius: var(--radius-control); background: var(--card); color: var(--fg); }`；`textarea { min-height: 6rem; padding: 0.4rem 0.6rem; }` 紧随其后新增一行。`admin.css` 里 `.sr-only` 的定义（`:424`）删除，改用这里的。

- [ ] **Step 4: 跑测试确认绿，再跑全量与构建**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/styles.test.ts > /tmp/t1.log 2>&1; echo $?`
Expected: 0。

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run > /tmp/t1-all.log 2>&1; echo $?; tail -5 /tmp/t1-all.log`
Expected: 0（`Chart.test.tsx` 的颜色断言用的是 stub，不读 CSS，不受影响）。

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm build > /tmp/t1-build.log 2>&1; echo $?`
Expected: 0；`__BUILT_IN_ACCENT__` 仍从 CSS 读出。

- [ ] **Step 5: 缺陷注入**

把 `--status-online` 临时改成 `var(--accent)`，跑 `src/styles.test.ts`，应只有「五个状态色都是字面量」与 `--status-online = #10b981` 两条红；改回后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/styles.css web/src/admin.css web/src/styles.test.ts && git commit -m "feat(web): 设计 token 收进 styles.css 的 :root，状态色与主色分离"
```

---

### Task 2: 四态判定与阈值

**Files:**
- Create: `web/src/lib/status.ts`
- Create: `web/src/lib/status.test.ts`

**Interfaces:**
- Produces:
  ```ts
  export type NodeStatus = "online" | "offline" | "never" | "maintenance";
  export type StatusInput = { online: boolean; maintenance: boolean; lastSeenAt?: bigint };
  export function nodeStatus(n: StatusInput): NodeStatus;
  export const STATUS_LABEL: Record<NodeStatus, string>;        // 在线 / 离线 / 从未上报 / 维护中
  export const STATUS_ORDER: readonly NodeStatus[];             // ["online", "maintenance", "never", "offline"]
  export type Level = "neutral" | "attention" | "critical";
  export function usageLevel(percent: number): Level;
  export function expiryLevel(daysLeft: number | undefined): Level;
  ```
  `PublicNode`（`public_pb.ts`）与管理端 `Node` 都满足 `StatusInput`。

- [ ] **Step 1: 写测试（红）**

`web/src/lib/status.test.ts`：

```ts
import { expect, it } from "vitest";
import { expiryLevel, nodeStatus, STATUS_LABEL, STATUS_ORDER, usageLevel } from "./status";

it.each([
  [{ online: true, maintenance: false, lastSeenAt: 10n }, "online"],
  [{ online: false, maintenance: false, lastSeenAt: 10n }, "offline"],
  [{ online: false, maintenance: false }, "never"],
  // 维护中优先：站长主动摘出的节点即便仍在上报也不算在线（设计 §6）。
  [{ online: true, maintenance: true, lastSeenAt: 10n }, "maintenance"],
  [{ online: false, maintenance: true }, "maintenance"],
] as const)("nodeStatus(%o) = %s", (input, want) => {
  expect(nodeStatus(input)).toBe(want);
});

it("四种状态各有标签，顺序表覆盖全部四种且不重复", () => {
  expect(Object.keys(STATUS_LABEL).sort()).toEqual(["maintenance", "never", "offline", "online"]);
  expect([...STATUS_ORDER].sort()).toEqual(["maintenance", "never", "offline", "online"]);
  expect(STATUS_LABEL.never).toBe("从未上报");
});

it.each([
  [0, "neutral"], [69.9, "neutral"], [70, "attention"], [90, "attention"], [90.1, "critical"], [100, "critical"],
])("usageLevel(%d) = %s", (v, want) => {
  expect(usageLevel(v)).toBe(want);
});

it.each([
  [undefined, "neutral"], [31, "neutral"], [30, "attention"], [0, "attention"], [-1, "critical"],
])("expiryLevel(%s) = %s", (d, want) => {
  expect(expiryLevel(d)).toBe(want);
});
```

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/lib/status.test.ts > /tmp/t2-red.log 2>&1; echo $?; grep -m1 'Failed to resolve import\|Cannot find' /tmp/t2-red.log`
Expected: 退出码 1，原因是模块不存在。

- [ ] **Step 3: 实现**

`web/src/lib/status.ts`：

```ts
// 节点状态只有四种（设计 §6），一个节点只属于一种：汇总条四段相加等于总数，在线计数、状态墙、徽章都用这一个判定。
// 优先级：维护中 > 从未上报 > 在线 > 离线。维护中是站长主动摘出的节点，agent 仍在上报也不计入在线；
// 从未上报由 lastSeenAt 缺失判定（proto：从未上报则缺失），hub 对这类节点 online 恒为 false。
export type NodeStatus = "online" | "offline" | "never" | "maintenance";
export type StatusInput = { online: boolean; maintenance: boolean; lastSeenAt?: bigint };

export function nodeStatus(n: StatusInput): NodeStatus {
  if (n.maintenance) return "maintenance";
  if (n.lastSeenAt === undefined) return "never";
  return n.online ? "online" : "offline";
}

export const STATUS_LABEL: Record<NodeStatus, string> = { online: "在线", offline: "离线", never: "从未上报", maintenance: "维护中" };
// 汇总条分段与图例的固定顺序（设计 §3.1）。
export const STATUS_ORDER: readonly NodeStatus[] = ["online", "maintenance", "never", "offline"];

export type Level = "neutral" | "attention" | "critical";

// 进度条阈值（设计 §2）：<70 中性、70–90 琥珀、>90 玫红；两个边界都归琥珀。
export function usageLevel(percent: number): Level {
  if (percent > 90) return "critical";
  if (percent >= 70) return "attention";
  return "neutral";
}

// 到期：已过期玫红、30 天内琥珀、其余中性；没有到期日（hub 不下发 daysLeft）中性。
export function expiryLevel(daysLeft: number | undefined): Level {
  if (daysLeft === undefined) return "neutral";
  if (daysLeft < 0) return "critical";
  return daysLeft <= 30 ? "attention" : "neutral";
}
```

- [ ] **Step 4: 跑测试确认绿**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/lib/status.test.ts > /tmp/t2.log 2>&1; echo $?`
Expected: 0。

- [ ] **Step 5: 缺陷注入**

把 `if (n.maintenance)` 移到 `online` 判断之后，跑测试：应只有「online+maintenance → maintenance」那条红；改回后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/lib/status.ts web/src/lib/status.test.ts && git commit -m "feat(web): 节点四态判定与进度条、到期阈值"
```

---

### Task 3: 状态徽章、阈值进度条与 ⓘ

**Files:**
- Create: `web/src/components/StatusBadge.tsx`、`web/src/components/StatusBadge.test.tsx`
- Modify: `web/src/components/Bar.tsx`（`Bar` 加 `data-level` 与 `thin`）
- Create: `web/src/components/InfoTip.tsx`、`web/src/components/InfoTip.test.tsx`
- Modify: `web/src/styles.css`（末尾追加样式）

**Interfaces:**
- Consumes: `nodeStatus`、`STATUS_LABEL`、`usageLevel`（Task 2）。
- Produces:
  ```ts
  export function StatusBadge({ status, detail }: { status: NodeStatus; detail?: string }): JSX.Element;  // <span class="status-badge is-online" data-status>色点+文字[ · detail]
  export function Bar({ value, label, thin }: { value: number; label: string; thin?: boolean }): JSX.Element; // role=meter，data-level=usageLevel(value)
  export function Missing(): JSX.Element; export function ratio(used: bigint, total: bigint): number;   // 不变
  export function InfoTip({ label, children }: { label?: string; children: ReactNode }): JSX.Element;  // 按钮 aria-label=label（默认「说明」），正文 role=tooltip 始终在 DOM
  ```

- [ ] **Step 1: 写测试（红）**

`web/src/components/StatusBadge.test.tsx`：

```tsx
import { render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { StatusBadge } from "./StatusBadge";
import { Bar } from "./Bar";

it("徽章是色点加文字：文字是可访问名，色点对读屏隐藏，状态写在 data-status 上", () => {
  render(<StatusBadge status="offline" detail="3 天前" />);
  const badge = screen.getByText("离线 · 3 天前");
  expect(badge).toHaveAttribute("data-status", "offline");
  expect(badge.querySelector(".status-dot")).toHaveAttribute("aria-hidden", "true");
  expect(screen.queryByRole("img")).toBeNull();
});

it("没有补充文字时只有状态名", () => {
  render(<StatusBadge status="maintenance" />);
  expect(screen.getByText("维护中")).toHaveAttribute("data-status", "maintenance");
});

it.each([[42, "neutral"], [75, "attention"], [95, "critical"]])("进度条 %d%% 的档位是 %s", (value, level) => {
  render(<Bar value={value} label={`${value}%`} thin />);
  const meter = screen.getByRole("meter", { name: `${value}%` });
  expect(meter).toHaveAttribute("data-level", level);
  expect(meter).toHaveClass("thin");
});
```

`web/src/components/InfoTip.test.tsx`：

```tsx
import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { InfoTip } from "./InfoTip";

it("说明文字始终在 DOM 里并由按钮 aria-describedby 指向；点击、聚焦展开，Escape 收起", () => {
  render(<InfoTip>级别 5m，每点 300 秒</InfoTip>);
  const button = screen.getByRole("button", { name: "说明" });
  const tip = screen.getByRole("tooltip");
  expect(tip).toHaveTextContent("级别 5m，每点 300 秒");
  expect(button).toHaveAttribute("aria-describedby", tip.id);
  expect(button).toHaveAttribute("aria-expanded", "false");
  fireEvent.click(button);
  expect(button).toHaveAttribute("aria-expanded", "true");
  fireEvent.keyDown(button, { key: "Escape" });
  expect(button).toHaveAttribute("aria-expanded", "false");
  fireEvent.focus(button);
  expect(button).toHaveAttribute("aria-expanded", "true");
  fireEvent.blur(button);
  expect(button).toHaveAttribute("aria-expanded", "false");
});
```

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/components/StatusBadge.test.tsx src/components/InfoTip.test.tsx > /tmp/t3-red.log 2>&1; echo $?`
Expected: 1（模块不存在；`Bar` 的 `data-level` 断言也红）。

- [ ] **Step 3: 实现**

`web/src/components/StatusBadge.tsx`：

```tsx
import { STATUS_LABEL, type NodeStatus } from "../lib/status";

// 状态一律「色点 + 文字」（设计 §2）：颜色只是辅助，文字才是可访问名，所以色点对读屏隐藏、不再给它 role=img。
// detail 是「· 3 天前」这类补充，与状态名连成一个文本节点，测试与读屏读到的是一句完整的话。
export function StatusBadge({ status, detail }: { status: NodeStatus; detail?: string }) {
  return (
    <span className={`status-badge is-${status}`} data-status={status}>
      <span className="status-dot" aria-hidden="true" />
      {detail ? `${STATUS_LABEL[status]} · ${detail}` : STATUS_LABEL[status]}
    </span>
  );
}
```

`web/src/components/Bar.tsx` 的 `Bar` 改为：

```tsx
import { usageLevel } from "../lib/status";

// 进度条按阈值着色（设计 §2）：档位写在 data-level 上，颜色由 styles.css 按它取状态色；thin 是状态墙与卡片里的细条。
export function Bar({ value, label, thin = false }: { value: number; label: string; thin?: boolean }) {
  const v = Math.max(0, Math.min(100, value));
  return (
    <div className={thin ? "bar thin" : "bar"} data-level={usageLevel(v)} role="meter" aria-valuenow={Math.round(v)} aria-valuemin={0} aria-valuemax={100} aria-label={label}>
      <div className="fill" style={{ width: `${v}%` }} />
      <span>{label}</span>
    </div>
  );
}
```

`web/src/components/InfoTip.tsx`：

```tsx
import { type ReactNode, useId, useState } from "react";

// 口径说明收进 ⓘ（设计 §2）：说明文字始终在 DOM 里（aria-describedby 指向它，读屏与测试都读得到），
// 只用 CSS 按 data-open 控制可见；点击切换，悬停与聚焦展开，Escape 收起。
export function InfoTip({ label = "说明", children }: { label?: string; children: ReactNode }) {
  const id = useId();
  const [open, setOpen] = useState(false);
  return (
    <span className="info-tip" data-open={open || undefined} onMouseEnter={() => setOpen(true)} onMouseLeave={() => setOpen(false)}>
      <button type="button" aria-label={label} aria-expanded={open} aria-describedby={id} onClick={() => setOpen((o) => !o)}
        onFocus={() => setOpen(true)} onBlur={() => setOpen(false)} onKeyDown={(event) => { if (event.key === "Escape") setOpen(false); }}>ⓘ</button>
      <span role="tooltip" id={id} className="info-tip-body">{children}</span>
    </span>
  );
}
```

`web/src/styles.css` 末尾追加：

```css
/* 状态徽章（components/StatusBadge.tsx）：色点 + 文字，背景取状态色 10%、边框 40%（设计 §2）。 */
.status-badge { display: inline-flex; align-items: center; gap: 6px; padding: 0 8px; height: 22px; border-radius: 999px; font-size: 12px; white-space: nowrap; color: var(--status); background: color-mix(in srgb, var(--status) 10%, transparent); border: 1px solid color-mix(in srgb, var(--status) 40%, transparent); }
.status-badge.is-online, .status-dot[data-status="online"] { --status: var(--status-online); }
.status-badge.is-offline, .status-dot[data-status="offline"] { --status: var(--status-offline); }
.status-badge.is-never, .status-dot[data-status="never"] { --status: var(--status-never); }
.status-badge.is-maintenance, .status-dot[data-status="maintenance"] { --status: var(--status-maintenance); }
.status-dot { display: inline-block; width: 7px; height: 7px; border-radius: 50%; background: var(--status, var(--status-never)); flex: none; }
/* 进度条档位（components/Bar.tsx）：中性用主色、琥珀与玫红用状态色。 */
.bar[data-level="neutral"] .fill { background: var(--accent); }
.bar[data-level="attention"] .fill { background: var(--status-attention); }
.bar[data-level="critical"] .fill { background: var(--status-offline); }
.bar.thin { min-width: 0; height: 4px; border: 0; border-radius: 999px; background: var(--line); }
.bar.thin .fill { opacity: 1; border-radius: inherit; }
.bar.thin > span { display: none; }
/* ⓘ（components/InfoTip.tsx）：正文在 DOM 里，data-open 时显示为浮层。 */
.info-tip { position: relative; display: inline-flex; }
.info-tip > button { width: 20px; height: 20px; min-height: 0; padding: 0; border: 0; background: none; color: var(--muted); font-size: 14px; line-height: 1; }
.info-tip-body { display: none; position: absolute; z-index: 30; top: calc(100% + 6px); left: 0; width: max-content; max-width: min(360px, 80vw); padding: 8px 10px; font-size: 12px; line-height: 1.5; color: var(--fg); background: var(--card-raised); border: 1px solid var(--line); border-radius: var(--radius-control); box-shadow: var(--shadow-float); }
.info-tip[data-open] .info-tip-body { display: block; }
```

- [ ] **Step 4: 跑测试确认绿，再跑全量**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/components/StatusBadge.test.tsx src/components/InfoTip.test.tsx > /tmp/t3.log 2>&1; echo $?`
Expected: 0。

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run > /tmp/t3-all.log 2>&1; echo $?`
Expected: 0（`Bar` 签名向后兼容，管理端 `pages/Overview.tsx:92-94` 与公开 `NodeCard.tsx:65` 不变）。

- [ ] **Step 5: 缺陷注入**

在 `InfoTip` 里把 `aria-describedby={id}` 删掉，跑 InfoTip 测试应红在 `aria-describedby`；在 `Bar` 里把 `usageLevel(v)` 换成 `"neutral"`，Bar 测试应有两条红（attention、critical）。改回后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/components/StatusBadge.tsx web/src/components/StatusBadge.test.tsx web/src/components/Bar.tsx web/src/components/InfoTip.tsx web/src/components/InfoTip.test.tsx web/src/styles.css && git commit -m "feat(web): 状态徽章、按阈值着色的进度条与 ⓘ 说明"
```

---

### Task 4: 访客明暗选择与 ThemeToggle

**Files:**
- Create: `web/src/lib/scheme.ts`、`web/src/lib/scheme.test.ts`
- Create: `web/src/components/ThemeToggle.tsx`、`web/src/components/ThemeToggle.test.tsx`
- Modify: `web/src/public/site.ts:14-30`（`applySite` 加第二个参数）、`web/src/public/site.test.ts`

**Interfaces:**
- Produces:
  ```ts
  export type SchemeChoice = "auto" | "light" | "dark";
  export const PUBLIC_SCHEME_KEY = "heron-public-scheme";
  export const SCHEME_LABEL: Record<SchemeChoice, string>;                 // 跟随系统 / 浅色 / 深色
  export function readSchemeChoice(key: string): SchemeChoice;
  export function writeSchemeChoice(key: string, choice: SchemeChoice): void;
  export function nextSchemeChoice(choice: SchemeChoice): SchemeChoice;     // auto → light → dark → auto
  export function forcedTheme(siteTheme: string, choice: SchemeChoice): "light" | "dark" | undefined;
  export function ThemeToggle({ choice, onChange }: { choice: SchemeChoice; onChange: (next: SchemeChoice) => void }): JSX.Element; // button aria-label="明暗切换"
  export function applySite(site: PublicSite, choice?: SchemeChoice): () => void;  // 默认 "auto"
  ```

- [ ] **Step 1: 写测试（红）**

`web/src/lib/scheme.test.ts`：

```ts
import { afterEach, expect, it, vi } from "vitest";
import { forcedTheme, nextSchemeChoice, readSchemeChoice, writeSchemeChoice } from "./scheme";

afterEach(() => { localStorage.clear(); vi.restoreAllMocks(); });

it("访客选择压过站点设置，auto 时按站点，站点 auto 时不写 data-theme", () => {
  expect(forcedTheme("dark", "light")).toBe("light");
  expect(forcedTheme("dark", "auto")).toBe("dark");
  expect(forcedTheme("auto", "auto")).toBeUndefined();
  expect(forcedTheme("", "dark")).toBe("dark");
  // 站点设置只认 light / dark：其它值（旧 hub、手写）与 auto 同义。
  expect(forcedTheme("sepia", "auto")).toBeUndefined();
});

it("选择循环：auto → light → dark → auto", () => {
  expect(nextSchemeChoice("auto")).toBe("light");
  expect(nextSchemeChoice("light")).toBe("dark");
  expect(nextSchemeChoice("dark")).toBe("auto");
});

it("读写 localStorage；auto 表示删除键；非法值当 auto", () => {
  writeSchemeChoice("k", "dark");
  expect(localStorage.getItem("k")).toBe("dark");
  expect(readSchemeChoice("k")).toBe("dark");
  writeSchemeChoice("k", "auto");
  expect(localStorage.getItem("k")).toBeNull();
  localStorage.setItem("k", "sepia");
  expect(readSchemeChoice("k")).toBe("auto");
});

// Safari 无痕与企业策略下 localStorage 的 getItem / setItem 都会抛：读失败按 auto，写失败不抛到页面。
it("存储被禁用时读得到 auto、写不抛", () => {
  vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new Error("denied"); });
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("denied"); });
  expect(readSchemeChoice("k")).toBe("auto");
  expect(() => writeSchemeChoice("k", "dark")).not.toThrow();
});
```

`web/src/components/ThemeToggle.test.tsx`：

```tsx
import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { ThemeToggle } from "./ThemeToggle";

it("一个按钮循环三态，title 写明当前与下一档", () => {
  const onChange = vi.fn();
  render(<ThemeToggle choice="light" onChange={onChange} />);
  const button = screen.getByRole("button", { name: "明暗切换" });
  expect(button).toHaveAttribute("title", "当前：浅色，点击切到深色");
  fireEvent.click(button);
  expect(onChange).toHaveBeenCalledWith("dark");
});
```

在 `web/src/public/site.test.ts` 追加：

```ts
it("访客的明暗选择压过站点设置；撤销后 data-theme 清掉", () => {
  const root = document.documentElement;
  const undo = applySite(create(PublicSiteSchema, { theme: "dark" }), "light");
  expect(root.dataset.theme).toBe("light");
  undo();
  expect(root.dataset.theme).toBeUndefined();
  applySite(create(PublicSiteSchema, { theme: "dark" }), "auto");
  expect(root.dataset.theme).toBe("dark");
});
```

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/lib/scheme.test.ts src/components/ThemeToggle.test.tsx src/public/site.test.ts > /tmp/t4-red.log 2>&1; echo $?`
Expected: 1。scheme 与 ThemeToggle 因模块不存在；site.test 的新用例红在 `data-theme` 为 `dark` 而非 `light`（第二个参数被忽略）。

- [ ] **Step 3: 实现**

`web/src/lib/scheme.ts`：

```ts
export type SchemeChoice = "auto" | "light" | "dark";
export const PUBLIC_SCHEME_KEY = "heron-public-scheme";
const CHOICES: readonly SchemeChoice[] = ["auto", "light", "dark"];
export const SCHEME_LABEL: Record<SchemeChoice, string> = { auto: "跟随系统", light: "浅色", dark: "深色" };

// 存储被禁用（Safari 无痕、企业策略）时 localStorage 的读写都会抛：读失败当 auto，写失败只影响下次打开，当前页面仍按 choice 显示。
export function readSchemeChoice(key: string): SchemeChoice {
  try {
    const value = localStorage.getItem(key);
    return CHOICES.includes(value as SchemeChoice) ? (value as SchemeChoice) : "auto";
  } catch {
    return "auto";
  }
}

export function writeSchemeChoice(key: string, choice: SchemeChoice): void {
  try {
    if (choice === "auto") localStorage.removeItem(key);
    else localStorage.setItem(key, choice);
  } catch {
    // 见 readSchemeChoice。
  }
}

export function nextSchemeChoice(choice: SchemeChoice): SchemeChoice {
  return CHOICES[(CHOICES.indexOf(choice) + 1) % CHOICES.length];
}

// 写到 html[data-theme] 的值：访客的显式选择 > 站点设置的 light / dark > 跟随系统（不写）。
// styles.css 的 color-scheme 规则与 colorScheme.ts 的 currentScheme 都只看 data-theme，所以合成只在这一处。
export function forcedTheme(siteTheme: string, choice: SchemeChoice): "light" | "dark" | undefined {
  if (choice !== "auto") return choice;
  return siteTheme === "light" || siteTheme === "dark" ? siteTheme : undefined;
}
```

`web/src/components/ThemeToggle.tsx`：

```tsx
import { nextSchemeChoice, SCHEME_LABEL, type SchemeChoice } from "../lib/scheme";
import { Icon } from "./Icon";

// 公开页顶栏的明暗切换（设计 §3.1）：一个按钮循环三态，title 说明当前档与下一档；选择的持久化由调用方做。
export function ThemeToggle({ choice, onChange }: { choice: SchemeChoice; onChange: (next: SchemeChoice) => void }) {
  const next = nextSchemeChoice(choice);
  return (
    <button type="button" className="icon-button theme-toggle" aria-label="明暗切换" title={`当前：${SCHEME_LABEL[choice]}，点击切到${SCHEME_LABEL[next]}`} onClick={() => onChange(next)}>
      <Icon name={choice === "dark" ? "moon" : "sun"} />
    </button>
  );
}
```

`web/src/public/site.ts`：import `forcedTheme, type SchemeChoice` from `../lib/scheme`；`applySite(site: PublicSite, choice: SchemeChoice = "auto")`，把

```ts
  if (site.theme === "light" || site.theme === "dark") root.dataset.theme = site.theme;
  else delete root.dataset.theme;
```

改为

```ts
  const theme = forcedTheme(site.theme, choice);
  if (theme) root.dataset.theme = theme;
  else delete root.dataset.theme;
```

并把函数上方注释里「明暗写在 html 的 data-theme 上（auto 不写，跟随系统…）」一句改为「明暗按 forcedTheme 合成（访客选择 > 站点设置 > 跟随系统）写到 html 的 data-theme」。

- [ ] **Step 4: 跑测试确认绿**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/lib/scheme.test.ts src/components/ThemeToggle.test.tsx src/public/site.test.ts > /tmp/t4.log 2>&1; echo $?`
Expected: 0。

- [ ] **Step 5: 缺陷注入**

在 `readSchemeChoice` 去掉 `try/catch`，scheme 测试「存储被禁用」应红在 `denied` 抛出；改回后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/lib/scheme.ts web/src/lib/scheme.test.ts web/src/components/ThemeToggle.tsx web/src/components/ThemeToggle.test.tsx web/src/public/site.ts web/src/public/site.test.ts && git commit -m "feat(web): 访客明暗选择，压过站点设置且不依赖存储可用"
```

---

### Task 5: 多选下拉 MultiSelect

**Files:**
- Create: `web/src/components/MultiSelect.tsx`、`web/src/components/MultiSelect.test.tsx`
- Modify: `web/src/styles.css`（末尾追加 `.multi-select*`、`.chip`）

**Interfaces:**
- Consumes: `literalPattern(text, whole)`（`web/src/lib/fold.ts`）。
- Produces:
  ```ts
  export type MultiSelectOption = { value: string; label: string; count?: number };
  export function MultiSelect(props: {
    label: string;                       // role=group 的名字，也是触发按钮文字
    options: readonly MultiSelectOption[];
    selected: readonly string[];         // 调用方已按当前 options 规范化
    onChange: (next: string[]) => void;
    searchable?: boolean;                // 展开后带搜索框 aria-label=`搜索${label}`
    foldAt?: number;                     // 已选胶囊最多显示几个，默认 3，其余折成「+N」
  }): JSX.Element;
  ```
  DOM：触发按钮 `aria-expanded`；展开后每个选项是 `checkbox`，`aria-label` 恰为 option.label；已选胶囊里的移除按钮名为 `移除 ${label}`；有已选时有「清除」按钮。

- [ ] **Step 1: 写测试（红）**

`web/src/components/MultiSelect.test.tsx`：

```tsx
import { fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";
import { expect, it } from "vitest";
import { MultiSelect, type MultiSelectOption } from "./MultiSelect";

const regions: MultiSelectOption[] = [
  { value: "HK", label: "🇭🇰 香港", count: 3 }, { value: "JP", label: "🇯🇵 日本", count: 2 }, { value: "US", label: "🇺🇸 美国", count: 1 }, { value: "", label: "未知", count: 1 },
];

function Harness({ searchable = false, foldAt }: { searchable?: boolean; foldAt?: number }) {
  const [selected, setSelected] = useState<string[]>([]);
  return <MultiSelect label="地区" options={regions} selected={selected} onChange={setSelected} searchable={searchable} foldAt={foldAt} />;
}
const group = () => within(screen.getByRole("group", { name: "地区" }));
const trigger = () => group().getByRole("button", { name: /^地区/ });

it("收起时只有触发按钮；展开后选项是带计数的复选框，勾选即生效并显示为胶囊，触发按钮带已选数", () => {
  render(<Harness />);
  expect(group().queryByRole("checkbox")).toBeNull();
  fireEvent.click(trigger());
  expect(trigger()).toHaveAttribute("aria-expanded", "true");
  expect(group().getAllByRole("checkbox").map((c) => c.getAttribute("aria-label"))).toEqual(["🇭🇰 香港", "🇯🇵 日本", "🇺🇸 美国", "未知"]);
  expect(group().getByRole("checkbox", { name: "🇭🇰 香港" }).closest("label")).toHaveTextContent("3");
  fireEvent.click(group().getByRole("checkbox", { name: "🇯🇵 日本" }));
  fireEvent.click(group().getByRole("checkbox", { name: "未知" }));
  expect(group().getByRole("checkbox", { name: "🇯🇵 日本" })).toBeChecked();
  expect(trigger()).toHaveTextContent("地区 2");
  expect(group().getByRole("button", { name: "移除 🇯🇵 日本" })).toBeInTheDocument();
  fireEvent.click(group().getByRole("button", { name: "移除 🇯🇵 日本" }));
  expect(group().getByRole("checkbox", { name: "🇯🇵 日本" })).not.toBeChecked();
  fireEvent.click(group().getByRole("button", { name: "清除" }));
  expect(trigger()).toHaveTextContent(/^地区$/);
});

it("Escape 与点击外部收起；选择保留", () => {
  render(<Harness />);
  fireEvent.click(trigger());
  fireEvent.click(group().getByRole("checkbox", { name: "🇺🇸 美国" }));
  fireEvent.keyDown(trigger(), { key: "Escape" });
  expect(trigger()).toHaveAttribute("aria-expanded", "false");
  fireEvent.click(trigger());
  fireEvent.pointerDown(document.body);
  expect(trigger()).toHaveAttribute("aria-expanded", "false");
  expect(trigger()).toHaveTextContent("地区 1");
});

it("可搜索：按折叠大小写的字面匹配过滤选项，正则特殊字符按字面处理，无匹配时说明", () => {
  render(<MultiSelect label="标签" options={[{ value: "DB", label: "DB" }, { value: "db-2", label: "db-2" }, { value: "a.b", label: "a.b" }]} selected={[]} onChange={() => {}} searchable />);
  const g = within(screen.getByRole("group", { name: "标签" }));
  fireEvent.click(g.getByRole("button", { name: /^标签/ }));
  const search = g.getByRole("searchbox", { name: "搜索标签" });
  fireEvent.change(search, { target: { value: "db" } });
  expect(g.getAllByRole("checkbox").map((c) => c.getAttribute("aria-label"))).toEqual(["DB", "db-2"]);
  fireEvent.change(search, { target: { value: "a.b" } });
  expect(g.getAllByRole("checkbox").map((c) => c.getAttribute("aria-label"))).toEqual(["a.b"]);
  fireEvent.change(search, { target: { value: "zzz" } });
  expect(g.queryByRole("checkbox")).toBeNull();
  expect(g.getByText("没有匹配的选项")).toBeInTheDocument();
});

it("已选超过 foldAt 个时只显示前几个胶囊，其余折成 +N", () => {
  render(<Harness foldAt={2} />);
  fireEvent.click(trigger());
  for (const name of ["🇭🇰 香港", "🇯🇵 日本", "🇺🇸 美国"]) fireEvent.click(group().getByRole("checkbox", { name }));
  expect(group().getAllByRole("button", { name: /^移除/ })).toHaveLength(2);
  expect(group().getByText("+1")).toBeInTheDocument();
});

// 调用方按快照规范化 selected：不在 options 里的值不画胶囊，也不计入折叠数。
it("selected 里不在选项中的值被忽略", () => {
  render(<MultiSelect label="地区" options={regions} selected={["XX", "HK"]} onChange={() => {}} />);
  expect(group().getAllByRole("button", { name: /^移除/ })).toHaveLength(1);
});
```

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/components/MultiSelect.test.tsx > /tmp/t5-red.log 2>&1; echo $?`
Expected: 1（模块不存在）。

- [ ] **Step 3: 实现**

`web/src/components/MultiSelect.tsx`：

```tsx
import { useEffect, useId, useRef, useState } from "react";
import { literalPattern } from "../lib/fold";

export type MultiSelectOption = { value: string; label: string; count?: number };

// 标签与地区由数据动态产生（设计 §2「动态集合」）：选项来自当前快照，选择集由调用方按快照规范化后传回，
// 本组件不存选择，只存开合与搜索词。已选以可移除胶囊显示，超过 foldAt 个折成「+N」。
// 搜索与标签判重同一口径（lib/fold 的折叠字面匹配）：输入 "db" 能找到 "DB"，输入 "a.b" 不会匹配 "aXb"。
export function MultiSelect({ label, options, selected, onChange, searchable = false, foldAt = 3 }: {
  label: string;
  options: readonly MultiSelectOption[];
  selected: readonly string[];
  onChange: (next: string[]) => void;
  searchable?: boolean;
  foldAt?: number;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const root = useRef<HTMLDivElement>(null);
  const listId = useId();
  useEffect(() => {
    if (!open) return;
    const away = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", away);
    return () => document.removeEventListener("pointerdown", away);
  }, [open]);
  const pattern = query === "" ? null : literalPattern(query, false);
  const shown = pattern ? options.filter((option) => pattern.test(option.label)) : options;
  const toggle = (value: string) => onChange(selected.includes(value) ? selected.filter((v) => v !== value) : [...selected, value]);
  const chips = selected.flatMap((value) => options.filter((option) => option.value === value));
  return (
    <div ref={root} className="multi-select" role="group" aria-label={label} onKeyDown={(event) => { if (event.key === "Escape") setOpen(false); }}>
      <button type="button" className="multi-select-trigger" aria-expanded={open} aria-controls={listId} onClick={() => setOpen((o) => !o)}>
        {label}{chips.length > 0 && <span className="num"> {chips.length}</span>}
      </button>
      {chips.length > 0 && (
        <ul className="multi-select-chips">
          {chips.slice(0, foldAt).map((option) => (
            <li key={option.value} className="chip">
              {option.label}
              <button type="button" aria-label={`移除 ${option.label}`} onClick={() => toggle(option.value)}>×</button>
            </li>
          ))}
          {chips.length > foldAt && <li className="chip chip-more num">+{chips.length - foldAt}</li>}
        </ul>
      )}
      {open && (
        <div className="multi-select-popup">
          {searchable && <input type="search" aria-label={`搜索${label}`} value={query} onChange={(event) => setQuery(event.target.value)} autoFocus />}
          <ul id={listId}>
            {shown.map((option) => (
              <li key={option.value}>
                <label>
                  <input type="checkbox" aria-label={option.label} checked={selected.includes(option.value)} onChange={() => toggle(option.value)} />
                  <span>{option.label}</span>
                  {option.count !== undefined && <span className="num muted">{option.count}</span>}
                </label>
              </li>
            ))}
            {shown.length === 0 && <li className="muted">没有匹配的选项</li>}
          </ul>
          {chips.length > 0 && <button type="button" className="link" onClick={() => onChange([])}>清除</button>}
        </div>
      )}
    </div>
  );
}
```

`web/src/styles.css` 末尾追加：

```css
/* 胶囊：已选标签 / 地区、「+N」。全圆角只给胶囊与状态点（设计 §2）。 */
.chip { display: inline-flex; align-items: center; gap: 4px; height: 22px; padding: 0 8px; border: 1px solid var(--line); border-radius: 999px; font-size: 12px; background: var(--card-raised); white-space: nowrap; max-width: 14rem; }
.chip > button { border: 0; background: none; padding: 0 0 0 2px; min-height: 0; color: var(--muted); line-height: 1; }
.chip > button:hover { color: var(--fg); }
/* 多选下拉（components/MultiSelect.tsx）：桌面为浮层；窄屏改为底部弹出（设计 §3.4）。 */
.multi-select { position: relative; display: inline-flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.multi-select-trigger { height: var(--control-h); }
.multi-select-trigger[aria-expanded="true"] { border-color: var(--accent); }
.multi-select-chips { display: contents; list-style: none; margin: 0; padding: 0; }
.multi-select-popup { position: absolute; z-index: 40; top: calc(100% + 4px); left: 0; min-width: 220px; max-height: 320px; overflow: auto; padding: 8px; background: var(--card-raised); border: 1px solid var(--line); border-radius: var(--radius-control); box-shadow: var(--shadow-float); }
.multi-select-popup input[type="search"] { width: 100%; margin-bottom: 6px; }
.multi-select-popup ul { list-style: none; margin: 0; padding: 0; }
.multi-select-popup label { display: flex; align-items: center; gap: 8px; min-height: 32px; padding: 0 6px; border-radius: var(--radius-control); cursor: pointer; }
.multi-select-popup label:hover { background: var(--card-hover); }
.multi-select-popup label > span:first-of-type { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.multi-select-popup input[type="checkbox"] { accent-color: var(--accent); width: 16px; height: 16px; min-height: 0; margin: 0; }
@media (max-width: 640px) {
  .multi-select-popup { position: fixed; inset: auto 0 0 0; top: auto; max-height: 60vh; border-radius: 12px 12px 0 0; }
}
```

- [ ] **Step 4: 跑测试确认绿**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/components/MultiSelect.test.tsx > /tmp/t5.log 2>&1; echo $?`
Expected: 0。

- [ ] **Step 5: 缺陷注入**

把 `pattern.test(option.label)` 换成 `option.label.includes(query)`，「可搜索」用例应红在 `db` 查不到 `DB`；改回后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/components/MultiSelect.tsx web/src/components/MultiSelect.test.tsx web/src/styles.css && git commit -m "feat(web): 动态集合用的多选下拉，已选胶囊可移除并折叠"
```

---

### Task 6: 迷你趋势线 Sparkline

**Files:**
- Create: `web/src/components/Sparkline.tsx`、`web/src/components/Sparkline.test.tsx`
- Modify: `web/src/styles.css`（追加 `.sparkline`）

**Interfaces:**
- Produces: `Sparkline({ values, label, width = 120, height = 28 }: { values: readonly (number | null | undefined)[]; label: string; width?: number; height?: number })` → `<svg role="img" aria-label={label}>`；null 处断线；没有有限值时画「无读数」文字。

- [ ] **Step 1: 写测试（红）**

`web/src/components/Sparkline.test.tsx`：

```tsx
import { render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { Sparkline } from "./Sparkline";

it("有限值连成折线，null 处断开成新的一段；y 按最大值归一到高度", () => {
  render(<Sparkline values={[0, 4, null, 2, 2]} label="下行速率" width={40} height={12} />);
  const svg = screen.getByRole("img", { name: "下行速率" });
  const d = svg.querySelector("path")?.getAttribute("d");
  // 四个点横向等分 40px：x = 0, 10, 30, 40；最大值 4 贴顶（y=1），0 贴底（y=11）。
  expect(d).toBe("M0.0 11.0 L10.0 1.0 M30.0 6.0 L40.0 6.0");
});

it("全部为 null 时没有路径，写「无读数」", () => {
  render(<Sparkline values={[null, undefined]} label="上行速率" />);
  const svg = screen.getByRole("img", { name: "上行速率" });
  expect(svg.querySelector("path")).toBeNull();
  expect(svg).toHaveTextContent("无读数");
});

it("恒为 0 画一条贴底的线，不当成无读数", () => {
  render(<Sparkline values={[0, 0]} label="x" width={10} height={12} />);
  expect(screen.getByRole("img", { name: "x" }).querySelector("path")?.getAttribute("d")).toBe("M0.0 11.0 L10.0 11.0");
});
```

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/components/Sparkline.test.tsx > /tmp/t6-red.log 2>&1; echo $?`
Expected: 1（模块不存在）。

- [ ] **Step 3: 实现**

`web/src/components/Sparkline.tsx`：

```tsx
// 迷你趋势线（设计 §2）：只给网络速率用。null 是无读数，线在这里断开而不是连过去（与 Chart 的 spanGaps 关闭同一规则）；
// 恒为 0 是一条贴底的线，不是无读数。y 按本段数据的最大值归一，没有坐标轴，所以只表达走势不表达量级。
export function Sparkline({ values, label, width = 120, height = 28 }: {
  values: readonly (number | null | undefined)[]; label: string; width?: number; height?: number;
}) {
  const finite = values.filter((v): v is number => typeof v === "number" && Number.isFinite(v));
  const max = finite.length > 0 ? Math.max(...finite) : 0;
  const step = values.length > 1 ? width / (values.length - 1) : 0;
  const segments: string[] = [];
  let pen = false;
  values.forEach((v, i) => {
    if (typeof v !== "number" || !Number.isFinite(v)) {
      pen = false;
      return;
    }
    const x = (i * step).toFixed(1);
    const y = (height - 1 - (max > 0 ? (v / max) * (height - 2) : 0)).toFixed(1);
    segments.push(`${pen ? "L" : "M"}${x} ${y}`);
    pen = true;
  });
  return (
    <svg className="sparkline" role="img" aria-label={label} width={width} height={height} viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none">
      {segments.length > 0
        ? <path d={segments.join(" ")} fill="none" stroke="currentColor" strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
        : <text x="2" y={height - 8} fontSize="10">无读数</text>}
    </svg>
  );
}
```

`web/src/styles.css` 追加：`.sparkline { display: block; color: var(--accent); max-width: 100%; }`

- [ ] **Step 4: 跑测试确认绿**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/components/Sparkline.test.tsx > /tmp/t6.log 2>&1; echo $?`
Expected: 0。

- [ ] **Step 5: 缺陷注入**

把 `pen = false` 从 null 分支删掉，第一条用例应红在 `M30.0` 变成 `L30.0`；改回后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/components/Sparkline.tsx web/src/components/Sparkline.test.tsx web/src/styles.css && git commit -m "feat(web): 网络速率迷你趋势线"
```

---

### Task 7: 横轴刻度的手机写法

**Files:**
- Modify: `web/src/lib/chartMarks.ts:79-110`（`formatChartTimes` 加 `compact`）
- Modify: `web/src/lib/chartMarks.test.ts`（追加）

**Interfaces:**
- Produces: `formatChartTimes(timestampsSec, opts?: { incrSec?: number; timeZone?: string; compact?: boolean }): string[]`。`compact` 只影响横轴（给了 `incrSec`）：刻度间隔小于一天时一律只写「时:分」，不在首格与日界补日期；间隔不小于一天时仍写「月-日」。

- [ ] **Step 1: 写测试（红）**

在 `web/src/lib/chartMarks.test.ts` 追加：

```ts
// 手机上的横轴只留时分（设计 §5）：1h / 6h / 24h 窗口刻度不足一天，跨日界也不补日期；7d / 30d 的刻度落在日界，仍写月-日。
it("compact：小于一天的刻度只写时分，跨日界不补日期；不小于一天的刻度仍写月-日", () => {
  const t0 = Date.parse("2026-10-06T22:00:00+08:00") / 1000;
  const hourly = [t0, t0 + 3600, t0 + 7200, t0 + 10800];
  expect(formatChartTimes(hourly, { incrSec: 3600, timeZone: "Asia/Shanghai" })).toEqual(["10-06 22:00", "23:00", "10-07 00:00", "01:00"]);
  expect(formatChartTimes(hourly, { incrSec: 3600, timeZone: "Asia/Shanghai", compact: true })).toEqual(["22:00", "23:00", "00:00", "01:00"]);
  const daily = [t0 + 7200, t0 + 7200 + 86_400 * 2];
  expect(formatChartTimes(daily, { incrSec: 86_400 * 2, timeZone: "Asia/Shanghai", compact: true })).toEqual(["10-07", "10-09"]);
});

it("compact 不影响图例（没有 incrSec 时仍是完整时间）", () => {
  const t = Date.parse("2026-10-06T22:00:00+08:00") / 1000;
  expect(formatChartTimes([t], { timeZone: "Asia/Shanghai", compact: true })).toEqual(["2026-10-06 22:00"]);
});
```

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/lib/chartMarks.test.ts > /tmp/t7-red.log 2>&1; echo $?; grep -m2 'expected\|Expected' /tmp/t7-red.log`
Expected: 1；红在 compact 用例得到 `"10-06 22:00"` 而非 `"22:00"`（第二条可能因类型检查通过、运行通过而绿，这是预期——它钉的是不变量）。

- [ ] **Step 3: 实现**

`web/src/lib/chartMarks.ts`：函数签名改为 `opts: { incrSec?: number; timeZone?: string; compact?: boolean } = {}`；注释段末尾补一句「compact 是手机横轴：间隔小于一天时只写时:分（日期由窗口按钮说明，窄屏放不下 `10-07 00:00`），间隔不小于一天时与非 compact 相同」；把

```ts
    const showDate = dayLevel || (prev == null ? spansDays : dayChanged);
```

改为

```ts
    const showDate = dayLevel || (!opts.compact && (prev == null ? spansDays : dayChanged));
```

- [ ] **Step 4: 跑测试确认绿**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/lib/chartMarks.test.ts > /tmp/t7.log 2>&1; echo $?`
Expected: 0。

- [ ] **Step 5: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/lib/chartMarks.ts web/src/lib/chartMarks.test.ts && git commit -m "feat(web): 横轴刻度的紧凑写法，小于一天的刻度只写时分"
```

---

### Task 8: 系列色与状态色保持距离

**Files:**
- Create: `web/src/lib/seriesPalette.ts`、`web/src/lib/seriesPalette.test.ts`

**Interfaces:**
- Produces:
  ```ts
  export const SERIES_PALETTE: readonly string[];           // 6 色小写 #rrggbb
  export function seriesColors(soft: readonly boolean[]): string[];  // 与 labels 对齐；浅线取前一条实线的颜色
  ```

- [ ] **Step 1: 写测试（红）**

`web/src/lib/seriesPalette.test.ts`：

```ts
// @vitest-environment node
// @ts-nocheck 读 styles.css 需要 node 类型（与 styles.test.ts 同一原因）。
import { readFileSync } from "node:fs";
import { expect, it } from "vitest";
import { SERIES_PALETTE, seriesColors } from "./seriesPalette";

const css = readFileSync(new URL("../styles.css", import.meta.url), "utf8");
const statusColors = [...css.matchAll(/--status-[a-z]+:\s*(#[0-9a-f]{6});/g)].map((m) => m[1]);
const rgb = (hex) => [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16));
const distance = (a, b) => Math.hypot(...rgb(a).map((v, i) => v - rgb(b)[i]));

// 系列色不与五个状态色混淆（设计 §5）：按 RGB 欧氏距离至少 70。先冒烟状态色真的读到了五个，空集合会让下面的 every 恒真。
it("五个状态色都读到了", () => {
  expect(statusColors).toHaveLength(5);
});

it.each(SERIES_PALETTE)("%s 与每个状态色的距离都 ≥ 70", (series) => {
  for (const status of statusColors) expect(distance(series, status)).toBeGreaterThanOrEqual(70);
});

it("浅线取它前面最近一条实线的颜色；实线按顺序取色、用完循环", () => {
  expect(seriesColors([false, true, false, true])).toEqual([SERIES_PALETTE[0], SERIES_PALETTE[0], SERIES_PALETTE[1], SERIES_PALETTE[1]]);
  expect(seriesColors([true, false])).toEqual([SERIES_PALETTE[0], SERIES_PALETTE[0]]);
  expect(seriesColors(Array(7).fill(false)).at(-1)).toBe(SERIES_PALETTE[0]);
});
```

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/lib/seriesPalette.test.ts > /tmp/t8-red.log 2>&1; echo $?`
Expected: 1（模块不存在）。

- [ ] **Step 3: 实现**

`web/src/lib/seriesPalette.ts`：

```ts
// 图表系列色（设计 §5）：与 styles.css 的五个状态色保持 RGB 距离 ≥70（seriesPalette.test.ts 核对），图上的线不会被读成
// 在线 / 离线 / 需关注。橙、紫、红、绿、灰系因此都不在表里；六色用完循环，对比图几十条线本来就靠悬停高亮分辨。
export const SERIES_PALETTE: readonly string[] = ["#3b82f6", "#06b6d4", "#d946ef", "#84cc16", "#fdba74", "#2dd4bf"];

// 浅线（峰值、最小 / 最大）取它前面最近一条实线的颜色：调用方把峰值紧跟在同指标的均值之后，颜色就成对；
// 第一条就是浅线时没有实线可跟，取首色。
export function seriesColors(soft: readonly boolean[]): string[] {
  let solidCount = 0;
  let current = SERIES_PALETTE[0];
  return soft.map((isSoft) => {
    if (!isSoft) current = SERIES_PALETTE[solidCount++ % SERIES_PALETTE.length];
    return current;
  });
}
```

- [ ] **Step 4: 跑测试确认绿**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/lib/seriesPalette.test.ts > /tmp/t8.log 2>&1; echo $?`
Expected: 0。

- [ ] **Step 5: 缺陷注入**

把 `#3b82f6` 临时换成 `#f59e0b`（需关注色），距离用例应红；改回后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/lib/seriesPalette.ts web/src/lib/seriesPalette.test.ts && git commit -m "feat(web): 图表系列色与状态色保持距离"
```

---

### Task 9: Chart 统一样式：自绘图例、浅线、填充带、刻度间距、占位

**Files:**
- Modify: `web/src/components/Chart.tsx`（整体重写）
- Modify: `web/src/components/Chart.test.tsx`
- Modify: `web/src/styles.css:89-94`（`.chart*`）

**Interfaces:**
- Consumes: `SERIES_PALETTE`、`seriesColors`（Task 8）、`formatChartTimes(..., { compact })`（Task 7）。
- Produces:
  ```ts
  export const COMPACT_WIDTH = 480;   // 窄于此按手机刻度
  export const X_TICK_SPACE = 80;     // 相邻刻度最小像素距离
  export function Chart(props: {
    data: AlignedData; labels: string[]; unit: string; height?: number;
    soft?: readonly boolean[];                               // 与 labels 对齐；true 画成同色浅线
    bands?: readonly { lower: number; upper: number }[];     // labels 下标，两条序列之间填充（最小 / 最大带）
    legend?: boolean;                                        // 默认 true；false 时不画图例（对比页用表格代替）
    hidden?: ReadonlySet<number>;                            // 受控的隐藏序列（labels 下标）；不传则组件自管
    onHiddenChange?: (next: ReadonlySet<number>) => void;
    onFocus?: (index: number | null) => void;                // 悬停高亮到的序列下标
  }): JSX.Element;
  ```
  DOM：`.chart > .chart-plot > (p.chart-gap? + div[data-x-ticks])` + `ul.chart-legend[aria-label="图例"]`（首项 `.legend-time`，其余每项一个 `button[aria-pressed]`，含 `.legend-swatch` 与 `.num` 值）。`data-x-ticks` 是横轴当前刻度数（画布上画的文字 DOM 里看不到，e2e 靠它验证间距）。

- [ ] **Step 1: 改测试（红）**

`web/src/components/Chart.test.tsx` 的 uPlot 替身加 `setSeries = vi.fn()`、`cursor = { idx: null as number | null }`、`width = 600`；并作如下改动：

把「数据原地更新…」用例里

```ts
    // 图例保持 uPlot 默认：显示，并且点击切换该条线。对比图靠这个隐藏单条节点。
    expect(plots[0].options.legend?.show ?? true).toBe(true);
```

改为

```ts
    // 图例由组件自绘（设计 §5：没有方块图例），uPlot 自己的关掉。
    expect(plots[0].options.legend?.show).toBe(false);
```

把「横轴与图例走本地 24 小时格式…」用例整个替换为：

```tsx
it("横轴：刻度间距 80px，刻度数写在 data-x-ticks，窄图只写时分；点过滤只留孤立读数", () => {
  const { container, unmount } = mountChart({ data: [[0, 60], [1, 2]], labels: ["cpu"], unit: "count" });
  try {
    const opts = plots.at(-1)!.options;
    const axis = opts.axes?.[0];
    expect(axis?.space).toBe(X_TICK_SPACE);
    if (typeof axis?.values !== "function") throw new Error("横轴 values 不是函数");
    const t0 = Date.parse("2026-10-06T22:00:00+08:00") / 1000;
    const ticks = [t0, t0 + 3600, t0 + 7200];
    const wide = axis.values({ width: 1200 } as uPlot, ticks, 0, 80, 3600);
    expect(wide).toEqual(formatChartTimes(ticks, { incrSec: 3600 }));
    expect(container.querySelector("[data-x-ticks]")).toHaveAttribute("data-x-ticks", "3");
    const narrow = axis.values({ width: COMPACT_WIDTH - 1 } as uPlot, ticks, 0, 80, 3600);
    expect(narrow).toEqual(formatChartTimes(ticks, { incrSec: 3600, compact: true }));
    expect(narrow.join(" ")).not.toMatch(/\d{2}-\d{2}/);
    expect(opts.axes?.[0]?.grid).toMatchObject({ width: 1 });
    const filter = opts.series?.[1]?.points?.filter;
    if (typeof filter !== "function") throw new Error("points.filter 不是函数");
    const sandwiched = [1, 2, null, 5, null, 3, 4];
    expect(filter({ data: [[], sandwiched] } as unknown as uPlot, 1, true)).toEqual(isolatedPointIndices(sandwiched));
  } finally {
    unmount();
  }
});

it("图例无悬停显示每条序列的最新读数，悬停显示该时刻的值；点击一项隐藏该序列", () => {
  const { unmount } = mountChart({ data: [[0, 60, 120], [1, 2, null], [null, 5, 6]], labels: ["CPU 均值", "CPU 峰值"], unit: "percent", soft: [false, true] });
  try {
    const legend = within(screen.getByRole("list", { name: "图例" }));
    expect(legend.getByText("最新")).toBeInTheDocument();
    expect(legend.getByRole("button", { name: /CPU 均值/ })).toHaveTextContent("2.0%");
    expect(legend.getByRole("button", { name: /CPU 峰值/ })).toHaveTextContent("6.0%");
    const plot = plots.at(-1)!;
    plot.cursor.idx = 0;
    act(() => { plot.options.hooks!.setCursor![0](plot as unknown as uPlot); });
    expect(legend.getByText(formatChartTimes([0])[0])).toBeInTheDocument();
    expect(legend.getByRole("button", { name: /CPU 均值/ })).toHaveTextContent("1.0%");
    expect(legend.getByRole("button", { name: /CPU 峰值/ })).toHaveTextContent("–");
    fireEvent.click(legend.getByRole("button", { name: /CPU 峰值/ }));
    expect(plot.setSeries).toHaveBeenLastCalledWith(2, { show: false });
    expect(legend.getByRole("button", { name: /CPU 峰值/ })).toHaveAttribute("aria-pressed", "false");
    // 浅线与它前面的实线同色、更细。
    expect(plot.options.series?.[2]?.stroke).toBe(plot.options.series?.[1]?.stroke);
    expect(plot.options.series?.[2]?.width).toBeLessThan(plot.options.series?.[1]?.width as number);
  } finally {
    unmount();
  }
});

it("受控隐藏集合与悬停回调：隐藏传给 uPlot，焦点序列回报 labels 下标，legend=false 时不画图例", () => {
  const onFocus = vi.fn();
  const { rerender, unmount } = mountChart({ data: [[0], [1], [2]], labels: ["a", "b"], unit: "ms", legend: false, hidden: new Set([1]), onFocus });
  try {
    const plot = plots.at(-1)!;
    expect(screen.queryByRole("list", { name: "图例" })).toBeNull();
    expect(plot.setSeries).toHaveBeenCalledWith(2, { show: false });
    rerender(<Chart data={[[0], [1], [2]]} labels={["a", "b"]} unit="ms" legend={false} hidden={new Set()} onFocus={onFocus} />);
    expect(plot.setSeries).toHaveBeenLastCalledWith(2, { show: true });
    // uPlot 运行时在悬停时给 setSeries 钩子传 { focus: true }，d.ts 把 opts 声明成 Series，这里按运行时形状传。
    const focus = { focus: true } as unknown as uPlot.Series;
    plot.options.hooks!.setSeries![0](plot as unknown as uPlot, 1, focus);
    expect(onFocus).toHaveBeenLastCalledWith(0);
    plot.options.hooks!.setSeries![0](plot as unknown as uPlot, null, focus);
    expect(onFocus).toHaveBeenLastCalledWith(null);
    // 自己调用 setSeries 切换显示时 uPlot 也会触发同一个钩子，这不是悬停。
    onFocus.mockClear();
    plot.options.hooks!.setSeries![0](plot as unknown as uPlot, 1, { show: false });
    expect(onFocus).not.toHaveBeenCalled();
  } finally {
    unmount();
  }
});

it("填充带按 labels 下标换算成 uPlot 序列下标，取上界序列的颜色", () => {
  const { unmount } = mountChart({ data: [[0], [1], [0], [2]], labels: ["均值", "最小", "最大"], unit: "ms", soft: [false, true, true], bands: [{ lower: 1, upper: 2 }] });
  try {
    const opts = plots.at(-1)!.options;
    expect(opts.bands).toEqual([{ series: [3, 2], fill: `${opts.series![3].stroke}33` }]);
  } finally {
    unmount();
  }
});
```

`mountChart` 的参数类型改为 `Parameters<typeof Chart>[0]`；import 行加 `within, fireEvent`、`COMPACT_WIDTH, X_TICK_SPACE`（从 `./Chart`）。「无读数提示在图的上方」用例不改（提示仍在 uPlot 根之前，只是靠 CSS 叠到图区中央）。

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/components/Chart.test.tsx > /tmp/t9-red.log 2>&1; echo $?`
Expected: 1；红的原因是 `legend.show` 仍为 undefined、`axis.space` undefined、没有「图例」列表。

- [ ] **Step 3: 重写 Chart**

`web/src/components/Chart.tsx` 整体替换为：

```tsx
import { type CSSProperties, useEffect, useMemo, useRef, useState } from "react";
import uPlot, { type AlignedData, type Options } from "uplot";
import "uplot/dist/uPlot.min.css";
import { formatUnit } from "../lib/format";
import { axisValues } from "../lib/axis";
import { formatChartTimes, isolatedPointIndices, readingGap, type ReadingGap } from "../lib/chartMarks";
import { resolveColor, useColorScheme } from "../lib/colorScheme";
import { seriesColors } from "../lib/seriesPalette";

// 窄于这个宽度的图按手机刻度：横轴只写时分（设计 §5）。
export const COMPACT_WIDTH = 480;
// 相邻刻度至少相隔的像素（设计 §5）：uPlot 据此从画布宽度算刻度数，节点页与对比页都不各自设刻度。
export const X_TICK_SPACE = 80;

type Column = readonly (number | null | undefined)[];

function yColumns(data: AlignedData): Column[] {
  const cols: Column[] = [];
  for (let i = 1; i < data.length; i++) cols.push(data[i] as Column);
  return cols;
}

// 文案与「窗口内没有探测结果」同一语气。图级与序列级分开：全部没有读数时不把每条序列的名字再列一遍。
function gapMessage(gap: ReadingGap): string | null {
  if (gap.kind === "chart") return "窗口内没有读数";
  if (gap.kind === "series") return `${gap.labels.join("、")}：窗口内没有读数`;
  return null;
}

function isReading(v: number | null | undefined): v is number {
  return typeof v === "number" && Number.isFinite(v);
}

function lastReadingIndex(col: Column): number | null {
  for (let i = col.length - 1; i >= 0; i--) if (isReading(col[i])) return i;
  return null;
}

type ChartProps = {
  data: AlignedData;
  labels: string[];
  unit: string;
  height?: number;
  soft?: readonly boolean[];
  bands?: readonly { lower: number; upper: number }[];
  legend?: boolean;
  hidden?: ReadonlySet<number>;
  onHiddenChange?: (next: ReadonlySet<number>) => void;
  onFocus?: (index: number | null) => void;
};

// spanGaps 关闭：null 是无读数，线在这里必须断开而不是把两侧连起来。
// 时间不传时区：formatChartTimes 与 uPlot 的刻度对齐都用浏览器本地时区，不引入 hub 时区。
// 图例由本组件自绘（设计 §5）：无悬停时每条序列显示它最后一个有限读数，悬停时显示光标所在时刻的值；点击一项隐藏该序列。
// hidden 受控时由调用方持有集合（对比页的表格开关），否则组件自管。
export function Chart({ data, labels, unit, height = 180, soft, bands = [], legend = true, hidden, onHiddenChange, onFocus }: ChartProps) {
  const el = useRef<HTMLDivElement>(null);
  const plot = useRef<uPlot | null>(null);
  const initialData = useRef(data);
  const key = labels.join("|");
  const softFlags = useMemo(() => labels.map((_, i) => soft?.[i] ?? false), [key, soft]);
  const softKey = softFlags.map((s) => (s ? 1 : 0)).join("");
  const bandKey = bands.map((b) => `${b.lower}-${b.upper}`).join("|");
  const colors = useMemo(() => seriesColors(softFlags), [softKey]);
  const scheme = useColorScheme();
  const gap = gapMessage(readingGap(yColumns(data), labels));
  const [cursorIdx, setCursorIdx] = useState<number | null>(null);
  const [ownHidden, setOwnHidden] = useState<ReadonlySet<number>>(() => new Set());
  const shownHidden = hidden ?? ownHidden;
  const onFocusRef = useRef(onFocus);
  onFocusRef.current = onFocus;
  const hiddenRef = useRef(shownHidden);
  hiddenRef.current = shownHidden;
  useEffect(() => {
    const host = el.current;
    if (!host) return;
    const axisColor = resolveColor(host, "var(--muted)");
    const gridColor = resolveColor(host, "var(--line)");
    const axisStyle = { stroke: axisColor, grid: { stroke: gridColor, width: 1 }, ticks: { show: false } };
    const opts: Options = {
      width: host.clientWidth || 600,
      height,
      legend: { show: false },
      cursor: { focus: { prox: 16 } },
      focus: { alpha: 0.25 },
      scales: { x: { time: true }, y: unit === "percent" ? { range: [0, 100] } : {} },
      axes: [
        {
          ...axisStyle,
          space: X_TICK_SPACE,
          // 刻度文字画在 canvas 上，DOM 里看不到；把刻度数写到宿主上，e2e 才能核对间距规则真的起了作用。
          values: (u, splits, _axisIdx, _foundSpace, foundIncr) => {
            host.dataset.xTicks = String(splits.length);
            return formatChartTimes(splits, { incrSec: foundIncr, compact: u.width < COMPACT_WIDTH });
          },
        },
        { ...axisStyle, size: 80, values: (_u, vals) => axisValues(vals, unit) },
      ],
      // uPlot 的 bands 用序列下标（0 是时间轴），本组件对外用 labels 下标；填充取上界序列的颜色加 20% 透明度。
      bands: bands.map((b) => ({ series: [b.upper + 1, b.lower + 1], fill: `${colors[b.upper]}33` })),
      series: [
        { label: "时间" },
        ...labels.map((label, i) => ({
          label,
          stroke: colors[i],
          width: softFlags[i] ? 1 : 1.5,
          alpha: softFlags[i] ? 0.55 : 1,
          spanGaps: false,
          // 默认点填充是白色，浅色背景上只剩细描边。填成与线相同的颜色，孤立读数才是一粒看得见的实心点。
          // filter 必须返回数组：返回 null 时 uPlot 会把可见范围内的每个点都画出来。
          points: {
            show: true,
            fill: colors[i],
            filter: (u: uPlot, seriesIdx: number) => isolatedPointIndices(u.data[seriesIdx] as Column),
          },
        })),
      ],
      hooks: {
        setCursor: [(u) => setCursorIdx(u.cursor.idx ?? null)],
        // 悬停高亮经 cursor.focus 触发 setSeries 且 opts 带 focus；本组件自己调 setSeries 切显示时 opts 只有 show。
        setSeries: [(_u, idx, seriesOpts: { focus?: boolean; show?: boolean }) => {
          if (!("focus" in seriesOpts)) return;
          onFocusRef.current?.(idx == null ? null : idx - 1);
        }],
      },
    };
    const u = new uPlot(opts, initialData.current, host);
    plot.current = u;
    for (const i of hiddenRef.current) u.setSeries(i + 1, { show: false });
    const ro = new ResizeObserver(() => plot.current?.setSize({ width: host.clientWidth, height }));
    ro.observe(host);
    return () => {
      ro.disconnect();
      plot.current?.destroy();
      plot.current = null;
    };
    // 标签、单位、尺寸、明暗、浅线与填充带改变才重建；下面的数据 effect 维护最近提交的数据快照并应用当前数据。
  }, [key, unit, height, scheme, softKey, bandKey, colors]);
  useEffect(() => {
    initialData.current = data;
    plot.current?.setData(data);
  }, [data]);
  useEffect(() => {
    const u = plot.current;
    if (!u) return;
    labels.forEach((_, i) => u.setSeries(i + 1, { show: !shownHidden.has(i) }));
  }, [shownHidden, key]);
  const toggle = (i: number) => {
    const next = new Set(shownHidden);
    if (next.has(i)) next.delete(i);
    else next.add(i);
    if (hidden === undefined) setOwnHidden(next);
    onHiddenChange?.(next);
  };
  const xs = data[0] as Column;
  // uPlot 把自己的根节点 append 进宿主。提示若也放在宿主里，图建好之后才出现的提示会被 React 追加到
  // uPlot 根之后，位置就随提示与图谁先出现而变；所以宿主只交给 uPlot，提示放在宿主之前、由 CSS 叠到图区中央。
  return (
    <div className="chart">
      <div className="chart-plot">
        {gap && <p className="muted chart-gap">{gap}</p>}
        <div ref={el} />
      </div>
      {legend && labels.length > 0 && (
        <ul className="chart-legend" aria-label="图例">
          <li className="legend-time num">{cursorIdx == null || !isReading(xs[cursorIdx]) ? "最新" : formatChartTimes([xs[cursorIdx]])[0]}</li>
          {labels.map((label, i) => {
            const col = data[i + 1] as Column;
            const at = cursorIdx ?? lastReadingIndex(col);
            const v = at == null ? null : col[at];
            const off = shownHidden.has(i);
            return (
              <li key={label}>
                <button type="button" aria-pressed={!off} style={{ "--series": colors[i] } as CSSProperties} onClick={() => toggle(i)}>
                  <span className={softFlags[i] ? "legend-swatch soft" : "legend-swatch"} aria-hidden="true" />
                  {label}
                  <span className="num">{isReading(v) ? formatUnit(v, unit) : "–"}</span>
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
```

`web/src/styles.css` 第 89–94 行（`.chart` 到 `.chart .u-legend…`）替换为：

```css
/* 图表（components/Chart.tsx）：占位说明叠在图区中央；图例自绘，线段色样代替方块（设计 §5）。 */
.chart { width: 100%; min-width: 0; }
.chart-plot { position: relative; }
.chart-gap { position: absolute; z-index: 1; left: 0; right: 0; top: 40%; margin: 0; text-align: center; font-size: 12px; pointer-events: none; }
.chart .uplot { width: 100%; max-width: 100%; }
.chart-legend { list-style: none; margin: 6px 0 0; padding: 0; display: flex; flex-wrap: wrap; gap: 2px 14px; font-size: 12px; }
.chart-legend li { min-width: 0; }
.chart-legend .legend-time { color: var(--muted); }
.chart-legend button { display: inline-flex; align-items: center; gap: 6px; min-height: 24px; padding: 0 2px; border: 0; background: none; color: var(--fg); max-width: 100%; }
.chart-legend button[aria-pressed="false"] { color: var(--faint); text-decoration: line-through; }
.legend-swatch { width: 14px; height: 0; border-top: 2px solid var(--series); flex: none; }
.legend-swatch.soft { border-top-width: 1px; opacity: .6; }
.chart-legend .num { color: var(--muted); }
.chart-legend button[aria-pressed="true"] .num { color: var(--fg); }
```

- [ ] **Step 4: 跑测试确认绿，再跑全量与类型检查**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/components/Chart.test.tsx > /tmp/t9.log 2>&1; echo $?`
Expected: 0。

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm typecheck > /tmp/t9-tsc.log 2>&1; echo $?; pnpm vitest run > /tmp/t9-all.log 2>&1; echo $?`
Expected: 两个 0。`History.test.tsx`、`NodePage.test.tsx`、`ProbeComparison.test.tsx` 都 mock 了 Chart，不受影响。

- [ ] **Step 5: 缺陷注入**

把 `if (!("focus" in seriesOpts)) return;` 删掉，「受控隐藏集合与悬停回调」应红在「自己调用 setSeries 时 onFocus 被调用」；把 `space: X_TICK_SPACE` 删掉，「横轴」用例应红在 `space`。改回后绿。

- [ ] **Step 6: 真机冒烟**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm dev --mode public > /tmp/t9-dev.log 2>&1 &` 然后用 web-access（或 Playwright MCP）打开 `http://localhost:5173/nodes/<任一公开节点 id>`，确认图例能看到最新值、悬停随光标变化、点击一项线消失。本步只看渲染与交互，不下结论到刻度数（刻度在 Task 20 的 e2e 里核对）。完成后结束 dev 进程。

- [ ] **Step 7: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/components/Chart.tsx web/src/components/Chart.test.tsx web/src/styles.css && git commit -m "feat(web): 图表统一样式：自绘图例显示最新值，浅线与填充带，刻度按 80px 间距"
```

---

### Task 10: History 拆分：指标图、探测面板与每任务 RTT 图

**Files:**
- Modify: `web/src/lib/probes.ts`（加 `rttMinMs`、`rttMaxMs`、`toProbeTaskAligned`）、`web/src/lib/probes.test.ts`
- Modify: `web/src/components/History.tsx`（`RangeStatus` 口径进 ⓘ；`useHistory` 加 `probeTaskCharts`；拆出 `MetricCharts`、`ProbePanels`、`ProbeTaskCharts`）
- Modify: `web/src/components/History.test.tsx`、`web/src/public/NodePage.test.tsx:84`（`parentElement` 断言）

**Interfaces:**
- Consumes: `InfoTip`（Task 3）、`Chart` 的 `soft` / `bands`（Task 9）。
- Produces:
  ```ts
  export const rttMinMs: ProbeValue; export const rttMaxMs: ProbeValue;
  export function toProbeTaskAligned(resp: QueryProbesResponse, taskId: bigint, from: number, to: number, values: readonly ProbeValue[]): AlignedData;
  // History
  export function RangeStatus(props: { shown?: { level: string; stepS: number } | null; stale: boolean; rangeLabel: string; updating?: boolean; note?: ReactNode }): JSX.Element;
  //   shown 时渲染 <InfoTip label="口径说明">级别 {level}，每点 {stepS} 秒。{note}</InfoTip>；note 默认为峰值说明 PEAK_NOTE
  export const PEAK_NOTE: string;   // "峰值为每个图表时间桶内已采集样本的最大值，不代表采样间隔内的瞬时最高值；缺少峰值时留空。"
  // useHistory 返回值新增：
  //   charts[i].soft: boolean[]（selections 里 value === "max" 的位置为 true）
  //   probeTaskCharts: { taskId: bigint; kind: ProbeKind; title: string; labels: string[]; soft: boolean[]; bands: {lower:number;upper:number}[]; unit: "ms"; data: AlignedData }[]
  export function MetricCharts({ history, showCoverage }: { history: HistoryState; showCoverage?: boolean }): JSX.Element;
  export function ProbePanels({ history, noProbes, probeFooter }: { history: HistoryState; noProbes: ReactNode; probeFooter?: ReactNode }): JSX.Element;
  export function ProbeTaskCharts({ history, noProbes, titleLink }: { history: HistoryState; noProbes: ReactNode; titleLink: (taskId: bigint, title: string) => ReactNode }): JSX.Element;
  export function HistoryCharts(props): JSX.Element;   // = MetricCharts + ProbePanels，签名不变，管理端 NodeDetail 继续用
  ```

- [ ] **Step 1: 写测试（红）**

在 `web/src/lib/probes.test.ts` 追加：

```ts
it("每任务对齐：均值 / 最小 / 最大三列同网格，没有成功探测的点三列都是 null", () => {
  const resp = create(QueryProbesResponseSchema, { level: "1m", stepS: 60, series: [
    { taskId: 3n, samples: [{ ts: 60n, sent: 4, lost: 0, errors: 0, rttMeanUs: 20_000, rttMinUs: 10_000, rttMaxUs: 30_000 }, { ts: 120n, sent: 4, lost: 4, errors: 0 }] },
  ] });
  expect(toProbeTaskAligned(resp, 3n, 60, 240, [rttMeanMs, rttMinMs, rttMaxMs])).toEqual([[60, 120, 180], [20, null, null], [10, null, null], [30, null, null]]);
  expect(toProbeTaskAligned(resp, 9n, 60, 180, [rttMeanMs])).toEqual([[60, 120], [null, null]]);
});
```

（文件顶部若没有，补 `import { create } from "@bufbuild/protobuf"; import { QueryProbesResponseSchema } from "../gen/heron/v1/query_pb";` 与 `rttMinMs, rttMaxMs, toProbeTaskAligned` 的 import。）

在 `web/src/components/History.test.tsx`：
1. Chart 替身加 `data-soft={JSON.stringify(soft ?? [])}` 与 `data-bands={JSON.stringify(bands ?? [])}`（替身签名加 `soft?: boolean[]; bands?: unknown[]`）。
2. 「均值与峰值不混用」用例的 `expectedPanels` 每项加 `soft` 字段，并在读取时加 `soft: JSON.parse(chart.dataset.soft!)`：CPU `[false, true]`、内存 `[false, true, false]`、网络 `[false, false, true, true]`、磁盘速率 `[false, false]`、steal `[false, false]`、负载 `[false]`、按核负载 `[false]`。
3. 该用例末尾的 `expect(screen.getByText("峰值为…")).toBeInTheDocument()` 改为 `expect(screen.getByRole("tooltip")).toHaveTextContent("级别 1m，每点 60 秒。峰值为每个图表时间桶内已采集样本的最大值")`。
4. 追加用例：

```tsx
it("公开节点页每个探测任务一张 RTT 图：均值实线，最小 / 最大浅线并在两者之间填充；标题链到对比页", async () => {
  vi.spyOn(Date, "now").mockReturnValue(86_400_000);
  renderWithService(PublicService, {
    getSnapshot: async () => ({ nodes: [{ id: 7n, name: "node", online: true, lastSeenAt: 1n }] }),
    queryMetrics,
    queryProbes: async () => create(QueryProbesResponseSchema, { level: "1m", stepS: 60, series: [
      { taskId: 3n, kind: ProbeKind.TCP, target: "example.com:443", samples: [{ ts: 86_340n, sent: 4, lost: 0, errors: 0, rttMeanUs: 20_000, rttMinUs: 10_000, rttMaxUs: 30_000 }] },
      { taskId: 4n, kind: ProbeKind.UNSPECIFIED, target: "", samples: [] },
    ] }),
  }, [{ path: "/nodes/:id", Component: NodePage }], "/nodes/7");
  const heading = await screen.findByRole("heading", { name: "TCP example.com:443" });
  expect(within(heading).getByRole("link")).toHaveAttribute("href", "/probes/3");
  const chart = within(heading.parentElement!).getByTestId("chart");
  expect(chart.dataset.labels).toBe("RTT 均值,最小,最大");
  expect(chart.dataset.soft).toBe("[false,true,true]");
  expect(chart.dataset.bands).toBe('[{"lower":1,"upper":2}]');
  expect(chart.dataset.unit).toBe("ms");
  // 未标注（已撤下）的任务只有编号，没有对比页链接。
  const gone = screen.getByRole("heading", { name: "任务 #4" });
  expect(within(gone).queryByRole("link")).toBeNull();
  expect(screen.queryByRole("heading", { name: "探测 · 丢包率" })).toBeNull();
});
```

（import 补 `QueryProbesResponseSchema`、`ProbeKind`。）

在 `web/src/public/NodePage.test.tsx` 把 `/级别 1m，每点 60s/` 全部改为 `/级别 1m，每点 60 秒/`，第 84 行 `expect(screen.getByText(/级别 1m，每点 60 秒/).parentElement).toBe(rangeHeader())` 改为 `expect(screen.getByText(/级别 1m，每点 60 秒/).closest("header")).toBe(rangeHeader())`；首个用例里 `expect(screen.getAllByTestId("chart")).toHaveLength(12)` 改为 `11`（10 张指标图 + 1 个任务）。`web/src/components/ProbeComparison.test.tsx` 中出现的 `每点 60s` 同样改为 `每点 60 秒`（grep 确认处数）。

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/lib/probes.test.ts src/components/History.test.tsx src/public/NodePage.test.tsx > /tmp/t10-red.log 2>&1; echo $?`
Expected: 1；probes 红在 `toProbeTaskAligned` 不存在，History 红在 `data-soft` 为 `[]`、tooltip 不存在、`TCP example.com:443` 标题不存在。

- [ ] **Step 3: 实现 lib/probes.ts**

在 `rttMeanMs` 下方追加：

```ts
export const rttMinMs: ProbeValue = (s) => (s.rttMinUs === undefined ? null : s.rttMinUs / 1000);
export const rttMaxMs: ProbeValue = (s) => (s.rttMaxUs === undefined ? null : s.rttMaxUs / 1000);

// 一个任务的几种取值各一列（均值、最小、最大），同一网格：Chart 据此画均值实线与最小 / 最大之间的填充带。
export function toProbeTaskAligned(resp: QueryProbesResponse, taskId: bigint, from: number, to: number, values: readonly ProbeValue[]): AlignedData {
  const xs = gridOf(resp.stepS, from, to);
  const at = new Map<number, ProbeSample>();
  resp.series.find((s) => s.taskId === taskId)?.samples.forEach((s) => at.set(Number(s.ts), s));
  const columns = values.map((value) => xs.map((t) => {
    const s = at.get(t);
    return s === undefined ? null : value(s);
  }));
  return [xs, ...columns] as AlignedData;
}
```

- [ ] **Step 4: 实现 History.tsx**

1. import 加 `import { InfoTip } from "./InfoTip";`、`rttMaxMs, rttMinMs, toProbeTaskAligned`、`import type { ProbeKind } from "../gen/heron/v1/types_pb";`。
2. `PANELS` 上方加常量：

```ts
// 峰值口径随 ⓘ 展示（设计 §2：口径说明不占首屏）。
export const PEAK_NOTE = "峰值为每个图表时间桶内已采集样本的最大值，不代表采样间隔内的瞬时最高值；缺少峰值时留空。";
```

3. `RangeStatus` 改为：

```tsx
// 时间窗口按钮的伴随内容：级别与口径进 ⓘ，「非当前窗口」提示与刷新中仍是可见文字（它们是当下的状态，不是口径）。
// 几段之间没有分隔符，只靠 .row 头部的 gap 隔开，调用方要把 RangeStatus 与 RangeButtons 放进同一个 .row 头部。
export function RangeStatus({ shown, stale, rangeLabel, updating = false, note = PEAK_NOTE }: {
  shown?: { level: string; stepS: number } | null; stale: boolean; rangeLabel: string; updating?: boolean; note?: ReactNode;
}) {
  return (
    <>
      {shown && <InfoTip label="口径说明">级别 {shown.level}，每点 {shown.stepS} 秒。{note}</InfoTip>}
      {stale && <span className="muted">{rangeStaleText(rangeLabel)}</span>}
      {shown && updating && <span className="muted">更新中</span>}
    </>
  );
}
```

4. `useHistory` 里 `charts` 的 map 加 `soft: p.selections.map((selection) => selection.value === "max"),`；`probeCharts` 之后加：

```ts
  // 每任务一张图（公开节点页，设计 §3.2）：均值实线，最小 / 最大浅线并在两者之间填充。标签同 seriesLabels。
  const probeTaskCharts = useMemo(() => {
    if (!probes.data) return [];
    const titles = seriesLabels(probes.data.series);
    return probes.data.series.map((s, i) => ({
      taskId: s.taskId,
      kind: s.kind as ProbeKind,
      title: titles[i],
      labels: ["RTT 均值", "最小", "最大"],
      soft: [false, true, true],
      bands: [{ lower: 1, upper: 2 }],
      unit: "ms",
      data: toProbeTaskAligned(probes.data!, s.taskId, from, to, [rttMeanMs, rttMinMs, rttMaxMs]),
    }));
  }, [probes.data, from, to]);
```

并把返回值改为 `{ range, setRange, metrics, probes, charts, probeCharts, probeTaskCharts, rangeStale }`。

5. `HistoryCharts` 拆成三个导出 + 一个组合：

```tsx
// 指标图。showCoverage 默认不显示：本组件由管理端与公开页共用，覆盖率口径（hub 的观测与保留期、节点首报）
// 只在管理端展示；默认方向取"不显示"，新调用方忘记传参时覆盖率不会被带到公开页。
export function MetricCharts({ history, showCoverage = false }: { history: HistoryState; showCoverage?: boolean }) {
  const { charts, metrics } = history;
  const coverage = coverageView(metrics.data?.coverageSummary);
  return (
    <>
      {/* 覆盖率取与图表同一次 QueryMetrics 响应的 coverageSummary，不另发请求；旧 hub 没有这个字段，
          absent 时整项不显示（不显示 0%、也不显示"未知"）。 */}
      {showCoverage && coverage.kind !== "absent" && (
        <p className="muted">
          {coverage.kind === "no-start" && "尚无覆盖记录"}
          {coverage.kind === "no-observed" && "无可观测区间"}
          {coverage.kind === "rate" && <>上报覆盖 {coverage.percent}%{coverage.unknown && <>，未知 {coverage.unknown}</>}</>}
          。这是 hub 观测到的分钟里节点有上报的比例，不是在线率；hub 未运行、超出保留期等无法观测的时段计为未知。
        </p>
      )}
      <div className="grid">
        {charts.map((c) => (
          <div className="card" key={c.title}>
            <h2>{c.title}</h2>
            <Chart data={c.data} labels={c.labels} unit={c.unit} soft={c.soft} />
          </div>
        ))}
      </div>
    </>
  );
}

// 探测的两张面板（丢包率、RTT 均值，每任务一条线）：管理端节点详情沿用。noProbes 是窗口内没有探测结果时的说明。
export function ProbePanels({ history, noProbes, probeFooter = null }: { history: HistoryState; noProbes: ReactNode; probeFooter?: ReactNode }) {
  const { probeCharts, probes } = history;
  if (!probes.data) return null;
  if (probes.data.series.length === 0) return <>{noProbes}</>;
  return (
    <>
      <div className="grid">
        {probeCharts.map((c) => (
          <div className="card" key={c.title}>
            <h2>{c.title}</h2>
            <Chart data={c.data} labels={c.labels} unit={c.unit} />
          </div>
        ))}
      </div>
      {probeFooter}
    </>
  );
}

// 每任务一张 RTT 图（公开节点页）。titleLink 由调用方给：公开页链到 /probes/:id，未标注（kind 为 UNSPECIFIED）的任务没有
// 对比页可去，只给标题文字。
export function ProbeTaskCharts({ history, noProbes, titleLink }: { history: HistoryState; noProbes: ReactNode; titleLink: (taskId: bigint, title: string) => ReactNode }) {
  const { probeTaskCharts, probes } = history;
  if (!probes.data) return null;
  if (probes.data.series.length === 0) return <>{noProbes}</>;
  return (
    <div className="grid">
      {probeTaskCharts.map((c) => (
        <div className="card" key={String(c.taskId)}>
          <h2>{c.kind === 0 ? c.title : titleLink(c.taskId, c.title)}</h2>
          <Chart data={c.data} labels={c.labels} unit={c.unit} soft={c.soft} bands={c.bands} />
        </div>
      ))}
    </div>
  );
}

export function HistoryCharts({ history, noProbes, showCoverage = false, probeFooter = null }: { history: HistoryState; noProbes: ReactNode; showCoverage?: boolean; probeFooter?: ReactNode }) {
  return (
    <>
      <MetricCharts history={history} showCoverage={showCoverage} />
      <ProbePanels history={history} noProbes={noProbes} probeFooter={probeFooter} />
    </>
  );
}
```

（`c.kind === 0` 即 `ProbeKind.UNSPECIFIED`；用 `ProbeKind.UNSPECIFIED` 需要值 import，`import { ProbeKind } from "../gen/heron/v1/types_pb"` 不含管理服务代码，可以直接用，优先写成 `c.kind === ProbeKind.UNSPECIFIED`。）

6. 公开 `NodePage.tsx` 暂时把 `<HistoryCharts … probeFooter={<ProbeLinks …/>} />` 改为 `<MetricCharts history={history} /><ProbeTaskCharts history={history} noProbes={<p className="muted">窗口内没有探测结果。</p>} titleLink={(id, title) => <Link to={`/probes/${id}`}>{title}</Link>} />`，删掉 `ProbeLinks`（Task 17 还会整体重写 NodePage）。

- [ ] **Step 5: 跑测试确认绿，再跑全量**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/lib/probes.test.ts src/components/History.test.tsx src/public/NodePage.test.tsx > /tmp/t10.log 2>&1; echo $?`
Expected: 0。

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm typecheck > /tmp/t10-tsc.log 2>&1; echo $?; pnpm vitest run > /tmp/t10-all.log 2>&1; echo $?`
Expected: 两个 0（`ProbeComparison.test.tsx` 的 `每点 60 秒` 已同步）。

- [ ] **Step 6: 缺陷注入**

把 `probeTaskCharts` 的 `soft` 改成 `[false, false, false]`，History 新用例应红在 `data-soft`；改回后绿。

- [ ] **Step 7: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/lib/probes.ts web/src/lib/probes.test.ts web/src/components/History.tsx web/src/components/History.test.tsx web/src/public/NodePage.tsx web/src/public/NodePage.test.tsx web/src/components/ProbeComparison.test.tsx && git commit -m "feat(web): 历史图口径进 ⓘ，峰值画成浅线，公开节点页每任务一张带最小最大带的 RTT 图"
```

---

## 公开页

### Task 11: 搜索、筛选、排序、分组与汇总的纯函数

**Files:**
- Create: `web/src/public/filters.ts`、`web/src/public/filters.test.ts`

**Interfaces:**
- Consumes: `nodeStatus`、`usageLevel`、`expiryLevel`、`STATUS_ORDER`（Task 2）、`literalPattern`（`lib/fold.ts`）、`matchesTags`（`lib/tags.ts`）、`sortByExpiry`（`lib/billing.ts`）、`ratio`（`components/Bar.tsx`）、`flag`（`lib/country.ts`）。
- Produces:
  ```ts
  export type PublicFilters = { search: string; regions: readonly string[]; tags: readonly string[]; onlineOnly: boolean };
  export const NO_FILTERS: PublicFilters;
  export function matchesSearch(node: PublicNode, search: string): boolean;         // 名称、标签、公开备注；折叠大小写的字面匹配
  export function filterPublicNodes(nodes: readonly PublicNode[], f: PublicFilters): PublicNode[];
  export type CardSort = "default" | "expiry" | "cpu" | "traffic";
  export const CARD_SORTS: readonly { value: CardSort; label: string }[];           // 默认 / 到期 / CPU / 流量
  export function sortCards(nodes: readonly PublicNode[], sort: CardSort): PublicNode[];
  export type ColorBy = "status" | "cpu" | "memory" | "expiry";
  export const COLOR_BYS: readonly { value: ColorBy; label: string }[];             // 状态 / CPU / 内存 / 到期
  export function tileLevel(node: PublicNode, by: ColorBy): Level | undefined;      // status 或无读数时 undefined
  export function regionName(code: string): string;                                  // Intl.DisplayNames zh-CN；"" → 未知；失败回退代码
  export type RegionOption = { value: string; label: string; count: number };
  export function regionOptions(nodes: readonly PublicNode[]): RegionOption[];      // 按代码排序，未知最后；label = `${flag} ${regionName}`
  export type RegionGroup = { code: string; name: string; nodes: PublicNode[]; online: number };
  export function groupByRegion(nodes: readonly PublicNode[]): RegionGroup[];       // 按在线数降序，再总数降序，再代码；未知最后
  export type Summary = { total: number; counts: Record<NodeStatus, number>; rxBps: bigint; txBps: bigint; periodBytes: bigint };
  export function summarize(nodes: readonly PublicNode[]): Summary;                 // 速率只累计在线与维护中且有读数的节点；本周期流量累计全部
  ```

- [ ] **Step 1: 写测试（红）**

`web/src/public/filters.test.ts`：

```ts
import { create } from "@bufbuild/protobuf";
import { expect, it } from "vitest";
import { PublicNodeSchema, type PublicNode } from "../gen/heron/v1/public_pb";
import { filterPublicNodes, groupByRegion, matchesSearch, NO_FILTERS, regionName, regionOptions, sortCards, summarize, tileLevel } from "./filters";

const node = (init: Parameters<typeof create<typeof PublicNodeSchema>>[1]): PublicNode => create(PublicNodeSchema, init);
const nodes = [
  node({ id: 1n, name: "tokyo-1", country: "JP", online: true, lastSeenAt: 9n, tags: ["prod"], metrics: { cpuPct: 95, memUsed: 8n, memTotal: 10n, netRxBps: 100n, netTxBps: 10n }, traffic: { periodRx: 5n, periodTx: 5n } }),
  node({ id: 2n, name: "tokyo-2", country: "JP", online: false, lastSeenAt: 1n, tags: ["prod", "DB"], publicRemark: "联通 4837", metrics: { cpuPct: 10, netRxBps: 999n }, billing: { expiresOn: "2030-01-01", daysLeft: 12 } }),
  node({ id: 3n, name: "hk-1", country: "HK", online: true, lastSeenAt: 9n, maintenance: true, metrics: { cpuPct: 50, netRxBps: 1n, netTxBps: 1n }, traffic: { periodRx: 1n, periodTx: 0n } }),
  node({ id: 4n, name: "fresh", country: "", online: false }),
  node({ id: 5n, name: "hk-2", country: "HK", online: true, lastSeenAt: 9n, metrics: { cpuPct: 72 }, billing: { expiresOn: "2026-01-01", daysLeft: -3 } }),
];
const names = (list: readonly PublicNode[]) => list.map((n) => n.name);

it("搜索匹配名称、标签、公开备注，折叠大小写，正则特殊字符按字面；空串匹配一切", () => {
  expect(matchesSearch(nodes[1], "db")).toBe(true);
  expect(matchesSearch(nodes[1], "4837")).toBe(true);
  expect(matchesSearch(nodes[1], "TOKYO")).toBe(true);
  expect(matchesSearch(nodes[1], "t.k")).toBe(false);
  expect(matchesSearch(nodes[3], "")).toBe(true);
});

it("筛选取交集：地区之间取并集，标签取交集，只看在线按四态（维护中不算在线）", () => {
  expect(names(filterPublicNodes(nodes, NO_FILTERS))).toEqual(["tokyo-1", "tokyo-2", "hk-1", "fresh", "hk-2"]);
  expect(names(filterPublicNodes(nodes, { ...NO_FILTERS, regions: ["JP", ""] }))).toEqual(["tokyo-1", "tokyo-2", "fresh"]);
  expect(names(filterPublicNodes(nodes, { ...NO_FILTERS, tags: ["prod", "db"] }))).toEqual(["tokyo-2"]);
  expect(names(filterPublicNodes(nodes, { ...NO_FILTERS, onlineOnly: true }))).toEqual(["tokyo-1", "hk-2"]);
  expect(names(filterPublicNodes(nodes, { ...NO_FILTERS, search: "hk", onlineOnly: true }))).toEqual(["hk-2"]);
});

it("卡片排序：到期用 daysLeft（已过期最前、无到期最后），CPU 与流量降序且缺读数最后，默认保持面板顺序", () => {
  expect(names(sortCards(nodes, "default"))).toEqual(names(nodes));
  expect(names(sortCards(nodes, "expiry"))).toEqual(["hk-2", "tokyo-2", "tokyo-1", "hk-1", "fresh"]);
  expect(names(sortCards(nodes, "cpu"))).toEqual(["tokyo-1", "hk-2", "hk-1", "tokyo-2", "fresh"]);
  expect(names(sortCards(nodes, "traffic"))).toEqual(["tokyo-1", "hk-1", "tokyo-2", "fresh", "hk-2"]);
});

it("着色依据：状态时不给档位；CPU / 内存按阈值；到期按剩余天数；无读数时不给档位", () => {
  expect(tileLevel(nodes[0], "status")).toBeUndefined();
  expect(tileLevel(nodes[0], "cpu")).toBe("critical");
  expect(tileLevel(nodes[0], "memory")).toBe("attention");
  expect(tileLevel(nodes[3], "cpu")).toBeUndefined();
  expect(tileLevel(nodes[1], "expiry")).toBe("attention");
  expect(tileLevel(nodes[4], "expiry")).toBe("critical");
  expect(tileLevel(nodes[0], "expiry")).toBe("neutral");
});

it("地区名来自 Intl.DisplayNames；空代码是「未知」；不认识的代码回退代码本身", () => {
  expect(regionName("JP")).toBe("日本");
  expect(regionName("")).toBe("未知");
  // ZZ 在 CLDR 里是「未知地区」，不是查不到；XX 才是真的查不到。
  expect(regionName("XX")).toBe("XX");
});

it("地区选项按代码排序、未知最后，带国旗与计数；分组按在线数降序，未知最后", () => {
  expect(regionOptions(nodes)).toEqual([
    { value: "HK", label: "🇭🇰 香港", count: 2 }, { value: "JP", label: "🇯🇵 日本", count: 2 }, { value: "", label: "未知", count: 1 },
  ]);
  const groups = groupByRegion(nodes);
  expect(groups.map((g) => [g.code, g.name, g.online, g.nodes.length])).toEqual([["JP", "日本", 1, 2], ["HK", "香港", 1, 2], ["", "未知", 0, 1]]);
});

it("同在线数按总数降序再按代码；只有未知时分组只有一组", () => {
  const tie = [node({ id: 1n, name: "a", country: "US", online: true, lastSeenAt: 1n }), node({ id: 2n, name: "b", country: "DE", online: true, lastSeenAt: 1n }), node({ id: 3n, name: "c", country: "DE", online: false, lastSeenAt: 1n })];
  expect(groupByRegion(tie).map((g) => g.code)).toEqual(["DE", "US"]);
  expect(groupByRegion([nodes[3]]).map((g) => [g.code, g.name])).toEqual([["", "未知"]]);
  expect(groupByRegion([])).toEqual([]);
});

it("汇总：四态计数之和等于总数，维护中的不算在线；速率只累计在线与维护中的读数；本周期累计全部", () => {
  const s = summarize(nodes);
  expect(s.total).toBe(5);
  expect(s.counts).toEqual({ online: 2, maintenance: 1, never: 1, offline: 1 });
  expect(s.rxBps).toBe(101n);
  expect(s.txBps).toBe(11n);
  expect(s.periodBytes).toBe(11n);
  expect(summarize([])).toEqual({ total: 0, counts: { online: 0, maintenance: 0, never: 0, offline: 0 }, rxBps: 0n, txBps: 0n, periodBytes: 0n });
});
```

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/public/filters.test.ts > /tmp/t11-red.log 2>&1; echo $?`
Expected: 1（模块不存在）。

- [ ] **Step 3: 实现**

`web/src/public/filters.ts`：

```ts
import { ratio } from "../components/Bar";
import type { PublicNode } from "../gen/heron/v1/public_pb";
import { sortByExpiry } from "../lib/billing";
import { flag } from "../lib/country";
import { literalPattern } from "../lib/fold";
import { expiryLevel, nodeStatus, STATUS_ORDER, usageLevel, type Level, type NodeStatus } from "../lib/status";
import { matchesTags } from "../lib/tags";

// 公开总览的筛选、排序、分组、汇总都是纯函数：组件只渲染结果。本文件只 import public_pb 与 lib，不经任何管理服务的生成代码。

export type PublicFilters = { search: string; regions: readonly string[]; tags: readonly string[]; onlineOnly: boolean };
export const NO_FILTERS: PublicFilters = { search: "", regions: [], tags: [], onlineOnly: false };

// 搜索范围是名称、标签、公开备注（设计 §3.1），与标签判重同一口径（lib/fold 的折叠字面匹配）。
export function matchesSearch(node: PublicNode, search: string): boolean {
  if (search === "") return true;
  const pattern = literalPattern(search, false);
  return pattern.test(node.name) || node.tags.some((tag) => pattern.test(tag)) || pattern.test(node.publicRemark);
}

// 地区之间取并集、标签取交集（lib/tags 的 matchesTags）、搜索与「只看在线」再取交集。regions 为空不过滤——
// 空条件匹配一切，这一分支显式写出。只看在线按四态判定：维护中与从未上报都不是在线。
export function filterPublicNodes(nodes: readonly PublicNode[], f: PublicFilters): PublicNode[] {
  return nodes.filter((n) =>
    matchesSearch(n, f.search)
    && (f.regions.length === 0 || f.regions.includes(n.country))
    && matchesTags(n.tags, f.tags)
    && (!f.onlineOnly || nodeStatus(n) === "online"));
}

export type CardSort = "default" | "expiry" | "cpu" | "traffic";
export const CARD_SORTS: readonly { value: CardSort; label: string }[] = [
  { value: "default", label: "默认" }, { value: "expiry", label: "到期" }, { value: "cpu", label: "CPU" }, { value: "traffic", label: "流量" },
];

// 降序，缺读数的排最后；相等保持传入顺序（sort 稳定）。
function byDesc(key: (n: PublicNode) => number | undefined) {
  return (a: PublicNode, b: PublicNode) => {
    const ka = key(a), kb = key(b);
    if (ka === kb) return 0;
    if (ka === undefined) return 1;
    if (kb === undefined) return -1;
    return kb - ka;
  };
}

export function sortCards(nodes: readonly PublicNode[], sort: CardSort): PublicNode[] {
  switch (sort) {
    case "expiry": return sortByExpiry(nodes);
    case "cpu": return [...nodes].sort(byDesc((n) => n.metrics?.cpuPct));
    case "traffic": return [...nodes].sort(byDesc((n) => (n.traffic ? Number(n.traffic.periodRx + n.traffic.periodTx) : undefined)));
    default: return [...nodes];
  }
}

export type ColorBy = "status" | "cpu" | "memory" | "expiry";
export const COLOR_BYS: readonly { value: ColorBy; label: string }[] = [
  { value: "status", label: "状态" }, { value: "cpu", label: "CPU" }, { value: "memory", label: "内存" }, { value: "expiry", label: "到期" },
];

// 状态墙方块的档位：按状态着色时不给档位（颜色来自四态）；其余按进度条阈值（设计 §9 第二条）。无读数不给档位。
export function tileLevel(node: PublicNode, by: ColorBy): Level | undefined {
  const m = node.metrics;
  switch (by) {
    case "cpu": return m?.cpuPct === undefined ? undefined : usageLevel(m.cpuPct);
    case "memory": return m?.memUsed === undefined || !m.memTotal ? undefined : usageLevel(ratio(m.memUsed, m.memTotal));
    case "expiry": return expiryLevel(node.billing?.daysLeft);
    default: return undefined;
  }
}

// 国家码到中文名：浏览器自带的 Intl.DisplayNames，不维护名表。style 取 short：长名会把 HK 写成「中国香港特别行政区」，
// 组头放不下。不支持或查不到时退回代码本身；空代码是「未知」（DisplayNames 对空串抛 RangeError，先拦）。
export function regionName(code: string): string {
  if (code === "") return "未知";
  try {
    return new Intl.DisplayNames(["zh-CN"], { type: "region", style: "short", fallback: "code" }).of(code) ?? code;
  } catch {
    return code;
  }
}

function regionLabel(code: string): string {
  return code === "" ? "未知" : `${flag(code)} ${regionName(code)}`;
}

const byCodeUnknownLast = (a: string, b: string) => (a === "" ? 1 : b === "" ? -1 : a.localeCompare(b));

export type RegionOption = { value: string; label: string; count: number };

export function regionOptions(nodes: readonly PublicNode[]): RegionOption[] {
  const counts = new Map<string, number>();
  for (const n of nodes) counts.set(n.country, (counts.get(n.country) ?? 0) + 1);
  return [...counts.keys()].sort(byCodeUnknownLast).map((code) => ({ value: code, label: regionLabel(code), count: counts.get(code)! }));
}

export type RegionGroup = { code: string; name: string; nodes: PublicNode[]; online: number };

// 状态墙分组（设计 §3.1）：组按在线数降序，同在线数按总数降序，再按代码；没有国家的归「未知」固定排最后。
export function groupByRegion(nodes: readonly PublicNode[]): RegionGroup[] {
  const groups = new Map<string, RegionGroup>();
  for (const n of nodes) {
    const g = groups.get(n.country) ?? { code: n.country, name: regionName(n.country), nodes: [], online: 0 };
    g.nodes.push(n);
    if (nodeStatus(n) === "online") g.online++;
    groups.set(n.country, g);
  }
  return [...groups.values()].sort((a, b) => {
    if ((a.code === "") !== (b.code === "")) return a.code === "" ? 1 : -1;
    return b.online - a.online || b.nodes.length - a.nodes.length || a.code.localeCompare(b.code);
  });
}

export type Summary = { total: number; counts: Record<NodeStatus, number>; rxBps: bigint; txBps: bigint; periodBytes: bigint };

// 汇总条（设计 §3.1）：四态计数之和等于总数。实时速率只累计在线与维护中的节点——离线节点的最后读数是旧值，
// 加进去会把早已不存在的流量算进「实时合计」。本周期流量是 hub 累计的，离线节点的也真实，全部累计。
export function summarize(nodes: readonly PublicNode[]): Summary {
  const counts = Object.fromEntries(STATUS_ORDER.map((s) => [s, 0])) as Record<NodeStatus, number>;
  let rxBps = 0n, txBps = 0n, periodBytes = 0n;
  for (const n of nodes) {
    const status = nodeStatus(n);
    counts[status]++;
    if (status === "online" || status === "maintenance") {
      rxBps += n.metrics?.netRxBps ?? 0n;
      txBps += n.metrics?.netTxBps ?? 0n;
    }
    if (n.traffic) periodBytes += n.traffic.periodRx + n.traffic.periodTx;
  }
  return { total: nodes.length, counts, rxBps, txBps, periodBytes };
}
```

- [ ] **Step 4: 跑测试确认绿**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/public/filters.test.ts > /tmp/t11.log 2>&1; echo $?`
Expected: 0（jsdom 下的 Node 自带完整 ICU，`Intl.DisplayNames` 给出「日本」「香港」；若 Node 是 small-icu 构建，`regionName` 用例会红在得到 `JP`——那是环境事实，换 full-icu 的 Node 重跑，不改断言）。

- [ ] **Step 5: 缺陷注入**

把 `summarize` 里的 `status === "maintenance"` 去掉，汇总用例应红在 `rxBps`；把 `groupByRegion` 的未知判断去掉，分组用例应红在顺序。改回后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/public/filters.ts web/src/public/filters.test.ts && git commit -m "feat(web): 公开总览的筛选、排序、地区分组与汇总"
```

---

### Task 12: 公开页顶栏

**Files:**
- Modify: `web/src/public/Layout.tsx`、`web/src/public/Layout.test.tsx`
- Modify: `web/src/public/public.css`（整体重写开头的顶栏与布局部分；卡片部分在 Task 16 重写）
- Modify: `web/src/public/index.html:7`（`<title>Heron</title>` → `<title>服务器状态</title>`，与 `DEFAULT_TITLE` 一致，避免首屏闪一下 Heron）

**Interfaces:**
- Consumes: `ThemeToggle`、`readSchemeChoice`、`writeSchemeChoice`、`PUBLIC_SCHEME_KEY`、`applySite(site, choice)`（Task 4）、`POLL_MS`（`lib/poll.ts`）、`HeronMark`。
- Produces: 顶栏 DOM `header.public-header`：`a.brand`（logo + 标题）、`span.live`（「实时 · 每 2 秒」）、`ThemeToggle`、`a.admin`（登录，仅 adminPath 非空）。没有 `nav`、没有页脚。

- [ ] **Step 1: 改测试（红）**

在 `web/src/public/Layout.test.tsx` 追加（并在 `afterEach` 里加 `localStorage.clear(); delete document.documentElement.dataset.theme;`）：

```tsx
// 顶栏只有：logo、站点标题、「实时 · 每 N 秒」、明暗切换、「登录」（设计 §3.1）；没有导航、铃铛、刷新、页脚。
it("顶栏只有标题链接、实时说明、明暗切换与登录；没有导航与页脚", async () => {
  renderWithService(PublicService, { getSite: async () => ({ title: "机房", adminPath: "/admin/" }) }, [{ path: "/", Component: PublicLayout }], "/");
  const header = (await screen.findByRole("link", { name: "机房" })).closest("header")!;
  expect(within(header).getAllByRole("link").map((a) => a.textContent)).toEqual(["机房", "登录"]);
  expect(within(header).getByText(`实时 · 每 ${POLL_MS / 1000} 秒`)).toBeInTheDocument();
  expect(within(header).getByRole("button", { name: "明暗切换" })).toBeInTheDocument();
  expect(screen.queryByRole("navigation")).toBeNull();
  expect(screen.queryByRole("contentinfo")).toBeNull();
});

it("访客点明暗切换：立即写 data-theme 并记到 localStorage，压过站点设置", async () => {
  renderWithService(PublicService, { getSite: async () => ({ theme: "dark" }) }, [{ path: "/", Component: PublicLayout }], "/");
  await waitFor(() => expect(document.documentElement.dataset.theme).toBe("dark"));
  fireEvent.click(screen.getByRole("button", { name: "明暗切换" }));   // auto → light
  await waitFor(() => expect(document.documentElement.dataset.theme).toBe("light"));
  expect(localStorage.getItem(PUBLIC_SCHEME_KEY)).toBe("light");
});
```

import 补 `fireEvent, within`、`POLL_MS`（`../lib/poll`）、`PUBLIC_SCHEME_KEY`（`../lib/scheme`）。

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/public/Layout.test.tsx > /tmp/t12-red.log 2>&1; echo $?`
Expected: 1；红在找不到「实时 · 每 2 秒」与「明暗切换」按钮。

- [ ] **Step 3: 实现 Layout**

`web/src/public/Layout.tsx` 的 `PublicLayout` 改为：

```tsx
import { useEffect, useState } from "react";
import { PUBLIC_SCHEME_KEY, readSchemeChoice, writeSchemeChoice } from "../lib/scheme";
import { POLL_MS } from "../lib/poll";
import { ThemeToggle } from "../components/ThemeToggle";
// （其余 import 不变）

export function PublicLayout() {
  const site = useQuery(PublicService.method.getSite, {}, { staleTime: Infinity });
  // 访客的明暗选择只存浏览器，压过站点设置（lib/scheme 的 forcedTheme）；存储不可用时当前页面仍按选择显示。
  const [choice, setChoice] = useState(() => readSchemeChoice(PUBLIC_SCHEME_KEY));
  useEffect(() => applySite(site.data ?? create(PublicSiteSchema), choice), [site.data, choice]);
  return (
    <div className="layout public-layout">
      <header className="public-header">
        <Link to="/" className="brand">
          {site.data?.logo ? <img src={site.data.logo} alt="" className="logo" /> : <HeronMark />}
          {site.data?.title || DEFAULT_TITLE}
        </Link>
        {/* 数据每 POLL_MS 自动刷新（Overview 的 refetchInterval），所以顶栏不放手动刷新。 */}
        <span className="live muted">实时 · 每 {POLL_MS / 1000} 秒</span>
        <ThemeToggle choice={choice} onChange={(next) => { writeSchemeChoice(PUBLIC_SCHEME_KEY, next); setChoice(next); }} />
        {/* 面板是另一个前端入口，用普通链接整页跳转；只接受 hub 给出的面板路径。 */}
        {site.data?.adminPath && <a href={site.data.adminPath} className="admin">登录</a>}
      </header>
      <main className="main">
        {errorBanner(site.error)}
        <Outlet />
      </main>
    </div>
  );
}
```

`web/src/public/public.css` 第 1–4 行与 `.public-layout`、`.public-layout .main:has(…)`、`.public-overview > .row`、`.public-overview h1` 这几条替换为：

```css
/* 公开页在共用 token（../styles.css）之上的布局。站点设置的自定义 CSS 排在这之后。 */
.public-layout { min-height: 100dvh; }
.public-header { position: sticky; top: 0; z-index: 20; height: 48px; display: flex; align-items: center; gap: 16px; padding: 0 clamp(12px, 2vw, 24px); background: var(--card); border-bottom: 1px solid var(--line); }
.public-header .brand { color: var(--fg); font-weight: 600; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.public-header .brand .logo, .public-header .heron-mark { height: 24px; width: auto; }
.public-header .live { font-size: 12px; white-space: nowrap; }
.public-header .theme-toggle { margin-left: auto; }
.public-header .admin { color: var(--muted); font-size: 13px; }
.public-layout .main { max-width: 1680px; padding: 16px clamp(12px, 2vw, 24px); }
```

- [ ] **Step 4: 跑测试确认绿，再跑公开入口测试**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/public/Layout.test.tsx src/public/main.test.tsx src/public/importScan.test.ts > /tmp/t12.log 2>&1; echo $?`
Expected: 0（`main.test.tsx` 断言 `data-theme` 为站点的 dark：localStorage 为空时访客选择是 auto）。

- [ ] **Step 5: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/public/Layout.tsx web/src/public/Layout.test.tsx web/src/public/public.css web/src/public/index.html && git commit -m "feat(web): 公开页 48px 顶栏，带实时说明与访客明暗切换"
```

---

### Task 13: 汇总条 StatusSummary

**Files:**
- Create: `web/src/public/StatusSummary.tsx`、`web/src/public/StatusSummary.test.tsx`
- Modify: `web/src/public/public.css`（追加 `.summary*`）

**Interfaces:**
- Consumes: `summarize`（Task 11）、`STATUS_ORDER`、`STATUS_LABEL`（Task 2）、`bytes`（`lib/format.ts`）。
- Produces: `StatusSummary({ nodes }: { nodes: readonly PublicNode[] })` → `<section class="summary" aria-label="汇总">`：`strong.summary-count` 文本恰为 `${online} / ${total} 在线`；`div.segments[role=img]`，`aria-label` 为「在线 N、维护中 N、从未上报 N、离线 N」；`span[role=group][aria-label=实时合计]` 含「↓ x/s ↑ y/s」；`span.summary-period` 含「本周期 z」。

- [ ] **Step 1: 写测试（红）**

`web/src/public/StatusSummary.test.tsx`：

```tsx
import { create } from "@bufbuild/protobuf";
import { render, screen, within } from "@testing-library/react";
import { expect, it } from "vitest";
import { PublicNodeSchema } from "../gen/heron/v1/public_pb";
import { StatusSummary } from "./StatusSummary";

const n = (init: Parameters<typeof create<typeof PublicNodeSchema>>[1]) => create(PublicNodeSchema, init);

it("在线 / 总数按四态算（维护中不算在线），分段条按比例、零段不画，速率与本周期合计", () => {
  render(<StatusSummary nodes={[
    n({ id: 1n, name: "a", online: true, lastSeenAt: 1n, metrics: { netRxBps: 2048n, netTxBps: 1024n }, traffic: { periodRx: 1024n ** 3n, periodTx: 0n } }),
    n({ id: 2n, name: "b", online: true, lastSeenAt: 1n, maintenance: true }),
    n({ id: 3n, name: "c", online: false, lastSeenAt: 1n }),
    n({ id: 4n, name: "d", online: false }),
  ]} />);
  const summary = within(screen.getByRole("region", { name: "汇总" }));
  expect(summary.getByText("1 / 4 在线")).toBeInTheDocument();
  const bar = summary.getByRole("img", { name: "在线 1、维护中 1、从未上报 1、离线 1" });
  const widths = [...bar.querySelectorAll("[data-status]")].map((el) => [el.getAttribute("data-status"), (el as HTMLElement).style.width]);
  expect(widths).toEqual([["online", "25%"], ["maintenance", "25%"], ["never", "25%"], ["offline", "25%"]]);
  expect(summary.getByRole("group", { name: "实时合计" })).toHaveTextContent("↓ 2.0 KiB/s ↑ 1.0 KiB/s");
  expect(summary.getByText(/本周期/)).toHaveTextContent("本周期 1.0 GiB");
});

it("总数为 0 时不出现 NaN 宽度，分段条为空", () => {
  render(<StatusSummary nodes={[]} />);
  expect(screen.getByText("0 / 0 在线")).toBeInTheDocument();
  expect(screen.getByRole("img", { name: "在线 0、维护中 0、从未上报 0、离线 0" }).querySelectorAll("[data-status]")).toHaveLength(0);
});
```

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/public/StatusSummary.test.tsx > /tmp/t13-red.log 2>&1; echo $?`
Expected: 1（模块不存在）。

- [ ] **Step 3: 实现**

`web/src/public/StatusSummary.tsx`：

```tsx
import type { PublicNode } from "../gen/heron/v1/public_pb";
import { bytes } from "../lib/format";
import { STATUS_LABEL, STATUS_ORDER } from "../lib/status";
import { summarize } from "./filters";

// 汇总条（设计 §3.1）：在线 / 总数 + 四态比例分段条 + 实时合计 ↓↑ + 本周期总流量。
// 计数按传入的（已筛选的）节点算，与下方墙 / 卡片看到的是同一批节点。
export function StatusSummary({ nodes }: { nodes: readonly PublicNode[] }) {
  const s = summarize(nodes);
  const segments = STATUS_ORDER.map((status) => ({ status, count: s.counts[status] }));
  return (
    <section className="summary" aria-label="汇总">
      <strong className="summary-count num">{s.counts.online} / {s.total} 在线</strong>
      <div className="segments" role="img" aria-label={segments.map(({ status, count }) => `${STATUS_LABEL[status]} ${count}`).join("、")}>
        {/* 总数为 0 时没有段：0/0 是 NaN，不能写成宽度。 */}
        {s.total > 0 && segments.filter((seg) => seg.count > 0).map(({ status, count }) => (
          <span key={status} data-status={status} style={{ width: `${(count / s.total) * 100}%` }} />
        ))}
      </div>
      <span className="summary-rates num" role="group" aria-label="实时合计">↓ {bytes(s.rxBps)}/s ↑ {bytes(s.txBps)}/s</span>
      <span className="summary-period num">本周期 {bytes(s.periodBytes)}</span>
    </section>
  );
}
```

`web/src/public/public.css` 追加：

```css
/* 汇总条（StatusSummary.tsx）。 */
.summary { display: flex; align-items: center; flex-wrap: wrap; gap: 8px 20px; margin: 4px 0 12px; font-size: 13px; }
.summary-count { font-size: 16px; }
.segments { display: flex; height: 6px; width: min(320px, 100%); border-radius: 999px; overflow: hidden; background: var(--line); }
.segments > span { height: 100%; }
.segments > [data-status="online"] { background: var(--status-online); }
.segments > [data-status="maintenance"] { background: var(--status-maintenance); }
.segments > [data-status="never"] { background: var(--status-never); }
.segments > [data-status="offline"] { background: var(--status-offline); }
.summary-rates, .summary-period { color: var(--muted); white-space: nowrap; }
```

- [ ] **Step 4: 跑测试确认绿**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/public/StatusSummary.test.tsx > /tmp/t13.log 2>&1; echo $?`
Expected: 0。

- [ ] **Step 5: 缺陷注入**

把 `s.total > 0 &&` 删掉，「总数为 0」用例应红在段数（出现 `NaN%` 的段不被过滤——count 为 0 的段已被 filter 掉，所以改成同时把 `.filter(...)` 也删掉再看：应红在段数 4 且宽度 `NaN%`）；改回后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/public/StatusSummary.tsx web/src/public/StatusSummary.test.tsx web/src/public/public.css && git commit -m "feat(web): 公开总览汇总条"
```

---

### Task 14: 筛选行与总览的状态骨架

**Files:**
- Create: `web/src/public/FilterRow.tsx`
- Modify: `web/src/public/Overview.tsx`（整体重写）
- Modify: `web/src/public/Overview.test.tsx`、`web/src/public/RegionFilter.test.tsx`（重写）
- Delete: `web/src/public/FilterBar.tsx`、`web/src/public/FilterBar.test.tsx`、`web/src/public/ViewOptions.tsx`
- Modify: `web/src/public/public.css`（追加 `.filter-row*`；删除旧的 `.view-bar`、`.filter-bar`、`.view-options`、`.chip[aria-pressed]`、`@media (pointer: coarse)` 四条）

**Interfaces:**
- Consumes: `MultiSelect`（Task 5）、`filters.ts`（Task 11）、`StatusSummary`（Task 13）、`sameTag`（`lib/tags.ts`）。
- Produces:
  ```ts
  export type View = "wall" | "cards";
  export function FilterRow(props: {
    filters: PublicFilters; onFilters: (next: PublicFilters) => void;
    regions: RegionOption[]; tags: readonly { value: string; label: string; count: number }[];
    view: View; onView: (v: View) => void;
    colorBy: ColorBy; onColorBy: (c: ColorBy) => void;
    sort: CardSort; onSort: (s: CardSort) => void;
  }): JSX.Element;
  ```
  DOM：`div.filter-row[role=group][aria-label=筛选]`：`input[type=search][aria-label=搜索节点]`、`MultiSelect 地区`、`MultiSelect 标签`（仅 tags 非空；searchable）、`button[aria-pressed] 只看在线`、`div[role=group][aria-label=视图]` 里两个 `button[aria-pressed]`「状态墙」「卡片」、墙视图下 `select[aria-label=着色依据]`、卡片视图下 `select[aria-label=排序]`。
  Overview 本任务先用占位渲染（`<ul>` 列出节点名的 `article`）让测试跑通，Task 15 / 16 换成真实的墙与卡片。

- [ ] **Step 1: 重写测试（红）**

`web/src/public/Overview.test.tsx` 整体替换为：

```tsx
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { PublicService } from "../gen/heron/v1/public_pb";
import { POLL_MS } from "../lib/poll";
import { renderWithService } from "../test/harness";
import { PublicOverview } from "./Overview";

const snapshot = {
  now: 1_000n,
  reportIntervalMs: 4000,
  tags: ["db", "prod", "web"],
  nodes: [
    { id: 1n, name: "web-1", online: true, lastSeenAt: 998n, sortOrder: 0, country: "JP", tags: ["web", "prod"], metrics: { cpuPct: 42 } },
    { id: 2n, name: "db-1", online: false, lastSeenAt: 900n, sortOrder: 1, country: "JP", tags: ["db", "prod"], publicRemark: "联通 4837" },
    { id: 3n, name: "lab-1", online: true, lastSeenAt: 998n, sortOrder: 2, country: "HK", tags: ["DB"] },
    { id: 4n, name: "bare-1", online: false, sortOrder: 3, country: "" },
  ],
};
afterEach(() => vi.useRealTimers());

const shown = () => screen.queryAllByRole("article").map((a) => a.getAttribute("aria-label"));
const open = (label: string) => fireEvent.click(within(screen.getByRole("group", { name: label })).getByRole("button", { name: new RegExp(`^${label}`) }));
const check = (label: string, name: string) => fireEvent.click(within(screen.getByRole("group", { name: label })).getByRole("checkbox", { name }));
function render(getSnapshot: () => Promise<typeof snapshot> = async () => snapshot) {
  renderWithService(PublicService, { getSnapshot }, [{ path: "/", Component: PublicOverview }], "/");
}

it("汇总、筛选行与节点一起出现；地区与标签是带计数的多选下拉", async () => {
  render();
  expect(await screen.findByText("2 / 4 在线")).toBeInTheDocument();
  expect(shown()).toEqual(["web-1", "db-1", "lab-1", "bare-1"]);
  open("地区");
  expect(within(screen.getByRole("group", { name: "地区" })).getAllByRole("checkbox").map((c) => c.getAttribute("aria-label"))).toEqual(["🇭🇰 香港", "🇯🇵 日本", "未知"]);
  open("标签");
  // 标签的集合与顺序取 hub 下发的并集（按折叠键排序），页面不自己汇总、排序。
  expect(within(screen.getByRole("group", { name: "标签" })).getAllByRole("checkbox").map((c) => c.getAttribute("aria-label"))).toEqual(["db", "prod", "web"]);
});

it("地区并集、标签交集、搜索与只看在线叠加；计数按筛选后的节点算；滤空时说明", async () => {
  render();
  await screen.findByText("2 / 4 在线");
  open("地区");
  check("地区", "🇯🇵 日本");
  check("地区", "未知");
  expect(shown()).toEqual(["web-1", "db-1", "bare-1"]);
  expect(screen.getByText("1 / 3 在线")).toBeInTheDocument();
  open("标签");
  check("标签", "prod");
  expect(shown()).toEqual(["web-1", "db-1"]);
  fireEvent.click(screen.getByRole("button", { name: "只看在线" }));
  expect(shown()).toEqual(["web-1"]);
  fireEvent.change(screen.getByRole("searchbox", { name: "搜索节点" }), { target: { value: "4837" } });
  expect(shown()).toEqual([]);
  expect(screen.getByText("没有符合筛选条件的节点。")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "只看在线" }));
  expect(shown()).toEqual(["db-1"]);
});

it("标签折叠比较：选 db 时 DB 的节点也命中", async () => {
  render();
  await screen.findByText("2 / 4 在线");
  open("标签");
  check("标签", "db");
  expect(shown()).toEqual(["db-1", "lab-1"]);
});

it("被选中的标签或地区从快照消失后从选择集里移除，不留下看不见的过滤", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  let current = snapshot;
  render(async () => current);
  await screen.findByText("2 / 4 在线");
  open("标签");
  check("标签", "web");
  open("地区");
  check("地区", "未知");
  expect(shown()).toEqual([]);
  current = { ...snapshot, tags: ["db", "prod"], nodes: snapshot.nodes.map((n) => ({ ...n, tags: n.tags?.filter((t) => t !== "web"), country: n.country || "CA" })) };
  await act(async () => vi.advanceTimersByTimeAsync(POLL_MS + 100));
  await waitFor(() => expect(shown()).toEqual(["web-1", "db-1", "lab-1", "bare-1"]));
  expect(within(screen.getByRole("group", { name: "标签" })).queryByRole("button", { name: /^移除/ })).toBeNull();
  expect(within(screen.getByRole("group", { name: "地区" })).queryByRole("button", { name: /^移除/ })).toBeNull();
});

it("没有任何节点带标签时不画标签下拉；没有公开节点时只说明", async () => {
  render(async () => ({ ...snapshot, tags: [], nodes: snapshot.nodes.map((n) => ({ ...n, tags: [] })) }));
  await screen.findByText("2 / 4 在线");
  expect(screen.queryByRole("group", { name: "标签" })).toBeNull();
});

it("没有公开节点时说明，不画汇总与筛选行", async () => {
  render(async () => ({ now: 1n, nodes: [], tags: [] }));
  expect(await screen.findByText("没有公开的节点。")).toBeInTheDocument();
  expect(screen.queryByRole("group", { name: "筛选" })).toBeNull();
});

it("视图切换：默认状态墙带着色依据；卡片视图带排序", async () => {
  render();
  await screen.findByText("2 / 4 在线");
  const views = within(screen.getByRole("group", { name: "视图" }));
  expect(views.getByRole("button", { name: "状态墙" })).toHaveAttribute("aria-pressed", "true");
  expect(screen.getByRole("combobox", { name: "着色依据" })).toHaveValue("status");
  expect(screen.queryByRole("combobox", { name: "排序" })).toBeNull();
  fireEvent.click(views.getByRole("button", { name: "卡片" }));
  expect(views.getByRole("button", { name: "卡片" })).toHaveAttribute("aria-pressed", "true");
  expect(screen.getByRole("combobox", { name: "排序" })).toHaveValue("default");
  expect(screen.queryByRole("combobox", { name: "着色依据" })).toBeNull();
});

it("按 POLL_MS 轮询快照", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  const getSnapshot = vi.fn(async () => snapshot);
  render(getSnapshot);
  await screen.findByText("2 / 4 在线");
  await act(async () => vi.advanceTimersByTimeAsync(POLL_MS + 100));
  await waitFor(() => expect(getSnapshot.mock.calls.length).toBeGreaterThanOrEqual(2));
});
```

`web/src/public/RegionFilter.test.tsx` 删除（地区筛选已由上面的用例与 Task 11 覆盖）。`FilterBar.test.tsx` 删除。

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/public/Overview.test.tsx > /tmp/t14-red.log 2>&1; echo $?`
Expected: 1；红在找不到「地区」group、「搜索节点」等。

- [ ] **Step 3: 实现 FilterRow**

`web/src/public/FilterRow.tsx`：

```tsx
import { MultiSelect } from "../components/MultiSelect";
import { CARD_SORTS, COLOR_BYS, type CardSort, type ColorBy, type PublicFilters, type RegionOption } from "./filters";

export type View = "wall" | "cards";

// 筛选行（设计 §3.1）：搜索、地区多选、标签多选（可搜索）、只看在线、视图切换；状态墙多一个着色依据，卡片多一个排序。
// 地区与标签是动态集合（设计 §2），选项由调用方从当前快照算出并带计数。
export function FilterRow({ filters, onFilters, regions, tags, view, onView, colorBy, onColorBy, sort, onSort }: {
  filters: PublicFilters; onFilters: (next: PublicFilters) => void;
  regions: RegionOption[]; tags: readonly { value: string; label: string; count: number }[];
  view: View; onView: (view: View) => void;
  colorBy: ColorBy; onColorBy: (by: ColorBy) => void;
  sort: CardSort; onSort: (sort: CardSort) => void;
}) {
  return (
    <div className="filter-row" role="group" aria-label="筛选">
      <input type="search" aria-label="搜索节点" placeholder="搜索名称、标签、备注" value={filters.search} onChange={(event) => onFilters({ ...filters, search: event.target.value })} />
      <MultiSelect label="地区" options={regions} selected={filters.regions} onChange={(next) => onFilters({ ...filters, regions: next })} />
      {tags.length > 0 && <MultiSelect label="标签" options={tags} selected={filters.tags} onChange={(next) => onFilters({ ...filters, tags: next })} searchable />}
      <button type="button" className="toggle" aria-pressed={filters.onlineOnly} onClick={() => onFilters({ ...filters, onlineOnly: !filters.onlineOnly })}>只看在线</button>
      <div className="view-switch" role="group" aria-label="视图">
        <button type="button" aria-pressed={view === "wall"} onClick={() => onView("wall")}>状态墙</button>
        <button type="button" aria-pressed={view === "cards"} onClick={() => onView("cards")}>卡片</button>
      </div>
      {view === "wall" && (
        <label className="inline">着色依据
          <select aria-label="着色依据" value={colorBy} onChange={(event) => onColorBy(event.target.value as ColorBy)}>
            {COLOR_BYS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
          </select>
        </label>
      )}
      {view === "cards" && (
        <label className="inline">排序
          <select aria-label="排序" value={sort} onChange={(event) => onSort(event.target.value as CardSort)}>
            {CARD_SORTS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
          </select>
        </label>
      )}
    </div>
  );
}
```

- [ ] **Step 4: 重写 Overview（本任务用占位列表）**

`web/src/public/Overview.tsx`：

```tsx
import { useQuery } from "@connectrpc/connect-query";
import { useState } from "react";
import { errorBanner, queryGate } from "../api/queryGate";
import { PublicService } from "../gen/heron/v1/public_pb";
import { POLL_MS } from "../lib/poll";
import { sameTag } from "../lib/tags";
import { FilterRow, type View } from "./FilterRow";
import { filterPublicNodes, NO_FILTERS, regionOptions, sortCards, type CardSort, type ColorBy, type PublicFilters } from "./filters";
import { StatusSummary } from "./StatusSummary";

export function PublicOverview() {
  const snap = useQuery(PublicService.method.getSnapshot, {}, { refetchInterval: POLL_MS });
  const [filters, setFilters] = useState<PublicFilters>(NO_FILTERS);
  const [view, setView] = useState<View>("wall");
  const [colorBy, setColorBy] = useState<ColorBy>("status");
  const [sort, setSort] = useState<CardSort>("default");
  const gate = queryGate(snap);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const now = Number(gate.data.now);
  const all = gate.data.nodes;
  const regions = regionOptions(all);
  // 标签的集合与顺序取 hub 下发的并集（按折叠键排序，与面板同序），页面不自己汇总、排序：折叠规则只在 hub 一处。
  const tagCounts = new Map(gate.data.tags.map((tag) => [tag, all.filter((n) => n.tags.some((t) => sameTag(t, tag))).length]));
  const tags = gate.data.tags.map((tag) => ({ value: tag, label: tag, count: tagCounts.get(tag) ?? 0 }));
  // 生效的选择集只取当前快照里仍存在的地区与标签（标签按折叠比较，换成标签栏上的写法）：被选的项在轮询后消失时
  // 从选择集里移除，而不是留下一个看不见的过滤条件。
  const liveRegions = filters.regions.filter((code) => regions.some((r) => r.value === code));
  const liveTags = gate.data.tags.filter((tag) => filters.tags.some((s) => sameTag(s, tag)));
  if (liveRegions.length !== filters.regions.length || liveTags.length !== filters.tags.length) setFilters({ ...filters, regions: liveRegions, tags: liveTags });
  const effective: PublicFilters = { ...filters, regions: liveRegions, tags: liveTags };
  const nodes = filterPublicNodes(all, effective);
  return (
    <section className="public-overview">
      {gate.banner}
      {all.length === 0 && <p className="muted">没有公开的节点。</p>}
      {all.length > 0 && (
        <>
          <StatusSummary nodes={nodes} />
          <FilterRow filters={effective} onFilters={setFilters} regions={regions} tags={tags} view={view} onView={setView} colorBy={colorBy} onColorBy={setColorBy} sort={sort} onSort={setSort} />
          {nodes.length === 0 && <p className="muted">没有符合筛选条件的节点。</p>}
          {nodes.length > 0 && (
            <ul>
              {(view === "cards" ? sortCards(nodes, sort) : nodes).map((n) => <li key={String(n.id)}><article aria-label={n.name}>{n.name}</article></li>)}
            </ul>
          )}
        </>
      )}
    </section>
  );
}
```

（`now`、`colorBy` 在本任务暂未使用；为通过 `noUnusedLocals`，本任务先不声明 `now`，Task 15 再加。）

`web/src/public/public.css` 删除 `.view-bar`、`.filter-bar, .view-options`、`.filter-bar { flex-basis… }`、`@media (pointer: coarse) { .filter-bar .chip … }`、`.chip { user-select… }`、`.chip[aria-pressed="true"]` 六条，追加：

```css
/* 筛选行（FilterRow.tsx）。 */
.filter-row { display: flex; align-items: center; flex-wrap: wrap; gap: 8px 12px; margin: 0 0 16px; }
.filter-row input[type="search"] { width: min(100%, 260px); }
.filter-row .toggle[aria-pressed="true"], .view-switch button[aria-pressed="true"] { border-color: var(--accent); color: var(--accent); background: color-mix(in srgb, var(--accent) 10%, transparent); }
.view-switch { display: inline-flex; }
.view-switch button:first-child { border-radius: var(--radius-control) 0 0 var(--radius-control); }
.view-switch button:last-child { border-radius: 0 var(--radius-control) var(--radius-control) 0; margin-left: -1px; }
.filter-row label.inline { gap: 6px; font-size: 13px; color: var(--muted); }
```

- [ ] **Step 5: 跑测试确认绿，再跑全量与类型检查**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/public/Overview.test.tsx > /tmp/t14.log 2>&1; echo $?`
Expected: 0。

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm typecheck > /tmp/t14-tsc.log 2>&1; echo $?; pnpm vitest run > /tmp/t14-all.log 2>&1; echo $?`
Expected: 两个 0（`NodeCard.test.tsx` 此时会红——它断言旧卡片；本任务把它一并删除，Task 16 重写）。删除 `web/src/public/NodeCard.test.tsx` 后重跑应为 0。

- [ ] **Step 6: 缺陷注入**

把 `liveTags` 的规范化去掉（直接用 `filters.tags`），「被选中的标签或地区从快照消失后…」应红在 `shown()` 仍为空；改回后绿。

- [ ] **Step 7: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add -A web/src/public && git commit -m "feat(web): 公开总览改为搜索、地区与标签多选、只看在线与视图切换"
```

---

### Task 15: 状态墙、详情面板与手机版

**Files:**
- Create: `web/src/lib/useMediaQuery.ts`
- Create: `web/src/public/StatusWall.tsx`、`web/src/public/DetailPanel.tsx`、`web/src/public/StatusWall.test.tsx`
- Modify: `web/src/public/Overview.tsx`（墙视图接入、选中状态）、`web/src/test/setup.ts`（`matchMedia` 替身）
- Modify: `web/src/public/public.css`（追加 `.wall*`、`.tile*`、`.detail-panel*`、手机媒体查询）

**Interfaces:**
- Consumes: `groupByRegion`、`tileLevel`、`regionName`（Task 11）、`StatusBadge`、`Bar thin`（Task 3）、`Sparkline`（Task 6）、`nodeStatus`、`STATUS_LABEL`（Task 2）、`toAligned`（`lib/series.ts`）、`ago/bytes/duration/percent`（`lib/format.ts`）、`priceText/expiryText`（`lib/billing.ts`）。
- Produces:
  ```ts
  export function useMediaQuery(query: string): boolean;             // useSyncExternalStore 包 matchMedia
  export const WIDE_QUERY = "(min-width: 901px)";
  export function StatusWall(props: { nodes: readonly PublicNode[]; now: number; colorBy: ColorBy; selectedId: bigint | null; onSelect: (id: bigint) => void }): JSX.Element;
  export function DetailPanel({ node, now }: { node: PublicNode; now: number }): JSX.Element;
  ```
  DOM：`.wall-layout > (div.wall + aside.detail-panel[aria-label=节点详情])`；每组 `details.wall-group[open] > summary`（文本「🇯🇵 日本 · 1 / 2 在线」）+ `ul.tiles > li.tile[data-status][data-level][aria-current]` 内一个 `a`（名为节点名，href `/nodes/:id`）。宽屏点 tile 不导航、只选中；窄屏（`useMediaQuery(WIDE_QUERY)` 为 false）照常导航、不渲染详情面板。详情面板为选中节点查 `QueryMetrics` 最近 1 小时（`maxPoints: 60`，`staleTime: 60_000`），迷你线取 `rx_bytes`/`tx_bytes` 的 `sum-rate`；底部链接「查看完整历史 →」到 `/nodes/:id`。

- [ ] **Step 1: 写测试（红）**

`web/src/test/setup.ts` 末尾追加（jsdom 没有 matchMedia；默认按宽屏、浅色，具体用例再 stub）：

```ts
if (typeof window.matchMedia !== "function") {
  window.matchMedia = (query: string) => ({ matches: query === "(min-width: 901px)", media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false }) as MediaQueryList;
}
```

`web/src/public/StatusWall.test.tsx`：

```tsx
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { PublicService } from "../gen/heron/v1/public_pb";
import { POLL_MS } from "../lib/poll";
import { renderWithService } from "../test/harness";
import { PublicOverview } from "./Overview";

const snapshot = {
  now: 1_000n, tags: ["prod"],
  nodes: [
    { id: 1n, name: "tokyo-1", online: true, lastSeenAt: 998n, sortOrder: 0, country: "JP", tags: ["prod"], publicRemark: "联通 4837",
      facts: { os: "Debian 13", arch: "amd64", virtualization: "kvm", cpuModel: "EPYC", cpuCores: 2 },
      metrics: { cpuPct: 95, memUsed: 8n * 1024n ** 3n, memTotal: 10n * 1024n ** 3n, swapUsed: 0n, swapTotal: 1024n ** 3n, diskUsed: 1024n ** 3n, diskTotal: 10n * 1024n ** 3n, load1: 0.5, load5: 0.4, load15: 0.3, netRxBps: 2048n, netTxBps: 1024n, diskReadBps: 10n, diskWriteBps: 20n, tcpConns: 12, udpConns: 3, procs: 99, uptimeS: 90_000n },
      traffic: { periodRx: 1024n ** 3n, periodTx: 0n }, billing: { price: "12.50", currency: "USD", billingCycle: 1, expiresOn: "2030-01-01", daysLeft: 12 } },
    { id: 2n, name: "tokyo-2", online: false, lastSeenAt: 1n, sortOrder: 1, country: "JP", metrics: { cpuPct: 10 } },
    { id: 3n, name: "hk-1", online: true, lastSeenAt: 998n, sortOrder: 2, country: "HK", maintenance: true },
    { id: 4n, name: "fresh", online: false, sortOrder: 3, country: "" },
  ],
};
const queryMetrics = vi.fn(async () => ({ level: "1m", stepS: 60, ts: [900n, 960n], series: [
  { name: "rx_bytes", unit: "bytes", samples: [{ n: 1, sum: 6000 }, { n: 1, sum: 0 }] },
  { name: "tx_bytes", unit: "bytes", samples: [{ n: 1, sum: 600 }, { n: 1, sum: 0 }] },
] }));
afterEach(() => { vi.useRealTimers(); queryMetrics.mockClear(); });
function render(getSnapshot: () => Promise<typeof snapshot> = async () => snapshot) {
  return renderWithService(PublicService, { getSnapshot, queryMetrics }, [{ path: "/", Component: PublicOverview }, { path: "/nodes/:id", element: <p>节点页</p> }], "/");
}
const tile = (name: string) => screen.getByRole("link", { name });

it("按地区分组、在线数降序、未知最后；组头只写计数；方块按四态标记，离线写最后上报并变淡", async () => {
  render();
  await screen.findByText("2 / 4 在线");
  const groups = screen.getAllByRole("group").filter((g) => g.classList.contains("wall-group"));
  expect(groups.map((g) => g.querySelector("summary")?.textContent)).toEqual(["🇯🇵 日本 · 1 / 2 在线", "🇭🇰 香港 · 0 / 1 在线", "未知 · 0 / 1 在线"]);
  expect(tile("tokyo-1").closest("li")).toHaveAttribute("data-status", "online");
  expect(within(tile("tokyo-1").closest("li")!).getByRole("meter", { name: "CPU 95%" })).toHaveAttribute("data-level", "critical");
  expect(tile("tokyo-2").closest("li")).toHaveAttribute("data-status", "offline");
  expect(within(tile("tokyo-2").closest("li")!).getByText("离线 · 16 分钟前")).toBeInTheDocument();
  expect(within(tile("tokyo-2").closest("li")!).queryByRole("meter")).toBeNull();
  expect(tile("hk-1").closest("li")).toHaveAttribute("data-status", "maintenance");
  expect(within(tile("hk-1").closest("li")!).getByText("维护中")).toBeInTheDocument();
  expect(tile("fresh").closest("li")).toHaveAttribute("data-status", "never");
});

it("宽屏：默认选中墙上第一个节点，点方块只切换详情不导航；详情面板显示全部公开字段与 1 小时迷你线", async () => {
  const { router } = render();
  await screen.findByText("2 / 4 在线");
  const panel = within(await screen.findByRole("complementary", { name: "节点详情" }));
  expect(panel.getByRole("heading", { name: "tokyo-1" })).toBeInTheDocument();
  expect(tile("tokyo-1").closest("li")).toHaveAttribute("aria-current", "true");
  expect(panel.getByText("在线 · 最近上报 刚刚")).toBeInTheDocument();
  expect(panel.getByText("联通 4837")).toBeInTheDocument();
  expect(panel.getByText("prod")).toBeInTheDocument();
  expect(panel.getByText("Debian 13 · kvm · amd64")).toBeInTheDocument();
  expect(panel.getByText("EPYC × 2")).toBeInTheDocument();
  expect(panel.getByText("运行 1d 1h")).toBeInTheDocument();
  expect(panel.getByRole("meter", { name: "内存 8.0 GiB / 10 GiB" })).toHaveAttribute("aria-valuenow", "80");
  expect(panel.getByRole("meter", { name: "交换 0 B / 1.0 GiB" })).toHaveAttribute("aria-valuenow", "0");
  expect(panel.getByText("0.50 / 0.40 / 0.30")).toBeInTheDocument();
  expect(panel.getByText("↓ 2.0 KiB/s ↑ 1.0 KiB/s")).toBeInTheDocument();
  expect(panel.getByText("本周期 1.0 GiB")).toBeInTheDocument();
  expect(panel.getByText("读 10 B/s 写 20 B/s")).toBeInTheDocument();
  expect(panel.getByText("TCP 12 · UDP 3 · 进程 99")).toBeInTheDocument();
  expect(panel.getByText("US$12.50 / 月")).toBeInTheDocument();
  expect(panel.getByText("2030-01-01（剩 12 天）")).toBeInTheDocument();
  expect(panel.getByRole("link", { name: "查看完整历史 →" })).toHaveAttribute("href", "/nodes/1");
  // 迷你线：1 小时、60 点，取 rx+tx 的速率。请求窗口以 hub 的 now 为准。
  await waitFor(() => expect(queryMetrics).toHaveBeenCalled());
  // now=1000 对齐到整分钟是 960：from = 960 − 3600，to = 960 + 60。
  expect((queryMetrics.mock.calls[0] as unknown[])[0]).toMatchObject({ nodeId: 1n, from: -2_640n, to: 1_020n, maxPoints: 60 });
  expect(await panel.findByRole("img", { name: "最近 1 小时网络速率" })).toBeInTheDocument();
  fireEvent.click(tile("hk-1"));
  expect(router.state.location.pathname).toBe("/");
  expect(panel.getByRole("heading", { name: "hk-1" })).toBeInTheDocument();
  expect(panel.getByText("维护中 · 最近上报 刚刚")).toBeInTheDocument();
  expect(tile("hk-1").closest("li")).toHaveAttribute("aria-current", "true");
  expect(tile("tokyo-1").closest("li")).not.toHaveAttribute("aria-current");
});

it("详情面板里无读数的项显示为破折号，从未上报的节点没有读数也没有系统信息", async () => {
  render();
  await screen.findByText("2 / 4 在线");
  fireEvent.click(tile("fresh"));
  const panel = within(screen.getByRole("complementary", { name: "节点详情" }));
  expect(panel.getByText("从未上报")).toBeInTheDocument();
  expect(panel.queryAllByRole("meter")).toHaveLength(0);
  expect(panel.getAllByLabelText("无读数").length).toBeGreaterThan(0);
  expect(panel.queryByText(/×/)).toBeNull();
});

it("选中的节点从快照消失后退回墙上第一个节点", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  let current = snapshot;
  render(async () => current);
  await screen.findByText("2 / 4 在线");
  fireEvent.click(tile("hk-1"));
  expect(within(screen.getByRole("complementary", { name: "节点详情" })).getByRole("heading", { name: "hk-1" })).toBeInTheDocument();
  current = { ...snapshot, nodes: snapshot.nodes.filter((n) => n.name !== "hk-1") };
  await act(async () => vi.advanceTimersByTimeAsync(POLL_MS + 100));
  await waitFor(() => expect(screen.queryByRole("link", { name: "hk-1" })).toBeNull());
  expect(within(screen.getByRole("complementary", { name: "节点详情" })).getByRole("heading", { name: "tokyo-1" })).toBeInTheDocument();
});

it("窄屏：点方块直接进节点页，不渲染详情面板", async () => {
  vi.stubGlobal("matchMedia", (query: string) => ({ matches: false, media: query, addEventListener() {}, removeEventListener() {} }));
  try {
    const { router } = render();
    await screen.findByText("2 / 4 在线");
    expect(screen.queryByRole("complementary", { name: "节点详情" })).toBeNull();
    fireEvent.click(tile("tokyo-2"));
    await waitFor(() => expect(router.state.location.pathname).toBe("/nodes/2"));
  } finally {
    vi.unstubAllGlobals();
  }
});

it("宽屏按住修饰键点方块仍是普通链接（新标签页打开）", async () => {
  const { router } = render();
  await screen.findByText("2 / 4 在线");
  fireEvent.click(tile("tokyo-2"), { metaKey: true });
  // react-router 对带修饰键的点击不接管，jsdom 不会真的打开新标签，路径不变即可。
  expect(router.state.location.pathname).toBe("/");
  expect(within(screen.getByRole("complementary", { name: "节点详情" })).getByRole("heading", { name: "tokyo-1" })).toBeInTheDocument();
});

it("着色依据切到 CPU 时方块带档位，状态时不带", async () => {
  render();
  await screen.findByText("2 / 4 在线");
  expect(tile("tokyo-1").closest("li")).not.toHaveAttribute("data-level");
  fireEvent.change(screen.getByRole("combobox", { name: "着色依据" }), { target: { value: "cpu" } });
  expect(tile("tokyo-1").closest("li")).toHaveAttribute("data-level", "critical");
  expect(tile("fresh").closest("li")).not.toHaveAttribute("data-level");
});
```

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/public/StatusWall.test.tsx > /tmp/t15-red.log 2>&1; echo $?`
Expected: 1；红在找不到 `wall-group`、`complementary`。

- [ ] **Step 3: 实现 useMediaQuery**

`web/src/lib/useMediaQuery.ts`：

```ts
import { useSyncExternalStore } from "react";

// 状态墙在宽屏点方块切换详情、窄屏直接进节点页（设计 §3.4）：分支由同一个媒体查询决定，与 public.css 隐藏详情面板的断点一致。
export const WIDE_QUERY = "(min-width: 901px)";

export function useMediaQuery(query: string): boolean {
  return useSyncExternalStore(
    (onChange) => {
      const media = window.matchMedia(query);
      media.addEventListener("change", onChange);
      return () => media.removeEventListener("change", onChange);
    },
    () => window.matchMedia(query).matches,
  );
}
```

- [ ] **Step 4: 实现 StatusWall 与 DetailPanel**

`web/src/public/StatusWall.tsx`：

```tsx
import type { MouseEvent } from "react";
import { Link } from "react-router";
import { Bar, ratio } from "../components/Bar";
import type { PublicNode } from "../gen/heron/v1/public_pb";
import { flag } from "../lib/country";
import { ago, percent } from "../lib/format";
import { nodeStatus, STATUS_LABEL } from "../lib/status";
import { useMediaQuery, WIDE_QUERY } from "../lib/useMediaQuery";
import { DetailPanel } from "./DetailPanel";
import { groupByRegion, tileLevel, type ColorBy } from "./filters";

// 状态墙（设计 §3.1）：左侧按地区分组的方块，右侧固定详情面板随点选切换。方块是节点页的链接：宽屏上不带修饰键的左键点击
// 只切换详情（面板里有「查看完整历史」去节点页），其余情况（窄屏、中键、⌘ / Ctrl / Shift）都是普通链接。
// 选中的节点不在当前列表里（被筛掉或转私有）时退回墙上第一个节点。
export function StatusWall({ nodes, now, colorBy, selectedId, onSelect }: {
  nodes: readonly PublicNode[]; now: number; colorBy: ColorBy; selectedId: bigint | null; onSelect: (id: bigint) => void;
}) {
  const wide = useMediaQuery(WIDE_QUERY);
  const groups = groupByRegion(nodes);
  const selected = nodes.find((n) => n.id === selectedId) ?? groups[0]?.nodes[0];
  const pick = (event: MouseEvent, id: bigint) => {
    if (!wide || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    onSelect(id);
  };
  return (
    <div className="wall-layout">
      <div className="wall">
        {groups.map((group) => (
          <details className="wall-group" role="group" aria-label={`${group.name} ${group.online} / ${group.nodes.length} 在线`} key={group.code} open>
            <summary>{group.code ? `${flag(group.code)} ${group.name}` : group.name} · {group.online} / {group.nodes.length} 在线</summary>
            <ul className="tiles">
              {group.nodes.map((n) => {
                const status = nodeStatus(n);
                const m = n.metrics;
                const live = status === "online" || status === "maintenance";
                return (
                  <li key={String(n.id)} className="tile" data-status={status} data-level={tileLevel(n, colorBy)} aria-current={selected?.id === n.id ? "true" : undefined}>
                    <Link to={`/nodes/${n.id}`} aria-label={n.name} onClick={(event) => pick(event, n.id)}>
                      <span className="tile-name" title={n.name}>{n.name}</span>
                      <span className="status-dot" data-status={status} aria-hidden="true" />
                      {status === "offline" && n.lastSeenAt !== undefined && <span className="tile-note">离线 · {ago(n.lastSeenAt, now)}</span>}
                      {(status === "never" || status === "maintenance") && <span className="tile-note">{STATUS_LABEL[status]}</span>}
                      {live && m && (
                        <span className="tile-meters">
                          {m.cpuPct !== undefined && <><Bar thin value={m.cpuPct} label={`CPU ${percent(m.cpuPct)}`} /><span className="num">{percent(m.cpuPct)}</span></>}
                          {m.memUsed !== undefined && m.memTotal ? <><Bar thin value={ratio(m.memUsed, m.memTotal)} label={`内存 ${percent(ratio(m.memUsed, m.memTotal))}`} /><span className="num">{percent(ratio(m.memUsed, m.memTotal))}</span></> : null}
                        </span>
                      )}
                      <span className="tile-chevron" aria-hidden="true">›</span>
                    </Link>
                  </li>
                );
              })}
            </ul>
          </details>
        ))}
      </div>
      {wide && selected && <DetailPanel node={selected} now={now} />}
    </div>
  );
}
```

`web/src/public/DetailPanel.tsx`：

```tsx
import { useQuery } from "@connectrpc/connect-query";
import type { ReactNode } from "react";
import { Link } from "react-router";
import { Bar, Missing, ratio } from "../components/Bar";
import { CountryBadge } from "../components/CountryBadge";
import { Sparkline } from "../components/Sparkline";
import { StatusBadge } from "../components/StatusBadge";
import { PublicService, type PublicNode } from "../gen/heron/v1/public_pb";
import { expired, expiryText, priceText } from "../lib/billing";
import { ago, bytes, duration, percent } from "../lib/format";
import { toAligned } from "../lib/series";
import { nodeStatus } from "../lib/status";

const HOUR_S = 3600;
// 迷你线 60 个点、每点一分钟。窗口右端对齐到 hub 时钟的整分钟：快照每 2 秒刷新，不对齐会每次都换查询键。
const SPARK_POINTS = 60;

function Row({ label, children }: { label: string; children: ReactNode }) {
  return <div className="detail-row"><dt>{label}</dt><dd>{children}</dd></div>;
}

function Capacity({ label, used, total }: { label: string; used?: bigint; total?: bigint }) {
  if (used === undefined || total === undefined || total === 0n) return <Row label={label}><Missing /></Row>;
  return <Row label={label}><Bar value={ratio(used, total)} label={`${label} ${bytes(used)} / ${bytes(total)}`} /></Row>;
}

// 详情面板（设计 §3.1）：选中节点的全部公开字段；网络速率带最近 1 小时迷你线——这是公开总览唯一的历史查询，
// 只为选中的一个节点发（公开限流桶 60、每秒补 10，按卡片各发一次会把访客自己限掉）。
export function DetailPanel({ node, now }: { node: PublicNode; now: number }) {
  const status = nodeStatus(node);
  const m = node.metrics;
  const f = node.facts;
  const minute = Math.floor(now / 60) * 60;
  const from = minute - HOUR_S;
  const to = minute + 60;
  const history = useQuery(PublicService.method.queryMetrics, { nodeId: node.id, from: BigInt(from), to: BigInt(to), maxPoints: SPARK_POINTS }, { staleTime: 60_000 });
  const spark = history.data ? toAligned(history.data, [{ name: "rx_bytes", value: "sum-rate", label: "下行" }, { name: "tx_bytes", value: "sum-rate", label: "上行" }], from, to) : null;
  const rate = spark ? (spark[1] as (number | null)[]).map((rx, i) => {
    const tx = (spark[2] as (number | null)[])[i];
    return rx === null && tx === null ? null : (rx ?? 0) + (tx ?? 0);
  }) : null;
  const price = priceText(node.billing);
  const expiry = expiryText(node.billing);
  return (
    <aside className="detail-panel" aria-label="节点详情">
      <header>
        <h2>{node.name}</h2>
        {node.country && <CountryBadge code={node.country} />}
        <StatusBadge status={status} detail={node.lastSeenAt !== undefined ? `最近上报 ${ago(node.lastSeenAt, now)}` : undefined} />
      </header>
      {node.publicRemark && <p className="node-remark">{node.publicRemark}</p>}
      {node.tags.length > 0 && <ul className="tag-chips">{node.tags.map((tag) => <li key={tag} className="chip">{tag}</li>)}</ul>}
      <dl className="detail-list">
        {f && <Row label="系统">{[f.os, f.virtualization, f.arch].filter(Boolean).join(" · ") || <Missing />}</Row>}
        {f && f.cpuModel && <Row label="CPU">{f.cpuModel} × {f.cpuCores}</Row>}
        <Row label="运行时长">{m?.uptimeS !== undefined ? `运行 ${duration(m.uptimeS)}` : <Missing />}</Row>
        <Row label="CPU 使用">{m?.cpuPct !== undefined ? <Bar value={m.cpuPct} label={`CPU ${percent(m.cpuPct)}`} /> : <Missing />}</Row>
        <Capacity label="内存" used={m?.memUsed} total={m?.memTotal} />
        <Capacity label="交换" used={m?.swapUsed} total={m?.swapTotal} />
        <Capacity label="磁盘" used={m?.diskUsed} total={m?.diskTotal} />
        <Row label="负载">{m?.load1 !== undefined && m.load5 !== undefined && m.load15 !== undefined ? <span className="num">{m.load1.toFixed(2)} / {m.load5.toFixed(2)} / {m.load15.toFixed(2)}</span> : <Missing />}</Row>
        <Row label="网络">
          {m?.netRxBps !== undefined && m.netTxBps !== undefined ? <span className="num">↓ {bytes(m.netRxBps)}/s ↑ {bytes(m.netTxBps)}/s</span> : <Missing />}
          {rate && <Sparkline values={rate} label="最近 1 小时网络速率" />}
        </Row>
        <Row label="流量">{node.traffic ? <span className="num">本周期 {bytes(node.traffic.periodRx + node.traffic.periodTx)}</span> : <Missing />}</Row>
        <Row label="磁盘读写">{m?.diskReadBps !== undefined && m.diskWriteBps !== undefined ? <span className="num">读 {bytes(m.diskReadBps)}/s 写 {bytes(m.diskWriteBps)}/s</span> : <Missing />}</Row>
        <Row label="连接">{m?.tcpConns !== undefined && m.udpConns !== undefined && m.procs !== undefined ? <span className="num">TCP {m.tcpConns} · UDP {m.udpConns} · 进程 {m.procs}</span> : <Missing />}</Row>
        {price && <Row label="费用"><span className="num">{price}</span></Row>}
        {expiry && <Row label="到期"><span className={expired(node.billing) ? "num error" : "num"}>{expiry}</span></Row>}
      </dl>
      <Link to={`/nodes/${node.id}`} className="detail-more">查看完整历史 →</Link>
    </aside>
  );
}
```

`web/src/public/Overview.tsx`：加 `const [selectedId, setSelectedId] = useState<bigint | null>(null);` 与 `const now = Number(gate.data.now);`，把占位 `<ul>` 换成：

```tsx
          {nodes.length > 0 && view === "wall" && <StatusWall nodes={nodes} now={now} colorBy={colorBy} selectedId={selectedId} onSelect={setSelectedId} />}
          {nodes.length > 0 && view === "cards" && (
            <ul>
              {sortCards(nodes, sort).map((n) => <li key={String(n.id)}><article aria-label={n.name}>{n.name}</article></li>)}
            </ul>
          )}
```

`Overview.test.tsx` 里 `shown()` 在墙视图下要改为按方块里的链接取（详情面板的「查看完整历史」也是 /nodes/ 链接，按 `.tile` 祖先过滤）：`const shown = () => screen.queryAllByRole("link").filter((a) => a.closest(".tile")).map((a) => a.getAttribute("aria-label"));`。

`web/src/public/public.css` 追加：

```css
/* 状态墙（StatusWall.tsx）+ 详情面板（DetailPanel.tsx）：左 2/3 右 1/3，面板随滚动固定。 */
.wall-layout { display: grid; grid-template-columns: minmax(0, 2fr) minmax(300px, 1fr); gap: 16px; align-items: start; }
.wall-group { margin: 0 0 12px; }
.wall-group > summary { display: flex; align-items: center; gap: 8px; height: 32px; font-weight: 600; font-size: 13px; cursor: pointer; list-style: none; }
.wall-group > summary::-webkit-details-marker { display: none; }
.tiles { list-style: none; margin: 0; padding: 0; display: grid; grid-template-columns: repeat(auto-fill, minmax(128px, 1fr)); gap: 6px; }
.tile > a { position: relative; display: grid; grid-template-columns: minmax(0, 1fr) auto; grid-template-rows: auto auto; gap: 2px 6px; min-height: 56px; padding: 6px 8px; border: 1px solid var(--line); border-radius: var(--radius-card); background: var(--card); color: var(--fg); font-size: 12px; }
.tile > a:hover { background: var(--card-hover); border-color: var(--line-strong); }
.tile[aria-current] > a { border-color: var(--accent); box-shadow: inset 0 0 0 1px var(--accent); }
.tile-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 500; }
.tile .status-dot { align-self: center; }
.tile-note, .tile-meters { grid-column: 1 / -1; color: var(--muted); }
.tile-meters { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 2px 6px; align-items: center; font-size: 11px; }
.tile-meters .bar { min-width: 0; }
.tile-chevron { display: none; }
.tile[data-status="offline"] > a, .tile[data-status="never"] > a { opacity: .6; }
.tile[data-status="maintenance"] > a { border-color: color-mix(in srgb, var(--status-maintenance) 50%, var(--line)); }
.tile[data-level="attention"] > a { background: color-mix(in srgb, var(--status-attention) 12%, var(--card)); }
.tile[data-level="critical"] > a { background: color-mix(in srgb, var(--status-offline) 14%, var(--card)); }
.detail-panel { position: sticky; top: 64px; max-height: calc(100dvh - 80px); overflow: auto; padding: 16px; border: 1px solid var(--line); border-radius: var(--radius-card); background: var(--card); font-size: 13px; }
.detail-panel > header { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; margin-bottom: 8px; }
.detail-panel h2 { margin: 0; font-size: 16px; overflow-wrap: anywhere; }
.tag-chips { list-style: none; display: flex; flex-wrap: wrap; gap: 4px; margin: 0 0 8px; padding: 0; }
.detail-list { margin: 0; display: grid; gap: 6px; }
.detail-row { display: grid; grid-template-columns: 64px minmax(0, 1fr); gap: 8px; align-items: center; }
.detail-row dt { color: var(--muted); }
.detail-row dd { margin: 0; min-width: 0; overflow-wrap: anywhere; }
.detail-row .bar { min-width: 0; height: 14px; }
.detail-row .sparkline { margin-top: 4px; width: 100%; }
.detail-more { display: inline-block; margin-top: 12px; }
/* 手机版（设计 §3.4）：墙降级为单列列表，组头吸顶；详情面板不渲染（StatusWall 按同一断点判断）。 */
@media (max-width: 900px) {
  .wall-layout { grid-template-columns: minmax(0, 1fr); }
  .wall-group > summary { position: sticky; top: 48px; z-index: 5; background: var(--bg); }
  .tiles { grid-template-columns: minmax(0, 1fr); gap: 0; }
  .tile > a { grid-template-columns: auto minmax(0, 1fr) auto auto; grid-template-rows: auto; align-items: center; min-height: 44px; border-radius: 0; border-width: 0 0 1px; }
  .tile .status-dot { order: -1; }
  .tile-note, .tile-meters { grid-column: auto; }
  .tile-meters { grid-template-columns: auto auto; }
  .tile-meters .bar { display: none; }
  .tile-chevron { display: inline; color: var(--faint); }
}
```

- [ ] **Step 5: 跑测试确认绿，再跑全量与类型检查**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/public/StatusWall.test.tsx src/public/Overview.test.tsx > /tmp/t15.log 2>&1; echo $?`
Expected: 0。

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm typecheck > /tmp/t15-tsc.log 2>&1; echo $?; pnpm vitest run > /tmp/t15-all.log 2>&1; echo $?`
Expected: 两个 0。

- [ ] **Step 6: 缺陷注入**

把 `StatusWall` 的 `?? groups[0]?.nodes[0]` 删掉，「选中的节点从快照消失后…」应红在面板不存在；把 `pick` 里的 `!wide ||` 删掉，「窄屏」用例应红在路径仍是 `/`。改回后绿。

- [ ] **Step 7: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/lib/useMediaQuery.ts web/src/public/StatusWall.tsx web/src/public/DetailPanel.tsx web/src/public/StatusWall.test.tsx web/src/public/Overview.tsx web/src/public/Overview.test.tsx web/src/public/public.css web/src/test/setup.ts && git commit -m "feat(web): 公开总览默认为地区分组状态墙与详情面板，手机上降级为列表"
```

---

### Task 16: 卡片视图与离线折叠

**Files:**
- Modify: `web/src/public/NodeCard.tsx`（整体重写，新增导出 `CardGrid`）
- Create: `web/src/public/NodeCard.test.tsx`（Task 14 已删旧文件）
- Modify: `web/src/public/Overview.tsx`（卡片视图接入）
- Modify: `web/src/public/public.css`（替换旧的 `.cards`、`.node-card*`、`.node-*` 全部规则）

**Interfaces:**
- Consumes: `StatusBadge`、`Bar thin`（Task 3）、`nodeStatus`、`expiryLevel`（Task 2）、`sortCards`（Task 11）。
- Produces:
  ```ts
  export function NodeCard({ node, now }: { node: PublicNode; now: number }): JSX.Element;   // <article aria-label={name} data-status>
  export function CardGrid({ nodes, now }: { nodes: readonly PublicNode[]; now: number }): JSX.Element;
  ```
  卡片只放在线与维护中；其余折进 `details.folded-nodes`（`summary` 文本「离线与从未上报 · N」），展开是表格（名称、地区、状态、最后上报）。卡片底部两行：`本周期 <num> · 费用 <num>`、`到期 <num> · 剩 N 天`（`data-level` 由 `expiryLevel`）。

- [ ] **Step 1: 写测试（红）**

`web/src/public/NodeCard.test.tsx`：

```tsx
import { fireEvent, screen, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { PublicService } from "../gen/heron/v1/public_pb";
import { BillingCycle } from "../gen/heron/v1/types_pb";
import { renderWithService } from "../test/harness";
import { PublicOverview } from "./Overview";

afterEach(() => vi.useRealTimers());

async function renderCards(nodes: object[]) {
  renderWithService(PublicService, { getSnapshot: async () => ({ now: 1000n, nodes, tags: [] }) }, [{ path: "/", Component: PublicOverview }], "/");
  await screen.findByRole("group", { name: "视图" });
  fireEvent.click(screen.getByRole("button", { name: "卡片" }));
}

it("卡片：名称、国家、状态徽章；系统 · 虚拟化 · 架构 · 运行时长；三条进度条；↓↑ 速率；底部两行费用与到期", async () => {
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(new Date(2031, 0, 1));
  await renderCards([{
    id: 1n, name: "香港家宽", online: true, lastSeenAt: 998n, country: "HK", tags: ["家宽"], publicRemark: "联通 4837",
    facts: { os: "Debian 13", arch: "amd64", virtualization: "kvm", cpuCores: 2 },
    metrics: { cpuPct: 0, memUsed: 0n, memTotal: 1024n ** 3n, diskUsed: 9n * 1024n ** 3n, diskTotal: 10n * 1024n ** 3n, uptimeS: 90000n, netRxBps: 0n, netTxBps: 1024n },
    traffic: { periodRx: 1024n ** 3n, periodTx: 2n * 1024n ** 3n },
    billing: { price: "12.50", currency: "USD", billingCycle: BillingCycle.MONTHLY, expiresOn: "2026-10-01", daysLeft: 4 },
  }]);
  const card = within(screen.getByRole("article", { name: "香港家宽" }));
  expect(card.getByRole("link", { name: "香港家宽" })).toHaveAttribute("href", "/nodes/1");
  expect(card.getByTitle("国家 / 地区 HK")).toBeInTheDocument();
  expect(card.getByText("在线")).toHaveAttribute("data-status", "online");
  expect(card.getByText("联通 4837")).toBeInTheDocument();
  expect(card.getByText("Debian 13 · kvm · amd64 · 运行 1d 1h")).toBeInTheDocument();
  expect(card.getByRole("meter", { name: "CPU 0.0%" })).toHaveAttribute("aria-valuenow", "0");
  expect(card.getByRole("meter", { name: "内存 0 B / 1.0 GiB" })).toHaveAttribute("aria-valuenow", "0");
  expect(card.getByRole("meter", { name: "磁盘 9.0 GiB / 10 GiB" })).toHaveAttribute("data-level", "attention");
  expect(card.getByRole("group", { name: "网络速率" })).toHaveTextContent("↓ 0 B/s ↑ 1.0 KiB/s");
  expect(card.getByText("本周期").nextElementSibling).toHaveTextContent("3.0 GiB");
  expect(card.getByText("费用").nextElementSibling).toHaveTextContent("US$12.50 / 月");
  expect(card.getByText("到期").nextElementSibling).toHaveTextContent("2026-10-01");
  expect(card.getByText("剩 4 天")).toHaveAttribute("data-level", "attention");
});

it("没填费用与到期时底行写破折号；已过期写已过期天数并标玫红；无读数不画进度条", async () => {
  await renderCards([
    { id: 1n, name: "plain", online: true, lastSeenAt: 998n },
    { id: 2n, name: "lapsed", online: true, lastSeenAt: 998n, billing: { expiresOn: "2026-09-24", daysLeft: -3 } },
  ]);
  const plain = within(screen.getByRole("article", { name: "plain" }));
  expect(plain.getByText("费用").nextElementSibling).toHaveTextContent("–");
  expect(plain.getByText("到期").nextElementSibling).toHaveTextContent("–");
  expect(plain.queryAllByRole("meter")).toHaveLength(0);
  expect(plain.getByText("系统未知")).toBeInTheDocument();
  const lapsed = within(screen.getByRole("article", { name: "lapsed" }));
  expect(lapsed.getByText("已过期 3 天")).toHaveAttribute("data-level", "critical");
});

it("只有在线与维护中出卡片；离线与从未上报折进默认收起的表格（名称、地区、状态、最后上报）", async () => {
  await renderCards([
    { id: 1n, name: "on", online: true, lastSeenAt: 998n, country: "JP" },
    { id: 2n, name: "maint", online: false, lastSeenAt: 900n, maintenance: true },
    { id: 3n, name: "off", online: false, lastSeenAt: 400n, country: "HK" },
    { id: 4n, name: "fresh", online: false },
  ]);
  expect(screen.getAllByRole("article").map((a) => a.getAttribute("aria-label"))).toEqual(["on", "maint"]);
  expect(within(screen.getByRole("article", { name: "maint" })).getByText("维护中")).toHaveAttribute("data-status", "maintenance");
  const folded = screen.getByText("离线与从未上报 · 2").closest("details")!;
  expect(folded).not.toHaveAttribute("open");
  const rows = within(folded).getAllByRole("row").slice(1).map((row) => within(row).getAllByRole("cell").map((cell) => cell.textContent));
  expect(rows).toEqual([["off", "🇭🇰 HK", "离线", "10 分钟前"], ["fresh", "", "从未上报", "–"]]);
  expect(within(folded).getByRole("link", { name: "off" })).toHaveAttribute("href", "/nodes/3");
});

it("卡片排序：到期 / CPU / 流量只重排卡片", async () => {
  await renderCards([
    { id: 1n, name: "a", online: true, lastSeenAt: 1n, metrics: { cpuPct: 10 }, billing: { expiresOn: "2030-01-01", daysLeft: 30 } },
    { id: 2n, name: "b", online: true, lastSeenAt: 1n, metrics: { cpuPct: 90 }, billing: { expiresOn: "2030-01-01", daysLeft: 5 } },
  ]);
  const order = () => screen.getAllByRole("article").map((a) => a.getAttribute("aria-label"));
  expect(order()).toEqual(["a", "b"]);
  fireEvent.change(screen.getByRole("combobox", { name: "排序" }), { target: { value: "cpu" } });
  expect(order()).toEqual(["b", "a"]);
  fireEvent.change(screen.getByRole("combobox", { name: "排序" }), { target: { value: "expiry" } });
  expect(order()).toEqual(["b", "a"]);
  fireEvent.change(screen.getByRole("combobox", { name: "排序" }), { target: { value: "default" } });
  expect(order()).toEqual(["a", "b"]);
});
```

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/public/NodeCard.test.tsx > /tmp/t16-red.log 2>&1; echo $?`
Expected: 1；红在 article 里找不到链接 / 徽章（占位 article 只有名字）。

- [ ] **Step 3: 实现**

`web/src/public/NodeCard.tsx` 整体替换为：

```tsx
import { Link } from "react-router";
import { Bar, Missing, ratio } from "../components/Bar";
import { CountryBadge } from "../components/CountryBadge";
import { StatusBadge } from "../components/StatusBadge";
import type { PublicNode } from "../gen/heron/v1/public_pb";
import { priceText } from "../lib/billing";
import { ago, bytes, duration, percent } from "../lib/format";
import { expiryLevel, nodeStatus } from "../lib/status";

// 卡片（设计 §3.1「卡片视图」）：只放在线与维护中的节点，离线与从未上报折在网格下方。
export function NodeCard({ node, now }: { node: PublicNode; now: number }) {
  const status = nodeStatus(node);
  const m = node.metrics;
  const f = node.facts;
  const system = [f?.os, f?.virtualization, f?.arch].filter(Boolean).join(" · ") || "系统未知";
  const uptime = m?.uptimeS !== undefined ? `运行 ${duration(m.uptimeS)}` : null;
  const price = priceText(node.billing);
  const b = node.billing;
  const daysLeft = b?.daysLeft;
  return (
    <article className="node-card" aria-label={node.name} data-status={status}>
      <header className="node-card-head">
        <h2><Link to={`/nodes/${node.id}`} title={node.name}>{node.name}</Link></h2>
        {node.country && <CountryBadge code={node.country} />}
        <StatusBadge status={status} />
      </header>
      {node.publicRemark && <p className="node-remark">{node.publicRemark}</p>}
      <p className="node-card-meta muted">{uptime ? `${system} · ${uptime}` : system}</p>
      <div className="node-meters">
        <Meter label="CPU" value={m?.cpuPct} text={m?.cpuPct !== undefined ? percent(m.cpuPct) : undefined} />
        <Meter label="内存" value={m?.memUsed !== undefined && m.memTotal ? ratio(m.memUsed, m.memTotal) : undefined} text={m?.memUsed !== undefined && m.memTotal ? `${bytes(m.memUsed)} / ${bytes(m.memTotal)}` : undefined} />
        <Meter label="磁盘" value={m?.diskUsed !== undefined && m.diskTotal ? ratio(m.diskUsed, m.diskTotal) : undefined} text={m?.diskUsed !== undefined && m.diskTotal ? `${bytes(m.diskUsed)} / ${bytes(m.diskTotal)}` : undefined} />
      </div>
      <p className="node-network num" role="group" aria-label="网络速率">
        ↓ {m?.netRxBps !== undefined ? `${bytes(m.netRxBps)}/s` : <Missing />} ↑ {m?.netTxBps !== undefined ? `${bytes(m.netTxBps)}/s` : <Missing />}
      </p>
      <footer className="node-card-foot">
        <div><span className="muted">本周期</span><span className="num">{node.traffic ? bytes(node.traffic.periodRx + node.traffic.periodTx) : "–"}</span><span className="muted">费用</span><span className="num">{price || "–"}</span></div>
        <div>
          <span className="muted">到期</span><span className="num">{b?.expiresOn || "–"}</span>
          {daysLeft !== undefined && <span className="num" data-level={expiryLevel(daysLeft)}>{daysLeft < 0 ? `已过期 ${-daysLeft} 天` : `剩 ${daysLeft} 天`}</span>}
        </div>
      </footer>
    </article>
  );
}

// 无读数与 0 是两个事实：缺失不画条，画一条虚线轨。
function Meter({ label, value, text }: { label: string; value?: number; text?: string }) {
  return (
    <div className="node-meter">
      <span className="muted">{label}</span>
      {value !== undefined ? <Bar thin value={value} label={`${label} ${text ?? percent(value)}`} /> : <span className="meter-missing" aria-hidden="true" />}
      <span className="num">{text ?? <Missing />}</span>
    </div>
  );
}

// 卡片网格 + 折叠的离线表。传入的 nodes 已按调用方的排序；折叠表保持同一顺序。
export function CardGrid({ nodes, now }: { nodes: readonly PublicNode[]; now: number }) {
  const live = nodes.filter((n) => { const s = nodeStatus(n); return s === "online" || s === "maintenance"; });
  const rest = nodes.filter((n) => !live.includes(n));
  return (
    <>
      <div className="cards">
        {live.map((n) => <NodeCard key={String(n.id)} node={n} now={now} />)}
      </div>
      {rest.length > 0 && (
        <details className="folded-nodes">
          <summary>离线与从未上报 · {rest.length}</summary>
          <table className="compact-table">
            <thead><tr><th>名称</th><th>地区</th><th>状态</th><th>最后上报</th></tr></thead>
            <tbody>
              {rest.map((n) => (
                <tr key={String(n.id)}>
                  <td><Link to={`/nodes/${n.id}`}>{n.name}</Link></td>
                  <td>{n.country && <CountryBadge code={n.country} />}</td>
                  <td><StatusBadge status={nodeStatus(n)} /></td>
                  <td className="num">{n.lastSeenAt !== undefined ? ago(n.lastSeenAt, now) : "–"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </details>
      )}
    </>
  );
}
```

`web/src/public/Overview.tsx` 卡片分支换成 `<CardGrid nodes={sortCards(nodes, sort)} now={now} />`（import `CardGrid`）。`Overview.test.tsx` 的「视图切换」用例不依赖卡片内容，不改。

`web/src/public/public.css`：删除从 `.cards {` 到 `@media (max-width: 480px) { … }` 这段旧卡片规则（含 `.node-card*`、`.node-resources`、`.node-network`、`.node-billing`、`.node-card-footer`、`.stale-reading`、`@keyframes node-card-enter`、两条媒体查询），追加：

```css
/* 卡片视图（NodeCard.tsx）：五列约 200px 高，1px 边框分层，不用阴影与渐变（设计 §2、§3.1）。 */
.cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(min(240px, 100%), 1fr)); gap: 10px; }
.node-card { display: flex; flex-direction: column; gap: 6px; min-width: 0; padding: 10px 12px; border: 1px solid var(--line); border-radius: var(--radius-card); background: var(--card); font-size: 13px; }
.node-card:hover { border-color: var(--line-strong); }
.node-card-head { display: flex; align-items: center; gap: 6px; min-width: 0; }
.node-card h2 { flex: 1; min-width: 0; margin: 0; font-size: 14px; }
.node-card h2 a { display: block; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; color: var(--fg); }
.node-card .country-badge { flex: none; }
.node-card-meta { margin: 0; font-size: 12px; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
.node-remark { margin: 0; font-size: 12px; color: var(--muted); overflow-wrap: anywhere; }
.node-meters { display: grid; gap: 4px; }
.node-meter { display: grid; grid-template-columns: 2.5em minmax(0, 1fr) auto; align-items: center; gap: 8px; font-size: 12px; }
.meter-missing { height: 4px; border-radius: 999px; background: repeating-linear-gradient(90deg, var(--line) 0 4px, transparent 4px 8px); }
.node-network { margin: 0; font-size: 12px; }
.node-card-foot { margin-top: auto; padding-top: 6px; border-top: 1px solid var(--line); font-size: 12px; display: grid; gap: 2px; }
.node-card-foot > div { display: flex; gap: 6px; align-items: baseline; flex-wrap: wrap; }
.node-card-foot [data-level="attention"] { color: var(--status-attention); }
.node-card-foot [data-level="critical"] { color: var(--status-offline); }
.folded-nodes { margin-top: 16px; }
.folded-nodes > summary { cursor: pointer; color: var(--muted); font-size: 13px; height: 32px; display: flex; align-items: center; }
.compact-table { width: 100%; border-collapse: collapse; font-size: 13px; background: var(--card); border: 1px solid var(--line); border-radius: var(--radius-card); }
.compact-table th { height: 32px; text-align: left; padding: 0 10px; color: var(--muted); font-weight: 500; font-size: 12px; border-bottom: 1px solid var(--line); }
.compact-table td { height: 36px; padding: 0 10px; border-bottom: 1px solid var(--line); white-space: nowrap; }
.compact-table td:first-child { max-width: 0; overflow: hidden; text-overflow: ellipsis; }
```

- [ ] **Step 4: 跑测试确认绿，再跑全量**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/public/NodeCard.test.tsx src/public/Overview.test.tsx > /tmp/t16.log 2>&1; echo $?; pnpm vitest run > /tmp/t16-all.log 2>&1; echo $?`
Expected: 两个 0。

- [ ] **Step 5: 缺陷注入**

把 `CardGrid` 的 `live` 过滤改成 `nodes.filter(() => true)`，折叠用例应红在 article 数；改回后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/public/NodeCard.tsx web/src/public/NodeCard.test.tsx web/src/public/Overview.tsx web/src/public/public.css && git commit -m "feat(web): 公开卡片视图只放在线节点，底行写费用与到期，离线折成表格"
```

---

### Task 17: 节点页

**Files:**
- Modify: `web/src/public/NodePage.tsx`（整体重写）
- Modify: `web/src/public/NodePage.test.tsx`
- Modify: `web/src/public/public.css`（追加 `.node-head*`、`.now-grid*`）

**Interfaces:**
- Consumes: `StatusBadge`（Task 3）、`MetricCharts`、`ProbeTaskCharts`、`RangePicker`（Task 10）、`nodeStatus`、`expiryLevel`（Task 2）。
- Produces: `NodePage()` 路由组件不变。DOM：`header.node-head`（h1 名称 + CountryBadge + StatusBadge（detail「最近上报 …」）+ 备注 + 标签胶囊）；`dl.now-grid` 六格，每格 `div[role=group][aria-label=<标签>]`：CPU、内存、磁盘、↓↑、运行时长、剩余天数；`header.row.detail-header` 里 `RangePicker`；指标图；探测区；系统信息卡 `dl.card.facts`（系统、架构、CPU、虚拟化）。

- [ ] **Step 1: 改测试（红）**

在 `web/src/public/NodePage.test.tsx` 把首个用例改为：

```tsx
it("公开节点的历史图表走 PublicService；首屏是实时状态头与六个现值格；每任务一张 RTT 图", async () => {
  const queryMetrics = vi.fn(async () => ({ level: "1m", stepS: 60, ts: [], series: [] }));
  const queryProbes = vi.fn(async () => create(QueryProbesResponseSchema, { level: "1m", stepS: 60, series: [{ taskId: 3n, kind: ProbeKind.TCP, target: "example.com:443" }] }));
  const live = async () => ({ now: 1_000n, nodes: [{ id: 7n, name: "edge-1", online: true, lastSeenAt: 998n, country: "DE", tags: ["edge", "eu"], publicRemark: "法兰克福",
    facts: { os: "Alpine 3.21", arch: "arm64", cpuModel: "Neoverse", cpuCores: 2, virtualization: "kvm" },
    metrics: { cpuPct: 42, memUsed: 512n * 1024n ** 2n, memTotal: 1024n ** 3n, diskUsed: 1024n ** 3n, diskTotal: 10n * 1024n ** 3n, netRxBps: 2048n, netTxBps: 1024n, uptimeS: 90_000n },
    billing: { expiresOn: "2030-01-01", daysLeft: 20 } }] });
  renderWithService(PublicService, { getSnapshot: live, queryMetrics, queryProbes }, [{ path: "/nodes/:id", Component: NodePage }], "/nodes/7");
  const head = (await screen.findByRole("heading", { level: 1, name: "edge-1" })).closest("header")!;
  expect(within(head).getByText("在线 · 最近上报 刚刚")).toHaveAttribute("data-status", "online");
  expect(within(head).getByText("法兰克福")).toBeInTheDocument();
  expect(within(head).getAllByText(/^(edge|eu)$/).map((el) => el.textContent)).toEqual(["edge", "eu"]);
  expect(within(screen.getByRole("group", { name: "CPU" })).getByText("42%")).toBeInTheDocument();
  expect(within(screen.getByRole("group", { name: "内存" })).getByText("50%")).toBeInTheDocument();
  expect(within(screen.getByRole("group", { name: "磁盘" })).getByText("10%")).toBeInTheDocument();
  expect(screen.getByRole("group", { name: "网络" })).toHaveTextContent("↓ 2.0 KiB/s ↑ 1.0 KiB/s");
  expect(screen.getByRole("group", { name: "运行时长" })).toHaveTextContent("1d 1h");
  expect(within(screen.getByRole("group", { name: "剩余天数" })).getByText("20 天")).toHaveAttribute("data-level", "attention");
  expect(await screen.findByRole("heading", { name: "TCP example.com:443" })).toBeInTheDocument();
  expect(screen.getAllByTestId("chart")).toHaveLength(11);
  for (const r of ["1h", "6h", "24h", "7d", "30d"]) expect(screen.getByRole("button", { name: r })).toBeInTheDocument();
  expect(screen.getByText("Alpine 3.21")).toBeInTheDocument();
  expect(screen.queryByLabelText("执行环境")).not.toBeInTheDocument();
  expect((queryMetrics.mock.calls[0] as unknown[])[0]).toMatchObject({ nodeId: 7n, maxPoints: 1000 });
});

it("从未上报的节点：状态头写从未上报，六格都是破折号，没有系统信息卡", async () => {
  renderWithService(PublicService, { getSnapshot: async () => ({ now: 1_000n, nodes: [{ id: 7n, name: "fresh", online: false }] }), queryMetrics: async () => ({ level: "1m", stepS: 60, ts: [], series: [] }), queryProbes: async () => ({ level: "1m", stepS: 60, series: [] }) }, [{ path: "/nodes/:id", Component: NodePage }], "/nodes/7");
  await screen.findByRole("heading", { level: 1, name: "fresh" });
  expect(screen.getByText("从未上报")).toHaveAttribute("data-status", "never");
  expect(screen.getAllByLabelText("无读数")).toHaveLength(6);
  expect(screen.queryByText("系统")).toBeNull();
});
```

「静态信息卡带费用与到期两行…」用例：费用与到期不再在信息卡（已在现值格与详情里），把该用例改名为「系统信息卡：主机信息缺失时不画卡片」并只保留 `show(7)` 断言 `Alpine 3.21`、`show(9)` 断言 `queryByRole("definition")` 为 null；其余两段删除。「节点页标题带国家 / 地区徽章」用例改为 `expect(h1).toHaveTextContent(/^edge-1$/)` + `expect(screen.getByTitle("国家 / 地区 DE"))…`（徽章移到 h1 外、同一 header 内）。import 补 `within`。

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/public/NodePage.test.tsx > /tmp/t17-red.log 2>&1; echo $?`
Expected: 1；红在找不到 `在线 · 最近上报 刚刚` 与 `group CPU`。

- [ ] **Step 3: 实现**

`web/src/public/NodePage.tsx` 整体替换为：

```tsx
import { useQuery } from "@connectrpc/connect-query";
import type { ReactNode } from "react";
import { Link, useParams } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
import { Missing, ratio } from "../components/Bar";
import { CountryBadge } from "../components/CountryBadge";
import { MetricCharts, ProbeTaskCharts, RangePicker, useHistory, type HistoryMethods } from "../components/History";
import { StatusBadge } from "../components/StatusBadge";
import { PublicService } from "../gen/heron/v1/public_pb";
import { ago, bytes, duration, percent } from "../lib/format";
import { POLL_MS } from "../lib/poll";
import { expiryLevel, nodeStatus } from "../lib/status";

const PUBLIC_HISTORY: HistoryMethods = { queryMetrics: PublicService.method.queryMetrics, queryProbes: PublicService.method.queryProbes };

function Now({ label, children }: { label: string; children: ReactNode }) {
  return <div className="now-cell" role="group" aria-label={label}><dt>{label}</dt><dd className="num">{children}</dd></div>;
}

// 节点页（设计 §3.2）：首屏是实时状态头与六个现值格，其下时间窗口、指标图、每任务一张 RTT 图、系统信息卡。
// 公开页不出现 IP、主机名、内核、agent 版本：PublicFacts 已 reserved 这些字段，前端不另有来源。
export function NodePage() {
  const { id } = useParams();
  const validId = /^\d+$/.test(id ?? "");
  const nodeId = validId ? BigInt(id!) : 0n;
  const snap = useQuery(PublicService.method.getSnapshot, {}, { enabled: validId, refetchInterval: POLL_MS });
  const node = snap.data?.nodes.find((n) => n.id === nodeId);
  // 只在快照里有这个节点时查历史：未公开或不存在的节点，历史查询只会得到 NotFound。
  const history = useHistory(PUBLIC_HISTORY, nodeId, node !== undefined);
  const missing = <p role="alert" className="error">节点 {id} 不存在或未公开。<Link to="/">返回总览</Link></p>;
  if (!validId) return missing;
  const gate = queryGate(snap);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  if (!node) return missing;
  const now = Number(gate.data.now);
  const m = node.metrics;
  const f = node.facts;
  const daysLeft = node.billing?.daysLeft;
  return (
    <section className="node-page">
      {errorBanner(snap.error, history.metrics.error, history.probes.error)}
      <header className="node-head">
        <div className="node-head-title">
          <h1>{node.name}</h1>
          {node.country && <CountryBadge code={node.country} />}
          <StatusBadge status={nodeStatus(node)} detail={node.lastSeenAt !== undefined ? `最近上报 ${ago(node.lastSeenAt, now)}` : undefined} />
        </div>
        {node.publicRemark && <p className="node-remark">{node.publicRemark}</p>}
        {node.tags.length > 0 && <ul className="tag-chips">{node.tags.map((tag) => <li key={tag} className="chip">{tag}</li>)}</ul>}
        <dl className="now-grid">
          <Now label="CPU">{m?.cpuPct !== undefined ? percent(m.cpuPct) : <Missing />}</Now>
          <Now label="内存">{m?.memUsed !== undefined && m.memTotal ? percent(ratio(m.memUsed, m.memTotal)) : <Missing />}</Now>
          <Now label="磁盘">{m?.diskUsed !== undefined && m.diskTotal ? percent(ratio(m.diskUsed, m.diskTotal)) : <Missing />}</Now>
          <Now label="网络">{m?.netRxBps !== undefined && m.netTxBps !== undefined ? `↓ ${bytes(m.netRxBps)}/s ↑ ${bytes(m.netTxBps)}/s` : <Missing />}</Now>
          <Now label="运行时长">{m?.uptimeS !== undefined ? duration(m.uptimeS) : <Missing />}</Now>
          <Now label="剩余天数">{daysLeft !== undefined ? <span data-level={expiryLevel(daysLeft)}>{daysLeft < 0 ? `已过期 ${-daysLeft} 天` : `${daysLeft} 天`}</span> : <Missing />}</Now>
        </dl>
      </header>
      <header className="row detail-header">
        <RangePicker history={history} />
      </header>
      <MetricCharts history={history} />
      <ProbeTaskCharts history={history} noProbes={<p className="muted">窗口内没有探测结果。</p>} titleLink={(taskId, title) => <Link to={`/probes/${taskId}`}>{title}</Link>} />
      {/* 系统信息卡：主机信息从未上报时缺失，整张不画。 */}
      {f && (
        <dl className="card facts">
          <dt>系统</dt><dd>{f.os}</dd>
          <dt>架构</dt><dd>{f.arch}</dd>
          <dt>CPU</dt><dd>{f.cpuModel} × {f.cpuCores}</dd>
          <dt>虚拟化</dt><dd>{f.virtualization || "无 / 未知"}</dd>
        </dl>
      )}
    </section>
  );
}
```

`web/src/public/public.css` 追加：

```css
/* 节点页（NodePage.tsx）：状态头 + 2×3 / 1×6 现值格（设计 §3.2、§3.4）。 */
.node-head { display: grid; gap: 8px; margin-bottom: 16px; }
.node-head-title { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; }
.node-head h1 { margin: 0; font-size: 22px; line-height: 28px; overflow-wrap: anywhere; }
.now-grid { margin: 4px 0 0; display: grid; grid-template-columns: repeat(6, minmax(0, 1fr)); gap: 8px; }
.now-cell { padding: 8px 12px; border: 1px solid var(--line); border-radius: var(--radius-card); background: var(--card); min-width: 0; }
.now-cell dt { font-size: 12px; color: var(--muted); }
.now-cell dd { margin: 2px 0 0; font-size: 16px; font-weight: 500; overflow-wrap: anywhere; }
.now-cell [data-level="attention"] { color: var(--status-attention); }
.now-cell [data-level="critical"] { color: var(--status-offline); }
.node-page .grid { grid-template-columns: repeat(auto-fit, minmax(min(360px, 100%), 1fr)); }
.node-page .facts { display: grid; grid-template-columns: minmax(80px, auto) minmax(0, 1fr); gap: 6px 16px; margin-top: 16px; font-size: 13px; }
.node-page .facts dt { color: var(--muted); }
.node-page .facts dd { margin: 0; overflow-wrap: anywhere; }
@media (max-width: 900px) { .now-grid { grid-template-columns: repeat(3, minmax(0, 1fr)); } }
@media (max-width: 480px) { .now-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); } .node-page .grid { grid-template-columns: minmax(0, 1fr); } }
```

- [ ] **Step 4: 跑测试确认绿，再跑全量**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/public/NodePage.test.tsx src/components/History.test.tsx > /tmp/t17.log 2>&1; echo $?; pnpm vitest run > /tmp/t17-all.log 2>&1; echo $?`
Expected: 两个 0。

- [ ] **Step 5: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/public/NodePage.tsx web/src/public/NodePage.test.tsx web/src/public/public.css && git commit -m "feat(web): 公开节点页首屏改为实时状态头与现值格"
```

---

### Task 18: 探测对比页

**Files:**
- Modify: `web/src/lib/probeComparison.ts`（加 `summarizeComparison`、`sortRows`）、`web/src/lib/probeComparison.test.ts`
- Modify: `web/src/components/ProbeComparison.tsx`（页头、大图、表格、脚注）
- Modify: `web/src/components/ProbeComparison.test.tsx`
- Modify: `web/src/styles.css`（追加 `.compare*`；对比组件两端共用，样式放共享文件）

**Interfaces:**
- Consumes: `Chart` 的 `legend={false}`、`hidden`、`onHiddenChange`、`onFocus`（Task 9）、`RangeStatus note`（Task 10）、`kindLabel`（`lib/probes.ts`）。
- Produces:
  ```ts
  export type ComparisonRow = { id: bigint; label: string; mean: number | null; min: number | null; max: number | null; lossPercent: number | null; errors: number; unavailable: boolean };
  export function summarizeComparison(nodeIds: readonly bigint[], names: ReadonlyMap<bigint, string>, chunks: readonly ComparisonChunk[]): ComparisonRow[];
  //   mean = Σ(rttMean × 成功数) ÷ Σ成功数（成功数 = sent − lost − errors），min / max 取所有样本的最小 / 最大；
  //   lossPercent = Σlost ÷ Σsent × 100（Σsent 为 0 时 null）；errors = Σerrors；全部样本都无 RTT 时三者为 null。
  export type SortKey = "label" | "mean" | "min" | "max" | "lossPercent" | "errors";
  export function sortRows(rows: readonly ComparisonRow[], key: SortKey, dir: "asc" | "desc"): ComparisonRow[];  // null 永远排最后
  ```
  DOM：`section.compare` → `header.row.detail-header`（`h1` 含 `span.kind-badge` + 目标；`span` 「N 个节点执行此探测」；RangeButtons；RangeStatus）；一张 `Chart`（`legend={false}`）；`table.compare-table`：表头按钮可排序（`aria-sort`），每行：`checkbox`（名为「显示 <节点>」）、节点、均值、最小、最大、丢包率（>1% `data-level="attention"`）、错误数；`tr[data-focused]` 对应悬停高亮；表下 `p.muted` 写「丢包率 = 超时数 ÷ 发送数；错误不计入丢包。」。

- [ ] **Step 1: 写测试（红）**

在 `web/src/lib/probeComparison.test.ts` 追加：

```ts
import { sortRows, summarizeComparison } from "./probeComparison";

const chunk = (series: { nodeId: bigint; samples: { ts: bigint; sent: number; lost: number; errors: number; rttMeanUs?: number; rttMinUs?: number; rttMaxUs?: number }[] }[], unavailableNodeIds: bigint[] = []) =>
  ({ stepS: 60, series, unavailableNodeIds });

it("按节点汇总：均值按成功数加权，最小 / 最大取极值，丢包率 = Σlost ÷ Σsent，错误累计；不可见节点标记", () => {
  const rows = summarizeComparison([1n, 2n, 3n], new Map([[1n, "a"], [2n, "b"]]), [chunk([
    { nodeId: 1n, samples: [
      { ts: 60n, sent: 4, lost: 1, errors: 1, rttMeanUs: 10_000, rttMinUs: 8_000, rttMaxUs: 12_000 },   // 成功 2
      { ts: 120n, sent: 4, lost: 0, errors: 0, rttMeanUs: 40_000, rttMinUs: 5_000, rttMaxUs: 90_000 },  // 成功 4
    ] },
    { nodeId: 2n, samples: [{ ts: 60n, sent: 2, lost: 2, errors: 0 }] },
  ], [3n])]);
  expect(rows).toEqual([
    { id: 1n, label: "a", mean: 30, min: 5, max: 90, lossPercent: 12.5, errors: 1, unavailable: false },
    // 全部超时：没有 RTT，丢包 100%。
    { id: 2n, label: "b", mean: null, min: null, max: null, lossPercent: 100, errors: 0, unavailable: false },
    { id: 3n, label: "#3", mean: null, min: null, max: null, lossPercent: null, errors: 0, unavailable: true },
  ]);
});

it("排序：数值列按方向，null 永远最后；标签按本地比较", () => {
  const rows = [
    { id: 1n, label: "b", mean: 30, min: 5, max: 90, lossPercent: 12.5, errors: 1, unavailable: false },
    { id: 2n, label: "a", mean: null, min: null, max: null, lossPercent: 100, errors: 0, unavailable: false },
    { id: 3n, label: "c", mean: 10, min: 1, max: 20, lossPercent: 0, errors: 3, unavailable: false },
  ];
  expect(sortRows(rows, "mean", "asc").map((r) => r.id)).toEqual([3n, 1n, 2n]);
  expect(sortRows(rows, "mean", "desc").map((r) => r.id)).toEqual([1n, 3n, 2n]);
  expect(sortRows(rows, "lossPercent", "desc").map((r) => r.id)).toEqual([2n, 1n, 3n]);
  expect(sortRows(rows, "label", "asc").map((r) => r.label)).toEqual(["a", "b", "c"]);
  expect(rows.map((r) => r.id)).toEqual([1n, 2n, 3n]);
});
```

在 `web/src/components/ProbeComparison.test.tsx`：

1. Chart 替身签名加 `hidden?: ReadonlySet<number>; legend?: boolean; onFocus?: (i: number | null) => void`，输出 `data-hidden={JSON.stringify([...(hidden ?? [])])}`、`data-legend={String(legend ?? true)}`，并渲染一个 `<button type="button" data-testid="focus-1" onClick={() => onFocus?.(1)}>` 供测试触发悬停。
2. 所有 `chart-percent` 的断言删除（对比页只有 RTT 大图，丢包进表格）；`screen.getByTestId("chart-ms")` 保留。
3. 追加用例（用文件里已有的 `pinClock`、`renderComparison`、`listed`、`chunkOf` 工具）：

```tsx
it("页头是类型徽章 + 目标 + 节点数；大图不带图例；表格每节点一行可排序，显示开关控制图上的线，丢包 >1% 标琥珀，脚注写丢包口径", async () => {
  const { ts } = pinClock();
  renderComparison({
    listProbeComparisonNodes: async () => listed([1n, 4n, 5n], 8),
    queryProbeComparison: async (req) => create(QueryProbeComparisonResponseSchema, { level: "1m", stepS: 60, unavailableNodeIds: [4n], series: [
      { nodeId: 1n, samples: [{ ts: BigInt(ts), sent: 4, lost: 0, errors: 0, rttMeanUs: 20_000, rttMinUs: 10_000, rttMaxUs: 30_000 }] },
      { nodeId: 5n, samples: [{ ts: BigInt(ts), sent: 100, lost: 2, errors: 1, rttMeanUs: 5_000, rttMinUs: 1_000, rttMaxUs: 9_000 }] },
    ] }),
  });
  const heading = await screen.findByRole("heading", { name: "ICMP edge.example" });
  expect(within(heading).getByText("ICMP")).toHaveClass("kind-badge");
  expect(screen.getByText("3 个节点执行此探测")).toBeInTheDocument();
  expect(screen.getByTestId("chart-ms")).toHaveAttribute("data-legend", "false");
  const table = within(screen.getByRole("table"));
  expect(table.getAllByRole("columnheader").map((th) => th.textContent)).toEqual(["显示", "节点", "均值", "最小", "最大", "丢包率", "错误数"]);
  const cells = (name: string) => within(table.getByRole("row", { name: new RegExp(name) })).getAllByRole("cell").map((td) => td.textContent);
  expect(cells("edge")).toEqual(["", "edge", "20.0 ms", "10.0 ms", "30.0 ms", "0.0%", "0"]);
  expect(cells("ok")).toEqual(["", "ok", "5.00 ms", "1.00 ms", "9.00 ms", "2.0%", "1"]);
  expect(within(table.getByRole("row", { name: /ok/ })).getByText("2.0%")).toHaveAttribute("data-level", "attention");
  expect(cells("gone")).toEqual(["", "gone", "–", "–", "–", "–", "0"]);
  expect(table.getByRole("checkbox", { name: "显示 gone" })).toBeDisabled();
  fireEvent.click(table.getByRole("checkbox", { name: "显示 edge" }));
  expect(screen.getByTestId("chart-ms")).toHaveAttribute("data-hidden", "[0]");
  fireEvent.click(table.getByRole("button", { name: "均值" }));
  expect(table.getAllByRole("row").slice(1).map((row) => row.getAttribute("aria-label"))).toEqual(["ok", "edge", "gone"]);
  fireEvent.click(table.getByRole("button", { name: "均值" }));
  expect(table.getAllByRole("row").slice(1).map((row) => row.getAttribute("aria-label"))).toEqual(["edge", "ok", "gone"]);
  fireEvent.click(screen.getByTestId("focus-1"));
  expect(table.getByRole("row", { name: "ok" })).toHaveAttribute("data-focused", "true");
  expect(screen.getByText("丢包率 = 超时数 ÷ 发送数；错误不计入丢包。")).toBeInTheDocument();
});
```

4. 文件里原有的 `names` 夹具 `{ id: 1n, name: "edge" }, { id: 2n, name: "edge" }, { id: 3n, name: "edge" }, { id: 4n, name: "gone" }, { id: 5n, name: "ok" }` 不变；其它用例若断言 `已不可见的节点` 列表或 `该图窗口内没有读数的节点`，改为断言表格里对应行（不可见节点行的 checkbox disabled；没有读数的节点行均值为「–」）。

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/lib/probeComparison.test.ts src/components/ProbeComparison.test.tsx > /tmp/t18-red.log 2>&1; echo $?`
Expected: 1；lib 红在导出不存在，组件红在找不到 `table`。

- [ ] **Step 3: 实现 lib**

`web/src/lib/probeComparison.ts` 末尾追加：

```ts
export type ComparisonRow = {
  id: bigint; label: string;
  mean: number | null; min: number | null; max: number | null;
  lossPercent: number | null; errors: number;
  unavailable: boolean;
};

// 表格一行一个节点（设计 §3.3）。均值按成功探测数加权（sent − lost − errors；hub 只在成功数 > 0 时给 rtt），
// 不按样本数平均——每个桶的样本数不同，简单平均会让样本少的桶权重虚高。丢包率只看超时（lossPercent 同一口径）。
export function summarizeComparison(nodeIds: readonly bigint[], names: ReadonlyMap<bigint, string>, chunks: readonly ComparisonChunk[]): ComparisonRow[] {
  const seriesById = new Map<bigint, readonly ProbeSample[]>();
  const unavailable = new Set<bigint>();
  for (const chunk of chunks) {
    for (const series of chunk.series) if (!seriesById.has(series.nodeId)) seriesById.set(series.nodeId, series.samples);
    for (const id of chunk.unavailableNodeIds) unavailable.add(id);
  }
  const labels = disambiguate(nodeIds.map((id) => labelOf(id, names)), nodeIds);
  return nodeIds.map((id, i) => {
    const samples = seriesById.get(id) ?? [];
    let sent = 0, lost = 0, errors = 0, weighted = 0, ok = 0;
    let min: number | null = null, max: number | null = null;
    for (const s of samples) {
      sent += s.sent;
      lost += s.lost;
      errors += s.errors;
      const succeeded = s.sent - s.lost - s.errors;
      if (s.rttMeanUs !== undefined && succeeded > 0) {
        weighted += (s.rttMeanUs / 1000) * succeeded;
        ok += succeeded;
      }
      if (s.rttMinUs !== undefined) min = min === null ? s.rttMinUs / 1000 : Math.min(min, s.rttMinUs / 1000);
      if (s.rttMaxUs !== undefined) max = max === null ? s.rttMaxUs / 1000 : Math.max(max, s.rttMaxUs / 1000);
    }
    return {
      id, label: labels[i],
      mean: ok > 0 ? weighted / ok : null, min, max,
      lossPercent: sent > 0 ? (lost / sent) * 100 : null, errors,
      unavailable: unavailable.has(id),
    };
  });
}

export type SortKey = "label" | "mean" | "min" | "max" | "lossPercent" | "errors";

// null 永远排最后，与方向无关：「没有读数」不是最小也不是最大。不改动传入数组。
export function sortRows(rows: readonly ComparisonRow[], key: SortKey, dir: "asc" | "desc"): ComparisonRow[] {
  const sign = dir === "asc" ? 1 : -1;
  return [...rows].sort((a, b) => {
    if (key === "label") return sign * a.label.localeCompare(b.label);
    const va = a[key], vb = b[key];
    if (va === null && vb === null) return 0;
    if (va === null) return 1;
    if (vb === null) return -1;
    return sign * (va - vb);
  });
}
```

- [ ] **Step 4: 实现组件**

`web/src/components/ProbeComparison.tsx`：

1. import 加 `formatUnit`（`../lib/format`）、`percent`（`../lib/format`）、`summarizeComparison, sortRows, type ComparisonRow, type SortKey`（`../lib/probeComparison`）。删除 `NameList`、`FOLD_AT`、`ChartBlock`。
2. 组件内 state 加 `const [hidden, setHidden] = useState<ReadonlySet<number>>(() => new Set()); const [focused, setFocused] = useState<number | null>(null); const [sortKey, setSortKey] = useState<SortKey>("label"); const [sortDir, setSortDir] = useState<"asc" | "desc">("asc");`；`rows` 用 `useMemo(() => (shown ? sortRows(summarizeComparison(shown.nodeIds, names, shown.chunks), sortKey, sortDir) : []), [shown, names, sortKey, sortDir])`。换任务时（`holdTask !== taskId` 分支）把 `hidden` 清空。
3. `taskHeading` 改成返回元素：

```tsx
function TaskHeading({ taskId, hold }: { taskId: bigint; hold: Hold | null }) {
  if (!hold || hold.kind === ProbeKind.UNSPECIFIED || hold.target === "") return <h1>任务 #{String(taskId)}</h1>;
  return <h1><span className="kind-badge">{kindLabel(hold.kind)}</span> {hold.target}</h1>;
}
```

4. 渲染部分替换为：

```tsx
  const stale = shown !== null && shown.rangeLabel !== rangeLabel;
  const labelIndex = new Map(view?.rtt.labels.map((label, i) => [label, i]) ?? []);
  const sortButton = (key: SortKey, text: string) => (
    <th aria-sort={sortKey === key ? (sortDir === "asc" ? "ascending" : "descending") : "none"}>
      <button type="button" className="link" onClick={() => { if (sortKey === key) setSortDir(sortDir === "asc" ? "desc" : "asc"); else { setSortKey(key); setSortDir("asc"); } }}>{text}</button>
    </th>
  );
  return (
    <section className="compare" aria-busy={updating}>
      <header className="row detail-header">
        <TaskHeading taskId={taskId} hold={shown} />
        {shown && <span className="muted">{shown.nodeIds.length} 个节点执行此探测</span>}
        <RangeButtons range={range} setRange={setRange} />
        <RangeStatus shown={shown} stale={stale} rangeLabel={rangeLabel} updating={updating} note="均值是时间桶内成功探测的 RTT 均值；表格按整个窗口汇总。" />
      </header>
      {error && (
        <p role="alert" className="error">
          {error} <button type="button" onClick={() => setAttempt((n) => n + 1)}>重试</button>
        </p>
      )}
      {!shown && updating && !error && !notFound && <p className="muted">加载中…</p>}
      {notFound && !updating && <p className="muted">{NO_COMPARISON_NODES}</p>}
      {view?.complete && !notFound && (
        <>
          <div className="card compare-chart">
            {view.rtt.labels.length > 0
              ? <Chart data={view.rtt.data} labels={view.rtt.labels} unit="ms" height={280} legend={false} hidden={hidden} onHiddenChange={setHidden} onFocus={setFocused} />
              : <p className="muted">窗口内没有读数</p>}
          </div>
          <table className="compare-table">
            <thead>
              <tr>
                <th>显示</th>
                {sortButton("label", "节点")}{sortButton("mean", "均值")}{sortButton("min", "最小")}{sortButton("max", "最大")}{sortButton("lossPercent", "丢包率")}{sortButton("errors", "错误数")}
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => {
                // 图上只有至少一个读数的节点有线（assembleComparison）；不可见或没有线的行没有开关可切。
                const line = labelIndex.get(row.label);
                return (
                  <tr key={String(row.id)} aria-label={row.label} data-focused={line !== undefined && focused === line ? "true" : undefined} className={row.unavailable ? "muted" : undefined}>
                    <td><input type="checkbox" aria-label={`显示 ${row.label}`} disabled={line === undefined} checked={line !== undefined && !hidden.has(line)} onChange={() => {
                      if (line === undefined) return;
                      const next = new Set(hidden);
                      if (next.has(line)) next.delete(line); else next.add(line);
                      setHidden(next);
                    }} /></td>
                    <td>{row.label}</td>
                    <td className="num">{row.mean === null ? "–" : formatUnit(row.mean, "ms")}</td>
                    <td className="num">{row.min === null ? "–" : formatUnit(row.min, "ms")}</td>
                    <td className="num">{row.max === null ? "–" : formatUnit(row.max, "ms")}</td>
                    <td className="num">{row.lossPercent === null ? "–" : <span data-level={row.lossPercent > 1 ? "attention" : undefined}>{percent(row.lossPercent)}</span>}</td>
                    <td className="num">{row.errors}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          {/* ProbeSample 的口径：丢包只计超时，error 是本地无法发起。 */}
          <p className="muted">丢包率 = 超时数 ÷ 发送数；错误不计入丢包。</p>
        </>
      )}
    </section>
  );
```

`web/src/styles.css` 追加：

```css
/* 探测对比（components/ProbeComparison.tsx），两端共用。 */
.kind-badge { display: inline-block; padding: 0 6px; height: 22px; line-height: 20px; border: 1px solid var(--line-strong); border-radius: var(--radius-control); font-size: 12px; font-weight: 500; vertical-align: middle; }
.compare-chart { margin-bottom: 12px; }
.compare-table { width: 100%; border-collapse: collapse; font-size: 13px; background: var(--card); border: 1px solid var(--line); border-radius: var(--radius-card); }
.compare-table th { height: 32px; text-align: left; padding: 0 10px; color: var(--muted); font-weight: 500; font-size: 12px; border-bottom: 1px solid var(--line); }
.compare-table th .link { font: inherit; color: inherit; }
.compare-table th[aria-sort="ascending"] .link::after { content: " ↑"; }
.compare-table th[aria-sort="descending"] .link::after { content: " ↓"; }
.compare-table td { height: 36px; padding: 0 10px; border-bottom: 1px solid var(--line); }
.compare-table td.num { text-align: right; }
.compare-table tr[data-focused] td { background: var(--card-hover); }
.compare-table [data-level="attention"] { color: var(--status-attention); }
@media (max-width: 640px) { .compare-table { display: block; overflow-x: auto; } }
```

- [ ] **Step 5: 跑测试确认绿，再跑全量与类型检查**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/lib/probeComparison.test.ts src/components/ProbeComparison.test.tsx > /tmp/t18.log 2>&1; echo $?; pnpm typecheck > /tmp/t18-tsc.log 2>&1; echo $?; pnpm vitest run > /tmp/t18-all.log 2>&1; echo $?`
Expected: 三个 0（管理端 `ProbeTasks` 页的「对比」也用这个组件，`ProbeComparison.test.tsx` 里的管理端用例一并通过）。

- [ ] **Step 6: 缺陷注入**

把 `summarizeComparison` 的加权改成简单平均（`weighted += s.rttMeanUs / 1000; ok += 1`），lib 用例应红在 `mean: 30` 变成 25；改回后绿。

- [ ] **Step 7: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/lib/probeComparison.ts web/src/lib/probeComparison.test.ts web/src/components/ProbeComparison.tsx web/src/components/ProbeComparison.test.tsx web/src/styles.css && git commit -m "feat(web): 探测对比改为 RTT 大图加可排序的节点汇总表"
```

---

### Task 19: 公开页关闭时的说明页

**Files:**
- Modify: `internal/hub/web/web.go:45`（`closedPage`）
- Modify: `internal/hub/web/web_test.go`（`TestPublicGateClosedFollowsTheFallbackRule` 加内容断言）

**Interfaces:**
- Produces: `closedPage` 常量仍是完整 HTML；内含内联鹭鸟 SVG、「公开页已关闭」、一句说明、指向 `Prefix` 的「管理员登录」链接；内联 `<style>` 在 CSP `style-src 'unsafe-inline'` 下允许。

- [ ] **Step 1: 改测试（红）**

在 `web_test.go` 的 `TestPublicGateClosedFollowsTheFallbackRule` 里，`checkClosed` 返回 body 后、循环之前，加：

```go
	body := checkClosed(t, "/nodes/7", false)
	// 分享出去的链接要能看出是站点关了而不是链接失效（设计 §3.5）：说明页带品牌图形、原因与面板入口。
	for _, want := range []string{"<svg", "公开页已关闭", "站长暂时关闭了公开页", `href="` + Prefix + `"`, "管理员登录", `<meta name="viewport"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("closed page lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "http") || strings.Contains(body, "@import") {
		t.Fatalf("closed page must not reference remote resources:\n%s", body)
	}
```

（`strings` 已 import 则不重复。）

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe && go test -count=1 ./internal/hub/web -run TestPublicGateClosedFollowsTheFallbackRule > /tmp/t19-red.log 2>&1; echo $?; grep -m1 'lacks' /tmp/t19-red.log`
Expected: 1；`closed page lacks "<svg"`。

- [ ] **Step 3: 实现**

`internal/hub/web/web.go` 把 `const closedPage = …` 替换为：

```go
// closedPage 是总闸关闭时 assets/ 之外的公开路径得到的页面（设计 §3.5）：居中品牌图形、「公开页已关闭」、一句说明、面板入口。
// 自包含：内联 SVG 与样式，不引用任何资源——关闭时 assets/ 也是 404。面板与本页挂在同一个 mux（cmd/hub/serve.go），
// 入口写 Prefix 即可。颜色跟随系统明暗，与 web/src/assets/heron.svg 同一套。
const closedPage = `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>公开页已关闭</title>
<style>
:root{color-scheme:light dark;--bg:light-dark(#f6f7f9,#0b0d12);--fg:light-dark(#111827,#e6e8ee);--muted:light-dark(#4b5563,#9aa3b5);--accent:light-dark(#2563eb,#5b9bf8);--bird:light-dark(#214e57,#a9d4d6)}
body{margin:0;min-height:100dvh;display:grid;place-items:center;font:14px/1.5 Inter,system-ui,-apple-system,"PingFang SC","Noto Sans SC",sans-serif;color:var(--fg);background:var(--bg)}
main{text-align:center;padding:24px}svg{width:64px;height:64px}h1{margin:12px 0 4px;font-size:22px;font-weight:600}p{margin:0 0 16px;color:var(--muted)}a{color:var(--accent);text-decoration:none}
</style>
<main>
<svg viewBox="0 0 64 64" fill="none" aria-hidden="true"><path fill="var(--bird)" d="M7 43c4-10 11-17 21-19l5-1c4-1 5-3 3-6l-3-4c-3-5 0-10 5-10 4 0 6 2 7 5l12 4-14 2c-2-1-3-2-3-4-2 0-3 1-2 3l4 5c4 6 1 11-5 14-2 10-10 15-20 13L7 43Z"/><path d="M12 40c5-7 11-10 19-11-3 7-9 11-19 11Z" fill="#fff" fill-opacity=".24"/><path stroke="var(--bird)" d="m24 45-2 13h-7m17-14 4 8-5 6h7" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"/><circle cx="40" cy="7.5" r="1.5" fill="#e5ad64"/></svg>
<h1>公开页已关闭</h1>
<p>站长暂时关闭了公开页，这个链接本身没有失效。</p>
<a href="` + Prefix + `">管理员登录</a>
</main>`
```

- [ ] **Step 4: 跑测试确认绿**

Run: `cd /Users/xjetry/work/vibe/probe && go test -count=1 ./internal/hub/web/... > /tmp/t19.log 2>&1; echo $?`
Expected: 0。

- [ ] **Step 5: 真机冒烟**

Run: `cd /Users/xjetry/work/vibe/probe && go run ./cmd/hub serve --help > /tmp/t19-help.log 2>&1; echo $?` 确认能跑；随后用 `web/e2e/server.mjs` 的方式起一个临时 hub（Task 20 的 e2e 会把 `publicEnabled:false` 场景包进去），本步只要求 `go vet ./internal/hub/web/` 为 0。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add internal/hub/web/web.go internal/hub/web/web_test.go && git commit -m "feat(hub): 公开页关闭时的说明页带品牌图形与面板入口"
```

---

### Task 20: 从用户入口验收（e2e）

**Files:**
- Create: `web/e2e/public-overview.spec.ts`、`web/e2e/public-node.spec.ts`
- Modify: `web/e2e/region-filter.spec.ts`
- Delete: `web/e2e/public-cards.spec.ts`

**Interfaces:**
- Consumes: Task 12–19 的 DOM（顶栏、汇总、筛选行、墙、卡片、节点页 `data-x-ticks`）；`e2e/server.mjs` 起真实 hub（`make hub-binary` 先构建）。

- [ ] **Step 1: 写 e2e**

`web/e2e/public-overview.spec.ts`：

```ts
import { expect, test, type Page } from "@playwright/test";

// 设计 §6 的公开页禁止字段：任何一个出现在页面文字里都算失败。
const FORBIDDEN = ["可用率", "在线率", "SLA", "宕机", "不可达", "主机名", "内核", "agent 版本", "IPv4", "IPv6", "事件", "告警", "刷新"];

// e2e 的 hub 是每次新建的库，公开页总闸默认关闭（关闸时连 SPA 的静态资源都是 404），先登录打开；RPC 再由 route 拦截。
async function setPublicEnabled(page: Page, enabled: boolean) {
  await page.goto("/admin/login");
  await page.evaluate(async (enabled) => {
    const call = async (method: string, body: unknown) => {
      const response = await fetch("/heron.v1.AdminService/" + method, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      if (!response.ok) throw new Error(method + ": " + await response.text());
    };
    await call("Login", { password: "local-browser-test-password" });
    await call("UpdateSettings", { settings: { publicEnabled: enabled } });
  }, enabled);
}

test("公开总览：状态墙、详情、卡片、手机列表与数据边界", async ({ page }, testInfo) => {
  await setPublicEnabled(page, true);
  const now = Math.floor(Date.now() / 1000);
  const nodes = [
    { id: "1", name: "tokyo-core", country: "JP", online: true, tags: ["机房"], lastSeenAt: String(now - 2), facts: { os: "Debian 13", arch: "amd64", virtualization: "kvm", cpuModel: "EPYC", cpuCores: 4 }, metrics: { cpuPct: 72, memUsed: String(3 * 1024 ** 3), memTotal: String(4 * 1024 ** 3), diskUsed: String(8 * 1024 ** 3), diskTotal: String(32 * 1024 ** 3), uptimeS: "720000", load1: 0.3, load5: 0.2, load15: 0.1, netRxBps: String(128 * 1024), netTxBps: String(32 * 1024), tcpConns: 20, udpConns: 2, procs: 120 }, traffic: { periodRx: String(12 * 1024 ** 3), periodTx: String(8 * 1024 ** 3) }, billing: { price: "12", currency: "USD", billingCycle: "BILLING_CYCLE_MONTHLY", expiresOn: "2027-10-01", daysLeft: 25 } },
    { id: "2", name: "tokyo-home", country: "JP", online: false, tags: ["家宽"], lastSeenAt: String(now - 7200), metrics: { cpuPct: 5 } },
    { id: "3", name: "hk-edge", country: "HK", online: true, tags: ["机房"], lastSeenAt: String(now - 1), maintenance: true, metrics: { cpuPct: 10, memUsed: String(1024 ** 3), memTotal: String(4 * 1024 ** 3) } },
    { id: "4", name: "香港家宽-超长节点名称用于验证窄屏布局与完整可访问名称", country: "HK", online: true, tags: ["家宽"], lastSeenAt: String(now - 1), metrics: { cpuPct: 95, memUsed: String(3.9 * 1024 ** 3), memTotal: String(4 * 1024 ** 3) } },
    { id: "5", name: "等待首次接入", country: "", online: false, tags: [] },
  ];
  await page.route("**/heron.v1.PublicService/GetSite**", (route) => route.fulfill({ json: { title: "Heron · 基础设施", theme: "dark", adminPath: "/admin/" } }));
  await page.route("**/heron.v1.PublicService/GetSnapshot**", (route) => route.fulfill({ json: { now: String(now), nodes, tags: ["家宽", "机房"] } }));
  await page.route("**/heron.v1.PublicService/QueryMetrics**", (route) => route.fulfill({ json: { level: "1m", stepS: 60, ts: [String(now - 120), String(now - 60)], series: [{ name: "rx_bytes", unit: "bytes", samples: [{ n: 1, sum: "6000" }, { n: 1, sum: "3000" }] }, { name: "tx_bytes", unit: "bytes", samples: [{ n: 1, sum: "600" }, { n: 1, sum: "300" }] }] } }));
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 1440, height: 960 });
  await page.goto("/");

  // 顶栏只有标题、实时说明、明暗切换、登录；没有导航。
  const header = page.locator("header.public-header");
  await expect(header.getByRole("link")).toHaveText(["Heron · 基础设施", "登录"]);
  await expect(header.getByText("实时 · 每 2 秒")).toBeVisible();
  await expect(page.getByRole("navigation")).toHaveCount(0);
  await expect(page.locator("footer.site-footer")).toHaveCount(0);

  // 汇总与分组：维护中不算在线；组按在线数降序，未知最后。
  await expect(page.getByText("2 / 5 在线")).toBeVisible();
  // 日本与香港都是 1 / 2：同在线数、同总数时按代码排，HK 在 JP 前。
  await expect(page.locator(".wall-group > summary")).toHaveText(["🇭🇰 香港 · 1 / 2 在线", "🇯🇵 日本 · 1 / 2 在线", "未知 · 0 / 1 在线"]);
  await expect(page.locator(".tile[data-status='offline']").getByText("离线 · 2 小时前")).toBeVisible();

  // 详情面板默认选中第一个节点；点另一个方块只切换，不导航。
  const panel = page.getByRole("complementary", { name: "节点详情" });
  await expect(panel.getByRole("heading", { name: "tokyo-core" })).toBeVisible();
  await expect(panel.getByRole("img", { name: "最近 1 小时网络速率" })).toBeVisible();
  await page.getByRole("link", { name: "hk-edge" }).click();
  await expect(page).toHaveURL(/\/$/);
  await expect(panel.getByRole("heading", { name: "hk-edge" })).toBeVisible();
  await expect(panel.getByText("维护中 · 最近上报 刚刚")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(1440);
  await page.screenshot({ path: testInfo.outputPath("wall-dark.png"), fullPage: true });

  // 筛选：地区下拉 + 标签下拉 + 只看在线。
  await page.getByRole("group", { name: "地区" }).getByRole("button", { name: /^地区/ }).click();
  await page.getByRole("checkbox", { name: "🇯🇵 日本" }).check();
  await page.keyboard.press("Escape");
  await expect(page.getByText("1 / 2 在线")).toBeVisible();
  await page.getByRole("button", { name: "只看在线" }).click();
  await expect(page.locator(".tile")).toHaveCount(1);
  await page.getByRole("button", { name: "移除 🇯🇵 日本" }).click();
  await page.getByRole("button", { name: "只看在线" }).click();

  // 卡片视图：只有在线与维护中出卡片，离线折叠；卡片有费用与到期。
  await page.getByRole("group", { name: "视图" }).getByRole("button", { name: "卡片" }).click();
  await expect(page.getByRole("article")).toHaveCount(3);
  const card = page.getByRole("article", { name: "tokyo-core" });
  await expect(card.getByText("US$12 / 月")).toBeVisible();
  await expect(card.getByText("剩 25 天")).toHaveAttribute("data-level", "attention");
  const folded = page.locator("details.folded-nodes");
  await expect(folded.locator("summary")).toHaveText("离线与从未上报 · 2");
  await folded.locator("summary").click();
  await expect(folded.getByRole("row")).toHaveCount(3);
  await page.screenshot({ path: testInfo.outputPath("cards-dark.png"), fullPage: true });

  // 浅色：访客切换压过站点的 dark。
  await page.getByRole("button", { name: "明暗切换" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
  await page.screenshot({ path: testInfo.outputPath("cards-light.png"), fullPage: true });

  // 数据边界：页面文字里没有禁止字段。
  const text = await page.evaluate(() => document.body.innerText);
  for (const word of FORBIDDEN) expect(text, word).not.toContain(word);

  // 手机：墙是单列列表，点行直接进节点页，没有横向溢出。
  await page.getByRole("group", { name: "视图" }).getByRole("button", { name: "状态墙" }).click();
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(panel).toHaveCount(0);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390);
  await page.screenshot({ path: testInfo.outputPath("wall-mobile.png"), fullPage: true });
  await page.getByRole("link", { name: nodes[3].name }).click();
  await expect(page).toHaveURL(/\/nodes\/4$/);
});

test("公开页关闭时分享链接得到说明页而不是 404", async ({ page }) => {
  await setPublicEnabled(page, false);
  const response = await page.goto("/nodes/7");
  expect(response?.status()).toBe(200);
  await expect(page.getByRole("heading", { name: "公开页已关闭" })).toBeVisible();
  await expect(page.getByRole("link", { name: "管理员登录" })).toHaveAttribute("href", "/admin/");
});
```

`web/e2e/public-node.spec.ts`：

```ts
import { expect, test } from "@playwright/test";

// 横轴刻度由共享 Chart 按容器宽度决定（设计 §5）：相邻刻度 ≥80px。刻度文字画在 canvas 上，Chart 把刻度数写在
// data-x-ticks；这里从用户入口在两个宽度下核对，并确认画布不超出容器。
test("节点页：现值头、三列图表与横轴刻度随宽度变化", async ({ page }, testInfo) => {
  // 总闸默认关闭，先打开（同 public-overview.spec）。
  await page.goto("/admin/login");
  await page.evaluate(async () => {
    const call = async (method: string, body: unknown) => { const r = await fetch("/heron.v1.AdminService/" + method, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }); if (!r.ok) throw new Error(method + ": " + await r.text()); };
    await call("Login", { password: "local-browser-test-password" });
    await call("UpdateSettings", { settings: { publicEnabled: true } });
  });
  const now = Math.floor(Date.now() / 1000);
  const from = now - 86_400;
  const ts = Array.from({ length: 24 }, (_, i) => String(from - (from % 3600) + i * 3600));
  await page.route("**/heron.v1.PublicService/GetSite**", (route) => route.fulfill({ json: { title: "状态" } }));
  await page.route("**/heron.v1.PublicService/GetSnapshot**", (route) => route.fulfill({ json: { now: String(now), nodes: [{ id: "1", name: "tokyo-core", online: true, lastSeenAt: String(now - 2), facts: { os: "Debian 13", arch: "amd64", cpuModel: "EPYC", cpuCores: 4 }, metrics: { cpuPct: 42, memUsed: String(1024 ** 3), memTotal: String(4 * 1024 ** 3), uptimeS: "7200" } }], tags: [] } }));
  await page.route("**/heron.v1.PublicService/QueryMetrics**", (route) => route.fulfill({ json: { level: "1h", stepS: 3600, ts, series: [{ name: "cpu", unit: "percent", samples: ts.map((_, i) => ({ n: 60, mean: 20 + i, max: 40 + i })) }] } }));
  await page.route("**/heron.v1.PublicService/QueryProbes**", (route) => route.fulfill({ json: { level: "1h", stepS: 3600, series: [{ taskId: "3", kind: "PROBE_KIND_ICMP", target: "1.1.1.1", samples: ts.map((t) => ({ ts: t, sent: 60, lost: 1, errors: 0, rttMeanUs: 20000, rttMinUs: 10000, rttMaxUs: 50000 })) }] } }));
  await page.setViewportSize({ width: 1440, height: 960 });
  await page.goto("/nodes/1");
  await expect(page.getByRole("group", { name: "CPU" })).toContainText("42%");
  const cpu = page.locator(".card", { has: page.getByRole("heading", { name: "CPU", exact: true }) });
  await expect(cpu.locator("[data-x-ticks]")).toHaveAttribute("data-x-ticks", /^\d+$/);
  const wideTicks = Number(await cpu.locator("[data-x-ticks]").getAttribute("data-x-ticks"));
  const wideWidth = await cpu.locator("canvas").evaluate((c) => c.getBoundingClientRect().width);
  expect(wideTicks).toBeGreaterThanOrEqual(4);
  expect(wideWidth / wideTicks).toBeGreaterThanOrEqual(80);
  // 图例显示最新值而不是破折号。
  await expect(cpu.getByRole("list", { name: "图例" }).getByRole("button", { name: /CPU 均值/ })).toContainText("43%");
  await expect(page.getByRole("heading", { name: "ICMP 1.1.1.1" }).getByRole("link")).toHaveAttribute("href", "/probes/3");
  await page.screenshot({ path: testInfo.outputPath("node-desktop.png"), fullPage: true });

  await page.setViewportSize({ width: 390, height: 844 });
  await expect.poll(async () => Number(await cpu.locator("[data-x-ticks]").getAttribute("data-x-ticks"))).toBeLessThanOrEqual(5);
  const mobileTicks = Number(await cpu.locator("[data-x-ticks]").getAttribute("data-x-ticks"));
  const mobileWidth = await cpu.locator("canvas").evaluate((c) => c.getBoundingClientRect().width);
  expect(mobileTicks).toBeGreaterThanOrEqual(3);
  expect(mobileWidth / mobileTicks).toBeGreaterThanOrEqual(80);
  expect(mobileWidth).toBeLessThanOrEqual(390);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390);
  await page.screenshot({ path: testInfo.outputPath("node-mobile.png"), fullPage: true });
});
```

`web/e2e/region-filter.spec.ts` 改交互部分（`const regions = …` 到 `finally` 之前）为：

```ts
    await page.goto('/');
    const tiles = page.locator('.tile');
    await expect(tiles).toHaveCount(6);
    const regions = page.getByRole('group', { name: '地区' });
    await regions.getByRole('button', { name: /^地区/ }).click();
    await expect(regions.getByRole('checkbox')).toHaveText(['🇭🇰 香港', '🇯🇵 日本', '🇺🇸 美国', '未知']);
    await regions.getByRole('checkbox', { name: '🇭🇰 香港' }).check();
    await regions.getByRole('checkbox', { name: '🇯🇵 日本' }).check();
    await page.keyboard.press('Escape');
    const tags = page.getByRole('group', { name: '标签' });
    await tags.getByRole('button', { name: /^标签/ }).click();
    await tags.getByRole('checkbox', { name: '家宽' }).check();
    await page.keyboard.press('Escape');
    await expect(tiles).toHaveCount(2);
    await expect(tiles.getByRole('link')).toHaveText([`香港家宽-${browserName}`, `日本家宽-${browserName}`]);
    await page.setViewportSize({ width: 1440, height: 960 });
    await page.screenshot({ path: testInfo.outputPath('regions-desktop.png'), fullPage: true });
    await page.setViewportSize({ width: 375, height: 812 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(375);
    await page.screenshot({ path: testInfo.outputPath('regions-mobile.png'), fullPage: true });
    await page.getByRole('button', { name: '移除 🇭🇰 香港' }).click();
    await page.getByRole('button', { name: '移除 🇯🇵 日本' }).click();
    await expect(tiles).toHaveCount(3);
    await regions.getByRole('button', { name: /^地区/ }).click();
    await regions.getByRole('checkbox', { name: '未知' }).check();
    await page.keyboard.press('Escape');
    await expect(tiles).toHaveCount(1);
    await expect(tiles.getByRole('link')).toHaveText([`待探测-${browserName}`]);
```

（从未上报的「待探测」节点标签是「家宽」，仍被标签筛选命中。）

- [ ] **Step 2: 构建并跑 e2e**

Run: `cd /Users/xjetry/work/vibe/probe && make hub-binary > /tmp/t20-build.log 2>&1; echo $?`
Expected: 0（先 `make web` 再 `go build`）。

Run: `cd /Users/xjetry/work/vibe/probe && make web-e2e > /tmp/t20-e2e.log 2>&1; echo $?; grep -E 'passed|failed' /tmp/t20-e2e.log | tail -3`
Expected: 0；三个浏览器项目全过。总闸状态由每个用例自己设置（`setPublicEnabled`），不依赖 hub 默认值。

- [ ] **Step 3: 看截图**

打开 `web/test-results/**/wall-dark.png`、`cards-light.png`、`wall-mobile.png`、`node-desktop.png`、`node-mobile.png`，逐张对照设计 §3：顶栏 48px、方块约 128×56、详情面板在右 1/3、手机单列、横轴刻度不重叠。发现与规范不符的视觉问题记入计划末尾「验收记录」，属于本计划范围的当场修。

- [ ] **Step 4: 缺陷注入**

把 `Chart.tsx` 的 `space: X_TICK_SPACE` 临时改成 `space: 30`，只跑 `pnpm --dir web exec playwright test e2e/public-node.spec.ts --project=chromium`，应红在 `mobileWidth / mobileTicks ≥ 80`；改回后绿。

- [ ] **Step 5: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/e2e/public-overview.spec.ts web/e2e/public-node.spec.ts web/e2e/region-filter.spec.ts && git rm -q web/e2e/public-cards.spec.ts && git commit -m "test(web): 公开页从用户入口验收状态墙、卡片、节点页刻度与数据边界"
```

---

### Task 21: 全量验证与收尾

**Files:**
- Modify: `docs/superpowers/plans/2026-10-07-web-redesign-public.md`（末尾追加「验收记录」）
- Modify: `web/src/public/public.css`（删除不再被任何 tsx 引用的类）

- [ ] **Step 1: 死样式清理**

Run: `cd /Users/xjetry/work/vibe/probe/web && for c in $(grep -o '^\.[a-z][a-z0-9-]*' src/public/public.css | sort -u | tr -d .); do grep -rq -- "$c" src --include='*.tsx' || echo "unused: $c"; done > /tmp/t21-css.log 2>&1; cat /tmp/t21-css.log`
Expected: 列出未被引用的类（`wall-layout`、`tile-*` 这类由父选择器引用的会误报，逐个核对后删除真正无用的）。

- [ ] **Step 2: 全量**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm typecheck > /tmp/t21-tsc.log 2>&1; echo $?; pnpm vitest run > /tmp/t21-unit.log 2>&1; echo $?; pnpm build > /tmp/t21-build.log 2>&1; echo $?`
Expected: 三个 0。`importScan.test.ts` 在 unit 里：公开包不含 `admin_pb.ts`。

Run: `cd /Users/xjetry/work/vibe/probe && go test -count=1 ./internal/hub/... > /tmp/t21-go.log 2>&1; echo $?; make web-e2e > /tmp/t21-e2e.log 2>&1; echo $?`
Expected: 两个 0。

- [ ] **Step 3: 管理面板回归冒烟**

管理面板本计划不改版，但共享的 `styles.css`、`Bar`、`Chart`、`History`、`ProbeComparison` 都变了。Run: `cd /Users/xjetry/work/vibe/probe && make hub-binary > /tmp/t21-hub.log 2>&1; echo $?`，起 `node web/e2e/server.mjs`，用浏览器登录 `https://localhost:18988/admin/`，看总览、节点详情（图表、ⓘ）、探测任务的「对比」三页能正常渲染、没有控制台报错。记录看到的内容。

- [ ] **Step 4: 写验收记录并提交**

在本计划末尾追加「## 验收记录」：日期、命令与退出码、截图路径、真机看到的内容、遗留问题（若有）。

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/public/public.css docs/superpowers/plans/2026-10-07-web-redesign-public.md && git commit -m "chore(web): 清理公开页改版后无引用的样式并记录验收"
```

---

## 自审记录

- 规范覆盖：§2（Task 1–3、5）、§3.1（Task 11–16）、§3.2（Task 10、17）、§3.3（Task 18）、§3.4（Task 5 底部弹出、Task 15 手机列表、Task 17 现值格 2×3）、§3.5（Task 19）、§5（Task 7–10）、§6（Task 20 禁止字段断言；Task 11 四态）、§8 验收（Task 20–21）。§4 与抽屉 / ⋯ 菜单不在本计划。
- 接口一致：`Bar({ value, label, thin })` 在 Task 3 定义、Task 15 / 16 使用；`Chart` 的 `soft/bands/legend/hidden/onHiddenChange/onFocus` 在 Task 9 定义、Task 10 / 18 使用；`RangeStatus note` 在 Task 10 定义、Task 18 使用；`PublicFilters/regionOptions/groupByRegion/summarize/tileLevel/sortCards` 在 Task 11 定义、Task 13–16 使用；`applySite(site, choice)` 在 Task 4 定义、Task 12 使用；`MetricCharts/ProbeTaskCharts` 在 Task 10 定义、Task 17 使用。
- Review Focus 五条各有归属：online+maintenance（Task 2、13）、选中节点消失（Task 15）、localStorage 抛错（Task 4）、全部超时的行（Task 18）、零节点 / 只有未知（Task 11、13）。
