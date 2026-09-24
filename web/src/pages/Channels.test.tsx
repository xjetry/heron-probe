import { expect, it } from "vitest";
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { ConnectError, Code } from "@connectrpc/connect";
import { ChannelKind, ListNotifyChannelsResponseSchema, type SaveNotifyChannelRequest } from "../gen/probe/v1/admin_pb";
import { renderWithAdmin, type AdminImpl } from "../test/harness";
import { Channels } from "./Channels";

const channels = create(ListNotifyChannelsResponseSchema, { channels: [
  { id: 1n, name: "tg", kind: ChannelKind.TELEGRAM, telegram: { chatId: "42", hasBotToken: true }, createdAt: 1_700_000_000n },
  { id: 2n, name: "hook", kind: ChannelKind.WEBHOOK, webhook: { method: "POST", hasUrl: true, urlHost: "https://hooks.example", headerNames: ["Authorization"], bodyTemplate: "{{.Summary}}" }, createdAt: 1_700_000_000n },
] });
const routes = [{ path: "/channels", Component: Channels }];
const render = (impl: AdminImpl) => renderWithAdmin({ listNotifyChannels: async () => channels, ...impl }, routes, "/channels");

it("列表只显示非凭据字段", async () => {
  render({});
  expect(await screen.findByRole("cell", { name: "会话 42" })).toBeInTheDocument();
  expect(screen.getByRole("cell", { name: "POST https://hooks.example，头 Authorization" })).toBeInTheDocument();
});

