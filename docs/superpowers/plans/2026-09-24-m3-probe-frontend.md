# M3 探测（二）：前端——探测任务管理与探测历史图表 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 管理面板能创建、修改、删除探测任务并把任务分配到节点；节点详情页在现有七张指标图旁增加两张探测图（丢包率、RTT 均值，每个任务一条线），并在主机信息里显示 ICMP 是否可用。

**Architecture:** 纯前端新增，不动 proto、生成代码与 hub。任务页复用 `Nodes.tsx` 的行内编辑与两段式删除范式，创建与编辑共用同一个表单组件；图表复用 `Chart.tsx` 与节点详情页已有的时间窗口，探测样本另写一份对齐函数（`ProbeSample` 的字段形状与 `MetricSample` 不同，`toAligned` 不能复用）。所有数据经生成的 Connect-ES 客户端调 `AdminService` 已有的四个探测方法。

**Tech Stack:** 与管理面板计划一致：pnpm、Vite、React 19、TypeScript、react-router、@connectrpc/connect-query、@tanstack/react-query、uPlot、vitest + jsdom + testing-library。

**Spec:** `docs/superpowers/specs/2026-09-17-probe-architecture-design.md` §8（探测）、§8.3（`QueryProbes` 的返回形状）、§10（前端）、§12（从用户可见入口测）；`docs/guidelines/agent-first.md`。前置：`docs/superpowers/plans/2026-09-23-m3-probe-backend.md` 已合入 main（7792102）。

## Global Constraints

**来自 spec 与已合入接口的硬值**

- `QueryProbes` 返回 `level`、`step_s`、按 `task_id` 升序的 `series`；每条 series 的 `samples` 按 `ts` 升序、只含 `sent > 0` 的点；`rtt_mean_us`/`rtt_min_us`/`rtt_max_us` 为 optional，只在 `sent − lost − errors > 0` 时存在（§8.3、proto 注释）。缺失的 ts 与缺失的 rtt 在图上都是空洞（null），不是 0。
- 丢包率 = `lost / sent`，`errors` 不计入（proto 注释）。
- 已删除任务的历史仍按 `task_id` 返回（proto 注释）：图上必须能为没有对应任务的 series 给出标签。
- `SaveProbeTask`：`task.id` 为 0 创建，否则整体替换；`node_ids` 是保存后的**完整**分配列表（proto 注释）。字段约束由 hub 用 `probelimit` 校验并返回自解释的 `InvalidArgument` 文本；前端不复制第二份规则，只用原生表单约束（`required`、`min`、`max`、`maxLength`）拦住明显非法的输入，服务端错误原文经 `errorText` 展示（agent-first：错误由服务端说清，面板只是一个消费者）。
- `Facts.icmp_available` 为 false 时该节点的 ICMP 任务每次回报 error（§8.2）；详情页把它显示出来，让 100% error 有处可解释。
- 面板不引入任何专用端点（agent-first）。

**沿用管理面板计划的取值**

- 历史窗口预设与 `max_points: 1000` 与 `NodeDetail.tsx` 的 `RANGES` 相同；探测查询与指标查询用同一对 `from`/`to`。
- 文案简体中文直出；无 UI 框架；样式只加进 `web/src/styles.css`；错误一律 `<p role="alert" className="error">{errorText(err)}</p>`。
- 危险操作两段式确认（`confirming` 状态切换按钮文案），不用 `window.confirm`。
- bigint 约定：React `key` 用 `String(id)`；请求体里的 id 保持 bigint；时间戳 `Number(ts) * 1000`。
- Query key 失效用 `createConnectQueryKey({ schema, cardinality: "finite" })`，只失效相关 key。

**代码与提交规范（来自用户全局规则，对子代理同样生效）**

- 注释、commit message 里禁止过程信息（任务 / 步骤编号、里程碑代号、审阅轮次、"按计划 / 简报 / 裁决"）。写 WHY 与不变式。
- 不打补丁；不复制第二份实现（创建与编辑共用一个表单组件；对齐函数不复制 `toAligned` 的循环而是共用网格生成）；不写 TODO；同形缺陷一次改全。
- 每条新断言先红后绿；注入前 `git diff --quiet` 确认落地；红要红在正确原因。
- 判成败的命令不接管道：`cmd > log 2>&1; echo $?`。

## 文件结构

```
web/src/lib/probes.ts(+.test.ts)          ProbeSample → uPlot 对齐数据；任务标签
web/src/lib/series.ts                     抽出网格生成 gridOf(step, from, to)，toAligned 与探测对齐共用
web/src/lib/format.ts(+.test.ts)          formatUnit 增加 "ms"
web/src/lib/axis.ts(+.test.ts)            axisValues 增加 "ms" 后缀
web/src/components/Chart.tsx              调色板扩到 8 色
web/src/pages/NodeDetail.tsx(+.test.tsx)  探测两张图、空状态、facts 增加 ICMP
web/src/pages/ProbeTasks.tsx(+.test.tsx)  任务管理页：列表、创建、编辑、删除、分配
web/src/components/Layout.tsx(+.test.tsx) 导航增加"探测任务"
web/src/App.tsx                           路由 probes
web/src/styles.css                        任务表单里的节点复选框布局
```

---

### Task 1: 探测样本对齐、`ms` 单位、调色板

**Files:**
- Create: `web/src/lib/probes.ts`、`web/src/lib/probes.test.ts`
- Modify: `web/src/lib/series.ts`、`web/src/lib/format.ts`、`web/src/lib/format.test.ts`、`web/src/lib/axis.ts`、`web/src/lib/axis.test.ts`、`web/src/components/Chart.tsx`

