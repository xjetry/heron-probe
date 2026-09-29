import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { PublicService } from "../gen/probe/v1/public_pb";
import { BillingCycle } from "../gen/probe/v1/types_pb";
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

it("填了费用与到期的节点卡片多两行，已过期的到期标红，没填的不显示这两行", async () => {
  // 时钟放在与夹具错开的日期：剩余天数只能来自 hub 下发的 daysLeft，页面按本地日期重算会得出另一个数。
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(new Date(2031, 0, 1));
  const billed = {
    now: 1_000n,
    nodes: [
      { id: 5n, name: "paid", online: true, sortOrder: 0, billing: { price: "12.50", currency: "USD", billingCycle: BillingCycle.MONTHLY, expiresOn: "2026-10-01", daysLeft: 4 } },
      { id: 6n, name: "lapsed", online: true, sortOrder: 1, billing: { expiresOn: "2026-09-24", daysLeft: -3 } },
      { id: 7n, name: "plain", online: true, sortOrder: 2 },
    ],
  };
  renderWithService(PublicService, { getSnapshot: async () => billed }, [{ path: "/", Component: PublicOverview }], "/");
  const paid = within(await screen.findByRole("article", { name: "paid" }));
  expect(paid.getByText("费用").nextElementSibling).toHaveTextContent(/^USD 12\.50 \/ 月$/);
  const due = paid.getByText("到期").nextElementSibling;
  expect(due).toHaveTextContent(/^2026-10-01（剩 4 天）$/);
  expect(due).not.toHaveClass("error");
  const lapsed = within(screen.getByRole("article", { name: "lapsed" }));
  expect(lapsed.queryByText("费用")).toBeNull();
  expect(lapsed.getByText("2026-09-24（已过期 3 天）")).toHaveClass("error");
  const plain = within(screen.getByRole("article", { name: "plain" }));
  expect([plain.queryByText("费用"), plain.queryByText("到期")]).toEqual([null, null]);
});

it("卡片名称旁是国家 / 地区徽章：旗帜由国家码算出，照写国家码；没有国家的卡片不画徽章", async () => {
  const withCountry = { ...snapshot, nodes: [{ ...snapshot.nodes[0], country: "JP" }, snapshot.nodes[1]] };
  renderWithService(PublicService, { getSnapshot: async () => withCountry }, [{ path: "/", Component: PublicOverview }], "/");
  const web = within(await screen.findByRole("article", { name: "web-1" }));
  expect(web.getByTitle("国家 / 地区 JP")).toHaveTextContent("\u{1F1EF}\u{1F1F5} JP");
  expect(web.getByRole("heading", { level: 2 })).toHaveTextContent("web-1 \u{1F1EF}\u{1F1F5} JP");
  // 徽章在链接之外，不改变链接的可访问名。
  expect(web.getByRole("link", { name: "web-1" })).toBeInTheDocument();
  const db = within(screen.getByRole("article", { name: "db-1" }));
  expect(db.getByRole("heading", { level: 2 })).toHaveTextContent(/^db-1$/);
});

const tagged = {
  now: 1_000n,
  reportIntervalMs: 4000,
  nodes: [
    { id: 1n, name: "web-1", online: true, sortOrder: 0, tags: ["web", "prod"] },
    { id: 2n, name: "db-1", online: false, sortOrder: 1, tags: ["db", "prod"] },
    { id: 3n, name: "lab-1", online: true, sortOrder: 2, tags: ["DB"] },
    { id: 4n, name: "bare-1", online: true, sortOrder: 3, tags: [] },
  ],
};

const shown = () => screen.queryAllByRole("article").map((a) => a.getAttribute("aria-label"));
const chip = (name: string) => screen.getByRole("button", { name });

function renderTagged(getSnapshot: () => Promise<typeof tagged> = async () => tagged) {
  renderWithService(PublicService, { getSnapshot }, [{ path: "/", Component: PublicOverview }], "/");
}

