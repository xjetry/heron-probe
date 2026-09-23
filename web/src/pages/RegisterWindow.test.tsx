import { Code, ConnectError } from "@connectrpc/connect";
import { act, fireEvent, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { renderWithAdmin } from "../test/harness";
import { RegisterWindow } from "./RegisterWindow";

describe("RegisterWindow", () => {
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
    const openRegisterWindow = vi.fn(async () => ({ key: "cafe", expiresAt: 100n, maxNodes: 5 }));
    const { router } = renderWithAdmin(
      { getRegisterWindow: async () => ({ open: false, expiresAt: 0n, remaining: 0 }), openRegisterWindow },
      [{ path: "/register", Component: RegisterWindow }, { path: "/away", element: <h1>away</h1> }], "/register",
    );
    await screen.findByText("当前没有开启的窗口。");
    fireEvent.click(screen.getByRole("button", { name: "开启新窗口" }));
    expect(await screen.findByLabelText("注册 key")).toHaveTextContent("cafe");
    expect(screen.getByText(/probe-agent register --hub .* --key cafe/)).toBeInTheDocument();
    expect(openRegisterWindow).toHaveBeenCalledWith(expect.objectContaining({ ttlS: 3600, maxNodes: 5 }), expect.anything());
    await act(() => router.navigate("/away"));
    await act(() => router.navigate("/register"));
    await screen.findByText("当前没有开启的窗口。");
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
