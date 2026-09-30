import { expect, it } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { renderWithAdmin, type AdminImpl } from "../test/harness";
import { Updates } from "./Updates";

const targets = [
  { nodeId: 0n, status: { supported: true, version: "v0.2.0" } },
  { nodeId: 1n, status: { supported: true, version: "v0.2.0" } },
  { nodeId: 2n, status: { supported: false, version: "v0.2.0", reason: "OpenRC is unsupported" } },
  { nodeId: 3n, status: { supported: true, version: "v0.2.0" } },
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
