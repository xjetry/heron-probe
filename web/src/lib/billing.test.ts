import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { BillingCycle, BillingCycleSchema } from "../gen/heron/v1/types_pb";
import { BILLING_CYCLES, cycleLabel, priceText, remainingText, sortByExpiry, type BillingView } from "./billing";

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
    expect(BILLING_CYCLES.map((e) => e.label)).toEqual(["月", "季", "半年", "年", "两年", "三年", "五年"]);
  });
  it("未指定是没有周期，表外值显示编号", () => {
    expect([cycleLabel(BillingCycle.UNSPECIFIED), cycleLabel(9 as BillingCycle)]).toEqual(["", "未知（9）"]);
  });
});

describe("priceText", () => {
  it.each([
    [{ price: "12.50", currency: "USD", billingCycle: BillingCycle.MONTHLY }, "US$12.50 / 月"],
    [{ price: "99", currency: "EUR", billingCycle: BillingCycle.TRIENNIAL }, "€99 / 三年"],
    [{ price: "150", currency: "TWD", billingCycle: BillingCycle.QUINQUENNIAL }, "NT$150 / 五年"],
    [{ price: "30", currency: "CNY" }, "¥30"],
    // 小数位按存储值：JPY 12.5 不四舍五入成 13，USD 整数不补 ".00"。
    [{ price: "12.5", currency: "JPY" }, "JP¥12.5"],
    [{ price: "5", currency: "USD" }, "US$5"],
    // 三位小数的币种照存三位；zh-CN 里 KWD 的符号就是 "KWD"，与数额之间是不换行空格（U+00A0）。
    [{ price: "12.500", currency: "KWD" }, "KWD\u00A012.500"],
    // 未知三字母币种不抛错：CLDR 把代码本身当符号回退（与数额之间是不换行空格）；
    // 个别引擎直接拒绝（RangeError）时落入 catch，原样显示。两种都保留存储的小数位。
    [{ price: "10.00", currency: "XYZ" }, /^(XYZ[\u00A0 ]10\.00|XYZ 10\.00)$/],
    [{ price: "10.00", currency: "usd" }, "usd 10.00"],
    [{ price: "12.5.0", currency: "USD" }, "USD 12.5.0"],
    [{ price: "1e3", currency: "USD" }, "USD 1e3"],
    [{ billingCycle: BillingCycle.YEARLY }, "每年"],
    [{ currency: "USD" }, ""],
    [{}, ""],
  ])("%o → %s", (b, want) => {
    const got = priceText({ ...none, ...b });
    if (want instanceof RegExp) expect(got).toMatch(want);
    else expect(got).toBe(want);
  });
});

describe("remainingText", () => {
  it.each([[4, "剩 4 天"], [0, "剩 0 天"], [-3, "已过期 3 天"]])("%i → %s", (daysLeft, want) => {
    expect(remainingText(daysLeft)).toBe(want);
  });
});

describe("没有 billing 的节点", () => {
  it("价格当作什么都没填", () => {
    expect(priceText(undefined)).toBe("");
  });
});

describe("sortByExpiry", () => {
  const due = (days: number | undefined): BillingView => ({ ...none, expiresOn: days === undefined ? "" : "2030-07-01", daysLeft: days });
  const names = (xs: { name: string }[]) => xs.map((x) => x.name);

  it("到期早的在前：已过期最前，其后按剩余天数升序", () => {
    const nodes = [
      { name: "c", billing: due(30) }, { name: "a", billing: due(-3) }, { name: "b", billing: due(5) },
    ];
    expect(names(sortByExpiry(nodes))).toEqual(["a", "b", "c"]);
  });

  it("没有到期日的排最后：没有 billing、没填到期日、到期日无法解析（daysLeft 缺失）都算没有", () => {
    const nodes = [
      { name: "none" }, { name: "blank", billing: due(undefined) },
      { name: "bad", billing: { ...none, expiresOn: "not-a-date" } }, { name: "soon", billing: due(1) },
    ];
    expect(names(sortByExpiry(nodes))).toEqual(["soon", "none", "blank", "bad"]);
  });

  it("到期相同的保持原有相对顺序，并且不改动传入的数组", () => {
    const nodes = [
      { name: "x", billing: due(7) }, { name: "y", billing: due(2) }, { name: "z", billing: due(7) }, { name: "w", billing: due(2) },
    ];
    const before = names(nodes);
    expect(names(sortByExpiry(nodes))).toEqual(["y", "w", "x", "z"]);
    expect(names(nodes)).toEqual(before);
  });

  it("今天到期（0 天）排在已过期之后、未到期之前", () => {
    const nodes = [{ name: "later", billing: due(1) }, { name: "today", billing: due(0) }, { name: "gone", billing: due(-1) }];
    expect(names(sortByExpiry(nodes))).toEqual(["gone", "today", "later"]);
  });
});
