import { ratio } from "../components/Bar";
import type { PublicNode } from "../gen/heron/v1/public_pb";
import { sortByExpiry } from "../lib/billing";
import { literalPattern } from "../lib/fold";
import { expiryLevel, nodeStatus, STATUS_ORDER, usageLevel, type Level, type NodeStatus } from "../lib/status";
import { matchesTags, sameTag, type TagMatch } from "../lib/tags";

// tagMatch 只在选了至少两个标签时影响结果；默认交集，与管理端的标签过滤同一口径。
export type PublicFilters = { search: string; regions: readonly string[]; tags: readonly string[]; tagMatch: TagMatch; onlineOnly: boolean };
export const NO_FILTERS: PublicFilters = { search: "", regions: [], tags: [], tagMatch: "all", onlineOnly: false };

// 名称、标签与公开备注共用 lib/fold 的折叠字面匹配，不把访客输入当成正则表达式。
export function matchesSearch(node: PublicNode, search: string): boolean {
  if (search === "") return true;
  const pattern = literalPattern(search, false);
  return pattern.test(node.name) || node.tags.some((tag) => pattern.test(tag)) || pattern.test(node.publicRemark);
}

// 地区取并集（一个节点只有一个地区，选了至少两个地区时交集必然为空），标签按 tagMatch 取交集或并集，再与搜索及
// 四态在线判定取交集。
// 地区空选择表示不过滤、匹配一切；显式保留这一放宽分支。
export function filterPublicNodes(nodes: readonly PublicNode[], f: PublicFilters): PublicNode[] {
  return nodes.filter((n) =>
    matchesSearch(n, f.search)
    && (f.regions.length === 0 || f.regions.includes(n.country))
    && matchesTags(n.tags, f.tags, f.tagMatch)
    && (!f.onlineOnly || nodeStatus(n) === "online"));
}

export type NodeSort = "default" | "expiry" | "cpu" | "traffic";
export const NODE_SORTS: readonly { value: NodeSort; label: string }[] = [
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

export function sortNodes(nodes: readonly PublicNode[], sort: NodeSort): PublicNode[] {
  switch (sort) {
    case "expiry": return sortByExpiry(nodes);
    case "cpu": return [...nodes].sort(byDesc((n) => n.metrics?.cpuPct));
    // 按卡片与列表上显示的那个数排序：本周期用量按节点自己的配额口径计（与 trafficText 同源）。
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

const byCodeUnknownLast = (a: string, b: string) => (a === b ? 0 : a === "" ? 1 : b === "" ? -1 : a.localeCompare(b));

// 地区与标签筛选的选项（public/Facet.tsx），计数是当前快照里的节点数、不随其他筛选变化。
export type FacetOption = { value: string; label: string; count: number };

export function regionOptions(nodes: readonly PublicNode[]): FacetOption[] {
  const counts = new Map<string, number>();
  for (const n of nodes) counts.set(n.country, (counts.get(n.country) ?? 0) + 1);
  return [...counts.keys()].sort(byCodeUnknownLast).map((code) => ({ value: code, label: regionName(code), count: counts.get(code)! }));
}

// 标签的集合与顺序取 hub 下发的并集（按折叠键排序，与面板同序），页面不自己汇总、排序：折叠规则只在 hub 一处。
export function tagOptions(nodes: readonly PublicNode[], tags: readonly string[]): FacetOption[] {
  return tags.map((tag) => ({ value: tag, label: tag, count: nodes.filter((n) => n.tags.some((t) => sameTag(t, tag))).length }));
}

export type GroupBy = "region" | "tag";
export const GROUP_BYS: readonly { value: GroupBy; label: string }[] = [{ value: "region", label: "地区" }, { value: "tag", label: "标签" }];

export type WallGroup = { key: string; name: string; nodes: PublicNode[]; online: number };

const countOnline = (nodes: readonly PublicNode[]) => nodes.filter((n) => nodeStatus(n) === "online").length;

// 两种分组同一排序：兜底组（未知地区、无标签）固定最后；其余按四态在线数降序、总数降序，再按各自的 tiebreak。
function ordered(groups: WallGroup[], fallbackKey: string, tiebreak: (a: WallGroup, b: WallGroup) => number): WallGroup[] {
  return groups.sort((a, b) => {
    if ((a.key === fallbackKey) !== (b.key === fallbackKey)) return a.key === fallbackKey ? 1 : -1;
    return b.online - a.online || b.nodes.length - a.nodes.length || tiebreak(a, b);
  });
}

// 组键是国家代码，未知地区为空串；同在线数、同总数时按代码排。
export function groupByRegion(nodes: readonly PublicNode[]): WallGroup[] {
  const groups = new Map<string, WallGroup>();
  for (const n of nodes) {
    const g = groups.get(n.country) ?? { key: n.country, name: regionName(n.country), nodes: [], online: 0 };
    g.nodes.push(n);
    groups.set(n.country, g);
  }
  for (const g of groups.values()) g.online = countOnline(g.nodes);
  return ordered([...groups.values()], "", (a, b) => a.key.localeCompare(b.key));
}

const UNTAGGED = "untagged";

// 组的集合、写法与 tiebreak 次序取 hub 下发的标签并集（按折叠键排序），与标签筛选同源；节点标签按 sameTag 折叠比较归组。
// 一个节点出现在它的每个标签组里，各组计数独立，组计数之和可以大于节点数。并集覆盖全部公开节点的标签，
// 落不进任何组的只有没有标签的节点，归入「无标签」。筛选后没有节点的标签不成组。
export function groupByTag(nodes: readonly PublicNode[], tags: readonly string[]): WallGroup[] {
  const groups: WallGroup[] = [];
  const placed = new Set<PublicNode>();
  for (const tag of tags) {
    const members = nodes.filter((n) => n.tags.some((t) => sameTag(t, tag)));
    for (const n of members) placed.add(n);
    if (members.length > 0) groups.push({ key: `tag:${tag}`, name: tag, nodes: members, online: countOnline(members) });
  }
  const rest = nodes.filter((n) => !placed.has(n));
  if (rest.length > 0) groups.push({ key: UNTAGGED, name: "无标签", nodes: rest, online: countOnline(rest) });
  const order = new Map(tags.map((tag, i) => [`tag:${tag}`, i]));
  return ordered(groups, UNTAGGED, (a, b) => order.get(a.key)! - order.get(b.key)!);
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
