import { screen, within } from "@testing-library/react";
import { expect, it } from "vitest";
import { PublicService } from "../gen/heron/v1/public_pb";
import { renderWithService } from "../test/harness";
import { PublicOverview } from "./Overview";

it("卡片区分运行时长与在线状态，展示核数、负载、虚拟化和双向流量", async () => {
  renderWithService(PublicService, { getSnapshot: async () => ({ now: 1000n, nodes: [{
    id: 1n, name: "香港家宽", online: true, lastSeenAt: 998n, country: "HK",
    facts: { os: "Debian 13", arch: "amd64", virtualization: "kvm", cpuCores: 2 },
    metrics: { cpuPct: 0, load1: 0, load5: 0.12, load15: 1.5, memUsed: 0n, memTotal: 1024n ** 3n, diskUsed: 1024n ** 3n, diskTotal: 10n * 1024n ** 3n, uptimeS: 90000n, netRxBps: 0n, netTxBps: 1024n },
    traffic: { periodRx: 1024n ** 3n, periodTx: 2n * 1024n ** 3n },
  }] }) }, [{ path: "/", Component: PublicOverview }], "/");
  const card = within(await screen.findByRole("article", { name: "香港家宽" }));
  expect(card.getByText("CPU 2 核")).toBeInTheDocument();
  expect(card.getByText("Debian 13 · kvm · amd64")).toBeInTheDocument();
  expect(card.getByText("运行 1d 1h")).toBeInTheDocument();
  expect(card.getByLabelText("负载 1 / 5 / 15 分钟")).toHaveTextContent("0.00 / 0.12 / 1.50");
  expect(card.getByRole("meter", { name: "0.0%" })).toHaveAttribute("aria-valuenow", "0");
  expect(card.getByRole("meter", { name: "0 B / 1.0 GiB" })).toHaveAttribute("aria-valuenow", "0");
  expect(card.getByText("3.0 GiB")).toBeInTheDocument();
  expect(card.getByRole("group", { name: "下载" })).toHaveTextContent("0 B/s");
  expect(card.getByRole("group", { name: "下载" })).toHaveTextContent("本周期 1.0 GiB");
  expect(card.getByRole("group", { name: "上传" })).toHaveTextContent("1.0 KiB/s");
  expect(card.getByRole("group", { name: "上传" })).toHaveTextContent("本周期 2.0 GiB");
  expect(card.queryByText("∞")).toBeNull();
});

it("离线保留最后读数并明确标记，从未上报不伪造零值、无限配额或运行时间", async () => {
  renderWithService(PublicService, { getSnapshot: async () => ({ now: 1000n, nodes: [
    { id: 1n, name: "旧读数", online: false, lastSeenAt: 900n, metrics: { cpuPct: 80, uptimeS: 5000n } },
    { id: 2n, name: "待接入", online: false },
  ] }) }, [{ path: "/", Component: PublicOverview }], "/");
  const stale = within(await screen.findByRole("article", { name: "旧读数" }));
  expect(stale.getByText("最后读数")).toBeInTheDocument();
  expect(stale.getByRole("meter", { name: "80%" })).toBeInTheDocument();
  expect(stale.getByText("运行 1h 23m")).toBeInTheDocument();
  const pending = within(screen.getByRole("article", { name: "待接入" }));
  expect(pending.getByText("从未上报")).toBeInTheDocument();
  expect(pending.queryAllByRole("meter")).toHaveLength(0);
  expect(pending.queryByText(/0 B|0%|∞|运行 0/)).toBeNull();
});
