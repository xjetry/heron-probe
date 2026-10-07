import { type Traffic, TrafficQuotaMode } from "../gen/heron/v1/types_pb";
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

// 配额的计入口径决定 quota_used_bytes 是哪个分子；展示分子的地方必须用同一口径作副标题，否则"只收"的数值配着"下载 + 上传"的说明。
// 未设配额时 hub 按 sum 计分子（traffic.Quota 的默认分支），副标题与之一致。
const QUOTA_MODE_LABELS: Record<TrafficQuotaMode, string> = {
  [TrafficQuotaMode.UNSPECIFIED]: "下载 + 上传",
  [TrafficQuotaMode.SUM]: "下载 + 上传",
  [TrafficQuotaMode.RX]: "仅下载",
  [TrafficQuotaMode.TX]: "仅上传",
  [TrafficQuotaMode.MAX]: "下载与上传取大者",
};

export function trafficDetail(t: Traffic): string {
  return t.quotaBytes > 0n ? QUOTA_MODE_LABELS[t.quotaMode] : QUOTA_MODE_LABELS[TrafficQuotaMode.SUM];
}
