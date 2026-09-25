// hub 要求 id 列表升序去重；bigint 不能用默认的字典序比较。
// 去重由调用方保证（Set 或服务端唯一 id 列表）。
export const ascending = (ids: Iterable<bigint>): bigint[] => [...ids].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));

export function toggled(set: ReadonlySet<bigint>, id: bigint): Set<bigint> {
  const next = new Set(set);
  if (next.has(id)) next.delete(id); else next.add(id);
  return next;
}
