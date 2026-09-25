import { expect, it } from "vitest";
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { createConnectQueryKey } from "@connectrpc/connect-query";
import { ConnectError, Code } from "@connectrpc/connect";
import { AdminService, AlertEventSchema, ChannelKind, ListNodesResponseSchema, ListNotifyChannelsResponseSchema, type ListAlertEventsRequest } from "../gen/probe/v1/admin_pb";
import { renderWithAdmin, type AdminImpl } from "../test/harness";
import { AlertEvents } from "./AlertEvents";

const nodes = create(ListNodesResponseSchema, { nodes: [{ id: 1n, name: "东京" }, { id: 2n, name: "法兰克福" }] });
const channels = create(ListNotifyChannelsResponseSchema, { channels: [{ id: 5n, name: "hook", kind: ChannelKind.WEBHOOK }] });
const event = (id: bigint, nodeId = 1n) => create(AlertEventSchema, { id, nodeId, ruleId: 7n, transition: "firing", at: 1_700_000_000n, summary: `事件 ${id}` });
const routes = [{ path: "/events", Component: AlertEvents }];
const render = (impl: AdminImpl, path = "/events") =>
  renderWithAdmin({ listNodes: async () => nodes, listNotifyChannels: async () => channels, ...impl }, routes, path);

it("一页满 100 条时可加载更早的事件，从本页最小 id 之前继续", async () => {
  const requests: ListAlertEventsRequest[] = [];
  render({ listAlertEvents: async (req) => {
    requests.push(req);
    if (req.beforeId === 0n) return { events: Array.from({ length: 100 }, (_, i) => event(200n - BigInt(i))) };
    return { events: [event(100n), event(99n)] };
  } });
  await screen.findByText("事件 200");
  fireEvent.click(screen.getByRole("button", { name: "加载更早的事件" }));
  await screen.findByText("事件 99");
  expect(requests.map((r) => r.beforeId)).toEqual([0n, 101n]);
  expect(requests[0].limit).toBe(100);
  expect(screen.queryByRole("button", { name: "加载更早的事件" })).toBeNull();
});

it("同名节点按 id 筛选第二个", async () => {
  const requests: ListAlertEventsRequest[] = [];
  render({
    listNodes: async () => create(ListNodesResponseSchema, { nodes: [{ id: 1n, name: "东京" }, { id: 11n, name: "东京" }] }),
    listAlertEvents: async (req) => { requests.push(req); return { events: [] }; },
  });
  const select = await screen.findByLabelText("节点");
  // 带 id 时只命中第二项；名称不含 id 时两项同名，getBy 必须报多个。
  const option = within(select).getByRole("option", { name: (n) => n === "东京（#11）" || n === "东京" });
  fireEvent.change(select, { target: { value: (option as HTMLOptionElement).value } });
  await waitFor(() => expect(requests.at(-1)?.nodeId).toBe(11n));
});

it("URL 里的节点筛选", async () => {
  const requests: ListAlertEventsRequest[] = [];
  const { router } = render({ listAlertEvents: async (req) => { requests.push(req); return { events: [] }; } }, "/events?node=2");
  await waitFor(() => expect(requests).toHaveLength(1));
  expect(requests[0].nodeId).toBe(2n);
  const select = screen.getByLabelText("节点");
  expect(select).toHaveValue("2");
  fireEvent.change(select, { target: { value: "0" } });
  await waitFor(() => expect(requests).toHaveLength(2));
  expect(requests[1].nodeId).toBe(0n);
  expect(router.state.location.search).toBe("");
});

it("无效的节点参数不发请求", async () => {
  let calls = 0;
  render({ listAlertEvents: async () => { calls++; return { events: [] }; } }, "/events?node=abc");
  await screen.findByText(/节点参数 abc 无效。|没有告警事件。/);
  expect(calls).toBe(0);
  expect(screen.getByRole("alert")).toHaveTextContent("节点参数 abc 无效。");
});

it("投递文案与渠道回退", async () => {
  render({ listAlertEvents: async () => ({ events: [
    create(AlertEventSchema, { id: 1n, nodeId: 1n, ruleId: 7n, transition: "firing", at: 1_700_000_000n, summary: "带投递",
      deliveries: [
        { channelId: 5n, ok: true, done: true, attempts: 1 },
        { channelId: 7n, ok: false, done: true, attempts: 0, lastError: "channel deleted" },
      ] }),
    create(AlertEventSchema, { id: 2n, nodeId: 1n, ruleId: 7n, transition: "recovered", at: 1_700_000_000n, summary: "无投递" }),
  ] }) });
  expect(await screen.findByText("hook：已送达")).toBeInTheDocument();
  expect(screen.getByText("渠道 #7：失败（0 次）channel deleted")).toBeInTheDocument();
  expect(screen.getByText("未配置渠道")).toBeInTheDocument();
});