**Interfaces:**
- Consumes: `QueryProbesResponse`、`ProbeSample`、`ProbeTaskDetail`（`web/src/gen/probe/v1/admin_pb.ts`）、`ProbeKind`（`types_pb.ts`）。
- Produces（Task 2 使用）：
  - `gridOf(step: number, from: number, to: number): number[]`（series.ts 导出）
  - `type ProbeValue = (s: ProbeSample) => number | null`
  - `lossPercent: ProbeValue`、`rttMeanMs: ProbeValue`
  - `toProbeAligned(resp: QueryProbesResponse, taskIds: bigint[], from: number, to: number, value: ProbeValue): AlignedData`
  - `taskIdsOf(resp: QueryProbesResponse): bigint[]`
  - `taskLabel(id: bigint, tasks: ProbeTaskDetail[] | undefined): string`
  - `formatUnit(v, "ms")`、`axisValues(vals, "ms")`

- [ ] **Step 1: 失败测试——`probes.test.ts`**

```ts
import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { ProbeTaskDetailSchema, QueryProbesResponseSchema } from "../gen/probe/v1/admin_pb";
import { ProbeKind } from "../gen/probe/v1/types_pb";
import { lossPercent, rttMeanMs, taskIdsOf, taskLabel, toProbeAligned } from "./probes";

const resp = create(QueryProbesResponseSchema, {
  level: "1m", stepS: 60,
  series: [
    { taskId: 3n, samples: [
      { ts: 120n, sent: 10, lost: 2, errors: 0, rttMeanUs: 12_500 },
      { ts: 240n, sent: 10, lost: 10, errors: 0 },
    ] },
    { taskId: 7n, samples: [{ ts: 180n, sent: 5, lost: 0, errors: 5 }] },
  ],
});

describe("toProbeAligned", () => {
  it("每个任务一列，缺 ts 与缺 rtt 都是 null", () => {
    const loss = toProbeAligned(resp, [3n, 7n], 100, 300, lossPercent);
    expect(loss[0]).toEqual([60, 120, 180, 240]);
    expect(loss[1]).toEqual([null, 20, null, 100]);
    expect(loss[2]).toEqual([null, null, 0, null]);
    const rtt = toProbeAligned(resp, [3n, 7n], 100, 300, rttMeanMs);
    expect(rtt[1]).toEqual([null, 12.5, null, null]);
    expect(rtt[2]).toEqual([null, null, null, null]);
  });
  it("全部失败的点丢包率为 0、rtt 为空：error 不计入丢包", () => {
    const s = resp.series[1].samples[0];
    expect(lossPercent(s)).toBe(0);
    expect(rttMeanMs(s)).toBeNull();
  });
  it("taskIdsOf 保持响应顺序", () => {
    expect(taskIdsOf(resp)).toEqual([3n, 7n]);
  });
});

describe("taskLabel", () => {
  const tasks = [create(ProbeTaskDetailSchema, { task: { id: 3n, kind: ProbeKind.TCP, target: "1.1.1.1:443", intervalS: 30, timeoutMs: 1000 }, nodeIds: [] })];
  it("有任务时用类型与目标", () => expect(taskLabel(3n, tasks)).toBe("TCP 1.1.1.1:443"));
  it("找不到任务（已删除）时退回编号", () => expect(taskLabel(7n, tasks)).toBe("任务 #7"));
  it("任务列表未到时也退回编号", () => expect(taskLabel(3n, undefined)).toBe("任务 #3"));
});
```

- [ ] **Step 2: 失败测试——`format.test.ts` 与 `axis.test.ts` 追加**

```ts
// format.test.ts
it("ms 按量级取位数", () => {
  expect(formatUnit(0.4567, "ms")).toBe("0.46 ms");
  expect(formatUnit(12.34, "ms")).toBe("12.3 ms");
  expect(formatUnit(250.7, "ms")).toBe("251 ms");
});
// axis.test.ts
it("ms 轴带后缀且精度足以区分刻度", () => {
  expect(axisValues([0, 0.5, 1], "ms")).toEqual(["0.0 ms", "0.5 ms", "1.0 ms"]);
});
```

- [ ] **Step 3: 运行，确认失败在"模块不存在 / 断言不等"**

Run: `cd web && pnpm exec vitest run src/lib > /tmp/m3c-t1-red.log 2>&1; echo $?`
Expected: 1，probes.test.ts 因 `./probes` 不存在失败，format/axis 因 `"0.46"`≠`"0.46 ms"` 失败。

- [ ] **Step 4: 实现**

`series.ts`：把网格生成抽成导出函数，`toAligned` 改为调用它（行为不变）：

```ts
// x 轴按 step 补全 [from, to) 的网格；起点向下对齐到 step 的整数倍，与 hub 的点对齐规则一致。
export function gridOf(step: number, from: number, to: number): number[] {
  const start = from - (from % step);
  const xs: number[] = [];
  for (let t = start; t < to; t += step) xs.push(t);
  return xs;
}
```

`probes.ts`：

