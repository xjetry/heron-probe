import { Code, ConnectError } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { createConnectQueryKey } from "@connectrpc/connect-query";
import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { renderWithAdmin } from "../test/harness";
import { Nodes } from "./Nodes";
import { AdminService, GetSnapshotResponseSchema, GetRegisterWindowResponseSchema } from "../gen/probe/v1/admin_pb";

const two = [
  { id: 1n, name: "a", public: false, note: "", sortOrder: 0, createdAt: 0n, trafficResetDay: 1 },
  { id: 2n, name: "b", public: true, note: "db", sortOrder: 1, createdAt: 0n, trafficResetDay: 1 },
];

describe("Nodes", () => {
  it("编辑保存挂起与失败保留草稿，成功后才退出", async () => {
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    const updateNode = vi.fn(async () => {
      await gate;
      if (updateNode.mock.calls.length === 1) throw new ConnectError("node update rejected", Code.InvalidArgument);
      return { node: { ...two[0], name: "changed" } };
    });
    renderWithAdmin({ listNodes: async () => ({ nodes: two }), updateNode }, [{ path: "/nodes", Component: Nodes }], "/nodes");
    await screen.findByRole("link", { name: "a" });
    fireEvent.click(screen.getAllByRole("button", { name: "编辑" })[0]);
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "changed" } });
    fireEvent.click(screen.getByRole("button", { name: "保存" }));
    try {
      await waitFor(() => expect(updateNode).toHaveBeenCalledTimes(1));
      expect(screen.getByLabelText("名称")).toHaveValue("changed");
      expect(screen.getByRole("button", { name: "保存" })).toBeDisabled();
    } finally { await act(async () => { release(); }); }
    expect(await screen.findByRole("alert")).toHaveTextContent(/^node update rejected$/);
    expect(screen.getByLabelText("名称")).toHaveValue("changed");
    fireEvent.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(updateNode).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.queryByLabelText("名称")).toBeNull());
  });
  it.each(["", "29", "1.5", "15"])("重置日 %s 只有 1–28 的整数能保存", async (value) => {
    renderWithAdmin({ listNodes: async () => ({ nodes: two }) }, [{ path: "/nodes", Component: Nodes }], "/nodes");
    await screen.findByRole("link", { name: "a" });
    fireEvent.click(screen.getAllByRole("button", { name: "编辑" })[0]);
    fireEvent.change(screen.getByLabelText("重置日"), { target: { value } });
    if (value === "15") expect(screen.getByRole("button", { name: "保存" })).toBeEnabled();
    else expect(screen.getByRole("button", { name: "保存" })).toBeDisabled();
  });

  it("编辑重置日说明周期量清零的后果", async () => {
    renderWithAdmin({ listNodes: async () => ({ nodes: two }) }, [{ path: "/nodes", Component: Nodes }], "/nodes");
    await screen.findByRole("link", { name: "a" });
    fireEvent.click(screen.getAllByRole("button", { name: "编辑" })[0]);
    expect(screen.getByText("若从本周期起点算起新的重置日已经过去，本周期用量会立即清零。")).toBeInTheDocument();
  });

  it("列表显示重置日", async () => {
    renderWithAdmin({ listNodes: async () => ({ nodes: [{ ...two[0], trafficResetDay: 20 }] }) }, [{ path: "/nodes", Component: Nodes }], "/nodes");
    expect(await screen.findByRole("cell", { name: "每月 20 日" })).toBeInTheDocument();
  });

  it("变更只失效节点列表，不失效快照与注册窗口", async () => {
    const listNodes = vi.fn(async () => ({ nodes: two }));
    const { queryClient } = renderWithAdmin({ listNodes, createNode: async () => ({ node: two[0], token: "new" }) }, [{ path: "/nodes", Component: Nodes }], "/nodes");
    const snapshotKey = createConnectQueryKey({ schema: AdminService.method.getSnapshot, cardinality: "finite" });
    const windowKey = createConnectQueryKey({ schema: AdminService.method.getRegisterWindow, cardinality: "finite" });
    queryClient.setQueryData(snapshotKey, create(GetSnapshotResponseSchema));
    queryClient.setQueryData(windowKey, create(GetRegisterWindowResponseSchema));
    await screen.findByRole("link", { name: "a" });
    fireEvent.change(screen.getByLabelText("新节点名称"), { target: { value: "c" } });
    fireEvent.click(screen.getByRole("button", { name: "创建" }));
    await waitFor(() => expect(listNodes).toHaveBeenCalledTimes(2));
    expect([snapshotKey, windowKey].map((key) => queryClient.getQueryState(key)?.isInvalidated)).toEqual([false, false]);
  });

  it("再次编辑从当前节点而非旧草稿开始", async () => {
    let list = two;
    const { queryClient } = renderWithAdmin({ listNodes: async () => ({ nodes: list }) }, [{ path: "/nodes", Component: Nodes }], "/nodes");
    await screen.findByRole("link", { name: "a" });
    fireEvent.click(screen.getAllByRole("button", { name: "编辑" })[0]);
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "abandoned" } });
    fireEvent.click(screen.getByRole("button", { name: "取消" }));
    list = [{ ...two[0], name: "current", public: true, note: "current note" }, two[1]];
    await act(async () => { await queryClient.refetchQueries(); });
    await screen.findByRole("link", { name: "current" });
    fireEvent.click(screen.getAllByRole("button", { name: "编辑" })[0]);
    expect({
      name: (screen.getByLabelText("名称") as HTMLInputElement).value,
      public: (screen.getByLabelText("公开") as HTMLInputElement).checked,
      note: (screen.getByLabelText("备注") as HTMLInputElement).value,
    }).toEqual({ name: "current", public: true, note: "current note" });
  });

  it.each(["delete", "rotate"])("%s 请求挂起时禁止重复操作", async (operation) => {
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    const pending = async () => { await gate; return { token: "new" }; };
    renderWithAdmin({ listNodes: async () => ({ nodes: two }), deleteNode: async () => { await gate; return {}; }, rotateNodeToken: pending }, [{ path: "/nodes", Component: Nodes }], "/nodes");
    await screen.findByRole("link", { name: "a" });
    if (operation === "delete") fireEvent.click(screen.getAllByRole("button", { name: "删除" })[0]);
    const button = screen.getAllByRole("button", { name: operation === "delete" ? "确认删除 a" : "换 token" })[0];
    fireEvent.click(button);
    try { await waitFor(() => expect(button).toBeDisabled()); }
    finally { await act(async () => { release(); }); }
  });
  it.each(["list", "create"])("%s 失败时展示错误正文", async (source) => {
    const fail = async () => { throw new ConnectError("request failed", Code.Unavailable); };
    renderWithAdmin({ listNodes: source === "list" ? fail : async () => ({ nodes: two }), createNode: fail }, [{ path: "/nodes", Component: Nodes }], "/nodes");
    if (source === "create") {
      await screen.findByRole("link", { name: "a" });
      fireEvent.change(screen.getByLabelText("新节点名称"), { target: { value: "c" } });
      fireEvent.click(screen.getByRole("button", { name: "创建" }));
    }
    expect(await screen.findByRole("alert")).toHaveTextContent(/^request failed$/);
  });
  it("创建后一次性展示 token", async () => {
    const createNode = vi.fn(async () => ({ node: { ...two[0], id: 3n, name: "c" }, token: "deadbeef" }));
    const { router } = renderWithAdmin({ listNodes: async () => ({ nodes: two }), createNode }, [
      { path: "/nodes", Component: Nodes }, { path: "/away", element: <h1>away</h1> },
    ], "/nodes");
    await screen.findByRole("link", { name: "a" });
    fireEvent.change(screen.getByLabelText("新节点名称"), { target: { value: "c" } });
    fireEvent.click(screen.getByRole("button", { name: "创建" }));
    expect(await screen.findByLabelText("节点 c 的 token")).toHaveTextContent("deadbeef");
    expect(createNode).toHaveBeenCalledWith(expect.objectContaining({ name: "c" }), expect.anything());
    await act(() => router.navigate("/away"));
    await act(() => router.navigate("/nodes"));
    await screen.findByRole("link", { name: "a" });
    expect(screen.queryByText("deadbeef")).not.toBeInTheDocument();
  });

  it("删除需要二次确认", async () => {
    const deleteNode = vi.fn(async () => ({}));
    renderWithAdmin({ listNodes: async () => ({ nodes: two }), deleteNode }, [{ path: "/nodes", Component: Nodes }], "/nodes");
    await screen.findByRole("link", { name: "a" });
    await act(async () => { fireEvent.click(screen.getAllByRole("button", { name: "删除" })[0]); });
    expect(deleteNode).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "确认删除 a" }));
    await waitFor(() => expect(deleteNode).toHaveBeenCalledWith(expect.objectContaining({ id: 1n }), expect.anything()));
  });

  it.each(["下移 a", "上移 b"])("%s 提交完整排列", async (button) => {
    const reorderNodes = vi.fn(async () => ({}));
    const three = [...two, { ...two[0], id: 3n, name: "c", sortOrder: 2 }];
    renderWithAdmin({ listNodes: async () => ({ nodes: three }), reorderNodes }, [{ path: "/nodes", Component: Nodes }], "/nodes");
    await screen.findByRole("link", { name: "a" });
    fireEvent.click(screen.getByRole("button", { name: button }));
    await waitFor(() => expect(reorderNodes).toHaveBeenCalledWith(expect.objectContaining({ ids: [2n, 1n, 3n] }), expect.anything()));
  });

  it("编辑整体提交四个字段", async () => {
    const updateNode = vi.fn(async () => ({ node: two[0] }));
    renderWithAdmin({ listNodes: async () => ({ nodes: two }), updateNode }, [{ path: "/nodes", Component: Nodes }], "/nodes");
    await screen.findByRole("link", { name: "a" });
    fireEvent.click(screen.getAllByRole("button", { name: "编辑" })[0]);
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "a2" } });
    fireEvent.click(screen.getByLabelText("公开"));
    fireEvent.change(screen.getByLabelText("备注"), { target: { value: "changed note" } });
    fireEvent.change(screen.getByLabelText("重置日"), { target: { value: "15" } });
    fireEvent.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(updateNode).toHaveBeenCalledWith(expect.objectContaining({ id: 1n, name: "a2", public: true, note: "changed note", trafficResetDay: 15 }), expect.anything()));
  });

  it("轮换后显示并复制新 token", async () => {
    const rotateNodeToken = vi.fn(async () => ({ token: "new-token" }));
    const writeText = vi.fn(async () => {});
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    try {
      renderWithAdmin({ listNodes: async () => ({ nodes: two }), rotateNodeToken }, [{ path: "/nodes", Component: Nodes }], "/nodes");
      await screen.findByRole("link", { name: "a" });
      fireEvent.click(screen.getAllByRole("button", { name: "换 token" })[0]);
      await waitFor(() => expect(rotateNodeToken).toHaveBeenCalledWith(expect.objectContaining({ id: 1n }), expect.anything()));
      expect(await screen.findByLabelText("节点 a 的新 token")).toHaveTextContent("new-token");
      fireEvent.click(screen.getByRole("button", { name: "复制" }));
      expect(await screen.findByRole("button", { name: "已复制" })).toBeInTheDocument();
      expect(writeText).toHaveBeenCalledWith("new-token");
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
