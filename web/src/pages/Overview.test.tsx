import { Code, ConnectError } from "@connectrpc/connect";
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithAdmin } from "../test/harness";
import { POLL_MS } from "../lib/poll";
import { Overview } from "./Overview";

const snapshot = {
  now: 1_000_000n,
  reportIntervalMs: 10_000,
  nodes: [
    {
      id: 1n, name: "web-01", online: true, lastSeenAt: 999_990n,
      traffic: { totalRx: 10n * 1024n ** 3n, totalTx: 5n * 1024n ** 3n, periodRx: 1024n ** 3n, periodTx: 512n * 1024n ** 2n, periodStart: 1_756_684_800n, nextResetAt: 1_759_276_800n, resetDay: 1 },
      metrics: { cpuPct: 42, memUsed: 512n * 1024n * 1024n, memTotal: 1024n * 1024n * 1024n, load1: 0.5, load5: 0.4, load15: 0.3, netRxBps: 1024n, netTxBps: 2048n },
    },
    { id: 2n, name: "never", online: false },
  ],
};

afterEach(() => vi.useRealTimers());

describe("Overview", () => {
  it("摘要只统计在线有读数节点，真实零值不当作缺失", async () => {
    renderWithAdmin({ getSnapshot: async () => ({ ...snapshot, nodes: [
      { id: 1n, name: "zero", online: true, metrics: { cpuPct: 0, netRxBps: 0n, netTxBps: 0n } },
      { id: 2n, name: "active", online: true, metrics: { cpuPct: 60, netRxBps: 1024n, netTxBps: 2048n } },
      { id: 3n, name: "missing", online: true },
      { id: 4n, name: "offline", online: false, metrics: { cpuPct: 100, netRxBps: 900000n, netTxBps: 900000n } },
    ] }) }, [{ path: "/", Component: Overview }], "/");
    await screen.findByText("3 / 4 在线");
    const cards = screen.getAllByRole("definition");
    expect(cards.map((card) => card.textContent)).toEqual(["4", "30%", "1.0 KiB/s", "2.0 KiB/s"]);
    expect(screen.getByText("2 个在线节点有读数")).toBeInTheDocument();
    expect(screen.getAllByText("2 个在线节点合计")).toHaveLength(2);
  });

  it("无在线读数时不把缺失统计显示成零", async () => {
    renderWithAdmin({ getSnapshot: async () => ({ ...snapshot, nodes: [{ id: 1n, name: "missing", online: true }] }) }, [{ path: "/", Component: Overview }], "/");
    await screen.findByText("1 / 1 在线");
    expect(screen.getAllByRole("definition").map((card) => card.textContent)).toEqual(["1", "暂无读数", "暂无读数", "暂无读数"]);
  });

  it.each(["WEB", "CUSTOMER", "HOSTNAME"])("搜索 %s 关联节点资料并清空恢复实时列表", async (search) => {
    renderWithAdmin({
      getSnapshot: async () => snapshot,
      listNodes: async () => ({ nodes: [
        { id: 2n, name: "never", note: "unrelated", facts: { hostname: "other.internal" } },
        { id: 1n, name: "web-01", note: "customer", facts: { hostname: "hostname.internal" } },
      ] }),
    }, [{ path: "/", Component: Overview }], "/");
    await screen.findByRole("link", { name: "never（#2）" });
    const input = screen.getByRole("searchbox", { name: "搜索节点" });
    fireEvent.change(input, { target: { value: search } });
    await screen.findByRole("link", { name: "web-01（#1）" });
    expect(screen.getAllByRole("link").map((link) => link.textContent)).toEqual(["web-01"]);
    expect(screen.getByRole("meter", { name: "42%" })).toHaveAttribute("aria-valuenow", "42");
    fireEvent.change(input, { target: { value: "absent" } });
    expect(screen.queryAllByRole("link")).toEqual([]);
    expect(screen.getByRole("status")).toHaveTextContent("没有匹配的节点。");
    fireEvent.change(input, { target: { value: "" } });
    expect(screen.getAllByRole("link").map((link) => link.textContent)).toEqual(["web-01", "never"]);
  });

  it("搜索资料加载与失败不伪装成无匹配，清空无需等待资料", async () => {
    let reject!: (error: unknown) => void;
    const pending = new Promise<never>((_resolve, fail) => { reject = fail; });
    renderWithAdmin({ getSnapshot: async () => snapshot, listNodes: () => pending }, [{ path: "/", Component: Overview }], "/");
    await screen.findByRole("link", { name: "web-01（#1）" });
    const input = screen.getByRole("searchbox", { name: "搜索节点" });
    fireEvent.change(input, { target: { value: "hostname" } });
    expect(screen.getByText("加载中…")).toBeInTheDocument();
    expect(screen.queryByRole("table")).toBeNull();
    await act(async () => { reject(new ConnectError("node metadata unavailable", Code.Unavailable)); });
    expect(await screen.findByRole("alert")).toHaveTextContent("node metadata unavailable");
    fireEvent.change(input, { target: { value: "" } });
    expect(screen.getAllByRole("link").map((link) => link.textContent)).toEqual(["web-01", "never"]);
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("搜索资料刷新失败保留结果与输入并显示错误", async () => {
    const listNodes = vi.fn().mockResolvedValueOnce({ nodes: [{ id: 1n, name: "web-01", note: "customer" }] })
      .mockRejectedValue(new ConnectError("metadata refresh failed", Code.Unavailable));
    const { queryClient } = renderWithAdmin({ getSnapshot: async () => snapshot, listNodes }, [{ path: "/", Component: Overview }], "/");
    await screen.findByRole("link", { name: "web-01（#1）" });
    const input = screen.getByRole("searchbox", { name: "搜索节点" });
    fireEvent.change(input, { target: { value: "customer" } });
    await waitFor(() => expect(screen.getAllByRole("link")).toHaveLength(1));
    await act(async () => { await queryClient.refetchQueries(); });
    expect(await screen.findByRole("alert")).toHaveTextContent("metadata refresh failed");
    expect(input).toHaveValue("customer");
    expect(screen.getAllByRole("link").map((link) => link.textContent)).toEqual(["web-01"]);
  });
  it("已有快照时请求失败仍保留表格并显示错误", async () => {
    const getSnapshot = vi.fn().mockResolvedValueOnce(snapshot).mockRejectedValue(new ConnectError("snapshot unavailable", Code.Unavailable));
    const { queryClient } = renderWithAdmin({ getSnapshot }, [{ path: "/", Component: Overview }], "/");
    await screen.findByText("1 / 2 在线");
    await act(async () => { await queryClient.refetchQueries(); });
    expect(await screen.findByRole("alert")).toHaveTextContent(/^snapshot unavailable$/);
    expect(screen.getByRole("table")).toBeInTheDocument();
  });
  it("在线计数、读数条与无读数的破折号", async () => {
    renderWithAdmin({ getSnapshot: async () => snapshot }, [{ path: "/", Component: Overview }], "/");
    expect(await screen.findByText("1 / 2 在线")).toBeInTheDocument();
    const rows = screen.getAllByRole("row").slice(1);
    const web = within(rows[0]);
    expect(web.getByRole("img", { name: "在线" })).toBeInTheDocument();
    expect(web.getByRole("meter", { name: "42%" })).toHaveAttribute("aria-valuenow", "42");
    expect(web.getByRole("meter", { name: "512 MiB / 1.0 GiB" })).toHaveAttribute("aria-valuenow", "50");
    expect(web.getByText("10 秒前")).toBeInTheDocument();
    expect(web.getByText("↓ 1.0 GiB ↑ 512 MiB")).toBeInTheDocument();
    const never = within(rows[1]);
    expect(never.getByRole("img", { name: "离线" })).toBeInTheDocument();
    expect(never.getAllByLabelText("无读数")).toHaveLength(6);
    for (const missing of never.getAllByLabelText("无读数")) {
      expect(missing).toHaveTextContent(/^–$/);
    }
    expect(never.getByText("从未")).toBeInTheDocument();
    expect(web.getByRole("link", { name: "web-01（#1）" })).toHaveAttribute("href", "/nodes/1");
  });

  it("按 POLL_MS 轮询", async () => {
    expect(POLL_MS).toBe(2000);
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const getSnapshot = vi.fn(async () => snapshot);
    renderWithAdmin({ getSnapshot }, [{ path: "/", Component: Overview }], "/");
    await waitFor(() => expect(getSnapshot).toHaveBeenCalledTimes(1));
    await screen.findByText("1 / 2 在线");
    await act(async () => vi.advanceTimersByTimeAsync(POLL_MS + 100));
    await waitFor(() => expect(getSnapshot.mock.calls.length).toBeGreaterThanOrEqual(2));
  });

  it("没有节点时给出去处", async () => {
    renderWithAdmin({ getSnapshot: async () => ({ now: 1n, reportIntervalMs: 10_000, nodes: [] }) }, [{ path: "/", Component: Overview }], "/");
    expect(await screen.findByText(/还没有节点/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "注册窗口" })).toHaveAttribute("href", "/register");
  });

  it("在线状态只取 hub 裁决，离线节点仍显示真实零读数", async () => {
    renderWithAdmin({ getSnapshot: async () => ({
      ...snapshot,
      nodes: [
        { id: 1n, name: "old-online", online: true, lastSeenAt: 1n },
        { id: 2n, name: "recent-offline", online: false, lastSeenAt: snapshot.now, metrics: { cpuPct: 0 } },
      ],
    }) }, [{ path: "/", Component: Overview }], "/");
    await screen.findByText("1 / 2 在线");
    const old = within(screen.getByRole("row", { name: /old-online/ }));
    const recent = within(screen.getByRole("row", { name: /recent-offline/ }));
    expect(old.getByRole("img", { name: "在线" })).toBeInTheDocument();
    expect(recent.getByRole("img", { name: "离线" })).toBeInTheDocument();
    expect(recent.getByRole("meter", { name: "0.0%" })).toHaveAttribute("aria-valuenow", "0");
  });

  it("请求失败时显示 hub 的错误正文", async () => {
    renderWithAdmin({ getSnapshot: async () => {
      throw new ConnectError("snapshot unavailable", Code.Unavailable);
    } }, [{ path: "/", Component: Overview }], "/");
    expect(await screen.findByRole("alert")).toHaveTextContent(/^snapshot unavailable$/);
  });
});
