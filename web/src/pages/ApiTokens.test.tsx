import { afterEach, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { ListApiTokensResponseSchema } from "../gen/probe/v1/admin_pb";
import { renderWithAdmin, type AdminImpl } from "../test/harness";
import { ApiTokens } from "./ApiTokens";

const tokens = create(ListApiTokensResponseSchema, { tokens: [
  { id: 1n, name: "ci", createdAt: 1_700_000_000n, lastUsedAt: 1_700_000_600n },
  { id: 2n, name: "laptop", createdAt: 1_700_000_000n },
] });
const routes = [{ path: "/tokens", Component: ApiTokens }];
const render = (impl: AdminImpl) => renderWithAdmin({ listApiTokens: async () => tokens, ...impl }, routes, "/tokens");

afterEach(() => { vi.restoreAllMocks(); });

it("列表区分用过与从未使用", async () => {
  render({});
  const laptop = (await screen.findByRole("cell", { name: "laptop" })).closest("tr")!;
  expect(within(laptop).getByText("从未使用")).toBeInTheDocument();
  const ci = screen.getByRole("cell", { name: "ci" }).closest("tr")!;
  expect(within(ci).queryByText("从未使用")).toBeNull();
});

it("创建后只显示一次明文并刷新列表", async () => {
  let lists = 0;
  const created: string[] = [];
  render({
    listApiTokens: async () => { lists++; return tokens; },
    createApiToken: async (req) => { created.push(req.name); return { apiToken: { id: 3n, name: req.name, createdAt: 1n }, token: "probe_at_abc" }; },
  });
  const form = await screen.findByRole("form", { name: "新建 API token" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "agent" } });
  fireEvent.click(within(form).getByRole("button", { name: "创建" }));
  expect(await screen.findByLabelText("API token agent")).toHaveTextContent("probe_at_abc");
  expect(created).toEqual(["agent"]);
  await waitFor(() => expect(lists).toBe(2));
  expect(within(form).getByLabelText("名称")).toHaveValue("");
});

it("创建失败显示 hub 的错误原文", async () => {
  render({ createApiToken: async () => { throw new ConnectError("at most 100 API tokens may exist; delete an unused one first", Code.ResourceExhausted); } });
  const form = await screen.findByRole("form", { name: "新建 API token" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "x" } });
  fireEvent.click(within(form).getByRole("button", { name: "创建" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("at most 100 API tokens");
});

it("吊销需要确认，确认后按 id 删除", async () => {
  const deleted: bigint[] = [];
  render({ deleteApiToken: async (req) => { deleted.push(req.id); return {}; } });
  fireEvent.click(await screen.findByRole("button", { name: "吊销 laptop" }));
  expect(deleted).toEqual([]);
  fireEvent.click(screen.getByRole("button", { name: "确认吊销 laptop" }));
  await waitFor(() => expect(deleted).toEqual([2n]));
});

it("下载的入口卡片就是 hub 下发的 guide", async () => {
  const blobs: Blob[] = [];
  vi.spyOn(URL, "createObjectURL").mockImplementation((b) => { blobs.push(b as Blob); return "blob:card"; });
  vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
  const clicked: HTMLAnchorElement[] = [];
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) { clicked.push(this); });
  render({ getApiReference: async () => ({ guide: "---\nname: probe-hub\n---\n卡片", files: [] }) });
  fireEvent.click(await screen.findByRole("button", { name: "下载入口卡片" }));
  await waitFor(() => expect(clicked).toHaveLength(1));
  expect(clicked[0].download).toBe("SKILL.md");
  // undici 的 Response 不认 jsdom 的 Blob，读出来是 "[object Blob]"；这个 jsdom 实现了 Blob.text。
  expect(await blobs[0].text()).toBe("---\nname: probe-hub\n---\n卡片");
});

it("列表挂起时显示加载中", async () => {
  render({ listApiTokens: () => new Promise(() => {}) });
  expect(await screen.findByText("加载中…")).toBeInTheDocument();
});
