import { Code, ConnectError } from "@connectrpc/connect";
import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { renderWithAdmin } from "../test/harness";
import { Nodes } from "./Nodes";

const two = [
  { id: 1n, name: "a", public: false, note: "", sortOrder: 0, createdAt: 0n },
  { id: 2n, name: "b", public: true, note: "db", sortOrder: 1, createdAt: 0n },
];

describe("Nodes", () => {
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

  it("编辑整体提交三个字段", async () => {
    const updateNode = vi.fn(async () => ({ node: two[0] }));
    renderWithAdmin({ listNodes: async () => ({ nodes: two }), updateNode }, [{ path: "/nodes", Component: Nodes }], "/nodes");
    await screen.findByRole("link", { name: "a" });
    fireEvent.click(screen.getAllByRole("button", { name: "编辑" })[0]);
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "a2" } });
    fireEvent.click(screen.getByLabelText("公开"));
    fireEvent.change(screen.getByLabelText("备注"), { target: { value: "changed note" } });
    fireEvent.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(updateNode).toHaveBeenCalledWith(expect.objectContaining({ id: 1n, name: "a2", public: true, note: "changed note" }), expect.anything()));
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
      expect(await screen.findByLabelText("节点 1 的新 token")).toHaveTextContent("new-token");
      fireEvent.click(screen.getByRole("button", { name: "复制" }));
      expect(await screen.findByRole("button", { name: "已复制" })).toBeInTheDocument();
      expect(writeText).toHaveBeenCalledWith("new-token");
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
