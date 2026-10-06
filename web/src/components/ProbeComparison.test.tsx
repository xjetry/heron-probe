import { create } from "@bufbuild/protobuf";
import { Code, ConnectError, type HandlerContext } from "@connectrpc/connect";
import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { AlignedData } from "uplot";
import { ProbeComparison, type ProbeComparisonMethods } from "./ProbeComparison";
import { AdminService, ListNodesResponseSchema, ListProbeTasksResponseSchema } from "../gen/heron/v1/admin_pb";
import { PublicService } from "../gen/heron/v1/public_pb";
import { ListProbeComparisonNodesResponseSchema, QueryProbeComparisonResponseSchema, QueryProbesResponseSchema, type QueryProbeComparisonRequest } from "../gen/heron/v1/query_pb";
import { ProbeKind } from "../gen/heron/v1/types_pb";
import { ProbeTasks } from "../pages/ProbeTasks";
import { NodePage } from "../public/NodePage";
import { renderWithAdmin, renderWithService } from "../test/harness";

vi.mock("./Chart", () => ({
  Chart: ({ labels, unit, data }: { labels: string[]; unit: string; data: AlignedData }) => (
    <div data-testid={`chart-${unit}`} data-labels={labels.join("|")} data-values={JSON.stringify(data.slice(1).map((column) => column[0]))} />
  ),
}));

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

const methods: ProbeComparisonMethods = {
  list: AdminService.method.listProbeComparisonNodes,
  query: AdminService.method.queryProbeComparison,
};
const names = [
  { id: 1n, name: "edge" },
  { id: 2n, name: "edge" },
  { id: 3n, name: "edge" },
  { id: 4n, name: "gone" },
  { id: 5n, name: "ok" },
];

function pinClock(ms = 1_700_000_000_000) {
  vi.spyOn(Date, "now").mockReturnValue(ms);
  const to = Math.floor(ms / 1000) + 60;
  const from = to - 86400;
  return { from, to, ts: from - (from % 60) };
}

function renderComparison(impl: Parameters<typeof renderWithAdmin>[0], nodes = names, path = "/c") {
  return renderWithAdmin(impl, [{ path, element: <ProbeComparison taskId={9n} methods={methods} nodes={nodes} /> }], path);
}

function listed(nodeIds: bigint[], maxNodesPerQuery: number) {
  return create(ListProbeComparisonNodesResponseSchema, {
    kind: ProbeKind.ICMP, target: "edge.example", nodeIds, maxNodesPerQuery,
  });
}

function chunkOf(req: QueryProbeComparisonRequest, ts: number, unavailable: readonly bigint[] = []) {
  return create(QueryProbeComparisonResponseSchema, {
    level: "1m", stepS: 60, unavailableNodeIds: [...unavailable],
    series: req.nodeIds.filter((id) => !unavailable.includes(id)).map((id) => ({ nodeId: id, samples: [{ ts: BigInt(ts), sent: 1, lost: 0, errors: 1 }] })),
  });
}

it.each([2, 4])("块大小取自 List 的 max_nodes_per_query=%i，不写死", async (max) => {
  pinClock();
  const seen: bigint[][] = [];
  renderComparison({
    listProbeComparisonNodes: async () => listed([1n, 2n, 3n, 4n, 5n], max),
    queryProbeComparison: async (req) => {
      seen.push([...req.nodeIds]);
      return create(QueryProbeComparisonResponseSchema, { level: "1m", stepS: 60, series: req.nodeIds.map((id) => ({ nodeId: id, samples: [] })) });
    },
  });
  expect(await screen.findByRole("heading", { name: "ICMP edge.example" })).toBeInTheDocument();
  const want = max === 2 ? [[1n, 2n], [3n, 4n], [5n]] : [[1n, 2n, 3n, 4n], [5n]];
  expect(seen).toEqual(want);
});

