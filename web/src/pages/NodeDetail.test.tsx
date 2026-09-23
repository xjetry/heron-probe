import { Code, ConnectError } from "@connectrpc/connect";
import { act, screen, fireEvent } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { renderWithAdmin, type AdminImpl } from "../test/harness";
import { NodeDetail } from "./NodeDetail";

vi.mock("../components/Chart", () => ({
  Chart: ({ labels, unit, data }: { labels: string[]; unit: string; data: unknown[] }) => (
    <div data-testid="chart" data-labels={labels.join(",")} data-unit={unit} data-points={String((data[0] as unknown[]).length)} />
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
const getTraffic = async () => ({ now: 1_757_000_000n, nodes: [trafficOf(7n)] });

describe("NodeDetail", () => {
  it("流量卡显示周期与总量，并按 GiB 提交校正后刷新", async () => {
    const adjustTraffic = vi.fn(async () => ({ traffic: trafficOf(7n).traffic }));
    const traffic = vi.fn(getTraffic);
    renderWithAdmin({ listNodes, queryMetrics: async () => ({ level: "1m", stepS: 60, ts: [], series: [] }), getTraffic: traffic, adjustTraffic },
      [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
    expect(await screen.findByText("↓ 1.0 GiB ↑ 512 MiB")).toBeInTheDocument();
    expect(screen.getByText("↓ 10 GiB ↑ 5.0 GiB")).toBeInTheDocument();
    expect(screen.getByText(/每月 1 日/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("本周期下行 (GiB)"), { target: { value: "2.5" } });
    fireEvent.change(screen.getByLabelText("本周期上行 (GiB)"), { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "校正本周期" }));
    await vi.waitFor(() => expect(adjustTraffic).toHaveBeenCalledWith(expect.objectContaining({ nodeId: 7n, periodRx: 2_684_354_560n, periodTx: 0n }), expect.anything()));
    await vi.waitFor(() => expect(traffic.mock.calls.length).toBeGreaterThanOrEqual(2));
  });

  it("校正输入不是非负数时按钮禁用", async () => {
    renderWithAdmin({ listNodes, queryMetrics: async () => ({ level: "1m", stepS: 60, ts: [], series: [] }), getTraffic },
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
    renderWithAdmin({ listNodes, queryMetrics: async () => ({ level: "1m", stepS: 60, ts: [], series: [] }), getTraffic },
      [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
    await screen.findByText("↓ 1.0 GiB ↑ 512 MiB");
    fireEvent.change(screen.getByLabelText("本周期下行 (GiB)"), { target: { value } });
    expect(screen.getByRole("button", { name: "校正本周期" })).toBeDisabled();
  });

  it("流量请求失败只在卡内报错，不影响图表", async () => {
    renderWithAdmin({ listNodes, queryMetrics: async () => ({ level: "1m", stepS: 60, ts: [], series: [] }),
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
    renderWithAdmin({ getTraffic, listNodes, queryMetrics }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
    await screen.findByText(/级别 1m/);
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "7d" })); await pending; });
    try { expect(screen.queryAllByTestId("chart").length).toBe(7); }
    finally { await act(async () => { release(); }); }
  });

  it("非数字节点路径不发查询并显示返回链接", async () => {
    const list = vi.fn(listNodes);
    const queryMetrics = vi.fn(async () => ({ level: "1m", stepS: 60, ts: [], series: [] }));
    await act(async () => { renderWithAdmin({ getTraffic, listNodes: list, queryMetrics }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/abc"); });
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
    renderWithAdmin({ getTraffic, listNodes, queryMetrics }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
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
    renderWithAdmin({ getTraffic, listNodes, queryMetrics }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
    await screen.findByRole("heading", { level: 1, name: "db-01" });
    await screen.findByText(/级别 1m，每点 60s/);
    fireEvent.click(screen.getByRole("button", { name: label }));
    await vi.waitFor(() => expect(queryMetrics.mock.calls.length).toBeGreaterThanOrEqual(2));
    const last = queryMetrics.mock.calls.at(-1)![0] as { from: bigint; to: bigint };
    expect(Number(last.to - last.from)).toBe(seconds);
  });

  it("不存在的节点给出返回链接", async () => {
    renderWithAdmin({ getTraffic, listNodes, queryMetrics: async () => ({ level: "1m", stepS: 60, ts: [], series: [] }) }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/99");
    expect(await screen.findByRole("alert")).toHaveTextContent("节点 99 不存在");
    expect(screen.getByRole("link", { name: "返回总览" })).toHaveAttribute("href", "/");
  });

  it.each(["nodes", "history"])("%s 请求失败时显示 hub 的错误正文", async (source) => {
    const fail = async () => { throw new ConnectError("data unavailable", Code.Unavailable); };
    renderWithAdmin({
      getTraffic,
      listNodes: source === "nodes" ? fail : listNodes,
      queryMetrics: source === "history" ? fail : async () => ({ level: "1m", stepS: 60, ts: [], series: [] }),
    }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
    expect(await screen.findByRole("alert")).toHaveTextContent(/^data unavailable$/);
  });
});
