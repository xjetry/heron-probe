import { errorText } from "../api/auth";

// 轮询与失效刷新随时可能失败，已渲染的内容与用户未保存的草稿不能因一次刷新失败被卸载。
// 查询保留了 data 时只加横幅；没有数据的失败才阻断内容。
export function queryFeedback({ data, error }: { data: unknown; error: unknown }) {
  return {
    blocked: data === undefined && error != null,
    banner: error != null ? <p role="alert" className="error">{errorText(error)}</p> : null,
  };
}