it("新建 Telegram 渠道只发 telegram 配置，成功后表单复位", async () => {
  const saved: SaveNotifyChannelRequest[] = [];
  render({ saveNotifyChannel: async (req) => { saved.push(req); return {}; } });
  const form = await screen.findByRole("form", { name: "新建通知渠道" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "值班群" } });
  fireEvent.change(within(form).getByLabelText("Bot token"), { target: { value: "123:abc" } });
  fireEvent.change(within(form).getByLabelText("Chat ID"), { target: { value: "-100" } });
  fireEvent.click(within(form).getByRole("button", { name: "创建" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  const c = saved[0].channel!;
  expect({ id: c.id, name: c.name, kind: c.kind, token: c.telegram?.botToken, chat: c.telegram?.chatId, webhook: c.webhook }).toEqual(
    { id: 0n, name: "值班群", kind: ChannelKind.TELEGRAM, token: "123:abc", chat: "-100", webhook: undefined });
  await waitFor(() => expect(within(screen.getByRole("form", { name: "新建通知渠道" })).getByLabelText("名称")).toHaveValue(""));
});

it("编辑同种类时凭据留空即保留", async () => {
  const saved: SaveNotifyChannelRequest[] = [];
  render({ saveNotifyChannel: async (req) => { saved.push(req); return {}; } });
  fireEvent.click(await screen.findByRole("button", { name: "编辑 hook" }));
  const form = screen.getByRole("form", { name: "编辑 hook" });
  const url = within(form).getByLabelText("URL");
  expect(url).toHaveAttribute("placeholder", "已保存 https://hooks.example，留空保持不变");
  expect(url).not.toBeRequired();
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "hook2" } });
  fireEvent.click(within(form).getByRole("button", { name: "保存" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  const w = saved[0].channel!.webhook!;
  expect({ url: w.url, headers: w.headers, removeHeaders: w.removeHeaders, method: w.method, bodyTemplate: w.bodyTemplate }).toEqual(
    { url: "", headers: {}, removeHeaders: [], method: "POST", bodyTemplate: "{{.Summary}}" });
});

it("删除已保存的头", async () => {
  const saved: SaveNotifyChannelRequest[] = [];
  render({ saveNotifyChannel: async (req) => { saved.push(req); return {}; } });
  fireEvent.click(await screen.findByRole("button", { name: "编辑 hook" }));
  const form = screen.getByRole("form", { name: "编辑 hook" });
  fireEvent.click(within(form).getByLabelText("删除 Authorization"));
  fireEvent.click(within(form).getByRole("button", { name: "保存" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  expect(saved[0].channel!.webhook!.removeHeaders).toEqual(["Authorization"]);
});

it("新增头与覆盖", async () => {
  const saved: SaveNotifyChannelRequest[] = [];
  render({ saveNotifyChannel: async (req) => { saved.push(req); return {}; } });
  fireEvent.click(await screen.findByRole("button", { name: "编辑 hook" }));
  const form = screen.getByRole("form", { name: "编辑 hook" });
  fireEvent.click(within(form).getByRole("button", { name: "添加请求头" }));
  fireEvent.change(within(form).getByLabelText("请求头名"), { target: { value: "X-Tag" } });
  fireEvent.change(within(form).getByLabelText("请求头值"), { target: { value: "v1" } });
  fireEvent.click(within(form).getByRole("button", { name: "保存" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  expect(saved[0].channel!.webhook!.headers).toEqual({ "X-Tag": "v1" });
});

it("同名头拒绝提交", async () => {
  let calls = 0;
  render({ saveNotifyChannel: async () => { calls++; return {}; } });
  const form = await screen.findByRole("form", { name: "新建通知渠道" });
  fireEvent.change(within(form).getByLabelText("类型"), { target: { value: String(ChannelKind.WEBHOOK) } });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "hook3" } });
  fireEvent.change(within(form).getByLabelText("URL"), { target: { value: "https://hook3.example" } });
  fireEvent.click(within(form).getByRole("button", { name: "添加请求头" }));
  fireEvent.click(within(form).getByRole("button", { name: "添加请求头" }));
  const names = within(form).getAllByLabelText("请求头名");
  fireEvent.change(names[0], { target: { value: "X-A" } });
  fireEvent.change(names[1], { target: { value: "x-a" } });
  fireEvent.click(within(form).getByRole("button", { name: "创建" }));
  await new Promise((r) => setTimeout(r, 20));
  expect(calls).toBe(0);
  expect(screen.getByRole("alert")).toHaveTextContent("请求头 x-a 填写了两次");
});

it("编辑时切换种类要求新凭据", async () => {
  render({});
  fireEvent.click(await screen.findByRole("button", { name: "编辑 tg" }));
  const form = screen.getByRole("form", { name: "编辑 tg" });
  fireEvent.change(within(form).getByLabelText("类型"), { target: { value: String(ChannelKind.WEBHOOK) } });
  expect(within(form).getByLabelText("URL")).toBeRequired();
  expect(within(form).queryByText("已保存的请求头（值不回显）")).toBeNull();
});

it("Telegram 编辑 token 可留空", async () => {
  render({});
  fireEvent.click(await screen.findByRole("button", { name: "编辑 tg" }));
  const form = screen.getByRole("form", { name: "编辑 tg" });
  const token = within(form).getByLabelText("Bot token");
  expect(token).toHaveValue("");
  expect(token).not.toBeRequired();
  expect(token).toHaveAttribute("placeholder", "已保存，留空保持不变");
});

it("发送测试成功与失败", async () => {
  const tested: bigint[] = [];
  let fail = false;
  render({ testNotifyChannel: async (req) => {
    tested.push(req.id);
    if (fail) throw new ConnectError("telegram: 401 Unauthorized", Code.FailedPrecondition);
    return {};
  } });
  expect((await screen.findByRole("status")).textContent).toBe("");
  fireEvent.click(await screen.findByRole("button", { name: "测试 tg" }));
  await waitFor(() => expect(screen.getByRole("status").textContent).toBe("已向 tg 发送测试消息"));
  fail = true;
  fireEvent.click(screen.getByRole("button", { name: "测试 tg" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("telegram: 401 Unauthorized");
  expect(screen.getByRole("status").textContent).toBe("");
  expect(tested).toEqual([1n, 1n]);
});

it("在途测试被新操作打断后不出现成功提示", async () => {
  let releaseTest!: () => void;
  const testGate = new Promise<void>((r) => { releaseTest = r; });
  render({
    testNotifyChannel: async () => { await testGate; return {}; },
    deleteNotifyChannel: async () => { throw new ConnectError("ref", Code.FailedPrecondition); },
  });
  fireEvent.click(await screen.findByRole("button", { name: "测试 tg" }));
  fireEvent.click(screen.getByRole("button", { name: "删除 tg" }));
  fireEvent.click(screen.getByRole("button", { name: "确认删除 tg" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("ref");
  await act(async () => { releaseTest(); });
  await waitFor(() => expect(screen.getByRole("button", { name: "测试 tg" })).toBeEnabled());
  expect(screen.getByRole("status").textContent).toBe("");
  expect(screen.getByRole("alert")).toHaveTextContent("ref");
});

it("删除被引用渠道显示服务端原文", async () => {
  render({ deleteNotifyChannel: async () => { throw new ConnectError("notify channel 1 is referenced by alert rules: 离线 (id 4)", Code.FailedPrecondition); } });
  fireEvent.click(await screen.findByRole("button", { name: "删除 tg" }));
  fireEvent.click(screen.getByRole("button", { name: "确认删除 tg" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("notify channel 1 is referenced by alert rules: 离线 (id 4)");
  expect(screen.getByRole("cell", { name: "tg" })).toBeInTheDocument();
});

it("表格可聚焦滚动", async () => {
  render({});
  const region = await screen.findByRole("region", { name: "通知渠道管理" });
  expect(region).toHaveAttribute("tabindex", "0");
  expect(within(region).getByRole("columnheader", { name: "操作" })).toBeInTheDocument();
});
