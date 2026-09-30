import { literalPattern } from "./fold";

type SearchableNode = { name: string; note?: string; lastSource?: string; country?: string; facts?: { hostname: string; network?: { ipv4?: { address: string }; ipv6?: { address: string } } } };

export function filterNodes<T extends SearchableNode>(nodes: readonly T[], search: string): T[] {
  // 空输入表示不过滤，连名称与 facts 都为空的节点也必须保留。
  if (search === "") return nodes.slice();
  const pattern = literalPattern(search, false);
  return nodes.filter((node) => [node.name, node.note, node.facts?.hostname, node.lastSource, node.country, node.facts?.network?.ipv4?.address, node.facts?.network?.ipv6?.address]
    .some((field) => field !== undefined && pattern.test(field)));
}
