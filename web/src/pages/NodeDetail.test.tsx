import { Code, ConnectError } from "@connectrpc/connect";
import { screen, fireEvent } from "@testing-library/react";
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

describe("NodeDetail", () => {
  it("按面板画图，单位随数据，显示 hub 选定的级别", async () => {
    const queryMetrics = vi.fn<NonNullable<AdminImpl["queryMetrics"]>>(async () => ({
      level: "5m", stepS: 300, ts: [],
      series: [{ name: "cpu", unit: "percent", samples: [] }, { name: "mem_used", unit: "bytes", samples: [] }],
    }));
    renderWithAdmin({ listNodes, queryMetrics }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
    expect(await screen.findByRole("heading", { level: 1, name: "db-01" })).toBeInTheDocument();
    expect(await screen.findByText(/级别 5m，每点 300s/)).toBeInTheDocument();
    const charts = screen.getAllByTestId("chart");
    expect(charts).toHaveLength(6);
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
    renderWithAdmin({ listNodes, queryMetrics }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
    await screen.findByRole("heading", { level: 1, name: "db-01" });
    await screen.findByText(/级别 1m，每点 60s/);
    fireEvent.click(screen.getByRole("button", { name: label }));
    await vi.waitFor(() => expect(queryMetrics.mock.calls.length).toBeGreaterThanOrEqual(2));
    const last = queryMetrics.mock.calls.at(-1)![0] as { from: bigint; to: bigint };
    expect(Number(last.to - last.from)).toBe(seconds);
  });

  it("不存在的节点给出返回链接", async () => {
    renderWithAdmin({ listNodes, queryMetrics: async () => ({ level: "1m", stepS: 60, ts: [], series: [] }) }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/99");
    expect(await screen.findByRole("alert")).toHaveTextContent("节点 99 不存在");
    expect(screen.getByRole("link", { name: "返回总览" })).toHaveAttribute("href", "/");
  });

  it.each(["nodes", "history"])("%s 请求失败时显示 hub 的错误正文", async (source) => {
    const fail = async () => { throw new ConnectError("data unavailable", Code.Unavailable); };
    renderWithAdmin({
      listNodes: source === "nodes" ? fail : listNodes,
      queryMetrics: source === "history" ? fail : async () => ({ level: "1m", stepS: 60, ts: [], series: [] }),
    }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
    expect(await screen.findByRole("alert")).toHaveTextContent(/^data unavailable$/);
  });
});
