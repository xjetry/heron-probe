import type { Node, NodeStatus as LiveNode } from "../gen/heron/v1/admin_pb";
import { nodeStatus, type NodeStatus } from "./status";

export function liveById(live: readonly LiveNode[] | undefined): ReadonlyMap<bigint, LiveNode> {
  return new Map((live ?? []).map((n) => [n.id, n]));
}

// 四态判定只有 nodeStatus 一处；管理端的输入分两个来源：快照条目带 hub 裁决的 online 与 lastSeenAt，节点资料才带 maintenance。
// 快照里没有这个节点（快照未到、刷新失败、刚创建还没进快照）时不能编一个 online=false 交给判定，否则会把未知误判为从未上报；
// 维护中例外：它由站长设置，不依赖快照。
export function liveStatus(node: Pick<Node, "maintenance">, live: Pick<LiveNode, "online" | "lastSeenAt"> | undefined): NodeStatus | undefined {
  if (!live && !node.maintenance) return undefined;
  return nodeStatus({ online: live?.online ?? false, maintenance: node.maintenance, lastSeenAt: live?.lastSeenAt });
}
