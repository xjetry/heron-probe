import type { AlignedData } from "uplot";
import type { ProbeSample, ProbeTaskDetail, QueryProbesResponse } from "../gen/probe/v1/admin_pb";
import { ProbeKind } from "../gen/probe/v1/types_pb";
import { gridOf } from "./series";

export type ProbeValue = (s: ProbeSample) => number | null;

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

// 已删除任务的历史仍会返回；没有任务可查时用编号，让线仍有名字。
export function taskLabel(id: bigint, tasks: ProbeTaskDetail[] | undefined): string {
  const t = tasks?.find((d) => d.task?.id === id)?.task;
  if (!t) return `任务 #${id}`;
  return `${t.kind === ProbeKind.TCP ? "TCP" : "ICMP"} ${t.target}`;
}
