import { expect, it, vi } from "vitest";
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { createConnectQueryKey } from "@connectrpc/connect-query";
import { ConnectError, Code } from "@connectrpc/connect";
import { AdminService, AlertKind, ChannelKind, ListAlertRulesResponseSchema, ListNodesResponseSchema, ListNotifyChannelsResponseSchema, ListProbeTasksResponseSchema, ProbeMetric, type SaveAlertRuleRequest } from "../gen/probe/v1/admin_pb";
import { ProbeKind } from "../gen/probe/v1/types_pb";
import { renderWithAdmin, type AdminImpl } from "../test/harness";
import { AlertRules } from "./AlertRules";

const nodes = create(ListNodesResponseSchema, { nodes: [{ id: 1n, name: "东京" }, { id: 2n, name: "法兰克福" }] });
const channels = create(ListNotifyChannelsResponseSchema, { channels: [{ id: 5n, name: "hook", kind: ChannelKind.WEBHOOK }] });
const tasks = create(ListProbeTasksResponseSchema, { tasks: [{ task: { id: 3n, kind: ProbeKind.TCP, target: "1.1.1.1:443" }, nodeIds: [1n, 2n] }] });
const rules = create(ListAlertRulesResponseSchema, {
  rules: [
    { id: 7n, name: "离线", kind: AlertKind.OFFLINE, enabled: true, allNodes: true, channelIds: [5n] },
    { id: 8n, name: "丢包", kind: AlertKind.PROBE, enabled: true, nodeIds: [1n, 9n], taskId: 3n, metric: ProbeMetric.LOSS_PCT, threshold: 50, forMinutes: 3 },
    { id: 9n, name: "停用", kind: AlertKind.OFFLINE, enabled: false, allNodes: true },
  ],
  states: [
    { ruleId: 7n, nodeId: 1n, state: "firing" },
    { ruleId: 7n, nodeId: 2n, state: "pending" },
  ],
});
const routes = [{ path: "/alerts", Component: AlertRules }];
const base: AdminImpl = { listNodes: async () => nodes, listNotifyChannels: async () => channels, listProbeTasks: async () => tasks, listAlertRules: async () => rules };
const render = (impl: AdminImpl) => renderWithAdmin({ ...base, ...impl }, routes, "/alerts");

it("轮询成功更新状态，随后刷新失败仍保留同一编辑表单与草稿", async () => {
  let calls = 0;
  vi.useFakeTimers();
  try {
    render({ listAlertRules: async () => {
      calls++;
      if (calls > 2) throw new ConnectError("rules refresh failed", Code.Unavailable);
      return calls === 1 ? rules : create(ListAlertRulesResponseSchema, {
        rules: rules.rules, states: [{ ruleId: 7n, nodeId: 2n, state: "firing" }],
      });
    } });
    await act(async () => { await vi.advanceTimersByTimeAsync(100); });
    fireEvent.click(screen.getByRole("button", { name: "编辑 丢包" }));
    const form = screen.getByRole("form", { name: "编辑 丢包" });
    fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "尚未保存" } });
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(calls).toBe(2);
    expect(screen.getByRole("cell", { name: "触发：法兰克福" })).toBeInTheDocument();
    expect(screen.getByRole("form", { name: "编辑 丢包" })).toBe(form);
    expect(within(form).getByLabelText("名称")).toHaveValue("尚未保存");
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(calls).toBe(3);
    expect(screen.getByRole("alert")).toHaveTextContent("rules refresh failed");
    expect(screen.getByRole("form", { name: "编辑 丢包" })).toBe(form);
    expect(within(form).getByLabelText("名称")).toHaveValue("尚未保存");
  } finally { vi.useRealTimers(); }
});

