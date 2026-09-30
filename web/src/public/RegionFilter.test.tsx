import { act, fireEvent, screen, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { PublicService } from "../gen/heron/v1/public_pb";
import { POLL_MS } from "../lib/poll";
import { renderWithService } from "../test/harness";
import { PublicOverview } from "./Overview";

const nodes = [
  { id: 1n, name: "us", country: "US", online: true, tags: ["prod"] },
  { id: 2n, name: "jp-off", country: "JP", online: false, tags: ["prod"], billing: { expiresOn: "2030-01-01", daysLeft: 2 } },
  { id: 3n, name: "hk", country: "HK", online: true },
  { id: 4n, name: "unknown", country: "", online: false },
  { id: 5n, name: "jp-on", country: "JP", online: true, tags: ["prod"], billing: { expiresOn: "2030-01-01", daysLeft: 1 } },
];
const shown = () => screen.queryAllByRole("article").map((a) => a.getAttribute("aria-label"));
const region = () => within(screen.getByRole("group", { name: "按地区筛选" }));
const render = (getSnapshot = async () => ({ now: 1n, tags: ["prod"], nodes })) => renderWithService(PublicService, { getSnapshot }, [{ path: "/", Component: PublicOverview }], "/");
afterEach(() => vi.useRealTimers());

it("地区自动去重按代码排序，未知最后；切换只过滤，不改变节点顺序", async () => {
  render();
  await screen.findByRole("article", { name: "us" });
  expect(region().getAllByRole("button").map((b) => b.textContent)).toEqual(["全部", "HK", "JP", "US", "未知"]);
  expect(region().getByRole("button", { name: "全部" })).toHaveAttribute("aria-pressed", "true");
  expect(screen.queryByText("地区", { exact: true })).toBeNull();
  expect(screen.queryByText("可多选，未选则显示全部地区")).toBeNull();
  fireEvent.click(region().getByRole("button", { name: "JP" }));
  expect(shown()).toEqual(["jp-off", "jp-on"]);
  expect(screen.getByText("1 / 2 在线")).toBeInTheDocument();
  expect(region().getByRole("button", { name: "JP" })).toHaveAttribute("aria-pressed", "true");
  fireEvent.click(region().getByRole("button", { name: "未知" }), { shiftKey: true });
  expect(shown()).toEqual(["jp-off", "unknown", "jp-on"]);
  fireEvent.click(region().getByRole("button", { name: "JP" }));
  expect(shown()).toEqual(["jp-off", "jp-on"]);
  fireEvent.click(region().getByRole("button", { name: "未知" }));
  expect(shown()).toEqual(["unknown"]);
  fireEvent.click(region().getByRole("button", { name: "未知" }));
  expect(shown()).toEqual(["us", "jp-off", "hk", "unknown", "jp-on"]);
});

it("地区与标签、离线、到期排序叠加，分类不随过滤结果消失", async () => {
  render();
  await screen.findByRole("article", { name: "us" });
  fireEvent.click(region().getByRole("button", { name: "JP" }));
  fireEvent.click(within(screen.getByRole("group", { name: "按标签筛选" })).getByRole("button", { name: "prod" }));
  fireEvent.click(screen.getByRole("button", { name: "按到期时间排序" }));
  expect(shown()).toEqual(["jp-on", "jp-off"]);
  fireEvent.click(screen.getByRole("button", { name: "仅离线" }));
  expect(shown()).toEqual(["jp-off"]);
  fireEvent.click(region().getByRole("button", { name: "JP" }));
  fireEvent.click(region().getByRole("button", { name: "HK" }));
  expect(shown()).toEqual([]);
  expect(screen.getByText("没有符合筛选条件的节点。")).toBeInTheDocument();
  expect(region().getAllByRole("button")).toHaveLength(5);
});

it("未知节点探测成功后分类更新，已消失的选择回到全部且不会隐式恢复", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  let current = nodes;
  render(async () => ({ now: 1n, tags: ["prod"], nodes: current }));
  await screen.findByRole("article", { name: "unknown" });
  fireEvent.click(region().getByRole("button", { name: "未知" }));
  current = nodes.map((n) => n.country === "" ? { ...n, country: "CA" } : n);
  await act(async () => vi.advanceTimersByTimeAsync(POLL_MS + 100));
  expect(region().queryByRole("button", { name: "未知" })).toBeNull();
  expect(region().getByRole("button", { name: "CA" })).toBeInTheDocument();
  expect(region().getByRole("button", { name: "全部" })).toHaveAttribute("aria-pressed", "true");
  current = nodes;
  await act(async () => vi.advanceTimersByTimeAsync(POLL_MS + 100));
  expect(region().getByRole("button", { name: "未知" })).toHaveAttribute("aria-pressed", "false");
  expect(shown()).toHaveLength(5);
});

it("没有公开节点时不渲染地区分类", async () => {
  render(async () => ({ now: 1n, tags: [], nodes: [] }));
  await screen.findByText("没有公开的节点。");
  expect(screen.queryByRole("group", { name: "按地区筛选" })).toBeNull();
});

it("家宽标签与香港日本地区取交集，地区之间取并集", async () => {
  const home = [
    { id: 11n, name: "香港家宽", country: "HK", online: true, tags: ["家宽"] },
    { id: 12n, name: "日本家宽", country: "JP", online: true, tags: ["家宽"] },
    { id: 13n, name: "美国家宽", country: "US", online: true, tags: ["家宽"] },
    { id: 14n, name: "香港机房", country: "HK", online: true, tags: ["机房"] },
    { id: 15n, name: "日本机房", country: "JP", online: true, tags: ["机房"] },
  ];
  render(async () => ({ now: 1n, tags: ["家宽", "机房"], nodes: home }));
  await screen.findByRole("article", { name: "香港家宽" });
  fireEvent.click(within(screen.getByRole("group", { name: "按标签筛选" })).getByRole("button", { name: "家宽" }));
  fireEvent.click(region().getByRole("button", { name: "HK" }));
  fireEvent.click(region().getByRole("button", { name: "JP" }), { shiftKey: true });
  expect(shown()).toEqual(["香港家宽", "日本家宽"]);
  fireEvent.click(region().getByRole("button", { name: "全部" }));
  expect(shown()).toEqual(["香港家宽", "日本家宽", "美国家宽"]);
});
