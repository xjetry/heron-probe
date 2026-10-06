import type { AlignedData } from "uplot";
import type { ProbeSample } from "../gen/heron/v1/query_pb";
import { disambiguate, lossPercent, rttMeanMs, type ProbeValue } from "./probes";
import { gridOf } from "./series";

// 一次对比先 List 再按 List 给出的上限切块。上限缺席即 0（proto3 标量），0 不是“不限制”：
// 没有可以退回的固定块大小，调用方必须停下来报协议错误，而不是自己挑一个数继续请求。
export type ChunkPlan =
  | { ok: true; chunks: bigint[][] }
  | { ok: false; reason: "non-positive-limit" };

// 路由里的任务编号：只接受十进制正整数。0 与前导零不是任务 id 的写法，直接当无效路径。
export function taskIdParam(param: string | undefined): bigint | undefined {
  if (param === undefined || !/^[1-9][0-9]*$/.test(param)) return undefined;
  return BigInt(param);
}

export function chunkNodeIds(nodeIds: readonly bigint[], maxNodesPerQuery: number): ChunkPlan {
  if (!Number.isSafeInteger(maxNodesPerQuery) || maxNodesPerQuery < 1) {
    return { ok: false, reason: "non-positive-limit" };
  }
  const chunks: bigint[][] = [];
  for (let i = 0; i < nodeIds.length; i += maxNodesPerQuery) chunks.push(nodeIds.slice(i, i + maxNodesPerQuery));
  return { ok: true, chunks };
}

// 一块 QueryProbeComparison 的可见序列与不可见节点。stepS 由这一次窗口的选级决定，同一窗口的各块应当相同。
export type ComparisonChunk = {
  stepS: number;
  series: readonly { nodeId: bigint; samples: readonly ProbeSample[] }[];
  unavailableNodeIds: readonly bigint[];
};

export type ComparisonChart = {
  // 只含该图至少有一个读数的节点。整列没有读数的序列不放进来：Chart 会把它们的名字拼进一条提示，节点多时不可读。
  labels: string[];
  data: AlignedData;
  // 已经返回、且该图在窗口网格上没有任何读数的节点。还没返回的块里的节点不在这里——没取到不是“无结果”。
  missing: string[];
};

export type ComparisonAssembly = {
  // 每个候选都出现在某块的 series 或 unavailable 里，且各块 stepS 一致、为正。否则不能画：缺的块不是空序列。
  complete: boolean;
  stepS: number;
  loss: ComparisonChart;
  rtt: ComparisonChart;
  unavailable: string[];
};

function labelOf(id: bigint, names: ReadonlyMap<bigint, string>): string {
  const name = names.get(id);
  return name ? name : `#${id}`;
}

function column(samples: readonly ProbeSample[], xs: readonly number[], value: ProbeValue): (number | null)[] {
  const at = new Map<number, ProbeSample>();
  for (const sample of samples) at.set(Number(sample.ts), sample);
  return xs.map((t) => {
    const sample = at.get(t);
    return sample === undefined ? null : value(sample);
  });
}

// 0 是读数（全部本地错误的丢包率就是 0）。null 是没有读数，不能当成 0 画上去。
function isPlotted(v: number | null): v is number {
  return v !== null && Number.isFinite(v);
}

// nodeIds 是 List 的全序。chunks 只含已经返回的块：没出现在任何块里的节点既不画线，也不进“没有读数”。
// 丢包率只用 lossPercent（lost/sent），不因缺少 RTT 另记成 100%。RTT 图只保留至少有一个 RTT 的节点。
export function assembleComparison(
  nodeIds: readonly bigint[],
  names: ReadonlyMap<bigint, string>,
  chunks: readonly ComparisonChunk[],
  from: number,
  to: number,
): ComparisonAssembly {
  const seriesById = new Map<bigint, readonly ProbeSample[]>();
  const unavailable = new Set<bigint>();
  for (const chunk of chunks) {
    for (const series of chunk.series) {
      if (!seriesById.has(series.nodeId)) seriesById.set(series.nodeId, series.samples);
    }
    for (const id of chunk.unavailableNodeIds) unavailable.add(id);
  }
  const accounted = (id: bigint) => seriesById.has(id) || unavailable.has(id);
  const stepS = chunks[0]?.stepS ?? 0;
  const canGrid = stepS > 0 && chunks.every((chunk) => chunk.stepS === stepS);
  const complete = canGrid && nodeIds.every(accounted);
  const extraUnavailable = [...unavailable].filter((id) => !nodeIds.includes(id));
  const labelIds = [...nodeIds.filter(accounted), ...extraUnavailable];
  const labels = disambiguate(labelIds.map((id) => labelOf(id, names)), labelIds);
  const labelById = new Map(labelIds.map((id, i) => [id, labels[i]]));
  const xs = canGrid ? gridOf(stepS, from, to) : [];
  const chartFor = (value: ProbeValue): ComparisonChart => {
    const plotted: bigint[] = [];
    const cols: (number | null)[][] = [];
    const missing: string[] = [];
    for (const id of nodeIds) {
      if (!seriesById.has(id) || unavailable.has(id)) continue;
      const col = column(seriesById.get(id)!, xs, value);
      if (col.some(isPlotted)) {
        plotted.push(id);
        cols.push(col);
      } else if (canGrid) {
        missing.push(labelById.get(id)!);
      }
    }
    return { labels: plotted.map((id) => labelById.get(id)!), data: [xs, ...cols] as AlignedData, missing };
  };
  return {
    complete,
    stepS,
    loss: chartFor(lossPercent),
    rtt: chartFor(rttMeanMs),
    unavailable: [...nodeIds.filter((id) => unavailable.has(id)), ...extraUnavailable].map((id) => labelById.get(id)!),
  };
}
