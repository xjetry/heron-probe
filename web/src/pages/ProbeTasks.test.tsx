import { describe, expect, it, vi } from "vitest";
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { createConnectQueryKey } from "@connectrpc/connect-query";
import { ConnectError, Code } from "@connectrpc/connect";
import { AdminService, ListNodesResponseSchema, ListProbeTasksResponseSchema, SaveProbeTaskResponseSchema, DeleteProbeTaskResponseSchema, type SaveProbeTaskRequest } from "../gen/probe/v1/admin_pb";
import { ProbeKind } from "../gen/probe/v1/types_pb";
import { renderWithAdmin, type AdminImpl } from "../test/harness";
import { ProbeTasks } from "./ProbeTasks";

const nodes = create(ListNodesResponseSchema, { nodes: [{ id: 1n, name: "东京" }, { id: 2n, name: "法兰克福" }] });
const tasks = create(ListProbeTasksResponseSchema, { version: 9n, tasks: [
  { task: { id: 3n, kind: ProbeKind.TCP, target: "1.1.1.1:443", intervalS: 30, timeoutMs: 1000 }, nodeIds: [1n, 2n] },
] });
const routes = [{ path: "/probes", Component: ProbeTasks }];

describe("ProbeTasks", () => {
  it("编辑保存挂起与失败保留草稿，成功后才退出", async () => {
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    const saveProbeTask = vi.fn(async () => {
      await gate;
      if (saveProbeTask.mock.calls.length === 1) throw new ConnectError("task update rejected", Code.InvalidArgument);
      return create(SaveProbeTaskResponseSchema, { version: 10n });
    });
    renderWithAdmin({ listNodes: async () => nodes, listProbeTasks: async () => tasks, saveProbeTask }, routes, "/probes");
    await screen.findByText("1.1.1.1:443");
    fireEvent.click(screen.getByRole("button", { name: "编辑 1.1.1.1:443" }));
    const form = screen.getByRole("form", { name: "编辑探测任务" });
    fireEvent.change(within(form).getByLabelText("目标"), { target: { value: "8.8.8.8:443" } });
    fireEvent.submit(form);
    try {
      await waitFor(() => expect(saveProbeTask).toHaveBeenCalledTimes(1));
      expect(screen.getByRole("form", { name: "编辑探测任务" })).toBeInTheDocument();
      expect(within(form).getByRole("button", { name: "保存" })).toBeDisabled();
    } finally { await act(async () => { release(); }); }
    expect(await screen.findByRole("alert")).toHaveTextContent(/^task update rejected$/);
    expect(within(screen.getByRole("form", { name: "编辑探测任务" })).getByLabelText("目标")).toHaveValue("8.8.8.8:443");
    fireEvent.submit(form);
    await waitFor(() => expect(saveProbeTask).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.queryByRole("form", { name: "编辑探测任务" })).toBeNull());
  });

  it("没有任务时显示空状态", async () => {
    renderWithAdmin({ listNodes: async () => nodes, listProbeTasks: async () => create(ListProbeTasksResponseSchema, {}) }, routes, "/probes");
    expect(await screen.findByText("还没有探测任务。")).toBeInTheDocument();
  });
  it("列出任务与分配的节点名", async () => {
    renderWithAdmin({ listNodes: async () => nodes, listProbeTasks: async () => tasks }, routes, "/probes");
    expect(await screen.findByText("1.1.1.1:443")).toBeInTheDocument();
    expect(screen.getByText("东京、法兰克福")).toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "TCP" })).toBeInTheDocument();
  });

  it("创建提交 id 0、完整字段与升序节点列表，并只失效任务列表", async () => {
    const saved: SaveProbeTaskRequest[] = [];
    const listNodes = vi.fn(async () => nodes);
    const listProbeTasks = vi.fn(async () => tasks);
    const { queryClient } = renderWithAdmin({
      listNodes, listProbeTasks,
      saveProbeTask: async (req) => { saved.push(req); return create(SaveProbeTaskResponseSchema, { version: 10n }); },
    }, routes, "/probes");
    await screen.findByText("东京、法兰克福");
    const nodesKey = createConnectQueryKey({ schema: AdminService.method.listNodes, cardinality: "finite" });
    queryClient.setQueryData(nodesKey, nodes);
    const form = screen.getByRole("form", { name: "新建探测任务" });
    fireEvent.change(within(form).getByLabelText("类型"), { target: { value: String(ProbeKind.ICMP) } });
    fireEvent.change(within(form).getByLabelText("目标"), { target: { value: "8.8.8.8" } });
    fireEvent.change(within(form).getByLabelText("间隔 (s)"), { target: { value: "60" } });
    fireEvent.change(within(form).getByLabelText("超时 (ms)"), { target: { value: "800" } });
    fireEvent.click(within(form).getByLabelText("法兰克福"));
    fireEvent.click(within(form).getByLabelText("东京"));
    fireEvent.submit(form);
    await waitFor(() => expect(saved).toHaveLength(1));
    expect(saved[0].task).toMatchObject({ id: 0n, kind: ProbeKind.ICMP, target: "8.8.8.8", intervalS: 60, timeoutMs: 800 });
    expect(saved[0].nodeIds).toEqual([1n, 2n]);
    await waitFor(() => expect(listProbeTasks).toHaveBeenCalledTimes(2));
    expect(queryClient.getQueryState(nodesKey)?.isInvalidated).toBe(false);
    expect(listNodes).toHaveBeenCalledTimes(1);
  });

  it("原生约束不满足时不发请求", async () => {
    const save = vi.fn(async () => create(SaveProbeTaskResponseSchema, {}));
    renderWithAdmin({ listNodes: async () => nodes, listProbeTasks: async () => tasks, saveProbeTask: save }, routes, "/probes");
    await screen.findByText("1.1.1.1:443");
    const form = screen.getByRole("form", { name: "新建探测任务" });
    fireEvent.change(within(form).getByLabelText("目标"), { target: { value: "8.8.8.8" } });
    fireEvent.change(within(form).getByLabelText("间隔 (s)"), { target: { value: "4" } });
    fireEvent.submit(form);
    await new Promise((r) => setTimeout(r, 20));
    expect(save).not.toHaveBeenCalled();
  });

  it("编辑提交整个任务与当前分配", async () => {
    const saved: SaveProbeTaskRequest[] = [];
    renderWithAdmin({
      listNodes: async () => nodes, listProbeTasks: async () => tasks,
      saveProbeTask: async (req) => { saved.push(req); return create(SaveProbeTaskResponseSchema, { version: 10n }); },
    }, routes, "/probes");
    await screen.findByText("1.1.1.1:443");
    fireEvent.click(screen.getByRole("button", { name: "编辑 1.1.1.1:443" }));
    const form = screen.getByRole("form", { name: "编辑探测任务" });
    fireEvent.change(within(form).getByLabelText("超时 (ms)"), { target: { value: "2000" } });
    fireEvent.click(within(form).getByLabelText("法兰克福"));
    fireEvent.submit(form);
    await waitFor(() => expect(saved).toHaveLength(1));
    expect(saved[0].task).toMatchObject({ id: 3n, kind: ProbeKind.TCP, target: "1.1.1.1:443", intervalS: 30, timeoutMs: 2000 });
    expect(saved[0].nodeIds).toEqual([1n]);
  });

  it("删除需要二次确认", async () => {
    const remove = vi.fn<NonNullable<AdminImpl["deleteProbeTask"]>>(async () => create(DeleteProbeTaskResponseSchema, { version: 11n }));
    renderWithAdmin({ listNodes: async () => nodes, listProbeTasks: async () => tasks, deleteProbeTask: remove }, routes, "/probes");
    await screen.findByText("1.1.1.1:443");
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "删除 1.1.1.1:443" })); });
    expect(remove).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "确认删除 1.1.1.1:443" }));
    await waitFor(() => expect(remove).toHaveBeenCalledTimes(1));
    expect(remove.mock.calls[0][0]).toMatchObject({ id: 3n });
  });

  it("服务端错误原文可见", async () => {
    renderWithAdmin({
      listNodes: async () => nodes, listProbeTasks: async () => tasks,
      saveProbeTask: async () => { throw new ConnectError("task.target: target for a TCP task must be host:port; got \"x\"", Code.InvalidArgument); },
    }, routes, "/probes");
    await screen.findByText("1.1.1.1:443");
    const form = screen.getByRole("form", { name: "新建探测任务" });
    fireEvent.change(within(form).getByLabelText("目标"), { target: { value: "x" } });
    fireEvent.change(within(form).getByLabelText("间隔 (s)"), { target: { value: "60" } });
    fireEvent.change(within(form).getByLabelText("超时 (ms)"), { target: { value: "800" } });
    fireEvent.submit(form);
    expect(await screen.findByRole("alert")).toHaveTextContent("task.target: target for a TCP task must be host:port");
  });
});
