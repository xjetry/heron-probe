type SearchableNode = { name: string; note?: string; facts?: { hostname: string } };

export function filterNodes<T extends SearchableNode>(nodes: readonly T[], search: string): T[] {
  // 空输入表示不过滤，连名称与 facts 都为空的节点也必须保留。
  if (search === "") return nodes.slice();
  // Unicode 正则的 iu 模式做简单折叠（如 σ 与 ς）；转义元字符后只匹配字面子串。
  const pattern = new RegExp(search.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), "iu");
  return nodes.filter((node) => [node.name, node.note, node.facts?.hostname]
    .some((field) => field !== undefined && pattern.test(field)));
}