```ts
import type { AlignedData } from "uplot";
import type { ProbeSample, ProbeTaskDetail, QueryProbesResponse } from "../gen/probe/v1/admin_pb";
import { ProbeKind } from "../gen/probe/v1/types_pb";
import { gridOf } from "./series";

export type ProbeValue = (s: ProbeSample) => number | null;

// 丢包率只看超时：error 是本地无法发起（无 socket、解析失败），不是链路事实。
// hub 只返回 sent > 0 的点；这里仍显式守住除零，让不变式不依赖上游。
export const lossPercent: ProbeValue = (s) => (s.sent > 0 ? (s.lost / s.sent) * 100 : null);
// rtt 只在该点有成功探测时存在；没有就是空洞，不能画成 0。
export const rttMeanMs: ProbeValue = (s) => (s.rttMeanUs === undefined ? null : s.rttMeanUs / 1000);

// 每个任务一列，网格规则与指标图相同，缺 ts 为 null；同一窗口的探测图与指标图因此可以对齐比较。
export function toProbeAligned(resp: QueryProbesResponse, taskIds: bigint[], from: number, to: number, value: ProbeValue): AlignedData {
  const xs = gridOf(resp.stepS, from, to);
  const columns = taskIds.map((id) => {
    const at = new Map<number, ProbeSample>();
    resp.series.find((s) => s.taskId === id)?.samples.forEach((s) => at.set(Number(s.ts), s));
    return xs.map((t) => {
      const s = at.get(t);
      return s === undefined ? null : value(s);
    });
  });
  return [xs, ...columns] as AlignedData;
}

export function taskIdsOf(resp: QueryProbesResponse): bigint[] {
  return resp.series.map((s) => s.taskId);
}

// 已删除任务的历史仍会返回；没有任务可查时用编号，让线仍有名字。
export function taskLabel(id: bigint, tasks: ProbeTaskDetail[] | undefined): string {
  const t = tasks?.find((d) => d.task?.id === id)?.task;
  if (!t) return `任务 #${id}`;
  return `${t.kind === ProbeKind.TCP ? "TCP" : "ICMP"} ${t.target}`;
}
```

`format.ts` 的 `formatUnit` 增加：

```ts
    case "ms":
      return `${v.toFixed(v < 10 ? 2 : v < 100 ? 1 : 0)} ms`;
