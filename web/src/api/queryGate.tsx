import type { ReactNode } from "react";
import { errorText } from "./auth";

type QuerySlice = { data: unknown; error: unknown; isPending: boolean };
type NotReady = { ready: false; fallback: ReactNode };
type Ready<T> = { ready: true; data: T; banner: ReactNode };
type DataOf<S> = S extends { data: infer D } ? NonNullable<D> : never;

const errorView = (error: unknown) => <p role="alert" className="error">{errorText(error)}</p>;

// 轮询与失效刷新随时可能失败，已渲染的内容与未保存的草稿不能因一次刷新失败被卸载：
// 有数据时的失败只加横幅，没有数据的失败才整页报错。
// 数据是否到达由类型表达：页面先处理未就绪分支才拿得到 data（非 undefined），
// 不用空数组兜底把"还在加载"伪装成"没有数据"。
export function queryGateAll<Ss extends readonly QuerySlice[]>(...queries: Ss): NotReady | Ready<{ [K in keyof Ss]: DataOf<Ss[K]> }> {
  for (const q of queries) {
    // 重试期间 error 仍为空，整页错误只在首次加载彻底失败后出现；此前按加载中占位。
    if (q.data === undefined) {
      return { ready: false, fallback: q.error != null ? errorView(q.error) : <p className="muted">加载中…</p> };
    }
  }
  const failed = queries.find((q) => q.error != null);
  return { ready: true, data: queries.map((q) => q.data) as { [K in keyof Ss]: DataOf<Ss[K]> }, banner: failed ? errorView(failed.error) : null };
}

export function queryGate<T>(q: { data: T | undefined; error: unknown; isPending: boolean }): NotReady | Ready<T> {
  const gate = queryGateAll(q);
  return gate.ready ? { ready: true, data: gate.data[0], banner: gate.banner } : gate;
}
