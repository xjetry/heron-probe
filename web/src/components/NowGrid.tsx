import type { ReactNode } from "react";
import { Missing, ratio } from "./Bar";
import { bytes, duration, percent } from "../lib/format";
import { expiryLevel } from "../lib/status";

export type NowMetrics = { cpuPct?: number; memUsed?: bigint; memTotal?: bigint; diskUsed?: bigint; diskTotal?: bigint; netRxBps?: bigint; netTxBps?: bigint; uptimeS?: bigint };

function Now({ label, children }: { label: string; children: ReactNode }) {
  return <div className="now-cell" role="group" aria-label={label}><dt>{label}</dt><dd className="num">{children}</dd></div>;
}

// 节点页首屏六个现值格（设计 §3.2 / §4.3）：公开页与管理详情同一份。入参是结构类型，两端的快照条目都满足，
// 本文件不 import 任何生成代码，公开包的模块图里不会因此多出管理服务。
export function NowGrid({ metrics: m, daysLeft }: { metrics: NowMetrics | undefined; daysLeft: number | undefined }) {
  return (
    <dl className="now-grid">
      <Now label="CPU">{m?.cpuPct !== undefined ? percent(m.cpuPct) : <Missing />}</Now>
      <Now label="内存">{m?.memUsed !== undefined && m.memTotal ? percent(ratio(m.memUsed, m.memTotal)) : <Missing />}</Now>
      <Now label="磁盘">{m?.diskUsed !== undefined && m.diskTotal ? percent(ratio(m.diskUsed, m.diskTotal)) : <Missing />}</Now>
      <Now label="网络">{m?.netRxBps !== undefined && m.netTxBps !== undefined ? `↓ ${bytes(m.netRxBps)}/s ↑ ${bytes(m.netTxBps)}/s` : <Missing />}</Now>
      <Now label="运行时长">{m?.uptimeS !== undefined ? duration(m.uptimeS) : <Missing />}</Now>
      <Now label="剩余天数">{daysLeft !== undefined ? <span data-level={expiryLevel(daysLeft)}>{daysLeft < 0 ? `已过期 ${-daysLeft} 天` : `${daysLeft} 天`}</span> : <Missing />}</Now>
    </dl>
  );
}
