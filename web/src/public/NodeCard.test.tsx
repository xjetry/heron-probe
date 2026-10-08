import { act, fireEvent, screen, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { PublicService } from "../gen/heron/v1/public_pb";
import { BillingCycle } from "../gen/heron/v1/types_pb";
import { TrafficQuotaMode } from "../gen/heron/v1/types_pb";
import { renderWithService } from "../test/harness";
import { PublicOverview } from "./Overview";

afterEach(() => { vi.useRealTimers(); localStorage.clear(); });

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
    traffic: { periodRx: 1024n ** 3n, periodTx: 2n * 1024n ** 3n, quotaUsedBytes: 2n * 1024n ** 3n, quotaBytes: 4n * 1024n ** 3n, quotaUsedPct: 50 },
    billing: { price: "12.50", currency: "USD", billingCycle: BillingCycle.MONTHLY, expiresOn: "2026-10-01", daysLeft: 4 },
  }]);
  const card = within(screen.getByRole("article", { name: "香港家宽" }));
  expect.soft(card.getByRole("link", { name: "香港家宽" })).toHaveAttribute("href", "/nodes/1");
  expect.soft(card.getByTitle("国家 / 地区 HK")).toBeInTheDocument();
  expect.soft(card.getByText("在线")).toHaveAttribute("data-status", "online");
  expect.soft(card.getByText("联通 4837")).toBeInTheDocument();
  expect.soft(card.getByText("Debian 13 · kvm · amd64 · 运行 1d 1h")).toBeInTheDocument();
  expect.soft(card.getByRole("meter", { name: "CPU 0.0%" })).toHaveAttribute("aria-valuenow", "0");
  expect.soft(card.getByRole("meter", { name: "内存 0 B / 1.0 GiB" })).toHaveAttribute("aria-valuenow", "0");
  expect.soft(card.getByRole("meter", { name: "磁盘 9.0 GiB / 10 GiB" })).toHaveAttribute("data-level", "attention");
  expect.soft(card.getByRole("group", { name: "网络速率" })).toHaveTextContent("↓ 0 B/s ↑ 1.0 KiB/s");
  expect.soft(card.getByText("本周期").nextElementSibling).toHaveTextContent("2.0 GiB / 4.0 GiB (50.0%)下载 + 上传");
  expect.soft(card.getByText("费用").nextElementSibling).toHaveTextContent("US$12.50 / 月");
  expect.soft(card.getByText("到期").nextElementSibling).toHaveTextContent("2026-10-01");
  expect.soft(card.getByText("剩 4 天").closest(".expiry")).toHaveAttribute("data-level", "attention");
});

it("没填费用与到期时底行写破折号；已过期写已过期天数并标玫红；无读数不画进度条", async () => {
  await renderCards([
    { id: 1n, name: "plain", online: true, lastSeenAt: 998n },
    { id: 2n, name: "lapsed", online: true, lastSeenAt: 998n, billing: { expiresOn: "2026-09-24", daysLeft: -3 } },
  ]);
  const plain = within(screen.getByRole("article", { name: "plain" }));
  expect.soft(plain.getByText("费用").nextElementSibling).toHaveTextContent("–");
  expect.soft(plain.getByText("到期").nextElementSibling).toHaveTextContent("–");
  expect.soft(plain.queryAllByRole("meter")).toHaveLength(0);
  expect.soft(plain.getByText("系统未知")).toBeInTheDocument();
  const lapsed = within(screen.getByRole("article", { name: "lapsed" }));
  expect.soft(lapsed.getByText("已过期 3 天").closest(".expiry")).toHaveAttribute("data-level", "critical");
});

it("只有在线与维护中出卡片；离线与从未上报列在网格下方默认展开的表格里（名称、地区、状态、最后上报）", async () => {
  await renderCards([
    { id: 1n, name: "on", online: true, lastSeenAt: 998n, country: "JP" },
    { id: 2n, name: "maint", online: false, lastSeenAt: 900n, maintenance: true },
    { id: 3n, name: "off", online: false, lastSeenAt: 400n, country: "HK" },
    { id: 4n, name: "fresh", online: false },
  ]);
  expect.soft(screen.getAllByRole("article").map((a) => a.getAttribute("aria-label"))).toEqual(["on", "maint"]);
  expect.soft(within(screen.getByRole("article", { name: "maint" })).getByText("维护中")).toHaveAttribute("data-status", "maintenance");
  const folded = screen.getByText("离线与从未上报 · 2").closest("details")!;
  expect.soft(folded).toHaveAttribute("open");
  const rows = within(folded).getAllByRole("row").slice(1).map((row) => within(row).getAllByRole("cell").map((cell) => cell.textContent));
  expect.soft(rows).toEqual([["off", "HK", "离线", "10 分钟前"], ["fresh", "", "从未上报", "–"]]);
  expect.soft(within(folded).getByRole("link", { name: "off" })).toHaveAttribute("href", "/nodes/3");
});

