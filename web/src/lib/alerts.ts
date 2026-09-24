import { ChannelKind, type NotifyChannel } from "../gen/probe/v1/admin_pb";

export type Entry<K> = { value: K; label: string };

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

// 已保存 Webhook 的空 method 由 hub 的 Engine.SaveChannel 规范成 POST；
// 面板为没有 Webhook 配置的渠道（例如 Telegram 渠道被编辑时）建立草稿也需要 POST 作初值。
export function methodOf(method: string | undefined): string {
  return method || "POST";
}
