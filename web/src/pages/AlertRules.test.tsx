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
    fireEvent.click(screen.getByRole("button", { name: "编辑 丢包（#8）" }));
    const form = screen.getByRole("form", { name: "编辑 丢包（#8）" });
    fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "尚未保存" } });
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(calls).toBe(2);
    expect(screen.getByRole("cell", { name: "触发：法兰克福" })).toBeInTheDocument();
    expect(screen.getByRole("form", { name: "编辑 丢包（#8）" })).toBe(form);
    expect(within(form).getByLabelText("名称")).toHaveValue("尚未保存");
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(calls).toBe(3);
    expect(screen.getByRole("alert")).toHaveTextContent("rules refresh failed");
    expect(screen.getByRole("form", { name: "编辑 丢包（#8）" })).toBe(form);
    expect(within(form).getByLabelText("名称")).toHaveValue("尚未保存");
  } finally { vi.useRealTimers(); }
});

it("规则首次失败无数据时只显示错误而无表单与表格", async () => {
  render({ listAlertRules: async () => { throw new ConnectError("rules unavailable", Code.Unavailable); } });
  expect(await screen.findByRole("alert")).toHaveTextContent("rules unavailable");
  expect(screen.queryByRole("form")).toBeNull();
  expect(screen.queryByRole("table")).toBeNull();
});

it("依赖列表刷新失败显示横幅且编辑中的表单与草稿仍在", async () => {
  let fail = false;
  const { queryClient } = render({ listNodes: async () => {
    if (fail) throw new ConnectError("nodes refresh failed", Code.Unavailable);
    return nodes;
  } });
  fireEvent.click(await screen.findByRole("button", { name: "编辑 丢包（#8）" }));
  const form = screen.getByRole("form", { name: "编辑 丢包（#8）" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "尚未保存" } });
  fail = true;
  await act(async () => { await queryClient.refetchQueries(); });
  expect(await screen.findByRole("alert")).toHaveTextContent("nodes refresh failed");
  expect(within(screen.getByRole("form", { name: "编辑 丢包（#8）" })).getByLabelText("名称")).toHaveValue("尚未保存");
});

it("多个依赖同时刷新失败时错误全部可见，恢复其一即只移除其错误", async () => {
  const failing = new Set<string>();
  const { queryClient } = render({
    listNodes: async () => {
      if (failing.has("listNodes")) throw new ConnectError("nodes refresh failed", Code.Unavailable);
      return nodes;
    },
    listNotifyChannels: async () => {
      if (failing.has("listNotifyChannels")) throw new ConnectError("channels refresh failed", Code.Unavailable);
      return channels;
    },
    listProbeTasks: async () => {
      if (failing.has("listProbeTasks")) throw new ConnectError("tasks refresh failed", Code.Unavailable);
      return tasks;
    },
  });
  fireEvent.click(await screen.findByRole("button", { name: "编辑 丢包（#8）" }));
  const form = screen.getByRole("form", { name: "编辑 丢包（#8）" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "尚未保存" } });
  failing.add("listNodes");
  failing.add("listNotifyChannels");
  failing.add("listProbeTasks");
  await act(async () => { await queryClient.refetchQueries(); });
  expect((await screen.findAllByRole("alert")).map((a) => a.textContent)).toEqual(["nodes refresh failed", "channels refresh failed", "tasks refresh failed"]);
  expect(within(screen.getByRole("form", { name: "编辑 丢包（#8）" })).getByLabelText("名称")).toHaveValue("尚未保存");
  failing.delete("listNodes");
  await act(async () => { await queryClient.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listNodes, cardinality: "finite" }) }); });
  await waitFor(() => expect(screen.getAllByRole("alert").map((a) => a.textContent)).toEqual(["channels refresh failed", "tasks refresh failed"]));
});

