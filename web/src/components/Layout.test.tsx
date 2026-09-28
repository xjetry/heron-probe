import { Code, ConnectError } from "@connectrpc/connect";
import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { renderWithAdmin } from "../test/harness";
import { Layout } from "./Layout";
import { router as appRouter } from "../App";

vi.mock("./Chart", () => ({ Chart: () => null }));

const routes = [{ path: "/", Component: Layout }, { path: "/login", element: <h1>login</h1> }];

it("安全导航进入应用的会话管理页", async () => {
  renderWithAdmin({ listNodes: async () => ({ nodes: [] }), listSessions: async () => ({ sessions: [] }) }, appRouter.routes, "/nodes");
  const link = screen.getByRole("link", { name: "安全" });
  fireEvent.click(link);
  expect(await screen.findByRole("heading", { name: "安全" })).toBeInTheDocument();
});

it("探测任务导航进入应用的任务页路由", async () => {
  renderWithAdmin({ listNodes: async () => ({ nodes: [] }), listProbeTasks: async () => ({ tasks: [] }) }, appRouter.routes, "/nodes");
  const link = screen.getByRole("link", { name: "探测任务" });
  expect(link).toHaveAttribute("href", "/probes");
  fireEvent.click(link);
  expect(await screen.findByRole("heading", { name: "探测任务" })).toBeInTheDocument();
});

it("通知渠道导航进入应用的渠道页路由", async () => {
  renderWithAdmin({ listNodes: async () => ({ nodes: [] }), listNotifyChannels: async () => ({ channels: [] }) }, appRouter.routes, "/nodes");
  const link = screen.getByRole("link", { name: "通知渠道" });
  expect(link).toHaveAttribute("href", "/channels");
  fireEvent.click(link);
  expect(await screen.findByRole("heading", { name: "通知渠道" })).toBeInTheDocument();
});

it("API token 导航进入应用的 token 页路由", async () => {
  renderWithAdmin({ listNodes: async () => ({ nodes: [] }), listApiTokens: async () => ({ tokens: [] }) }, appRouter.routes, "/nodes");
  const link = screen.getByRole("link", { name: "API token" });
  expect(link).toHaveAttribute("href", "/tokens");
  fireEvent.click(link);
  expect(await screen.findByRole("heading", { name: "API token" })).toBeInTheDocument();
});

it("外观导航进入应用的外观页路由", async () => {
  renderWithAdmin({ listNodes: async () => ({ nodes: [] }), getSettings: async () => ({ settings: { theme: "auto" } }) }, appRouter.routes, "/nodes");
  const link = screen.getByRole("link", { name: "外观" });
  expect(link).toHaveAttribute("href", "/appearance");
  fireEvent.click(link);
  expect(await screen.findByRole("heading", { name: "外观" })).toBeInTheDocument();
});

it("存储导航进入应用的存储页路由", async () => {
  renderWithAdmin({ listNodes: async () => ({ nodes: [] }), getStorageStats: async () => ({}) }, appRouter.routes, "/nodes");
  const link = screen.getByRole("link", { name: "存储" });
  expect(link).toHaveAttribute("href", "/storage");
  fireEvent.click(link);
  expect(await screen.findByRole("heading", { name: "存储" })).toBeInTheDocument();
});

it("告警规则导航进入应用的规则页路由", async () => {
  renderWithAdmin({ listNodes: async () => ({ nodes: [] }), listNotifyChannels: async () => ({ channels: [] }),
    listProbeTasks: async () => ({ tasks: [] }), listAlertRules: async () => ({ rules: [], states: [] }) }, appRouter.routes, "/nodes");
  const link = screen.getByRole("link", { name: "告警规则" });
  expect(link).toHaveAttribute("href", "/alerts");
  fireEvent.click(link);
  expect(await screen.findByRole("heading", { name: "告警规则" })).toBeInTheDocument();
});

it("告警事件导航进入应用的事件页路由", async () => {
  renderWithAdmin({ listNodes: async () => ({ nodes: [] }), listNotifyChannels: async () => ({ channels: [] }),
    listAlertEvents: async () => ({ events: [] }) }, appRouter.routes, "/nodes");
  const link = screen.getByRole("link", { name: "告警事件" });
  expect(link).toHaveAttribute("href", "/events");
  fireEvent.click(link);
  expect(await screen.findByRole("heading", { name: "告警事件" })).toBeInTheDocument();
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