```

`axis.ts`：`let suffix = unit === "percent" ? "%" : unit === "ms" ? " ms" : "";`

`Chart.tsx`：调色板扩到 8 色（一个节点常有多条探测线），注释写明超过 8 条会循环：

```ts
const palette = ["#3b82f6", "#f59e0b", "#10b981", "#ef4444", "#8b5cf6", "#06b6d4", "#84cc16", "#ec4899"];
```

- [ ] **Step 5: 运行 lib 测试与 Chart 测试，确认通过**

Run: `cd web && pnpm exec vitest run src/lib src/components > /tmp/m3c-t1-green.log 2>&1; echo $?`
Expected: 0。

- [ ] **Step 6: 缺陷注入（各一次，注入前 `git diff --quiet` 确认落地）**

- `rttMeanMs` 把 undefined 画成 0 → probes.test 第一条红在 `rtt[1]` 断言。
- `lossPercent` 改为 `(lost + errors) / sent` → 第二条红（期望 0 得 100）。
- `taskLabel` 找不到任务时返回空串 → 第二组红。
- `gridOf` 起点不对齐（`start = from`）→ 现有 `series.test.ts` 与 probes.test 的 `loss[0]` 断言红。

- [ ] **Step 7: 提交**

```bash
git add web/src/lib web/src/components/Chart.tsx
git commit -m "web: 探测样本对齐到指标图同一网格，缺 rtt 与缺点都是空洞"
```

---

### Task 2: 节点详情页——探测两张图、空状态、ICMP 可用性

**Files:**
- Modify: `web/src/pages/NodeDetail.tsx`、`web/src/pages/NodeDetail.test.tsx`

**Interfaces:**
- Consumes: Task 1 的 `toProbeAligned`、`lossPercent`、`rttMeanMs`、`taskIdsOf`、`taskLabel`；`AdminService.method.queryProbes`、`AdminService.method.listProbeTasks`。
- Produces: 无（页面）。

- [ ] **Step 1: 失败测试——`NodeDetail.test.tsx` 追加**

现有测试文件已有 `listNodes`/`queryMetrics` 的内存实现与 `renderWithAdmin` 用法，沿用同一夹具追加三个用例：

```ts
it("探测图每个任务一条线，已删除任务用编号，且与指标查询共用同一窗口", async () => {
  const windows: { name: string; from: bigint; to: bigint }[] = [];
  renderWithAdmin({
    listNodes: async () => nodesResponse,
    queryMetrics: async (req) => { windows.push({ name: "metrics", from: req.from, to: req.to }); return metricsResponse; },
    listProbeTasks: async () => create(ListProbeTasksResponseSchema, { version: 5n, tasks: [
      { task: { id: 3n, kind: ProbeKind.ICMP, target: "1.1.1.1", intervalS: 30, timeoutMs: 1000 }, nodeIds: [1n] },
    ] }),
    queryProbes: async (req) => {
      windows.push({ name: "probes", from: req.from, to: req.to });
      return create(QueryProbesResponseSchema, { level: "1m", stepS: 60, series: [
        { taskId: 3n, samples: [{ ts: req.from, sent: 10, lost: 1, errors: 0, rttMeanUs: 9000 }] },
        { taskId: 9n, samples: [{ ts: req.from, sent: 10, lost: 0, errors: 0, rttMeanUs: 1000 }] },
      ] });
    },
  }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/1");
  expect(await screen.findByRole("heading", { name: "探测 · 丢包率" })).toBeInTheDocument();
  expect(screen.getByRole("heading", { name: "探测 · RTT 均值" })).toBeInTheDocument();
  // uPlot 图例把 series label 渲染成文本
  expect(await screen.findAllByText("ICMP 1.1.1.1")).toHaveLength(2);
  expect(screen.getAllByText("任务 #9")).toHaveLength(2);
  await waitFor(() => expect(windows.filter((w) => w.name === "probes")).toHaveLength(1));
  const m = windows.find((w) => w.name === "metrics")!;
  const p = windows.find((w) => w.name === "probes")!;
  expect([p.from, p.to]).toEqual([m.from, m.to]);
});

it("窗口内没有探测结果时给出去向", async () => {
  renderWithAdmin({
    listNodes: async () => nodesResponse,
    queryMetrics: async () => metricsResponse,
    listProbeTasks: async () => create(ListProbeTasksResponseSchema, { version: 1n, tasks: [] }),
    queryProbes: async () => create(QueryProbesResponseSchema, { level: "1m", stepS: 60, series: [] }),
  }, [{ path: "/nodes/:id", Component: NodeDetail }, { path: "/probes", element: <p>任务页</p> }], "/nodes/1");
  expect(await screen.findByText(/窗口内没有探测结果/)).toBeInTheDocument();
  expect(screen.queryByRole("heading", { name: "探测 · 丢包率" })).toBeNull();
  fireEvent.click(screen.getByRole("link", { name: "管理探测任务" }));
  expect(await screen.findByText("任务页")).toBeInTheDocument();
});

it("主机信息显示 ICMP 是否可用", async () => {
  // nodesResponse 里 facts.icmpAvailable 为 false
  renderWithAdmin({ listNodes: async () => nodesResponse, queryMetrics: async () => metricsResponse,
    listProbeTasks: async () => create(ListProbeTasksResponseSchema, {}), queryProbes: async () => create(QueryProbesResponseSchema, { stepS: 60 }) },
    [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/1");
  const dt = await screen.findByText("ICMP 探测");
  expect(dt.nextElementSibling).toHaveTextContent("不可用");
});
```

若现有夹具的 `nodesResponse` 没有 `facts`，在夹具里补 `facts: { hostname: "h", icmpAvailable: false }`。

- [ ] **Step 2: 运行，确认红在"找不到标题 / 找不到文本"**

Run: `cd web && pnpm exec vitest run src/pages/NodeDetail.test.tsx > /tmp/m3c-t2-red.log 2>&1; echo $?`

- [ ] **Step 3: 实现**

`NodeDetail.tsx`：

```ts
import { lossPercent, rttMeanMs, taskIdsOf, taskLabel, toProbeAligned, type ProbeValue } from "../lib/probes";

// 探测图两张：丢包率与 RTT 均值，每个任务一条线。单位不随数据来——探测样本没有 unit 字段，
// 两种量各自固定。
const PROBE_PANELS: { title: string; unit: string; value: ProbeValue }[] = [
  { title: "探测 · 丢包率", unit: "percent", value: lossPercent },
  { title: "探测 · RTT 均值", unit: "ms", value: rttMeanMs },
];
```

在 `history` 查询后追加（同一对 from/to，同样 `keepPreviousData`，任务列表只为标签）：

```ts
  const probes = useQuery(AdminService.method.queryProbes, { nodeId, from: BigInt(from), to: BigInt(to), maxPoints: 1000 }, {
    enabled: validId, placeholderData: keepPreviousData,
  });
  const tasks = useQuery(AdminService.method.listProbeTasks, {}, { enabled: validId });
  const probeCharts = useMemo(() => {
    if (!probes.data) return [];
    const ids = taskIdsOf(probes.data);
    const labels = ids.map((id) => taskLabel(id, tasks.data?.tasks));
    return PROBE_PANELS.map((p) => ({ ...p, labels, data: toProbeAligned(probes.data!, ids, from, to, p.value) }));
  }, [probes.data, tasks.data, from, to]);
```

渲染：在指标 `.grid` 之后、facts 之前：

```tsx
      {probes.error && <p role="alert" className="error">{errorText(probes.error)}</p>}
      {probes.data && probes.data.series.length === 0 && (
        <p className="muted">窗口内没有探测结果。<Link to="/probes">管理探测任务</Link></p>
      )}
      {probeCharts.length > 0 && probes.data!.series.length > 0 && (
        <div className="grid">
          {probeCharts.map((c) => (
            <div className="card" key={c.title}>
              <h2>{c.title}</h2>
              <Chart data={c.data} labels={c.labels} unit={c.unit} />
            </div>
          ))}
        </div>
      )}
```

facts 里 `agent` 之后加：

```tsx
          <dt>ICMP 探测</dt><dd>{node.facts.icmpAvailable ? "可用" : "不可用"}</dd>
```

- [ ] **Step 4: 运行页面测试与全部 web 测试，确认通过**

Run: `cd web && pnpm exec vitest run > /tmp/m3c-t2-green.log 2>&1; echo $?`

- [ ] **Step 5: 缺陷注入**

- 探测查询用 `from - 60` → 第一条用例的窗口相等断言红。
- 空状态条件改为 `probes.data.series.length >= 0` → 第二条用例 `queryByRole` 为 null 的断言红。
- 标签改用 `String(id)` 而不经 `taskLabel` → 第一条 `ICMP 1.1.1.1` 断言红。
- facts 的 ICMP 行去掉 → 第三条红。

- [ ] **Step 6: 提交**

```bash
git add web/src/pages/NodeDetail.tsx web/src/pages/NodeDetail.test.tsx
git commit -m "web: 节点详情按任务画丢包率与 RTT，与指标图共用窗口"
```

---

### Task 3: 探测任务管理页

**Files:**
- Create: `web/src/pages/ProbeTasks.tsx`、`web/src/pages/ProbeTasks.test.tsx`
- Modify: `web/src/App.tsx`、`web/src/components/Layout.tsx`、`web/src/components/Layout.test.tsx`（若它枚举导航项）、`web/src/styles.css`

**Interfaces:**
- Consumes: `AdminService.method.listProbeTasks / saveProbeTask / deleteProbeTask / listNodes`；`errorText`；`createConnectQueryKey`。
- Produces: 路由 `/probes`，导航"探测任务"。

**页面结构**

- 顶部 `TaskForm`（创建，`id` 为 0n）；下方表格，每行 `TaskRow`：查看态显示 类型 / 目标 / 间隔 / 超时 / 节点名列表 / 操作（编辑、删除）；编辑态整行替换为同一个 `TaskForm`（带初值），保存后回到查看态。
- `TaskForm` 是唯一的表单实现：`kind` 下拉（ICMP / TCP），`target` 文本（`required maxLength={253}`；TCP 时 `placeholder="host:port"`），`interval_s` 数字（`required min={5} max={3600}`），`timeout_ms` 数字（`required min={100} max={5000}`），节点复选框列表（来自 `listNodes`，按现有顺序）。提交按钮 `disabled={pending}`；原生约束不满足时浏览器阻止提交（jsdom 下用 `form.checkValidity()` 守门：`onSubmit` 里 `if (!e.currentTarget.checkValidity()) return;`）。
- 保存请求：`saveProbeTask({ task: { id, kind, target, intervalS, timeoutMs }, nodeIds })`，`nodeIds` 为选中的完整列表，按 bigint 升序。
- 删除两段式：第一次点"删除"变为"确认删除 <目标>"+"取消"，第二次才发请求。
- 成功后只失效 `listProbeTasks` 的 key；错误 `anyError = create.error ?? update.error ?? remove.error`。
- 表格空时显示"还没有探测任务。"

- [ ] **Step 1: 失败测试——`ProbeTasks.test.tsx`**

```ts
import { describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { createConnectQueryKey } from "@connectrpc/connect-query";
import { ConnectError, Code } from "@connectrpc/connect";
import { AdminService, ListNodesResponseSchema, ListProbeTasksResponseSchema, SaveProbeTaskResponseSchema, DeleteProbeTaskResponseSchema, type SaveProbeTaskRequest } from "../gen/probe/v1/admin_pb";
import { ProbeKind } from "../gen/probe/v1/types_pb";
import { renderWithAdmin } from "../test/harness";
import { ProbeTasks } from "./ProbeTasks";

const nodes = create(ListNodesResponseSchema, { nodes: [{ id: 1n, name: "东京" }, { id: 2n, name: "法兰克福" }] });
const tasks = create(ListProbeTasksResponseSchema, { version: 9n, tasks: [
  { task: { id: 3n, kind: ProbeKind.TCP, target: "1.1.1.1:443", intervalS: 30, timeoutMs: 1000 }, nodeIds: [1n, 2n] },
] });
const routes = [{ path: "/probes", Component: ProbeTasks }];

describe("ProbeTasks", () => {
  it("列出任务与分配的节点名", async () => {
    renderWithAdmin({ listNodes: async () => nodes, listProbeTasks: async () => tasks }, routes, "/probes");
    expect(await screen.findByText("1.1.1.1:443")).toBeInTheDocument();
    expect(screen.getByText("东京、法兰克福")).toBeInTheDocument();
    expect(screen.getByText("TCP")).toBeInTheDocument();
  });

  it("创建提交 id 0、完整字段与升序节点列表，并只失效任务列表", async () => {
    const saved: SaveProbeTaskRequest[] = [];
    const { queryClient } = renderWithAdmin({
      listNodes: async () => nodes, listProbeTasks: async () => tasks,
      saveProbeTask: async (req) => { saved.push(req); return create(SaveProbeTaskResponseSchema, { version: 10n }); },
    }, routes, "/probes");
    await screen.findByText("1.1.1.1:443");
    const form = screen.getByRole("form", { name: "新建探测任务" });
    fireEvent.change(within(form).getByLabelText("类型"), { target: { value: String(ProbeKind.ICMP) } });
    fireEvent.change(within(form).getByLabelText("目标"), { target: { value: "8.8.8.8" } });
    fireEvent.change(within(form).getByLabelText("间隔 (s)"), { target: { value: "60" } });
    fireEvent.change(within(form).getByLabelText("超时 (ms)"), { target: { value: "800" } });
    fireEvent.click(within(form).getByLabelText("法兰克福"));
    fireEvent.click(within(form).getByLabelText("东京"));
    fireEvent.submit(form);
    await waitFor(() => expect(saved).toHaveLength(1));
    expect(saved[0].task).toMatchObject({ id: 0n, kind: ProbeKind.ICMP, target: "8.8.8.8", intervalS: 60, timeoutMs: 800 });
    expect(saved[0].nodeIds).toEqual([1n, 2n]);
    const listKey = createConnectQueryKey({ schema: AdminService.method.listProbeTasks, cardinality: "finite" });
    const nodesKey = createConnectQueryKey({ schema: AdminService.method.listNodes, cardinality: "finite" });
    await waitFor(() => expect(queryClient.getQueryState(listKey)?.isInvalidated).toBe(true));
    expect(queryClient.getQueryState(nodesKey)?.isInvalidated).toBe(false);
  });

  it("原生约束不满足时不发请求", async () => {
    const save = vi.fn(async () => create(SaveProbeTaskResponseSchema, {}));
    renderWithAdmin({ listNodes: async () => nodes, listProbeTasks: async () => tasks, saveProbeTask: save }, routes, "/probes");
    await screen.findByText("1.1.1.1:443");
    const form = screen.getByRole("form", { name: "新建探测任务" });
    fireEvent.change(within(form).getByLabelText("间隔 (s)"), { target: { value: "4" } });
    fireEvent.submit(form);
    await new Promise((r) => setTimeout(r, 20));
    expect(save).not.toHaveBeenCalled();
  });

  it("编辑提交整个任务与当前分配", async () => {
    const saved: SaveProbeTaskRequest[] = [];
    renderWithAdmin({
      listNodes: async () => nodes, listProbeTasks: async () => tasks,
      saveProbeTask: async (req) => { saved.push(req); return create(SaveProbeTaskResponseSchema, { version: 10n }); },
    }, routes, "/probes");
    await screen.findByText("1.1.1.1:443");
    fireEvent.click(screen.getByRole("button", { name: "编辑 1.1.1.1:443" }));
    const form = screen.getByRole("form", { name: "编辑探测任务" });
    fireEvent.change(within(form).getByLabelText("超时 (ms)"), { target: { value: "2000" } });
    fireEvent.click(within(form).getByLabelText("法兰克福"));
    fireEvent.submit(form);
    await waitFor(() => expect(saved).toHaveLength(1));
    expect(saved[0].task).toMatchObject({ id: 3n, kind: ProbeKind.TCP, target: "1.1.1.1:443", intervalS: 30, timeoutMs: 2000 });
    expect(saved[0].nodeIds).toEqual([1n]);
  });

  it("删除需要二次确认", async () => {
    const remove = vi.fn(async () => create(DeleteProbeTaskResponseSchema, { version: 11n }));
    renderWithAdmin({ listNodes: async () => nodes, listProbeTasks: async () => tasks, deleteProbeTask: remove }, routes, "/probes");
    await screen.findByText("1.1.1.1:443");
    fireEvent.click(screen.getByRole("button", { name: "删除 1.1.1.1:443" }));
    expect(remove).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "确认删除 1.1.1.1:443" }));
    await waitFor(() => expect(remove).toHaveBeenCalledTimes(1));
    expect(remove.mock.calls[0][0]).toMatchObject({ id: 3n });
  });

  it("服务端错误原文可见", async () => {
    renderWithAdmin({
      listNodes: async () => nodes, listProbeTasks: async () => tasks,
      saveProbeTask: async () => { throw new ConnectError("task.target: target for a TCP task must be host:port; got \"x\"", Code.InvalidArgument); },
    }, routes, "/probes");
    await screen.findByText("1.1.1.1:443");
    const form = screen.getByRole("form", { name: "新建探测任务" });
    fireEvent.change(within(form).getByLabelText("目标"), { target: { value: "x" } });
    fireEvent.change(within(form).getByLabelText("间隔 (s)"), { target: { value: "60" } });
    fireEvent.change(within(form).getByLabelText("超时 (ms)"), { target: { value: "800" } });
    fireEvent.submit(form);
    expect(await screen.findByRole("alert")).toHaveTextContent("task.target: target for a TCP task must be host:port");
  });
});
```

（`within` 来自 `@testing-library/react`。）

- [ ] **Step 2: 运行，确认红在"模块不存在"**

Run: `cd web && pnpm exec vitest run src/pages/ProbeTasks.test.tsx > /tmp/m3c-t3-red.log 2>&1; echo $?`

- [ ] **Step 3: 实现 `ProbeTasks.tsx`**

```tsx
import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { errorText } from "../api/auth";
import { AdminService, type Node, type ProbeTaskDetail } from "../gen/probe/v1/admin_pb";
import { ProbeKind } from "../gen/probe/v1/types_pb";

type Draft = { kind: ProbeKind; target: string; intervalS: string; timeoutMs: string; nodeIds: Set<bigint> };

const emptyDraft = (): Draft => ({ kind: ProbeKind.ICMP, target: "", intervalS: "60", timeoutMs: "1000", nodeIds: new Set() });
const draftOf = (d: ProbeTaskDetail): Draft => ({
  kind: d.task?.kind ?? ProbeKind.ICMP, target: d.task?.target ?? "", intervalS: String(d.task?.intervalS ?? 60),
  timeoutMs: String(d.task?.timeoutMs ?? 1000), nodeIds: new Set(d.nodeIds),
});
const kindName = (k: ProbeKind) => (k === ProbeKind.TCP ? "TCP" : "ICMP");

export function ProbeTasks() {
  const qc = useQueryClient();
  const nodes = useQuery(AdminService.method.listNodes, {});
  const list = useQuery(AdminService.method.listProbeTasks, {});
  // 任务与分配的每次修改都让列表重新拉取；节点列表没有变化，不失效它。
  const refresh = () => qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listProbeTasks, cardinality: "finite" }) });
  const create = useMutation(AdminService.method.saveProbeTask, { onSuccess: refresh });
  const update = useMutation(AdminService.method.saveProbeTask, { onSuccess: refresh });
  const remove = useMutation(AdminService.method.deleteProbeTask, { onSuccess: refresh });
  const submit = (m: typeof create, id: bigint, d: Draft) =>
    m.mutate({ task: { id, kind: d.kind, target: d.target.trim(), intervalS: Number(d.intervalS), timeoutMs: Number(d.timeoutMs) },
      nodeIds: [...d.nodeIds].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0)) });
  const anyError = create.error ?? update.error ?? remove.error;
  if (list.error) return <p role="alert" className="error">{errorText(list.error)}</p>;
  return (
    <section>
      <h1>探测任务</h1>
      <TaskForm title="新建探测任务" nodes={nodes.data?.nodes ?? []} initial={emptyDraft()} pending={create.isPending}
        onSubmit={(d) => submit(create, 0n, d)} />
      {anyError && <p role="alert" className="error">{errorText(anyError)}</p>}
      <div className="table-scroll">
        <table className="nodes">
          <thead><tr><th>类型</th><th>目标</th><th>间隔 (s)</th><th>超时 (ms)</th><th>节点</th><th /></tr></thead>
          <tbody>
            {list.data?.tasks.map((d) => (
              <TaskRow key={String(d.task?.id)} detail={d} nodes={nodes.data?.nodes ?? []} saving={update.isPending} deleting={remove.isPending}
                onSave={(draft) => submit(update, d.task?.id ?? 0n, draft)} onDelete={() => remove.mutate({ id: d.task?.id ?? 0n })} />
            ))}
          </tbody>
        </table>
      </div>
      {list.data && list.data.tasks.length === 0 && <p className="muted">还没有探测任务。</p>}
    </section>
  );
}

