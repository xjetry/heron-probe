import { Code, ConnectError } from "@connectrpc/connect";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { expect, it } from "vitest";
import { renderWithAdmin } from "../test/harness";
import { Layout } from "./Layout";

const routes = [{ path: "/", Component: Layout }, { path: "/login", element: <h1>login</h1> }];

it("登出在导航前清空查询与变更缓存", async () => {
  const { queryClient, router } = renderWithAdmin({ logout: async () => ({}) }, routes, "/");
  queryClient.setQueryData(["previous-session"], "old data");
  await queryClient.getMutationCache().build(queryClient, { mutationFn: async () => "old token" }).execute(undefined);
  let cacheAtLogin: number[] = [];
  router.subscribe((state) => {
    if (state.location.pathname === "/login") cacheAtLogin = [queryClient.getQueryCache().getAll().length, queryClient.getMutationCache().getAll().length];
  });
  fireEvent.click(screen.getByRole("button", { name: "登出" }));
  await waitFor(() => expect(router.state.location.pathname).toBe("/login"));
  expect(cacheAtLogin).toEqual([0, 0]);
});

it("登出失败显示错误并保留当前页面", async () => {
  const { router } = renderWithAdmin({ logout: async () => { throw new ConnectError("logout unavailable", Code.Unavailable); } }, routes, "/");
  fireEvent.click(screen.getByRole("button", { name: "登出" }));
  await waitFor(() => expect({ message: screen.queryByRole("alert")?.textContent, path: router.state.location.pathname }).toEqual({ message: "logout unavailable", path: "/" }));
});
