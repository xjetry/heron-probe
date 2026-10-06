import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { act, cleanup, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { PublicService } from "../gen/heron/v1/public_pb";
import { QueryProbesResponseSchema } from "../gen/heron/v1/query_pb";
import { BillingCycle, ProbeKind } from "../gen/heron/v1/types_pb";
import { renderWithService } from "../test/harness";
import { NodePage } from "./NodePage";

vi.mock("../components/Chart", () => ({
  Chart: ({ labels }: { labels: string[] }) => <div data-testid="chart">{labels.map((l) => <span key={l}>{l}</span>)}</div>,
}));

afterEach(() => vi.useRealTimers());

const snapshot = async () => ({ now: 1_000n, nodes: [{ id: 7n, name: "edge-1", online: true, facts: { os: "Alpine 3.21", arch: "arm64", cpuModel: "Neoverse", cpuCores: 2 } }] });

it("公开节点的历史图表走 PublicService，与面板同一组时间范围与探测图例", async () => {
  const queryMetrics = vi.fn(async () => ({ level: "1m", stepS: 60, ts: [], series: [] }));
  const queryProbes = vi.fn(async () => create(QueryProbesResponseSchema, { level: "1m", stepS: 60, series: [{ taskId: 3n, kind: ProbeKind.TCP, target: "example.com:443" }] }));
  renderWithService(PublicService, { getSnapshot: snapshot, queryMetrics, queryProbes }, [{ path: "/nodes/:id", Component: NodePage }], "/nodes/7");
  expect(await screen.findByRole("heading", { level: 1, name: "edge-1" })).toBeInTheDocument();
  expect(await screen.findAllByText("TCP example.com:443")).toHaveLength(2);
  expect(screen.getAllByTestId("chart")).toHaveLength(12);
  for (const r of ["1h", "6h", "24h", "7d", "30d"]) expect(screen.getByRole("button", { name: r })).toBeInTheDocument();
  expect(screen.getByText("Alpine 3.21")).toBeInTheDocument();
  expect(screen.queryByLabelText("执行环境")).not.toBeInTheDocument();
  expect((queryMetrics.mock.calls[0] as unknown[])[0]).toMatchObject({ nodeId: 7n, maxPoints: 1000 });
});

it("快照里没有的节点说明不存在或未公开，也不去查历史", async () => {
  const queryMetrics = vi.fn(async () => ({ level: "1m", stepS: 60, ts: [], series: [] }));
  renderWithService(PublicService, { getSnapshot: snapshot, queryMetrics }, [{ path: "/nodes/:id", Component: NodePage }], "/nodes/9");
  expect(await screen.findByRole("alert")).toHaveTextContent("节点 9 不存在或未公开");
  expect(queryMetrics).not.toHaveBeenCalled();
});

it("静态信息卡带费用与到期两行；主机信息缺失时卡片照样显示这两行，都没有时不画卡片", async () => {
  // 时钟放在与夹具错开的日期：剩余天数只能来自 hub 下发的 daysLeft，页面按本地日期重算会得出另一个数。
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(new Date(2031, 0, 1));
  const nodes = [
    { id: 7n, name: "edge-1", online: true, publicRemark: "联通 4837", facts: { os: "Alpine 3.21", arch: "arm64" }, billing: { price: "5", currency: "EUR", billingCycle: BillingCycle.YEARLY, expiresOn: "2026-09-20", daysLeft: -7 } },
    { id: 8n, name: "fresh", online: false, billing: { price: "3", currency: "USD" } },
    { id: 9n, name: "bare", online: false },
    { id: 10n, name: "due", online: false, billing: { expiresOn: "2026-09-20", daysLeft: -7 } },
  ];
  const getSnapshot = async () => ({ now: 1_000n, nodes });
  const queryMetrics = async () => ({ level: "1m", stepS: 60, ts: [], series: [] });
  const queryProbes = async () => ({ level: "1m", stepS: 60, series: [] });
  const show = async (id: number) => {
    renderWithService(PublicService, { getSnapshot, queryMetrics, queryProbes }, [{ path: "/nodes/:id", Component: NodePage }], `/nodes/${id}`);
    await screen.findByRole("heading", { level: 1 });
  };
  await show(7);
  expect(screen.getByText("联通 4837")).toBeInTheDocument();
  expect(screen.getByText("Alpine 3.21")).toBeInTheDocument();
  expect(screen.getByText("费用").nextElementSibling).toHaveTextContent(/^€5 \/ 年$/);
  expect(screen.getByText("2026-09-20（已过期 7 天）")).toHaveClass("error");
  cleanup();
  await show(8);
  expect(screen.getByText("费用").nextElementSibling).toHaveTextContent(/^US\$3$/);
  expect(screen.queryByText("到期")).toBeNull();
  expect(screen.queryByText("系统")).toBeNull();
  cleanup();
  await show(9);
  expect(screen.queryByRole("definition")).toBeNull();
  cleanup();
  await show(10);
  expect(screen.getAllByRole("definition").map((d) => d.textContent)).toEqual(["2026-09-20（已过期 7 天）"]);
  expect(screen.getByText("2026-09-20（已过期 7 天）")).toHaveClass("error");
});

it("窗口每分钟前进后请求失败，图表与级别仍在并带横幅，不误报“非当前窗口”", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  let fail = false;
  const queryMetrics = vi.fn(async () => {
    if (fail) throw new ConnectError("history down", Code.Unavailable);
    return { level: "1m", stepS: 60, ts: [], series: [] };
  });
  const queryProbes = vi.fn(async () => ({ level: "1m", stepS: 60, series: [] }));
  renderWithService(PublicService, { getSnapshot: snapshot, queryMetrics, queryProbes }, [{ path: "/nodes/:id", Component: NodePage }], "/nodes/7");
  await screen.findByRole("heading", { level: 1, name: "edge-1" });
  expect(await screen.findAllByTestId("chart")).toHaveLength(10);
  await screen.findByText(/级别 1m，每点 60s/);
  fail = true;
  // 窗口右端每分钟前进一次（History.tsx 的 REFRESH_MS），换键后的这次请求失败。
  await act(async () => { await vi.advanceTimersByTimeAsync(60_000 + 100); });
  expect(await screen.findByRole("alert")).toHaveTextContent("history down");
  expect(screen.getAllByTestId("chart")).toHaveLength(10);
  expect(screen.getByText(/级别 1m，每点 60s/)).toBeInTheDocument();
  // 沿用的还是 24h 这个 range 自己的数据，只是这次刷新没成功；range 没变，不该报"看错窗口"。
  expect(screen.queryByText(/图表还不是/)).toBeNull();
});

