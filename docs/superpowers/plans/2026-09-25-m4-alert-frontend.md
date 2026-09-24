# M4 告警（二）：前端——通知渠道、告警规则、告警事件与节点宽限期 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 管理面板能维护通知渠道（Telegram / Webhook，凭据只写不读，可发测试消息）、维护告警规则并看到每条规则当前触发与待定的节点、按节点翻阅告警事件及其投递结果，并能在节点页编辑离线宽限期。

**Architecture:** 纯前端新增，不动 proto、生成代码与 hub。三个新页面都复用探测任务页的范式：创建与编辑共用一个表单组件、行内编辑、两段式删除、`useLatestError` 汇总操作错误。节点与渠道的多选抽成一个共用组件，探测任务页同步改用它；枚举标签、规则条件描述、状态分组、渠道目标描述与投递文案集中在 `lib/alerts.ts`，页面只做装配。事件页用 connect-query 的 `useInfiniteQuery` 按 `before_id` 翻页，节点筛选放在 URL 查询串里，节点详情页可以直接链接过去。

**Tech Stack:** 与管理面板计划一致：pnpm、Vite、React 19、TypeScript、react-router 8、@connectrpc/connect-query 2.3、@tanstack/react-query 5、vitest + jsdom + testing-library。

**Spec:** `docs/superpowers/specs/2026-09-17-probe-architecture-design.md` §6.6（显式作用域）、§9（规则、状态机、通知）、§10（前端）、§12（从用户可见入口测）；`docs/guidelines/agent-first.md`。前置：`docs/superpowers/plans/2026-09-24-m4-alert-backend.md` 已合入 main（f80ecb2）。

## Global Constraints

**来自 spec 与已合入接口的硬值**

- 渠道凭据只写不读（§9.3）：Telegram bot token、Webhook URL 与全部头值从不出现在任何响应里。列表只显示 `chat_id`、`url_host`、方法与头名；编辑表单里这些输入恒为空，留空提交表示保留已存值（`SaveNotifyChannel` 注释）。token 与头值输入用 `type="password" autoComplete="off"`。
- 只有种类未变时 hub 才用已存凭据补全空值（`Engine.SaveChannel` 只在旧渠道同为 Telegram 时合并 token）；编辑中切换种类后，新种类的凭据输入必须 `required`。
- 删除请求头用 `remove_headers`（只列头名）；显式给出的头覆盖同名旧头（proto `WebhookConfig`）。头以 map 传输，同名两行会在序列化时合并成一行，hub 看不到重复，所以面板在提交前拒绝同名（不区分大小写）的两行——这是唯一能看到重复的地方，不是复制 hub 的规则。
- 规则作用域显式（§6.6）：`all_nodes` 为真时 `node_ids` 发空数组；为假时 `node_ids` 就是作用域，空集由 hub 拒绝，不等于全部节点。
- 探测规则的候选节点是作用域与任务分配节点的交集（proto `ListAlertRulesResponse.states` 注释），表单里说明这一点。
- 状态列表只含已有记录的组合，缺失按 ok 显示；停用规则的状态行由 hub 在保存时清除，面板显示"已停用"而不是"正常"。
- "已送达"只来自 `ok` 为真的投递记录（§9.3）；`done` 为假的投递仍在队列里，显示"投递中"而不是失败。
- `ListAlertEvents`：按 id 倒序，`before_id` 为 0 从最新开始，`limit` 0 取 100；`node_id` 为 0 表示全部节点。面板每页取 100，一页不足 100 条即到底。
- `UpdateNodeRequest.offline_grace_s` 必填：0 表示清除（取 `PROBE_OFFLINE_AFTER`），非 0 须不小于它；hub 的错误文本带单位与下限，面板原样展示。`Node.offline_grace_s` 缺失表示取默认。
- 事件摘要 `summary` 是事件发生时由 hub 写好的文字，已含节点名、规则名与观测值；面板直接显示它，不根据当前规则推断观测值的单位（规则可能在事件之后改了种类或已删除）。
- 字段约束由 hub 校验并返回带字段路径的 `InvalidArgument` 文本；前端只用原生约束（`required`、`min`、`max`、`maxLength`）挡住明显非法输入，服务端错误原文经 `errorText` 展示（agent-first：面板只是一个消费者，不引入专用端点）。

**沿用管理面板计划的取值**

- 文案简体中文直出；无 UI 框架；样式只加进 `web/src/styles.css`；错误一律 `<p role="alert" className="error">{errorText(err)}</p>`，成功提示用 `<p role="status">`。
- 危险操作两段式确认（`confirming` 状态切换按钮文案），不用 `window.confirm`。
- bigint 约定：React `key` 用 `String(id)`；请求体里的 id 保持 bigint；时间戳 `Number(ts) * 1000`。
- Query key 失效用 `createConnectQueryKey({ schema, cardinality: "finite" })`，只失效被改动的那张列表。
- 各行共用同一个 mutation observer：任一行保存挂起时禁用全部行的保存；`useMutation` 的 `onSuccess` 返回刷新 promise，编辑态在列表显示已保存值之后才关闭（与 `ProbeTasks.tsx` 相同）。

**代码与提交规范（来自用户全局规则，对执行者同样生效）**

- 注释、commit message 里禁止过程信息（任务 / 步骤编号、里程碑代号、审阅轮次、"按计划 / 简报 / 裁决"）。写 WHY 与不变式，前提指明由谁保证。
- 不打补丁；不复制第二份实现（节点与渠道多选共用 `Picks`；id 排序与集合切换共用 `lib/ids.ts`；节点编辑草稿只由一个函数构造）；不写 TODO；同形缺陷一次改全。
- 每条新断言先红后绿；注入前 `git diff --quiet` 确认落地；红要红在正确原因。
- 判成败的命令不接管道：`cmd > log 2>&1; echo $?`。
- 不修改 `proto/`、`gen/`、`web/src/gen/`、`internal/`、`cmd/`、`docs/`。

## 文件结构

```
web/src/lib/ids.ts(+.test.ts)              bigint id 升序、集合切换
web/src/lib/alerts.ts(+.test.ts)           枚举标签、规则条件、状态分组、渠道目标、投递文案、变化标签、宽限期文案
web/src/components/Picks.tsx(+.test.tsx)   节点 / 渠道多选（探测任务与告警规则共用）
web/src/pages/Channels.tsx(+.test.tsx)     通知渠道页
web/src/pages/AlertRules.tsx(+.test.tsx)   告警规则页
web/src/pages/AlertEvents.tsx(+.test.tsx)  告警事件页
web/src/pages/ProbeTasks.tsx               改用 Picks、ascending 与 .edit-form
web/src/pages/Nodes.tsx(+.test.tsx)        宽限期列与编辑
web/src/pages/NodeDetail.tsx(+.test.tsx)   链接到该节点的告警事件
web/src/components/Layout.tsx(+.test.tsx)  导航增加告警规则、告警事件、通知渠道
web/src/App.tsx                            路由 alerts、events、channels
web/src/styles.css                         .task-form/.node-picks 更名为通用的 .edit-form/.picks；label.inline；textarea
```

导航最终顺序：总览、节点、探测任务、告警规则、告警事件、通知渠道、注册窗口。

---

### Task 1: 通知渠道页

**Files:**
- Create: `web/src/lib/alerts.ts`, `web/src/lib/alerts.test.ts`, `web/src/pages/Channels.tsx`, `web/src/pages/Channels.test.tsx`
- Modify: `web/src/styles.css`, `web/src/pages/ProbeTasks.tsx`（只改表单的 className）, `web/src/App.tsx`, `web/src/components/Layout.tsx`, `web/src/components/Layout.test.tsx`

**Interfaces:**
- Consumes: `AdminService.method.{listNotifyChannels,saveNotifyChannel,deleteNotifyChannel,testNotifyChannel}`；`ChannelKind`、`NotifyChannel` 来自 `web/src/gen/probe/v1/admin_pb`；`errorText`（`api/auth.ts`）、`useLatestError`（`api/useLatestError.ts`）。
- Produces: `lib/alerts.ts` 导出 `type Entry<K>`、`CHANNEL_KINDS`、`labelOf(table, value)`、`channelTarget(c)`；后续任务在同一文件追加导出。样式类 `.edit-form`、`.picks`、`label.inline`。

- [ ] **Step 1: 样式更名与新增**

`web/src/styles.css` 里把 `.task-form` 全部改名为 `.edit-form`、`.task-form .node-picks` 改为 `.picks`（多选框组今后不只用于节点，也不只在任务表单里）。更名后相关规则为：

```css
form.row, .edit-form .row { align-items: flex-end; flex-wrap: wrap; }
.picks { display: flex; flex-wrap: wrap; gap: .5rem 1rem; border: 1px solid var(--line); padding: .5rem .75rem; }
.edit-form { white-space: normal; }
.picks label, label.inline { display: inline-flex; align-items: center; gap: .3rem; }
.edit-form textarea { width: 100%; min-height: 6rem; font-family: ui-monospace, monospace; }
```

并把 `textarea` 加进 `input, select, button { … }` 那条规则的选择器。`web/src/pages/ProbeTasks.tsx` 里 `className="card task-form"` 改为 `className="card edit-form"`，`className="node-picks"` 改为 `className="picks"`（Task 2 会把这个 fieldset 换成共用组件）。`grep -rn "task-form\|node-picks" web/src` 结果必须为空。

- [ ] **Step 2: 写 `lib/alerts.ts` 的失败测试**

