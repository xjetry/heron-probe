# 管理面板改版（二）：探测任务、告警、系统页与登录 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把设计 §4.4（探测任务）、§4.5（告警）、§4.6（系统）与登录页改到位：列表页统一为 页头 → 筛选行 → 表格，新建与编辑进右侧抽屉，行操作收进 ⋯，分配 / 作用域改成三张卡片式单选加可搜索多选。

**Architecture:** 复用《管理面板改版（一）》交付的 `Drawer`、`RowMenu`、`PageHeader`、`MultiSelect` 与 `EventFeed`，本计划不再新建基础件；唯一的新共享部件是 `NodeAssignment`（替代 `NodeSelector`），探测任务、告警规则、维护静默三处同一份。每个页面的数据流（查询、mutation、`useLatestError`、`useOrder`、「保存后回读完成才关闭编辑态」）保持原样，改的是壳：表单从「行内卡片」搬进抽屉，行按钮收进菜单，表头按设计重排。既有单测只改期望不改语义；改动面按消费面清单画，不按被修函数画。

**Tech Stack:** React 19、react-router 8、connect-query 2、vitest 5（`globals` 关）、Testing Library、Playwright 1.63。

**Spec:** `docs/superpowers/specs/2026-10-07-web-redesign-design.md` §4.4–§4.7、§6；设计稿同目录 `web-redesign-stitch/`。

**前置：** 本计划建立在 `docs/superpowers/plans/2026-10-08-web-redesign-admin-shell-nodes.md` 的 Task 1–5 与 Task 11 之上（`Drawer`、`RowMenu`、`PageHeader`、`QuickSearch`、`EventFeed`）。并行执行时，本计划的任务从那边 Task 5 的提交点起分支；Task 4（事件页）还要等那边 Task 11。

**不在本计划里：** 节点、总览、节点详情（计划一）；新接口、新字段（§1 非目标）；通知渠道的「登录通知」表单与外观 / 备份 / 心跳 / 国家查询这些设置表单的字段与语义（只换页头与卡片样式，字段不动）。

## Global Constraints

- 不新增接口、不改协议口径：事件页的节点 / 规则 / 变化 / 时间筛选在已加载的页上做（`ListAlertEventsRequest` 只有 `nodeId / beforeId / limit`）；探测分配、告警作用域、静默作用域的三种形状（`allNodes` / `selectorTags` / `nodeIds`）与 hub 的裁决（空显式作用域由 hub 拒绝；`all_nodes` 与 `selector_tags` 互斥）不变。
- 抽屉与弹窗都是原生 `<dialog>`（`showModal`），e2e 与单测按 `getByRole("dialog")` 找；抽屉关闭按钮名「关闭抽屉」。同一时刻一个页面只有一个抽屉：保存在途时抽屉不可关闭、列表上其它编辑入口禁用。这替代了旧的「A 行保存挂起时 B 行保存禁用」（旧实现允许多行同时展开行内表单）。
- 行操作收进 ⋯：菜单项可访问名沿用旧按钮的写法 `动词 对象名（#id）`（`withId`）；危险项两段式，首击只武装成「确认删除 …」，再击才执行；菜单关闭即撤销武装；需要附注的删除（渠道）把附注放在武装项下方的 `small` 里。
- 列表页统一为 `PageHeader`（`h1` + 主按钮）→ `.filter-row[role=group][aria-label=筛选]`（有筛选的页）→ `.table-scroll > table.nodes`；空态文案不变。手机（≤900px）表格按 `td[data-label]` 降级为卡片，用计划一的同一套 CSS。
- 轮询不覆盖草稿：抽屉持有打开时的快照，列表每 10s / 3s 的刷新不重置表单。既有不变式（「探测任务刷新失败保留同一编辑表单与草稿」「编辑中的草稿不被周期刷新覆盖」）改成抽屉后必须仍成立。
- 分配 / 作用域的口径句「只保存本次选中的节点，之后标签变化不会改变分配」逐字保留在抽屉里（设计 §4.4）。
- `web/src/public/importScan.test.ts` 钉住公开包不得带入 `admin_pb.ts`：本计划新建的 `NodeAssignment`、各 `*Drawer` 都 import 管理服务类型，公开页不得引用。
- `web/src/test/setup.ts` 关掉了 vitest 的 `globals`，每个测试文件自己 import；管理端测试用 `renderWithAdmin(impl, routes, path)`（`web/src/test/harness.tsx`），返回 `{ router, queryClient }`。
- 代码注释与 commit message 不写过程信息（任务编号、方案代号、轮次），只写 WHY 与不变式；注释里的因果论断要能推演证伪。
- 每条新断言做一次缺陷注入（构造它本该抓住的缺陷，确认红在正确的原因上）。判成败的命令不接管道：`cmd > log 2>&1; echo $?`。
- Bash 每条命令用 `cd /Users/xjetry/work/vibe/probe/web && …`。vitest 单测文件：`pnpm vitest run src/path/file.test.tsx`。e2e：`cd /Users/xjetry/work/vibe/probe && make web-e2e`（端口 18987 / 18988，一次只能跑一份）。

## Review Focus

- 探测任务 / 告警规则 / 静默的编辑抽屉打开时，列表轮询把这条记录删掉（另一处删了它）：抽屉不自动关闭、草稿还在，保存时由 hub 以 NotFound 拒绝并把原文显示在抽屉内——Task 2、3、5。
- 「指定节点」里按标签快选加进来的节点，之后在列表里被删除：已选列表里那一项显示为「#id（已不存在）」且可移除，提交时被 `liveIds` 剔除；不能让一个看不见的 id 让 hub 拒绝整次保存——Task 1。
- 事件页筛选只作用于已加载的页：筛完为空时文案是「已加载的 N 条里没有匹配的事件」并保留「加载更早」按钮，不是「没有告警事件。」——Task 4。
- 告警规则行上的启用开关：保存在途时开关禁用且显示在途态，失败时开关弹回并显示原文；保存成功后不打开抽屉——Task 3。
- 注册窗口：开窗抽屉提交成功后抽屉关闭，key 与安装命令出现在页面上（不在抽屉里）；轮询发现窗口失效时 key 与命令撤下——Task 9。

---

## 文件地图

| 文件 | 职责 |
|---|---|
| `web/src/components/RowMenu.tsx` | 加 `note?: string`：武装后的危险项下方的一行附注 |
| `web/src/components/NodeAssignment.tsx` | 分配 / 作用域：三张卡片式单选（全部节点 / 动态标签选择器 / 指定节点）、按标签快选、可搜索的节点勾选列表、已选列表与计数 |
| `web/src/components/NodeSelector.tsx` | 删除（三处消费者全部改用 `NodeAssignment`） |
| `web/src/components/Picks.tsx` | 保留给通知渠道、登录通知与 API token 的授权节点 |
| `web/src/pages/ProbeTasks.tsx` | 页头、表格（分配摘要、⋯）、`ProbeTaskDrawer`（类型分段、目标、间隔、超时、证书指纹、分配） |
| `web/src/pages/AlertRules.tsx` | 页头、筛选行（只看触发中，`?state=firing`）、表格（启用开关、⋯）、`AlertRuleDrawer` |
| `web/src/pages/AlertEvents.tsx` | 页头、筛选行（节点 / 规则 / 变化 / 时间）、`EventFeed` |
| `web/src/components/EventFeed.tsx` | 列改为 时间、节点、规则、变化、观测值、投递；加 `ruleName` 与 `visible` 两个可选 prop |
| `web/src/pages/Silences.tsx` | 页头、表格、`SilenceDrawer` |
| `web/src/pages/Channels.tsx` | 页头、登录通知卡（不动）、表格（⋯：编辑 / 发送测试 / 删除）、`ChannelDrawer`（按 Telegram / Webhook 切字段） |
| `web/src/pages/Updates.tsx` | 页头、hub 卡（当前 / 最新 / 绑定的 agent 版本）、agent 表（版本 + 落后徽章、来源、状态、操作） |
| `web/src/pages/Sessions.tsx` | 页头「安全」、三张卡（密码 / TOTP / Passkey 的状态与去向）、会话表（⋯：撤销） |
| `web/src/pages/Security.tsx` | 只换页头与卡片类名，字段与流程不动 |
| `web/src/pages/ApiTokens.tsx` | 页头、`ApiTokenDrawer`（创建 → 同一抽屉显示明文）、表格（⋯：查看操作记录 / 吊销） |
| `web/src/pages/RegisterWindow.tsx` | 页头（主按钮「开启新窗口」→ 抽屉）、状态卡、key 与命令 |
| `web/src/pages/Storage.tsx`、`Appearance.tsx`、`Themes.tsx`、`HeartbeatSettings.tsx` | 只换 `PageHeader` 与卡片类名 |
| `web/src/pages/Login.tsx` | 居中卡片：品牌、密码、动态验证码 / 恢复码、Passkey 入口、表单内错误 |
| `web/src/admin.css` | 删 `.edit-form`、`.page-heading*`、`.update-summary*`；追加 `.assignment*`、`.hub-card`、`.login-card`、`.switch` |

## 消费面清单

按可访问名 grep 到的全部读者。执行时只改期望不改语义；再发现的同类断言照此处理并在 result 里列出位置。

| 旧契约 | 读者 | 新契约 | 归属 |
|---|---|---|---|
| `checkbox` 「全部节点（含以后新建的节点）」「动态标签选择器」；`listbox`（`select multiple`）「动态匹配标签（交集）」/「按标签筛选（交集）」；`button` 「选择筛选结果（N）」「清空选择」 | `pages/ProbeTasks.test.tsx:411-457`、`ProbeTasksSelector.test.tsx`、`AlertRules.test.tsx:109,237,256,308`、`Silences.test.tsx:81` | `radio` 「全部节点」「动态标签选择器」「指定节点」；`MultiSelect` 「匹配标签」；`button` 「按标签快选 X」；`searchbox` 「搜索节点」；节点 `checkbox` 名仍是 `withId`；`button` 「清空已选」 | Task 1 |
| `form` 「新建探测任务」常驻 | `ProbeTasks.test.tsx` 多处 | 点 `button` 「新建探测任务」后在 `dialog` 内；`form` 名不变 | Task 2 |
| 类型 `combobox`（探测任务） | `ProbeTasks.test.tsx:89,103` | `radiogroup` 「类型」四个 `radio`（ICMP / TCP / HTTP / DNS 标签不变） | Task 2 |
| `button` 「编辑 X」「删除 X」→「确认删除 X」、`link` 「对比」「证书」（探测任务行内） | `ProbeTasks.test.tsx:373-396,489` | `button` 「更多操作 X」→ `menuitem` 同名；对比 / 证书是 `menuitem`（`a`） | Task 2 |
| 「A 行保存挂起时 B 行保存禁用」「同名任务同时编辑时保存的是被改的那一行」 | `ProbeTasks.test.tsx:196,225`；`AlertRules.test.tsx:383`；`Channels.test.tsx:401` | 单抽屉：保存在途时抽屉的「关闭抽屉」与「取消」禁用（抽屉打开期间页面其余部分由原生 `showModal` 置为 inert，不另加 `RowMenu` 的入口禁用）；同名第二条经菜单打开后保存的是它的 id | Task 2、3、5 |
| `select[multiple][required]`（动态标签非空才能提交） | `ProbeTasksSelector.test.tsx`（空动态标签不发请求） | `NodeAssignment.tsx` 导出 `assignmentValid(value)`，三处表单在 `checkValidity()` 之后先过它再 `onSubmit` | Task 1、2、3、5 |
| `form` 「新建告警规则」常驻；`button` 「编辑 X」「删除 X」 | `AlertRules.test.tsx`、`AlertRulesResource.test.tsx` | `button` 「新建告警规则」→ `dialog`；菜单 | Task 3 |
| 规则表「状态」列文字（触发 / 待定 / 已停用） | `AlertRules.test.tsx:218,549,561` | 列保留；「已停用」改由启用列的 `switch` 表达，状态列显示「—」 | Task 3 |
| 事件表列「时间 节点 变化 摘要 通知」、系统事件节点列「系统」 | `pages/AlertEvents.test.tsx:17,81,199`；`NodeDetail.test.tsx`（计划一 Task 12 新增的 tab 用例只看「没有告警事件。」） | 列「时间 节点 规则 变化 观测值 投递」；系统事件节点列「—」、规则列「—」；摘要在变化列下一行 `small` | Task 4 |
| `label` 「节点」`combobox` 筛选 | `AlertEvents.test.tsx:47,60,247,268,350` | 不变（进 `.filter-row`） | Task 4 |
| `form` 「新建维护静默」常驻；`button` 「编辑 X」「删除 X」 | `Silences.test.tsx` | `button` 「新建维护静默」→ `dialog`；菜单 | Task 5 |
| `form` 「新建通知渠道」常驻；`button` 「编辑 X」「发送测试 X」「删除 X」；删除附注文字 | `Channels.test.tsx` 多处 | `button` 「新建通知渠道」→ `dialog`；菜单项同名；附注在武装项下方 | Task 5 |
| 在线更新：`heading` 「在线更新」、`button` 「更新 Hub」「更新选中节点（0）」、文字「不支持在线更新：」 | `e2e/admin-ui.spec.ts:297-311`、`Updates.test.tsx` | 不变；来源文字搬到独立列 | Task 6 |
| 安全：`link` 「管理 TOTP、恢复码与 Passkey」、`heading` 「账户安全」、`label` 「管理员密码」「认证器名称」、`button` 「添加 Passkey」「重新绑定并添加 Passkey」、`heading` 「认证方式已更新」 | `e2e/theme-sandbox.spec.ts:118-146`、`Security.test.tsx`、`Sessions.test.tsx` | 不变；会话撤销改走菜单（`menuitem` 「撤销会话 X」→「确认撤销会话 X」） | Task 7 |
| API token：`form` 「新建 API token」常驻；`button` 「查看 X 操作记录」「吊销 X」→「确认吊销 X」 | `e2e/agentic-write.spec.ts:28-33,71,81-82`、`ApiTokens.test.tsx` | `button` 「新建 API token」→ `dialog` 内同名 `form`；`menuitem` 「查看操作记录 X（#id）」「吊销 X（#id）」 | Task 8、Task 11 |
| 注册窗口：`button` 「开启新窗口」直接提交 | `e2e/admin-ui.spec.ts:12-35`、`RegisterWindow.test.tsx` | 「开启新窗口」打开抽屉，抽屉内 `button` 「开启」提交 | Task 9、Task 11 |
| 各系统页 `heading level=1` 名（外观 / 主题 / 存储 / 在线更新 / 安全 / 账户安全 / API token / 注册窗口） | `e2e/admin-ui.spec.ts:157-168` | 不变（`PageHeader` 渲染 `h1`） | Task 6–9 |
| 登录：`label` 「管理员密码」、`button` 「登录」「使用 Passkey 登录」、文字「轻量自托管主机监控」 | `Login.test.tsx`、e2e 多处 | 不变 | Task 10 |

