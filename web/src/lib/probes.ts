import type { AlignedData } from "uplot";
import type { ProbeSample, ProbeSeries, QueryProbesResponse } from "../gen/heron/v1/query_pb";
import { ProbeKind } from "../gen/heron/v1/types_pb";
import { gridOf } from "./series";

export type ProbeValue = (s: ProbeSample) => number | null;

// hub 的 Registry.Save 经 probelimit.CheckTask 只放行 ICMP 与 TCP，类型标签与选项依赖该准入。
export const PROBE_KINDS: readonly { kind: ProbeKind; label: string }[] = [
  { kind: ProbeKind.ICMP, label: "ICMP" },
  { kind: ProbeKind.TCP, label: "TCP" },
];
export const kindLabel = (kind: ProbeKind): string => PROBE_KINDS.find((entry) => entry.kind === kind)!.label;

// 丢包率只看超时：error 是本地无法发起（无 socket、解析失败），不是链路事实。
// hub 只返回 sent > 0 的点；这里仍显式守住除零，让不变式不依赖上游。
export const lossPercent: ProbeValue = (s) => (s.sent > 0 ? (s.lost / s.sent) * 100 : null);
// rtt 只在该点有成功探测时存在；没有就是空洞，不能画成 0。
export const rttMeanMs: ProbeValue = (s) => (s.rttMeanUs === undefined ? null : s.rttMeanUs / 1000);

// 每个任务一列，网格规则与指标图相同，缺 ts 为 null；同一窗口的探测图与指标图因此可以对齐比较。
export function toProbeAligned(resp: QueryProbesResponse, taskIds: bigint[], from: number, to: number, value: ProbeValue): AlignedData {
  const xs = gridOf(resp.stepS, from, to);
  const columns = taskIds.map((id) => {
    const at = new Map<number, ProbeSample>();
    resp.series.find((s) => s.taskId === id)?.samples.forEach((s) => at.set(Number(s.ts), s));
    return xs.map((t) => {
      const s = at.get(t);
      return s === undefined ? null : value(s);
    });
  });
  return [xs, ...columns] as AlignedData;
}

export function taskIdsOf(resp: QueryProbesResponse): bigint[] {
  return resp.series.map((s) => s.taskId);
}

// 序列自带任务的种类与目标（hub 查询时从任务清单读出）。kind 为 UNSPECIFIED 表示 hub 未标注，退回编号；
// 未标注的原因见 ProbeSeries 的注释：管理端是任务已删除，公开端另含已从该节点撤下的任务，所以不能当成"已删除"显示。
// hub 的任务准入只放行 ICMP 与 TCP（probelimit.CheckTask），所以其余种类只会是 UNSPECIFIED。
export function seriesLabel(s: ProbeSeries): string {
  return s.kind === ProbeKind.UNSPECIFIED ? `任务 #${s.taskId}` : `${kindLabel(s.kind)} ${s.target}`;
}

export function seriesLabels(series: readonly ProbeSeries[]): string[] {
  return disambiguate(series.map(seriesLabel), series.map((s) => s.taskId));
}

// 同一窗口可有配置不同却同名的任务；只给碰撞的标签追加编号，保留常见标签的简短形式。
export function disambiguate(labels: string[], ids: readonly bigint[]): string[] {
  const counts = new Map<string, number>();
  for (const label of labels) counts.set(label, (counts.get(label) ?? 0) + 1);
  return labels.map((label, i) => (counts.get(label)! > 1 ? `${label} #${ids[i]}` : label));
}
