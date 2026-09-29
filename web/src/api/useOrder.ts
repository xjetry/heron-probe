import { useLayoutEffect, useRef, useState } from "react";
import { errorText } from "./auth";

type Options<T> = {
  // 直接传查询结果数组；引用变化用于识别权威回读已送达观察器。
  items: readonly T[];
  id: (item: T) => bigint;
  enabled: boolean;
  save: (ids: bigint[]) => Promise<unknown>;
  // 必须取消旧读请求、读取完整列表，并在返回前写入调用方的查询缓存。
  reload: () => Promise<readonly T[]>;
};
type Session = { ids: bigint[]; invalidated: boolean; accepting: boolean };
type View<T> = { ids: bigint[] | null; source: readonly T[]; pending: boolean; blocked: boolean; error: unknown };

const same = (a: readonly bigint[], b: readonly bigint[]) => a.length === b.length && a.every((id, i) => id === b[i]);
const sameMembers = (a: readonly bigint[], b: readonly bigint[]) => {
  if (a.length !== b.length) return false;
  const members = new Set(b);
  return new Set(a).size === a.length && a.every((id) => members.has(id));
};

// 一次会话只允许一个写请求在途；点击改变期望排列，成功写入后才发送尚未保存的最新排列。
// 回读确认前，查询更新只能替换行内容，不能替换期望顺序；写入或回读失败均停止后续写入。
export function useOrder<T>({ items, id, enabled, save, reload }: Options<T>) {
  const [view, setView] = useState<View<T>>({ ids: null, source: items, pending: false, blocked: false, error: null });
  const session = useRef<Session | null>(null);
  const mounted = useRef(false);
  const latest = useRef({ items, id, enabled, save, reload });
  useLayoutEffect(() => {
    latest.current = { items, id, enabled, save, reload };
    // enabled 只在输入是完整列表时成立；过滤结果不能被误判为服务端删除了节点。
    if (enabled && session.current && !sameMembers(session.current.ids, items.map(id))) session.current.invalidated = true;
  });
  useLayoutEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; session.current = null; };
  }, []);

  // 权威回读可能先于查询观察器的下一帧返回；这段顺序在该帧到达前仍需保留。
  const settled = !view.pending && !view.blocked && items !== view.source;
  const order = settled ? null : view.ids;
  const positions = new Map(order?.map((value, i) => [value, i]));
  const ordered = order ? [...items].sort((a, b) => (positions.get(id(a)) ?? Infinity) - (positions.get(id(b)) ?? Infinity)) : items;

  const active = (s: Session) => mounted.current && session.current === s;
  const read = async (s: Session, error: unknown) => {
    try {
      const rows = await latest.current.reload();
      if (!active(s)) return;
      session.current = null;
      setView({ ids: rows.map(latest.current.id), source: latest.current.items, pending: false, blocked: false, error });
    } catch (readError) {
      if (!active(s)) return;
      session.current = null;
      setView((current) => ({ ...current, pending: false, blocked: true, error: new Error(`无法确认服务端排序，请重新读取后再排序。${[error, readError].filter(Boolean).map(errorText).join("；")}`) }));
    }
  };
  const run = async (s: Session) => {
    try {
      for (;;) {
        const sent = [...s.ids];
        await latest.current.save(sent);
        if (!active(s)) return;
        if (s.invalidated) throw new Error("列表成员已变化，未发送的排序已取消，请按最新列表重新排序。");
        if (!same(sent, s.ids)) continue;
        // 回读在途仍可累积点击；回读完成后才决定是否需要下一次写入。
        const rows = await latest.current.reload();
        if (!active(s)) return;
        if (!sameMembers(s.ids, rows.map(latest.current.id))) throw new Error("列表成员已变化，未发送的排序已取消，请按最新列表重新排序。");
        if (!same(sent, s.ids)) continue;
        session.current = null;
        setView({ ids: rows.map(latest.current.id), source: latest.current.items, pending: false, blocked: false, error: null });
        return;
      }
    } catch (error) {
      if (active(s)) {
        s.accepting = false;
        setView((current) => ({ ...current, blocked: true, error }));
        await read(s, error);
      }
    }
  };
  const move = (value: bigint, direction: -1 | 1) => {
    if (!enabled || view.blocked || session.current?.invalidated || session.current?.accepting === false) return;
    const current = session.current;
    const ids = [...(current?.ids ?? ordered.map(id))];
    const index = ids.indexOf(value);
    const destination = index + direction;
    if (index < 0 || destination < 0 || destination >= ids.length) return;
    [ids[index], ids[destination]] = [ids[destination], ids[index]];
    const s = current ?? { ids, invalidated: false, accepting: true };
    s.ids = ids;
    session.current = s;
    setView({ ids, source: items, pending: true, blocked: false, error: null });
    if (!current) void run(s);
  };
  const recover = () => {
    if (session.current || !view.blocked) return;
    const s = { ids: ordered.map(id), invalidated: false, accepting: false };
    session.current = s;
    setView((current) => ({ ...current, pending: true }));
    void read(s, view.error);
  };
  return { items: ordered, move, pending: view.pending, blocked: view.blocked, error: view.error, recover };
}
