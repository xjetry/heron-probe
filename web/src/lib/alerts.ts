import { AlertKind, ChannelKind, DeliveryFailure, ProbeMetric, type AlertDelivery, type AlertRule, type AlertStateEntry, type NotifyChannel, type ProbeTaskDetail } from "../gen/probe/v1/admin_pb";
import { formatUnit } from "./format";
import { disambiguate, kindLabel } from "./probes";

type Entry<K> = { value: K; label: string };

export const CHANNEL_KINDS: readonly Entry<ChannelKind>[] = [
  { value: ChannelKind.TELEGRAM, label: "Telegram" },
  { value: ChannelKind.WEBHOOK, label: "Webhook" },
];

// 更新的 hub 可能返回面板不认识的枚举值；显示原值而不是抛错，整页不因一个标签失效。
export function labelOf<K extends number>(table: readonly Entry<K>[], value: K): string {
  return table.find((e) => e.value === value)?.label ?? `未知（${value}）`;
}

// 凭据只写不读（spec §9.3）：目标描述只用到 hub 回显的 chat_id、scheme://host、方法与头名，秘密值不出现在页面上。
export function channelTarget(c: NotifyChannel): string {
  if (c.kind === ChannelKind.TELEGRAM) return `会话 ${c.telegram?.chatId ?? ""}`;
  if (c.kind === ChannelKind.WEBHOOK) {
    const w = c.webhook;
    const head = `${methodOf(w?.method)} ${w?.urlHost ?? ""}`;
    return w && w.headerNames.length > 0 ? `${head}，头 ${w.headerNames.join("、")}` : head;
  }
  return labelOf(CHANNEL_KINDS, c.kind);
}

// 出站节奏上限（proto NotifyChannel.rate_per_minute）：0 表示不限。hub 的响应里恒有值，缺席只会来自不带这个字段的旧 hub。
export function rateLabel(c: NotifyChannel): string {
  if (c.ratePerMinute === undefined) return "—";
  return c.ratePerMinute === 0 ? "不限" : `每分钟 ${c.ratePerMinute} 条`;
}

// 已保存 Webhook 的空 method 由 hub 的 Engine.SaveChannel 规范成 POST；
// 面板为没有 Webhook 配置的渠道（例如 Telegram 渠道被编辑时）建立草稿也需要 POST 作初值。
export function methodOf(method: string | undefined): string {
  return method || "POST";
}

export const ALERT_KINDS: readonly Entry<AlertKind>[] = [
  { value: AlertKind.OFFLINE, label: "离线" },
  { value: AlertKind.PROBE, label: "探测" },
  { value: AlertKind.EXPIRY, label: "到期" },
];
// unit 与 formatUnit 的单位名一致：丢包阈值是百分数，RTT 阈值是毫秒（proto AlertRule.threshold）。
export const PROBE_METRICS: readonly (Entry<ProbeMetric> & { unit: string })[] = [
  { value: ProbeMetric.LOSS_PCT, label: "丢包率", unit: "percent" },
  { value: ProbeMetric.RTT_MS, label: "RTT 均值", unit: "ms" },
];

// 调用方是告警规则页与 ruleCondition，任务取自管理端的任务列表；列表未到或查询失败时用编号，标签仍可辨认。
// 被告警规则引用的任务不能删除（store 的 checkAlertReferences），所以这里的编号回退不代表任务已删除。
// 历史图例不走这里：序列自带标注，用 probes.ts 的 seriesLabels。
export function taskLabel(id: bigint, tasks: readonly ProbeTaskDetail[] | undefined): string {
  const t = tasks?.find((d) => d.task?.id === id)?.task;
  if (!t) return `任务 #${id}`;
  return `${kindLabel(t.kind)} ${t.target}`;
}

export function taskLabels(ids: bigint[], tasks: readonly ProbeTaskDetail[] | undefined): string[] {
  return disambiguate(ids.map((id) => taskLabel(id, tasks)), ids);
}

