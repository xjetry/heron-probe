import { Code, ConnectError } from "@connectrpc/connect";
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { expect, it } from "vitest";
import { renderWithAdmin, type AdminImpl } from "../test/harness";
import { Sessions } from "./Sessions";

const currentID = "a".repeat(64);
const otherID = "b".repeat(64);
const sessions = [
  { id: currentID, createdAt: 1700000000n, lastUsedAt: 1700000600n, current: true },
  { id: otherID, createdAt: 1699000000n, lastUsedAt: 1699001200n, current: false },
];
const routes = [{ path: "/security", Component: Sessions }, { path: "/login", element: <h1>登录页</h1> }];
const render = (impl: AdminImpl = {}) => renderWithAdmin({ listSessions: async () => ({ sessions }), ...impl }, routes, "/security");

it("会话列表显示创建与最近使用时刻并仅标记当前会话", async () => {
  render();
  for (const s of sessions) {
    const row = (await screen.findByRole("button", { name: `撤销会话 ${s.id.slice(0, 12)}` })).closest("tr")!;
    expect(within(row).getByText(new Date(Number(s.createdAt) * 1000).toLocaleString())).toBeInTheDocument();
    expect(within(row).getByText(new Date(Number(s.lastUsedAt) * 1000).toLocaleString())).toBeInTheDocument();
    expect(within(row).queryByText("当前") != null).toBe(s.current);
  }
});

it("撤销其它会话先确认再按 hash 调用并刷新列表", async () => {
  const revoked: string[] = [];
  const { router } = render({
    listSessions: async () => ({ sessions: sessions.filter((s) => !revoked.includes(s.id)) }),
    revokeSession: async (req) => { revoked.push(req.id); return {}; },
  });
  const revokeButton = await screen.findByRole("button", { name: `撤销会话 ${otherID.slice(0, 12)}` });
  await act(async () => { fireEvent.click(revokeButton); });
  expect(revoked).toEqual([]);
  fireEvent.click(screen.getByRole("button", { name: `确认撤销会话 ${otherID.slice(0, 12)}` }));
  await waitFor(() => expect(revoked).toEqual([otherID]));
  await waitFor(() => expect(screen.queryByRole("button", { name: `确认撤销会话 ${otherID.slice(0, 12)}` })).not.toBeInTheDocument());
  expect(router.state.location.pathname).toBe("/security");
  expect(screen.getByRole("button", { name: `撤销会话 ${currentID.slice(0, 12)}` })).toBeInTheDocument();
});

it("撤销当前会话清空缓存后跳转登录页", async () => {
  const revoked: string[] = [];
  const { queryClient, router } = render({ revokeSession: async (req) => { revoked.push(req.id); return {}; } });
  queryClient.setQueryData(["private-data"], "private");
  let cacheAtLogin: number[] = [];
  router.subscribe((state) => {
    if (state.location.pathname === "/login") cacheAtLogin = [queryClient.getQueryCache().getAll().length, queryClient.getMutationCache().getAll().length];
  });
  fireEvent.click(await screen.findByRole("button", { name: `撤销会话 ${currentID.slice(0, 12)}` }));
  fireEvent.click(screen.getByRole("button", { name: `确认撤销会话 ${currentID.slice(0, 12)}` }));
  await screen.findByRole("heading", { name: "登录页" });
  expect(revoked).toEqual([currentID]);
  expect(cacheAtLogin).toEqual([0, 0]);
});

it("撤销失败保留页面并显示服务器错误", async () => {
  const { router } = render({ revokeSession: async () => { throw new ConnectError("revoking session failed", Code.Internal); } });
  fireEvent.click(await screen.findByRole("button", { name: `撤销会话 ${currentID.slice(0, 12)}` }));
  fireEvent.click(screen.getByRole("button", { name: `确认撤销会话 ${currentID.slice(0, 12)}` }));
  expect(await screen.findByRole("alert")).toHaveTextContent("revoking session failed");
  expect(router.state.location.pathname).toBe("/security");
});