`web/src/lib/alerts.test.ts`：

```ts
import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { ChannelKind, NotifyChannelSchema } from "../gen/probe/v1/admin_pb";
import { CHANNEL_KINDS, channelTarget, labelOf } from "./alerts";

describe("labelOf", () => {
  it("表内值给标签，表外值显示原值而不抛错", () => {
    expect(labelOf(CHANNEL_KINDS, ChannelKind.WEBHOOK)).toBe("Webhook");
    expect(labelOf(CHANNEL_KINDS, 9 as ChannelKind)).toBe("未知（9）");
  });
});

describe("channelTarget", () => {
  it("Telegram 只显示会话", () => {
    const c = create(NotifyChannelSchema, { kind: ChannelKind.TELEGRAM, telegram: { chatId: "-100", hasBotToken: true } });
    expect(channelTarget(c)).toBe("会话 -100");
  });
  it("Webhook 显示方法、主机与头名，空方法按 POST", () => {
    const c = create(NotifyChannelSchema, { kind: ChannelKind.WEBHOOK, webhook: { method: "", hasUrl: true, urlHost: "https://hooks.example", headerNames: ["Authorization", "X-Tag"] } });
    expect(channelTarget(c)).toBe("POST https://hooks.example，头 Authorization、X-Tag");
  });
  it("Webhook 没有头时不带头名段", () => {
    const c = create(NotifyChannelSchema, { kind: ChannelKind.WEBHOOK, webhook: { method: "PUT", hasUrl: true, urlHost: "http://10.0.0.2:8080" } });
    expect(channelTarget(c)).toBe("PUT http://10.0.0.2:8080");
  });
});
```

- [ ] **Step 3: 运行，确认失败**

Run: `cd web && pnpm exec vitest run src/lib/alerts.test.ts > /tmp/m4f-t1-lib-red.log 2>&1; echo $?`
Expected: 1，报错为找不到模块 `./alerts`。

- [ ] **Step 4: 实现 `lib/alerts.ts`（本任务部分）**

```ts
import { ChannelKind, type NotifyChannel } from "../gen/probe/v1/admin_pb";

export type Entry<K> = { value: K; label: string };

export const CHANNEL_KINDS: readonly Entry<ChannelKind>[] = [
  { value: ChannelKind.TELEGRAM, label: "Telegram" },
  { value: ChannelKind.WEBHOOK, label: "Webhook" },
];

// 更新的 hub 可能返回面板不认识的枚举值；显示原值而不是抛错，整页不因一个标签失效。
export function labelOf<K extends number>(table: readonly Entry<K>[], value: K): string {
  return table.find((e) => e.value === value)?.label ?? `未知（${value}）`;
}

// 凭据只写不读（spec §9.3）：hub 只回 chat_id、scheme://host、方法与头名，目标描述只能由这些拼成。
export function channelTarget(c: NotifyChannel): string {
  if (c.kind === ChannelKind.TELEGRAM) return `会话 ${c.telegram?.chatId ?? ""}`;
  if (c.kind === ChannelKind.WEBHOOK) {
    const w = c.webhook;
    const head = `${w?.method || "POST"} ${w?.urlHost ?? ""}`;
    return w && w.headerNames.length > 0 ? `${head}，头 ${w.headerNames.join("、")}` : head;
  }
  return labelOf(CHANNEL_KINDS, c.kind);
}
```

Run: `cd web && pnpm exec vitest run src/lib/alerts.test.ts > /tmp/m4f-t1-lib-green.log 2>&1; echo $?` → 0。

- [ ] **Step 5: 写渠道页的失败测试**

`web/src/pages/Channels.test.tsx`。夹具与前两条用例给出完整代码，其余用例按描述写，断言必须精确到下面列出的值：

```tsx
import { expect, it } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { ConnectError, Code } from "@connectrpc/connect";
import { ChannelKind, ListNotifyChannelsResponseSchema, type SaveNotifyChannelRequest } from "../gen/probe/v1/admin_pb";
import { renderWithAdmin, type AdminImpl } from "../test/harness";
import { Channels } from "./Channels";

const channels = create(ListNotifyChannelsResponseSchema, { channels: [
  { id: 1n, name: "tg", kind: ChannelKind.TELEGRAM, telegram: { chatId: "42", hasBotToken: true }, createdAt: 1_700_000_000n },
  { id: 2n, name: "hook", kind: ChannelKind.WEBHOOK, webhook: { method: "POST", hasUrl: true, urlHost: "https://hooks.example", headerNames: ["Authorization"], bodyTemplate: "{{.Summary}}" }, createdAt: 1_700_000_000n },
] });
const routes = [{ path: "/channels", Component: Channels }];
const render = (impl: AdminImpl) => renderWithAdmin({ listNotifyChannels: async () => channels, ...impl }, routes, "/channels");

it("列表只显示非凭据字段", async () => {
  render({});
  expect(await screen.findByRole("cell", { name: "会话 42" })).toBeInTheDocument();
  expect(screen.getByRole("cell", { name: "POST https://hooks.example，头 Authorization" })).toBeInTheDocument();
});

it("新建 Telegram 渠道只发 telegram 配置，成功后表单复位", async () => {
  const saved: SaveNotifyChannelRequest[] = [];
  render({ saveNotifyChannel: async (req) => { saved.push(req); return {}; } });
  const form = await screen.findByRole("form", { name: "新建通知渠道" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "值班群" } });
  fireEvent.change(within(form).getByLabelText("Bot token"), { target: { value: "123:abc" } });
  fireEvent.change(within(form).getByLabelText("Chat ID"), { target: { value: "-100" } });
  fireEvent.click(within(form).getByRole("button", { name: "创建" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  const c = saved[0].channel!;
  expect({ id: c.id, name: c.name, kind: c.kind, token: c.telegram?.botToken, chat: c.telegram?.chatId, webhook: c.webhook }).toEqual(
    { id: 0n, name: "值班群", kind: ChannelKind.TELEGRAM, token: "123:abc", chat: "-100", webhook: undefined });
  await waitFor(() => expect(within(screen.getByRole("form", { name: "新建通知渠道" })).getByLabelText("名称")).toHaveValue(""));
});
```

其余用例（每条一个 `it`）：

1. **编辑同种类时凭据留空即保留**：点"编辑 hook"，只改名称为 "hook2" 后保存；请求里 `webhook.url === ""`、`webhook.headers` 为 `{}`、`webhook.removeHeaders` 为 `[]`、`webhook.method === "POST"`、`webhook.bodyTemplate === "{{.Summary}}"`；URL 输入的 `placeholder` 为 `已保存 https://hooks.example，留空保持不变`，且 `not.toBeRequired()`。
2. **删除已保存的头**：编辑 hook，勾选"删除 Authorization"后保存；`removeHeaders` 为 `["Authorization"]`。
3. **新增头与覆盖**：编辑 hook，点"添加请求头"，填名 `X-Tag`、值 `v1` 后保存；`headers` 为 `{ "X-Tag": "v1" }`。
4. **同名头拒绝提交**：新建表单切到 Webhook，填名称与 URL，添加两行请求头 `X-A` 与 `x-a`；点创建后出现 `role="alert"` 文本 `请求头 x-a 填写了两次`，`saveNotifyChannel` 未被调用（用计数器断言为 0）。
5. **编辑时切换种类要求新凭据**：编辑 tg，把类型改为 Webhook；URL 输入 `toBeRequired()`，且不显示"已保存的请求头"组。
6. **Telegram 编辑 token 可留空**：编辑 tg，Bot token 输入 `toHaveValue("")`、`not.toBeRequired()`、placeholder 为 `已保存，留空保持不变`。
7. **发送测试成功与失败**：`testNotifyChannel` 第一次返回 `{}`、第二次抛 `new ConnectError("telegram: 401 Unauthorized", Code.FailedPrecondition)`；点"测试 tg"后出现 `role="status"` 文本 `已向 tg 发送测试消息`；再点一次后 status 消失、`role="alert"` 文本为 `telegram: 401 Unauthorized`，且请求的 `id` 为 `1n`。
8. **删除被引用渠道显示服务端原文**：`deleteNotifyChannel` 抛 `new ConnectError("notify channel 1 is referenced by alert rules: 离线 (id 4)", Code.FailedPrecondition)`；点"删除 tg"再点"确认删除 tg"，alert 文本为该原文，渠道行仍在。
9. **表格可聚焦滚动**：`region` 名为"通知渠道管理"、`tabindex="0"`，含列头"操作"。

- [ ] **Step 6: 运行，确认失败**

Run: `cd web && pnpm exec vitest run src/pages/Channels.test.tsx > /tmp/m4f-t1-red.log 2>&1; echo $?`
Expected: 1，找不到模块 `./Channels`。

- [ ] **Step 7: 实现 `pages/Channels.tsx`**

