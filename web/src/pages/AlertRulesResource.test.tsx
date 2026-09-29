import { expect, it } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { renderWithAdmin } from "../test/harness";
import { AlertKind, ResourceMetric, type SaveAlertRuleRequest } from "../gen/probe/v1/admin_pb";
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
