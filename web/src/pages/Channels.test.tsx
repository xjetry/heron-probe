import { expect, it, vi } from "vitest";
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

it("渠道刷新失败保留同一编辑表单与草稿", async () => {
  let fail = false;
  const { queryClient } = render({ listNotifyChannels: async () => {
    if (fail) throw new ConnectError("channels refresh failed", Code.Unavailable);
    return channels;
  } });
  fireEvent.click(await screen.findByRole("button", { name: "编辑 tg" }));
  const form = screen.getByRole("form", { name: "编辑 tg" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "尚未保存" } });
  fail = true;
  await act(async () => { await queryClient.refetchQueries(); });
  expect(await screen.findByRole("alert")).toHaveTextContent("channels refresh failed");
  expect(screen.getByRole("form", { name: "编辑 tg" })).toBe(form);
  expect(within(form).getByLabelText("名称")).toHaveValue("尚未保存");
});

it("列表挂起时显示加载中而不是空提示", async () => {
  render({ listNotifyChannels: () => new Promise(() => {}) });
  expect(await screen.findByText("加载中…")).toBeInTheDocument();
  expect(screen.queryByText("还没有通知渠道。")).toBeNull();
});

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
  const value = within(form).getByLabelText("请求头值");
  expect(value).toHaveAttribute("autocomplete", "new-password");
  fireEvent.change(value, { target: { value: "v1" } });
  fireEvent.click(within(form).getByRole("button", { name: "保存" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  expect(saved[0].channel!.webhook!.headers).toEqual({ "X-Tag": "v1" });
});

it("同名头拒绝提交", async () => {
  const saved: SaveNotifyChannelRequest[] = [];
  render({ saveNotifyChannel: async (req) => { saved.push(req); return {}; } });
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
  expect(screen.getByRole("alert")).toHaveTextContent("请求头 x-a 填写了两次");
  // 修正重名后同一表单可提交；若同名请求漏出，它经由同一路径排在这个请求之前。
  fireEvent.change(names[1], { target: { value: "X-B" } });
  fireEvent.click(within(form).getByRole("button", { name: "创建" }));
  await waitFor(() => expect(saved.at(-1)?.channel?.webhook?.headers).toEqual({ "X-A": "", "X-B": "" }));
  expect(saved).toHaveLength(1);
});

it("编辑时 Webhook 换成 Telegram 要求新 token", async () => {
  render({});
  fireEvent.click(await screen.findByRole("button", { name: "编辑 hook" }));
  const form = screen.getByRole("form", { name: "编辑 hook" });
  fireEvent.change(within(form).getByLabelText("类型"), { target: { value: String(ChannelKind.TELEGRAM) } });
  expect(within(form).getByLabelText("Bot token")).toBeRequired();
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
  expect(token).toHaveAttribute("autocomplete", "new-password");
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
  fireEvent.click(await screen.findByRole("button", { name: "发送测试 tg" }));
  await waitFor(() => expect(screen.getByRole("status").textContent).toBe("已向 tg 发送测试消息"));
  fail = true;
  fireEvent.click(screen.getByRole("button", { name: "发送测试 tg" }));
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
  fireEvent.click(await screen.findByRole("button", { name: "发送测试 tg" }));
  fireEvent.click(screen.getByRole("button", { name: "删除 tg" }));
  fireEvent.click(screen.getByRole("button", { name: "确认删除 tg" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("ref");
  await act(async () => { releaseTest(); });
  await waitFor(() => expect(screen.getByRole("button", { name: "发送测试 tg" })).toBeEnabled());
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

it("编辑往返撤销已武装的删除确认", async () => {
  render({});
  fireEvent.click(await screen.findByRole("button", { name: "删除 tg" }));
  expect(screen.getByRole("button", { name: "确认删除 tg" })).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "编辑 tg" }));
  fireEvent.click(screen.getByRole("button", { name: "取消" }));
  expect(screen.getByRole("button", { name: "删除 tg" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "确认删除 tg" })).toBeNull();
});

it("mutation 配置不携带展示层方法", async () => {
  const { queryClient } = render({ testNotifyChannel: async () => ({}) });
  fireEvent.click(await screen.findByRole("button", { name: "发送测试 tg" }));
  await waitFor(() => expect(screen.getByRole("status").textContent).toBe("已向 tg 发送测试消息"));
  const mutations = queryClient.getMutationCache().getAll();
  expect(mutations.length).toBeGreaterThan(0);
  for (const m of mutations) expect(m.options).not.toHaveProperty("isLatest");
});

it("表格可聚焦滚动", async () => {
  render({});
  const region = await screen.findByRole("region", { name: "通知渠道管理" });
  expect(region).toHaveAttribute("tabindex", "0");
  expect(within(region).getByRole("columnheader", { name: "操作" })).toBeInTheDocument();
});

const mk = (name: string) => create(ListNotifyChannelsResponseSchema, { channels: [
  { id: 1n, name: "tg", kind: ChannelKind.TELEGRAM, telegram: { chatId: "42", hasBotToken: true }, createdAt: 1_700_000_000n },
  { id: 2n, name, kind: ChannelKind.WEBHOOK, webhook: { method: "POST", hasUrl: true, urlHost: "https://hooks.example", headerNames: ["Authorization"], bodyTemplate: "{{.Summary}}" }, createdAt: 1_700_000_000n },
] });

it("删除首击不发请求", async () => {
  const removed: bigint[] = [];
  render({ deleteNotifyChannel: async (req) => { removed.push(req.id); throw new ConnectError("ref", Code.FailedPrecondition); } });
  fireEvent.click(await screen.findByRole("button", { name: "删除 tg" }));
  fireEvent.click(screen.getByRole("button", { name: "取消" }));
  fireEvent.click(screen.getByRole("button", { name: "删除 hook" }));
  fireEvent.click(screen.getByRole("button", { name: "确认删除 hook" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("ref");
  expect(removed).toEqual([2n]);
});

it("新操作开始时清除测试提示", async () => {
  render({
    testNotifyChannel: async () => ({}),
    deleteNotifyChannel: async () => { throw new ConnectError("ref", Code.FailedPrecondition); },
  });
  fireEvent.click(await screen.findByRole("button", { name: "发送测试 tg" }));
  await waitFor(() => expect(screen.getByRole("status").textContent).toBe("已向 tg 发送测试消息"));
  fireEvent.click(screen.getByRole("button", { name: "删除 tg" }));
  fireEvent.click(screen.getByRole("button", { name: "确认删除 tg" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("ref");
  expect(screen.getByRole("status").textContent).toBe("");
});

it("编辑态在刷新完成后才关闭", async () => {
  let releaseList!: () => void;
  const listGate = new Promise<void>((r) => { releaseList = r; });
  let listCalls = 0;
  let current = mk("hook");
  render({ listNotifyChannels: async () => { listCalls++; if (listCalls > 1) await listGate; return current; },
    saveNotifyChannel: async () => { current = mk("hook2"); return {}; } });
  fireEvent.click(await screen.findByRole("button", { name: "编辑 hook" }));
  const form = screen.getByRole("form", { name: "编辑 hook" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "hook2" } });
  fireEvent.click(within(form).getByRole("button", { name: "保存" }));
  try {
    await waitFor(() => expect(listCalls).toBe(2));
    vi.useFakeTimers();
    await act(async () => { await vi.runAllTimersAsync(); });
    expect(form).toBeInTheDocument();
  } finally { vi.useRealTimers(); await act(async () => { releaseList(); }); }
  await waitFor(() => expect(screen.queryByRole("form", { name: "编辑 hook" })).toBeNull());
  expect(screen.getByRole("cell", { name: "hook2" })).toBeInTheDocument();
});

it("一行保存挂起时其它行的保存禁用", async () => {
  let releaseSave!: () => void;
  const saveGate = new Promise<void>((r) => { releaseSave = r; });
  render({ saveNotifyChannel: async () => { await saveGate; return {}; } });
  fireEvent.click(await screen.findByRole("button", { name: "编辑 hook" }));
  fireEvent.click(screen.getByRole("button", { name: "编辑 tg" }));
  const hookForm = screen.getByRole("form", { name: "编辑 hook" });
  const tgForm = screen.getByRole("form", { name: "编辑 tg" });
  fireEvent.click(within(hookForm).getByRole("button", { name: "保存" }));
  try {
    await waitFor(() => expect(within(tgForm).getByRole("button", { name: "保存" })).toBeDisabled());
  } finally { await act(async () => { releaseSave(); }); }
});

it("创建失败保留草稿", async () => {
  render({ saveNotifyChannel: async () => { throw new ConnectError("channel.name: must not be empty", Code.InvalidArgument); } });
  const form = await screen.findByRole("form", { name: "新建通知渠道" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "值班群" } });
  fireEvent.change(within(form).getByLabelText("Bot token"), { target: { value: "123:abc" } });
  fireEvent.change(within(form).getByLabelText("Chat ID"), { target: { value: "-100" } });
  fireEvent.click(within(form).getByRole("button", { name: "创建" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("channel.name: must not be empty");
  expect(within(screen.getByRole("form", { name: "新建通知渠道" })).getByLabelText("名称")).toHaveValue("值班群");
});

it("请求头行带序号分组与序号化移除名", async () => {
  render({});
  const form = await screen.findByRole("form", { name: "新建通知渠道" });
  fireEvent.change(within(form).getByLabelText("类型"), { target: { value: String(ChannelKind.WEBHOOK) } });
  fireEvent.click(within(form).getByRole("button", { name: "添加请求头" }));
  fireEvent.click(within(form).getByRole("button", { name: "添加请求头" }));
  expect(within(form).getByRole("group", { name: "请求头 1" })).toBeInTheDocument();
  expect(within(form).getByRole("group", { name: "请求头 2" })).toBeInTheDocument();
  expect(within(form).getByRole("button", { name: "移除请求头 2" })).toBeInTheDocument();
  fireEvent.click(within(form).getByRole("button", { name: "移除请求头 1" }));
  expect(within(form).getByRole("group", { name: "请求头 1" })).toBeInTheDocument();
  expect(within(form).queryByRole("group", { name: "请求头 2" })).toBeNull();
});

it("移除中间请求头行不搬动其余行的输入节点", async () => {
  render({});
  const form = await screen.findByRole("form", { name: "新建通知渠道" });
  fireEvent.change(within(form).getByLabelText("类型"), { target: { value: String(ChannelKind.WEBHOOK) } });
  const add = () => fireEvent.click(within(form).getByRole("button", { name: "添加请求头" }));
  add();
  add();
  add();
  const values = within(form).getAllByLabelText("请求头值");
  fireEvent.change(values[0], { target: { value: "v1" } });
  fireEvent.change(values[2], { target: { value: "v3" } });
  const third = within(form).getAllByLabelText("请求头值")[2];
  fireEvent.click(within(form).getByRole("button", { name: "移除请求头 2" }));
  const rest = within(form).getAllByLabelText("请求头值");
  expect(rest.map((el) => (el as HTMLInputElement).value)).toEqual(["v1", "v3"]);
  expect(rest[1]).toBe(third);
});