```tsx
import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { errorText } from "../api/auth";
import { useLatestError } from "../api/useLatestError";
import { AdminService, ChannelKind, type NotifyChannel } from "../gen/probe/v1/admin_pb";
import { CHANNEL_KINDS, channelTarget, labelOf } from "../lib/alerts";

const METHODS = ["POST", "PUT", "PATCH"] as const;
type HeaderRow = { name: string; value: string };
type Draft = {
  name: string; kind: ChannelKind; botToken: string; chatId: string;
  url: string; method: string; headers: HeaderRow[]; removeHeaders: Set<string>; bodyTemplate: string;
};

const emptyDraft = (): Draft => ({
  name: "", kind: ChannelKind.TELEGRAM, botToken: "", chatId: "",
  url: "", method: "POST", headers: [], removeHeaders: new Set(), bodyTemplate: "",
});
// hub 不回显 token、URL 与头值，编辑草稿里它们恒为空；留空提交由 hub 保留已存值。
const draftOf = (c: NotifyChannel): Draft => ({
  ...emptyDraft(), name: c.name, kind: c.kind, chatId: c.telegram?.chatId ?? "",
  method: c.webhook?.method || "POST", bodyTemplate: c.webhook?.bodyTemplate ?? "",
});

function toChannel(id: bigint, d: Draft) {
  const base = { id, name: d.name.trim(), kind: d.kind };
  if (d.kind === ChannelKind.TELEGRAM) return { ...base, telegram: { botToken: d.botToken.trim(), chatId: d.chatId.trim() } };
  return { ...base, webhook: {
    url: d.url.trim(), method: d.method, bodyTemplate: d.bodyTemplate,
    headers: Object.fromEntries(d.headers.map((h) => [h.name.trim(), h.value])),
    removeHeaders: [...d.removeHeaders].sort(),
  } };
}

// 头以 map 传输，同名两行序列化后只剩一行，hub 无从发现；只有表单还能看到两行。
function duplicateHeader(rows: HeaderRow[]): string | undefined {
  const seen = new Set<string>();
  for (const r of rows) {
    const key = r.name.trim().toLowerCase();
    if (seen.has(key)) return r.name.trim();
    seen.add(key);
  }
  return undefined;
}

export function Channels() {
  const qc = useQueryClient();
  const [creation, setCreation] = useState(0);
  const [notice, setNotice] = useState<string | null>(null);
  const { error, ...latest } = useLatestError();
  // 任何新操作开始时，上一次测试的成功提示与失败一并失效。
  const tracked = { ...latest, onMutate: () => { setNotice(null); return latest.onMutate(); } };
  const list = useQuery(AdminService.method.listNotifyChannels, {});
  const refresh = () => qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listNotifyChannels, cardinality: "finite" }) });
  const create = useMutation(AdminService.method.saveNotifyChannel, { ...tracked, onSuccess: refresh });
  // 各行共用一个 mutation observer，重叠的 mutate 只回调最后一次；任一行保存挂起时禁用全部行的保存。
  // 返回刷新 promise，编辑态在列表显示已保存值之后才关闭。
  const update = useMutation(AdminService.method.saveNotifyChannel, { ...tracked, onSuccess: refresh });
  const remove = useMutation(AdminService.method.deleteNotifyChannel, { ...tracked, onSuccess: refresh });
  const test = useMutation(AdminService.method.testNotifyChannel, tracked);
  if (list.isPending) return <p className="muted">加载中…</p>;
  if (list.error) return <p role="alert" className="error">{errorText(list.error)}</p>;
  const channels = list.data.channels;
  return (
    <section>
      <h1>通知渠道</h1>
      <ChannelForm key={creation} title="新建通知渠道" initial={emptyDraft()} pending={create.isPending}
        onSubmit={(d) => create.mutate({ channel: toChannel(0n, d) }, { onSuccess: () => setCreation((k) => k + 1) })} />
      {error != null && <p role="alert" className="error">{errorText(error)}</p>}
      {notice && <p role="status">{notice}</p>}
      <div className="table-scroll" role="region" aria-label="通知渠道管理" tabIndex={0}>
        <table className="nodes">
          <thead><tr><th>名称</th><th>类型</th><th>目标</th><th>创建于</th><th>操作</th></tr></thead>
          <tbody>
            {channels.map((c) => (
              <ChannelRow key={String(c.id)} channel={c} saving={update.isPending} deleting={remove.isPending} testing={test.isPending}
                onSave={(d, onSuccess) => update.mutate({ channel: toChannel(c.id, d) }, { onSuccess })}
                onTest={() => test.mutate({ id: c.id }, { onSuccess: () => setNotice(`已向 ${c.name} 发送测试消息`) })}
                onDelete={() => remove.mutate({ id: c.id })} />
            ))}
          </tbody>
        </table>
      </div>
      {channels.length === 0 && <p className="muted">还没有通知渠道。</p>}
    </section>
  );
}

function ChannelForm({ title, initial, original, pending, onSubmit, onCancel }: {
  title: string; initial: Draft; original?: NotifyChannel; pending: boolean; onSubmit: (d: Draft) => void; onCancel?: () => void;
}) {
  // initial 只在挂载时读取；编辑期间的列表刷新不覆盖草稿。
  const [draft, setDraft] = useState(initial);
  const [problem, setProblem] = useState<string | null>(null);
  // hub 只在种类未变时用已存凭据补全空值；新建或换种类时凭据必须重新填写。
  const keeps = original !== undefined && original.kind === draft.kind;
  const saved = keeps ? original?.webhook?.headerNames ?? [] : [];
  const set = (patch: Partial<Draft>) => setDraft({ ...draft, ...patch });
  const setHeader = (i: number, patch: Partial<HeaderRow>) => set({ headers: draft.headers.map((h, j) => (j === i ? { ...h, ...patch } : h)) });
  const toggleRemove = (name: string) => {
    const next = new Set(draft.removeHeaders);
    if (next.has(name)) next.delete(name); else next.add(name);
    set({ removeHeaders: next });
  };
  const handle = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!e.currentTarget.checkValidity()) return;
    const dup = draft.kind === ChannelKind.WEBHOOK ? duplicateHeader(draft.headers) : undefined;
    setProblem(dup === undefined ? null : `请求头 ${dup} 填写了两次`);
    if (dup === undefined) onSubmit(draft);
  };
  return (
    <form className="card edit-form" aria-label={title} onSubmit={handle}>
      <div className="row">
        <label>名称<input required maxLength={64} value={draft.name} onChange={(e) => set({ name: e.target.value })} /></label>
        <label>类型
          <select value={draft.kind} onChange={(e) => set({ kind: Number(e.target.value) as ChannelKind })}>
            {CHANNEL_KINDS.map(({ value, label }) => <option key={value} value={value}>{label}</option>)}
          </select>
        </label>
      </div>
      {draft.kind === ChannelKind.TELEGRAM ? (
        <div className="row">
          <label>Bot token<input type="password" autoComplete="off" required={!keeps} placeholder={keeps ? "已保存，留空保持不变" : ""}
            value={draft.botToken} onChange={(e) => set({ botToken: e.target.value })} /></label>
          <label>Chat ID<input required value={draft.chatId} onChange={(e) => set({ chatId: e.target.value })} /></label>
        </div>
      ) : (
        <>
          <div className="row">
            <label>URL<input type="url" autoComplete="off" required={!keeps}
              placeholder={keeps ? `已保存 ${original?.webhook?.urlHost ?? ""}，留空保持不变` : "https://"}
              value={draft.url} onChange={(e) => set({ url: e.target.value })} /></label>
            <label>方法
              <select value={draft.method} onChange={(e) => set({ method: e.target.value })}>
                {METHODS.map((m) => <option key={m} value={m}>{m}</option>)}
              </select>
            </label>
          </div>
          {saved.length > 0 && (
            <fieldset className="picks">
              <legend>已保存的请求头（值不回显）</legend>
              {saved.map((name) => (
                <label key={name}><input type="checkbox" checked={draft.removeHeaders.has(name)} onChange={() => toggleRemove(name)} />删除 {name}</label>
              ))}
            </fieldset>
          )}
          {draft.headers.map((h, i) => (
            <div className="row" key={i}>
              <label>请求头名<input required value={h.name} onChange={(e) => setHeader(i, { name: e.target.value })} /></label>
              <label>请求头值<input type="password" autoComplete="off" value={h.value} onChange={(e) => setHeader(i, { value: e.target.value })} /></label>
              <button type="button" className="link" onClick={() => set({ headers: draft.headers.filter((_, j) => j !== i) })}>移除</button>
            </div>
          ))}
          <button type="button" className="link" onClick={() => set({ headers: [...draft.headers, { name: "", value: "" }] })}>添加请求头</button>
          <label>请求体模板<textarea value={draft.bodyTemplate} placeholder="留空使用默认 JSON 模板" onChange={(e) => set({ bodyTemplate: e.target.value })} /></label>
        </>
      )}
      {problem && <p role="alert" className="error">{problem}</p>}
      <div className="row">
        <button type="submit" disabled={pending}>{onCancel ? "保存" : "创建"}</button>
        {onCancel && <button type="button" className="link" onClick={onCancel}>取消</button>}
      </div>
    </form>
  );
}

function ChannelRow({ channel: c, saving, deleting, testing, onSave, onTest, onDelete }: {
  channel: NotifyChannel; saving: boolean; deleting: boolean; testing: boolean;
  onSave: (d: Draft, onSuccess: () => void) => void; onTest: () => void; onDelete: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const [confirming, setConfirming] = useState(false);
  if (editing) {
    return (
      <tr><td colSpan={5}>
        <ChannelForm title={`编辑 ${c.name}`} initial={draftOf(c)} original={c} pending={saving}
          onSubmit={(d) => onSave(d, () => setEditing(false))} onCancel={() => setEditing(false)} />
      </td></tr>
    );
  }
  return (
    <tr>
      <td>{c.name}</td>
      <td>{labelOf(CHANNEL_KINDS, c.kind)}</td>
      <td>{channelTarget(c)}</td>
      <td className="muted">{new Date(Number(c.createdAt) * 1000).toLocaleDateString()}</td>
      <td>
        <button type="button" className="link" aria-label={`编辑 ${c.name}`} onClick={() => setEditing(true)}>编辑</button>{" "}
        <button type="button" className="link" aria-label={`测试 ${c.name}`} disabled={testing} onClick={onTest}>发送测试</button>{" "}
        {confirming ? (
          <>
            <button type="button" className="danger" disabled={deleting} onClick={onDelete}>确认删除 {c.name}</button>{" "}
            <button type="button" className="link" onClick={() => setConfirming(false)}>取消</button>
          </>
        ) : (
          <button type="button" className="link danger" aria-label={`删除 ${c.name}`} onClick={() => setConfirming(true)}>删除</button>
        )}
      </td>
    </tr>
  );
}
```

