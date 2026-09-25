import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { AlertDeliverySchema, AlertKind, AlertRuleSchema, AlertStateEntrySchema, ChannelKind, ListProbeTasksResponseSchema, NotifyChannelSchema, ProbeMetric } from "../gen/probe/v1/admin_pb";
import { ProbeKind } from "../gen/probe/v1/types_pb";
import { CHANNEL_KINDS, channelTarget, deliveryText, graceText, labelOf, ruleCondition, statesOf, transitionLabel } from "./alerts";

describe("labelOf", () => {
  it("表内值给标签，表外值显示原值而不抛错", () => {
    expect(labelOf(CHANNEL_KINDS, ChannelKind.WEBHOOK)).toBe("Webhook");
    expect(labelOf(CHANNEL_KINDS, 9 as ChannelKind)).toBe("未知（9）");
  });
});

describe("channelTarget", () => {
  it("Telegram 只显示会话", () => {
    const c = create(NotifyChannelSchema, { kind: ChannelKind.TELEGRAM, telegram: { chatId: "-100", hasBotToken: true } });
    expect(channelTarget(c)).toBe("会话 -100");
  });
  it("Webhook 显示方法、主机与头名，空方法按 POST", () => {
    const c = create(NotifyChannelSchema, { kind: ChannelKind.WEBHOOK, webhook: { method: "", hasUrl: true, urlHost: "https://hooks.example", headerNames: ["Authorization", "X-Tag"] } });
    expect(channelTarget(c)).toBe("POST https://hooks.example，头 Authorization、X-Tag");
  });
  it("Webhook 没有头时不带头名段", () => {
    const c = create(NotifyChannelSchema, { kind: ChannelKind.WEBHOOK, webhook: { method: "PUT", hasUrl: true, urlHost: "http://10.0.0.2:8080" } });
    expect(channelTarget(c)).toBe("PUT http://10.0.0.2:8080");
  });
});

const tasks = create(ListProbeTasksResponseSchema, { tasks: [{ task: { id: 3n, kind: ProbeKind.TCP, target: "1.1.1.1:443" } }] }).tasks;

describe("ruleCondition", () => {
  it("离线规则", () => {
    expect(ruleCondition(create(AlertRuleSchema, { kind: AlertKind.OFFLINE }), tasks)).toBe("超过宽限期未上报");
  });
  it("丢包规则带百分号", () => {
    const r = create(AlertRuleSchema, { kind: AlertKind.PROBE, taskId: 3n, metric: ProbeMetric.LOSS_PCT, threshold: 50, forMinutes: 3 });
    expect(ruleCondition(r, tasks)).toBe("TCP 1.1.1.1:443 丢包率 ≥ 50%，连续 3 分钟");
  });
  it("RTT 规则带 ms，已删除任务用编号", () => {
    const r = create(AlertRuleSchema, { kind: AlertKind.PROBE, taskId: 9n, metric: ProbeMetric.RTT_MS, threshold: 150, forMinutes: 5 });
    expect(ruleCondition(r, tasks)).toBe("任务 #9 RTT 均值 ≥ 150 ms，连续 5 分钟");
  });
});

describe("statesOf", () => {
  it("按规则分组触发与待定，ok 记录不展示", () => {
    const states = [
      create(AlertStateEntrySchema, { ruleId: 1n, nodeId: 1n, state: "firing" }),
      create(AlertStateEntrySchema, { ruleId: 1n, nodeId: 2n, state: "pending" }),
      create(AlertStateEntrySchema, { ruleId: 2n, nodeId: 1n, state: "ok" }),
    ];
    const got = statesOf(states);
    expect(got.get(1n)?.firing.map((s) => s.nodeId)).toEqual([1n]);
    expect(got.get(1n)?.pending.map((s) => s.nodeId)).toEqual([2n]);
    expect(got.has(2n)).toBe(false);
  });
});

describe("deliveryText", () => {
  it("成功、投递中、终止失败三种", () => {
    expect(deliveryText(create(AlertDeliverySchema, { ok: true, done: true, attempts: 1 }), "hook")).toBe("hook：已送达");
    expect(deliveryText(create(AlertDeliverySchema, { ok: false, done: false, attempts: 1, lastError: "503" }), "hook")).toBe("hook：投递中（已尝试 1 次）");
    expect(deliveryText(create(AlertDeliverySchema, { ok: false, done: true, attempts: 3, lastError: "timeout" }), "hook")).toBe("hook：失败（3 次）timeout");
  });
});

it("transitionLabel 认识两种变化，未知值原样显示", () => {
  expect([transitionLabel("firing"), transitionLabel("recovered"), transitionLabel("x")]).toEqual(["触发", "恢复", "x"]);
});

it("graceText 缺失即默认", () => {
  expect([graceText(undefined), graceText(90)]).toEqual(["默认", "90 秒"]);
});