it("max_nodes_per_query 为 0 时显示协议错误，不发分块请求", async () => {
  pinClock();
  const queryProbeComparison = vi.fn();
  renderComparison({
    listProbeComparisonNodes: async () => listed([1n, 2n], 0),
    queryProbeComparison,
  });
  expect(await screen.findByRole("alert")).toHaveTextContent("分块上限无效");
  expect(queryProbeComparison).not.toHaveBeenCalled();
  expect(screen.queryByTestId("chart-percent")).toBeNull();
});

it("List NotFound 只说明没有可对比的节点，不展示服务端原文", async () => {
  pinClock();
  renderComparison({
    listProbeComparisonNodes: async () => { throw new ConnectError("task missing secret", Code.NotFound); },
  });
  expect(await screen.findByText("没有可对比的节点。")).toBeInTheDocument();
  expect(screen.queryByText(/secret/)).toBeNull();
  expect(screen.queryByRole("alert")).toBeNull();
});

it("FailedPrecondition 显示服务端消息", async () => {
  pinClock();
  renderComparison({
    listProbeComparisonNodes: async () => listed([1n], 8),
    queryProbeComparison: async () => { throw new ConnectError("读量超出额度，请缩小窗口", Code.FailedPrecondition); },
  });
  expect(await screen.findByRole("alert")).toHaveTextContent("读量超出额度，请缩小窗口");
});

it("全超时 100%、全本地错误 0%、混合按 lost/sent，三者都不进 RTT 图；同名消歧，不可见单列", async () => {
  const { ts } = pinClock();
  renderComparison({
    listProbeComparisonNodes: async () => listed([1n, 2n, 3n, 5n, 4n], 8),
    queryProbeComparison: async (req) => create(QueryProbeComparisonResponseSchema, {
      level: "1m", stepS: 60, unavailableNodeIds: req.nodeIds.filter((id) => id === 4n),
      series: [
        { nodeId: 1n, samples: [{ ts: BigInt(ts), sent: 10, lost: 10, errors: 0 }] },
        { nodeId: 2n, samples: [{ ts: BigInt(ts), sent: 5, lost: 0, errors: 5 }] },
        { nodeId: 3n, samples: [{ ts: BigInt(ts), sent: 10, lost: 4, errors: 3 }] },
        { nodeId: 5n, samples: [{ ts: BigInt(ts), sent: 10, lost: 1, errors: 0, rttMeanUs: 0 }] },
      ].filter((series) => req.nodeIds.includes(series.nodeId)),
    }),
  });
  const loss = await screen.findByTestId("chart-percent");
  expect(loss.dataset.labels).toBe("edge #1|edge #2|edge #3|ok");
  expect(loss.dataset.values).toBe("[100,0,40,10]");
  const rtt = screen.getByTestId("chart-ms");
  expect(rtt.dataset.labels).toBe("ok");
  expect(rtt.dataset.values).toBe("[0]");
  expect(screen.getByText("该图窗口内没有读数的节点（3 个）")).toBeInTheDocument();
  expect(screen.getByText("edge #1").closest("ul")?.textContent).toContain("edge #2");
  expect(screen.getByText("已不可见的节点（1 个）")).toBeInTheDocument();
  expect(screen.getByText("gone")).toBeInTheDocument();
  expect(screen.getByText("edge #1").closest("[data-testid]")).toBeNull();
});

it("未完成的块不算无结果，全部到齐才画", async () => {
  const { ts } = pinClock();
  let release!: () => void;
  const gate = new Promise<void>((resolve) => { release = resolve; });
  renderComparison({
    listProbeComparisonNodes: async () => listed([1n, 2n, 3n, 4n], 2),
    queryProbeComparison: async (req) => {
      if (req.nodeIds.includes(3n)) await gate;
      return chunkOf(req, ts);
    },
  }, names.slice(0, 4));
  expect(await screen.findByText("加载中…")).toBeInTheDocument();
  await waitFor(() => expect(screen.queryByText("加载中…")).toBeInTheDocument());
  expect(screen.queryByText(/没有读数/)).toBeNull();
  expect(screen.queryByText("edge")).toBeNull();
  expect(screen.queryByTestId("chart-percent")).toBeNull();
  await act(async () => { release(); });
  expect(await screen.findByText("该图窗口内没有读数的节点（4 个）")).toBeInTheDocument();
});