- [ ] **Step 8: 路由与导航**

`web/src/App.tsx` 在 `register` 之前加 `{ path: "channels", Component: Channels }`；`web/src/components/Layout.tsx` 在"注册窗口"之前加 `<NavLink to="/channels">通知渠道</NavLink>`。`Layout.test.tsx` 按现有"探测任务"用例的形状加一条：链接"通知渠道"的 `href` 为 `/channels`。

- [ ] **Step 9: 运行，确认通过**

Run: `cd web && pnpm exec vitest run > /tmp/m4f-t1-green.log 2>&1; echo $?` → 0。
Run: `make ci > /tmp/m4f-t1-ci.log 2>&1; echo $?` → 0（含 typecheck 与 lint）。

- [ ] **Step 10: 注入验红**（每条先 `git diff --quiet; echo $?` 为 1 确认落地，跑对应用例看红因，再 `git checkout -- web/src`）

- `toChannel` 的 Telegram 分支同时带上 `webhook: {}` → 用例"新建 Telegram"红在 `webhook` 不为 undefined。
- `keeps` 恒为 false → 用例 1 与 6 红（placeholder / required）。
- `duplicateHeader` 恒返回 undefined → 用例 4 红（保存被调用）。
- `tracked.onMutate` 不清 notice → 用例 7 红（status 仍在）。

- [ ] **Step 11: 提交**

```bash
git add web/src
git commit -m "web: 通知渠道管理，凭据只写不读，换种类须重填凭据，同名请求头在提交前拒绝"
```

---

### Task 2: 告警规则页与共用多选

**Files:**
- Create: `web/src/lib/ids.ts`, `web/src/lib/ids.test.ts`, `web/src/components/Picks.tsx`, `web/src/components/Picks.test.tsx`, `web/src/pages/AlertRules.tsx`, `web/src/pages/AlertRules.test.tsx`
- Modify: `web/src/lib/alerts.ts`, `web/src/lib/alerts.test.ts`, `web/src/pages/ProbeTasks.tsx`, `web/src/App.tsx`, `web/src/components/Layout.tsx`, `web/src/components/Layout.test.tsx`

**Interfaces:**
- Consumes: Task 1 的 `Entry`、`labelOf`、`.edit-form`、`.picks`、`label.inline`；`taskLabel(id, tasks)`（`lib/probes.ts`）；`formatUnit(v, unit)`（`lib/format.ts`）；`AdminService.method.{listAlertRules,saveAlertRule,deleteAlertRule,listNodes,listNotifyChannels,listProbeTasks}`。
- Produces: `lib/ids.ts` 的 `ascending(ids)`、`toggled(set, id)`；`components/Picks.tsx` 的 `Picks`；`lib/alerts.ts` 追加 `ALERT_KINDS`、`PROBE_METRICS`、`ruleCondition(rule, tasks)`、`type RuleStates`、`statesOf(states)`。

- [ ] **Step 1: 写 `ids`、`Picks` 与 `alerts` 追加部分的失败测试**

`web/src/lib/ids.test.ts`：

```ts
import { expect, it } from "vitest";
import { ascending, toggled } from "./ids";

it("ascending 按数值而不是字典序排 bigint", () => {
  expect(ascending(new Set([10n, 2n, 1n]))).toEqual([1n, 2n, 10n]);
});

it("toggled 返回新集合，不改原集合", () => {
  const before = new Set([1n]);
  expect([...toggled(before, 2n)]).toEqual([1n, 2n]);
  expect([...toggled(before, 1n)]).toEqual([]);
  expect([...before]).toEqual([1n]);
});
```

`web/src/components/Picks.test.tsx`：渲染 `<Picks legend="作用域节点" items={[{ id: 1n, name: "东京" }, { id: 2n, name: "法兰克福" }]} selected={new Set([1n])} onChange={spy} />`；断言 `getByRole("group", { name: "作用域节点" })` 存在，"东京"已勾、"法兰克福"未勾；点"法兰克福"后 `spy` 收到的集合为 `{1n, 2n}`。

`web/src/lib/alerts.test.ts` 追加：

```ts
import { AlertKind, AlertRuleSchema, AlertStateEntrySchema, ListProbeTasksResponseSchema, ProbeMetric } from "../gen/probe/v1/admin_pb";
import { ProbeKind } from "../gen/probe/v1/types_pb";
import { ruleCondition, statesOf } from "./alerts";

const tasks = create(ListProbeTasksResponseSchema, { tasks: [{ task: { id: 3n, kind: ProbeKind.TCP, target: "1.1.1.1:443" } }] }).tasks;

describe("ruleCondition", () => {
  it("离线规则", () => {
    expect(ruleCondition(create(AlertRuleSchema, { kind: AlertKind.OFFLINE }), tasks)).toBe("超过宽限期未上报");
  });
  it("丢包规则带百分号", () => {
    const r = create(AlertRuleSchema, { kind: AlertKind.PROBE, taskId: 3n, metric: ProbeMetric.LOSS_PCT, threshold: 50, forMinutes: 3 });
    expect(ruleCondition(r, tasks)).toBe("TCP 1.1.1.1:443 丢包率 ≥ 50%，连续 3 分钟");
  });
  it("RTT 规则带 ms，已删除任务用编号", () => {
    const r = create(AlertRuleSchema, { kind: AlertKind.PROBE, taskId: 9n, metric: ProbeMetric.RTT_MS, threshold: 150, forMinutes: 5 });
    expect(ruleCondition(r, tasks)).toBe("任务 #9 RTT 均值 ≥ 150 ms，连续 5 分钟");
  });
});

describe("statesOf", () => {
  it("按规则分组触发与待定，ok 记录不展示", () => {
    const states = [
      create(AlertStateEntrySchema, { ruleId: 1n, nodeId: 1n, state: "firing" }),
      create(AlertStateEntrySchema, { ruleId: 1n, nodeId: 2n, state: "pending" }),
      create(AlertStateEntrySchema, { ruleId: 2n, nodeId: 1n, state: "ok" }),
    ];
    const got = statesOf(states);
    expect(got.get(1n)?.firing.map((s) => s.nodeId)).toEqual([1n]);
    expect(got.get(1n)?.pending.map((s) => s.nodeId)).toEqual([2n]);
    expect(got.has(2n)).toBe(false);
  });
});
```

- [ ] **Step 2: 运行，确认失败**

Run: `cd web && pnpm exec vitest run src/lib src/components/Picks.test.tsx > /tmp/m4f-t2-lib-red.log 2>&1; echo $?`
Expected: 1，找不到 `./ids`、`./Picks`，以及 `ruleCondition`/`statesOf` 未导出。

- [ ] **Step 3: 实现 `lib/ids.ts`、`components/Picks.tsx` 与 `alerts.ts` 追加部分**

`web/src/lib/ids.ts`：

```ts
// hub 要求 id 列表升序去重；bigint 不能用默认的字典序比较。
export const ascending = (ids: Iterable<bigint>): bigint[] => [...ids].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));

export function toggled(set: ReadonlySet<bigint>, id: bigint): Set<bigint> {
  const next = new Set(set);
  if (next.has(id)) next.delete(id); else next.add(id);
  return next;
}
```

`web/src/components/Picks.tsx`：

```tsx
import { toggled } from "../lib/ids";

// 节点与渠道的多选共用；列表刷新后已删除对象的勾选仍留在集合里，由调用方提交时与当前列表求交。
export function Picks({ legend, items, selected, onChange }: {
  legend: string; items: readonly { id: bigint; name: string }[]; selected: ReadonlySet<bigint>; onChange: (next: Set<bigint>) => void;
}) {
  return (
    <fieldset className="picks">
      <legend>{legend}</legend>
      {items.map((it) => (
        <label key={String(it.id)}><input type="checkbox" checked={selected.has(it.id)} onChange={() => onChange(toggled(selected, it.id))} />{it.name}</label>
      ))}
    </fieldset>
  );
}
```

`web/src/lib/alerts.ts` 追加（import 补 `AlertKind`、`ProbeMetric`、`type AlertRule`、`type AlertStateEntry`、`type ProbeTaskDetail`，以及 `formatUnit`、`taskLabel`）：