it("变化标签与节点名回退", async () => {
  render({ listAlertEvents: async () => ({ events: [
    create(AlertEventSchema, { id: 1n, nodeId: 9n, ruleId: 7n, transition: "firing", at: 1_700_000_000n, summary: "触发的事件" }),
    create(AlertEventSchema, { id: 2n, nodeId: 1n, ruleId: 7n, transition: "recovered", at: 1_700_000_000n, summary: "恢复的事件" }),
  ] }) });
  expect(await screen.findByRole("cell", { name: "恢复" })).toBeInTheDocument();
  expect(screen.getByRole("cell", { name: "触发" })).toHaveClass("error");
  expect(screen.getByRole("cell", { name: "节点 #9" })).toBeInTheDocument();
});

it("空列表显示提示且没有加载更早按钮", async () => {
  render({ listAlertEvents: async () => ({ events: [] }) });
  expect(await screen.findByText("没有告警事件。")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "加载更早的事件" })).toBeNull();
});

it.each(["listNodes", "listAlertEvents", "listNotifyChannels"] as const)("%s 刷新失败保留已加载的事件行", async (method) => {
  let fail = false;
  const impls = {
    listNodes: async () => nodes,
    listNotifyChannels: async () => channels,
    listAlertEvents: async () => ({ events: [event(200n)] }),
  };
  const { queryClient } = render({ ...impls, [method]: async () => {
    if (fail) throw new ConnectError(`${method} refresh failed`, Code.Unavailable);
    return impls[method]();
  } });
  const row = (await screen.findByText("事件 200")).closest("tr")!;
  fail = true;
  await act(async () => { await queryClient.refetchQueries(); });
  expect(await screen.findByRole("alert")).toHaveTextContent(`${method} refresh failed`);
  expect(screen.getByText("事件 200").closest("tr")).toBe(row);
});

it("事件查询首次失败时区域内显示错误而外壳仍在", async () => {
  render({ listAlertEvents: async () => { throw new ConnectError("events unavailable", Code.Unavailable); } });
  expect(await screen.findByRole("alert")).toHaveTextContent("events unavailable");
  expect(screen.getByLabelText("节点")).toBeInTheDocument();
  expect(screen.queryByRole("table")).toBeNull();
});

it("节点列表首次失败显示整页错误", async () => {
  render({ listNodes: async () => { throw new ConnectError("nodes unavailable", Code.Unavailable); } });
  expect(await screen.findByRole("alert")).toHaveTextContent("nodes unavailable");
  expect(screen.queryByLabelText("节点")).toBeNull();
  expect(screen.queryByRole("table")).toBeNull();
});

it("切换筛选时挂起期间下拉保持挂载与焦点", async () => {
  let release!: () => void;
  const gate = new Promise<void>((r) => { release = r; });
  const requests: ListAlertEventsRequest[] = [];
  render({ listAlertEvents: async (req) => {
    requests.push(req);
    if (req.nodeId === 0n) return { events: [event(200n)] };
    await gate;
    return { events: [] };
  } });
  await screen.findByText("事件 200");
  const select = screen.getByLabelText("节点");
  select.focus();
  fireEvent.change(select, { target: { value: "2" } });
  try {
    await waitFor(() => expect(requests.some((r) => r.nodeId === 2n)).toBe(true));
    expect(screen.getByLabelText("节点")).toBe(select);
    expect(document.activeElement).toBe(select);
  } finally { await act(async () => { release(); }); }
});

it("筛到事件首次失败的节点后区域显示错误且能改回全部节点", async () => {
  render({ listAlertEvents: async (req) => {
    if (req.nodeId === 2n) throw new ConnectError("node 2 events unavailable", Code.Unavailable);
    return { events: [event(200n)] };
  } });
  await screen.findByText("事件 200");
  const select = screen.getByLabelText("节点");
  fireEvent.change(select, { target: { value: "2" } });
  expect(await screen.findByRole("alert")).toHaveTextContent("node 2 events unavailable");
  expect(screen.getByLabelText("节点")).toBe(select);
  expect(screen.queryByRole("table")).toBeNull();
  fireEvent.change(select, { target: { value: "0" } });
  expect(await screen.findByText("事件 200")).toBeInTheDocument();
});

