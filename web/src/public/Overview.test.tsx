import { create } from "@bufbuild/protobuf";
import { act, cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { PublicService, PublicSnapshotSchema } from "../gen/heron/v1/public_pb";
import { POLL_MS } from "../lib/poll";
import { renderWithService } from "../test/harness";
import { PublicOverview } from "./Overview";
import { PUBLIC_VIEW_KEY } from "./view";

const snapshot = create(PublicSnapshotSchema, {
  now: 1_000n,
  reportIntervalMs: 4000,
  tags: ["db", "prod", "web"],
  nodes: [
    { id: 1n, name: "web-1", online: true, lastSeenAt: 998n, sortOrder: 0, country: "JP", tags: ["web", "prod"], metrics: { cpuPct: 42 } },
    { id: 2n, name: "db-1", online: false, lastSeenAt: 900n, sortOrder: 1, country: "JP", tags: ["db", "prod"], publicRemark: "联通 4837" },
    { id: 3n, name: "lab-1", online: true, lastSeenAt: 998n, sortOrder: 2, country: "HK", tags: ["DB"] },
    { id: 4n, name: "bare-1", online: false, sortOrder: 3, country: "" },
  ],
});
afterEach(() => { vi.useRealTimers(); localStorage.clear(); });

// 状态墙把在线、离线与从未上报都画成方块，筛选用例在墙上看结果；卡片视图把离线折起来。
const wall = () => fireEvent.click(within(screen.getByRole("group", { name: "视图" })).getByRole("button", { name: "状态墙" }));
const shown = () => screen.queryAllByRole("link").filter((a) => a.closest(".tile")).map((a) => a.getAttribute("aria-label"));
const open = (label: string) => fireEvent.click(within(screen.getByRole("group", { name: label })).getByRole("button", { name: new RegExp(`^${label}`) }));
const check = (label: string, name: string) => fireEvent.click(within(screen.getByRole("group", { name: label })).getByRole("checkbox", { name }));
function render(getSnapshot: () => Promise<typeof snapshot> = async () => snapshot) {
  renderWithService(PublicService, { getSnapshot }, [{ path: "/", Component: PublicOverview }], "/");
}

it("汇总、筛选行与节点一起出现；地区与标签是带计数的多选下拉", async () => {
  render();
  expect(await screen.findByText("2 / 4 在线")).toBeInTheDocument();
  wall();
  expect(shown()).toEqual(["web-1", "db-1", "lab-1", "bare-1"]);
  open("地区");
  expect(within(screen.getByRole("group", { name: "地区" })).getAllByRole("checkbox").map((c) => c.getAttribute("aria-label"))).toEqual(["香港", "日本", "未知"]);
  open("标签");
  // 标签的集合与顺序取 hub 下发的并集（按折叠键排序），页面不自己汇总、排序。
  expect(within(screen.getByRole("group", { name: "标签" })).getAllByRole("checkbox").map((c) => c.getAttribute("aria-label"))).toEqual(["db", "prod", "web"]);
});

it("地区并集、标签交集、搜索与只看在线叠加；计数按筛选后的节点算；滤空时说明", async () => {
  render();
  await screen.findByText("2 / 4 在线");
  wall();
  open("地区");
  check("地区", "日本");
  check("地区", "未知");
  expect(shown()).toEqual(["web-1", "db-1", "bare-1"]);
  expect(screen.getByText("1 / 3 在线")).toBeInTheDocument();
  open("标签");
  check("标签", "prod");
  expect(shown()).toEqual(["web-1", "db-1"]);
  fireEvent.click(screen.getByRole("button", { name: "只看在线" }));
  expect(shown()).toEqual(["web-1"]);
  fireEvent.change(screen.getByRole("searchbox", { name: "搜索节点" }), { target: { value: "4837" } });
  expect(shown()).toEqual([]);
  expect(screen.getByText("没有符合筛选条件的节点。")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "只看在线" }));
  expect(shown()).toEqual(["db-1"]);
});