it("显式空作用域不是全部节点，编辑保存仍由 hub 拒绝而不放宽", async () => {
  const saved: SaveAlertRuleRequest[] = [];
  const message = "rule.node_ids must not be empty unless all_nodes is true";
  render({
    listAlertRules: async () => create(ListAlertRulesResponseSchema, { rules: [
      { id: 1n, name: "显式空", kind: AlertKind.OFFLINE, enabled: true, allNodes: false },
      { id: 2n, name: "全部", kind: AlertKind.OFFLINE, enabled: true, allNodes: true },
    ] }),
    saveAlertRule: async (req) => { saved.push(req); throw new ConnectError(message, Code.InvalidArgument); },
  });
  const empty = within((await screen.findByRole("cell", { name: "显式空" })).closest("tr")!);
  const all = within(screen.getByRole("cell", { name: "全部" }).closest("tr")!);
  expect(empty.getByRole("cell", { name: "无节点" })).toBeInTheDocument();
  expect(empty.queryByRole("cell", { name: "全部节点" })).toBeNull();
  expect(all.getByRole("cell", { name: "全部节点" })).toBeInTheDocument();
  fireEvent.click(empty.getByRole("button", { name: "编辑 显式空（#1）" }));
  const form = screen.getByRole("form", { name: "编辑 显式空（#1）" });
  expect(within(form).getByLabelText("全部节点（含以后新建的节点）")).not.toBeChecked();
  fireEvent.click(within(form).getByRole("button", { name: "保存" }));
  expect(await screen.findByRole("alert")).toHaveTextContent(message);
  expect(saved).toHaveLength(1);
  expect({ allNodes: saved[0].rule!.allNodes, nodeIds: saved[0].rule!.nodeIds }).toEqual({ allNodes: false, nodeIds: [] });
  expect(screen.getByRole("form", { name: "编辑 显式空（#1）" })).toBe(form);
});

const rttRules = create(ListAlertRulesResponseSchema, {
  rules: [{ id: 10n, name: "延迟", kind: AlertKind.PROBE, enabled: true, allNodes: false, nodeIds: [1n],
    channelIds: [5n, 9n], taskId: 7n, metric: ProbeMetric.RTT_MS, threshold: 75, forMinutes: 4 }],
  states: [{ ruleId: 10n, nodeId: 1n, state: "firing" }],
});
const rttTasks = create(ListProbeTasksResponseSchema, { tasks: [{ task: { id: 7n, kind: ProbeKind.ICMP, target: "127.0.0.1" }, nodeIds: [1n] }] });

