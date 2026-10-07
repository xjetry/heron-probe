import { Code, ConnectError } from "@connectrpc/connect";
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { renderWithAdmin } from "../test/harness";
import { POLL_MS } from "../lib/poll";
import { Overview } from "./Overview";

const snapshot = {
  now: 1_000_000n, reportIntervalMs: 10_000, hubVersion: "v0.9.0", boundAgentVersion: "v0.8.0",
  nodes: [
    { id: 1n, name: "web-01", online: true, lastSeenAt: 999_990n,
      traffic: { periodRx: 1024n ** 3n, periodTx: 512n * 1024n ** 2n },
      metrics: { cpuPct: 42, memUsed: 512n * 1024n ** 2n, memTotal: 1024n ** 3n, diskUsed: 1024n ** 3n, diskTotal: 10n * 1024n ** 3n, load1: 0.5, load5: 0.4, load15: 0.3, netRxBps: 1024n, netTxBps: 2048n } },
    { id: 2n, name: "never", online: false },
    { id: 3n, name: "gone", online: false, lastSeenAt: 900_000n },
  ],
};
const nodes = [
  { id: 1n, name: "web-01", note: "customer", maintenance: false, facts: { hostname: "hostname.internal", agentVersion: "v0.8.0" }, billing: { daysLeft: 20 } },
  { id: 2n, name: "never", maintenance: false },
  { id: 3n, name: "gone", maintenance: true, facts: { agentVersion: "v0.7.0" } },
  { id: 4n, name: "fresh", maintenance: false },
];
const rules = { rules: [], states: [{ ruleId: 1n, nodeId: 3n, state: "firing" }, { ruleId: 1n, nodeId: 1n, state: "pending" }] };
const impl = { getSnapshot: async () => snapshot, listNodes: async () => ({ nodes }), listAlertRules: async () => rules };
const render = (over: Partial<typeof impl> = {}) => renderWithAdmin({ ...impl, ...over }, [{ path: "/", Component: Overview }], "/");

afterEach(() => vi.useRealTimers());

it("需要处理四张卡按同一判定计数并链到对应筛选", async () => {
  render();
  const cards = within(await screen.findByRole("list", { name: "需要处理" })).getAllByRole("link");
  expect(cards.map((a) => [a.textContent, a.getAttribute("href")])).toEqual([
    ["0离线从未上报 1", "/nodes?status=offline"],      // gone 维护中不算离线；never 从未上报
    ["130 天内到期已过期 0", "/nodes?expiring=1"],
    ["1agent 版本落后低于 v0.8.0", "/nodes?lagging=1"],
    ["1触发中告警", "/alerts?state=firing"],
  ]);
});

it("实时表每行一个状态点与细条；维护中压过在线，不在快照里的节点是状态未知", async () => {
  render();
  const web = within(await screen.findByRole("row", { name: /web-01/ }));
  expect(web.getByRole("img", { name: "在线" })).toBeInTheDocument();
  expect(web.getByRole("meter", { name: "CPU 42%" })).toHaveAttribute("aria-valuenow", "42");
  expect(web.getByRole("meter", { name: "内存 50%" })).toBeInTheDocument();
  expect(web.getByRole("meter", { name: "磁盘 10%" })).toBeInTheDocument();
  expect(web.getByText("0.50 / 0.40 / 0.30")).toBeInTheDocument();
  expect(web.getByText("↓ 1.0 KiB/s ↑ 2.0 KiB/s")).toBeInTheDocument();
  expect(web.getByText("↓ 1.0 GiB ↑ 512 MiB")).toBeInTheDocument();
  expect(web.getByText("10 秒前")).toBeInTheDocument();
  expect(web.getByRole("link", { name: "web-01（#1）" })).toHaveAttribute("href", "/nodes/1");
  const never = within(screen.getByRole("row", { name: /never/ }));
  expect(never.getByRole("img", { name: "从未上报" })).toBeInTheDocument();
  expect(never.getAllByLabelText("无读数")).toHaveLength(6);
  expect(never.getByText("从未上报", { selector: "td" })).toBeInTheDocument();
  expect(within(screen.getByRole("row", { name: /gone/ })).getByRole("img", { name: "维护中" })).toBeInTheDocument();
  expect(within(screen.getByRole("row", { name: /fresh/ })).getByRole("img", { name: "状态未知" })).toBeInTheDocument();
});

