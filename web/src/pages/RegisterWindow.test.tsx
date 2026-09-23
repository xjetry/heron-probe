import { Code, ConnectError } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { createConnectQueryKey } from "@connectrpc/connect-query";
import { act, fireEvent, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithAdmin } from "../test/harness";
import { RegisterWindow } from "./RegisterWindow";
import { AdminService, GetSnapshotResponseSchema, ListNodesResponseSchema } from "../gen/probe/v1/admin_pb";

afterEach(() => vi.useRealTimers());

describe("RegisterWindow", () => {
  it.each(["open", "close"])("%s 只失效注册窗口，不标脏其他查询", async (operation) => {
    let opened = operation === "close";
    const getRegisterWindow = vi.fn(async () => ({ open: opened, expiresAt: 4_000_000_000n, remaining: 3 }));
    const { queryClient } = renderWithAdmin({
      getRegisterWindow,
      openRegisterWindow: async () => { opened = true; return { key: "key", expiresAt: 4_000_000_000n, maxNodes: 3 }; },
      closeRegisterWindow: async () => { opened = false; return {}; },
    }, [{ path: "/register", Component: RegisterWindow }], "/register");
    const snapshotKey = createConnectQueryKey({ schema: AdminService.method.getSnapshot, cardinality: "finite" });
    const nodesKey = createConnectQueryKey({ schema: AdminService.method.listNodes, cardinality: "finite" });
    queryClient.setQueryData(snapshotKey, create(GetSnapshotResponseSchema));
    queryClient.setQueryData(nodesKey, create(ListNodesResponseSchema));
    await vi.waitFor(() => expect(getRegisterWindow).toHaveBeenCalledTimes(1));
    fireEvent.click(await screen.findByRole("button", { name: operation === "open" ? "开启新窗口" : "关闭窗口" }));
    await vi.waitFor(() => expect(getRegisterWindow).toHaveBeenCalledTimes(2));
    expect([snapshotKey, nodesKey].map((key) => queryClient.getQueryState(key)?.isInvalidated)).toEqual([false, false]);
  });

  it("清空名额保留空白而不是零值", async () => {
    renderWithAdmin({ getRegisterWindow: async () => ({ open: false }) }, [{ path: "/register", Component: RegisterWindow }], "/register");
    const input = screen.getByLabelText("可注册节点数") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "" } });
    expect({ value: input.value, nan: Number.isNaN(input.valueAsNumber) }).toEqual({ value: "", nan: true });
  });

  it.each(["", "0", "-1"])("名额为 '%s' 时禁用开窗", async (value) => {
    renderWithAdmin({ getRegisterWindow: async () => ({ open: false }) }, [{ path: "/register", Component: RegisterWindow }], "/register");
    await screen.findByText("当前没有开启的窗口。");
    fireEvent.change(screen.getByLabelText("可注册节点数"), { target: { value } });
    expect(screen.getByRole("button", { name: "开启新窗口" })).toBeDisabled();
  });

  it("轮询发现窗口失效时撤下 key 与命令", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let opened = false;
    renderWithAdmin({
      getRegisterWindow: async () => ({ open: opened, expiresAt: 4_000_000_000n, remaining: 3 }),
      openRegisterWindow: async () => { opened = true; return { key: "expires", expiresAt: 4_000_000_000n, maxNodes: 3 }; },
    }, [{ path: "/register", Component: RegisterWindow }], "/register");
    await screen.findByText("当前没有开启的窗口。");
    fireEvent.click(screen.getByRole("button", { name: "开启新窗口" }));
    await screen.findByRole("button", { name: "关闭窗口" });
    await screen.findByLabelText("注册 key");
    opened = false;
    await act(async () => vi.advanceTimersByTimeAsync(10_000));
    await screen.findByText("当前没有开启的窗口。");
    expect(screen.queryByLabelText("注册 key")).not.toBeInTheDocument();
    expect(screen.queryByText(/probe-agent register/)).not.toBeInTheDocument();
  });
  it.each(["status", "open"])("%s 失败时展示错误正文", async (source) => {
    const fail = async () => { throw new ConnectError("request failed", Code.Unavailable); };
    renderWithAdmin({
      getRegisterWindow: source === "status" ? fail : async () => ({ open: false, expiresAt: 0n, remaining: 0 }),
      openRegisterWindow: fail,
    }, [{ path: "/register", Component: RegisterWindow }], "/register");
    if (source === "open") fireEvent.click(screen.getByRole("button", { name: "开启新窗口" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/^request failed$/);
  });
  it("开窗后展示 key 与安装命令", async () => {
    let opened = false;
    const openRegisterWindow = vi.fn(async () => { opened = true; return { key: "cafe", expiresAt: 4_000_000_000n, maxNodes: 5 }; });
    const { router } = renderWithAdmin(
      { getRegisterWindow: async () => ({ open: opened, expiresAt: 4_000_000_000n, remaining: 5 }), openRegisterWindow },
      [{ path: "/register", Component: RegisterWindow }, { path: "/away", element: <h1>away</h1> }], "/register",
    );
    await screen.findByText("当前没有开启的窗口。");
    fireEvent.click(screen.getByRole("button", { name: "开启新窗口" }));
    expect(await screen.findByLabelText("注册 key")).toHaveTextContent("cafe");
    expect(screen.getByText(/probe-agent register --hub .* --key cafe/)).toBeInTheDocument();
    expect(openRegisterWindow).toHaveBeenCalledWith(expect.objectContaining({ ttlS: 3600, maxNodes: 5 }), expect.anything());
    await act(() => router.navigate("/away"));
    await act(() => router.navigate("/register"));
    await screen.findByRole("button", { name: "关闭窗口" });
    expect(screen.queryByLabelText("注册 key")).not.toBeInTheDocument();
  });

  it("开启中的窗口显示剩余名额并可关闭", async () => {
    const closeRegisterWindow = vi.fn(async () => ({}));
    renderWithAdmin(
      { getRegisterWindow: async () => ({ open: true, expiresAt: 4_000_000_000n, remaining: 3 }), closeRegisterWindow },
      [{ path: "/register", Component: RegisterWindow }], "/register",
    );
    expect(await screen.findByText(/剩余 3 个名额/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "关闭窗口" }));
    await vi.waitFor(() => expect(closeRegisterWindow).toHaveBeenCalled());
  });

  it("关窗清除刚展示的 key 与命令", async () => {
    let opened = false;
    renderWithAdmin({
      getRegisterWindow: async () => ({ open: opened, expiresAt: 4_000_000_000n, remaining: 3 }),
      openRegisterWindow: async () => { opened = true; return { key: "ephemeral", expiresAt: 4_000_000_000n, maxNodes: 3 }; },
      closeRegisterWindow: async () => { opened = false; return {}; },
    }, [{ path: "/register", Component: RegisterWindow }], "/register");
    await screen.findByText("当前没有开启的窗口。");
    fireEvent.click(screen.getByRole("button", { name: "开启新窗口" }));
    await screen.findByLabelText("注册 key");
    fireEvent.click(await screen.findByRole("button", { name: "关闭窗口" }));
    await screen.findByText("当前没有开启的窗口。");
    expect(screen.queryByLabelText("注册 key")).not.toBeInTheDocument();
    expect(screen.queryByText(/probe-agent register/)).not.toBeInTheDocument();
  });
});