it("规则首次失败无数据时只显示错误而无表单与表格", async () => {
  render({ listAlertRules: async () => { throw new ConnectError("rules unavailable", Code.Unavailable); } });
  expect(await screen.findByRole("alert")).toHaveTextContent("rules unavailable");
  expect(screen.queryByRole("form")).toBeNull();
  expect(screen.queryByRole("table")).toBeNull();
});
const withRule8Name = (name: string) => create(ListAlertRulesResponseSchema, {
  rules: [
    { id: 7n, name: "离线", kind: AlertKind.OFFLINE, enabled: true, allNodes: true, channelIds: [5n] },
    { id: 8n, name, kind: AlertKind.PROBE, enabled: true, nodeIds: [1n, 9n], taskId: 3n, metric: ProbeMetric.LOSS_PCT, threshold: 50, forMinutes: 3 },
    { id: 9n, name: "停用", kind: AlertKind.OFFLINE, enabled: false, allNodes: true },
  ],
  states: [
    { ruleId: 7n, nodeId: 1n, state: "firing" },
    { ruleId: 7n, nodeId: 2n, state: "pending" },
  ],
});

it("列表展示名称、条件、作用域、通知与当前状态", async () => {
  render({});
  const rowOf = (cell: HTMLElement) => within(cell.closest("tr")!);
  const offline = rowOf(await screen.findByRole("cell", { name: "hook" }));
  expect(offline.getByRole("cell", { name: "全部节点" })).toBeInTheDocument();
  expect(offline.getByRole("cell", { name: "超过宽限期未上报" })).toBeInTheDocument();
  const state = offline.getByRole("cell", { name: /触发：东京/ });
  expect(state).toHaveTextContent("触发：东京");
  expect(state).toHaveTextContent("待定：法兰克福");
  const probe = rowOf(screen.getByRole("cell", { name: "TCP 1.1.1.1:443 丢包率 ≥ 50%，连续 3 分钟" }));
  expect(probe.getByRole("cell", { name: "东京、节点 #9" })).toBeInTheDocument();
  expect(probe.getByRole("cell", { name: "只记事件" })).toBeInTheDocument();
  expect(probe.getByRole("cell", { name: "正常" })).toBeInTheDocument();
  expect(screen.getByRole("cell", { name: "已停用" })).toBeInTheDocument();
});