it("标签栏：默认显示全部，标签按折叠去重", async () => {
  renderTagged();
  await screen.findByText("3 / 4 在线");
  expect(shown()).toEqual(["web-1", "db-1", "lab-1", "bare-1"]);
  expect(chip("全部")).toHaveAttribute("aria-pressed", "true");
  // db 与 DB 是同一个标签：只有一个按钮，保留先出现的写法。
  expect(screen.getAllByRole("button").map((b) => b.textContent)).toEqual(["全部", "db", "prod", "web"]);
});

it("没有任何节点带标签时不画标签栏", async () => {
  renderWithService(PublicService, { getSnapshot: async () => snapshot }, [{ path: "/", Component: PublicOverview }], "/");
  await screen.findByText("1 / 2 在线");
  expect(screen.queryByRole("button", { name: "全部" })).toBeNull();
});

it("单击只选这一个；再点同一个回到全部；点「全部」清空", async () => {
  renderTagged();
  await screen.findByText("3 / 4 在线");
  fireEvent.click(chip("prod"));
  expect(shown()).toEqual(["web-1", "db-1"]);
  expect(chip("prod")).toHaveAttribute("aria-pressed", "true");
  expect(chip("全部")).toHaveAttribute("aria-pressed", "false");
  // 顶部计数按过滤后的节点算。
  expect(screen.getByText("1 / 2 在线")).toBeInTheDocument();
  fireEvent.click(chip("web"));
  expect(shown()).toEqual(["web-1"]);
  expect(chip("prod")).toHaveAttribute("aria-pressed", "false");
  fireEvent.click(chip("web"));
  expect(shown()).toEqual(["web-1", "db-1", "lab-1", "bare-1"]);
  fireEvent.click(chip("db"));
  fireEvent.click(chip("全部"));
  expect(shown()).toEqual(["web-1", "db-1", "lab-1", "bare-1"]);
});

it("Shift+单击在其余标签状态不变的前提下翻转被点的一个，多选取交集", async () => {
  renderTagged();
  await screen.findByText("3 / 4 在线");
  fireEvent.click(chip("prod"));
  fireEvent.click(chip("db"), { shiftKey: true });
  expect(shown()).toEqual(["db-1"]);
  expect(chip("prod")).toHaveAttribute("aria-pressed", "true");
  expect(chip("db")).toHaveAttribute("aria-pressed", "true");
  fireEvent.click(chip("prod"), { shiftKey: true });
  // 只剩 db：DB 与 db 折叠后是同一个标签，两个节点都命中。
  expect(shown()).toEqual(["db-1", "lab-1"]);
  fireEvent.click(chip("db"), { shiftKey: true });
  expect(shown()).toEqual(["web-1", "db-1", "lab-1", "bare-1"]);
});

it("过滤后没有节点时给出说明，标签栏仍在", async () => {
  renderTagged();
  await screen.findByText("3 / 4 在线");
  fireEvent.click(chip("web"));
  fireEvent.click(chip("db"), { shiftKey: true });
  expect(shown()).toEqual([]);
  expect(screen.getByText("没有符合所选标签的节点。")).toBeInTheDocument();
  expect(chip("web")).toBeInTheDocument();
});

it("被选中的标签从快照里消失后回到显示全部，而不是留下看不见的过滤", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  let current: typeof tagged = tagged;
  renderTagged(async () => current);
  await screen.findByText("3 / 4 在线");
  fireEvent.click(chip("web"));
  expect(shown()).toEqual(["web-1"]);
  current = { ...tagged, nodes: tagged.nodes.map((n) => ({ ...n, tags: n.tags.filter((t) => t !== "web") })) };
  await act(async () => vi.advanceTimersByTimeAsync(POLL_MS + 100));
  await waitFor(() => expect(screen.queryByRole("button", { name: "web" })).toBeNull());
  expect(shown()).toEqual(["web-1", "db-1", "lab-1", "bare-1"]);
  expect(chip("全部")).toHaveAttribute("aria-pressed", "true");
});
