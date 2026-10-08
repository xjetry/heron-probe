import { useQuery } from "@connectrpc/connect-query";
import { Link, useParams } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
import { NowGrid } from "../components/NowGrid";
import { CountryBadge } from "../components/CountryBadge";
import { MetricCharts, ProbeTaskCharts, RangePicker, useHistory, type HistoryMethods } from "../components/History";
import { StatusBadge } from "../components/StatusBadge";
import { PublicService, type PublicNode } from "../gen/heron/v1/public_pb";
import { ago } from "../lib/format";
import { POLL_MS } from "../lib/poll";
import { nodeStatus } from "../lib/status";

const PUBLIC_HISTORY: HistoryMethods = { queryMetrics: PublicService.method.queryMetrics, queryProbes: PublicService.method.queryProbes };

// 节点页（设计 §3.2）：首屏是实时状态头与六个现值格，其下时间窗口、指标图、每任务一张 RTT 图、系统信息卡。
// 公开页不出现 IP、主机名、内核、agent 版本：PublicFacts 已 reserved 这些字段，前端不另有来源。
export function NodePage() {
  const { id } = useParams();
  const validId = /^\d+$/.test(id ?? "");
  const nodeId = validId ? BigInt(id!) : 0n;
  const snap = useQuery(PublicService.method.getSnapshot, {}, { enabled: validId, refetchInterval: POLL_MS });
  const node = snap.data?.nodes.find((n) => n.id === nodeId);
  const missing = <p role="alert" className="error">节点 {id} 不存在或未公开。<Link to="/">返回总览</Link></p>;
  if (!validId) return missing;
  const gate = queryGate(snap);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  if (!node) return missing;
  // 节点准入与 hub now 都由这份快照提供；缺任一项都不能开始历史查询。
  return <NodeContent node={node} now={Number(gate.data.now)} error={snap.error} />;
}

function NodeContent({ node, now, error }: { node: PublicNode; now: number; error: unknown }) {
  const history = useHistory(PUBLIC_HISTORY, node.id, now);
  const m = node.metrics;
  const f = node.facts;
  const daysLeft = node.billing?.daysLeft;
  return (
    <section className="node-page">
      {errorBanner(error, history.metrics.error, history.probes.error)}
      <header className="node-head">
        <div className="node-head-title">
          <h1>{node.name}</h1>
          {node.country && <CountryBadge code={node.country} />}
          <StatusBadge status={nodeStatus(node)} detail={node.lastSeenAt !== undefined ? `最近上报 ${ago(node.lastSeenAt, now)}` : undefined} />
        </div>
        {node.publicRemark && <p className="node-remark">{node.publicRemark}</p>}
        {node.tags.length > 0 && <ul className="tag-chips">{node.tags.map((tag) => <li key={tag} className="chip">{tag}</li>)}</ul>}
        <NowGrid metrics={m} daysLeft={daysLeft} />
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
