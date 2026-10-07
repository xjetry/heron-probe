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
  await screen.findByText("1 / 4 在线");
  const groups = screen.getAllByRole("group").filter((g) => g.classList.contains("wall-group"));
  expect(groups.map((g) => g.querySelector("summary")?.textContent)).toEqual(["🇯🇵 日本 · 1 / 2 在线", "🇭🇰 香港 · 0 / 1 在线", "未知 · 0 / 1 在线"]);
  expect(groups.map((g) => g.getAttribute("aria-label"))).toEqual(["日本 1 / 2 在线", "香港 0 / 1 在线", "未知 0 / 1 在线"]);
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
  await screen.findByText("1 / 4 在线");
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
  await screen.findByText("1 / 4 在线");
  fireEvent.click(tile("fresh"));
  const panel = within(screen.getByRole("complementary", { name: "节点详情" }));
  expect(panel.getByText("从未上报")).toBeInTheDocument();
  expect(panel.queryAllByRole("meter")).toHaveLength(0);
  expect(panel.getAllByLabelText("无读数")).toHaveLength(10);
  expect(panel.queryByText(/×/)).toBeNull();
});

it("选中的节点从快照消失后退回墙上第一个节点", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  let current = snapshot;
  render(async () => current);
  await screen.findByText("1 / 4 在线");
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
    await screen.findByText("1 / 4 在线");
    expect(screen.queryByRole("complementary", { name: "节点详情" })).toBeNull();
    const mobileTile = within(tile("tokyo-1").closest("li")!);
    expect(mobileTile.getByText("需关注")).toBeInTheDocument();
    expect(mobileTile.getByText("CPU 95%", { selector: ".num" })).toBeInTheDocument();
    expect(mobileTile.getByText("内存 80%", { selector: ".num" })).toBeInTheDocument();
    expect(mobileTile.getByText("↓ 2.0 KiB/s")).toBeInTheDocument();
    expect(within(tile("hk-1").querySelector(".tile-name")!).getByText("维护中")).toBeInTheDocument();
    expect(queryMetrics).not.toHaveBeenCalled();
    fireEvent.click(tile("tokyo-2"));
    await waitFor(() => expect(router.state.location.pathname).toBe("/nodes/2"));
  } finally {
    vi.unstubAllGlobals();
  }
});

it("历史查询在同一分钟内复用，满一分钟后重新选中会更新", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  render();
  await screen.findByText("1 / 4 在线");
  await waitFor(() => expect(queryMetrics).toHaveBeenCalledTimes(1));
  fireEvent.click(tile("hk-1"));
  await waitFor(() => expect(queryMetrics).toHaveBeenCalledTimes(2));
  fireEvent.click(tile("tokyo-1"));
  await act(async () => vi.advanceTimersByTimeAsync(100));
  expect(queryMetrics).toHaveBeenCalledTimes(2);
  await act(async () => vi.advanceTimersByTimeAsync(60_000));
  fireEvent.click(tile("hk-1"));
  await waitFor(() => expect(queryMetrics).toHaveBeenCalledTimes(3));
});

it("宽屏按住修饰键点方块仍是普通链接（新标签页打开）", async () => {
  const { router } = render();
  await screen.findByText("1 / 4 在线");
  fireEvent.click(tile("tokyo-2"), { metaKey: true });
  // react-router 对带修饰键的点击不接管，jsdom 不会真的打开新标签，路径不变即可。
  expect(router.state.location.pathname).toBe("/");
  expect(within(screen.getByRole("complementary", { name: "节点详情" })).getByRole("heading", { name: "tokyo-1" })).toBeInTheDocument();
});

it("着色依据切到 CPU 时方块带档位，状态时不带", async () => {
  render();
  await screen.findByText("1 / 4 在线");
  expect(tile("tokyo-1").closest("li")).not.toHaveAttribute("data-level");
  fireEvent.change(screen.getByRole("combobox", { name: "着色依据" }), { target: { value: "cpu" } });
  expect(tile("tokyo-1").closest("li")).toHaveAttribute("data-level", "critical");
  expect(tile("fresh").closest("li")).not.toHaveAttribute("data-level");
});