it("访客收起离线分组后，轮询带来新数据也不会把它再展开", async () => {
  let now = 1000n;
  const { queryClient } = renderWithService(PublicService, { getSnapshot: async () => ({ now, nodes: [
    { id: 1n, name: "on", online: true, lastSeenAt: now - 2n },
    { id: 3n, name: "off", online: false, lastSeenAt: 400n },
  ], tags: [] }) }, [{ path: "/", Component: PublicOverview }], "/");
  const folded = () => screen.getByText("离线与从未上报 · 1").closest("details")!;
  expect(await screen.findByText("10 分钟前")).toBeInTheDocument();
  expect(folded()).toHaveAttribute("open");
  // 访客点 summary 收起时，浏览器只改 DOM 上的 open。
  folded().open = false;
  now = 1060n;
  await act(async () => { await queryClient.refetchQueries(); });
  expect(await screen.findByText("11 分钟前")).toBeInTheDocument();
  expect(folded()).not.toHaveAttribute("open");
});

it("卡片排序：默认顺序与到期 / CPU / 流量排序", async () => {
  await renderCards([
    { id: 1n, name: "a", online: true, lastSeenAt: 1n, traffic: { periodRx: 1n, periodTx: 2n, quotaUsedBytes: 3n }, metrics: { cpuPct: 10 }, billing: { expiresOn: "2030-01-01", daysLeft: 30 } },
    { id: 2n, name: "b", online: true, lastSeenAt: 1n, traffic: { periodRx: 20n, periodTx: 10n, quotaUsedBytes: 30n }, metrics: { cpuPct: 90 }, billing: { expiresOn: "2030-01-01", daysLeft: 5 } },
  ]);
  const order = () => screen.getAllByRole("article").map((a) => a.getAttribute("aria-label"));
  expect.soft(order()).toEqual(["a", "b"]);
  fireEvent.change(screen.getByRole("combobox", { name: "排序" }), { target: { value: "cpu" } });
  expect.soft(order()).toEqual(["b", "a"]);
  fireEvent.change(screen.getByRole("combobox", { name: "排序" }), { target: { value: "expiry" } });
  expect.soft(order()).toEqual(["b", "a"]);
  fireEvent.change(screen.getByRole("combobox", { name: "排序" }), { target: { value: "traffic" } });
  expect.soft(order()).toEqual(["b", "a"]);
  fireEvent.change(screen.getByRole("combobox", { name: "排序" }), { target: { value: "default" } });
  expect.soft(order()).toEqual(["a", "b"]);
});

it("流量副标题跟随节点的配额口径，未设配额时沿用下载 + 上传", async () => {
  await renderCards([
    { id: 1n, name: "只收", online: true, lastSeenAt: 998n, traffic: { periodRx: 300n, periodTx: 700n, quotaUsedBytes: 300n, quotaBytes: 1000n, quotaMode: TrafficQuotaMode.RX, quotaUsedPct: 30 } },
    { id: 2n, name: "无配额", online: true, lastSeenAt: 998n, traffic: { periodRx: 300n, periodTx: 700n, quotaUsedBytes: 1000n, quotaMode: TrafficQuotaMode.TX } },
  ]);
  const rx = within(await screen.findByRole("article", { name: "只收" }));
  expect(rx.getByText("300 B / 1000 B (30.0%)")).toHaveClass("num");
  expect(rx.getByText("仅下载")).not.toHaveClass("num");
  const none = within(screen.getByRole("article", { name: "无配额" }));
  expect(none.getByText("1000 B")).toHaveClass("num");
  expect(none.getByText("下载 + 上传 · 未设配额")).not.toHaveClass("num");
});