it("同时至多两块在飞", async () => {
  const { ts } = pinClock();
  let inFlight = 0;
  let maxInFlight = 0;
  const resolvers: (() => void)[] = [];
  renderComparison({
    listProbeComparisonNodes: async () => listed([1n, 2n, 3n, 4n], 1),
    queryProbeComparison: async (req) => {
      inFlight += 1;
      maxInFlight = Math.max(maxInFlight, inFlight);
      await new Promise<void>((resolve) => { resolvers.push(resolve); });
      inFlight -= 1;
      return chunkOf(req, ts);
    },
  }, names.slice(0, 4));
  await waitFor(() => expect(resolvers).toHaveLength(2));
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 30)); });
  expect(resolvers).toHaveLength(2);
  expect(maxInFlight).toBe(2);
  await act(async () => { resolvers[0](); });
  await waitFor(() => expect(resolvers).toHaveLength(3));
  expect(maxInFlight).toBe(2);
  await act(async () => {
    for (let i = 0; i < 6; i++) {
      const batch = resolvers.splice(0);
      batch.forEach((resolve) => resolve());
      await new Promise((r) => setTimeout(r, 0));
    }
  });
  expect(await screen.findByText("该图窗口内没有读数的节点（4 个）")).toBeInTheDocument();
  expect(maxInFlight).toBe(2);
});

it("部分块失败显示错误与重试，不自动再请求", async () => {
  pinClock();
  const queryProbeComparison = vi.fn(async () => { throw new ConnectError("chunk down", Code.Unavailable); });
  renderComparison({
    listProbeComparisonNodes: async () => listed([1n, 2n, 3n], 1),
    queryProbeComparison,
  }, names.slice(0, 3));
  expect(await screen.findByRole("alert")).toHaveTextContent(/^chunk down/);
  const calls = queryProbeComparison.mock.calls.length;
  expect(calls).toBeGreaterThan(0);
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 40)); });
  expect(queryProbeComparison).toHaveBeenCalledTimes(calls);
  fireEvent.click(screen.getByRole("button", { name: "重试" }));
  await waitFor(() => expect(queryProbeComparison.mock.calls.length).toBeGreaterThan(calls));
});

it("刷新与换窗口取消尚未完成的块，离开页面同样取消", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  vi.setSystemTime(1_700_000_000_000);
  const signals: AbortSignal[] = [];
  const hang = async (_req: QueryProbeComparisonRequest, ctx: HandlerContext) => {
    signals.push(ctx.signal);
    await new Promise<void>((_resolve, reject) => {
      if (ctx.signal.aborted) reject(new ConnectError("canceled", Code.Canceled));
      else ctx.signal.addEventListener("abort", () => reject(new ConnectError("canceled", Code.Canceled)), { once: true });
    });
    return create(QueryProbeComparisonResponseSchema, { level: "1m", stepS: 60 });
  };
  const { router } = renderWithAdmin({
    listProbeComparisonNodes: async () => listed([1n], 1),
    queryProbeComparison: hang,
  }, [
    { path: "/c", element: <ProbeComparison taskId={9n} methods={methods} nodes={names.slice(0, 1)} /> },
    { path: "/gone", element: <p>离开</p> },
  ], "/c");
  await waitFor(() => expect(signals).toHaveLength(1));
  const first = signals[0];
  fireEvent.click(screen.getByRole("button", { name: "1h" }));
  await waitFor(() => expect(first.aborted).toBe(true));
  await waitFor(() => expect(signals.length).toBeGreaterThanOrEqual(2));
  const second = signals.at(-1)!;
  await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
  await waitFor(() => expect(second.aborted).toBe(true));
  await act(async () => { await router.navigate("/gone"); });
  expect(signals.at(-1)!.aborted).toBe(true);
  expect(screen.getByText("离开")).toBeInTheDocument();
});