---

## 里程碑 C：探测与告警

### Task 1: RowMenu 附注与 NodeAssignment

**Files:**
- Modify: `web/src/components/RowMenu.tsx`、`web/src/components/RowMenu.test.tsx`
- Create: `web/src/components/NodeAssignment.tsx`、`web/src/components/NodeAssignment.test.tsx`
- Delete: `web/src/components/NodeSelector.tsx`（本任务末尾三处消费者一起切换，否则编译不过；消费者的测试期望在 Task 2、3、5 改，本任务只保证 `pnpm typecheck` 过与本组件自己的测试绿）
- Modify: `web/src/styles.css`（`.assignment*`）

**Interfaces:**
- Consumes: `MultiSelect`、`withId / toggled`、`sameTag`、`listTags`。
- Produces:
  ```ts
  // RowMenu.tsx
  export type RowMenuItem = { label: string; onSelect?: (trigger: HTMLElement) => void; to?: string; danger?: boolean; disabled?: boolean; confirm?: string; note?: string };
  //   note 只在武装后渲染：<small className="row-menu-note">{note}</small> 紧跟确认项之后、「取消」之前。
  // NodeAssignment.tsx
  export type NodeSelection = { allNodes: boolean; nodeIds: Set<bigint>; selectorTags: string[]; dynamic: boolean };   // 与原 NodeSelector 同形，调用方的 Draft 不改
  export function NodeAssignment(props: { nodes: readonly Node[]; value: NodeSelection; onChange: (patch: Partial<NodeSelection>) => void; legend: string; noun?: "分配" | "作用域" }): JSX.Element;
  ```
  DOM：`fieldset.assignment > legend{legend}`；`div[role=radiogroup][aria-label="{noun}方式"]` 三张 `label.assignment-card > input[type=radio]`：「全部节点」（`small` 含以后新建的节点）、「动态标签选择器」（`small` 标签变化自动改变覆盖）、「指定节点」（`small` 只保存本次选中的节点，之后标签变化不会改变分配）。动态：`MultiSelect label="匹配标签"`（searchable）+ `p.muted` 「当前匹配 N 个节点」/「至少选择一个标签」。指定：`div.assignment-quick` 「按标签快选」+ 每个标签一个 `button` 「按标签快选 X」（点一下把带该标签的节点并入已选）；`input[type=search][aria-label=搜索节点]`；`ul.assignment-list` 每项 `label > input[type=checkbox][aria-label=withId]`（搜索过滤，不限数量，列表自身滚动）；`p.muted` 「已选择 N 个节点」+ `button` 「清空已选」；已选里不在当前节点列表的 id 显示为 `#id（已不存在）` 并可单独移除（`button` 「移除 #id」）。单选的切换只改 `allNodes` / `dynamic`，不清空 `nodeIds` 与 `selectorTags`（来回切不丢草稿；提交时由调用方按形状取舍，与现在一样）。

- [ ] **Step 1: 写测试（红）**

`RowMenu.test.tsx` 追加：

```tsx
it("武装后的危险项下方显示附注，取消后附注消失", () => {
  mount([{ label: "删除", danger: true, confirm: "确认删除 web-01（#1）", note: "它是登录通知唯一的接收渠道，删除后登录通知关闭。", onSelect: () => {} }]);
  fireEvent.click(screen.getByRole("button", { name: "更多操作 web-01（#1）" }));
  expect(screen.queryByText(/唯一的接收渠道/)).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("menuitem", { name: "删除 web-01（#1）" }));
  expect(screen.getByText("它是登录通知唯一的接收渠道，删除后登录通知关闭。")).toHaveClass("row-menu-note");
  fireEvent.click(screen.getByRole("menuitem", { name: "取消" }));
  expect(screen.queryByText(/唯一的接收渠道/)).not.toBeInTheDocument();
});
```

`web/src/components/NodeAssignment.test.tsx`：

```tsx
import { fireEvent, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { TransportProvider } from "@connectrpc/connect-query";
import { createRouterTransport } from "@connectrpc/connect";
import { useState } from "react";
import { expect, it, vi } from "vitest";
import { AdminService } from "../gen/heron/v1/admin_pb";
import { NodeAssignment, type NodeSelection } from "./NodeAssignment";

const nodes = [
  { id: 1n, name: "a", tags: ["prod", "edge"] }, { id: 2n, name: "b", tags: ["prod"] }, { id: 3n, name: "c", tags: [] },
] as never[];
const transport = createRouterTransport(({ service }) => service(AdminService, { listTags: async () => ({ tags: [{ name: "prod", nodeCount: 2 }, { name: "edge", nodeCount: 1 }] }) }));

function Harness({ initial, onChange }: { initial: NodeSelection; onChange?: (v: NodeSelection) => void }) {
  const [value, setValue] = useState(initial);
  return <TransportProvider transport={transport}><QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <NodeAssignment nodes={nodes} value={value} legend="分配到节点" onChange={(patch) => { const next = { ...value, ...patch }; setValue(next); onChange?.(next); }} />
  </QueryClientProvider></TransportProvider>;
}
const empty = (): NodeSelection => ({ allNodes: false, nodeIds: new Set(), selectorTags: [], dynamic: false });

it("三张单选卡：全部节点与动态标签隐藏节点列表，指定节点显示搜索、快选与已选计数", async () => {
  render(<Harness initial={{ ...empty(), allNodes: true }} />);
  const group = screen.getByRole("radiogroup", { name: "分配方式" });
  expect(within(group).getAllByRole("radio").map((r) => r.getAttribute("aria-label") ?? "")).toEqual(["全部节点", "动态标签选择器", "指定节点"]);
  expect(within(group).getByRole("radio", { name: "全部节点" })).toBeChecked();
  expect(screen.queryByRole("searchbox", { name: "搜索节点" })).not.toBeInTheDocument();
  fireEvent.click(within(group).getByRole("radio", { name: "指定节点" }));
  expect(screen.getByRole("searchbox", { name: "搜索节点" })).toBeInTheDocument();
  expect(screen.getByText("只保存本次选中的节点，之后标签变化不会改变分配")).toBeInTheDocument();
  expect(screen.getByText("已选择 0 个节点")).toBeInTheDocument();
  expect(await screen.findByRole("button", { name: "按标签快选 prod" })).toBeInTheDocument();
});

it("按标签快选并入带该标签的节点；搜索过滤勾选列表；清空已选", async () => {
  const onChange = vi.fn();
  render(<Harness initial={empty()} onChange={onChange} />);
  fireEvent.click(await screen.findByRole("button", { name: "按标签快选 prod" }));
  expect(onChange).toHaveBeenLastCalledWith(expect.objectContaining({ nodeIds: new Set([1n, 2n]) }));
  expect(screen.getByText("已选择 2 个节点")).toBeInTheDocument();
  fireEvent.change(screen.getByRole("searchbox", { name: "搜索节点" }), { target: { value: "c" } });
  expect(screen.getAllByRole("checkbox").map((c) => c.getAttribute("aria-label"))).toEqual(["c（#3）"]);
  fireEvent.click(screen.getByRole("checkbox", { name: "c（#3）" }));
  expect(onChange).toHaveBeenLastCalledWith(expect.objectContaining({ nodeIds: new Set([1n, 2n, 3n]) }));
  fireEvent.click(screen.getByRole("button", { name: "清空已选" }));
  expect(onChange).toHaveBeenLastCalledWith(expect.objectContaining({ nodeIds: new Set() }));
});

it("已选里不在当前列表的节点标为已不存在并可单独移除", () => {
  const onChange = vi.fn();
  render(<Harness initial={{ ...empty(), nodeIds: new Set([2n, 9n]) }} onChange={onChange} />);
  expect(screen.getByText("#9（已不存在）")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "移除 #9" }));
  expect(onChange).toHaveBeenLastCalledWith(expect.objectContaining({ nodeIds: new Set([2n]) }));
});

it("动态标签选择器：匹配标签多选，显示当前匹配数；没选标签时提示至少一个", async () => {
  const onChange = vi.fn();
  render(<Harness initial={{ ...empty(), dynamic: true }} onChange={onChange} />);
  expect(screen.getByText("至少选择一个标签")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: /^匹配标签/ }));
  fireEvent.click(await screen.findByRole("checkbox", { name: "prod" }));
  expect(onChange).toHaveBeenLastCalledWith(expect.objectContaining({ selectorTags: ["prod"] }));
  expect(screen.getByText("当前匹配 2 个节点")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("checkbox", { name: "edge" }));
  expect(screen.getByText("当前匹配 1 个节点")).toBeInTheDocument();
});

it("切换单选不丢另一种形状的草稿", () => {
  const onChange = vi.fn();
  render(<Harness initial={{ ...empty(), nodeIds: new Set([1n]) }} onChange={onChange} />);
  fireEvent.click(screen.getByRole("radio", { name: "动态标签选择器" }));
  expect(onChange).toHaveBeenLastCalledWith(expect.objectContaining({ dynamic: true, allNodes: false, nodeIds: new Set([1n]) }));
  fireEvent.click(screen.getByRole("radio", { name: "指定节点" }));
  expect(screen.getByText("已选择 1 个节点")).toBeInTheDocument();
});
```

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/components/RowMenu.test.tsx src/components/NodeAssignment.test.tsx > /tmp/c1-red.log 2>&1; echo $?` → 1

- [ ] **Step 3: 实现**

`RowMenu.tsx`：`RowMenuItem` 加 `note?: string`；渲染武装项处，在确认项与「取消」之间加 `{item.note && <small className="row-menu-note">{item.note}</small>}`。CSS：`.row-menu-note { display: block; padding: 2px 12px 6px; font-size: 12px; color: var(--muted); max-width: 32ch; }`。

`web/src/components/NodeAssignment.tsx`：

```tsx
import { useQuery } from "@connectrpc/connect-query";
import { useState } from "react";
import { AdminService, type Node } from "../gen/heron/v1/admin_pb";
import { toggled, withId } from "../lib/ids";
import { literalPattern } from "../lib/fold";
import { sameTag } from "../lib/tags";
import { MultiSelect } from "./MultiSelect";

