import type { DescMethodUnary } from "@bufbuild/protobuf";
import { useQuery } from "@connectrpc/connect-query";
import { keepPreviousData } from "@tanstack/react-query";
import { type ReactNode, useEffect, useMemo, useState } from "react";
import type { QueryMetricsRequestSchema, QueryMetricsResponseSchema, QueryProbesRequestSchema, QueryProbesResponseSchema } from "../gen/probe/v1/query_pb";
import { lossPercent, rttMeanMs, seriesLabels, taskIdsOf, toProbeAligned, type ProbeValue } from "../lib/probes";
import { toAligned, unitOf } from "../lib/series";
import { Chart } from "./Chart";

const RANGES = [
  { label: "1h", seconds: 3600 },
  { label: "6h", seconds: 6 * 3600 },
  { label: "24h", seconds: 86400 },
  { label: "7d", seconds: 7 * 86400 },
  { label: "30d", seconds: 30 * 86400 },
];

// 每个面板画哪些指标；名字与 hub 的描述表一致，单位随数据来。可加量（字节增量）以速率
// 作图，单位由面板指定：数据里的 bytes 是一个点内的总和，图上要的是 bytes/s。
const PANELS: { title: string; names: string[]; unit?: string }[] = [
  { title: "CPU", names: ["cpu"] },
  { title: "内存 / 交换", names: ["mem_used", "swap_used"] },
  { title: "磁盘", names: ["disk_used"] },
  { title: "负载（1 分钟）", names: ["load1"] },
  { title: "连接数", names: ["tcp", "udp"] },
  { title: "进程数", names: ["procs"] },
  { title: "网络", names: ["rx_bytes", "tx_bytes"], unit: "bytes/s" },
];

// 探测图两张：丢包率与 RTT 均值，每个任务一条线。单位不随数据来——探测样本没有 unit 字段，
// 两种量各自固定。
const PROBE_PANELS: { title: string; unit: string; value: ProbeValue }[] = [
  { title: "探测 · 丢包率", unit: "percent", value: lossPercent },
  { title: "探测 · RTT 均值", unit: "ms", value: rttMeanMs },
];

// 窗口右端每分钟前进一次：历史行本来就按分钟产生，更频繁的刷新看不到新东西。
const REFRESH_MS = 60_000;

// 两族历史查询在管理与公开两个服务上各有一份，请求与响应类型相同（query.proto）。图表按此共用，
// 调用方只决定查哪个服务；本文件不引用任何服务的生成代码，公开页因此能用它。
export type HistoryMethods = {
  queryMetrics: DescMethodUnary<typeof QueryMetricsRequestSchema, typeof QueryMetricsResponseSchema>;
  queryProbes: DescMethodUnary<typeof QueryProbesRequestSchema, typeof QueryProbesResponseSchema>;
};

export function useHistory(methods: HistoryMethods, nodeId: bigint, enabled: boolean) {
  const [range, setRange] = useState(RANGES[2]);
  const [to, setTo] = useState(() => Math.floor(Date.now() / 1000) + 60);
  useEffect(() => {
    const t = setInterval(() => setTo(Math.floor(Date.now() / 1000) + 60), REFRESH_MS);
    return () => clearInterval(t);
  }, []);
  const from = to - range.seconds;
  const request = { nodeId, from: BigInt(from), to: BigInt(to), maxPoints: 1000 };
  const metrics = useQuery(methods.queryMetrics, request, { enabled, placeholderData: keepPreviousData });
  const probes = useQuery(methods.queryProbes, request, { enabled, placeholderData: keepPreviousData });
  const charts = useMemo(
    () => metrics.data ? PANELS.map((p) => ({ ...p, data: toAligned(metrics.data, p.names, from, to), unit: p.unit ?? unitOf(metrics.data, p.names[0]) })) : [],
    [metrics.data, from, to],
  );
  // 标签随序列下发（任务当前的种类与目标），与数据同一次响应到达，不另查任务列表。
  const probeCharts = useMemo(() => {
    if (!probes.data) return [];
    const ids = taskIdsOf(probes.data);
    const labels = seriesLabels(probes.data.series);
    return PROBE_PANELS.map((p) => ({ ...p, labels, data: toProbeAligned(probes.data!, ids, from, to, p.value) }));
  }, [probes.data, from, to]);
  return { range, setRange, metrics, probes, charts, probeCharts };
}

export type HistoryState = ReturnType<typeof useHistory>;

export function RangePicker({ history }: { history: HistoryState }) {
  const { range, setRange, metrics } = history;
  return (
    <>
      <nav aria-label="时间窗口">
        {RANGES.map((r) => (
          <button key={r.label} type="button" className={r.label === range.label ? "active" : "link"} onClick={() => setRange(r)} aria-pressed={r.label === range.label}>
            {r.label}
          </button>
        ))}
      </nav>
      {metrics.data && <span className="muted">级别 {metrics.data.level}，每点 {metrics.data.stepS}s</span>}
    </>
  );
}

// noProbes 是窗口内没有探测结果时的说明：面板给出去任务页的链接，公开页只说明没有。
export function HistoryCharts({ history, noProbes }: { history: HistoryState; noProbes: ReactNode }) {
  const { charts, probeCharts, probes } = history;
  return (
    <>
      <div className="grid">
        {charts.map((c) => (
          <div className="card" key={c.title}>
            <h2>{c.title}</h2>
            <Chart data={c.data} labels={c.names} unit={c.unit} />
          </div>
        ))}
      </div>
      {probes.data && probes.data.series.length === 0 && noProbes}
      {probes.data && probes.data.series.length > 0 && (
        <div className="grid">
          {probeCharts.map((c) => (
            <div className="card" key={c.title}>
              <h2>{c.title}</h2>
              <Chart data={c.data} labels={c.labels} unit={c.unit} />
            </div>
          ))}
        </div>
      )}
    </>
  );
}