export function ruleCondition(rule: AlertRule, tasks: ProbeTaskDetail[] | undefined): string {
  if (rule.kind === AlertKind.OFFLINE) return "超过宽限期未上报";
  if (rule.kind === AlertKind.EXPIRY) return `到期日距今不超过 ${rule.daysBefore} 天（含已过期）`;
  const metric = PROBE_METRICS.find((m) => m.value === rule.metric);
  const threshold = metric ? formatUnit(rule.threshold, metric.unit) : String(rule.threshold);
  return `${taskLabel(rule.taskId, tasks)} ${labelOf(PROBE_METRICS, rule.metric)} ≥ ${threshold}，连续 ${rule.forMinutes} 分钟`;
}

export type RuleStates = { firing: AlertStateEntry[]; pending: AlertStateEntry[] };

// hub 只返回已有记录的组合，缺失即 ok（proto ListAlertRulesResponse.states）；ok 记录同样不展示。
export function statesOf(states: AlertStateEntry[]): Map<bigint, RuleStates> {
  const out = new Map<bigint, RuleStates>();
  for (const s of states) {
    if (s.state !== "firing" && s.state !== "pending") continue;
    const entry = out.get(s.ruleId) ?? { firing: [], pending: [] };
    entry[s.state].push(s);
    out.set(s.ruleId, entry);
  }
  return out;
}

export const transitionLabel = (t: string): string =>
  t === "firing" ? "触发" : t === "recovered" ? "恢复" : t === "disabled" ? "停用" : t;

// 与 proto DeliveryFailure 逐值对齐（测试按枚举全集核对）；UNSPECIFIED 表示没有失败，不在表里。
// 面板依赖"HTTP_STATUS 必带 http_status"，由 hub 写库前的校验（DeliveryResult.check）保证；
// 反方向"http_status 只随 HTTP_STATUS 出现"由 api 的 ListAlertEvents 映射保证。
// hasText：hub 为该类别记下错误原文；渠道删除与结果未落盘没有原文（proto 枚举注释），不给查看入口。
export const DELIVERY_FAILURES: readonly { value: DeliveryFailure; text: (d: AlertDelivery) => string; hasText: boolean }[] = [
  { value: DeliveryFailure.HTTP_STATUS, text: (d) => `HTTP ${d.httpStatus}`, hasText: true },
  { value: DeliveryFailure.TRANSPORT, text: () => "连接失败", hasText: true },
  { value: DeliveryFailure.REQUEST, text: () => "请求无法构造", hasText: true },
  { value: DeliveryFailure.CHANNEL_INVALID, text: () => "渠道配置无效", hasText: true },
  { value: DeliveryFailure.CHANNEL_DELETED, text: () => "渠道已删除", hasText: false },
  { value: DeliveryFailure.RESULT_UNRECORDED, text: () => "结果未记录", hasText: false },
  { value: DeliveryFailure.UNCLASSIFIED, text: () => "未分类", hasText: true },
];

// 只对终态失败给原文入口：未终态时类别属于上一次尝试，原文随后续尝试变化。
export function hasErrorText(d: AlertDelivery): boolean {
  return !d.ok && d.done && DELIVERY_FAILURES.some((e) => e.value === d.failure && e.hasText);
}

// 表外的值（hub 比面板新）显示编号而不是空：失败必须带着可追查的类别出现。
export function failureText(d: AlertDelivery): string {
  return DELIVERY_FAILURES.find((e) => e.value === d.failure)?.text(d) ?? `类别 ${d.failure}`;
}

// 已送达只由 ok 为真决定；done 为假表示尚未终态，可能等待入窗、正在尝试或等待重试，不能显示为终止失败。
export function deliveryText(d: AlertDelivery, channel: string): string {
  if (d.ok) return `${channel}：已送达`;
  if (!d.done) return `${channel}：投递中（已尝试 ${d.attempts} 次）`;
  return `${channel}：失败（${d.attempts} 次）${failureText(d)}`;
}

// 缺失表示取 hub 的 PROBE_OFFLINE_AFTER（proto Node.offline_grace_s）；清除后 hub 存 NULL，不会回显 0。
export const graceText = (s: number | undefined): string => (s === undefined ? "默认" : `${s} 秒`);