it("新建离线规则覆盖全部节点时不带节点列表", async () => {
  const saved: SaveAlertRuleRequest[] = [];
  render({ saveAlertRule: async (req) => { saved.push(req); return {}; } });
  const form = await screen.findByRole("form", { name: "新建告警规则" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "全网离线" } });
  fireEvent.click(within(form).getByLabelText("hook"));
  fireEvent.click(within(form).getByLabelText("全部节点（含以后新建的节点）"));
  fireEvent.click(within(form).getByLabelText("东京"));
  fireEvent.click(within(form).getByLabelText("全部节点（含以后新建的节点）"));
  fireEvent.click(within(form).getByRole("button", { name: "创建" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  const r = saved[0].rule!;
  expect({ id: r.id, name: r.name, kind: r.kind, enabled: r.enabled, allNodes: r.allNodes, nodeIds: r.nodeIds, channelIds: r.channelIds, taskId: r.taskId, metric: r.metric }).toEqual(
    { id: 0n, name: "全网离线", kind: AlertKind.OFFLINE, enabled: true, allNodes: true, nodeIds: [], channelIds: [5n], taskId: 0n, metric: ProbeMetric.UNSPECIFIED });
  await waitFor(() => expect(within(screen.getByRole("form", { name: "新建告警规则" })).getByLabelText("名称")).toHaveValue(""));
});

it("显式作用域按升序发出节点列表", async () => {
  const saved: SaveAlertRuleRequest[] = [];
  render({ saveAlertRule: async (req) => { saved.push(req); return {}; } });
  const form = await screen.findByRole("form", { name: "新建告警规则" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "部分节点" } });
  fireEvent.click(within(form).getByLabelText("全部节点（含以后新建的节点）"));
  const scope = within(form).getByRole("group", { name: "作用域节点" });
  fireEvent.click(within(scope).getByLabelText("法兰克福"));
  fireEvent.click(within(scope).getByLabelText("东京"));
  fireEvent.click(within(form).getByRole("button", { name: "创建" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  expect(saved[0].rule!.allNodes).toBe(false);
  expect(saved[0].rule!.nodeIds).toEqual([1n, 2n]);
});

it("探测规则字段随指标切换单位", async () => {
  const saved: SaveAlertRuleRequest[] = [];
  render({ saveAlertRule: async (req) => { saved.push(req); return {}; } });
  const form = await screen.findByRole("form", { name: "新建告警规则" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "高延迟" } });
  fireEvent.change(within(form).getByLabelText("类型"), { target: { value: String(AlertKind.PROBE) } });
  fireEvent.change(within(form).getByLabelText("探测任务"), { target: { value: "3" } });
  fireEvent.change(within(form).getByLabelText("指标"), { target: { value: String(ProbeMetric.RTT_MS) } });
  fireEvent.change(within(form).getByLabelText("阈值（ms）"), { target: { value: "150" } });
  fireEvent.change(within(form).getByLabelText("连续分钟"), { target: { value: "5" } });
  fireEvent.click(within(form).getByRole("button", { name: "创建" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  const r = saved[0].rule!;
  expect({ kind: r.kind, taskId: r.taskId, metric: r.metric, threshold: r.threshold, forMinutes: r.forMinutes }).toEqual(
    { kind: AlertKind.PROBE, taskId: 3n, metric: ProbeMetric.RTT_MS, threshold: 150, forMinutes: 5 });
});

it("编辑回填并去掉已删除节点", async () => {
  const saved: SaveAlertRuleRequest[] = [];
  render({ saveAlertRule: async (req) => { saved.push(req); return {}; } });
  fireEvent.click(await screen.findByRole("button", { name: "编辑 丢包" }));
  const form = screen.getByRole("form", { name: "编辑 丢包" });
  expect(within(form).getByLabelText("探测任务")).toHaveValue("3");
  expect(within(form).getByLabelText("阈值（%）")).toHaveValue(50);
  expect(within(form).getByLabelText("全部节点（含以后新建的节点）")).not.toBeChecked();
  expect(within(form).getByLabelText("东京")).toBeChecked();
  expect(within(form).getByLabelText("法兰克福")).not.toBeChecked();
  fireEvent.click(within(form).getByRole("button", { name: "保存" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  expect(saved[0].rule!.id).toBe(8n);
  expect(saved[0].rule!.nodeIds).toEqual([1n]);
});

it("空显式作用域被 hub 拒绝时显示原文", async () => {
  render({ saveAlertRule: async () => { throw new ConnectError("rule.node_ids must not be empty unless all_nodes is true", Code.InvalidArgument); } });
  const form = await screen.findByRole("form", { name: "新建告警规则" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "空作用域" } });
  fireEvent.click(within(form).getByLabelText("全部节点（含以后新建的节点）"));
  fireEvent.click(within(form).getByRole("button", { name: "创建" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("rule.node_ids must not be empty unless all_nodes is true");
});

it("删除两段式确认且事件记录保留", async () => {
  const removed: bigint[] = [];
  render({ deleteAlertRule: async (req) => { removed.push(req.id); return {}; } });
  fireEvent.click(await screen.findByRole("button", { name: "删除 离线" }));
  expect(removed).toEqual([]);
  expect(screen.getByRole("button", { name: "确认删除 离线" })).toBeInTheDocument();
  expect(screen.getByText("事件记录保留")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "确认删除 离线" }));
  await waitFor(() => expect(removed).toEqual([7n]));
});

it("没有渠道时提示只记事件", async () => {
  render({ listNotifyChannels: async () => create(ListNotifyChannelsResponseSchema, {}) });
  const form = await screen.findByRole("form", { name: "新建告警规则" });
  expect(within(form).getByText("还没有通知渠道；规则只记录事件，不发送通知。")).toBeInTheDocument();
  expect(within(form).queryByRole("group", { name: "通知渠道" })).toBeNull();
});

it("依赖列表未到达时不渲染表单与规则表格", async () => {
  const { queryClient } = render({ listProbeTasks: () => new Promise(() => {}) });
  const key = createConnectQueryKey({ schema: AdminService.method.listAlertRules, cardinality: "finite" });
  await waitFor(() => expect(queryClient.getQueriesData({ queryKey: key })[0]?.[1]).toEqual(rules));
  expect({
    loading: screen.queryByText("加载中…") !== null,
    forms: screen.queryAllByRole("form").length,
    rows: screen.queryAllByRole("row").length,
  }).toEqual({ loading: true, forms: 0, rows: 0 });
});

it("删除首击不发请求", async () => {
  const removed: bigint[] = [];
  render({ deleteAlertRule: async (req) => { removed.push(req.id); throw new ConnectError("ref", Code.FailedPrecondition); } });
  fireEvent.click(await screen.findByRole("button", { name: "删除 离线" }));
  fireEvent.click(screen.getByRole("button", { name: "取消" }));
  fireEvent.click(screen.getByRole("button", { name: "删除 丢包" }));
  fireEvent.click(screen.getByRole("button", { name: "确认删除 丢包" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("ref");
  expect(removed).toEqual([8n]);
});

it("编辑往返撤销已武装的删除确认", async () => {
  render({});
  fireEvent.click(await screen.findByRole("button", { name: "删除 离线" }));
  expect(screen.getByRole("button", { name: "确认删除 离线" })).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "编辑 离线" }));
  fireEvent.click(screen.getByRole("button", { name: "取消" }));
  expect(screen.getByRole("button", { name: "删除 离线" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "确认删除 离线" })).toBeNull();
});

it("一行保存挂起时其它行的保存禁用", async () => {
  let releaseSave!: () => void;
  const saveGate = new Promise<void>((r) => { releaseSave = r; });
  render({ saveAlertRule: async () => { await saveGate; return {}; } });
  fireEvent.click(await screen.findByRole("button", { name: "编辑 丢包" }));
  fireEvent.click(screen.getByRole("button", { name: "编辑 离线" }));
  const probeForm = screen.getByRole("form", { name: "编辑 丢包" });
  const offlineForm = screen.getByRole("form", { name: "编辑 离线" });
  fireEvent.click(within(probeForm).getByRole("button", { name: "保存" }));
  try {
    await waitFor(() => expect(within(offlineForm).getByRole("button", { name: "保存" })).toBeDisabled());
  } finally { await act(async () => { releaseSave(); }); }
});

it("编辑态在刷新完成后才关闭", async () => {
  let releaseList!: () => void;
  const listGate = new Promise<void>((r) => { releaseList = r; });
  let listCalls = 0;
  let current = rules;
  render({ listAlertRules: async () => { listCalls++; if (listCalls > 1) await listGate; return current; },
    saveAlertRule: async () => { current = withRule8Name("丢包2"); return {}; } });
  fireEvent.click(await screen.findByRole("button", { name: "编辑 丢包" }));
  const form = screen.getByRole("form", { name: "编辑 丢包" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "丢包2" } });
  fireEvent.click(within(form).getByRole("button", { name: "保存" }));
  try {
    await waitFor(() => expect(listCalls).toBe(2));
    vi.useFakeTimers();
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(form).toBeInTheDocument();
  } finally { vi.useRealTimers(); await act(async () => { releaseList(); }); }
  await waitFor(() => expect(screen.queryByRole("form", { name: "编辑 丢包" })).toBeNull());
  expect(screen.getByRole("cell", { name: "丢包2" })).toBeInTheDocument();
});

it("创建失败保留草稿", async () => {
  render({ saveAlertRule: async () => { throw new ConnectError("rule.name: must not be empty", Code.InvalidArgument); } });
  const form = await screen.findByRole("form", { name: "新建告警规则" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "全网离线" } });
  fireEvent.click(within(form).getByRole("button", { name: "创建" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("rule.name: must not be empty");
  expect(within(screen.getByRole("form", { name: "新建告警规则" })).getByLabelText("名称")).toHaveValue("全网离线");
});
