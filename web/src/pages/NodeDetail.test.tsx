import { create } from "@bufbuild/protobuf";
import { QueryProbesResponseSchema } from "../gen/probe/v1/query_pb";
import { ProbeKind } from "../gen/probe/v1/types_pb";
import { Code, ConnectError } from "@connectrpc/connect";
import { act, screen, fireEvent, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithAdmin, type AdminImpl } from "../test/harness";
import { NodeDetail } from "./NodeDetail";

afterEach(() => vi.useRealTimers());

vi.mock("../components/Chart", () => ({
  Chart: ({ labels, unit, data }: { labels: string[]; unit: string; data: unknown[] }) => (
    <div data-testid="chart" data-labels={labels.join(",")} data-unit={unit} data-points={String((data[0] as unknown[]).length)}>
      {labels.map((label) => <span key={label}>{label}</span>)}
    </div>
  ),
}));

const listNodes = async () => ({
  nodes: [{ id: 7n, name: "db-01", public: false, note: "", sortOrder: 0, createdAt: 0n,
    facts: { hostname: "db-01.internal", os: "Debian 12", kernel: "6.1", arch: "amd64", virtualization: "kvm", cpuModel: "EPYC", cpuCores: 4, agentVersion: "dev", icmpAvailable: false } }],
});

const trafficOf = (nodeId: bigint) => ({
  nodeId, name: "db-01",
  traffic: { totalRx: 10n * 1024n ** 3n, totalTx: 5n * 1024n ** 3n, periodRx: 1024n ** 3n, periodTx: 512n * 1024n ** 2n,
    periodStart: 1_756_684_800n, nextResetAt: 1_759_276_800n, resetDay: 1 },
});
const getTraffic = async () => ({ timezone: "UTC", now: 1_757_000_000n, nodes: [trafficOf(7n)] });

const defaultImpl = {
  listNodes, getTraffic,
  queryMetrics: async () => ({ level: "1m", stepS: 60, ts: [], series: [] }),
  queryProbes: async () => create(QueryProbesResponseSchema, { level: "1m", stepS: 60 }),
} satisfies AdminImpl;