it("切到另一个节点、新节点历史未返回时不显示上一个节点的图表与级别", async () => {
  let releaseNode8!: () => void;
  const node8Gate = new Promise<void>((resolve) => { releaseNode8 = resolve; });
  const twoNodes = async () => ({ now: 1_000n, nodes: [
    { id: 7n, name: "edge-1", online: true },
    { id: 8n, name: "edge-2", online: true },
  ] });
  const queryMetrics = vi.fn(async (req: { nodeId: bigint }) => {
    if (req.nodeId === 8n) await node8Gate;
    return req.nodeId === 8n ? { level: "5m", stepS: 300, ts: [], series: [] } : { level: "1m", stepS: 60, ts: [], series: [] };
  });
  const queryProbes = async () => ({ level: "1m", stepS: 60, series: [] });
  const { router } = renderWithService(PublicService, { getSnapshot: twoNodes, queryMetrics, queryProbes }, [{ path: "/nodes/:id", Component: NodePage }], "/nodes/7");
  await screen.findByRole("heading", { level: 1, name: "edge-1" });
  await screen.findByText(/级别 1m，每点 60s/);
  await act(async () => { router.navigate("/nodes/8"); });
  expect(await screen.findByRole("heading", { level: 1, name: "edge-2" })).toBeInTheDocument();
  // 8 的历史还没回来：不能把 7 的"级别 1m"标签或图表当成 8 的显示，这段时间没有图表比显示错的更安全。
  expect(screen.queryByText(/级别 1m/)).toBeNull();
  expect(screen.queryAllByTestId("chart")).toHaveLength(0);
  await act(async () => { releaseNode8(); });
  expect(await screen.findByText(/级别 5m，每点 300s/)).toBeInTheDocument();
  expect(screen.getAllByTestId("chart")).toHaveLength(10);
});

it("节点页标题带国家 / 地区徽章", async () => {
  const withCountry = async () => ({ now: 1_000n, nodes: [{ id: 7n, name: "edge-1", online: true, country: "DE" }] });
  renderWithService(PublicService, { getSnapshot: withCountry }, [{ path: "/nodes/:id", Component: NodePage }], "/nodes/7");
  const h1 = await screen.findByRole("heading", { level: 1 });
  expect(h1).toHaveTextContent("edge-1 \u{1F1E9}\u{1F1EA} DE");
  expect(screen.getByTitle("国家 / 地区 DE")).toHaveTextContent("\u{1F1E9}\u{1F1EA} DE");
});
