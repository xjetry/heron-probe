import { Code, ConnectError } from "@connectrpc/connect";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { renderWithAdmin } from "../test/harness";
import { Login } from "./Login";

function renderLogin(login: (req: { password: string }) => Promise<Record<string, never>>) {
  return renderWithAdmin(
    { login },
    [
      { path: "/login", Component: Login },
      { path: "/", element: <h1>home</h1> },
    ],
    "/login",
  );
}

describe("Login", () => {
  it("提交密码，成功后回到首页", async () => {
    const login = vi.fn(async () => ({}));
    const { router, queryClient } = renderLogin(login);
    queryClient.setQueryData(["previous-session"], "old data");
    await queryClient.getMutationCache().build(queryClient, { mutationFn: async () => "old token" }).execute(undefined);
    let cacheAtHome: number[] = [];
    router.subscribe((state) => {
      if (state.location.pathname === "/") cacheAtHome = [queryClient.getQueryCache().getAll().length, queryClient.getMutationCache().getAll().length];
    });
    fireEvent.change(screen.getByLabelText("管理员密码"), { target: { value: "correct horse battery" } });
    fireEvent.click(screen.getByRole("button", { name: "登录" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/"));
    expect(login).toHaveBeenCalledWith(expect.objectContaining({ password: "correct horse battery" }), expect.anything());
    expect(cacheAtHome).toEqual([0, 0]);
  });

  it("失败时原样显示 hub 的信息并留在登录页", async () => {
    const { router } = renderLogin(async () => {
      throw new ConnectError("wrong password", Code.Unauthenticated);
    });
    fireEvent.change(screen.getByLabelText("管理员密码"), { target: { value: "nope nope nope" } });
    fireEvent.click(screen.getByRole("button", { name: "登录" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/^wrong password$/);
    expect(router.state.location.pathname).toBe("/login");
  });

  it("空密码不能提交", () => {
    renderLogin(vi.fn(async () => ({})));
    expect(screen.getByRole("button", { name: "登录" })).toBeDisabled();
  });
});