// 创建与编辑共用；字段约束用原生属性表达，hub 的 probelimit 是最终裁决，错误原文回到页面上。
function TaskForm({ title, nodes, initial, pending, onSubmit, onCancel }: {
  title: string; nodes: Node[]; initial: Draft; pending: boolean; onSubmit: (d: Draft) => void; onCancel?: () => void;
}) {
  const [draft, setDraft] = useState(initial);
  const toggle = (id: bigint) => {
    const next = new Set(draft.nodeIds);
    if (next.has(id)) next.delete(id); else next.add(id);
    setDraft({ ...draft, nodeIds: next });
  };
  const handle = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!e.currentTarget.checkValidity()) return;
    onSubmit(draft);
  };
  return (
    <form className="card task-form" aria-label={title} onSubmit={handle}>
      <div className="row">
        <label>类型
          <select value={draft.kind} onChange={(e) => setDraft({ ...draft, kind: Number(e.target.value) as ProbeKind })}>
            <option value={ProbeKind.ICMP}>ICMP</option>
            <option value={ProbeKind.TCP}>TCP</option>
          </select>
        </label>
        <label>目标<input required maxLength={253} placeholder={draft.kind === ProbeKind.TCP ? "host:port" : "IP 或主机名"} value={draft.target}
          onChange={(e) => setDraft({ ...draft, target: e.target.value })} /></label>
        <label>间隔 (s)<input type="number" required min={5} max={3600} value={draft.intervalS} onChange={(e) => setDraft({ ...draft, intervalS: e.target.value })} /></label>
        <label>超时 (ms)<input type="number" required min={100} max={5000} value={draft.timeoutMs} onChange={(e) => setDraft({ ...draft, timeoutMs: e.target.value })} /></label>
      </div>
      <fieldset className="node-picks">
        <legend>分配到节点</legend>
        {nodes.map((n) => (
          <label key={String(n.id)}><input type="checkbox" checked={draft.nodeIds.has(n.id)} onChange={() => toggle(n.id)} />{n.name}</label>
        ))}
      </fieldset>
      <div className="row">
        <button type="submit" disabled={pending}>{onCancel ? "保存" : "创建"}</button>
        {onCancel && <button type="button" className="link" onClick={onCancel}>取消</button>}
      </div>
    </form>
  );
}

