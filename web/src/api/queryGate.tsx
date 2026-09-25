import type { ReactNode } from "react";
import { errorText } from "./auth";

type QuerySlice = { data: unknown; error: unknown };
type NotReady = { ready: false; fallback: ReactNode };
type Ready<T> = { ready: true; data: T; banner: ReactNode };
type DataOf<S> = S extends { data: infer D } ? Exclude<D, undefined> : never;

const errorView = (error: unknown) => <p role="alert" className="error">{errorText(error)}</p>;

// 轮询与失效刷新随时可能失败，已渲染的内容与未保存的草稿不能因一次刷新失败被卸载：
// 有数据时的失败只加横幅，没有数据的失败才整页报错。
// 数据是否到达由类型表达：页面先处理未就绪分支才拿得到 data（排除 undefined），
// 不用空数组兜底把"还在加载"伪装成"没有数据"。
// 本函数面向页面必需的查询；可选或禁用的查询要在业务层先区分——未就绪不代表请求正在进行。
export function queryGateAll<Ss extends readonly QuerySlice[]>(...queries: Ss): NotReady | Ready<{ [K in keyof Ss]: DataOf<Ss[K]> }> {
  for (const q of queries) {
    // 首次加载的自动重试期间 error 为空，整页错误只在首次加载彻底失败后出现；此前按加载中占位。
    if (q.data === undefined) {
      return { ready: false, fallback: q.error != null ? errorView(q.error) : <p className="muted">加载中…</p> };
    }
  }
  // 多个查询可同时失败：不同错误都要可见，同文去重，顺序与输入查询一致。
  const texts = [...new Set(queries.flatMap((q) => (q.error != null ? [errorText(q.error)] : [])))];
  return {
    ready: true,
    data: queries.map((q) => q.data) as { [K in keyof Ss]: DataOf<Ss[K]> },
    banner: texts.map((text) => <p key={text} role="alert" className="error">{text}</p>),
  };
}

export function queryGate<T>(q: { data: T | undefined; error: unknown }): NotReady | Ready<T> {
  const gate = queryGateAll(q);
  return gate.ready ? { ready: true, data: gate.data[0], banner: gate.banner } : gate;
}
