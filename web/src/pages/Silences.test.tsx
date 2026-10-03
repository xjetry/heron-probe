import { expect, it, vi } from "vitest";
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { ListNodesResponseSchema, ListSilencesResponseSchema, SilenceKind, type SaveSilenceRequest } from "../gen/heron/v1/admin_pb";
import { renderWithAdmin, type AdminImpl } from "../test/harness";
import { Silences } from "./Silences";

const nodes = create(ListNodesResponseSchema, { nodes: [{ id: 1n, name: "东京" }, { id: 2n, name: "法兰克福" }] });
const silences = create(ListSilencesResponseSchema, { silences: [
  { silence: { id: 7n, name: "夜间维护", enabled: true, allNodes: true, kind: SilenceKind.DAILY, startHhmm: "22:00", endHhmm: "06:00", reason: "机房巡检", createdAt: 100n }, active: true },
  { silence: { id: 8n, name: "升级窗口", enabled: true, kind: SilenceKind.ONCE, fromAt: 1760000000n, untilAt: 1760086400n, nodeIds: [1n], createdAt: 100n }, active: false },
  { silence: { id: 9n, name: "停用的", enabled: false, allNodes: true, kind: SilenceKind.DAILY, startHhmm: "00:00", endHhmm: "23:59", createdAt: 100n }, active: true },
] });
const routes = [{ path: "/silences", Component: Silences }];
const base: AdminImpl = { listNodes: async () => nodes, listSilences: async () => silences };
const render = (impl: AdminImpl = {}) => renderWithAdmin({ ...base, ...impl }, routes, "/silences");

it("列出窗口、作用域与生效状态", async () => {
  render();
  const daily = within((await screen.findByRole("cell", { name: /夜间维护/ })).closest("tr")!);
  expect(daily.getByRole("cell", { name: "每日 22:00–06:00（hub 时区）" })).toBeInTheDocument();
  expect(daily.getByRole("cell", { name: "全部节点" })).toBeInTheDocument();
  expect(daily.getByRole("cell", { name: "生效中" })).toBeInTheDocument();
  expect(daily.getByText("机房巡检")).toBeInTheDocument();
  const once = within(screen.getByRole("cell", { name: "升级窗口" }).closest("tr")!);
  expect(once.getByRole("cell", { name: "东京" })).toBeInTheDocument();
  expect(once.getByRole("cell", { name: "窗口外" })).toBeInTheDocument();
  const disabled = within(screen.getByRole("cell", { name: "停用的" }).closest("tr")!);
  expect(disabled.getByRole("cell", { name: "已停用" })).toBeInTheDocument();
});

it("每日静默只提交窗口字段，一次性静默只提交起止时间", async () => {
  const saved: SaveSilenceRequest[] = [];
  render({ saveSilence: async (req) => { saved.push(req); return {}; } });
  const form = await screen.findByRole("form", { name: "新建维护静默" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "夜间维护" } });
  fireEvent.click(within(form).getByRole("button", { name: "创建" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  expect(saved[0].silence).toMatchObject({
    name: "夜间维护", enabled: true, allNodes: true, nodeIds: [], selectorTags: [],
    kind: SilenceKind.DAILY, startHhmm: "22:00", endHhmm: "06:00", fromAt: 0n, untilAt: 0n,
  });
  // 创建成功后表单按 creation 键重挂（清空草稿），要重新取表单并重新填必填项。
  const again = await screen.findByRole("form", { name: "新建维护静默" });
  fireEvent.change(within(again).getByLabelText("名称"), { target: { value: "升级窗口" } });
  fireEvent.change(within(again).getByLabelText("类型"), { target: { value: String(SilenceKind.ONCE) } });
  fireEvent.change(within(again).getByLabelText("开始"), { target: { value: "2026-10-04T22:00" } });
  fireEvent.change(within(again).getByLabelText("结束"), { target: { value: "2026-10-05T06:00" } });
  fireEvent.click(within(again).getByRole("button", { name: "创建" }));
  await waitFor(() => expect(saved).toHaveLength(2));
  expect(saved[1].silence).toMatchObject({
    kind: SilenceKind.ONCE, startHhmm: "", endHhmm: "",
    fromAt: BigInt(Math.floor(new Date("2026-10-04T22:00").getTime() / 1000)),
    untilAt: BigInt(Math.floor(new Date("2026-10-05T06:00").getTime() / 1000)),
  });
});

it("编辑保留完整载荷，删除两段确认后发出", async () => {
  const saved: SaveSilenceRequest[] = [];
  const deleted: bigint[] = [];
  render({
    saveSilence: async (req) => { saved.push(req); return {}; },
    deleteSilence: async (req) => { deleted.push(req.id); return {}; },
  });
  fireEvent.click(await screen.findByRole("button", { name: "编辑 夜间维护（#7）" }));
  const form = screen.getByRole("form", { name: "编辑 夜间维护（#7）" });
  expect(within(form).getByLabelText("名称")).toHaveValue("夜间维护");
  expect(within(form).getByLabelText("开始（HH:MM）")).toHaveValue("22:00");
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "夜间巡检" } });
  fireEvent.click(within(form).getByRole("button", { name: "保存" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  // created_at 由 hub 在保存时保留原值，面板不回发。
  expect(saved[0].silence).toEqual({ ...silences.silences[0].silence, name: "夜间巡检", createdAt: 0n });
  await waitFor(() => expect(screen.queryByRole("form", { name: "编辑 夜间维护（#7）" })).toBeNull());
  fireEvent.click(screen.getByRole("button", { name: "删除 升级窗口（#8）" }));
  expect(deleted).toHaveLength(0);
  fireEvent.click(screen.getByRole("button", { name: "确认删除 升级窗口（#8）" }));
  await waitFor(() => expect(deleted).toEqual([8n]));
});

it("动态标签作用域展示交集与当前节点数", async () => {
  render({ listSilences: async () => create(ListSilencesResponseSchema, { silences: [
    { silence: { id: 1n, name: "数据库维护", enabled: true, kind: SilenceKind.DAILY, startHhmm: "01:00", endHhmm: "02:00", selectorTags: ["db", "west"], nodeIds: [2n], createdAt: 100n }, active: false },
  ] }) });
  const row = within((await screen.findByRole("cell", { name: "数据库维护" })).closest("tr")!);
  expect(row.getByRole("cell", { name: "动态标签：db ∩ west；当前 1 个节点" })).toBeInTheDocument();
});

// 页面的时间输入用本地墙钟，hub 按 Unix 秒判定；刷新不重置正在编辑的草稿。
it("编辑中的草稿不被周期刷新覆盖", async () => {
  vi.useFakeTimers();
  try {
    render();
    await act(async () => { await vi.advanceTimersByTimeAsync(100); });
    fireEvent.click(screen.getByRole("button", { name: "编辑 升级窗口（#8）" }));
    const form = screen.getByRole("form", { name: "编辑 升级窗口（#8）" });
    fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "尚未保存" } });
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(screen.getByRole("form", { name: "编辑 升级窗口（#8）" })).toBe(form);
    expect(within(form).getByLabelText("名称")).toHaveValue("尚未保存");
  } finally { vi.useRealTimers(); }
});
