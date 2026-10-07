import type { Traffic } from "../gen/heron/v1/types_pb";
import { bytes } from "./format";

export const QUOTA_UNITS = { GiB: 1n << 30n, TiB: 1n << 40n, GB: 1000000000n, TB: 1000000000000n };
export type QuotaUnit = keyof typeof QUOTA_UNITS;

// 十进制输入先用整数分子与分母表示，舍入只发生在最终字节数，避免浮点误差扩大到大配额。
export function parseQuota(text: string, unit: QuotaUnit): bigint | null {
  if (text.trim() === "") return 0n;
  if (!/^\d+(?:\.\d+)?$/.test(text) || text.length > 100) return null;
  const [whole, fraction = ""] = text.split(".");
  const denominator = 10n ** BigInt(fraction.length);
  const numerator = BigInt(whole + fraction) * QUOTA_UNITS[unit];
  const value = (numerator + denominator / 2n) / denominator;
  return value > 0n && value < (1n << 62n) ? value : null;
}

export function quotaInput(bytes: bigint, unit: QuotaUnit): string {
  if (bytes === 0n) return "";
  const scale = QUOTA_UNITS[unit];
  return `${bytes / scale}.${((bytes % scale) * 1000000n / scale).toString().padStart(6, "0")}`.replace(/\.?0+$/, "");
}

export function trafficText(t: Traffic): string {
  const used = bytes(t.quotaUsedBytes);
  return t.quotaBytes > 0n ? `${used} / ${bytes(t.quotaBytes)}（${(t.quotaUsedPct ?? 0).toFixed(1)}%）` : `${used}（未设配额）`;
}
