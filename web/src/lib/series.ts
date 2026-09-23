import type { AlignedData } from "uplot";
import type { QueryMetricsResponse } from "../gen/probe/v1/admin_pb";

// 把 hub 的响应铺成 uPlot 的对齐数组。x 轴按 step 补全 [from, to) 的网格：
// 缺桶与 n = 0 都是 null——无读数在图上是空洞，不是 0。均值直接用 hub 给的 mean。
export function toAligned(resp: QueryMetricsResponse, names: string[], from: number, to: number): AlignedData {
  const step = resp.stepS;
  const start = from - (from % step);
  const xs: number[] = [];
  for (let t = start; t < to; t += step) xs.push(t);
  const at = new Map<number, number>();
  resp.ts.forEach((t, i) => at.set(Number(t), i));
  const columns = names.map((name) => {
    const s = resp.series.find((x) => x.name === name);
    return xs.map((t) => {
      const i = at.get(t);
      const sample = i === undefined ? undefined : s?.samples[i];
      return sample !== undefined && sample.n > 0 && sample.mean !== undefined ? sample.mean : null;
    });
  });
  return [xs, ...columns] as AlignedData;
}

export function unitOf(resp: QueryMetricsResponse, name: string): string {
  return resp.series.find((x) => x.name === name)?.unit ?? "";
}
