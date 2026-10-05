import { expect, it } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { createConnectQueryKey } from "@connectrpc/connect-query";
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import type { MessageInitShape } from "@bufbuild/protobuf";
import { AdminService, type GetUpdatesResponse, GetUpdatesResponseSchema } from "../gen/heron/v1/admin_pb";
import { renderWithAdmin, type AdminImpl } from "../test/harness";
import { Updates } from "./Updates";

const targets = [
  { nodeId: 0n, status: { supported: true, version: "v0.2.0" } },
  { nodeId: 1n, status: { supported: true, version: "v0.2.0", source: "hub" } },
  { nodeId: 2n, status: { supported: false, version: "v0.2.0", reason: "OpenRC is unsupported" } },
  { nodeId: 3n, status: { supported: true, version: "v0.2.0", source: "github" } },
];
function render(impl: AdminImpl = {}) {
  return renderWithAdmin({
    getUpdates: async (req) => ({ targets, latestVersion: req.checkLatest ? "v0.3.0" : "" }),
    listNodes: async () => ({ nodes: [{ id: 1n, name: "东京" }, { id: 2n, name: "香港" }, { id: 3n, name: "西雅图" }] }),
    ...impl,
  }, [{ path: "/updates", Component: Updates }], "/updates");
}
async function check() { fireEvent.click(await screen.findByRole("button", { name: "检查官方新版本" })); await screen.findByRole("heading", { name: "v0.3.0" }); }

it("不自动联网检查，按实际更新能力禁用目标", async () => {
  const checks: boolean[] = [];
  render({ getUpdates: async (req) => { checks.push(req.checkLatest); return { targets, latestVersion: req.checkLatest ? "v0.3.0" : "" }; } });
  expect(await screen.findByRole("button", { name: "更新 Hub" })).toBeDisabled();
  expect(checks).toEqual([false]);
  await check();
  expect(screen.getByRole("button", { name: "更新 Hub" })).toBeEnabled();
  expect(screen.getByRole("checkbox", { name: "选择 香港（#2）" })).toBeDisabled();
  expect(screen.getByText(/OpenRC is unsupported/)).toBeInTheDocument();
});

it("Hub 要求确认，只发送固定目标和版本，提交不冒充成功", async () => {
  const calls: unknown[] = [];
  render({ startUpdate: async (req) => { calls.push({ nodeId: req.nodeId, version: req.version }); return {}; } });
  await check(); fireEvent.click(screen.getByRole("button", { name: "更新 Hub" }));
  const modal = await screen.findByRole("dialog", { name: "确认更新 Hub" });
  expect(calls).toEqual([]);
  expect(within(modal).getByText(/短暂断连/)).toBeInTheDocument();
  fireEvent.click(within(modal).getByRole("button", { name: "确认更新" }));
  await screen.findByText(/Hub：更新任务已提交/);
  expect(calls).toEqual([{ nodeId: 0n, version: "v0.3.0" }]);
  expect(screen.queryByText("更新成功")).toBeNull();
  expect(screen.getByRole("heading", { name: "v0.3.0" })).toBeInTheDocument();
});

it("批量更新逐目标显示部分失败", async () => {
  const ids: bigint[] = [];
  render({ startUpdate: async (req) => { ids.push(req.nodeId); if (req.nodeId === 3n) throw new ConnectError("节点已在更新", Code.FailedPrecondition); return {}; } });
  await check();
  fireEvent.click(screen.getByRole("checkbox", { name: "选择 东京（#1）" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "选择 西雅图（#3）" }));
  fireEvent.click(screen.getByRole("button", { name: "更新选中节点（2）" }));
  fireEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "确认更新" }));
  await screen.findByText(/西雅图：.*节点已在更新/);
  expect(screen.getByText(/东京：更新任务已提交/)).toBeInTheDocument();
  expect(ids).toEqual([1n, 3n]);
});

it("显示每个节点取产物的来源", async () => {
  render();
  expect(await screen.findByText("经 hub 中转")).toBeInTheDocument();
  expect(screen.getByText("GitHub 直连")).toBeInTheDocument();
  // 精确匹配每个标签：页面说明文字也提到"经 hub 中转"，子串正则会把它多算一个。
  expect(screen.getAllByText("经 hub 中转")).toHaveLength(1);
  expect(screen.getAllByText("GitHub 直连")).toHaveLength(1);
});

