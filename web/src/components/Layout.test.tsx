import { Code, ConnectError } from "@connectrpc/connect";
import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { renderWithAdmin } from "../test/harness";
import { Layout } from "./Layout";
import { router as appRouter } from "../App";

vi.mock("./Chart", () => ({ Chart: () => null }));

const routes = [{ path: "/", Component: Layout }, { path: "/login", element: <h1>login</h1> }];

it("探测任务导航进入应用的任务页路由", async () => {
  renderWithAdmin({ listNodes: async () => ({ nodes: [] }), listProbeTasks: async () => ({ tasks: [] }) }, appRouter.routes, "/nodes");
  const link = screen.getByRole("link", { name: "探测任务" });
  expect(link).toHaveAttribute("href", "/probes");
  fireEvent.click(link);
  expect(await screen.findByRole("heading", { name: "探测任务" })).toBeInTheDocument();
});

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

it("导航后常驻布局不保留上一页的登出错误", async () => {
  const { router } = renderWithAdmin({ logout: async () => { throw new ConnectError("logout unavailable", Code.Unavailable); } }, [
    { path: "/", Component: Layout, children: [{ index: true, element: <h1>home</h1> }, { path: "nodes", element: <h1>nodes</h1> }] },
  ], "/");
  fireEvent.click(screen.getByRole("button", { name: "登出" }));
  await screen.findByRole("alert");
  await act(() => router.navigate("/nodes"));
  await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
});
