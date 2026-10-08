import { ratio } from "../components/Bar";
import type { PublicNode } from "../gen/heron/v1/public_pb";
import { sortByExpiry } from "../lib/billing";
import { flag } from "../lib/country";
import { literalPattern } from "../lib/fold";
import { expiryLevel, nodeStatus, STATUS_ORDER, usageLevel, type Level, type NodeStatus } from "../lib/status";
import { matchesTags } from "../lib/tags";

export type PublicFilters = { search: string; regions: readonly string[]; tags: readonly string[]; onlineOnly: boolean };
export const NO_FILTERS: PublicFilters = { search: "", regions: [], tags: [], onlineOnly: false };

// 名称、标签与公开备注共用 lib/fold 的折叠字面匹配，不把访客输入当成正则表达式。
export function matchesSearch(node: PublicNode, search: string): boolean {
  if (search === "") return true;
  const pattern = literalPattern(search, false);
  return pattern.test(node.name) || node.tags.some((tag) => pattern.test(tag)) || pattern.test(node.publicRemark);
}

// 地区取并集，标签由 matchesTags 取交集，再与搜索及四态在线判定取交集。
// 地区空选择表示不过滤、匹配一切；显式保留这一放宽分支。
export function filterPublicNodes(nodes: readonly PublicNode[], f: PublicFilters): PublicNode[] {
  return nodes.filter((n) =>
    matchesSearch(n, f.search)
    && (f.regions.length === 0 || f.regions.includes(n.country))
    && matchesTags(n.tags, f.tags)
    && (!f.onlineOnly || nodeStatus(n) === "online"));
}

export type CardSort = "default" | "expiry" | "cpu" | "traffic";
export const CARD_SORTS: readonly { value: CardSort; label: string }[] = [
  { value: "default", label: "默认" }, { value: "expiry", label: "到期" }, { value: "cpu", label: "CPU" }, { value: "traffic", label: "流量" },
];

// 缺读数排最后，相等保持传入顺序；直接比较 bigint，避免字节数超过安全整数后丢失顺序。
function byDesc(key: (n: PublicNode) => number | bigint | undefined) {
  return (a: PublicNode, b: PublicNode) => {
    const ka = key(a), kb = key(b);
    if (ka === kb) return 0;
    if (ka === undefined) return 1;
    if (kb === undefined) return -1;
    return ka < kb ? 1 : -1;
  };
}

export function sortCards(nodes: readonly PublicNode[], sort: CardSort): PublicNode[] {
  switch (sort) {
    case "expiry": return sortByExpiry(nodes);
    case "cpu": return [...nodes].sort(byDesc((n) => n.metrics?.cpuPct));
    // 按卡片上显示的那个数排序：本周期用量按节点自己的配额口径计（与 trafficText 同源）。
    case "traffic": return [...nodes].sort(byDesc((n) => n.traffic?.quotaUsedBytes));
    default: return [...nodes];
  }
}

export type ColorBy = "status" | "cpu" | "memory" | "expiry";
export const COLOR_BYS: readonly { value: ColorBy; label: string }[] = [
  { value: "status", label: "状态" }, { value: "cpu", label: "CPU" }, { value: "memory", label: "内存" }, { value: "expiry", label: "到期" },
];

// 状态着色来自四态，不给使用量档位；缺使用量读数不给档位，缺到期日由 expiryLevel 判为中性。
export function tileLevel(node: PublicNode, by: ColorBy): Level | undefined {
  const m = node.metrics;
  switch (by) {
    case "cpu": return m?.cpuPct === undefined ? undefined : usageLevel(m.cpuPct);
    case "memory": return m?.memUsed === undefined || !m.memTotal ? undefined : usageLevel(ratio(m.memUsed, m.memTotal));
    case "expiry": return expiryLevel(node.billing?.daysLeft);
    default: return undefined;
  }
}

// 中文短名适合组头，不维护另一份国家名表；空串会被 DisplayNames 拒绝，先归入「未知」。
export function regionName(code: string): string {
  if (code === "") return "未知";
  try {
    return new Intl.DisplayNames(["zh-CN"], { type: "region", style: "short", fallback: "code" }).of(code) ?? code;
  } catch {
    return code;
  }
}

function regionLabel(code: string): string {
  return code === "" ? "未知" : `${flag(code)} ${regionName(code)}`;
}

const byCodeUnknownLast = (a: string, b: string) => (a === b ? 0 : a === "" ? 1 : b === "" ? -1 : a.localeCompare(b));

export type RegionOption = { value: string; label: string; count: number };

export function regionOptions(nodes: readonly PublicNode[]): RegionOption[] {
  const counts = new Map<string, number>();
  for (const n of nodes) counts.set(n.country, (counts.get(n.country) ?? 0) + 1);
  return [...counts.keys()].sort(byCodeUnknownLast).map((code) => ({ value: code, label: regionLabel(code), count: counts.get(code)! }));
}

export type RegionGroup = { code: string; name: string; nodes: PublicNode[]; online: number };

// 未知地区固定排最后；其余先按四态在线数降序，再按总数降序和代码排序。
export function groupByRegion(nodes: readonly PublicNode[]): RegionGroup[] {
  const groups = new Map<string, RegionGroup>();
  for (const n of nodes) {
    const g = groups.get(n.country) ?? { code: n.country, name: regionName(n.country), nodes: [], online: 0 };
    g.nodes.push(n);
    if (nodeStatus(n) === "online") g.online++;
    groups.set(n.country, g);
  }
  return [...groups.values()].sort((a, b) => {
    if ((a.code === "") !== (b.code === "")) return a.code === "" ? 1 : -1;
    return b.online - a.online || b.nodes.length - a.nodes.length || a.code.localeCompare(b.code);
  });
}

export type Summary = { total: number; counts: Record<NodeStatus, number>; rxBps: bigint; txBps: bigint; periodBytes: bigint };

// 四态互斥，计数之和等于总数。离线的最后读数是旧值，不计入实时速率；
// 本周期流量是 hub 累计值，离线节点也保留其贡献。
export function summarize(nodes: readonly PublicNode[]): Summary {
  const counts = Object.fromEntries(STATUS_ORDER.map((s) => [s, 0])) as Record<NodeStatus, number>;
  let rxBps = 0n, txBps = 0n, periodBytes = 0n;
  for (const n of nodes) {
    const status = nodeStatus(n);
    counts[status]++;
    if (status === "online" || status === "maintenance") {
      rxBps += n.metrics?.netRxBps ?? 0n;
      txBps += n.metrics?.netTxBps ?? 0n;
    }
    // 全站合计是实际收发的字节数，不是各节点配额口径的分子：只收、只发、取大者的分子相加没有物理含义。
    if (n.traffic) periodBytes += n.traffic.periodRx + n.traffic.periodTx;
  }
  return { total: nodes.length, counts, rxBps, txBps, periodBytes };
}
