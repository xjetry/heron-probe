import { useQuery } from "@connectrpc/connect-query";
import type { ReactNode } from "react";
import { Link, useParams } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
import { Missing, ratio } from "../components/Bar";
import { CountryBadge } from "../components/CountryBadge";
import { MetricCharts, ProbeTaskCharts, RangePicker, useHistory, type HistoryMethods } from "../components/History";
import { StatusBadge } from "../components/StatusBadge";
import { PublicService } from "../gen/heron/v1/public_pb";
import { ago, bytes, duration, percent } from "../lib/format";
import { POLL_MS } from "../lib/poll";
import { expiryLevel, nodeStatus } from "../lib/status";

const PUBLIC_HISTORY: HistoryMethods = { queryMetrics: PublicService.method.queryMetrics, queryProbes: PublicService.method.queryProbes };

function Now({ label, children }: { label: string; children: ReactNode }) {
  return <div className="now-cell" role="group" aria-label={label}><dt>{label}</dt><dd className="num">{children}</dd></div>;
}

// 节点页（设计 §3.2）：首屏是实时状态头与六个现值格，其下时间窗口、指标图、每任务一张 RTT 图、系统信息卡。
// 公开页不出现 IP、主机名、内核、agent 版本：PublicFacts 已 reserved 这些字段，前端不另有来源。
export function NodePage() {
  const { id } = useParams();
  const validId = /^\d+$/.test(id ?? "");
  const nodeId = validId ? BigInt(id!) : 0n;
  const snap = useQuery(PublicService.method.getSnapshot, {}, { enabled: validId, refetchInterval: POLL_MS });
  const node = snap.data?.nodes.find((n) => n.id === nodeId);
  // 只在快照里有这个节点时查历史：未公开或不存在的节点，历史查询只会得到 NotFound。
  const history = useHistory(PUBLIC_HISTORY, nodeId, node !== undefined);
  const missing = <p role="alert" className="error">节点 {id} 不存在或未公开。<Link to="/">返回总览</Link></p>;
  if (!validId) return missing;
  const gate = queryGate(snap);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  if (!node) return missing;
  const now = Number(gate.data.now);
  const m = node.metrics;
  const f = node.facts;
  const daysLeft = node.billing?.daysLeft;
  return (
    <section className="node-page">
      {errorBanner(snap.error, history.metrics.error, history.probes.error)}
      <header className="node-head">
        <div className="node-head-title">
          <h1>{node.name}</h1>
          {node.country && <CountryBadge code={node.country} />}
          <StatusBadge status={nodeStatus(node)} detail={node.lastSeenAt !== undefined ? `最近上报 ${ago(node.lastSeenAt, now)}` : undefined} />
        </div>
        {node.publicRemark && <p className="node-remark">{node.publicRemark}</p>}
        {node.tags.length > 0 && <ul className="tag-chips">{node.tags.map((tag) => <li key={tag} className="chip">{tag}</li>)}</ul>}
        <dl className="now-grid">
          <Now label="CPU">{m?.cpuPct !== undefined ? percent(m.cpuPct) : <Missing />}</Now>
          <Now label="内存">{m?.memUsed !== undefined && m.memTotal ? percent(ratio(m.memUsed, m.memTotal)) : <Missing />}</Now>
          <Now label="磁盘">{m?.diskUsed !== undefined && m.diskTotal ? percent(ratio(m.diskUsed, m.diskTotal)) : <Missing />}</Now>
          <Now label="网络">{m?.netRxBps !== undefined && m.netTxBps !== undefined ? `↓ ${bytes(m.netRxBps)}/s ↑ ${bytes(m.netTxBps)}/s` : <Missing />}</Now>
          <Now label="运行时长">{m?.uptimeS !== undefined ? duration(m.uptimeS) : <Missing />}</Now>
          <Now label="剩余天数">{daysLeft !== undefined ? <span data-level={expiryLevel(daysLeft)}>{daysLeft < 0 ? `已过期 ${-daysLeft} 天` : `${daysLeft} 天`}</span> : <Missing />}</Now>
        </dl>
      </header>
      <header className="row detail-header">
        <RangePicker history={history} />
      </header>
      <MetricCharts history={history} />
      <ProbeTaskCharts history={history} noProbes={<p className="muted">窗口内没有探测结果。</p>} titleLink={(taskId, title) => <Link to={`/probes/${taskId}`}>{title}</Link>} />
      {/* 系统信息卡：主机信息从未上报时缺失，整张不画。 */}
      {f && (
        <dl className="card facts">
          <dt>系统</dt><dd>{f.os}</dd>
          <dt>架构</dt><dd>{f.arch}</dd>
          <dt>CPU</dt><dd>{f.cpuModel} × {f.cpuCores}</dd>
          <dt>虚拟化</dt><dd>{f.virtualization || "无 / 未知"}</dd>
        </dl>
      )}
    </section>
  );
}
