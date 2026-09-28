import { useState } from "react";

// 输入会变的查询（换过滤条件即换查询键）在新键的请求挂起或失败时还没有自己的数据：react-query 的 placeholderData
// 只在挂起期间补位，请求一失败 data 就回到 undefined，queryGate 随之判成未就绪，已渲染的列表连同其中未保存的草稿
// 一起卸载。这里沿用本组件上一次取到的数据，直到当前键取到自己的数据为止，失败只由 error 表达。
// stale 标出显示的不是当前输入的结果：只对当前结果成立的操作（例如要求完整排列的排序）据它关闭。
// 沿用的范围是组件实例：输入换成另一个对象（例如另一个节点）时，调用方要让组件重新挂载，否则会沿用别的对象的数据。
export function useRetained<Q extends { data: unknown; error: unknown }>(q: Q): { data: Q["data"]; error: Q["error"]; stale: boolean } {
  // 上一次取到的数据在渲染期间比较并记进 state，这是 React 记住上一次渲染的值的写法；当帧直接用 q.data，
  // state 只在之后换键、q.data 变回 undefined 时派上用场。
  const [last, setLast] = useState(q.data);
  if (q.data !== undefined && q.data !== last) setLast(q.data);
  return { data: q.data ?? last, error: q.error, stale: q.data === undefined && last !== undefined };
}
