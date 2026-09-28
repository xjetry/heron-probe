import { useRef, useState } from "react";

// 输入会变的查询（换过滤条件即换查询键）在新键的请求挂起或失败时还没有自己的数据：react-query 的 placeholderData
// 只在挂起期间补位，请求一失败 data 就回到 undefined，queryGate 随之判成未就绪，已渲染的列表连同其中未保存的草稿
// 一起卸载。这里沿用本组件上一次取到的数据，直到当前键取到自己的数据为止，失败只由 error 表达。
// stale 标出显示的不是当前输入的结果：只对当前结果成立的操作（例如要求完整排列的排序）据它关闭。
// 沿用的范围默认是组件实例：同一个调用点跨渲染沿用。输入换成另一个对象（例如另一个节点）时不能沿用上一个
// 对象的数据——那会把它当成这一个对象的结果显示。对象的身份经 identity 参数显式声明：identity 相对上一次
// 渲染变化的那一帧连同之后，沿用值清空，直到当前 identity 下的查询自己取到数据为止；不传 identity 等于
// 调用点自己就是身份（默认行为不变，Nodes 页换过滤条件属于这一种——过滤条件变了，"节点列表"这个对象没变）。
export function useRetained<Q extends { data: unknown; error: unknown }>(q: Q, identity?: unknown): { data: Q["data"]; error: Q["error"]; stale: boolean } {
  const lastIdentity = useRef(identity);
  const changedIdentity = lastIdentity.current !== identity;
  lastIdentity.current = identity;
  // 上一次取到的数据在渲染期间比较并记进 state，这是 React 记住上一次渲染的值的写法；当帧直接用 q.data，
  // state 只在之后换键、q.data 变回 undefined 时派上用场。identity 变化的这一帧不论 q.data 是否已就绪都
  // 清空 last：换对象后就算新对象恰好已经有数据，也不该把这次更新的值当成"上一个对象的沿用"记下来，返回值
  // 改用 retained（当帧算出的有效沿用值，identity 变化时强制为 undefined），不再直接读 last——沿用 useState
  // 在渲染期间更新后要等下一次渲染才生效的写法，这一帧读 last 仍是变化前的值。
  const [last, setLast] = useState(q.data);
  if (changedIdentity) {
    if (last !== undefined) setLast(undefined);
  } else if (q.data !== undefined && q.data !== last) {
    setLast(q.data);
  }
  const retained = changedIdentity ? undefined : last;
  return { data: q.data ?? retained, error: q.error, stale: q.data === undefined && retained !== undefined };
}
