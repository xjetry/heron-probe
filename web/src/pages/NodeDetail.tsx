import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { keepPreviousData, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useMemo, useState } from "react";
import { Link, useParams } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
import { Chart } from "../components/Chart";
import { AdminService, type GetTrafficResponse } from "../gen/probe/v1/admin_pb";
import { toAligned, unitOf } from "../lib/series";
import { bytes } from "../lib/format";
import { errorText } from "../api/auth";
import { lossPercent, rttMeanMs, seriesLabels, taskIdsOf, toProbeAligned, type ProbeValue } from "../lib/probes";

export const RANGES = [
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

const REFRESH_MS = 60_000;
// 周期量随每次上报更新；流量卡以 10 秒节奏展示内存视图的变化，不依赖落盘刷出。
export const TRAFFIC_MS = 10_000;

export function NodeDetail() {
  const { id } = useParams();
  const validId = /^\d+$/.test(id ?? "");
  const nodeId = validId ? BigInt(id!) : 0n;
  const [range, setRange] = useState(RANGES[2]);
  // 窗口右端每分钟前进一次：历史行本来就按分钟产生，更频繁的刷新看不到新东西。
  const [to, setTo] = useState(() => Math.floor(Date.now() / 1000) + 60);
  useEffect(() => {
    const t = setInterval(() => setTo(Math.floor(Date.now() / 1000) + 60), REFRESH_MS);
    return () => clearInterval(t);
  }, []);
  const from = to - range.seconds;

  const nodes = useQuery(AdminService.method.listNodes, {}, { enabled: validId });
  const history = useQuery(AdminService.method.queryMetrics, { nodeId, from: BigInt(from), to: BigInt(to), maxPoints: 1000 }, {
    enabled: validId, placeholderData: keepPreviousData,
  });
  const probes = useQuery(AdminService.method.queryProbes, { nodeId, from: BigInt(from), to: BigInt(to), maxPoints: 1000 }, {
    enabled: validId, placeholderData: keepPreviousData,
  });
  // 流量与图表面向不同查询，各自降级；校正操作在卡片内保留自己的错误槽位。
  const traffic = useQuery(AdminService.method.getTraffic, {}, { enabled: validId, refetchInterval: TRAFFIC_MS });
  // 标签随序列下发（任务当前的种类与目标），与数据同一次响应到达，不另查任务列表。
  const probeCharts = useMemo(() => {
    if (!probes.data) return [];
    const ids = taskIdsOf(probes.data);
    const labels = seriesLabels(probes.data.series);
    return PROBE_PANELS.map((p) => ({ ...p, labels, data: toProbeAligned(probes.data!, ids, from, to, p.value) }));
  }, [probes.data, from, to]);
  const charts = useMemo(
    () => history.data ? PANELS.map((p) => ({ ...p, data: toAligned(history.data, p.names, from, to), unit: p.unit ?? unitOf(history.data, p.names[0]) })) : [],
    [history.data, from, to],
  );

  if (!validId) return <p role="alert" className="error">节点 {id} 不存在。<Link to="/">返回总览</Link></p>;
  const gate = queryGate(nodes);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const node = gate.data.nodes.find((n) => n.id === nodeId);
  if (!node) return <p role="alert" className="error">节点 {id} 不存在。<Link to="/">返回总览</Link></p>;
  return (
    <section>
      {errorBanner(nodes.error, history.error, probes.error, traffic.error)}
      <header className="row detail-header">
        <h1>{node.name}</h1>
        <Link to={`/events?node=${id}`}>告警事件</Link>
        <nav aria-label="时间窗口">
          {RANGES.map((r) => (
            <button key={r.label} type="button" className={r.label === range.label ? "active" : "link"} onClick={() => setRange(r)} aria-pressed={r.label === range.label}>
              {r.label}
            </button>
          ))}
        </nav>
        {history.data && <span className="muted">级别 {history.data.level}，每点 {history.data.stepS}s</span>}
      </header>
      <TrafficCard nodeId={nodeId} data={traffic.data} />
      <div className="grid">
        {charts.map((c) => (
          <div className="card" key={c.title}>
            <h2>{c.title}</h2>
            <Chart data={c.data} labels={c.names} unit={c.unit} />
          </div>
        ))}
      </div>
      {probes.data && probes.data.series.length === 0 && (
        <p className="muted">窗口内没有探测结果。<Link to="/probes">管理探测任务</Link></p>
      )}
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
      {node.facts && (
        <dl className="card facts">
          <dt>主机名</dt><dd>{node.facts.hostname}</dd>
          <dt>系统</dt><dd>{node.facts.os}</dd>
          <dt>内核</dt><dd>{node.facts.kernel}</dd>
          <dt>架构</dt><dd>{node.facts.arch}</dd>
          <dt>CPU</dt><dd>{node.facts.cpuModel} × {node.facts.cpuCores}</dd>
          <dt>虚拟化</dt><dd>{node.facts.virtualization || "无 / 未知"}</dd>
          <dt>agent</dt><dd>{node.facts.agentVersion}</dd>
          <dt>ICMP 探测</dt><dd>{node.facts.icmpAvailable ? "可用" : "不可用"}</dd>
        </dl>
      )}
    </section>
  );
}

const GIB = 2 ** 30;
const toGiB = (v: bigint) => (Number(v) / GIB).toFixed(2);
// 校正值以 GiB 输入，四舍五入到整字节；只有有限且落在 uint64 范围内的非负值才能提交。
const toBytes = (s: string): bigint | null => {
  const v = Number(s);
  const rounded = Math.round(v * GIB);
  if (!(s.trim() !== "" && Number.isFinite(v) && v >= 0 && Number.isFinite(rounded))) return null;
  const result = BigInt(rounded);
  return result <= (1n << 64n) - 1n ? result : null;
};

function TrafficCard({ nodeId, data }: { nodeId: bigint; data: GetTrafficResponse | undefined }) {
  const qc = useQueryClient();
  const [draft, setDraft] = useState<{ rx: string; tx: string } | null>(null);
  const adjust = useMutation(AdminService.method.adjustTraffic, {
    onSuccess: () => {
      setDraft(null);
      void qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.getTraffic, cardinality: "finite" }) });
      void qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.getSnapshot, cardinality: "finite" }) });
    },
  });
  const t = data?.nodes.find((n) => n.nodeId === nodeId)?.traffic;
  const timeZone = data?.timezone;
  if (!t) return null;
  // 编辑框首次出现时预填当前值；之后由用户输入驱动。
  const form = draft ?? { rx: toGiB(t.periodRx), tx: toGiB(t.periodTx) };
  const rx = toBytes(form.rx);
  const tx = toBytes(form.tx);
  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (rx !== null && tx !== null) adjust.mutate({ nodeId, periodRx: rx, periodTx: tx });
  };
  return (
    <div className="card">
      <h2>流量</h2>
      <dl className="facts">
        <dt>本周期</dt><dd>↓ {bytes(t.periodRx)} ↑ {bytes(t.periodTx)}</dd>
        <dt>总量</dt><dd>↓ {bytes(t.totalRx)} ↑ {bytes(t.totalTx)}</dd>
        <dt>周期起点</dt><dd>{new Date(Number(t.periodStart) * 1000).toLocaleString(undefined, { timeZone })}</dd>
        <dt>下次重置</dt><dd>{new Date(Number(t.nextResetAt) * 1000).toLocaleString(undefined, { timeZone })}（每月 {t.resetDay} 日，{timeZone}）</dd>
      </dl>
      <form onSubmit={onSubmit} className="row">
        <label>本周期下行 (GiB)<input value={form.rx} onChange={(e) => setDraft({ ...form, rx: e.target.value })} inputMode="decimal" /></label>
        <label>本周期上行 (GiB)<input value={form.tx} onChange={(e) => setDraft({ ...form, tx: e.target.value })} inputMode="decimal" /></label>
        <button type="submit" disabled={adjust.isPending || rx === null || tx === null}>校正本周期</button>
      </form>
      {adjust.error && <p role="alert" className="error">{errorText(adjust.error)}</p>}
    </div>
  );
}
