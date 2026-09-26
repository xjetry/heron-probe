import { act, screen, waitFor, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { PublicService } from "../gen/probe/v1/public_pb";
import { POLL_MS } from "../lib/poll";
import { renderWithService } from "../test/harness";
import { PublicOverview } from "./Overview";

const snapshot = {
  now: 1_000n,
  reportIntervalMs: 4000,
  nodes: [
    {
      id: 3n, name: "web-1", online: true, lastSeenAt: 998n, sortOrder: 0,
      facts: { os: "Debian 12", arch: "amd64" },
      metrics: { cpuPct: 42, memUsed: 512n * 1024n ** 2n, memTotal: 1024n ** 3n, diskUsed: 0n, diskTotal: 10n * 1024n ** 3n,
        netRxBps: 2048n, netTxBps: 1024n, uptimeS: 90_000n },
      traffic: { periodRx: 1024n ** 3n, periodTx: 0n },
    },
    { id: 4n, name: "db-1", online: false, sortOrder: 1 },
  ],
};

afterEach(() => vi.useRealTimers());

it("每个公开节点一张卡片：名称、在线、系统与架构、读数、运行时长与本周期流量", async () => {
  renderWithService(PublicService, { getSnapshot: async () => snapshot }, [{ path: "/", Component: PublicOverview }], "/");
  expect(await screen.findByText("1 / 2 在线")).toBeInTheDocument();
  const web = within(screen.getByRole("article", { name: "web-1" }));
  expect(web.getByRole("img", { name: "在线" })).toBeInTheDocument();
  expect(web.getByRole("link", { name: "web-1" })).toHaveAttribute("href", "/nodes/3");
  expect(web.getByText("Debian 12 · amd64")).toBeInTheDocument();
  expect(web.getByRole("meter", { name: "42%" })).toHaveAttribute("aria-valuenow", "42");
  expect(web.getByRole("meter", { name: "512 MiB / 1.0 GiB" })).toHaveAttribute("aria-valuenow", "50");
  // 读数为 0 与无读数是两个事实：0 画成空条，不是破折号。
  expect(web.getByRole("meter", { name: "0 B / 10 GiB" })).toHaveAttribute("aria-valuenow", "0");
  expect(web.getByText("↓ 2.0 KiB/s ↑ 1.0 KiB/s")).toBeInTheDocument();
  expect(web.getByText("1d 1h")).toBeInTheDocument();
  expect(web.getByText("↓ 1.0 GiB ↑ 0 B")).toBeInTheDocument();
  expect(web.getByText("最近上报 刚刚")).toBeInTheDocument();
  const db = within(screen.getByRole("article", { name: "db-1" }));
  expect(db.getByRole("img", { name: "离线" })).toBeInTheDocument();
  expect(db.getByText("系统未知")).toBeInTheDocument();
  expect(db.getAllByLabelText("无读数")).toHaveLength(6);
  expect(db.getByText("从未上报")).toBeInTheDocument();
});

it("没有公开节点时说明", async () => {
  renderWithService(PublicService, { getSnapshot: async () => ({ now: 1n, nodes: [] }) }, [{ path: "/", Component: PublicOverview }], "/");
  expect(await screen.findByText("没有公开的节点。")).toBeInTheDocument();
});

it("按 POLL_MS 轮询快照", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  const getSnapshot = vi.fn(async () => snapshot);
  renderWithService(PublicService, { getSnapshot }, [{ path: "/", Component: PublicOverview }], "/");
  await screen.findByText("1 / 2 在线");
  await act(async () => vi.advanceTimersByTimeAsync(POLL_MS + 100));
  await waitFor(() => expect(getSnapshot.mock.calls.length).toBeGreaterThanOrEqual(2));
});