```ts
export const ALERT_KINDS: readonly Entry<AlertKind>[] = [
  { value: AlertKind.OFFLINE, label: "离线" },
  { value: AlertKind.PROBE, label: "探测" },
];
// unit 与 formatUnit 的单位名一致：丢包阈值是百分数，RTT 阈值是毫秒（proto AlertRule.threshold）。
export const PROBE_METRICS: readonly (Entry<ProbeMetric> & { unit: string })[] = [
  { value: ProbeMetric.LOSS_PCT, label: "丢包率", unit: "percent" },
  { value: ProbeMetric.RTT_MS, label: "RTT 均值", unit: "ms" },
];

export function ruleCondition(rule: AlertRule, tasks: ProbeTaskDetail[] | undefined): string {
  if (rule.kind === AlertKind.OFFLINE) return "超过宽限期未上报";
  const metric = PROBE_METRICS.find((m) => m.value === rule.metric);
  const threshold = metric ? formatUnit(rule.threshold, metric.unit) : String(rule.threshold);
  return `${taskLabel(rule.taskId, tasks)} ${labelOf(PROBE_METRICS, rule.metric)} ≥ ${threshold}，连续 ${rule.forMinutes} 分钟`;
}

export type RuleStates = { firing: AlertStateEntry[]; pending: AlertStateEntry[] };

// hub 只返回已有记录的组合，缺失即 ok（proto ListAlertRulesResponse.states）；ok 记录同样不展示。
export function statesOf(states: AlertStateEntry[]): Map<bigint, RuleStates> {
  const out = new Map<bigint, RuleStates>();
  for (const s of states) {
    if (s.state !== "firing" && s.state !== "pending") continue;
    const entry = out.get(s.ruleId) ?? { firing: [], pending: [] };
    entry[s.state].push(s);
    out.set(s.ruleId, entry);
  }
  return out;
}
```

检查 `formatUnit(50, "percent")` 输出 `50%`、`formatUnit(150, "ms")` 输出 `150 ms`；若 `format.ts` 的实际输出不同，以它为准改测试期望，不改 `format.ts`。

Run: `cd web && pnpm exec vitest run src/lib src/components/Picks.test.tsx > /tmp/m4f-t2-lib-green.log 2>&1; echo $?` → 0。

- [ ] **Step 4: 探测任务页改用共用件**

`web/src/pages/ProbeTasks.tsx`：删掉 `TaskForm` 里的 `toggle` 与节点 fieldset，换成 `<Picks legend="分配到节点" items={nodes} selected={draft.nodeIds} onChange={(nodeIds) => setDraft({ ...draft, nodeIds })} />`；`submit` 里的内联比较器换成 `ascending(...)`。`ProbeTasks.test.tsx` 不改，必须原样通过——它按节点名找复选框，`Picks` 的标签文本不变。

- [ ] **Step 5: 写规则页的失败测试**

`web/src/pages/AlertRules.test.tsx`。夹具：

```tsx
const nodes = create(ListNodesResponseSchema, { nodes: [{ id: 1n, name: "东京" }, { id: 2n, name: "法兰克福" }] });
const channels = create(ListNotifyChannelsResponseSchema, { channels: [{ id: 5n, name: "hook", kind: ChannelKind.WEBHOOK }] });
const tasks = create(ListProbeTasksResponseSchema, { tasks: [{ task: { id: 3n, kind: ProbeKind.TCP, target: "1.1.1.1:443" }, nodeIds: [1n, 2n] }] });
const rules = create(ListAlertRulesResponseSchema, {
  rules: [
    { id: 7n, name: "离线", kind: AlertKind.OFFLINE, enabled: true, allNodes: true, channelIds: [5n] },
    { id: 8n, name: "丢包", kind: AlertKind.PROBE, enabled: true, nodeIds: [1n, 9n], taskId: 3n, metric: ProbeMetric.LOSS_PCT, threshold: 50, forMinutes: 3 },
    { id: 9n, name: "停用", kind: AlertKind.OFFLINE, enabled: false, allNodes: true },
  ],
  states: [
    { ruleId: 7n, nodeId: 1n, state: "firing" },
    { ruleId: 7n, nodeId: 2n, state: "pending" },
  ],
});
const routes = [{ path: "/alerts", Component: AlertRules }];
const base: AdminImpl = { listNodes: async () => nodes, listNotifyChannels: async () => channels, listProbeTasks: async () => tasks, listAlertRules: async () => rules };
```

用例（每条一个 `it`）：

1. **列表展示**：规则"离线"行含单元格 `全部节点`、`hook`、`超过宽限期未上报`，状态单元格文本含 `触发：东京` 与 `待定：法兰克福`；"丢包"行作用域单元格为 `东京、节点 #9`、通知单元格为 `只记事件`、条件单元格为 `TCP 1.1.1.1:443 丢包率 ≥ 50%，连续 3 分钟`、状态为 `正常`；"停用"行状态为 `已停用`。
2. **新建离线规则覆盖全部节点时不带节点列表**：填名称"全网离线"，勾"hook"；先取消"全部节点"、勾"东京"、再勾回"全部节点"，点创建；请求 `rule` 的 `{ id, name, kind, enabled, allNodes, nodeIds, channelIds, taskId, metric }` 等于 `{ id: 0n, name: "全网离线", kind: AlertKind.OFFLINE, enabled: true, allNodes: true, nodeIds: [], channelIds: [5n], taskId: 0n, metric: ProbeMetric.UNSPECIFIED }`；成功后表单名称复位为空。
3. **显式作用域**：新建时取消"全部节点"，出现名为"作用域节点"的组，勾"法兰克福"、"东京"后创建；`allNodes` 为 false、`nodeIds` 为 `[1n, 2n]`（升序）。
4. **探测规则字段**：类型改为"探测"，选任务 `TCP 1.1.1.1:443`，指标改"RTT 均值"，阈值标签变为 `阈值（ms）`，填 150，连续分钟填 5，创建；`{ kind, taskId, metric, threshold, forMinutes }` 等于 `{ AlertKind.PROBE, 3n, ProbeMetric.RTT_MS, 150, 5 }`。
5. **编辑回填并去掉已删除节点**：点"编辑 丢包"，表单里任务选择的值为 `"3"`、阈值为 `50`、"全部节点"未勾、"东京"已勾；直接保存，请求 `id` 为 `8n`、`nodeIds` 为 `[1n]`（节点 9 已不在节点列表中）。
6. **服务端校验原文展示**：`saveAlertRule` 抛 `new ConnectError("rule.node_ids must not be empty unless all_nodes is true", Code.InvalidArgument)`；新建时取消全部节点且不勾任何节点后创建，alert 文本为该原文。
7. **删除两段式**：点"删除 离线"出现"确认删除 离线"与说明 `事件记录保留`；确认后请求 `id` 为 `7n`。
8. **没有渠道时提示**：`listNotifyChannels` 返回空列表，新建表单里出现 `还没有通知渠道；规则只记录事件，不发送通知。`，且没有名为"通知渠道"的组。
9. **依赖未到达不渲染表单**：`listProbeTasks` 永不 resolve 时显示"加载中…"，页面上没有 form（与 `ProbeTasks.test.tsx` 的"节点列表挂起"用例同形）。

- [ ] **Step 6: 运行，确认失败**

Run: `cd web && pnpm exec vitest run src/pages/AlertRules.test.tsx > /tmp/m4f-t2-red.log 2>&1; echo $?`
Expected: 1，找不到模块 `./AlertRules`。

- [ ] **Step 7: 实现 `pages/AlertRules.tsx`**

