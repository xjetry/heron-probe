import type { CoverageSummary } from "../gen/heron/v1/query_pb";

// 上报覆盖率的显示口径（与 proto CoverageSummary 的注释一致）：
// - 响应没有 coverage_summary：对端是还没有这个字段的旧 hub，不是覆盖率为零；
// - coverage_start 缺席：hub 没有留存的覆盖起点，可能是从未上报，也可能是删除后经配置层恢复、
//   历史已丢失，两种情形无法区分，都不能说成"从未上报"；
// - observed_minutes = 0：窗口内没有 hub 能观测的分钟，分母为零，算出 NaN/Infinity 没有意义；
// - 否则覆盖率 = observed_reported ÷ observed，未知分钟 = eligible − observed（守恒式
//   0 ≤ observed_reported ≤ observed ≤ eligible 由 hub 汇总保证），未知不计入分母。
export type CoverageView =
  | { kind: "absent" }
  | { kind: "no-start" }
  | { kind: "no-observed" }
  | { kind: "rate"; percent: string; unknown: string | null };

export function coverageView(summary: CoverageSummary | undefined): CoverageView {
  if (!summary) return { kind: "absent" };
  if (summary.coverageStart === undefined) return { kind: "no-start" };
  if (summary.observedMinutes === 0n) return { kind: "no-observed" };
  const percent = ((Number(summary.observedReportedMinutes) / Number(summary.observedMinutes)) * 100).toFixed(1);
  const unknownMinutes = summary.eligibleMinutes - summary.observedMinutes;
  return { kind: "rate", percent, unknown: unknownMinutes > 0n ? spanText(unknownMinutes) : null };
}

// 未知时长按量级选单位：满 2 天按天、满 2 小时按小时、其余按分钟；非整除保留一位小数。
function spanText(minutes: bigint): string {
  const m = Number(minutes);
  if (m >= 2 * 1440) return `${trim(m / 1440)} 天`;
  if (m >= 2 * 60) return `${trim(m / 60)} 小时`;
  return `${m} 分钟`;
}

const trim = (v: number) => (Number.isInteger(v) ? String(v) : v.toFixed(1));
