import type { DescMethodUnary } from "@bufbuild/protobuf";
import { useQuery } from "@connectrpc/connect-query";
import { type ReactNode, useEffect, useMemo, useState } from "react";
import { useRetained } from "../api/useRetained";
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
  // 窗口右端每分钟前进一次、切换 range 都会换查询键；换键期间或失败时图表与“级别…”标签不能都消失，也不能
  // 沿用别的节点的数据。指标图与“级别…”标签由 metrics.data 派生，两张探测图由 probes.data 派生：data 回到
  // undefined 时它们都不再渲染。用 useRetained 沿用本节点上一份成功数据直到当前键取到自己的数据为止，失败
  // 只由 error 表达；不用 keepPreviousData——它只在挂起期间补位，请求一失败 data 就回到 undefined。identity
  // 传 nodeId：切到另一个节点时丢掉上一个节点的沿用值，否则新节点还没有自己的数据时会把上一个节点的图表当成
  // 这个节点显示。
  const metrics = useRetained(useQuery(methods.queryMetrics, request, { enabled }), nodeId);
  const probes = useRetained(useQuery(methods.queryProbes, request, { enabled }), nodeId);
  // useRetained 的返回值是一个不带判别字段的普通对象（不像 useQuery 按 status 分支的联合类型），narrow
  // 一次 metrics.data 只窄化这一次属性访问，不会像窄化整个联合类型变量那样带进下面 map 的回调里；
  // 这里先取到本地变量，回调里用的是这个已经排除 undefined 的变量，不是再次访问 metrics.data。
  const charts = useMemo(() => {
    const data = metrics.data;
    return data ? PANELS.map((p) => ({ ...p, data: toAligned(data, p.names, from, to), unit: p.unit ?? unitOf(data, p.names[0]) })) : [];
  }, [metrics.data, from, to]);
  // 标签随序列下发（任务当前的种类与目标），与数据同一次响应到达，不另查任务列表。
  const probeCharts = useMemo(() => {
    if (!probes.data) return [];
    const ids = taskIdsOf(probes.data);
    const labels = seriesLabels(probes.data.series);
    return PROBE_PANELS.map((p) => ({ ...p, labels, data: toProbeAligned(probes.data!, ids, from, to, p.value) }));
  }, [probes.data, from, to]);
  // stale 只说“这份数据不是当前查询键自己的”：窗口右端每分钟前进一次也会换键，请求还没回来的这一小段
  // 时间同样是 stale，但沿用的还是同一个 range，只晚了不到一分钟，不该报成“看错窗口”。这里另记一下
  // “当前沿用值最后一次确认属于哪个 range”：只有 metrics/probes 沿用中、且沿用值所属的 range 与当前
  // 选中的 range 不同——也就是真的切换过 range、新 range 还没有自己的数据——才算“看错窗口”。
  const [metricsRange, setMetricsRange] = useState(range);
  if (!metrics.stale && metrics.data !== undefined && metricsRange !== range) setMetricsRange(range);
  const [probesRange, setProbesRange] = useState(range);
  if (!probes.stale && probes.data !== undefined && probesRange !== range) setProbesRange(range);
  const rangeStale = (metrics.stale && metricsRange !== range) || (probes.stale && probesRange !== range);
  return { range, setRange, metrics, probes, charts, probeCharts, rangeStale };
}

export type HistoryState = ReturnType<typeof useHistory>;

export function RangePicker({ history }: { history: HistoryState }) {
  const { range, setRange, metrics, rangeStale } = history;
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
      {/* rangeStale 排除了“同一个 range 里晚了不到一分钟”的情况，只在真的换过 range 还没等到新 range
          自己的数据时才出现；不点出来，这里显示的级别与图表会被当成当前选中 range 的结果看。 */}
      {rangeStale && <span className="muted">图表还不是 {range.label} 窗口的结果，取到之后会更新</span>}
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
