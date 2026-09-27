import { describe, expect, it } from "vitest";
import { BillingCycle, BillingCycleSchema } from "../gen/probe/v1/types_pb";
import { BILLING_CYCLES, cycleLabel, expired, expiryText, priceText, type BillingView } from "./billing";

const none: BillingView = { price: "", currency: "", billingCycle: BillingCycle.UNSPECIFIED, expiresOn: "" };

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