it("RTT 规则只改名称保留完整载荷，已有渠道保留且删除的渠道掉出", async () => {
  const saved: SaveAlertRuleRequest[] = [];
  render({ listAlertRules: async () => rttRules, listProbeTasks: async () => rttTasks,
    saveAlertRule: async (req) => { saved.push(req); return {}; },
  });
  fireEvent.click(await screen.findByRole("button", { name: "编辑 延迟（#10）" }));
  const form = screen.getByRole("form", { name: "编辑 延迟（#10）" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "延迟新名" } });
  fireEvent.click(within(form).getByRole("button", { name: "保存" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  expect(saved[0].rule).toEqual({ ...rttRules.rules[0], name: "延迟新名", channelIds: [5n] });
});

it.each([true, false])("从 enabled=%s 编辑开关，保存与刷新后状态一致", async (enabled) => {
  let current = create(ListAlertRulesResponseSchema, { rules: [{ ...rules.rules[0], enabled }], states: enabled ? rules.states : [] });
  const saved: SaveAlertRuleRequest[] = [];
  render({ listAlertRules: async () => current, saveAlertRule: async (req) => {
    saved.push(req);
    current = create(ListAlertRulesResponseSchema, { rules: [req.rule!] });
    return {};
  } });
  fireEvent.click(await screen.findByRole("button", { name: "编辑 离线（#7）" }));
  const form = screen.getByRole("form", { name: "编辑 离线（#7）" });
  const input = within(form).getByLabelText("启用");
  expect((input as HTMLInputElement).checked).toBe(enabled);
  fireEvent.click(input);
  fireEvent.click(within(form).getByRole("button", { name: "保存" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  expect(saved[0].rule!.enabled).toBe(!enabled);
  await screen.findByRole("button", { name: "编辑 离线（#7）" });
  const row = within(screen.getByRole("button", { name: "编辑 离线（#7）" }).closest("tr")!);
  expect(row.getByRole("cell", { name: enabled ? "已停用" : "正常" })).toBeInTheDocument();
  expect(row.queryByText(/触发：/)).toBeNull();
});

it.each(["指标", "离线", "探测"])("主动切换%s发送新身份，刷新后不沿用旧状态", async (change) => {
  const original = change === "探测" ? rules.rules[0] : rttRules.rules[0];
  let current = create(ListAlertRulesResponseSchema, { rules: [original], states: [{ ruleId: original.id, nodeId: 1n, state: "firing" }] });
  const saved: SaveAlertRuleRequest[] = [];
  render({ listAlertRules: async () => current, listProbeTasks: async () => rttTasks, saveAlertRule: async (req) => {
    saved.push(req);
    current = create(ListAlertRulesResponseSchema, { rules: [req.rule!] });
    return {};
  } });
  fireEvent.click(await screen.findByRole("button", { name: `编辑 ${original.name}（#${original.id}）` }));
  const form = screen.getByRole("form", { name: `编辑 ${original.name}（#${original.id}）` });
  if (change === "指标") {
    fireEvent.change(within(form).getByLabelText("指标"), { target: { value: ProbeMetric.LOSS_PCT } });
  } else {
    fireEvent.change(within(form).getByLabelText("类型"), { target: { value: change === "离线" ? AlertKind.OFFLINE : AlertKind.PROBE } });
    if (change === "探测") {
      fireEvent.change(within(form).getByLabelText("探测任务"), { target: { value: "7" } });
      fireEvent.change(within(form).getByLabelText("阈值（%）"), { target: { value: "25" } });
    }
  }
  fireEvent.click(within(form).getByRole("button", { name: "保存" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  const expected = change === "离线"
    ? { kind: AlertKind.OFFLINE, taskId: 0n, metric: ProbeMetric.UNSPECIFIED, threshold: 0, forMinutes: 0 }
    : { kind: AlertKind.PROBE, taskId: 7n, metric: ProbeMetric.LOSS_PCT, threshold: change === "探测" ? 25 : 75, forMinutes: change === "探测" ? 3 : 4 };
  expect(saved[0].rule).toEqual({ ...original, channelIds: [5n], ...expected });
  await screen.findByRole("button", { name: `编辑 ${original.name}（#${original.id}）` });
  expect(screen.getByRole("cell", { name: "正常" })).toBeInTheDocument();
  expect(screen.queryByText(/触发：/)).toBeNull();
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

// 离线规则的探测字段与提前天数、到期规则的探测字段都必须是零值：hub 拒绝带着别的种类字段的规则（alert.CheckRule）。
// 新建草稿里连续分钟默认 3、提前天数默认 7，所以这里这两项的断言不是空转；任务与阈值在新建草稿里是空串，
// 由"主动切换离线"那例从探测规则出发钉住。
it("新建离线规则覆盖全部节点时不带节点列表，别的种类的字段都是零值", async () => {
  const saved: SaveAlertRuleRequest[] = [];
  render({ saveAlertRule: async (req) => { saved.push(req); return {}; } });
  const form = await screen.findByRole("form", { name: "新建告警规则" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "全网离线" } });
  fireEvent.click(within(form).getByLabelText("hook（#5）"));
  fireEvent.click(within(form).getByLabelText("全部节点（含以后新建的节点）"));
  fireEvent.click(within(form).getByLabelText("东京（#1）"));
  fireEvent.click(within(form).getByLabelText("全部节点（含以后新建的节点）"));
  fireEvent.click(within(form).getByRole("button", { name: "创建" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  const r = saved[0].rule!;
  expect({ id: r.id, name: r.name, kind: r.kind, enabled: r.enabled, allNodes: r.allNodes, nodeIds: r.nodeIds, channelIds: r.channelIds,
    taskId: r.taskId, metric: r.metric, threshold: r.threshold, forMinutes: r.forMinutes, daysBefore: r.daysBefore }).toEqual(
    { id: 0n, name: "全网离线", kind: AlertKind.OFFLINE, enabled: true, allNodes: true, nodeIds: [], channelIds: [5n],
      taskId: 0n, metric: ProbeMetric.UNSPECIFIED, threshold: 0, forMinutes: 0, daysBefore: 0 });
  await waitFor(() => expect(within(screen.getByRole("form", { name: "新建告警规则" })).getByLabelText("名称")).toHaveValue(""));
});

it("显式作用域按升序发出节点列表", async () => {
  const saved: SaveAlertRuleRequest[] = [];
  render({ saveAlertRule: async (req) => { saved.push(req); return {}; } });
  const form = await screen.findByRole("form", { name: "新建告警规则" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "部分节点" } });
  fireEvent.click(within(form).getByLabelText("全部节点（含以后新建的节点）"));
  const scope = within(form).getByRole("group", { name: "作用域节点" });
  fireEvent.click(within(scope).getByLabelText("法兰克福（#2）"));
  fireEvent.click(within(scope).getByLabelText("东京（#1）"));
  fireEvent.click(within(form).getByRole("button", { name: "创建" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  expect(saved[0].rule!.allNodes).toBe(false);
  expect(saved[0].rule!.nodeIds).toEqual([1n, 2n]);
});

it("同类型同目标的探测任务按 id 保存第二个", async () => {
  const saved: SaveAlertRuleRequest[] = [];
  const duplicate = create(ListProbeTasksResponseSchema, { tasks: [
    { task: { id: 3n, kind: ProbeKind.ICMP, target: "1.1.1.1" }, nodeIds: [1n] },
    { task: { id: 11n, kind: ProbeKind.ICMP, target: "1.1.1.1" }, nodeIds: [2n] },
  ] });
  render({ listProbeTasks: async () => duplicate, saveAlertRule: async (req) => { saved.push(req); return {}; } });
  const form = await screen.findByRole("form", { name: "新建告警规则" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "同名任务" } });
  fireEvent.change(within(form).getByLabelText("类型"), { target: { value: String(AlertKind.PROBE) } });
  const select = within(form).getByLabelText("探测任务");
  // 带 id 时只命中第二项；名称不含 id 时两项同名，getBy 必须报多个。
  const option = within(select).getByRole("option", { name: (n) => n === "ICMP 1.1.1.1 #11" || n === "ICMP 1.1.1.1" });
  fireEvent.change(select, { target: { value: (option as HTMLOptionElement).value } });
  fireEvent.change(within(form).getByLabelText("阈值（%）"), { target: { value: "10" } });
  fireEvent.click(within(form).getByRole("button", { name: "创建" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  expect(saved[0].rule!.taskId).toBe(11n);
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
  fireEvent.click(await screen.findByRole("button", { name: "编辑 丢包（#8）" }));
  const form = screen.getByRole("form", { name: "编辑 丢包（#8）" });
  expect(within(form).getByLabelText("探测任务")).toHaveValue("3");
  expect(within(form).getByLabelText("阈值（%）")).toHaveValue(50);
  expect(within(form).getByLabelText("全部节点（含以后新建的节点）")).not.toBeChecked();
  expect(within(form).getByLabelText("东京（#1）")).toBeChecked();
  expect(within(form).getByLabelText("法兰克福（#2）")).not.toBeChecked();
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
  fireEvent.click(await screen.findByRole("button", { name: "删除 离线（#7）" }));
  expect(removed).toEqual([]);
  expect(screen.getByRole("button", { name: "确认删除 离线（#7）" })).toBeInTheDocument();
  expect(screen.getByText("事件记录保留")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "确认删除 离线（#7）" }));
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
  fireEvent.click(await screen.findByRole("button", { name: "删除 离线（#7）" }));
  fireEvent.click(screen.getByRole("button", { name: "取消删除 离线（#7）" }));
  fireEvent.click(screen.getByRole("button", { name: "删除 丢包（#8）" }));
  fireEvent.click(screen.getByRole("button", { name: "确认删除 丢包（#8）" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("ref");
  expect(removed).toEqual([8n]);
});

it("编辑往返撤销已武装的删除确认", async () => {
  render({});
  fireEvent.click(await screen.findByRole("button", { name: "删除 离线（#7）" }));
  expect(screen.getByRole("button", { name: "确认删除 离线（#7）" })).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "编辑 离线（#7）" }));
  fireEvent.click(screen.getByRole("button", { name: "取消" }));
  expect(screen.getByRole("button", { name: "删除 离线（#7）" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "确认删除 离线（#7）" })).toBeNull();
});

it("一行保存挂起时其它行的保存禁用", async () => {
  let releaseSave!: () => void;
  const saveGate = new Promise<void>((r) => { releaseSave = r; });
  render({ saveAlertRule: async () => { await saveGate; return {}; } });
  fireEvent.click(await screen.findByRole("button", { name: "编辑 丢包（#8）" }));
  fireEvent.click(screen.getByRole("button", { name: "编辑 离线（#7）" }));
  const probeForm = screen.getByRole("form", { name: "编辑 丢包（#8）" });
  const offlineForm = screen.getByRole("form", { name: "编辑 离线（#7）" });
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
  fireEvent.click(await screen.findByRole("button", { name: "编辑 丢包（#8）" }));
  const form = screen.getByRole("form", { name: "编辑 丢包（#8）" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "丢包2" } });
  fireEvent.click(within(form).getByRole("button", { name: "保存" }));
  try {
    await waitFor(() => expect(listCalls).toBe(2));
    vi.useFakeTimers();
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(form).toBeInTheDocument();
  } finally { vi.useRealTimers(); await act(async () => { releaseList(); }); }
  await waitFor(() => expect(screen.queryByRole("form", { name: "编辑 丢包（#8）" })).toBeNull());
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

const expiryRules = create(ListAlertRulesResponseSchema, {
  rules: [{ id: 12n, name: "续费", kind: AlertKind.EXPIRY, enabled: true, allNodes: true, channelIds: [5n], daysBefore: 14 }],
  states: [{ ruleId: 12n, nodeId: 2n, state: "firing" }],
});

it("新建到期规则只发提前天数，默认 7 天", async () => {
  const saved: SaveAlertRuleRequest[] = [];
  render({ saveAlertRule: async (req) => { saved.push(req); return {}; } });
  const form = await screen.findByRole("form", { name: "新建告警规则" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "续费提醒" } });
  fireEvent.change(within(form).getByLabelText("类型"), { target: { value: String(AlertKind.EXPIRY) } });
  expect(within(form).getByLabelText("提前天数")).toHaveValue(7);
  expect(within(form).queryByLabelText("探测任务")).toBeNull();
  fireEvent.change(within(form).getByLabelText("提前天数"), { target: { value: "30" } });
  fireEvent.click(within(form).getByRole("button", { name: "创建" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  const r = saved[0].rule!;
  expect({ kind: r.kind, daysBefore: r.daysBefore, taskId: r.taskId, metric: r.metric, threshold: r.threshold, forMinutes: r.forMinutes }).toEqual(
    { kind: AlertKind.EXPIRY, daysBefore: 30, taskId: 0n, metric: ProbeMetric.UNSPECIFIED, threshold: 0, forMinutes: 0 });
});

it("提前天数超出 1–365 时表单不提交", async () => {
  const saved: SaveAlertRuleRequest[] = [];
  render({ saveAlertRule: async (req) => { saved.push(req); return {}; } });
  const form = await screen.findByRole("form", { name: "新建告警规则" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "越界" } });
  fireEvent.change(within(form).getByLabelText("类型"), { target: { value: String(AlertKind.EXPIRY) } });
  for (const value of ["0", "366", ""]) {
    fireEvent.change(within(form).getByLabelText("提前天数"), { target: { value } });
    fireEvent.click(within(form).getByRole("button", { name: "创建" }));
  }
  await act(async () => {});
  expect(saved).toHaveLength(0);
});

it("列表写出到期规则的条件与状态，编辑时带回提前天数", async () => {
  const saved: SaveAlertRuleRequest[] = [];
  render({ listAlertRules: async () => expiryRules, saveAlertRule: async (req) => { saved.push(req); return {}; } });
  const row = within((await screen.findByRole("cell", { name: "到期日距今不超过 14 天（含已过期）" })).closest("tr")!);
  expect(row.getByRole("cell", { name: "到期" })).toBeInTheDocument();
  expect(row.getByRole("cell", { name: "触发：法兰克福" })).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "编辑 续费（#12）" }));
  const form = screen.getByRole("form", { name: "编辑 续费（#12）" });
  expect(within(form).getByLabelText("提前天数")).toHaveValue(14);
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "续费提醒" } });
  fireEvent.click(within(form).getByRole("button", { name: "保存" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  expect(saved[0].rule).toEqual({ ...expiryRules.rules[0], name: "续费提醒" });
});

it("到期规则改成离线时不再带提前天数", async () => {
  const saved: SaveAlertRuleRequest[] = [];
  render({ listAlertRules: async () => expiryRules, saveAlertRule: async (req) => { saved.push(req); return {}; } });
  fireEvent.click(await screen.findByRole("button", { name: "编辑 续费（#12）" }));
  const form = screen.getByRole("form", { name: "编辑 续费（#12）" });
  fireEvent.change(within(form).getByLabelText("类型"), { target: { value: String(AlertKind.OFFLINE) } });
  fireEvent.click(within(form).getByRole("button", { name: "保存" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  expect({ kind: saved[0].rule!.kind, daysBefore: saved[0].rule!.daysBefore }).toEqual({ kind: AlertKind.OFFLINE, daysBefore: 0 });
});

// 新建草稿的探测任务与阈值是空串，换算后本来就是零值；从带着任务与阈值的探测规则切过来，这两项的断言才不是空转。
it("探测规则改成到期时只带提前天数", async () => {
  const saved: SaveAlertRuleRequest[] = [];
  render({ listAlertRules: async () => rttRules, listProbeTasks: async () => rttTasks,
    saveAlertRule: async (req) => { saved.push(req); return {}; },
  });
  fireEvent.click(await screen.findByRole("button", { name: "编辑 延迟（#10）" }));
  const form = screen.getByRole("form", { name: "编辑 延迟（#10）" });
  fireEvent.change(within(form).getByLabelText("类型"), { target: { value: String(AlertKind.EXPIRY) } });
  fireEvent.click(within(form).getByRole("button", { name: "保存" }));
  await waitFor(() => expect(saved).toHaveLength(1));
  expect(saved[0].rule).toEqual({ ...rttRules.rules[0], channelIds: [5n], kind: AlertKind.EXPIRY,
    taskId: 0n, metric: ProbeMetric.UNSPECIFIED, threshold: 0, forMinutes: 0, daysBefore: 7 });
});