it("标签折叠比较：选 db 时 DB 的节点也命中", async () => {
  render();
  await screen.findByText("2 / 4 在线");
  wall();
  open("标签");
  check("标签", "db");
  expect(shown()).toEqual(["lab-1", "db-1"]);
});

it("被选中的标签或地区从快照消失后从选择集里移除，不留下看不见的过滤", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  let current = snapshot;
  render(async () => current);
  await screen.findByText("2 / 4 在线");
  wall();
  open("标签");
  check("标签", "web");
  open("地区");
  check("地区", "未知");
  expect(shown()).toEqual([]);
  current = { ...snapshot, tags: ["db", "prod"], nodes: snapshot.nodes.map((n) => ({ ...n, tags: n.tags?.filter((t) => t !== "web"), country: n.country || "CA" })) };
  await act(async () => vi.advanceTimersByTimeAsync(POLL_MS + 100));
  await waitFor(() => expect(shown()).toEqual(["web-1", "db-1", "lab-1", "bare-1"]));
  expect(within(screen.getByRole("group", { name: "标签" })).queryByRole("button", { name: /^移除/ })).toBeNull();
  expect(within(screen.getByRole("group", { name: "地区" })).queryByRole("button", { name: /^移除/ })).toBeNull();
});

it("没有任何节点带标签时不画标签下拉；没有公开节点时只说明", async () => {
  render(async () => ({ ...snapshot, tags: [], nodes: snapshot.nodes.map((n) => ({ ...n, tags: [] })) }));
  await screen.findByText("2 / 4 在线");
  expect(screen.queryByRole("group", { name: "标签" })).toBeNull();
});

it("没有公开节点时说明，不画汇总与筛选行", async () => {
  render(async () => ({ ...snapshot, now: 1n, nodes: [], tags: [] }));
  expect(await screen.findByText("没有公开的节点。")).toBeInTheDocument();
  expect(screen.queryByRole("group", { name: "筛选" })).toBeNull();
});

it("视图切换：没选过时默认卡片带排序；状态墙带着色依据", async () => {
  render();
  await screen.findByText("2 / 4 在线");
  const views = within(screen.getByRole("group", { name: "视图" }));
  expect(views.getByRole("button", { name: "卡片" })).toHaveAttribute("aria-pressed", "true");
  expect(screen.getByRole("combobox", { name: "排序" })).toHaveValue("default");
  expect(screen.queryByRole("combobox", { name: "着色依据" })).toBeNull();
  expect(screen.getAllByRole("article").map((a) => a.getAttribute("aria-label"))).toEqual(["web-1", "lab-1"]);
  wall();
  expect(views.getByRole("button", { name: "状态墙" })).toHaveAttribute("aria-pressed", "true");
  expect(screen.getByRole("combobox", { name: "着色依据" })).toHaveValue("status");
  expect(screen.queryByRole("combobox", { name: "排序" })).toBeNull();
});

it("访客选的视图记到 localStorage，下次打开沿用", async () => {
  render();
  await screen.findByText("2 / 4 在线");
  wall();
  expect(localStorage.getItem(PUBLIC_VIEW_KEY)).toBe("wall");
  cleanup();
  render();
  await screen.findByText("2 / 4 在线");
  expect(within(screen.getByRole("group", { name: "视图" })).getByRole("button", { name: "状态墙" })).toHaveAttribute("aria-pressed", "true");
  expect(shown()).toEqual(["web-1", "db-1", "lab-1", "bare-1"]);
  fireEvent.click(within(screen.getByRole("group", { name: "视图" })).getByRole("button", { name: "卡片" }));
  expect(localStorage.getItem(PUBLIC_VIEW_KEY)).toBe("cards");
});

it("按 POLL_MS 轮询快照", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  const getSnapshot = vi.fn(async () => snapshot);
  render(getSnapshot);
  await screen.findByText("2 / 4 在线");
  await act(async () => vi.advanceTimersByTimeAsync(POLL_MS + 100));
  await waitFor(() => expect(getSnapshot.mock.calls.length).toBeGreaterThanOrEqual(2));
});
