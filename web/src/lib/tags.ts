import { literalPattern } from "./fold";

// 标签名在 hub 按去首尾空白后的简单折叠比较（db 与 DB 是同一个标签）；页面判重用同一口径，免得同一个标签在草稿里出现两次。
export function sameTag(a: string, b: string): boolean {
  return literalPattern(a.trim(), true).test(b.trim());
}

// 把 raw 加进 tags：去首尾空白后为空或与已有的同名（折叠后）时原样返回，已有的写法优先。
export function withTag(tags: readonly string[], raw: string): string[] {
  const name = raw.trim();
  if (name === "" || tags.some((t) => sameTag(t, name))) return [...tags];
  return [...tags, name];
}

export function withoutTag(tags: readonly string[], name: string): string[] {
  return tags.filter((t) => !sameTag(t, name));
}
