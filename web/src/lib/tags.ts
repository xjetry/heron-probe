import { literalPattern } from "./fold";

// 去首尾空白用的字符集与 hub 的 strings.TrimSpace 相同：Unicode 的 White_Space 属性（unicode.IsSpace 的定义）。
// 不用 String.prototype.trim：它按 ECMAScript 的 WhiteSpace 与 LineTerminator 去，比 White_Space 多 U+FEFF、少 U+0085。
// 用它时，经 API 建的 "\uFEFFdb" 在 hub 是另一个标签，页面却会把它与 db 判成同一个。
const edgeSpace = /^\p{White_Space}+|\p{White_Space}+$/gu;
const trimTag = (raw: string) => raw.replace(edgeSpace, "");

// 标签名在 hub 按去首尾空白后的简单折叠比较（db 与 DB 是同一个标签）；页面判重用同一口径（去空白见 trimTag，折叠见
// literalPattern），免得同一个标签在草稿里出现两次。
export function sameTag(a: string, b: string): boolean {
  return literalPattern(trimTag(a), true).test(trimTag(b));
}

// 把 raw 加进 tags：去首尾空白后为空或与已有的同名（折叠后）时原样返回，已有的写法优先。
export function withTag(tags: readonly string[], raw: string): string[] {
  const name = trimTag(raw);
  if (name === "" || tags.some((t) => sameTag(t, name))) return [...tags];
  return [...tags, name];
}

export function withoutTag(tags: readonly string[], name: string): string[] {
  return tags.filter((t) => !sameTag(t, name));
}

// 公开页标签栏的点击语义。selected 为空表示不过滤；普通单击把选择收成"只选这一个"，当前恰好只选它时再点一次清空
// （回到全部）；shift 单击只翻转被点的一个，其余标签的状态不动。比较全部经 sameTag，与 hub 的折叠口径一致。
export function nextSelection(selected: readonly string[], tag: string, shift: boolean): string[] {
  const on = selected.some((s) => sameTag(s, tag));
  if (shift) return on ? withoutTag(selected, tag) : withTag(selected, tag);
  return on && selected.length === 1 ? [] : [tag];
}

// 多选取交集：节点必须带有所选的每一个标签。空选择不过滤——空条件匹配一切，这一分支显式写出而不靠 every 对空数组恒真。
export function matchesTags(nodeTags: readonly string[], selected: readonly string[]): boolean {
  if (selected.length === 0) return true;
  return selected.every((s) => nodeTags.some((t) => sameTag(t, s)));
}

// 快照里出现过的标签：跨节点按折叠去重、保留先出现的写法，按码元序排序（不用 localeCompare：顺序不随浏览器语言变）。
export function presentTags(nodes: readonly { tags: readonly string[] }[]): string[] {
  const out: string[] = [];
  for (const n of nodes) for (const t of n.tags) if (!out.some((o) => sameTag(o, t))) out.push(t);
  return out.sort();
}