```tsx
import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { errorText } from "../api/auth";
import { useLatestError } from "../api/useLatestError";
import { Picks } from "../components/Picks";
import { AdminService, AlertKind, ProbeMetric, type AlertRule, type Node, type NotifyChannel, type ProbeTaskDetail } from "../gen/probe/v1/admin_pb";
import { ALERT_KINDS, PROBE_METRICS, labelOf, ruleCondition, statesOf, type RuleStates } from "../lib/alerts";
import { ascending } from "../lib/ids";
import { taskLabel } from "../lib/probes";

type Draft = {
  name: string; kind: AlertKind; enabled: boolean; allNodes: boolean; nodeIds: Set<bigint>; channelIds: Set<bigint>;
  taskId: string; metric: ProbeMetric; threshold: string; forMinutes: string;
};

const emptyDraft = (): Draft => ({
  name: "", kind: AlertKind.OFFLINE, enabled: true, allNodes: true, nodeIds: new Set(), channelIds: new Set(),
  taskId: "", metric: ProbeMetric.LOSS_PCT, threshold: "", forMinutes: "3",
});
const draftOf = (r: AlertRule): Draft => {
  const probe = r.kind === AlertKind.PROBE;
  return {
    name: r.name, kind: r.kind, enabled: r.enabled, allNodes: r.allNodes, nodeIds: new Set(r.nodeIds), channelIds: new Set(r.channelIds),
    taskId: probe ? String(r.taskId) : "", metric: probe ? r.metric : ProbeMetric.LOSS_PCT,
    threshold: probe ? String(r.threshold) : "", forMinutes: probe ? String(r.forMinutes) : "3",
  };
};

// 当前列表不再包含的节点与渠道自然掉出，避免已删除对象让 hub 拒绝整次保存；
// 显式作用域因此变空时由 hub 拒绝（spec §6.6：空集不等于全部节点），不会悄悄放宽成全部。
function toRule(id: bigint, d: Draft, nodes: Node[], channels: NotifyChannel[]) {
  const live = (ids: ReadonlySet<bigint>, items: { id: bigint }[]) => ascending(items.filter((it) => ids.has(it.id)).map((it) => it.id));
  const probe = d.kind === AlertKind.PROBE
    ? { taskId: BigInt(d.taskId), metric: d.metric, threshold: Number(d.threshold), forMinutes: Number(d.forMinutes) }
    : {};
  return {
    id, name: d.name.trim(), kind: d.kind, enabled: d.enabled, allNodes: d.allNodes,
    nodeIds: d.allNodes ? [] : live(d.nodeIds, nodes), channelIds: live(d.channelIds, channels), ...probe,
  };
}

export function AlertRules() {
  const qc = useQueryClient();
  const [creation, setCreation] = useState(0);
  const { error, ...latest } = useLatestError();
  const nodes = useQuery(AdminService.method.listNodes, {});
  const channels = useQuery(AdminService.method.listNotifyChannels, {});
  const tasks = useQuery(AdminService.method.listProbeTasks, {});
  // 离线巡检每 10 秒一轮（spec §9.2）；按同一周期刷新，触发中的节点不用手动刷新就能看到。
  const rules = useQuery(AdminService.method.listAlertRules, {}, { refetchInterval: 10_000 });
  const refresh = () => qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listAlertRules, cardinality: "finite" }) });
  const create = useMutation(AdminService.method.saveAlertRule, { ...latest, onSuccess: refresh });
  // 各行共用一个 mutation observer，重叠的 mutate 只回调最后一次；任一行保存挂起时禁用全部行的保存。
  const update = useMutation(AdminService.method.saveAlertRule, { ...latest, onSuccess: refresh });
  const remove = useMutation(AdminService.method.deleteAlertRule, { ...latest, onSuccess: refresh });
  // 作用域、渠道与任务的求交依赖三张列表都已到达；任一未到达时不渲染可提交的表单。
  if (!nodes.data || !channels.data || !tasks.data) {
    const failed = nodes.error ?? channels.error ?? tasks.error;
    return failed ? <p role="alert" className="error">{errorText(failed)}</p> : <p className="muted">加载中…</p>;
  }
  const nodeList = nodes.data.nodes;
  const channelList = channels.data.channels;
  const taskList = tasks.data.tasks;
  if (rules.error) return <p role="alert" className="error">{errorText(rules.error)}</p>;
  const byRule = statesOf(rules.data?.states ?? []);
  const nodeName = (id: bigint) => nodeList.find((n) => n.id === id)?.name ?? `节点 #${id}`;
  const channelName = (id: bigint) => channelList.find((c) => c.id === id)?.name ?? `渠道 #${id}`;
  const lists = { nodes: nodeList, channels: channelList, tasks: taskList };
  return (
    <section>
      <h1>告警规则</h1>
      <RuleForm key={creation} title="新建告警规则" {...lists} initial={emptyDraft()} pending={create.isPending}
        onSubmit={(d) => create.mutate({ rule: toRule(0n, d, nodeList, channelList) }, { onSuccess: () => setCreation((k) => k + 1) })} />
      {error != null && <p role="alert" className="error">{errorText(error)}</p>}
      <div className="table-scroll" role="region" aria-label="告警规则管理" tabIndex={0}>
        <table className="nodes">
          <thead><tr><th>名称</th><th>类型</th><th>条件</th><th>作用域</th><th>通知</th><th>状态</th><th>操作</th></tr></thead>
          <tbody>
            {(rules.data?.rules ?? []).map((r) => (
              <RuleRow key={String(r.id)} rule={r} states={byRule.get(r.id)} {...lists} nodeName={nodeName} channelName={channelName}
                saving={update.isPending} deleting={remove.isPending}
                onSave={(d, onSuccess) => update.mutate({ rule: toRule(r.id, d, nodeList, channelList) }, { onSuccess })}
                onDelete={() => remove.mutate({ id: r.id })} />
            ))}
          </tbody>
        </table>
      </div>
      {rules.data && rules.data.rules.length === 0 && <p className="muted">还没有告警规则。</p>}
    </section>
  );
}

type Lists = { nodes: Node[]; channels: NotifyChannel[]; tasks: ProbeTaskDetail[] };

function RuleForm({ title, nodes, channels, tasks, initial, pending, onSubmit, onCancel }: Lists & {
  title: string; initial: Draft; pending: boolean; onSubmit: (d: Draft) => void; onCancel?: () => void;
}) {
  // initial 只在挂载时读取；列表的周期刷新不覆盖草稿。
  const [draft, setDraft] = useState(initial);
  const set = (patch: Partial<Draft>) => setDraft({ ...draft, ...patch });
  const probe = draft.kind === AlertKind.PROBE;
  const loss = draft.metric === ProbeMetric.LOSS_PCT;
  const handle = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!e.currentTarget.checkValidity()) return;
    onSubmit(draft);
  };
  return (
    <form className="card edit-form" aria-label={title} onSubmit={handle}>
      <div className="row">
        <label>名称<input required maxLength={64} value={draft.name} onChange={(e) => set({ name: e.target.value })} /></label>
        <label>类型
          <select value={draft.kind} onChange={(e) => set({ kind: Number(e.target.value) as AlertKind })}>
            {ALERT_KINDS.map(({ value, label }) => <option key={value} value={value}>{label}</option>)}
          </select>
        </label>
        <label className="inline"><input type="checkbox" checked={draft.enabled} onChange={(e) => set({ enabled: e.target.checked })} />启用</label>
      </div>
      {probe ? (
        <div className="row">
          <label>探测任务
            <select required value={draft.taskId} onChange={(e) => set({ taskId: e.target.value })}>
              <option value="">选择任务</option>
              {tasks.flatMap((d) => (d.task ? [<option key={String(d.task.id)} value={String(d.task.id)}>{taskLabel(d.task.id, tasks)}</option>] : []))}
            </select>
          </label>
          <label>指标
            <select value={draft.metric} onChange={(e) => set({ metric: Number(e.target.value) as ProbeMetric })}>
              {PROBE_METRICS.map(({ value, label }) => <option key={value} value={value}>{label}</option>)}
            </select>
          </label>
          <label>阈值（{loss ? "%" : "ms"}）<input type="number" required step="any" min={0} max={loss ? 100 : undefined}
            value={draft.threshold} onChange={(e) => set({ threshold: e.target.value })} /></label>
          <label>连续分钟<input type="number" required min={1} max={60} value={draft.forMinutes} onChange={(e) => set({ forMinutes: e.target.value })} /></label>
        </div>
      ) : (
        <p className="muted">节点超过离线宽限期未上报即触发，收到上报即恢复；宽限期在节点页按节点设置，未设置时取 hub 的 PROBE_OFFLINE_AFTER。</p>
      )}
      <label className="inline"><input type="checkbox" checked={draft.allNodes} onChange={(e) => set({ allNodes: e.target.checked })} />全部节点（含以后新建的节点）</label>
      {!draft.allNodes && <Picks legend="作用域节点" items={nodes} selected={draft.nodeIds} onChange={(nodeIds) => set({ nodeIds })} />}
      {probe && <p className="muted">探测规则只在既属于作用域、又分配了该任务的节点上评估。</p>}
      {channels.length > 0
        ? <Picks legend="通知渠道" items={channels} selected={draft.channelIds} onChange={(channelIds) => set({ channelIds })} />
        : <p className="muted">还没有通知渠道；规则只记录事件，不发送通知。</p>}
      <div className="row">
        <button type="submit" disabled={pending}>{onCancel ? "保存" : "创建"}</button>
        {onCancel && <button type="button" className="link" onClick={onCancel}>取消</button>}
      </div>
    </form>
  );
}

