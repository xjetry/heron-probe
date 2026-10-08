import type { Node, NodeStatus as LiveNode } from "../gen/heron/v1/admin_pb";
import { liveStatus } from "./adminStatus";
import { filterNodes } from "./nodeSearch";
import { expiryLevel, STATUS_LABEL, STATUS_ORDER, type NodeStatus } from "./status";
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

export const isScoped = (scope: ScopeFilters): boolean => scope.status !== null || scope.expiring || scope.lagging;

export function applyScope(nodes: readonly Node[], live: ReadonlyMap<bigint, LiveNode>, boundAgentVersion: string | undefined, scope: ScopeFilters, search: string): Node[] {
  return filterNodes(nodes, search).filter((node) => {
    if (scope.status && liveStatus(node, live.get(node.id)) !== scope.status) return false;
    if (scope.expiring && expiryLevel(node.billing?.daysLeft) === "neutral") return false;
    // 绑定版本未知时谁都判不出落后：结果为空，调用方说明原因（与「需要处理」卡上的「—」同一含义）。
    if (scope.lagging && (boundAgentVersion === undefined || !olderThan(node.facts?.agentVersion, boundAgentVersion))) return false;
    return true;
  });
}