it("仅排队任务可取消，回滚原因可见", async () => {
  const cancelled: unknown[] = [];
  render({ getUpdates: async () => ({ targets: [
    { nodeId: 1n, status: { supported: true, version: "v0.2.0", task: { id: "queued-task", version: "v0.3.0", state: "queued" } } },
    { nodeId: 3n, status: { supported: true, version: "v0.2.0", task: { id: "rollback-task", version: "v0.3.0", state: "rolled_back", error: "new process timeout" } } },
  ] }), cancelUpdate: async (req) => { cancelled.push({ nodeId: req.nodeId, id: req.id }); return {}; } });
  fireEvent.click(await screen.findByRole("button", { name: "取消排队" }));
  await waitFor(() => expect(cancelled).toEqual([{ nodeId: 1n, id: "queued-task" }]));
  expect(screen.getByText("已回滚")).toBeInTheDocument();
  expect(screen.getByText("new process timeout")).toBeInTheDocument();
});

type TargetsInit = NonNullable<Exclude<MessageInitShape<typeof GetUpdatesResponseSchema>, GetUpdatesResponse>["targets"]>;
const selectAll = () => screen.getByRole("checkbox", { name: "选择全部可更新节点" });
const row = (name: string) => screen.getByRole("checkbox", { name: `选择 ${name}` });

it("全选只选行上可勾的节点：检查版本前禁用，部分选中为半选，全选后再点清空", async () => {
  render();
  await screen.findByRole("button", { name: "更新 Hub" });
  expect(selectAll()).toBeDisabled();
  await check();
  expect(selectAll()).toBeEnabled();
  expect(selectAll()).not.toBeChecked();
  fireEvent.click(row("东京（#1）"));
  expect(selectAll()).toHaveAttribute("aria-checked", "mixed");
  expect((selectAll() as HTMLInputElement).indeterminate).toBe(true);
  fireEvent.click(selectAll());
  expect(selectAll()).toBeChecked();
  expect(row("东京（#1）")).toBeChecked();
  expect(row("西雅图（#3）")).toBeChecked();
  // 香港不支持在线更新，行上不可勾，全选也不能选中它。
  expect(row("香港（#2）")).not.toBeChecked();
  expect(screen.getByRole("button", { name: "更新选中节点（2）" })).toBeEnabled();
  fireEvent.click(selectAll());
  expect(selectAll()).not.toBeChecked();
  expect(row("东京（#1）")).not.toBeChecked();
  expect(row("西雅图（#3）")).not.toBeChecked();
  expect(screen.getByRole("button", { name: "更新选中节点（0）" })).toBeDisabled();
});

it("全选后提交按节点列表顺序逐个下发", async () => {
  const ids: bigint[] = [];
  render({ startUpdate: async (req) => { ids.push(req.nodeId); return {}; } });
  await check();
  fireEvent.click(selectAll());
  fireEvent.click(screen.getByRole("button", { name: "更新选中节点（2）" }));
  fireEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "确认更新" }));
  await screen.findByText(/西雅图：更新任务已提交/);
  expect(ids).toEqual([1n, 3n]);
});

it("轮询使已选节点失去更新资格时，计数与全选状态一起收缩", async () => {
  let current: TargetsInit = targets;
  const { queryClient } = render({ getUpdates: async (req) => ({ targets: current, latestVersion: req.checkLatest ? "v0.3.0" : "" }) });
  await check();
  fireEvent.click(selectAll());
  expect(screen.getByRole("button", { name: "更新选中节点（2）" })).toBeEnabled();
  // 西雅图在别处已开始更新：有进行中的任务即不再可选。
  current = targets.map((t) => t.nodeId === 3n ? { ...t, status: { ...t.status, task: { id: "running", version: "v0.3.0", state: "dispatched" } } } : t);
  await act(async () => { await queryClient.refetchQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.getUpdates, cardinality: "finite" }) }); });
  await waitFor(() => expect(row("西雅图（#3）")).toBeDisabled());
  expect(screen.getByRole("button", { name: "更新选中节点（1）" })).toBeEnabled();
  // 仍可更新的只剩东京且已选中，全选框是勾选而不是半选。
  expect(selectAll()).toBeChecked();
  expect(selectAll()).toHaveAttribute("aria-checked", "true");
});