it("告警状态未到或失败时触发卡显示「—」而不是 0，页面其余照常", async () => {
  render({ listAlertRules: async () => { throw new ConnectError("rules down", Code.Unavailable); } });
  const cards = within(await screen.findByRole("list", { name: "需要处理" })).getAllByRole("link");
  expect(cards[3]).toHaveTextContent("—触发中告警");
  expect(await screen.findByRole("alert")).toHaveTextContent("rules down");
  expect(screen.getByRole("table")).toBeInTheDocument();
});

it.each(["WEB", "CUSTOMER", "HOSTNAME"])("搜索 %s 过滤实时表，清空恢复", async (search) => {
  render();
  await screen.findByRole("row", { name: /never/ });
  const input = screen.getByRole("searchbox", { name: "搜索节点" });
  fireEvent.change(input, { target: { value: search } });
  expect(screen.getAllByRole("row").slice(1).map((row) => row.getAttribute("aria-label") ?? within(row).getByRole("link").textContent)).toEqual(["web-01"]);
  fireEvent.change(input, { target: { value: "absent" } });
  expect(screen.getByRole("status")).toHaveTextContent("没有匹配的节点。");
  fireEvent.change(input, { target: { value: "" } });
  expect(screen.getAllByRole("row")).toHaveLength(5);
});

it("已有快照时刷新失败仍保留表格并显示错误", async () => {
  const getSnapshot = vi.fn().mockResolvedValueOnce(snapshot).mockRejectedValue(new ConnectError("snapshot unavailable", Code.Unavailable));
  const { queryClient } = render({ getSnapshot });
  await screen.findByRole("row", { name: /web-01/ });
  await act(async () => { await queryClient.refetchQueries(); });
  expect(await screen.findByRole("alert")).toHaveTextContent(/^snapshot unavailable$/);
  expect(screen.getByRole("table")).toBeInTheDocument();
});

it("按 POLL_MS 轮询快照", async () => {
  expect(POLL_MS).toBe(2000);
  vi.useFakeTimers({ shouldAdvanceTime: true });
  const getSnapshot = vi.fn(async () => snapshot);
  render({ getSnapshot });
  await waitFor(() => expect(getSnapshot).toHaveBeenCalledTimes(1));
  await screen.findByRole("row", { name: /web-01/ });
  await act(async () => vi.advanceTimersByTimeAsync(POLL_MS + 100));
  await waitFor(() => expect(getSnapshot.mock.calls.length).toBeGreaterThanOrEqual(2));
});

it("没有节点时四张卡都是 0 且可点，表格位置给出去处", async () => {
  render({ getSnapshot: async () => ({ ...snapshot, nodes: [] }), listNodes: async () => ({ nodes: [] }), listAlertRules: async () => ({ rules: [], states: [] }) });
  const cards = within(await screen.findByRole("list", { name: "需要处理" })).getAllByRole("link");
  expect(cards.map((a) => a.querySelector("strong")?.textContent)).toEqual(["0", "0", "0", "0"]);
  expect(screen.getByText(/还没有节点/)).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "注册窗口" })).toHaveAttribute("href", "/register");
});

it("请求首次失败时显示 hub 的错误正文", async () => {
  render({ getSnapshot: async () => { throw new ConnectError("snapshot unavailable", Code.Unavailable); } });
  expect(await screen.findByRole("alert")).toHaveTextContent(/^snapshot unavailable$/);
});
