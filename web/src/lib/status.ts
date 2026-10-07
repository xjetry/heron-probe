// 一个节点只属于一种状态，在线计数、汇总条、状态墙与徽章共用此判定。
// 维护中表示站长主动摘出节点，即使 agent 仍在上报也不计入在线。
// proto 以 lastSeenAt 缺失表示从未上报；hub 的 liveState 对这类节点返回 online=false。
export type NodeStatus = "online" | "offline" | "never" | "maintenance";
export type StatusInput = { online: boolean; maintenance: boolean; lastSeenAt?: bigint };

export function nodeStatus(n: StatusInput): NodeStatus {
  if (n.maintenance) return "maintenance";
  if (n.lastSeenAt === undefined) return "never";
  return n.online ? "online" : "offline";
}

export const STATUS_LABEL: Record<NodeStatus, string> = { online: "在线", offline: "离线", never: "从未上报", maintenance: "维护中" };
// 汇总条分段与图例共用固定顺序。
export const STATUS_ORDER: readonly NodeStatus[] = ["online", "maintenance", "never", "offline"];

export type Level = "neutral" | "attention" | "critical";

// 进度条两个边界都归需关注：<70 中性、70 至 90 琥珀、>90 玫红。
export function usageLevel(percent: number): Level {
  if (percent > 90) return "critical";
  if (percent >= 70) return "attention";
  return "neutral";
}

// 缺失到期日不表示已过期：只有负数归严重，0 至 30 天归需关注。
export function expiryLevel(daysLeft: number | undefined): Level {
  if (daysLeft === undefined) return "neutral";
  if (daysLeft < 0) return "critical";
  return daysLeft <= 30 ? "attention" : "neutral";
}
