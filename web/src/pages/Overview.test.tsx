import { Code, ConnectError } from "@connectrpc/connect";
import { act, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithAdmin } from "../test/harness";
import { Overview, POLL_MS } from "./Overview";

const snapshot = {
  now: 1_000_000n,
  reportIntervalMs: 10_000,
  nodes: [
    {
      id: 1n, name: "web-01", online: true, lastSeenAt: 999_990n,
      metrics: { cpuPct: 42, memUsed: 512n * 1024n * 1024n, memTotal: 1024n * 1024n * 1024n, load1: 0.5, load5: 0.4, load15: 0.3, netRxBps: 1024n, netTxBps: 2048n },
    },
    { id: 2n, name: "never", online: false },
  ],
};

afterEach(() => vi.useRealTimers());

describe("Overview", () => {
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
    const never = within(rows[1]);
    expect(never.getByRole("img", { name: "离线" })).toBeInTheDocument();
    expect(never.getAllByLabelText("无读数")).toHaveLength(5);
    for (const missing of never.getAllByLabelText("无读数")) {
      expect(missing).toHaveTextContent(/^–$/);
    }
    expect(never.getByText("从未")).toBeInTheDocument();
    expect(web.getByRole("link", { name: "web-01" })).toHaveAttribute("href", "/nodes/1");
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
