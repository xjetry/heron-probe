import type { Node, NodeStatus as LiveNode } from "../gen/heron/v1/admin_pb";
import { liveStatus } from "./adminStatus";
import { filterNodes } from "./nodeSearch";
import { expiryLevel, STATUS_LABEL, STATUS_ORDER, type NodeStatus } from "./status";
import { sameTag } from "./tags";
import { olderThan } from "./version";

export type ScopeFilters = { status: NodeStatus | null; expiring: boolean; lagging: boolean };

export const STATUS_OPTIONS: readonly { value: NodeStatus; label: string }[] = STATUS_ORDER.map((value) => ({ value, label: STATUS_LABEL[value] }));

// 「需要处理」的卡链到这里（lib/attention.ts 的 to）：谓词必须与那边逐个相同，否则卡上的数与列表行数对不上。
// URL 是外部输入：不认识的值当没写，列表不能因为一个错字变空且无法清除。
export function scopeFromParams(params: URLSearchParams): ScopeFilters {
  const status = params.get("status");
  return {
    status: STATUS_ORDER.includes(status as NodeStatus) ? (status as NodeStatus) : null,
    expiring: params.get("expiring") === "1",
    lagging: params.get("lagging") === "1",
  };
}

export function paramsWithScope(params: URLSearchParams, scope: ScopeFilters): URLSearchParams {
  const next = new URLSearchParams(params);
  if (scope.status) next.set("status", scope.status); else next.delete("status");
  if (scope.expiring) next.set("expiring", "1"); else next.delete("expiring");
  if (scope.lagging) next.set("lagging", "1"); else next.delete("lagging");
  return next;
}

export const NO_SCOPE: ScopeFilters = { status: null, expiring: false, lagging: false };
export const isScoped = (scope: ScopeFilters): boolean => scope.status !== null || scope.expiring || scope.lagging;

// 标签过滤二选一：按一组标签取交集，或只要无标签节点。空 names 表示不过滤。
// 用判别式联合让"既选了标签又选了无标签"在类型上不可表示——hub 对两个条件同时给出返回 InvalidArgument
// （ListNodesRequest.untagged 注释），页面不该存在能构造出该请求的路径。
export type TagFilter = { kind: "tags"; names: string[] } | { kind: "untagged" };
export const NO_TAG_FILTER: TagFilter = { kind: "tags", names: [] };

// 搜索词与标签过滤同样由 URL 持有（q；tag 可重复；untagged=1），从详情返回列表时连同其余筛选一起还原。
// URL 是外部输入：untagged=1 与 tag 同时出现时取无标签（两者只能二选一），空的 tag 丢弃。
export function tagFilterFromParams(params: URLSearchParams): TagFilter {
  if (params.get("untagged") === "1") return { kind: "untagged" };
  return { kind: "tags", names: params.getAll("tag").filter((name) => name !== "") };
}

export function paramsWithTagFilter(params: URLSearchParams, filter: TagFilter): URLSearchParams {
  const next = new URLSearchParams(params);
  next.delete("tag");
  next.delete("untagged");
  if (filter.kind === "untagged") next.set("untagged", "1");
  else for (const name of filter.names) next.append("tag", name);
  return next;
}

export const searchFromParams = (params: URLSearchParams): string => params.get("q") ?? "";

export function paramsWithSearch(params: URLSearchParams, search: string): URLSearchParams {
  const next = new URLSearchParams(params);
  if (search !== "") next.set("q", search); else next.delete("q");
  return next;
}

// URL 里的标签名按现有标签清单折叠比较、换成清单里的写法并去重：多选框按写法精确匹配胶囊，换写法之前 URL 里的「DB」
// 既不算从清单消失的标签、又对不上选项「db」，会成为看不见的过滤条件。清单里没有的原样保留，由列表页作为可移除的
// 选项显示（与已选标签从清单消失同一处理）；清单未到或取不到时整体原样使用。
export function canonicalTagFilter(filter: TagFilter, known: readonly string[] | undefined): TagFilter {
  if (filter.kind === "untagged" || known === undefined) return filter;
  const names: string[] = [];
  for (const name of filter.names) {
    const canonical = known.find((tag) => sameTag(tag, name)) ?? name;
    if (!names.some((n) => sameTag(n, canonical))) names.push(canonical);
  }
  return { kind: "tags", names };
}

// 从节点列表进入详情时，列表把自己的查询串放进导航 state；详情页的「返回节点列表」据此回到同一组筛选。
// state 来自浏览器历史，按外部输入处理：只认以 "?" 开头的字符串，其余一律回到不带筛选的列表。
export const nodeListReturnState = (search: string) => ({ nodeListSearch: search });

export function nodeListReturnPath(state: unknown): string {
  const search = typeof state === "object" && state !== null ? (state as { nodeListSearch?: unknown }).nodeListSearch : undefined;
  return typeof search === "string" && search.startsWith("?") ? `/nodes${search}` : "/nodes";
}

export function applyScope(nodes: readonly Node[], live: ReadonlyMap<bigint, LiveNode>, boundAgentVersion: string | undefined, scope: ScopeFilters, search: string): Node[] {
  return filterNodes(nodes, search).filter((node) => {
    if (scope.status && liveStatus(node, live.get(node.id)) !== scope.status) return false;
    if (scope.expiring && expiryLevel(node.billing?.daysLeft) === "neutral") return false;
    // 绑定版本未知时谁都判不出落后：结果为空，调用方说明原因（与「需要处理」卡上的「—」同一含义）。
    if (scope.lagging && (boundAgentVersion === undefined || !olderThan(node.facts?.agentVersion, boundAgentVersion))) return false;
    return true;
  });
}
