import { expect, it } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { renderWithAdmin } from "../test/harness";
import { AlertKind, ResourceMetric, type SaveAlertRuleRequest } from "../gen/heron/v1/admin_pb";
import { AlertRules } from "./AlertRules";

it("资源规则提交资源指标与滞回阈值，不携带探测专用字段", async () => {
  const saved: SaveAlertRuleRequest[] = [];
  renderWithAdmin({
    listNodes: async () => ({ nodes: [{ id: 1n, name: "n", tags: ["db"] }] }),
    listTags: async () => ({ tags: [{ name: "db" }] }),
    listNotifyChannels: async () => ({}), listProbeTasks: async () => ({}), listAlertRules: async () => ({}),
    saveAlertRule: async (req) => { saved.push(req); return {}; },
  }, [{ path: "/alerts", Component: AlertRules }], "/alerts");
  const form = await screen.findByRole("form", { name: "新建告警规则" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "磁盘压力" } });
  fireEvent.change(within(form).getByLabelText("类型"), { target: { value: String(AlertKind.RESOURCE) } });
  fireEvent.change(within(form).getByLabelText("资源指标"), { target: { value: String(ResourceMetric.DISK_USED_PCT) } });
  fireEvent.change(within(form).getByLabelText("触发阈值（%）"), { target: { value: "92" } });
  fireEvent.change(within(form).getByLabelText("恢复阈值（%）"), { target: { value: "77" } });
  fireEvent.change(within(form).getByLabelText("连续分钟"), { target: { value: "4" } });
  fireEvent.click(within(form).getByLabelText("全部节点（含以后新建的节点）"));
  fireEvent.click(within(form).getByLabelText("动态标签选择器"));
  fireEvent.change(within(form).getByLabelText("动态匹配标签（交集）"), { target: { value: "db" } });
  fireEvent.submit(form);
  await waitFor(() => expect(saved).toHaveLength(1));
  expect(saved[0].rule).toMatchObject({ kind: AlertKind.RESOURCE, resourceMetric: ResourceMetric.DISK_USED_PCT, threshold: 92, recoveryThreshold: 77, forMinutes: 4, taskId: 0n, metric: 0, allNodes: false, nodeIds: [], selectorTags: ["db"] });
});

it("资源指标下拉含六项，速率以 Mbps 输入并换算成 bytes/s 提交", async () => {
  const saved: SaveAlertRuleRequest[] = [];
  renderWithAdmin({
    listNodes: async () => ({ nodes: [] }),
    listNotifyChannels: async () => ({}), listProbeTasks: async () => ({}), listAlertRules: async () => ({}),
    saveAlertRule: async (req) => { saved.push(req); return {}; },
  }, [{ path: "/alerts", Component: AlertRules }], "/alerts");
  const form = await screen.findByRole("form", { name: "新建告警规则" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "出口带宽" } });
  fireEvent.change(within(form).getByLabelText("类型"), { target: { value: String(AlertKind.RESOURCE) } });
  const select = within(form).getByLabelText("资源指标");
  expect(within(select).getAllByRole("option").map((o) => o.textContent)).toEqual(["内存使用率", "磁盘使用率", "CPU 使用率", "每核负载", "下行速率", "上行速率"]);
  fireEvent.change(select, { target: { value: String(ResourceMetric.NET_TX_BPS) } });
  expect(within(form).getByLabelText("触发阈值（Mbps）")).toHaveAttribute("max", String((2 ** 40) / 125000));
  expect(form).toHaveTextContent(`触发阈值大于 0 且不超过 ${(2 ** 40) / 125000} Mbps，恢复阈值必须低于触发阈值`);
  fireEvent.change(within(form).getByLabelText("触发阈值（Mbps）"), { target: { value: "500" } });
  fireEvent.change(within(form).getByLabelText("恢复阈值（Mbps）"), { target: { value: "250" } });
  fireEvent.submit(form);
  await waitFor(() => expect(saved).toHaveLength(1));
  expect(saved[0].rule).toMatchObject({ kind: AlertKind.RESOURCE, resourceMetric: ResourceMetric.NET_TX_BPS, threshold: 62_500_000, recoveryThreshold: 31_250_000, forMinutes: 3 });
});

it("每核负载阈值按原值提交并提示旧 agent 缺读数", async () => {
  const saved: SaveAlertRuleRequest[] = [];
  renderWithAdmin({
    listNodes: async () => ({ nodes: [] }),
    listNotifyChannels: async () => ({}), listProbeTasks: async () => ({}), listAlertRules: async () => ({}),
    saveAlertRule: async (req) => { saved.push(req); return {}; },
  }, [{ path: "/alerts", Component: AlertRules }], "/alerts");
  const form = await screen.findByRole("form", { name: "新建告警规则" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "负载" } });
  fireEvent.change(within(form).getByLabelText("类型"), { target: { value: String(AlertKind.RESOURCE) } });
  fireEvent.change(within(form).getByLabelText("资源指标"), { target: { value: String(ResourceMetric.LOAD1_PER_CORE) } });
  expect(within(form).getByLabelText("触发阈值（每核）")).toHaveAttribute("max", "64");
  expect(form).toHaveTextContent("旧 agent 不上报时该分钟按缺失读数处理");
  fireEvent.change(within(form).getByLabelText("触发阈值（每核）"), { target: { value: "8" } });
  fireEvent.change(within(form).getByLabelText("恢复阈值（每核）"), { target: { value: "4" } });
  fireEvent.submit(form);
  await waitFor(() => expect(saved).toHaveLength(1));
  expect(saved[0].rule).toMatchObject({ resourceMetric: ResourceMetric.LOAD1_PER_CORE, threshold: 8, recoveryThreshold: 4 });
});