it("同文的多个查询错误只显示一条", async () => {
  let fail = false;
  const { queryClient } = render({
    listNodes: async () => {
      if (fail) throw new ConnectError("hub unreachable", Code.Unavailable);
      return nodes;
    },
    listAlertEvents: async () => {
      if (fail) throw new ConnectError("hub unreachable", Code.Unavailable);
      return { events: [event(200n)] };
    },
  });
  await screen.findByText("事件 200");
  fail = true;
  await act(async () => { await queryClient.refetchQueries(); });
  expect((await screen.findAllByRole("alert")).map((a) => a.textContent)).toEqual(["hub unreachable"]);
  expect(screen.getByText("事件 200")).toBeInTheDocument();
});

it("事件首次失败与节点列表刷新失败同文时只显示一条", async () => {
  let failNodes = false;
  const { queryClient } = render({
    listNodes: async () => {
      if (failNodes) throw new ConnectError("hub unreachable", Code.Unavailable);
      return nodes;
    },
    listAlertEvents: async () => { throw new ConnectError("hub unreachable", Code.Unavailable); },
  });
  expect(await screen.findByRole("alert")).toHaveTextContent("hub unreachable");
  failNodes = true;
  await act(async () => { await queryClient.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listNodes, cardinality: "finite" }) }); });
  await waitFor(() => expect(screen.getAllByRole("alert")).toHaveLength(1));
  expect(screen.getByRole("alert")).toHaveTextContent("hub unreachable");
});

it("渠道首次失败时事件仍按编号显示且出现横幅", async () => {
  render({
    listNotifyChannels: async () => { throw new ConnectError("channels unavailable", Code.Unavailable); },
    listAlertEvents: async () => ({ events: [create(AlertEventSchema, { id: 1n, nodeId: 1n, ruleId: 7n, transition: "firing", at: 1_700_000_000n, summary: "带投递",
      deliveries: [{ channelId: 5n, ok: true, done: true, attempts: 1 }] })] }),
  });
  expect(await screen.findByRole("alert")).toHaveTextContent("channels unavailable");
  expect(screen.getByText("渠道 #5：已送达")).toBeInTheDocument();
  expect(screen.getByText("带投递")).toBeInTheDocument();
});

it("第二页在途时加载按钮禁用，空尾页不显示空提示", async () => {
  let release!: () => void;
  const gate = new Promise<void>((r) => { release = r; });
  const requests: ListAlertEventsRequest[] = [];
  render({ listAlertEvents: async (req) => {
    requests.push(req);
    if (req.beforeId === 0n) return { events: Array.from({ length: 100 }, (_, i) => event(200n - BigInt(i))) };
    await gate;
    return { events: [] };
  } });
  await screen.findByText("事件 200");
  fireEvent.click(screen.getByRole("button", { name: "加载更早的事件" }));
  try {
    await waitFor(() => expect(requests).toHaveLength(2));
    expect(screen.getByRole("button", { name: "加载更早的事件" })).toBeDisabled();
  } finally { await act(async () => { release(); }); }
  await waitFor(() => expect(screen.queryByRole("button", { name: "加载更早的事件" })).toBeNull());
  expect(screen.getByText("事件 101")).toBeInTheDocument();
  expect(screen.queryByText("没有告警事件。")).toBeNull();
});

it("筛选切换时挂起期间不残留旧节点的事件", async () => {
  let release!: () => void;
  const gate = new Promise<void>((r) => { release = r; });
  const requests: ListAlertEventsRequest[] = [];
  const { router } = render({ listAlertEvents: async (req) => {
    requests.push(req);
    if (req.nodeId === 0n) {
      if (req.beforeId === 0n) return { events: Array.from({ length: 100 }, (_, i) => event(300n - BigInt(i))) };
      return { events: [event(200n), event(199n)] };
    }
    await gate;
    return { events: [event(50n, 2n)] };
  } });
  await screen.findByText("事件 300");
  fireEvent.click(screen.getByRole("button", { name: "加载更早的事件" }));
  await screen.findByText("事件 199");
  fireEvent.change(screen.getByLabelText("节点"), { target: { value: "2" } });
  try {
    await waitFor(() => expect(requests).toHaveLength(3));
    expect(requests[2]).toMatchObject({ nodeId: 2n, beforeId: 0n });
    expect(router.state.location.search).toBe("?node=2");
    expect(screen.queryByText("事件 300")).toBeNull();
    expect(screen.queryByText("事件 199")).toBeNull();
  } finally { await act(async () => { release(); }); }
  expect(await screen.findByText("事件 50")).toBeInTheDocument();
  expect(screen.queryByText("事件 300")).toBeNull();
});