function RuleRow({ rule: r, states, nodes, channels, tasks, nodeName, channelName, saving, deleting, onSave, onDelete }: Lists & {
  rule: AlertRule; states: RuleStates | undefined; nodeName: (id: bigint) => string; channelName: (id: bigint) => string;
  saving: boolean; deleting: boolean; onSave: (d: Draft, onSuccess: () => void) => void; onDelete: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const [confirming, setConfirming] = useState(false);
  if (editing) {
    return (
      <tr><td colSpan={7}>
        <RuleForm title={`编辑 ${r.name}`} nodes={nodes} channels={channels} tasks={tasks} initial={draftOf(r)} pending={saving}
          onSubmit={(d) => onSave(d, () => setEditing(false))} onCancel={() => setEditing(false)} />
      </td></tr>
    );
  }
  return (
    <tr>
      <td>{r.name}</td>
      <td>{labelOf(ALERT_KINDS, r.kind)}</td>
      <td>{ruleCondition(r, tasks)}</td>
      <td>{r.allNodes ? "全部节点" : r.nodeIds.map(nodeName).join("、") || "无节点"}</td>
      <td>{r.channelIds.map(channelName).join("、") || "只记事件"}</td>
      <td><RuleState enabled={r.enabled} states={states} nodeName={nodeName} /></td>
      <td>
        <button type="button" className="link" aria-label={`编辑 ${r.name}`} onClick={() => setEditing(true)}>编辑</button>{" "}
        {confirming ? (
          <>
            <button type="button" className="danger" disabled={deleting} onClick={onDelete}>确认删除 {r.name}</button>{" "}
            <span className="muted">事件记录保留</span>{" "}
            <button type="button" className="link" onClick={() => setConfirming(false)}>取消</button>
          </>
        ) : (
          <button type="button" className="link danger" aria-label={`删除 ${r.name}`} onClick={() => setConfirming(true)}>删除</button>
        )}
      </td>
    </tr>
  );
}

// 停用的规则不评估，hub 保存停用时已清除其状态行；显示"已停用"而不是"正常"，避免读成一切无事。
function RuleState({ enabled, states, nodeName }: { enabled: boolean; states: RuleStates | undefined; nodeName: (id: bigint) => string }) {
  if (!enabled) return <span className="muted">已停用</span>;
  if (!states) return <span className="muted">正常</span>;
  const names = (list: RuleStates["firing"]) => list.map((s) => nodeName(s.nodeId)).join("、");
  return (
    <>
      {states.firing.length > 0 && <span className="error">触发：{names(states.firing)}</span>}
      {states.firing.length > 0 && states.pending.length > 0 && " "}
      {states.pending.length > 0 && <span>待定：{names(states.pending)}</span>}
    </>
  );
}
```

- [ ] **Step 8: 路由与导航**

`App.tsx` 在 `probes` 之后加 `{ path: "alerts", Component: AlertRules }`；`Layout.tsx` 在"探测任务"之后加 `<NavLink to="/alerts">告警规则</NavLink>`；`Layout.test.tsx` 加"告警规则"链接 `href` 为 `/alerts` 的断言。

- [ ] **Step 9: 运行，确认通过**

Run: `cd web && pnpm exec vitest run > /tmp/m4f-t2-green.log 2>&1; echo $?` → 0。
Run: `make ci > /tmp/m4f-t2-ci.log 2>&1; echo $?` → 0。

- [ ] **Step 10: 注入验红**（落地确认与还原同 Task 1）

- `toRule` 的 `nodeIds` 在 `allNodes` 为真时也发 `live(...)` → 用例 2 红（`nodeIds` 为 `[1n]`，草稿里残留的勾选随请求发出）。
- `live` 不与当前列表求交（直接 `ascending(ids)`）→ 用例 5 红（`nodeIds` 含 `9n`）。
- `RuleState` 去掉 `!enabled` 分支 → 用例 1 红（"停用"行显示"正常"）。
- `statesOf` 不跳过 ok → `alerts.test.ts` 的 statesOf 用例红。
- `ascending` 改成默认 `.sort()` → `ids.test.ts` 红（`[1n, 10n, 2n]`）。

- [ ] **Step 11: 提交**

```bash
git add web/src
git commit -m "web: 告警规则管理与当前状态，作用域显式且已删除对象不随请求发出，多选组件与探测任务共用"
```

---

### Task 3: 告警事件页与节点宽限期

**Files:**
- Create: `web/src/pages/AlertEvents.tsx`, `web/src/pages/AlertEvents.test.tsx`
- Modify: `web/src/lib/alerts.ts`, `web/src/lib/alerts.test.ts`, `web/src/pages/Nodes.tsx`, `web/src/pages/Nodes.test.tsx`, `web/src/pages/NodeDetail.tsx`, `web/src/pages/NodeDetail.test.tsx`, `web/src/App.tsx`, `web/src/components/Layout.tsx`, `web/src/components/Layout.test.tsx`

**Interfaces:**
- Consumes: Task 1–2 的 `lib/alerts.ts`；`AdminService.method.{listAlertEvents,listNodes,listNotifyChannels,updateNode}`；connect-query 2.3 的 `useInfiniteQuery(schema, input, { pageParamKey, getNextPageParam })`——初始页参数取 `input[pageParamKey]`（已在 `node_modules/@connectrpc/connect-query-core` 的 `createInfiniteQueryOptions` 确认）；`skipToken` 来自 `@tanstack/react-query`。
- Produces: `lib/alerts.ts` 追加 `transitionLabel(t)`、`deliveryText(d, channel)`、`graceText(s)`；路由 `/events?node=<id>`。

- [ ] **Step 1: 写 `alerts.ts` 追加部分的失败测试**

```ts
import { AlertDeliverySchema } from "../gen/probe/v1/admin_pb";
import { deliveryText, graceText, transitionLabel } from "./alerts";

describe("deliveryText", () => {
  it("成功、投递中、终止失败三种", () => {
    expect(deliveryText(create(AlertDeliverySchema, { ok: true, done: true, attempts: 1 }), "hook")).toBe("hook：已送达");
    expect(deliveryText(create(AlertDeliverySchema, { ok: false, done: false, attempts: 1, lastError: "503" }), "hook")).toBe("hook：投递中（已尝试 1 次）");
    expect(deliveryText(create(AlertDeliverySchema, { ok: false, done: true, attempts: 3, lastError: "timeout" }), "hook")).toBe("hook：失败（3 次）timeout");
  });
});

it("transitionLabel 认识两种变化，未知值原样显示", () => {
  expect([transitionLabel("firing"), transitionLabel("recovered"), transitionLabel("x")]).toEqual(["触发", "恢复", "x"]);
});

it("graceText 缺失即默认", () => {
  expect([graceText(undefined), graceText(90)]).toEqual(["默认", "90 秒"]);
});
```

Run: `cd web && pnpm exec vitest run src/lib/alerts.test.ts > /tmp/m4f-t3-lib-red.log 2>&1; echo $?` → 1（未导出）。

- [ ] **Step 2: 实现追加部分**

```ts
export const transitionLabel = (t: string): string => (t === "firing" ? "触发" : t === "recovered" ? "恢复" : t);

// "已送达"只来自成功的投递记录（spec §9.3）；done 为假的投递仍在队列里，不能显示成失败。
export function deliveryText(d: AlertDelivery, channel: string): string {
  if (d.ok) return `${channel}：已送达`;
  if (!d.done) return `${channel}：投递中（已尝试 ${d.attempts} 次）`;
  return `${channel}：失败（${d.attempts} 次）${d.lastError}`;
}

// 缺失表示取 hub 的 PROBE_OFFLINE_AFTER（proto Node.offline_grace_s）；清除后 hub 存 NULL，不会回显 0。
export const graceText = (s: number | undefined): string => (s === undefined ? "默认" : `${s} 秒`);
```

Run: `cd web && pnpm exec vitest run src/lib/alerts.test.ts > /tmp/m4f-t3-lib-green.log 2>&1; echo $?` → 0。

- [ ] **Step 3: 写事件页的失败测试**

`web/src/pages/AlertEvents.test.tsx`。夹具与分页用例：

```tsx
import { expect, it } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { AlertEventSchema, ChannelKind, ListNodesResponseSchema, ListNotifyChannelsResponseSchema, type ListAlertEventsRequest } from "../gen/probe/v1/admin_pb";
import { renderWithAdmin, type AdminImpl } from "../test/harness";
import { AlertEvents } from "./AlertEvents";

const nodes = create(ListNodesResponseSchema, { nodes: [{ id: 1n, name: "东京" }, { id: 2n, name: "法兰克福" }] });
const channels = create(ListNotifyChannelsResponseSchema, { channels: [{ id: 5n, name: "hook", kind: ChannelKind.WEBHOOK }] });
const event = (id: bigint, nodeId = 1n) => create(AlertEventSchema, { id, nodeId, ruleId: 7n, transition: "firing", at: 1_700_000_000n, summary: `事件 ${id}` });
const routes = [{ path: "/events", Component: AlertEvents }];
const render = (impl: AdminImpl, path = "/events") =>
  renderWithAdmin({ listNodes: async () => nodes, listNotifyChannels: async () => channels, ...impl }, routes, path);

it("一页满 100 条时可加载更早的事件，从本页最小 id 之前继续", async () => {
  const requests: ListAlertEventsRequest[] = [];
  render({ listAlertEvents: async (req) => {
    requests.push(req);
    if (req.beforeId === 0n) return { events: Array.from({ length: 100 }, (_, i) => event(200n - BigInt(i))) };
    return { events: [event(100n), event(99n)] };
  } });
  await screen.findByText("事件 200");
  fireEvent.click(screen.getByRole("button", { name: "加载更早的事件" }));
  await screen.findByText("事件 99");
  expect(requests.map((r) => r.beforeId)).toEqual([0n, 101n]);
  expect(requests[0].limit).toBe(100);
  expect(screen.queryByRole("button", { name: "加载更早的事件" })).toBeNull();
});
```

其余用例：

1. **URL 里的节点筛选**：路径 `/events?node=2`；请求 `nodeId` 为 `2n`；筛选下拉（标签"节点"）的值为 `"2"`；改选"全部节点"后，下一次请求 `nodeId` 为 `0n`，且 `router.state.location.search` 为 `""`（`renderWithAdmin` 返回 `router`）。
2. **无效的节点参数不发请求**：路径 `/events?node=abc`；出现 alert `节点参数 abc 无效。`，`listAlertEvents` 调用计数为 0。
3. **投递文案与渠道回退**：事件的 `deliveries` 为 `[{ channelId: 5n, ok: true, done: true, attempts: 1 }, { channelId: 7n, ok: false, done: true, attempts: 0, lastError: "channel deleted" }]`；单元格内出现 `hook：已送达` 与 `渠道 #7：失败（0 次）channel deleted`；另一个 `deliveries` 为空的事件显示 `未配置渠道`。
4. **变化与节点名**：`transition` 为 `recovered` 的事件显示 `恢复`；`firing` 显示 `触发` 且该单元格有 `error` 类；`nodeId` 为 9 的事件节点列显示 `节点 #9`。
5. **空列表**：返回空数组时显示 `没有告警事件。`，没有"加载更早的事件"按钮。

- [ ] **Step 4: 运行，确认失败**

Run: `cd web && pnpm exec vitest run src/pages/AlertEvents.test.tsx > /tmp/m4f-t3-red.log 2>&1; echo $?` → 1。

- [ ] **Step 5: 实现 `pages/AlertEvents.tsx`**

```tsx
import { useInfiniteQuery, useQuery } from "@connectrpc/connect-query";
import { skipToken } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router";
import { errorText } from "../api/auth";
import { AdminService } from "../gen/probe/v1/admin_pb";
import { deliveryText, transitionLabel } from "../lib/alerts";

// 与 hub 的默认页长一致；不足一页即已到最早的事件。
const PAGE = 100;

export function AlertEvents() {
  const [params, setParams] = useSearchParams();
  const raw = params.get("node");
  // 无效参数不能退化成"全部节点"——那是放宽；直接报错，不发请求。
  const valid = raw === null || /^[1-9]\d*$/.test(raw);
  const nodeId = raw !== null && valid ? BigInt(raw) : 0n;
  const nodes = useQuery(AdminService.method.listNodes, {});
  const channels = useQuery(AdminService.method.listNotifyChannels, {});
  const events = useInfiniteQuery(AdminService.method.listAlertEvents, valid ? { nodeId, beforeId: 0n, limit: PAGE } : skipToken, {
    pageParamKey: "beforeId",
    // 事件按 id 倒序；下一页从本页最小 id 之前开始。
    getNextPageParam: (last) => (last.events.length < PAGE ? undefined : last.events[last.events.length - 1].id),
  });
  if (!valid) return <p role="alert" className="error">节点参数 {raw} 无效。<Link to="/events">查看全部事件</Link></p>;
  if (nodes.isPending) return <p className="muted">加载中…</p>;
  if (nodes.error) return <p role="alert" className="error">{errorText(nodes.error)}</p>;
  const nodeList = nodes.data.nodes;
  const nodeName = (id: bigint) => nodeList.find((n) => n.id === id)?.name ?? `节点 #${id}`;
  const channelName = (id: bigint) => channels.data?.channels.find((c) => c.id === id)?.name ?? `渠道 #${id}`;
  const rows = events.data?.pages.flatMap((p) => p.events) ?? [];
  const pageError = events.error ?? channels.error;
  return (
    <section>
      <h1>告警事件</h1>
      <label>节点
        <select value={String(nodeId)} onChange={(e) => setParams(e.target.value === "0" ? {} : { node: e.target.value })}>
          <option value="0">全部节点</option>
          {nodeList.map((n) => <option key={String(n.id)} value={String(n.id)}>{n.name}</option>)}
          {nodeId !== 0n && !nodeList.some((n) => n.id === nodeId) && <option value={String(nodeId)}>{nodeName(nodeId)}</option>}
        </select>
      </label>
      {pageError != null && <p role="alert" className="error">{errorText(pageError)}</p>}
      <div className="table-scroll" role="region" aria-label="告警事件" tabIndex={0}>
        <table className="nodes">
          <thead><tr><th>时间</th><th>节点</th><th>变化</th><th>摘要</th><th>通知</th></tr></thead>
          <tbody>
            {rows.map((ev) => (
              <tr key={String(ev.id)}>
                <td>{new Date(Number(ev.at) * 1000).toLocaleString()}</td>
                <td>{nodeName(ev.nodeId)}</td>
                <td className={ev.transition === "firing" ? "error" : undefined}>{transitionLabel(ev.transition)}</td>
                <td>{ev.summary}</td>
                <td>
                  {ev.deliveries.length === 0
                    ? <span className="muted">未配置渠道</span>
                    : ev.deliveries.map((d) => <div key={String(d.channelId)}>{deliveryText(d, channelName(d.channelId))}</div>)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {events.hasNextPage && (
        <button type="button" disabled={events.isFetchingNextPage} onClick={() => void events.fetchNextPage()}>加载更早的事件</button>
      )}
      {events.data && rows.length === 0 && <p className="muted">没有告警事件。</p>}
    </section>
  );
}
```

若 `useInfiniteQuery` 的类型不接受 `skipToken` 与对象的条件表达式，按 `use-infinite-query.d.ts` 的签名调整写法，语义不变：参数无效时不发请求。

- [ ] **Step 6: 节点页的宽限期**

`web/src/pages/Nodes.tsx`：

- 新增一个草稿构造函数，初始 `useState` 与"编辑"按钮都调用它（现在两处各写一份对象字面量，加字段会变成三份）：

```tsx
// 宽限期以字符串编辑，0 表示清除（取 hub 的 PROBE_OFFLINE_AFTER）。
const draftOf = (node: Node) => ({
  name: node.name, public: node.public, note: node.note, trafficResetDay: node.trafficResetDay,
  offlineGraceS: String(node.offlineGraceS ?? 0),
});
const validGrace = (s: string) => /^\d+$/.test(s);
```

- `onSave` 的 patch 类型加 `offlineGraceS: number`，保存时传 `{ ...draft, offlineGraceS: Number(draft.offlineGraceS) }`；保存按钮在 `!validGrace(draft.offlineGraceS)` 时也禁用。
- `Nodes()` 里 `update.mutate({ id: n.id, offlineGraceS: n.offlineGraceS ?? 0, ...patch }, …)` 改为 `update.mutate({ id: n.id, ...patch }, …)`：宽限期现在由编辑行显式给出，不再需要从当前值补。
- 表头在"重置日"之后加"离线宽限期"；展示行对应单元格为 `graceText(node.offlineGraceS)`；编辑行对应单元格为 `<input type="number" min={0} aria-label="离线宽限期（秒）" …/>` 加说明 `<p className="muted">0 表示取 hub 的 PROBE_OFFLINE_AFTER；非 0 不能小于它。</p>`。编辑行与展示行的单元格数必须与表头一致。

`web/src/pages/Nodes.test.tsx` 新增用例：

1. 宽限期列：`offlineGraceS` 缺失的节点显示 `默认`，为 90 的节点显示 `90 秒`。
2. 编辑宽限期：把 90 改为 120 后保存，请求 `offlineGraceS` 为 `120`；另一节点只改名称，请求 `offlineGraceS` 为 `0`（缺失 → 0，保持清除状态）。
3. 非法输入禁用保存：输入 `-1` 时保存按钮 `toBeDisabled()`。
4. 服务端下限错误原文展示：`updateNode` 抛 `new ConnectError("offline_grace_s: must be 0 or at least 30 seconds (PROBE_OFFLINE_AFTER); got 20", Code.InvalidArgument)`，alert 文本为该原文。

现有断言"编辑载荷含 offlineGraceS"若与新用例 2 重复，删掉旧的那条，不保留两份。

- [ ] **Step 7: 节点详情链接、路由与导航**

- `NodeDetail.tsx` 的 `<header className="row detail-header">` 里、`<h1>` 之后加 `<Link to={`/events?node=${id}`}>告警事件</Link>`；`NodeDetail.test.tsx` 加断言：链接"告警事件"的 `href` 为 `/events?node=1`（按该测试文件现有路由与夹具的节点 id）。
- `App.tsx` 在 `alerts` 之后加 `{ path: "events", Component: AlertEvents }`；`Layout.tsx` 在"告警规则"之后加 `<NavLink to="/events">告警事件</NavLink>`；`Layout.test.tsx` 加对应断言。

- [ ] **Step 8: 运行，确认通过**

Run: `cd web && pnpm exec vitest run > /tmp/m4f-t3-green.log 2>&1; echo $?` → 0。
Run: `make ci > /tmp/m4f-t3-ci.log 2>&1; echo $?` → 0。

- [ ] **Step 9: 注入验红**（落地确认与还原同 Task 1）

- `getNextPageParam` 返回本页第一条的 id → 分页用例红（第二次请求的 `beforeId` 为 `200n`）。
- `valid` 恒为 true（无效参数退化为全部节点）→ 用例 2 红（发出了请求）。
- `deliveryText` 去掉 `!d.done` 分支 → `alerts.test.ts` 的投递用例红。
- `Nodes()` 恢复 `offlineGraceS: n.offlineGraceS ?? 0` 并放在 `...patch` 之后 → 节点页用例 2 红（仍发 90）。

- [ ] **Step 10: 提交**

```bash
git add web/src
git commit -m "web: 告警事件按节点翻页并显示投递结果，节点页可编辑离线宽限期"
```

---

## 收尾：控制端的浏览器验收

控制端用验收台（真实 hub + 两个 Docker agent，形状同 `scripts/e2e.sh`）加一个本机 webhook 接收器，在浏览器里走一遍：新建 Webhook 渠道并发送测试；新建只覆盖一个节点的离线规则并勾选该渠道；停掉该节点的容器，规则页在 30 秒内出现"触发：<节点名>"，事件页出现该事件且通知为"已送达"；恢复容器后出现"恢复"事件；节点页把宽限期改成小于 `PROBE_OFFLINE_AFTER` 的值被拒并显示原文，改成合法值后列表显示新值；编辑渠道只改名称后再发送测试仍送达（凭据保留）。截图存档，验收结束删除 `.playwright-mcp/`。

## 自检

- spec §9.1–§9.3 的面板面：规则（离线 / 探测、作用域、渠道）、状态（触发 / 待定 / 已停用）、通知（渠道维护、测试、投递结果）、宽限期编辑——Task 1–3 各有对应。
- 凭据只写不读：渠道页从不读取 token、URL、头值字段；编辑留空即保留；换种类必填——Task 1 用例 1、5、6。
- 显式作用域：`allNodes` 为真发空数组、为假发求交后的列表，空集交给 hub 拒绝——Task 2 用例 2、3、5、6。
- 类型与命名一致：`Entry`、`labelOf`、`ALERT_KINDS`、`PROBE_METRICS`、`CHANNEL_KINDS`、`ruleCondition`、`statesOf`、`RuleStates`、`channelTarget`、`deliveryText`、`transitionLabel`、`graceText`、`ascending`、`toggled`、`Picks` 在定义与使用处同名。