function TaskRow({ detail, nodes, saving, deleting, onSave, onDelete }: {
  detail: ProbeTaskDetail; nodes: Node[]; saving: boolean; deleting: boolean; onSave: (d: Draft) => void; onDelete: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const t = detail.task!;
  const names = detail.nodeIds.map((id) => nodes.find((n) => n.id === id)?.name ?? `#${id}`).join("、");
  if (editing) {
    return (
      <tr><td colSpan={6}>
        <TaskForm title="编辑探测任务" nodes={nodes} initial={draftOf(detail)} pending={saving}
          onSubmit={(d) => { onSave(d); setEditing(false); }} onCancel={() => setEditing(false)} />
      </td></tr>
    );
  }
  return (
    <tr>
      <td>{kindName(t.kind)}</td>
      <td>{t.target}</td>
      <td>{t.intervalS}</td>
      <td>{t.timeoutMs}</td>
      <td>{names || <span className="muted">未分配</span>}</td>
      <td>
        <button type="button" className="link" aria-label={`编辑 ${t.target}`} onClick={() => setEditing(true)}>编辑</button>{" "}
        {confirming ? (
          <>
            <button type="button" className="danger" disabled={deleting} onClick={onDelete}>确认删除 {t.target}</button>{" "}
            <button type="button" className="link" onClick={() => setConfirming(false)}>取消</button>
          </>
        ) : (
          <button type="button" className="link danger" aria-label={`删除 ${t.target}`} onClick={() => setConfirming(true)}>删除</button>
        )}
      </td>
    </tr>
  );
}
```

`App.tsx` 在 `Layout` 的 children 加 `{ path: "probes", Component: ProbeTasks }`；`Layout.tsx` 加 `<NavLink to="/probes">探测任务</NavLink>`（放在"节点"之后）；`styles.css` 加：

```css
.task-form .node-picks { display: flex; flex-wrap: wrap; gap: .5rem 1rem; border: 1px solid var(--line); padding: .5rem .75rem; }
.task-form .node-picks label { display: inline-flex; align-items: center; gap: .3rem; }
```

- [ ] **Step 4: 运行全部 web 测试与类型检查**

Run: `cd web && pnpm exec vitest run > /tmp/m3c-t3-green.log 2>&1; echo $?` 与 `cd web && pnpm run typecheck > /tmp/m3c-t3-tsc.log 2>&1; echo $?`

- [ ] **Step 5: 缺陷注入**

- `nodeIds` 不排序 → 创建用例 `[1n, 2n]` 断言红（点击顺序是 2 再 1）。
- 删除按钮直接调用 `onDelete` → 二次确认用例红。
- `refresh` 改为 `qc.invalidateQueries()`（无 key）→ `nodesKey isInvalidated === false` 断言红。
- `handle` 去掉 `checkValidity` → 原生约束用例红。
- 编辑提交 `id: 0n` → 编辑用例红。

- [ ] **Step 6: 提交**

```bash
git add web/src/pages/ProbeTasks.tsx web/src/pages/ProbeTasks.test.tsx web/src/App.tsx web/src/components/Layout.tsx web/src/components/Layout.test.tsx web/src/styles.css
git commit -m "web: 探测任务管理页，创建与编辑共用一份表单并提交完整分配"
```

---

## 收尾：控制端的浏览器验收

代码任务结束后，由控制端用真实浏览器对着 `make e2e` 那样起的 hub（两个容器 agent）走一遍：登录 → 探测任务页创建一个 ICMP 任务（目标 127.0.0.1，分配到两个节点）与一个 TCP 任务（宿主 hub 地址）→ 约两分钟后进入一个节点详情看到"探测 · 丢包率""探测 · RTT 均值"两张图各有两条线、图例是"ICMP 127.0.0.1"等 → 编辑任务改超时并保存、列表刷新 → 删除 TCP 任务、详情页图上该线仍在（历史保留）且图例退回"任务 #N" → 主机信息显示"ICMP 探测 可用"→ 登出。任何一步不符即回到对应任务修复。

## 自检

**Spec 覆盖**：§8.3 的 `QueryProbes` 形状（level/step/按任务序列/optional rtt）在 Task 1 的对齐函数与 Task 2 的图上体现；proto 注释的"已删除任务历史仍返回"由 `taskLabel` 的退回分支覆盖；`SaveProbeTask` 的"完整分配列表"由 Task 3 的提交与测试钉住；§8.2 的 `icmp_available` 在 Task 2 显示；agent-first 的"错误由服务端说清"由 Task 3 的错误原文用例钉住。

**类型一致性**：`gridOf` 在 Task 1 定义并被 `toAligned` 与 `toProbeAligned` 共用；`ProbeValue`/`lossPercent`/`rttMeanMs`/`taskIdsOf`/`taskLabel` 在 Task 1 定义、Task 2 使用；`TaskForm`/`TaskRow`/`Draft` 只在 Task 3 内部。

**占位扫描**：无 TBD；三个任务各自可独立运行与测试。

## 执行修正

执行中发现的计划缺陷与实际采用的做法。计划正文保持原样，读计划时以本节为准。

### Task 2（节点详情）
- Step 5 的空状态注入写成"把 series.length === 0 改为 >= 0"，这只会让非空时也显示提示，指定断言不会红；实际注入改为把图表渲染条件 `series.length > 0` 改成 `>= 0`。
- 用例假设 uPlot 图例文字可被 testing-library 查询；现有 NodeDetail 测试把 Chart mock 成不含文字的 div。实际让测试桩呈现传入的 labels，生产 Chart 不变。
- 用例示意里的 nodesResponse / metricsResponse 与节点 1 改用现有函数式夹具与节点 7。
- 图表渲染条件里 `probeCharts.length > 0` 恒真，已去掉；tasks 查询不轮询、由任务页的失效驱动刷新，已写成注释。

### Task 3（任务管理页）
- TaskRow 原文在调用 mutate 后立刻 setEditing(false)：保存失败也会关闭表单、丢掉草稿。实际改为成功回调后才退出编辑，失败保留草稿并显示服务端错误；Nodes.tsx 的 NodeEditor 同形，一并改掉。
- 创建成功后表单未复位，再点一次会建出重复任务（hub 对 (kind, target) 无唯一约束）；实际用 key 重挂载复位。
- 类型到标签的映射原文在 taskLabel、kindName 与下拉选项三处各写一份；实际收到 lib/probes.ts 的 PROBE_KINDS 表与 kindLabel 一处。
- 两页各行共用一个 update mutation observer，重叠的 mutate 只回调最后一次；因此任一行保存挂起时禁用全部行的保存，注释与跨行用例钉住。
- 两页的错误提示原为 `a.error ?? b.error` 链，创建的旧错误会在之后编辑成功时滞留；实际统一为"最新一次操作的错误优先"（useLatestError，任一 mutation 开始即清空、只记录最新序号的失败）。
- 提交的 nodeIds 与当前节点列表求交，编辑期间被删的节点自然掉出，不让 hub 以 NotFound 拒绝整次保存。
- 用例观察方式四处与运行时不符：getByText("TCP") 同时命中 option 与单元格（改按 cell 查）；createConnectQueryKey 不带 transport/input 时 getQueryState 为 undefined 且活跃查询刷新会清掉 isInvalidated（改为观察重取与未激活缓存）；零参数 vi.fn 的 calls[0][0] 触发 TS2493；非法间隔用例未填 target，required 先于 min 拦截。
- 简报 TaskForm 用 form 内的 div.row，现有 .row 不换行；实际合并 `form.row, .task-form .row` 并让 .task-form 恢复 white-space: normal 抵消表格单元格的 nowrap。
- 提交时与节点列表求交的写法在节点列表未到达时会把全部分配静默清空；实际与 Nodes 页同一做法，节点列表就绪前不渲染任何可提交的表单，求交只在列表到达后发生。

### 整分支终审后的修正
- Nodes 页的删除、换 token、排序原为请求返回即解禁而列表尚未刷新，与探测任务页"挂起持续到刷新完成"口径不一致；统一为 onSuccess 返回刷新 promise。
- 节点详情页任务列表查询失败原来无处显示，图例会全部退回"任务 #N"而像"全部被删"；补 alert。
- 同类型同目标的两个任务图例同名（hub 对 (kind, target) 无唯一约束）；只在同窗口内碰撞时给标签附加 ` #id`。
- 断言页面状态不得持有旧元素引用：toHaveTextContent 对已卸载节点仍通过、toBeInTheDocument 对被 React 复用的 DOM 也通过；"编辑态仍在"的用例改为重新按角色与名称查询保存按钮。
- 任务表格补齐与节点页同形的可访问性结构（可聚焦的滚动 region、操作列表头）。

### 后续跟进（不在本计划）
- web/tsconfig.app.json 未开 strict，非空断言与 find() 的返回类型没有编译期约束；这是既有配置。
