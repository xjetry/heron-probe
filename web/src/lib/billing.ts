import { BillingCycle } from "../gen/probe/v1/types_pb";

// 面板的 Billing（types_pb）与公开页的 PublicBilling（public_pb）共有的字段（§9.4）；自动续期只在管理端有，这里不用。
// 节点五项都没填时 hub 不下发 billing，所以下面的函数都接受 undefined。
// 本文件只 import types_pb：公开入口也引用它，不能经它带进管理服务的生成代码（importScan.test.ts 钉住）。
export type BillingView = {
  price: string;
  currency: string;
  billingCycle: BillingCycle;
  expiresOn: string;
  // hub 按 --timezone 算好的剩余天数；负数是已过期天数，没有到期日时缺失。浏览器不自己再算：访客的时区与 hub 不同时，按本地日期算可能差一天。
  daysLeft?: number;
};

export const BILLING_CYCLES: readonly { value: BillingCycle; label: string }[] = [
  { value: BillingCycle.MONTHLY, label: "月" },
  { value: BillingCycle.QUARTERLY, label: "季" },
  { value: BillingCycle.SEMIANNUAL, label: "半年" },
  { value: BillingCycle.YEARLY, label: "年" },
  { value: BillingCycle.BIENNIAL, label: "两年" },
  { value: BillingCycle.TRIENNIAL, label: "三年" },
];

// 未指定是"没有周期"，给空串；表外的值（hub 比页面新）显示编号而不是空，免得读成没有周期。
export function cycleLabel(c: BillingCycle): string {
  if (c === BillingCycle.UNSPECIFIED) return "";
  return BILLING_CYCLES.find((e) => e.value === c)?.label ?? `未知（${c}）`;
}

// "USD 12.50 / 月"；没有价格但有周期时是"每月"；都没有时是空串。
export function priceText(b: BillingView | undefined): string {
  if (!b) return "";
  const cycle = cycleLabel(b.billingCycle);
  if (b.price === "") return cycle && `每${cycle}`;
  const amount = `${b.currency} ${b.price}`;
  return cycle ? `${amount} / ${cycle}` : amount;
}

// "2026-10-01（剩 4 天）" 或 "2026-09-24（已过期 3 天）"；没有到期日时是空串。
export function expiryText(b: BillingView | undefined): string {
  if (!b || b.expiresOn === "") return "";
  if (b.daysLeft === undefined) return b.expiresOn;
  return b.daysLeft < 0 ? `${b.expiresOn}（已过期 ${-b.daysLeft} 天）` : `${b.expiresOn}（剩 ${b.daysLeft} 天）`;
}

export const expired = (b: BillingView | undefined): boolean => b?.daysLeft !== undefined && b.daysLeft < 0;
