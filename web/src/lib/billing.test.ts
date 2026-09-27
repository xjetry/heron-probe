import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { BillingCycle, BillingCycleSchema } from "../gen/probe/v1/types_pb";
import { BILLING_CYCLES, cycleLabel, expired, expiryText, priceText, type BillingView } from "./billing";

const none: BillingView = { price: "", currency: "", billingCycle: BillingCycle.UNSPECIFIED, expiresOn: "" };

// 夹具的 daysLeft 是 hub 下发的值，与"到期日减去浏览器今天"无关。时钟钉在离夹具几年之外的日期：在任何时区里，
// 按本地日期重算出的天数都与夹具不同，用 Date 自己算的实现一定红；不钉时，夹具恰好等于某一天的日历差，那一天
// 本地重算照样全绿。只 fake Date，计时器保持真实。
beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime("2030-06-15T12:00:00Z");
});
afterEach(() => { vi.useRealTimers(); });

describe("cycleLabel", () => {
  it("周期表覆盖协议枚举里除未指定之外的每个值", () => {
    for (const { number, name } of BillingCycleSchema.values) {
      if (number === BillingCycle.UNSPECIFIED) continue;
      expect(cycleLabel(number), name).not.toMatch(/^(|未知（\d+）)$/);
    }
    expect(BILLING_CYCLES.map((e) => e.label)).toEqual(["月", "季", "半年", "年", "两年", "三年"]);
  });
  it("未指定是没有周期，表外值显示编号", () => {
    expect([cycleLabel(BillingCycle.UNSPECIFIED), cycleLabel(9 as BillingCycle)]).toEqual(["", "未知（9）"]);
  });
});

describe("priceText", () => {
  it.each([
    [{ price: "12.50", currency: "USD", billingCycle: BillingCycle.MONTHLY }, "USD 12.50 / 月"],
    [{ price: "99", currency: "EUR", billingCycle: BillingCycle.TRIENNIAL }, "EUR 99 / 三年"],
    [{ price: "30", currency: "CNY" }, "CNY 30"],
    [{ billingCycle: BillingCycle.YEARLY }, "每年"],
    [{ currency: "USD" }, ""],
    [{}, ""],
  ])("%o → %s", (b, want) => {
    expect(priceText({ ...none, ...b })).toBe(want);
  });
});

describe("expiryText", () => {
  it.each([
    [{ expiresOn: "2026-10-01", daysLeft: 4 }, "2026-10-01（剩 4 天）", false],
    [{ expiresOn: "2026-09-27", daysLeft: 0 }, "2026-09-27（剩 0 天）", false],
    [{ expiresOn: "2026-09-24", daysLeft: -3 }, "2026-09-24（已过期 3 天）", true],
    [{ expiresOn: "2026-10-01" }, "2026-10-01", false],
    [{}, "", false],
  ])("%o → %s", (b, want, isExpired) => {
    expect(expiryText({ ...none, ...b })).toBe(want);
    expect(expired({ ...none, ...b })).toBe(isExpired);
  });
});

describe("没有 billing 的节点", () => {
  it("三个函数都当作什么都没填", () => {
    expect([priceText(undefined), expiryText(undefined), expired(undefined)]).toEqual(["", "", false]);
  });
});
