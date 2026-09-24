import type { AlignedData } from "uplot";
import type { QueryMetricsResponse } from "../gen/probe/v1/admin_pb";

// x 轴按 step 补全 [from, to) 的网格；起点向下对齐到 step 的整数倍，与 hub 的点对齐规则一致。
export function gridOf(step: number, from: number, to: number): number[] {
  const start = from - (from % step);
  const xs: number[] = [];
  for (let t = start; t < to; t += step) xs.push(t);
  return xs;
}

// 把 hub 的响应铺成 uPlot 的对齐数组。x 轴按 step 补全 [from, to) 的网格：
// 缺桶与 n = 0 都是 null——无读数在图上是空洞，不是 0。均值直接用 hub 给的 mean；
// 可加量（带 sum 的样本）画速率 sum / step，让 1m 与 1h 两级在同一根轴上可比。
export function toAligned(resp: QueryMetricsResponse, names: string[], from: number, to: number): AlignedData {
  const step = resp.stepS;
  const xs = gridOf(step, from, to);
  const at = new Map<number, number>();
  resp.ts.forEach((t, i) => at.set(Number(t), i));
  const columns = names.map((name) => {
    const s = resp.series.find((x) => x.name === name);
    return xs.map((t) => {
      const i = at.get(t);
      const sample = i === undefined ? undefined : s?.samples[i];
      if (sample === undefined || sample.n === 0) return null;
      if (sample.sum !== undefined) return sample.sum / step;
      return sample.mean ?? null;
    });
  });
  return [xs, ...columns] as AlignedData;
}

export function unitOf(resp: QueryMetricsResponse, name: string): string {
  return resp.series.find((x) => x.name === name)?.unit ?? "";
}