export type NodeSelection = { allNodes: boolean; nodeIds: Set<bigint>; selectorTags: string[]; dynamic: boolean };
type Mode = "all" | "dynamic" | "picked";
const modeOf = (v: NodeSelection): Mode => (v.allNodes ? "all" : v.dynamic ? "dynamic" : "picked");

// 分配 / 作用域三种形状（设计 §4.4；hub：all_nodes 与 selector_tags 互斥，空显式集合由 hub 拒绝）用三张单选卡表达，
// 切换只改 allNodes / dynamic 两个开关，不清空另一种形状的草稿——来回切不丢已选；提交时调用方按形状取舍（与原 NodeSelector 相同）。
// 已选集合可能含当前列表里没有的 id（列表刷新后被删的节点）：必须可见、可移除，提交时由调用方的 liveIds 剔除。
export function NodeAssignment({ nodes, value, onChange, legend, noun = "分配" }: {
  nodes: readonly Node[]; value: NodeSelection; onChange: (patch: Partial<NodeSelection>) => void; legend: string; noun?: "分配" | "作用域";
}) {
  const tags = useQuery(AdminService.method.listTags, {});
  const [search, setSearch] = useState("");
  const mode = modeOf(value);
  const known = [...new Set([...(tags.data?.tags.map((t) => t.name) ?? nodes.flatMap((n) => n.tags)), ...value.selectorTags])].sort();
  const options = known.map((name) => ({ value: name, label: name, count: nodes.filter((n) => n.tags.some((t) => sameTag(t, name))).length }));
  const matching = nodes.filter((n) => value.selectorTags.every((tag) => n.tags.some((t) => sameTag(t, tag))));
  const pattern = search === "" ? null : literalPattern(search, false);
  const listed = pattern ? nodes.filter((n) => pattern.test(n.name) || pattern.test(String(n.id))) : nodes;
  const missing = [...value.nodeIds].filter((id) => !nodes.some((n) => n.id === id));
  const setMode = (next: Mode) => onChange({ allNodes: next === "all", dynamic: next === "dynamic" });
  return (
    <fieldset className="assignment">
      <legend>{legend}</legend>
      <div className="assignment-modes" role="radiogroup" aria-label={`${noun}方式`}>
        <label className="assignment-card"><input type="radio" name={`${legend}-mode`} aria-label="全部节点" checked={mode === "all"} onChange={() => setMode("all")} /><span>全部节点</span><small>含以后新建的节点</small></label>
        <label className="assignment-card"><input type="radio" name={`${legend}-mode`} aria-label="动态标签选择器" checked={mode === "dynamic"} onChange={() => setMode("dynamic")} /><span>动态标签选择器</span><small>以后新节点或标签变更会自动改变覆盖，移除标签也会撤销覆盖</small></label>
        <label className="assignment-card"><input type="radio" name={`${legend}-mode`} aria-label="指定节点" checked={mode === "picked"} onChange={() => setMode("picked")} /><span>指定节点</span><small>只保存本次选中的节点，之后标签变化不会改变分配</small></label>
      </div>
      {mode === "dynamic" && <>
        <MultiSelect label="匹配标签" searchable options={options} selected={value.selectorTags} onChange={(selectorTags) => onChange({ selectorTags })} />
        <p className="muted">{value.selectorTags.length === 0 ? "至少选择一个标签" : `当前匹配 ${matching.length} 个节点`}</p>
      </>}
      {mode === "picked" && <>
        {known.length > 0 && <div className="assignment-quick"><span className="muted">按标签快选</span>
          {known.map((name) => <button key={name} type="button" className="chip" aria-label={`按标签快选 ${name}`} onClick={() => onChange({ nodeIds: new Set([...value.nodeIds, ...nodes.filter((n) => n.tags.some((t) => sameTag(t, name))).map((n) => n.id)]) })}>{name}</button>)}
        </div>}
        <input type="search" aria-label="搜索节点" placeholder="名称或 #id" value={search} onChange={(event) => setSearch(event.target.value)} />
        <ul className="assignment-list">
          {listed.map((n) => <li key={String(n.id)}><label><input type="checkbox" aria-label={withId(n.name, n.id)} checked={value.nodeIds.has(n.id)} onChange={() => onChange({ nodeIds: toggled(value.nodeIds, n.id) })} />{withId(n.name, n.id)}</label></li>)}
          {missing.map((id) => <li key={String(id)} className="muted">#{String(id)}（已不存在）<button type="button" className="link" aria-label={`移除 #${id}`} onClick={() => onChange({ nodeIds: toggled(value.nodeIds, id) })}>移除</button></li>)}
          {listed.length === 0 && missing.length === 0 && <li className="muted">没有匹配的节点。</li>}
        </ul>
        <p className="muted">已选择 {value.nodeIds.size} 个节点 <button type="button" className="link" onClick={() => onChange({ nodeIds: new Set() })}>清空已选</button></p>
      </>}
    </fieldset>
  );
}
```

`styles.css`：

```css
/* 分配 / 作用域（components/NodeAssignment.tsx）：三张卡片式单选一行，指定节点的勾选列表自身滚动。 */
.assignment { border: 0; padding: 0; margin: 12px 0; min-width: 0; }
.assignment-modes { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 8px; margin: 6px 0 10px; }
.assignment-card { display: grid; grid-template-columns: auto 1fr; gap: 2px 8px; align-items: start; padding: 10px 12px; border: 1px solid var(--line); border-radius: var(--radius-card); background: var(--card); cursor: pointer; }
.assignment-card:has(input:checked) { border-color: var(--fg); }
.assignment-card input { margin: 3px 0 0; }
.assignment-card span { font-weight: 600; }
.assignment-card small { grid-column: 2; color: var(--muted); font-size: 12px; }
.assignment-quick { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; margin-bottom: 8px; }
.assignment-list { list-style: none; margin: 8px 0; padding: 0; max-height: 240px; overflow: auto; border: 1px solid var(--line); border-radius: var(--radius-control); }
.assignment-list li { padding: 0 10px; }
.assignment-list label { display: flex; gap: 8px; align-items: center; height: var(--control-h); }
@media (max-width: 900px) { .assignment-modes { grid-template-columns: 1fr; } }
```

三处消费者（`ProbeTasks.tsx`、`AlertRules.tsx`、`Silences.tsx`）把 `import { NodeSelector, type NodeSelection } from "../components/NodeSelector"` 换成 `import { NodeAssignment, type NodeSelection } from "../components/NodeAssignment"`，`<NodeSelector … legend="分配到节点" />` → `<NodeAssignment … legend="分配到节点" />`（告警与静默传 `noun="作用域"`）。删除 `NodeSelector.tsx`。

- [ ] **Step 4: 跑测试确认绿；typecheck**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/components/RowMenu.test.tsx src/components/NodeAssignment.test.tsx > /tmp/c1-green.log 2>&1; echo $?` → 0；`pnpm typecheck` → 0。（`ProbeTasks.test` 等消费者此时红，Task 2 / 3 / 5 收口；result 里列出红的标题。）

- [ ] **Step 5: 缺陷注入**

(a) `setMode` 改成同时 `nodeIds: new Set()`：「切换单选不丢另一种形状的草稿」红。(b) `missing` 固定为 `[]`：「已不存在」用例红。恢复后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add -A web/src/components/RowMenu.tsx web/src/components/RowMenu.test.tsx web/src/components/NodeAssignment.tsx web/src/components/NodeAssignment.test.tsx web/src/components/NodeSelector.tsx web/src/pages/ProbeTasks.tsx web/src/pages/AlertRules.tsx web/src/pages/Silences.tsx web/src/styles.css && git commit -m "feat(web): 分配与作用域改为三张卡片式单选，指定节点带按标签快选与可搜索勾选列表"
```

### Task 2: 探测任务：页头、表格、抽屉

**Files:**
- Modify: `web/src/pages/ProbeTasks.tsx`、`web/src/pages/ProbeTasks.test.tsx`、`web/src/pages/ProbeTasksSelector.test.tsx`
- Modify: `web/src/admin.css`（删 `.edit-form` 相关；加 `.segmented`）

**Interfaces:**
- Consumes: `Drawer`、`RowMenu`、`PageHeader`、`NodeAssignment`。
- Produces: DOM —— `PageHeader title="探测任务"`，主按钮 `button.primary-button` 「新建探测任务」；表头 `排序 | 类型 | 目标 | 间隔 | 超时 | 分配 | 操作(sr-only)`；分配格 `span[title=节点名清单]`：「全部节点」/「标签：a ∩ b（当前 N）」/「N 个指定节点」/「未分配」；`RowMenu label=withId(target,id)` 项目：编辑、对比（`to=/probes/:id/compare`）、证书（仅 https，`to=/probes/:id/certs`）、删除（`confirm: 确认删除 X`，`note: 历史保留至到期清理`）。`ProbeTaskDrawer({ title, nodes, initial, pending, error, opener, onClose, onSubmit })`：`Drawer` 内 `form[aria-label=title]`；类型 `div.segmented[role=radiogroup][aria-label=类型]` 四个 `radio`（`PROBE_KINDS` 的 label）；其余字段同原 `TaskForm`（目标、解析器、间隔 (s)、超时 (ms)、证书指纹、清除指纹）；`NodeAssignment legend="分配到节点"`；底部「取消」「创建 / 保存」。页面状态：`drawer: { kind: "create", opener } | { kind: "edit", entry, opener } | null`；保存在途时 `busy`，`RowMenu` 各项 `disabled`。

- [ ] **Step 1: 改既有测试的期望并加新用例（红）**

Run: `cd /Users/xjetry/work/vibe/probe/web && grep -n "新建探测任务\|name: \`编辑 \|name: \`删除 \|确认删除\|combobox\|getByLabelText(\"类型\")\|全部节点（含\|动态标签选择器\|选择筛选结果\|清空选择\|对比\|证书" src/pages/ProbeTasks.test.tsx src/pages/ProbeTasksSelector.test.tsx > /tmp/c2-consumers.log 2>&1; echo $?`

| 旧 | 新 |
|---|---|
| 直接 `getByRole("form", { name: "新建探测任务" })` | 先 `fireEvent.click(screen.getByRole("button", { name: "新建探测任务" }))`；helper `openCreate()` |
| `getByRole("button", { name: \`编辑 ${label}\` })` | `openRowAction(label, "编辑")`（与计划一同一个 helper，复制到本文件） |
| 删除 / 确认删除 | `openRowAction(label, "删除")` → `menuitem` 「确认删除 X」 |
| 类型 `combobox` `fireEvent.change(select, { target: { value } })` | `fireEvent.click(within(dialog).getByRole("radio", { name: "DNS" }))` |
| 「勾选全部节点后隐藏节点多选」 | `radio` 「全部节点」后 `queryByRole("searchbox", { name: "搜索节点" })` 为 null |
| 「标签批量选择保存固定节点，动态选择器只保存非空标签」 | 指定节点：`button` 「按标签快选 X」；动态：`radio` 「动态标签选择器」+ `MultiSelect` 「匹配标签」 |
| 「A 行保存挂起时 B 行保存禁用，刷新完成才关闭 A 行」 | 改名「保存挂起时抽屉不可关闭，刷新完成才关闭抽屉」：`button` 「关闭抽屉」与「取消」`toBeDisabled()`，刷新 resolve 后 `dialog` 消失；不断言其它行（抽屉打开期间它们由原生 dialog 置为 inert，jsdom 不模拟） |
| 「标签批量选择保存固定节点，动态选择器只保存非空标签」里的空动态标签不提交 | `handle` 在 `checkValidity()` 之后 `if (!assignmentValid(draft)) return;`；`assignmentValid` 定义在 `NodeAssignment.tsx` 末尾：`export const assignmentValid = (value: NodeSelection): boolean => value.allNodes \|\| !value.dynamic \|\| value.selectorTags.length > 0;` |
| 「同名任务同时编辑时保存的是被改的那一行」 | 改名「同名任务经菜单打开第二条，保存的是第二条的 id」 |
| 「创建成功后复位全部字段，下一次提交不沿用旧值」 | 成功后 `dialog` 消失；再 `openCreate()`，字段是默认值 |
| 「列出任务与分配的节点名」 | 分配格文字按新摘要：显式两个节点 → 「2 个指定节点」且 `title="a、b"`；`allNodes` → 「全部节点」；动态 → 「标签：prod ∩ edge（当前 1）」；显式空 → 「未分配」 |
| 「只有 https 的 HTTP 任务有证书链接」 | 打开菜单后 `queryByRole("menuitem", { name: /^证书/ })` |

新增：

```tsx
it("编辑抽屉打开时列表刷新把这条任务删掉：抽屉仍在，保存被 hub 拒绝时原文显示在抽屉内", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  let gone = false;
  const listProbeTasks = vi.fn(async () => ({ tasks: gone ? [] : [{ task: { id: 5n, kind: ProbeKind.ICMP, target: "1.1.1.1", intervalS: 60, timeoutMs: 1000 }, allNodes: true, nodeIds: [], selectorTags: [] }] }));
  const saveProbeTask = vi.fn(async () => { throw new ConnectError("task 5 not found", Code.NotFound); });
  renderWithAdmin({ listNodes: async () => ({ nodes: [] }), listTags: async () => ({ tags: [] }), listProbeTasks, saveProbeTask }, [{ path: "/probes", Component: ProbeTasks }], "/probes");
  await screen.findByText("1.1.1.1");
  openRowAction("1.1.1.1（#5）", "编辑");
  const dialog = screen.getByRole("dialog");
  gone = true;
  await act(async () => { await queryClientOf().invalidateQueries(); });   // 用 renderWithAdmin 返回的 queryClient
  expect(screen.getByRole("dialog")).toBe(dialog);
  fireEvent.click(within(dialog).getByRole("button", { name: "保存" }));
  expect(await within(dialog).findByRole("alert")).toHaveTextContent("task 5 not found");
});
```

（`queryClientOf()` 指 `renderWithAdmin` 返回值里的 `queryClient`，按本文件既有写法取。）

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/ProbeTasks.test.tsx src/pages/ProbeTasksSelector.test.tsx > /tmp/c2-red.log 2>&1; echo $?` → 1

- [ ] **Step 3: 实现**

`ProbeTasks.tsx`：

```tsx
// 顶部 import 增：Drawer、RowMenu、PageHeader、NodeAssignment（Task 1 已换）、Icon 不需要
type DrawerState = { kind: "create"; opener: HTMLElement } | { kind: "edit"; entry: TaskEntry; opener: HTMLElement } | null;

export function ProbeTasks() {
  // …查询、mutation、order 原样…
  const [drawer, setDrawer] = useState<DrawerState>(null);
  const busy = create.isPending || update.isPending;
  // …gate、submit 原样…
  const tasks = taskEntries(order.items);
  const assignmentText = (entry: TaskEntry) => {
    const names = entry.nodeIds.map((id) => nodeList.find((n) => n.id === id)?.name ?? `#${id}`);
    if (entry.allNodes) return { text: "全部节点", title: names.join("、") || "暂无节点" };
    if (entry.selectorTags.length) return { text: `标签：${entry.selectorTags.join(" ∩ ")}（当前 ${names.length}）`, title: names.join("、") || "无匹配" };
    // 显式分配为空显示"未分配"：它不覆盖任何节点，与"全部节点"区分开。
    return names.length ? { text: `${names.length} 个指定节点`, title: names.join("、") } : { text: "未分配", title: "" };
  };
  return (
    <section>
      <PageHeader title="探测任务" actions={<button type="button" className="primary-button" disabled={busy} onClick={(event) => { create.reset(); setDrawer({ kind: "create", opener: event.currentTarget }); }}>新建探测任务</button>} />
      {gate.banner}
      {drawer === null && error != null && <p role="alert" className="error">{error instanceof ConnectError && error.code === Code.FailedPrecondition ? `配置已变化，请刷新后再试。${errorText(error)}` : errorText(error)}</p>}
      {order.error != null && <p role="alert" className="error">排序未完成：{errorText(order.error)}</p>}
      {order.pending && <p role="status" className="muted">正在保存并确认排序…</p>}
      {order.blocked && <button type="button" onClick={order.recover} disabled={order.pending}>重新读取排序</button>}
      <div className="table-scroll" role="region" aria-label="探测任务管理" tabIndex={0}>
        <table className="nodes probe-table">
          <thead><tr><th><span className="sr-only">排序</span></th><th>类型</th><th>目标</th><th>间隔</th><th>超时</th><th>分配</th><th><span className="sr-only">操作</span></th></tr></thead>
          <tbody>{tasks.map((entry) => {
            const t = entry.task; const label = withId(t.target, t.id); const assignment = assignmentText(entry);
            const movable = !(order.blocked || list.isError);
            return <tr key={String(t.id)} aria-label={t.target}>
              <td data-label="排序"><button type="button" className="link" aria-label={`上移 ${label}`} disabled={!movable} onClick={() => order.move(t.id, -1)}>↑</button><button type="button" className="link" aria-label={`下移 ${label}`} disabled={!movable} onClick={() => order.move(t.id, 1)}>↓</button></td>
              <td data-label="类型">{kindLabel(t.kind)}</td>
              <td data-label="目标" className="num">{t.target}</td>
              <td data-label="间隔" className="num">{t.intervalS} s</td>
              <td data-label="超时" className="num">{t.timeoutMs} ms</td>
              <td data-label="分配"><span title={assignment.title || undefined} className={assignment.text === "未分配" ? "muted" : undefined}>{assignment.text}</span></td>
              <td data-column="actions"><RowMenu label={label} items={[
                { label: "编辑", disabled: busy, onSelect: (trigger) => { update.reset(); setDrawer({ kind: "edit", entry, opener: trigger }); } },
                { label: "对比", to: `/probes/${t.id}/compare` },
                ...(isHTTPSTarget(t.kind, t.target) ? [{ label: "证书", to: `/probes/${t.id}/certs` }] : []),
                { label: "删除", danger: true, confirm: `确认删除 ${label}`, note: "历史保留至到期清理", disabled: busy || remove.isPending, onSelect: () => remove.mutate({ id: t.id }) },
              ]} /></td>
            </tr>;
          })}</tbody>
        </table>
      </div>
      {tasks.length === 0 && <p className="muted">还没有探测任务。</p>}
      {drawer?.kind === "create" && <ProbeTaskDrawer title="新建探测任务" nodes={nodeList} initial={emptyDraft()} pending={create.isPending} error={create.error} opener={drawer.opener} onClose={() => setDrawer(null)}
        onSubmit={(d) => submit(create, 0n, d, () => setDrawer(null))} />}
      {drawer?.kind === "edit" && <ProbeTaskDrawer key={String(drawer.entry.task.id)} title={`编辑 ${withId(drawer.entry.task.target, drawer.entry.task.id)}`} nodes={nodeList} initial={draftOf(drawer.entry)} pending={update.isPending} error={update.error} opener={drawer.opener} onClose={() => setDrawer(null)}
        onSubmit={(d) => submit(update, drawer.entry.task.id, d, () => setDrawer(null))} />}
    </section>
  );
}
```

`submit` 的 `onSuccess` 仍在 `refresh` 之后触发（mutation 的 `onSuccess: refresh` 返回 promise，`mutate(req, { onSuccess })` 的回调在它 resolve 后），所以「刷新完成才关闭抽屉」的既有语义不变。

`TaskForm` → `ProbeTaskDrawer`：

```tsx
function ProbeTaskDrawer({ title, nodes, initial, pending, error, opener, onClose, onSubmit }: {
  title: string; nodes: Node[]; initial: Draft; pending: boolean; error: unknown; opener: HTMLElement; onClose: () => void; onSubmit: (d: Draft) => void;
}) {
  // initial 只在挂载时读取；编辑期间的列表刷新不覆盖草稿，节点列表以 props 实时更新，提交时与当前列表求交。
  const [draft, setDraft] = useState(initial);
  const [pinError, setPinError] = useState("");
  const showPin = draft.kind === ProbeKind.HTTP || draft.pin.trim() !== "" || draft.clearPin;
  const pinFits = isHTTPSTarget(draft.kind, draft.target);
  const handle = (e: FormEvent<HTMLFormElement>) => { /* 原 TaskForm.handle 原样 */ };
  return <Drawer title={title} busy={pending} opener={opener} onClose={onClose}>
    <form aria-label={title} onSubmit={handle}>
      <div className="modal-body">
        {errorBanner(error)}
        <fieldset className="bare" disabled={pending}>
          <div className="segmented" role="radiogroup" aria-label="类型">
            {PROBE_KINDS.map(({ kind, label }) => <label key={kind}><input type="radio" name="probe-kind" aria-label={label} checked={draft.kind === kind} onChange={() => setDraft({ ...draft, kind })} /><span>{label}</span></label>)}
          </div>
          <label>目标<input data-autofocus required maxLength={targetRule(draft.kind).maxLength} placeholder={targetRule(draft.kind).placeholder} value={draft.target} onChange={(e) => setDraft({ ...draft, target: e.target.value })} /></label>
          {draft.kind === ProbeKind.DNS && <label>解析器<input required maxLength={47} placeholder="ip:port，如 1.1.1.1:53" value={draft.dnsServer} onChange={(e) => setDraft({ ...draft, dnsServer: e.target.value })} /></label>}
          <div className="form-grid two">
            <label>间隔 (s)<input type="number" required min={5} max={3600} value={draft.intervalS} onChange={(e) => setDraft({ ...draft, intervalS: e.target.value })} /></label>
            <label>超时 (ms)<input type="number" required min={100} max={5000} value={draft.timeoutMs} onChange={(e) => setDraft({ ...draft, timeoutMs: e.target.value })} /></label>
          </div>
          {showPin && <label>证书指纹<input aria-label="证书指纹" placeholder="sha256// 加 base64，留空表示不改" value={draft.pin} onChange={(e) => setDraft({ ...draft, pin: e.target.value, clearPin: false })} /></label>}
          {showPin && <p className="muted">{draft.clearPin ? "保存时将清除指纹。" : draft.pin ? `当前显示 ${draft.pin}` : "未钉指纹。"}{" "}<button type="button" className="link" onClick={() => setDraft({ ...draft, pin: "", clearPin: true })}>清除指纹</button></p>}
          {showPin && !pinFits && <p className="muted">这个种类或地址不能钉指纹，改种类不会自动清除。请先清除指纹再保存。</p>}
          {pinError && <p role="alert" className="error">{pinError}</p>}
          <NodeAssignment nodes={nodes} value={draft} onChange={(patch) => setDraft({ ...draft, ...patch })} legend="分配到节点" />
        </fieldset>
      </div>
      <footer className="modal-footer"><button type="button" disabled={pending} onClick={onClose}>取消</button><button type="submit" className="primary-button" disabled={pending}>{initial.configId ? "保存" : "创建"}</button></footer>
    </form>
  </Drawer>;
}
```

（按钮文字按「是编辑还是新建」判定：`draftOf` 带 `configId`，`emptyDraft` 不带；若有任务没有 `configId`（旧 hub），用 `title.startsWith("编辑")` 兜不住——改为给 `ProbeTaskDrawer` 加 `submitLabel: "创建" | "保存"` prop，由调用方传。按这个实现，不用 `configId` 推断。）

`admin.css`：删 `.admin-shell .edit-form*`；追加 `.segmented { display: inline-flex; border: 1px solid var(--line); border-radius: var(--radius-control); overflow: hidden; margin-bottom: 12px; } .segmented label { position: relative; } .segmented input { position: absolute; inset: 0; opacity: 0; margin: 0; cursor: pointer; } .segmented span { display: inline-block; height: 30px; line-height: 30px; padding: 0 12px; font-size: 13px; color: var(--muted); } .segmented input:checked + span { background: var(--fg); color: var(--bg); } .segmented input:focus-visible + span { outline: 2px solid var(--accent); outline-offset: -2px; } .form-grid.two { display: grid; grid-template-columns: 1fr 1fr; gap: 0 12px; }`。

- [ ] **Step 4: 跑测试确认绿；全量**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/ProbeTasks.test.tsx src/pages/ProbeTasksSelector.test.tsx > /tmp/c2-green.log 2>&1; echo $?` → 0；`pnpm vitest run` → 0（AlertRules / Silences 仍可能红到 Task 3 / 5，result 里列出）；`pnpm typecheck` → 0。

- [ ] **Step 5: 缺陷注入**

(a) `setDrawer(null)` 直接写在 `mutate` 之前（不等刷新）：「刷新完成才关闭抽屉」用例红。(b) 抽屉打开时 `useQuery(listProbeTasks)` 的数据直接覆盖 `draft`（在 `ProbeTaskDrawer` 加 `useEffect(() => setDraft(initial), [initial])`）：「探测任务刷新失败保留同一编辑表单与草稿」红。恢复后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/pages/ProbeTasks.tsx web/src/pages/ProbeTasks.test.tsx web/src/pages/ProbeTasksSelector.test.tsx web/src/admin.css && git commit -m "feat(web): 探测任务改为页头、分配摘要表格与右侧抽屉，类型用分段单选"
```

### Task 3: 告警规则：页头、触发中筛选、启用开关、抽屉

**Files:**
- Modify: `web/src/pages/AlertRules.tsx`、`web/src/pages/AlertRules.test.tsx`、`web/src/pages/AlertRulesResource.test.tsx`
- Modify: `web/src/admin.css`（`.switch`）

**Interfaces:**
- Consumes: `Drawer`、`RowMenu`、`PageHeader`、`NodeAssignment`、`Picks`（通知渠道）、`useSearchParams`。
- Produces: DOM —— `PageHeader title="告警规则"`，主按钮 「新建告警规则」；`.filter-row[role=group][aria-label=筛选]`：`checkbox` 「只看触发中」（绑定 `?state=firing`；其它值忽略）、有筛选时 `button` 「清除筛选」、`span.num` 「{显示} / {总数}」；表头 `名称 | 类型 | 条件 | 作用域 | 通知渠道 | 启用 | 状态 | 操作(sr-only)`；启用格 `input[type=checkbox][role=switch][aria-label="启用 {withId}"]`，切换即 `update.mutate({ rule: { …toRule(r.id, draftOf(r)), enabled: !r.enabled } })`，在途时 `disabled` 且 `aria-busy`；状态格：`RuleState`（停用时「—」）；`RowMenu` 项目：编辑、删除（`confirm`，`note: 事件记录保留`）。`AlertRuleDrawer({ title, submitLabel, lists, initial, pending, error, opener, onClose, onSubmit })`：原 `RuleForm` 的字段进 `Drawer`；「启用」勾选留在表单顶部；通知渠道 `Picks`；没有渠道时 `p.muted` 「还没有通知渠道；规则只记录事件，不发送通知。」不变。

- [ ] **Step 1: 改既有测试的期望并加新用例（红）**

Run: `cd /Users/xjetry/work/vibe/probe/web && grep -n "新建告警规则\|name: \`编辑 \|name: \`删除 \|确认删除\|已停用\|全部节点（含\|动态标签选择器\|选择筛选结果\|getByRole(\"form\"" src/pages/AlertRules.test.tsx src/pages/AlertRulesResource.test.tsx > /tmp/c3-consumers.log 2>&1; echo $?`

| 旧 | 新 |
|---|---|
| `getByRole("form", { name: "新建告警规则" })` 常驻 | `openCreate()`（点「新建告警规则」） |
| `编辑 X` / `删除 X` 按钮 | `openRowAction(label, "编辑" / "删除")` |
| 「从 enabled=%s 编辑开关，保存与刷新后状态一致」 | 两条路径都钉：抽屉里的「启用」勾选；行上的 `switch` 「启用 X」点击提交整条规则（`saveAlertRule` 收到 `enabled` 翻转、其余字段等于 `toRule(draftOf(r))`） |
| 「列表展示名称、条件、作用域、通知与当前状态」中 `已停用` 文字 | 启用列 `switch` `not.toBeChecked()`，状态列 `—` |
| 「一行保存挂起时其它行的保存禁用」 | 「保存挂起时抽屉不可关闭、各行启用开关禁用」（不断言其它行的 `更多操作`：抽屉打开期间由原生 dialog 置为 inert）；表单 `handle` 同样先过 `assignmentValid` |
| 「编辑态在刷新完成后才关闭」 | `dialog` 在刷新 resolve 后消失 |
| NodeSelector 相关（109、237、256、308） | 按 Task 1 的新契约（`radio` / 快选 / 勾选列表） |

新增：

```tsx
it("?state=firing 只列有触发中状态的规则；非法值忽略；清除筛选回写 URL", async () => {
  const { router } = renderWithAdmin({ ...impl, listAlertRules: async () => ({ rules: [ruleA, ruleB], states: [{ ruleId: ruleA.id, nodeId: 1n, state: "firing", flapping: false, silenced: false }] }) }, [{ path: "/alerts", Component: AlertRules }], "/alerts?state=firing");
  await screen.findByRole("row", { name: ruleA.name });
  expect(screen.queryByRole("row", { name: ruleB.name })).not.toBeInTheDocument();
  expect(screen.getByRole("checkbox", { name: "只看触发中" })).toBeChecked();
  fireEvent.click(screen.getByRole("button", { name: "清除筛选" }));
  expect(router.state.location.search).toBe("");
  expect(screen.getByRole("row", { name: ruleB.name })).toBeInTheDocument();
  const second = renderWithAdmin(impl, [{ path: "/alerts", Component: AlertRules }], "/alerts?state=bogus");
  expect((await screen.findAllByRole("checkbox", { name: "只看触发中" })).at(-1)).not.toBeChecked();
  second.unmount();
});

it("行上的启用开关提交整条规则：在途禁用，失败弹回并显示原文，成功不打开抽屉", async () => {
  let resolve!: (v: unknown) => void;
  const saveAlertRule = vi.fn(() => new Promise((r) => { resolve = r; }));
  renderWithAdmin({ ...impl, saveAlertRule }, [{ path: "/alerts", Component: AlertRules }], "/alerts");
  const toggle = await screen.findByRole("switch", { name: `启用 ${withId(ruleA.name, ruleA.id)}` });
  expect(toggle).toBeChecked();
  fireEvent.click(toggle);
  expect(saveAlertRule).toHaveBeenCalledWith(expect.objectContaining({ rule: expect.objectContaining({ id: ruleA.id, enabled: false, name: ruleA.name }) }), expect.anything());
  expect(toggle).toBeDisabled();
  await act(async () => { resolve({}); });
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  saveAlertRule.mockImplementationOnce(async () => { throw new ConnectError("nope", Code.InvalidArgument); });
  fireEvent.click(screen.getByRole("switch", { name: `启用 ${withId(ruleA.name, ruleA.id)}` }));
  expect(await screen.findByRole("alert")).toHaveTextContent("nope");
  expect(screen.getByRole("switch", { name: `启用 ${withId(ruleA.name, ruleA.id)}` })).toBeChecked();
});
```

（`impl / ruleA / ruleB` 用本文件既有夹具；没有同形的就按既有 `rules` 夹具取前两条并命名。行 `tr` 加 `aria-label={r.name}`。）

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/AlertRules.test.tsx src/pages/AlertRulesResource.test.tsx > /tmp/c3-red.log 2>&1; echo $?` → 1

- [ ] **Step 3: 实现**

`AlertRules.tsx` 的页面部分：

```tsx
  const [params, setParams] = useSearchParams();
  // 「需要处理」的触发中卡链到这里；非法值当没写。
  const firingOnly = params.get("state") === "firing";
  const setFiringOnly = (on: boolean) => { const p = new URLSearchParams(params); if (on) p.set("state", "firing"); else p.delete("state"); setParams(p, { replace: true }); };
  const [drawer, setDrawer] = useState<{ kind: "create"; opener: HTMLElement } | { kind: "edit"; rule: AlertRule; opener: HTMLElement } | null>(null);
  // 行上的开关与抽屉共用 update：任一在途时开关与菜单都禁用（共用 observer，重叠的 mutate 只回调最后一次）。
  const [toggling, setToggling] = useState<bigint | null>(null);
  const busy = create.isPending || update.isPending;
  // …gate…
  const shown = firingOnly ? rulesData.rules.filter((r) => (byRule.get(r.id)?.firing.length ?? 0) > 0) : rulesData.rules;
  const toggleEnabled = (r: AlertRule) => {
    setToggling(r.id);
    update.mutate({ rule: { ...toRule(r.id, draftOf(r), nodeList, channelList), enabled: !r.enabled } }, { onSettled: () => setToggling(null) });
  };
  return (
    <section>
      <PageHeader title="告警规则" actions={<button type="button" className="primary-button" disabled={busy} onClick={(event) => { create.reset(); setDrawer({ kind: "create", opener: event.currentTarget }); }}>新建告警规则</button>} />
      {gate.banner}
      <div className="filter-row" role="group" aria-label="筛选">
        <label className="check"><input type="checkbox" aria-label="只看触发中" checked={firingOnly} onChange={(event) => setFiringOnly(event.target.checked)} />只看触发中</label>
        {firingOnly && <button type="button" className="link" onClick={() => setFiringOnly(false)}>清除筛选</button>}
        <span className="muted num">{shown.length} / {rulesData.rules.length}</span>
      </div>
      {drawer === null && error != null && <p role="alert" className="error">{errorText(error)}</p>}
      <div className="table-scroll" role="region" aria-label="告警规则管理" tabIndex={0}>
        <table className="nodes">
          <thead><tr><th>名称</th><th>类型</th><th>条件</th><th>作用域</th><th>通知渠道</th><th>启用</th><th>状态</th><th><span className="sr-only">操作</span></th></tr></thead>
          <tbody>{shown.map((r) => {
            const label = withId(r.name, r.id);
            return <tr key={String(r.id)} aria-label={r.name}>
              <td data-label="名称">{r.name}</td>
              <td data-label="类型">{labelOf(ALERT_KINDS, r.kind)}</td>
              <td data-label="条件">{ruleCondition(r, taskList)}</td>
              <td data-label="作用域">{r.allNodes ? "全部节点" : r.selectorTags.length ? `标签：${r.selectorTags.join(" ∩ ")}（当前 ${r.nodeIds.length}）` : r.nodeIds.length ? <span title={r.nodeIds.map(nodeName).join("、")}>{r.nodeIds.length} 个指定节点</span> : <span className="muted">无节点</span>}</td>
              <td data-label="通知渠道">{r.channelIds.map(channelName).join("、") || <span className="muted">只记事件</span>}</td>
              <td data-label="启用"><input type="checkbox" role="switch" className="switch" aria-label={`启用 ${label}`} checked={r.enabled} disabled={busy} aria-busy={toggling === r.id || undefined} onChange={() => toggleEnabled(r)} /></td>
              <td data-label="状态">{r.enabled ? <RuleState enabled states={byRule.get(r.id)} nodeName={nodeName} /> : <span className="muted">—</span>}</td>
              <td data-column="actions"><RowMenu label={label} items={[
                { label: "编辑", disabled: busy, onSelect: (trigger) => { update.reset(); setDrawer({ kind: "edit", rule: r, opener: trigger }); } },
                { label: "删除", danger: true, confirm: `确认删除 ${label}`, note: "事件记录保留", disabled: busy || remove.isPending, onSelect: () => remove.mutate({ id: r.id }) },
              ]} /></td>
            </tr>;
          })}</tbody>
        </table>
      </div>
      {rulesData.rules.length === 0 && <p className="muted">还没有告警规则。</p>}
      {rulesData.rules.length > 0 && shown.length === 0 && <p className="muted" role="status">没有触发中的规则。</p>}
      {drawer?.kind === "create" && <AlertRuleDrawer title="新建告警规则" submitLabel="创建" {...lists} initial={emptyDraft()} pending={create.isPending} error={create.error} opener={drawer.opener} onClose={() => setDrawer(null)}
        onSubmit={(d) => create.mutate({ rule: toRule(0n, d, nodeList, channelList) }, { onSuccess: () => setDrawer(null) })} />}
      {drawer?.kind === "edit" && <AlertRuleDrawer key={String(drawer.rule.id)} title={`编辑 ${withId(drawer.rule.name, drawer.rule.id)}`} submitLabel="保存" {...lists} initial={draftOf(drawer.rule)} pending={update.isPending} error={update.error} opener={drawer.opener} onClose={() => setDrawer(null)}
        onSubmit={(d) => update.mutate({ rule: toRule(drawer.rule.id, d, nodeList, channelList) }, { onSuccess: () => setDrawer(null) })} />}
    </section>
  );
```

`RuleForm` → `AlertRuleDrawer`：外层 `Drawer`，`form[aria-label=title]`，`div.modal-body` 内 `errorBanner(error)` + `fieldset.bare[disabled=pending]` 包原字段（`NodeSelector` 已是 `NodeAssignment noun="作用域"`），`footer.modal-footer` 「取消」「{submitLabel}」。`RuleState` 删掉 `!enabled` 分支（停用由调用方处理）。CSS：`.switch { appearance: none; width: 32px; height: 18px; border-radius: 999px; background: var(--line-strong); position: relative; cursor: pointer; } .switch::after { content: ""; position: absolute; top: 2px; left: 2px; width: 14px; height: 14px; border-radius: 50%; background: var(--card); transition: left .12s; } .switch:checked { background: var(--status-online); } .switch:checked::after { left: 16px; } .switch:disabled { opacity: .5; cursor: default; }`。

- [ ] **Step 4: 跑测试确认绿；全量**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/AlertRules.test.tsx src/pages/AlertRulesResource.test.tsx > /tmp/c3-green.log 2>&1; echo $?` → 0；全量 → 0；`pnpm typecheck` → 0。

- [ ] **Step 5: 缺陷注入**

(a) `toggleEnabled` 只发 `{ id, enabled }`：开关用例红在 `name: ruleA.name`。(b) `firingOnly` 直接 `params.get("state") !== null`：筛选用例红在 `bogus` 分支。恢复后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/pages/AlertRules.tsx web/src/pages/AlertRules.test.tsx web/src/pages/AlertRulesResource.test.tsx web/src/admin.css && git commit -m "feat(web): 告警规则改为页头、触发中筛选、行上启用开关与右侧抽屉"
```

### Task 4: 告警事件：筛选行与新列

**Files:**
- Modify: `web/src/components/EventFeed.tsx`、`web/src/pages/AlertEvents.tsx`、`web/src/pages/AlertEvents.test.tsx`

**Interfaces:**
- Consumes: `EventFeed / useAlertEvents`（计划一 Task 11）、`listAlertRules`（规则名）、`TRANSITIONS`。
- Produces:
  ```ts
  export function EventFeed(props: { events: ReturnType<typeof useAlertEvents>; nodeName: (id: bigint) => string; channelName: (id: bigint) => string; ruleName?: (id: bigint) => string; visible?: (ev: AlertEvent) => boolean }): JSX.Element;
  //   列：时间 | 节点 | 规则 | 变化 | 观测值 | 投递。nodeId === 0n 的系统事件节点列「—」；ruleId === 0n 规则列「—」；ruleName 缺省时规则列显示「规则 #id」。
  //   变化格：transitionLabel + 下一行 small.muted 摘要（summary）。观测值：value（number）按规则种类无单位地 toLocaleString，0 且 transition 不是 firing/recovered 时「—」。
  //   visible 过滤已加载的行；过滤后为空而总行数 > 0 时文案「已加载的 N 条里没有匹配的事件」，且「加载更早的事件」按钮照常。
  export type EventFilters = { ruleId: bigint | null; transition: string | null; from: string; to: string };   // from/to 是 yyyy-mm-dd（本地日），空串不限
  export function filtersFromParams(params: URLSearchParams): EventFilters;  // rule=数字、transition=TRANSITIONS 的键、from/to=日期；非法忽略
  export function paramsWithFilters(params: URLSearchParams, f: EventFilters): URLSearchParams;
  export function matchesFilters(ev: AlertEvent, f: EventFilters): boolean;
  ```
  页面 DOM：`PageHeader title="告警事件"`；`.filter-row`：`label` 「节点」`select`（不变）、`label` 「规则」`select`（全部规则 + 规则名 `withId`）、`label` 「变化」`select`（全部 + `TRANSITIONS`）、`input[type=date][aria-label=起始日期]`、`input[type=date][aria-label=结束日期]`、有筛选时 `button` 「清除筛选」。节点筛选仍是服务端（URL `node=`）；其余三项客户端（URL `rule / transition / from / to`）。

- [ ] **Step 1: 改既有测试的期望并加新用例（红）**

| 旧 | 新 |
|---|---|
| 「零节点的系统事件显示系统与登录、备份结果」中节点列「系统」 | 节点列「—」、规则列「—」；变化列文字不变 |
| 表头断言（若有） | 「时间 节点 规则 变化 观测值 投递」 |
| 摘要在独立列 | 摘要在变化列的 `small` 里，`getByText(summary)` 仍可用 |

新增：

```tsx
it("规则、变化与日期筛选只作用于已加载的行，筛空时保留加载更早；URL 回写且非法值忽略", async () => {
  const events = [
    { id: 3n, ruleId: 1n, nodeId: 1n, transition: "firing", at: 1_760_000_000n, summary: "A 触发", value: 95.5, deliveries: [], silenced: false },
    { id: 2n, ruleId: 2n, nodeId: 1n, transition: "recovered", at: 1_759_900_000n, summary: "B 恢复", value: 10, deliveries: [], silenced: false },
    ...Array.from({ length: 98 }, (_, i) => ({ id: BigInt(100 - i), ruleId: 2n, nodeId: 1n, transition: "firing", at: 1_759_000_000n - BigInt(i), summary: `x${i}`, value: 0, deliveries: [], silenced: false })),
  ];
  const { router } = renderWithAdmin({
    listNodes: async () => ({ nodes: [{ id: 1n, name: "a" }] }), listNotifyChannels: async () => ({ channels: [] }),
    listAlertRules: async () => ({ rules: [{ id: 1n, name: "cpu" }, { id: 2n, name: "mem" }], states: [] }),
    listAlertEvents: async () => ({ events }),
  }, [{ path: "/events", Component: AlertEvents }], "/events?transition=bogus");
  await screen.findByText("A 触发");
  expect(screen.getByRole("combobox", { name: "变化" })).toHaveValue("");
  fireEvent.change(screen.getByRole("combobox", { name: "规则" }), { target: { value: "1" } });
  expect(router.state.location.search).toBe("?rule=1");
  expect(screen.getAllByRole("row")).toHaveLength(2);
  expect(within(screen.getAllByRole("row")[1]).getAllByRole("cell").map((c) => c.textContent)).toEqual([expect.any(String), "a", "cpu（#1）", "触发A 触发", "95.5", "未配置渠道"]);
  fireEvent.change(screen.getByRole("combobox", { name: "变化" }), { target: { value: "recovered" } });
  expect(screen.getByRole("status")).toHaveTextContent("已加载的 100 条里没有匹配的事件");
  expect(screen.getByRole("button", { name: "加载更早的事件" })).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "清除筛选" }));
  expect(router.state.location.search).toBe("");
  expect(screen.getAllByRole("row")).toHaveLength(101);
});
```

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/AlertEvents.test.tsx > /tmp/c4-red.log 2>&1; echo $?` → 1

- [ ] **Step 3: 实现**

`EventFeed.tsx`：加 `filtersFromParams / paramsWithFilters / matchesFilters`：

```ts
export type EventFilters = { ruleId: bigint | null; transition: string | null; from: string; to: string };
const DATE = /^\d{4}-\d{2}-\d{2}$/;
export function filtersFromParams(params: URLSearchParams): EventFilters {
  const rule = params.get("rule"); const transition = params.get("transition");
  return {
    ruleId: rule !== null && /^[1-9]\d*$/.test(rule) ? BigInt(rule) : null,
    transition: transition !== null && transition in TRANSITIONS ? transition : null,
    from: DATE.test(params.get("from") ?? "") ? params.get("from")! : "",
    to: DATE.test(params.get("to") ?? "") ? params.get("to")! : "",
  };
}
export function paramsWithFilters(params: URLSearchParams, f: EventFilters): URLSearchParams {
  const next = new URLSearchParams(params);
  for (const [k, v] of [["rule", f.ruleId === null ? "" : String(f.ruleId)], ["transition", f.transition ?? ""], ["from", f.from], ["to", f.to]] as const) { if (v) next.set(k, v); else next.delete(k); }
  return next;
}
// 日期是本地日：from 含当天 00:00，to 含当天 23:59:59（用次日 00:00 作开区间上界）。
const dayStart = (ymd: string) => new Date(`${ymd}T00:00:00`).getTime() / 1000;
export function matchesFilters(ev: AlertEvent, f: EventFilters): boolean {
  if (f.ruleId !== null && ev.ruleId !== f.ruleId) return false;
  if (f.transition !== null && ev.transition !== f.transition) return false;
  const at = Number(ev.at);
  if (f.from && at < dayStart(f.from)) return false;
  if (f.to && at >= dayStart(f.to) + 86_400) return false;
  return true;
}
```

`EventList` 的表：

```tsx
<thead><tr><th>时间</th><th>节点</th><th>规则</th><th>变化</th><th>观测值</th><th>投递</th></tr></thead>
<tbody>{shown.map((ev) => (
  <tr key={String(ev.id)}>
    <td className="num">{new Date(Number(ev.at) * 1000).toLocaleString()}</td>
    <td>{ev.nodeId === 0n ? <span className="muted">—</span> : nodeName(ev.nodeId)}</td>
    <td>{ev.ruleId === 0n ? <span className="muted">—</span> : ruleName(ev.ruleId)}</td>
    <td className={alarming(ev.transition) ? "error" : undefined}>{transitionLabel(ev.transition)}<small className="muted">{ev.summary}</small></td>
    <td className="num">{ev.transition === "firing" || ev.transition === "recovered" ? ev.value.toLocaleString() : <span className="muted">—</span>}</td>
    <td>{/* 原「通知」格内容原样 */}</td>
  </tr>
))}</tbody>
```

`shown = visible ? rows.filter(visible) : rows`；空态：`rows.length === 0 ? "没有告警事件。" : shown.length === 0 && <p className="muted" role="status">已加载的 {rows.length} 条里没有匹配的事件。</p>`。`ruleName` 缺省 `(id) => \`规则 #${id}\``。

`AlertEvents.tsx`：加 `rules = useQuery(listAlertRules)`（名字用，失败只进横幅）、`filters = filtersFromParams(params)`、`setFilters = (f) => setParams(paramsWithFilters(params, f), { replace: true })`（保留 `node=`）；`PageHeader title="告警事件"`；`.filter-row` 里四个控件 + 「清除筛选」（同时清节点）；`<EventFeed events={events} nodeName={nodeName} channelName={channelName} ruleName={(id) => { const r = rules.data?.rules.find((x) => x.id === id); return r ? withId(r.name, r.id) : \`规则 #${id}\`; }} visible={(ev) => matchesFilters(ev, filters)} />`；`errorBanner(nodes.error, events.error, channels.error, rules.error)`。

- [ ] **Step 4: 跑测试确认绿；全量**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/AlertEvents.test.tsx src/pages/NodeDetail.test.tsx > /tmp/c4-green.log 2>&1; echo $?` → 0；全量 → 0。

- [ ] **Step 5: 缺陷注入**

(a) `matchesFilters` 的 `to` 用 `>` 而不是 `>= +86400`（即当天不含）：加一条 `to=1760-…` 的日期断言后红；(b) 筛空时渲染「没有告警事件。」：新用例红在 `status` 文案。恢复后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/components/EventFeed.tsx web/src/pages/AlertEvents.tsx web/src/pages/AlertEvents.test.tsx && git commit -m "feat(web): 告警事件表加规则与观测值列，按规则、变化、日期在已加载页上筛选"
```

### Task 5: 维护静默与通知渠道：页头、表格、抽屉

**Files:**
- Modify: `web/src/pages/Silences.tsx`、`web/src/pages/Silences.test.tsx`
- Modify: `web/src/pages/Channels.tsx`、`web/src/pages/Channels.test.tsx`

**Interfaces:**
- Consumes: `Drawer`、`RowMenu`、`PageHeader`、`NodeAssignment`、`deleteNote`（渠道，原函数不动）。
- Produces: 静默——`PageHeader title="维护静默"`（说明段落移到 `description`），主按钮 「新建维护静默」；表头 `名称 | 窗口 | 作用域 | 状态 | 操作(sr-only)`；作用域格文案同告警（「全部节点」/「标签：…（当前 N）」/「N 个指定节点」）；`RowMenu`：编辑、删除（`note: 已产生的告警事件保留`）；`SilenceDrawer({ title, submitLabel, nodes, initial, pending, error, opener, onClose, onSubmit })` 字段同原 `SilenceForm`。渠道——`PageHeader title="通知渠道"`，主按钮 「新建通知渠道」；`LoginNotifications` 卡原样放在表格之上；表头 `名称 | 类型 | 目标 | 节奏上限 | 创建于 | 操作(sr-only)`；`RowMenu`：编辑、发送测试（`disabled: testing`）、删除（`confirm`，`note: deleteNote(c.id, current)`）；`ChannelDrawer({ title, submitLabel, initial, original?, pending, error, opener, onClose, onSubmit })` 字段同原 `ChannelForm`，Telegram / Webhook 用 `div.segmented[role=radiogroup][aria-label=类型]` 两个 `radio`（`CHANNEL_KINDS` 的 label）。`notice`（测试成功提示）仍在页面的 `p[role=status]`。

- [ ] **Step 1: 改既有测试的期望（红）**

Run: `cd /Users/xjetry/work/vibe/probe/web && grep -n "新建维护静默\|新建通知渠道\|name: \`编辑 \|name: \`删除 \|name: \`发送测试\|确认删除\|getByRole(\"form\"\|combobox\|getByLabelText(\"类型\")\|全部节点（含\|动态标签选择器" src/pages/Silences.test.tsx src/pages/Channels.test.tsx > /tmp/c5-consumers.log 2>&1; echo $?`

| 旧 | 新 |
|---|---|
| 常驻 `form` 「新建维护静默」/「新建通知渠道」 | `openCreate()` |
| `编辑 X` / `发送测试 X` / `删除 X` | `openRowAction(label, …)`；删除确认 `menuitem` 「确认删除 X」，附注 `getByText(note)` 在武装后出现 |
| 渠道类型 `combobox` | `radio` 「Telegram」/「Webhook」（`CHANNEL_KINDS` 的 label 原文） |
| 「一行保存挂起时其它行的保存禁用」「编辑态在刷新完成后才关闭」 | 同 Task 2 的写法 |
| 「设置没读到时删除确认照最坏的情况提醒」等四条附注用例 | 武装后 `getByText(/…/)`；文案不变 |
| 「删除首击不发请求」「编辑往返撤销已武装的删除确认」 | 首击 `menuitem` 「删除 X」后 `deleteNotifyChannel` 未被调用；打开编辑抽屉（菜单关闭）再回来，武装已撤销 |

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/Silences.test.tsx src/pages/Channels.test.tsx > /tmp/c5-red.log 2>&1; echo $?` → 1

- [ ] **Step 3: 实现**

两页按 Task 2 / 3 的同一模式：`drawer` 状态、`busy`、`PageHeader`、`RowMenu`、`XDrawer = Drawer + form[aria-label=title] + .modal-body(errorBanner + fieldset.bare) + .modal-footer`。渠道的 `ChannelDrawer` 把 `kind` 的 `select` 换成 `.segmented` 单选（`name="channel-kind"`）。渠道删除项：`{ label: "删除", danger: true, confirm: \`确认删除 ${label}\`, note: deleteNote(c.id, current), disabled: busy || remove.isPending, onSelect: () => remove.mutate({ id: c.id }) }`。静默页的说明段落进 `PageHeader description`。

- [ ] **Step 4: 跑测试确认绿；全量**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/Silences.test.tsx src/pages/Channels.test.tsx > /tmp/c5-green.log 2>&1; echo $?` → 0；全量 → 0；`pnpm typecheck` → 0。

- [ ] **Step 5: 缺陷注入**

渠道删除项不传 `note`：「删除登录通知唯一的接收渠道时，确认写明登录通知会关闭」红。恢复后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/pages/Silences.tsx web/src/pages/Silences.test.tsx web/src/pages/Channels.tsx web/src/pages/Channels.test.tsx && git commit -m "feat(web): 维护静默与通知渠道改为页头、表格与右侧抽屉，删除附注随菜单内确认显示"
```

---

## 里程碑 D：系统页与登录

### Task 6: 在线更新：hub 卡与 agent 表

**Files:**
- Modify: `web/src/pages/Updates.tsx`、`web/src/pages/Updates.test.tsx`、`web/src/admin.css`

**Interfaces:**
- Produces: `PageHeader title="在线更新" description="官方正式发行版 · Linux systemd"`，actions 「检查官方新版本」；`section.hub-card[aria-label=Hub]`：`dl` 三项（「当前版本」`hub.version || 未知`、「官方最新正式版」`latest || 尚未检查`、「绑定的 agent 版本」`boundAgentVersion || —`）+ `Progress`（不含来源）+ `button` 「更新 Hub」+ 两段说明；提交进度区不变；节点段 `PageHeader`-同级的 `h2` 「节点 Agent」+ 说明 + `button` 「更新选中节点（N）」；表头 `全选 | 节点 | 当前版本 | 来源 | 更新状态 | 操作(sr-only)`；版本格 `code` + 落后徽章 `span.badge-attention` 「低于 {nodeTarget}」（`olderThan(status.version, nodeTarget)` 且 `nodeTarget` 非空）；来源格 `sourceLabels[status.source] ?? status.source ?? "—"`；`Progress` 去掉 `source` 的渲染。确认框仍是 `Modal`。

- [ ] **Step 1: 改既有测试的期望并加新用例（红）**

「显示每个节点取产物的来源」：来源文字改在「来源」列（`within(row).getAllByRole("cell")[3]`）。新增：

```tsx
it("版本列对低于绑定版本的节点标落后徽章，已到目标与绑定非正式版时不标", async () => {
  render({ ...impl, getUpdates: async () => ({ latestVersion: "", boundAgentVersion: "v0.8.0", targets: [{ nodeId: 1n, status: { supported: true, version: "v0.7.0" } }, { nodeId: 2n, status: { supported: true, version: "v0.8.0" } }] }) });
  const rows = await screen.findAllByRole("row");
  expect(within(rows[1]).getByText("低于 v0.8.0")).toHaveClass("badge-attention");
  expect(within(rows[2]).queryByText(/^低于/)).not.toBeInTheDocument();
  expect(within(screen.getByRole("region", { name: "Hub" })).getByText("绑定的 agent 版本").nextElementSibling).toHaveTextContent("v0.8.0");
});
```

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/Updates.test.tsx > /tmp/d6-red.log 2>&1; echo $?` → 1

- [ ] **Step 3: 实现**

按 Interfaces 改 JSX；`.update-summary*` 删掉，`.hub-card { display: grid; grid-template-columns: 1fr auto; gap: 8px 24px; align-items: start; padding: 16px; border: 1px solid var(--line); border-radius: var(--radius-card); background: var(--card); margin-bottom: 20px; } .hub-card dl { display: grid; grid-template-columns: repeat(3, auto); gap: 4px 24px; margin: 0; } .hub-card dt { font-size: 12px; color: var(--muted); } .hub-card dd { margin: 0; font-size: 18px; font-weight: 600; } @media (max-width: 900px) { .hub-card, .hub-card dl { grid-template-columns: 1fr; } }`。

- [ ] **Step 4: 跑测试确认绿**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/Updates.test.tsx > /tmp/d6-green.log 2>&1; echo $?` → 0

- [ ] **Step 5: 缺陷注入**

徽章条件改成 `olderThan(status.version, latest)`：新用例红（`latest` 为空）。恢复后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/pages/Updates.tsx web/src/pages/Updates.test.tsx web/src/admin.css && git commit -m "feat(web): 在线更新改为 hub 卡与带落后徽章、来源列的 agent 表"
```

### Task 7: 安全：三张卡与会话表

**Files:**
- Modify: `web/src/pages/Sessions.tsx`、`web/src/pages/Sessions.test.tsx`、`web/src/pages/Security.tsx`、`web/src/pages/Security.test.tsx`

**Interfaces:**
- Produces: `/security`（`Sessions`）：`PageHeader title="安全"`；`div.cards-3` 三张 `article.card`：「密码」（说明「密码由 hub 的启动配置提供；认证设备全部丢失时由运维者在 hub 主机运行安全重置命令。」）、「TOTP」（`getSecurity`：已启用 + 剩余 N 个恢复码 / 未启用；链接 「管理 TOTP、恢复码与 Passkey」→ `/security/credentials`）、「Passkey」（已注册 N 个；已绑定来源 `origin || 尚未绑定`；同一链接）；会话表头 `会话 | 创建于 | 最近使用 | 操作(sr-only)`，`RowMenu label={session.id.slice(0,12)}`：撤销会话（`danger`，`confirm: 确认撤销会话 {id12}`，`note: 当前 ? 将退出当前登录 : 该会话将立即失效`）。`getSecurity` 失败只让两张卡显示「读取失败」文案，不影响会话表。`/security/credentials`（`Security`）：`PageHeader title="账户安全"`，三个 `fieldset` 加 `className="card"`，其余不动。

- [ ] **Step 1: 改既有测试的期望并加新用例（红）**

`Sessions.test.tsx`：「撤销其它会话先确认再按 hash 调用并刷新列表」「撤销当前会话清空缓存后跳转登录页」「撤销失败保留页面并显示服务器错误」改走 `openRowAction(id12, "撤销会话")` → `menuitem` 「确认撤销会话 …」。新增：

```tsx
it("三张卡：TOTP 与 Passkey 读 getSecurity，读取失败只影响卡片，会话表照常", async () => {
  renderWithAdmin({ listSessions: async () => ({ sessions: [{ id: "abcdef123456xyz", createdAt: 1n, lastUsedAt: 2n, current: true }] }), getSecurity: async () => ({ totpEnabled: true, recoveryCodesRemaining: 7, passkeys: [{ id: "k1", name: "key" }], origin: "https://h.example", currentOrigin: "https://h.example", passkeyAvailable: true }) }, [{ path: "/security", Component: Sessions }], "/security");
  expect(await screen.findByRole("article", { name: "TOTP" })).toHaveTextContent("已启用，剩余 7 个恢复码");
  expect(screen.getByRole("article", { name: "Passkey" })).toHaveTextContent("已注册 1 个");
  expect(screen.getAllByRole("link", { name: "管理 TOTP、恢复码与 Passkey" })[0]).toHaveAttribute("href", "/security/credentials");
  expect(screen.getByRole("row", { name: /abcdef123456/ })).toBeInTheDocument();
});
```

（`article` 用 `aria-label` 命名。）

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/Sessions.test.tsx src/pages/Security.test.tsx > /tmp/d7-red.log 2>&1; echo $?` → 1

- [ ] **Step 3: 实现**

按 Interfaces；`.cards-3 { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; margin-bottom: 20px; } @media (max-width: 900px) { .cards-3 { grid-template-columns: 1fr; } }`。

- [ ] **Step 4: 跑测试确认绿**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/Sessions.test.tsx src/pages/Security.test.tsx > /tmp/d7-green.log 2>&1; echo $?` → 0

- [ ] **Step 5: 缺陷注入**

`getSecurity` 失败时整页返回错误：新用例改 `getSecurity` 抛错后应仍能找到会话行；注入后红。恢复后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/pages/Sessions.tsx web/src/pages/Sessions.test.tsx web/src/pages/Security.tsx web/src/pages/Security.test.tsx web/src/admin.css && git commit -m "feat(web): 安全页改为密码 / TOTP / Passkey 三张卡与会话表，撤销走行菜单"
```

### Task 8: API token：抽屉创建与行菜单

**Files:**
- Modify: `web/src/pages/ApiTokens.tsx`、`web/src/pages/ApiTokens.test.tsx`

**Interfaces:**
- Produces: `PageHeader title="API token"`（两段说明进 `description` 与页头下的 `p.muted`），actions 「下载入口卡片」「新建 API token」；`ApiTokenDrawer({ opener, pending, error, nodes, nodesError, onClose, onCreate, created })`：`Drawer` 标题「新建 API token」，内 `form[aria-label=新建 API token]`（字段与可访问名同原表单：名称、允许的写操作 `Picks`、节点范围 `select`、授权节点 `Picks`）；创建成功后同一抽屉切到 `created` 态：`Secret` + 可选 `UserscriptButton` + 「完成」。`UserscriptButton`（不带 token 的）留在页头下方。表头 `名称 | 权限 / 范围 | 创建于 | 最后使用 | 操作(sr-only)`；`RowMenu label=withId(name,id)`：查看操作记录（`onSelect` → `setAuditOwner`）、吊销（`danger`，`confirm: 确认吊销 X`，`note: 用它的请求立即失效`）。`Operations` 区域不变。

- [ ] **Step 1: 改既有测试的期望（红）**

| 旧 | 新 |
|---|---|
| 常驻 `form` 「新建 API token」 | `openCreate()`（点「新建 API token」） |
| 「创建后只显示一次明文并刷新列表」 | 明文在 `dialog` 内；点「完成」关闭；列表已刷新 |
| 「吊销需要确认…」「同名 token 的吊销按钮按 id 区分」「吊销卡片所属 token 时清掉明文」 | `openRowAction(label, "吊销")` → `menuitem` 「确认吊销 X」；明文卡在抽屉里时吊销需先关抽屉——改为：创建后关掉抽屉，明文不再保留（抽屉关闭即丢弃），该用例改断「关闭抽屉后明文不在页面上」 |
| `button` 「查看 X 操作记录」 | `openRowAction(label, "查看操作记录")` |

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/ApiTokens.test.tsx > /tmp/d8-red.log 2>&1; echo $?` → 1

- [ ] **Step 3: 实现**

把表单与 `secret` 状态搬进 `ApiTokenDrawer`（草稿属于抽屉；关闭即丢弃明文——hub 只存哈希，关闭后不能恢复，和节点凭据同一口径）；页面只留 `drawerOpener` 状态、`create / remove / reference` mutation、表格与 `Operations`。

- [ ] **Step 4: 跑测试确认绿**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/ApiTokens.test.tsx > /tmp/d8-green.log 2>&1; echo $?` → 0

- [ ] **Step 5: 缺陷注入**

创建成功后 `onClose()` 而不是切 `created` 态：「创建后只显示一次明文」红。恢复后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/pages/ApiTokens.tsx web/src/pages/ApiTokens.test.tsx && git commit -m "feat(web): API token 在抽屉里创建并一次性显示明文，吊销与操作记录走行菜单"
```

### Task 9: 注册窗口、存储、外观、主题、心跳：页头与模板

**Files:**
- Modify: `web/src/pages/RegisterWindow.tsx`、`web/src/pages/RegisterWindow.test.tsx`
- Modify: `web/src/pages/Storage.tsx`、`web/src/pages/Appearance.tsx`、`web/src/pages/Themes.tsx`、`web/src/pages/HeartbeatSettings.tsx`、`web/src/components/BackupSettingsForm.tsx`、`web/src/components/BackupStatus.tsx`（只换页头 / 卡片类名）
- Modify: `web/src/admin.css`

**Interfaces:**
- Produces: 注册窗口——`PageHeader title="注册窗口"`，actions `button.primary-button` 「开启新窗口」（窗口开启中时 `disabled`）；`section.card[aria-label=窗口状态]`：开启中「剩余 N 个名额，截止 …」+ `button.danger` 「关闭窗口」/「当前没有开启的窗口。」；key 与 `InstallCommands` 在状态卡下方（不变）；`RegisterWindowDrawer({ opener, pending, error, onClose, onOpen })`：有效期 `select`、可注册节点数 `input`、`button` 「开启」（`disabled={pending || !(maxNodes >= 1)}`）。其它四页：`h1` → `PageHeader title=…`；`form.card.edit-form` → `form.card`（`.edit-form` 规则删除后由通用 `.card` 承担）。

- [ ] **Step 1: 改既有测试的期望（红）**

`RegisterWindow.test.tsx`：所有点「开启新窗口」提交的地方改为 `openDrawer()` + 点 `dialog` 内「开启」；「名额为 '%s' 时禁用开窗」断 `dialog` 内「开启」禁用；「状态挂起时显示加载中，不渲染开窗表单」改为「不渲染页头主按钮」。其它四页的测试不依赖 `h1` 以外的结构，跑一遍确认。

- [ ] **Step 2: 跑测试确认红**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/RegisterWindow.test.tsx > /tmp/d9-red.log 2>&1; echo $?` → 1

- [ ] **Step 3: 实现**

按 Interfaces。`admin.css`：删 `.admin-shell .edit-form*`、`.page-heading*`；`.admin-shell .card` 的 `border-radius` 改 `var(--radius-card)`、`padding: 16px`。

- [ ] **Step 4: 跑测试确认绿；全量**

Run: `cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/RegisterWindow.test.tsx src/pages/Storage.test.tsx src/pages/Appearance.test.tsx src/pages/Themes.test.tsx src/pages/HeartbeatSettings.test.tsx src/pages/SettingsForms.test.tsx src/components/BackupSettingsForm.test.tsx src/components/BackupStatus.test.tsx > /tmp/d9-green.log 2>&1; echo $?` → 0；全量 → 0。

- [ ] **Step 5: 缺陷注入**

开窗成功后不关抽屉：「开窗后展示 key 与安装命令」改断 `dialog` 消失后红。恢复后绿。

- [ ] **Step 6: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/pages/RegisterWindow.tsx web/src/pages/RegisterWindow.test.tsx web/src/pages/Storage.tsx web/src/pages/Appearance.tsx web/src/pages/Themes.tsx web/src/pages/HeartbeatSettings.tsx web/src/components/BackupSettingsForm.tsx web/src/components/BackupStatus.tsx web/src/admin.css && git commit -m "feat(web): 注册窗口改为页头与开窗抽屉，系统各页统一页头与卡片"
```

### Task 10: 登录页

**Files:**
- Modify: `web/src/pages/Login.tsx`、`web/src/pages/Login.test.tsx`、`web/src/styles.css`

**Interfaces:**
- Produces: `main.login > form.login-card`：`h1.brand`（`HeronMark` + Heron）、`p.login-description` 「轻量自托管主机监控」、`label` 「管理员密码」、`label` 「动态验证码（已启用 TOTP 时必填）」、`details` 「使用一次性恢复码」、`button.primary-button[type=submit]` 「登录」、`button` 「使用 Passkey 登录」（次级，`passkey-button` 类，前置 `Icon name="key"`）、错误 `p[role=alert]` 在表单内（不变）。CSS：卡片 `width: min(360px, 100%)`，`padding: 24px`，`gap: 12px`，输入与按钮占满宽，两个按钮同高 `--control-h`。

- [ ] **Step 1: 测试**

既有 `Login.test.tsx` 六条不改；加一条：

```tsx
it("登录按钮是主按钮，Passkey 入口是次级按钮，两者都在表单内", () => {
  renderWithAdmin({}, [{ path: "/login", Component: Login }], "/login");
  const form = screen.getByRole("button", { name: "登录" }).closest("form")!;
  expect(form).toHaveClass("login-card");
  expect(screen.getByRole("button", { name: "登录" })).toHaveClass("primary-button");
  expect(within(form).getByRole("button", { name: "使用 Passkey 登录" })).toHaveClass("passkey-button");
});
```

- [ ] **Step 2: 红 → 实现 → 绿**

Run 红：`cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run src/pages/Login.test.tsx > /tmp/d10-red.log 2>&1; echo $?` → 1。实现按 Interfaces；`styles.css` 的 `.login .card` 规则改为 `.login-card`。Run 绿 → 0。

- [ ] **Step 3: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/pages/Login.tsx web/src/pages/Login.test.tsx web/src/styles.css && git commit -m "feat(web): 登录页居中卡片，登录为主按钮、Passkey 为次级入口"
```

---

## 验收

### Task 11: e2e 跟上并截图

**Files:**
- Modify: `web/e2e/agentic-write.spec.ts`（28：先 `await page.getByRole("button", { name: "新建 API token" }).click()`；71：`更多操作 {name}（#id）` → `menuitem` 「查看操作记录 …」；81–82：菜单内 `吊销 …` → `确认吊销 …`）
- Modify: `web/e2e/admin-ui.spec.ts`（12–35 注册窗口：`开启新窗口` 后在 `dialog` 内点 `开启`；297–311 不变；157–168 的路由巡检不变，追加对 `/admin/probes`、`/admin/alerts`、`/admin/channels` 各打开一次「新建 …」抽屉的截图 `probes-drawer-mobile.png` 等，Escape 关闭）
- `web/e2e/theme-sandbox.spec.ts` 不改，必须仍绿

- [ ] **Step 1: 改 spec**

按上表。

- [ ] **Step 2: 跑 e2e**

Run: `cd /Users/xjetry/work/vibe/probe && make web-e2e > /tmp/d11-e2e.log 2>&1; echo $?` → 0。看截图：抽屉从右侧出且手机下占满宽、分配三张卡在手机下单列、事件表筛选行不溢出。问题按根因修在组件 / CSS。

- [ ] **Step 3: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/e2e/agentic-write.spec.ts web/e2e/admin-ui.spec.ts && git commit -m "test(web): e2e 跟上 API token 抽屉、注册窗口抽屉与各列表页的行菜单"
```

### Task 12: 全量验证、死样式与验收记录

- [ ] **Step 1: 全量**

```bash
cd /Users/xjetry/work/vibe/probe/web && pnpm typecheck > /tmp/d12-tsc.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe/web && pnpm build > /tmp/d12-build.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe/web && pnpm vitest run > /tmp/d12-unit.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe && make web-e2e > /tmp/d12-e2e.log 2>&1; echo $?
```

四个都要 0（`web/package.json` 没有 lint 脚本，类型与构建由 typecheck 与 build 把关）。

- [ ] **Step 2: 死样式与死代码**

```bash
cd /Users/xjetry/work/vibe/probe/web && grep -o "^\.[a-zA-Z][a-zA-Z0-9_-]*" src/admin.css src/styles.css | sed 's/^[^:]*://' | sort -u | while read -r cls; do n=${cls#.}; if ! grep -rq -- "$n" src --include='*.tsx' --include='*.ts'; then echo "$cls"; fi; done > /tmp/d12-dead.log 2>&1; echo $?; cat /tmp/d12-dead.log
cd /Users/xjetry/work/vibe/probe/web && grep -rn "NodeSelector\|ConfirmDelete" src --include='*.tsx' --include='*.ts' > /tmp/d12-refs.log 2>&1; echo $?; cat /tmp/d12-refs.log
```

先用 `.zz-unused{}` 冒烟。`ConfirmDelete` 若已无消费者（节点、探测、告警、静默、渠道、会话、token、标签管理——标签管理 `TagManager` 仍用它，则保留），按实际 grep 结果决定删除与否并在验收记录写明。

- [ ] **Step 3: 验收记录**

在本计划末尾追加 `## 验收记录`：每个 Task 的 commit hash、四条命令退出码、e2e 截图路径、死样式清理清单、消费面清单之外新发现的断言位置。

- [ ] **Step 4: Commit**

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/admin.css web/src/styles.css web/src/components docs/superpowers/plans/2026-10-08-web-redesign-admin-probes-system.md && git commit -m "style(web): 清理改版后的无引用样式与组件；记录探测、告警与系统页改版的验收"
```

---

## 自审记录

- 设计 §4.4：Task 1（三张卡片式单选、按标签快选、可搜索多选、已选列表与计数、口径句原文）、Task 2（列：类型、目标、间隔、超时、分配摘要三种写法、⋯ 四项；抽屉类型分段）。
- §4.5：Task 3（列：名称、类型、条件、作用域、通知渠道、启用开关、⋯；抽屉按类型切条件字段；无渠道提示「规则只记录事件」保留原句）、Task 4（列：时间、节点、规则、变化、观测值、投递；系统事件节点「—」；顶部四项筛选；底部加载更早）、Task 5（静默与渠道同为表格 + 抽屉；渠道按 Telegram / Webhook 切字段；发送测试在菜单）。
- §4.6：Task 6（hub 卡三值 + 检查更新；agent 表版本与落后徽章、来源、状态含进度与失败原因、操作；全选批量不变）、Task 7（会话表 + 三张卡）、Task 8 / 9（API token、注册窗口、存储、外观、主题套页头 + 卡片 / 抽屉模板）、Task 10（登录居中卡片，密码 + 动态验证码 / 恢复码 + Passkey，错误在表单内）。
- §4.7 手机版：抽屉占满宽（计划一 CSS）、分配单列（Task 1）、表格降级沿用计划一的 `td[data-label]`。
- Review Focus 五条的归属：抽屉存活于记录被删（Task 2 新用例，Task 3 / 5 同模式）、已不存在的已选节点（Task 1 第 3 条）、筛空保留加载更早（Task 4 新用例）、启用开关三态（Task 3 新用例）、开窗抽屉关闭后命令在页面（Task 9 Step 5 的断言）。
- 类型一致性：`NodeSelection` 形状不变，`ProbeTasks / AlertRules / Silences` 的 `Draft` 继续 `NodeSelection & {...}`；`RowMenuItem.note` 在 Task 1 加入、Task 2 / 3 / 5 / 7 / 8 使用；`EventFeed` 的新 prop 在 Task 4 定义，节点详情（计划一）不传即旧行为；`submitLabel` 由各 Drawer 的调用方传，不用 `configId` 推断。
- 与计划一的接缝：`openRowAction` helper 两边各自复制；`RowMenu` 的 DOM 契约（`更多操作 X`、`menuitem`、`取消`）不变，只加 `note`；计划一 Task 11 的 `EventFeed` 签名向后兼容。
- 占位扫描：Task 5 / 7 / 8 / 9 的实现步骤写的是「按 Task 2 / 3 的同一模式」加各自的 Interfaces 清单——模式代码在 Task 2 / 3 完整给出，字段清单在各自 Interfaces 里逐项列出，不是「类似 Task N」的空引用。