it.each(["listNodes", "getTraffic"] as const)("详情 %s 刷新失败保留内容与校正草稿", async (method) => {
  let fail = false;
  const { queryClient } = renderWithAdmin({ ...defaultImpl, [method]: async () => {
    if (fail) throw new ConnectError(`${method} refresh failed`, Code.Unavailable);
    return defaultImpl[method]();
  } }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
  const input = await screen.findByLabelText("本周期下行 (GiB)");
  await screen.findByRole("heading", { name: "db-01" });
  fireEvent.change(input, { target: { value: "2.5" } });
  fail = true;
  await act(async () => { await queryClient.refetchQueries(); });
  expect(await screen.findByRole("alert")).toHaveTextContent(`${method} refresh failed`);
  expect(screen.getByLabelText("本周期下行 (GiB)")).toBe(input);
  expect(input).toHaveValue("2.5");
  expect(screen.getByRole("heading", { name: "db-01" })).toBeInTheDocument();
  expect(screen.getByText("↓ 1.0 GiB ↑ 512 MiB")).toBeInTheDocument();
});

it("四个查询同文刷新失败只显示一条", async () => {
  let fail = false;
  const failing = <A extends unknown[], R>(impl: (...args: A) => Promise<R>) => async (...args: A): Promise<R> => {
    if (fail) throw new ConnectError("hub unreachable", Code.Unavailable);
    return impl(...args);
  };
  const { queryClient } = renderWithAdmin({
    listNodes: failing(defaultImpl.listNodes),
    getTraffic: failing(defaultImpl.getTraffic),
    queryMetrics: failing(defaultImpl.queryMetrics),
    queryProbes: failing(defaultImpl.queryProbes),
  }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
  await screen.findByRole("heading", { name: "db-01" });
  fail = true;
  await act(async () => { await queryClient.refetchQueries(); });
  expect((await screen.findAllByRole("alert")).map((a) => a.textContent)).toEqual(["hub unreachable"]);
  expect(screen.getByRole("heading", { name: "db-01" })).toBeInTheDocument();
});

it("listNodes 从未成功但历史与流量已就绪时仍显示图表、流量卡与切窗按钮，只加错误横幅", async () => {
  renderWithAdmin({ ...defaultImpl, listNodes: async () => { throw new ConnectError("nodes down", Code.Unavailable); } },
    [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
  expect(await screen.findByRole("alert")).toHaveTextContent("nodes down");
  expect(await screen.findAllByTestId("chart")).toHaveLength(7);
  expect(screen.getByRole("button", { name: "24h" })).toBeInTheDocument();
  expect(screen.getByText("↓ 1.0 GiB ↑ 512 MiB")).toBeInTheDocument();
  expect(screen.getByRole("heading", { name: "节点 #7" })).toBeInTheDocument();
});

it("listNodes 挂起、历史就绪时图表与“加载中…”同时在", async () => {
  let release!: (v: Awaited<ReturnType<typeof listNodes>>) => void;
  const pending = new Promise<Awaited<ReturnType<typeof listNodes>>>((resolve) => { release = resolve; });
  renderWithAdmin({ ...defaultImpl, listNodes: () => pending }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
  expect(await screen.findAllByTestId("chart")).toHaveLength(7);
  expect(screen.getByText("加载中…")).toBeInTheDocument();
  expect(screen.queryByText("主机名")).toBeNull();
  await act(async () => { release(await listNodes()); });
});

it("窗口每分钟前进后请求失败，图表与级别仍在并带横幅和“非当前窗口”提示", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  let fail = false;
  const queryMetrics = vi.fn<NonNullable<AdminImpl["queryMetrics"]>>(async () => {
    if (fail) throw new ConnectError("history down", Code.Unavailable);
    return { level: "1m", stepS: 60, ts: [], series: [] };
  });
  renderWithAdmin({ ...defaultImpl, queryMetrics }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
  expect(await screen.findAllByTestId("chart")).toHaveLength(7);
  await screen.findByText(/级别 1m，每点 60s/);
  fail = true;
  // 窗口右端每分钟前进一次（History.tsx 的 REFRESH_MS），换键后的这次请求失败。
  await act(async () => { await vi.advanceTimersByTimeAsync(60_000 + 100); });
  expect(await screen.findByRole("alert")).toHaveTextContent("history down");
  expect(screen.getAllByTestId("chart")).toHaveLength(7);
  expect(screen.getByText(/级别 1m，每点 60s/)).toBeInTheDocument();
  expect(screen.getByText(/图表还不是 24h 窗口的结果/)).toBeInTheDocument();
});

it("切到另一个节点、新节点历史未返回时不显示上一个节点的图表与级别", async () => {
  let releaseNode8!: () => void;
  const node8Gate = new Promise<void>((resolve) => { releaseNode8 = resolve; });
  const listTwoNodes = async () => ({
    nodes: [
      { id: 7n, name: "db-07", public: false, note: "", sortOrder: 0, createdAt: 0n },
      { id: 8n, name: "db-08", public: false, note: "", sortOrder: 1, createdAt: 0n },
    ],
  });
  const queryMetrics = vi.fn<NonNullable<AdminImpl["queryMetrics"]>>(async (req) => {
    if (req.nodeId === 8n) await node8Gate;
    return req.nodeId === 8n ? { level: "5m", stepS: 300, ts: [], series: [] } : { level: "1m", stepS: 60, ts: [], series: [] };
  });
  const { router } = renderWithAdmin({ ...defaultImpl, listNodes: listTwoNodes, queryMetrics }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
  await screen.findByRole("heading", { name: "db-07" });
  await screen.findByText(/级别 1m，每点 60s/);
  await act(async () => { router.navigate("/nodes/8"); });
  expect(await screen.findByRole("heading", { name: "db-08" })).toBeInTheDocument();
  // 8 的历史还没回来：不能把 7 的"级别 1m"标签或图表当成 8 的显示，这段时间没有图表比显示错的更安全。
  expect(screen.queryByText(/级别 1m/)).toBeNull();
  expect(screen.queryAllByTestId("chart")).toHaveLength(0);
  await act(async () => { releaseNode8(); });
  expect(await screen.findByText(/级别 5m，每点 300s/)).toBeInTheDocument();
  expect(screen.getAllByTestId("chart")).toHaveLength(7);
});

it("头部链接到该节点的告警事件", async () => {
  renderWithAdmin(defaultImpl, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
  expect((await screen.findByRole("link", { name: "告警事件" }))).toHaveAttribute("href", "/events?node=7");
});

it("同窗口同名任务在两张探测图中带编号区分", async () => {
  renderWithAdmin({
    ...defaultImpl,
    queryProbes: async () => create(QueryProbesResponseSchema, { stepS: 60, series: [
      { taskId: 7n, kind: ProbeKind.ICMP, target: "1.1.1.1" },
      { taskId: 3n, kind: ProbeKind.ICMP, target: "1.1.1.1" },
    ] }),
  }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
  await screen.findAllByText("ICMP 1.1.1.1 #7");
  const charts = screen.getAllByTestId("chart").filter((chart) => chart.dataset.labels?.includes("ICMP"));
  expect(charts.map((chart) => chart.dataset.labels)).toEqual([
    "ICMP 1.1.1.1 #7,ICMP 1.1.1.1 #3",
    "ICMP 1.1.1.1 #7,ICMP 1.1.1.1 #3",
  ]);
});

describe("NodeDetail", () => {
  it.each(["UTC", "Asia/Tokyo"])("流量周期按 hub 时区 %s 显示", async (timezone) => {
    renderWithAdmin({ ...defaultImpl, listNodes, queryMetrics: async () => ({ level: "1m", stepS: 60, ts: [], series: [] }),
      getTraffic: async () => ({ ...await getTraffic(), timezone }) },
      [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
    const t = trafficOf(7n).traffic;
    const start = new Date(Number(t.periodStart) * 1000).toLocaleString(undefined, { timeZone: timezone });
    const next = new Date(Number(t.nextResetAt) * 1000).toLocaleString(undefined, { timeZone: timezone });
    expect(await screen.findByText(start)).toBeInTheDocument();
    expect(screen.getByText(`${next}（每月 1 日，${timezone}）`)).toBeInTheDocument();
  });

  it("流量卡显示周期与总量，并按 GiB 提交校正后刷新", async () => {
    const adjustTraffic = vi.fn(async () => ({ traffic: trafficOf(7n).traffic }));
    const traffic = vi.fn(getTraffic);
    renderWithAdmin({ ...defaultImpl, listNodes, queryMetrics: async () => ({ level: "1m", stepS: 60, ts: [], series: [] }), getTraffic: traffic, adjustTraffic },
      [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
    expect(await screen.findByText("↓ 1.0 GiB ↑ 512 MiB")).toBeInTheDocument();
    expect(screen.getByText("↓ 10 GiB ↑ 5.0 GiB")).toBeInTheDocument();
    expect(screen.getByText(/每月 1 日/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("本周期下行 (GiB)"), { target: { value: "2.5" } });
    fireEvent.change(screen.getByLabelText("本周期上行 (GiB)"), { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "校正本周期" }));
    await waitFor(() => expect(adjustTraffic).toHaveBeenCalledWith(expect.objectContaining({ nodeId: 7n, periodRx: 2_684_354_560n, periodTx: 0n }), expect.anything()));
    await waitFor(() => expect(traffic.mock.calls.length).toBeGreaterThanOrEqual(2));
  });

  it("校正输入不是非负数时按钮禁用", async () => {
    renderWithAdmin({ ...defaultImpl, listNodes, queryMetrics: async () => ({ level: "1m", stepS: 60, ts: [], series: [] }), getTraffic },
      [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
    await screen.findByText("↓ 1.0 GiB ↑ 512 MiB");
    fireEvent.change(screen.getByLabelText("本周期下行 (GiB)"), { target: { value: "-1" } });
    expect(screen.getByRole("button", { name: "校正本周期" })).toBeDisabled();
    fireEvent.change(screen.getByLabelText("本周期下行 (GiB)"), { target: { value: "abc" } });
    expect(screen.getByRole("button", { name: "校正本周期" })).toBeDisabled();
    fireEvent.change(screen.getByLabelText("本周期下行 (GiB)"), { target: { value: "0" } });
    expect(screen.getByRole("button", { name: "校正本周期" })).toBeEnabled();
  });

  it.each(["1e308", "17179869184"])("校正输入 %s 超出字节范围时按钮禁用", async (value) => {
    renderWithAdmin({ ...defaultImpl, listNodes, queryMetrics: async () => ({ level: "1m", stepS: 60, ts: [], series: [] }), getTraffic },
      [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
    await screen.findByText("↓ 1.0 GiB ↑ 512 MiB");
    fireEvent.change(screen.getByLabelText("本周期下行 (GiB)"), { target: { value } });
    expect(screen.getByRole("button", { name: "校正本周期" })).toBeDisabled();
  });

  it("流量请求失败只在卡内报错，不影响图表", async () => {
    renderWithAdmin({ ...defaultImpl, listNodes, queryMetrics: async () => ({ level: "1m", stepS: 60, ts: [], series: [] }),
      getTraffic: async () => { throw new ConnectError("traffic unavailable", Code.Unavailable); } },
      [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
    expect(await screen.findByRole("alert")).toHaveTextContent(/^traffic unavailable$/);
    expect(screen.getAllByTestId("chart")).toHaveLength(7);
  });

  it("切窗请求挂起期间保留七张图", async () => {
    const response = { level: "1m", stepS: 60, ts: [], series: [] };
    let release!: () => void;
    let started!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    const pending = new Promise<void>((resolve) => { started = resolve; });
    const queryMetrics = vi.fn(async () => {
      if (queryMetrics.mock.calls.length > 1) { started(); await gate; }
      return response;
    });
    renderWithAdmin({ ...defaultImpl, getTraffic, listNodes, queryMetrics }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
    await screen.findByText(/级别 1m/);
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "7d" })); await pending; });
    try { expect(screen.queryAllByTestId("chart").length).toBe(7); }
    finally { await act(async () => { release(); }); }
  });

  it("非数字节点路径不发查询并显示返回链接", async () => {
    const list = vi.fn(listNodes);
    const queryMetrics = vi.fn(async () => ({ level: "1m", stepS: 60, ts: [], series: [] }));
    await act(async () => { renderWithAdmin({ ...defaultImpl, getTraffic, listNodes: list, queryMetrics }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/abc"); });
    expect(screen.getByRole("alert")).toHaveTextContent("节点 abc 不存在");
    expect(screen.getByRole("link", { name: "返回总览" })).toHaveAttribute("href", "/");
    expect(list).not.toHaveBeenCalled();
    expect(queryMetrics).not.toHaveBeenCalled();
  });
  it("按面板画图，单位随数据，显示 hub 选定的级别", async () => {
    const queryMetrics = vi.fn<NonNullable<AdminImpl["queryMetrics"]>>(async () => ({
      level: "5m", stepS: 300, ts: [],
      series: [{ name: "cpu", unit: "percent", samples: [] }, { name: "mem_used", unit: "bytes", samples: [] }],
    }));
    renderWithAdmin({ ...defaultImpl, getTraffic, listNodes, queryMetrics }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
    expect(await screen.findByRole("heading", { level: 1, name: "db-01" })).toBeInTheDocument();
    expect(await screen.findByText(/级别 5m，每点 300s/)).toBeInTheDocument();
    const charts = screen.getAllByTestId("chart");
    expect(charts).toHaveLength(7);
    expect(charts[6]).toHaveAttribute("data-labels", "rx_bytes,tx_bytes");
    expect(charts[6]).toHaveAttribute("data-unit", "bytes/s");
    expect(charts[0]).toHaveAttribute("data-unit", "percent");
    expect(charts[1]).toHaveAttribute("data-labels", "mem_used,swap_used");
    expect(charts[1]).toHaveAttribute("data-unit", "bytes");
    expect(screen.getByText("db-01.internal")).toBeInTheDocument();
    const req = queryMetrics.mock.calls[0][0] as { nodeId: bigint; from: bigint; to: bigint; maxPoints: number };
    expect(req.nodeId).toBe(7n);
    expect(Number(req.to - req.from)).toBe(86400);
    expect(req.maxPoints).toBe(1000);
  });

  it.each([
    { label: "1h", seconds: 3600 },
    { label: "6h", seconds: 21600 },
    { label: "7d", seconds: 604800 },
    { label: "30d", seconds: 2592000 },
  ])("切换到 $label 重新查询", async ({ label, seconds }) => {
    const queryMetrics = vi.fn<NonNullable<AdminImpl["queryMetrics"]>>(async () => ({ level: "1m", stepS: 60, ts: [], series: [] }));
    renderWithAdmin({ ...defaultImpl, getTraffic, listNodes, queryMetrics }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
    await screen.findByRole("heading", { level: 1, name: "db-01" });
    await screen.findByText(/级别 1m，每点 60s/);
    fireEvent.click(screen.getByRole("button", { name: label }));
    await waitFor(() => expect(queryMetrics.mock.calls.length).toBeGreaterThanOrEqual(2));
    const last = queryMetrics.mock.calls.at(-1)![0] as { from: bigint; to: bigint };
    expect(Number(last.to - last.from)).toBe(seconds);
  });

  it("不存在的节点给出返回链接", async () => {
    renderWithAdmin({ ...defaultImpl, getTraffic, listNodes, queryMetrics: async () => ({ level: "1m", stepS: 60, ts: [], series: [] }) }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/99");
    expect(await screen.findByRole("alert")).toHaveTextContent("节点 99 不存在");
    expect(screen.getByRole("link", { name: "返回总览" })).toHaveAttribute("href", "/");
  });

  it.each(["nodes", "history"])("%s 请求失败时显示 hub 的错误正文", async (source) => {
    const fail = async () => { throw new ConnectError("data unavailable", Code.Unavailable); };
    renderWithAdmin({ ...defaultImpl,
      getTraffic,
      listNodes: source === "nodes" ? fail : listNodes,
      queryMetrics: source === "history" ? fail : async () => ({ level: "1m", stepS: 60, ts: [], series: [] }),
    }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
    expect(await screen.findByRole("alert")).toHaveTextContent(/^data unavailable$/);
  });
});

it("探测图每个任务一条线，已删除任务用编号，且与指标查询共用同一窗口", async () => {
  const windows: { name: string; from: bigint; to: bigint }[] = [];
  renderWithAdmin({ ...defaultImpl,
    listNodes,
    queryMetrics: async (req) => { windows.push({ name: "metrics", from: req.from, to: req.to }); return defaultImpl.queryMetrics(); },
    queryProbes: async (req) => {
      windows.push({ name: "probes", from: req.from, to: req.to });
      return create(QueryProbesResponseSchema, { level: "1m", stepS: 60, series: [
        { taskId: 3n, kind: ProbeKind.ICMP, target: "1.1.1.1", samples: [{ ts: req.from, sent: 10, lost: 1, errors: 0, rttMeanUs: 9000 }] },
        { taskId: 9n, samples: [{ ts: req.from, sent: 10, lost: 0, errors: 0, rttMeanUs: 1000 }] },
      ] });
    },
  }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
  expect(await screen.findByRole("heading", { name: "探测 · 丢包率" })).toBeInTheDocument();
  expect(screen.getByRole("heading", { name: "探测 · RTT 均值" })).toBeInTheDocument();
  expect(await screen.findAllByText("ICMP 1.1.1.1")).toHaveLength(2);
  expect(screen.getAllByText("任务 #9")).toHaveLength(2);
  await waitFor(() => expect(windows.filter((w) => w.name === "probes")).toHaveLength(1));
  const m = windows.find((w) => w.name === "metrics")!;
  const p = windows.find((w) => w.name === "probes")!;
  expect([p.from, p.to]).toEqual([m.from, m.to]);
  expect(screen.queryByText(/窗口内没有探测结果/)).toBeNull();
});

it("窗口内没有探测结果时给出去向", async () => {
  renderWithAdmin({ ...defaultImpl,
    listNodes,
    queryMetrics: defaultImpl.queryMetrics,
    queryProbes: async () => create(QueryProbesResponseSchema, { level: "1m", stepS: 60, series: [] }),
  }, [{ path: "/nodes/:id", Component: NodeDetail }, { path: "/probes", element: <p>任务页</p> }], "/nodes/7");
  expect(await screen.findByText(/窗口内没有探测结果/)).toBeInTheDocument();
  expect(screen.queryByRole("heading", { name: "探测 · 丢包率" })).toBeNull();
  fireEvent.click(screen.getByRole("link", { name: "管理探测任务" }));
  expect(await screen.findByText("任务页")).toBeInTheDocument();
});

it("主机信息显示 ICMP 是否可用", async () => {
  renderWithAdmin({ ...defaultImpl, listNodes, queryMetrics: defaultImpl.queryMetrics,
    queryProbes: async () => create(QueryProbesResponseSchema, { stepS: 60 }) },
    [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
  const dt = await screen.findByText("ICMP 探测");
  expect(dt.nextElementSibling).toHaveTextContent("不可用");
});

it("主机信息也显示 ICMP 可用", async () => {
  renderWithAdmin({ ...defaultImpl, listNodes: async () => {
    const response = await listNodes();
    response.nodes[0].facts.icmpAvailable = true;
    return response;
  } }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
  const dt = await screen.findByText("ICMP 探测");
  expect(dt.nextElementSibling).toHaveTextContent(/^可用$/);
});

it("探测查询失败显示错误，不吞掉失败", async () => {
  renderWithAdmin({ ...defaultImpl, queryProbes: async () => { throw new ConnectError("probes unavailable", Code.Unavailable); } },
    [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
  expect(await screen.findByRole("alert")).toHaveTextContent(/^probes unavailable$/);
});

it("切窗请求挂起时保留探测图与图例", async () => {
  let releaseProbes!: () => void;
  let started!: () => void;
  const probeGate = new Promise<void>((resolve) => { releaseProbes = resolve; });
  const pending = new Promise<void>((resolve) => { started = resolve; });
  const queryProbes = vi.fn<NonNullable<AdminImpl["queryProbes"]>>(async (req) => {
    if (queryProbes.mock.calls.length > 1) { started(); await probeGate; }
    return create(QueryProbesResponseSchema, { stepS: 60, series: [
      { taskId: 3n, kind: ProbeKind.ICMP, target: "1.1.1.1", samples: [{ ts: req.from, sent: 1, rttMeanUs: 1000 }] },
    ] });
  });
  renderWithAdmin({ ...defaultImpl, queryProbes }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
  try {
    expect(await screen.findAllByText("ICMP 1.1.1.1")).toHaveLength(2);
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "7d" })); await pending; });
    expect(screen.getAllByText("ICMP 1.1.1.1")).toHaveLength(2);
  } finally {
    await act(async () => { releaseProbes(); });
  }
});

it("主机名一格带上 hub 看到的来源地址；从未上报时不显示", async () => {
  renderWithAdmin({ ...defaultImpl, listNodes: async () => {
    const response = await listNodes();
    return { nodes: [{ ...response.nodes[0], lastSource: "2001:db8::7" }] };
  } }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
  const dt = await screen.findByText("主机名");
  expect(dt.nextElementSibling).toHaveTextContent(/^db-01\.internal（来源 2001:db8::7）$/);
});

it("来源地址为空时主机名一格只有主机名", async () => {
  renderWithAdmin({ ...defaultImpl, listNodes }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
  const dt = await screen.findByText("主机名");
  expect(dt.nextElementSibling).toHaveTextContent(/^db-01\.internal$/);
});
