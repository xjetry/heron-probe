import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { TransportProvider } from "@connectrpc/connect-query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { describe, expect, it, vi } from "vitest";
import { AdminService } from "../gen/probe/v1/admin_pb";
import { Login } from "./Login";

function renderLogin(login: (req: { password: string }) => Promise<Record<string, never>>) {
  const transport = createRouterTransport(({ service }) => {
    service(AdminService, { login });
  });
  const router = createMemoryRouter(
    [
      { path: "/login", Component: Login },
      { path: "/", element: <h1>home</h1> },
    ],
    { initialEntries: ["/login"] },
  );
  render(
    <TransportProvider transport={transport}>
      <QueryClientProvider client={new QueryClient()}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </TransportProvider>,
  );
  return router;
}

describe("Login", () => {
  it("提交密码，成功后回到首页", async () => {
    const login = vi.fn(async () => ({}));
    const router = renderLogin(login);
    fireEvent.change(screen.getByLabelText("管理员密码"), { target: { value: "correct horse battery" } });
    fireEvent.click(screen.getByRole("button", { name: "登录" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/"));
    expect(login).toHaveBeenCalledWith(expect.objectContaining({ password: "correct horse battery" }), expect.anything());
  });

  it("失败时原样显示 hub 的信息并留在登录页", async () => {
    const router = renderLogin(async () => {
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