it("刷新期间保留上一张完整的图，并标成更新中", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  vi.setSystemTime(1_700_000_000_000);
  const to = Math.floor(1_700_000_000_000 / 1000) + 60;
  const ts = (to - 86400) - ((to - 86400) % 60);
  let queries = 0;
  let release!: () => void;
  const gate = new Promise<void>((resolve) => { release = resolve; });
  renderComparison({
    listProbeComparisonNodes: async () => listed([1n], 1),
    queryProbeComparison: async (req) => {
      queries += 1;
      if (queries > 1) await gate;
      return chunkOf(req, ts);
    },
  }, names.slice(0, 1));
  expect(await screen.findByTestId("chart-percent")).toBeInTheDocument();
  await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
  expect(await screen.findByText("更新中")).toBeInTheDocument();
  expect(screen.getByTestId("chart-percent")).toBeInTheDocument();
  expect(screen.queryByText("加载中…")).toBeNull();
  await act(async () => { release(); });
  await waitFor(() => expect(screen.queryByText("更新中")).toBeNull());
  expect(screen.getByRole("heading", { name: "ICMP edge.example" })).toBeInTheDocument();
});

it("无读数名单超过 8 个时折叠", async () => {
  const { ts } = pinClock();
  const ids = [1n, 2n, 3n, 4n, 5n, 6n, 7n, 8n, 9n];
  renderComparison({
    listProbeComparisonNodes: async () => listed(ids, 9),
    queryProbeComparison: async (req) => chunkOf(req, ts),
  }, ids.map((id) => ({ id, name: `n${id}` })));
  // 全是本地错误：丢包率是 0，要画出来；RTT 没有读数，九个名字折进 details，不默认展开。
  const summaries = await screen.findAllByText("该图窗口内没有读数的节点（9 个）");
  expect(summaries).toHaveLength(1);
  expect(summaries[0].closest("details")).not.toHaveAttribute("open");
  expect(screen.getByTestId("chart-percent")).toBeInTheDocument();
  expect(screen.queryByTestId("chart-ms")).toBeNull();
});

it("管理端任务行进入对比页，公开端只给已标注的任务入口", async () => {
  const { router } = renderWithAdmin({
    listNodes: async () => create(ListNodesResponseSchema, { nodes: [{ id: 1n, name: "n" }] }),
    listProbeTasks: async () => create(ListProbeTasksResponseSchema, { tasks: [{ task: { id: 3n, kind: ProbeKind.ICMP, target: "1.1.1.1", intervalS: 60, timeoutMs: 1000 }, nodeIds: [1n] }] }),
  }, [
    { path: "/probes", Component: ProbeTasks },
    { path: "/probes/:id/compare", element: <p>对比页</p> },
  ], "/probes");
  fireEvent.click(await screen.findByRole("link", { name: "对比" }));
  await waitFor(() => expect(router.state.location.pathname).toBe("/probes/3/compare"));
  expect(screen.getByText("对比页")).toBeInTheDocument();
});

it("公开节点页的已标注任务链到 /probes/:id，未标注的不给入口", async () => {
  const { router } = renderWithService(PublicService, {
    getSnapshot: async () => ({ nodes: [{ id: 7n, name: "edge" }] }),
    queryMetrics: async () => ({ level: "1m", stepS: 60, ts: [], series: [] }),
    queryProbes: async () => create(QueryProbesResponseSchema, { level: "1m", stepS: 60, series: [
      { taskId: 3n, kind: ProbeKind.TCP, target: "example.com:443" },
      { taskId: 4n, kind: ProbeKind.UNSPECIFIED },
    ] }),
  }, [
    { path: "/nodes/:id", Component: NodePage },
    { path: "/probes/:id", element: <p>公开对比</p> },
  ], "/nodes/7");
  const link = await screen.findByRole("link", { name: "各节点对比：TCP example.com:443" });
  expect(screen.queryByRole("link", { name: /任务 #4/ })).toBeNull();
  fireEvent.click(link);
  await waitFor(() => expect(router.state.location.pathname).toBe("/probes/3"));
  expect(screen.getByText("公开对比")).toBeInTheDocument();
});
