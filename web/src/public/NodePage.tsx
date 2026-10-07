import { useQuery } from "@connectrpc/connect-query";
import { Link, useParams } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
import { CountryBadge } from "../components/CountryBadge";
import { HistoryCharts, RangePicker, useHistory, type HistoryMethods, type HistoryState } from "../components/History";
import { PublicService, type PublicNode } from "../gen/heron/v1/public_pb";
import { ProbeKind } from "../gen/heron/v1/types_pb";
import { expired, expiryText, priceText } from "../lib/billing";
import { POLL_MS } from "../lib/poll";
import { seriesLabels } from "../lib/probes";

const PUBLIC_HISTORY: HistoryMethods = { queryMetrics: PublicService.method.queryMetrics, queryProbes: PublicService.method.queryProbes };

// 只给带来了种类与目标的任务入口。未标注的序列是已删除或已撤下的任务，对比 List 对它们没有可画的节点。
function ProbeLinks({ history }: { history: HistoryState }) {
  const series = history.probes.data?.series ?? [];
  const labels = seriesLabels(series);
  const links = series.flatMap((seriesItem, i) => seriesItem.kind === ProbeKind.UNSPECIFIED ? [] : [{ id: seriesItem.taskId, label: labels[i] }]);
  if (links.length === 0) return null;
  return (
    <nav aria-label="各节点对比" className="compare-links">
      {links.map((link) => <Link key={String(link.id)} to={`/probes/${link.id}`}>各节点对比：{link.label}</Link>)}
    </nav>
  );
}

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
  const price = priceText(node.billing);
  const expiry = expiryText(node.billing);
  return (
    <section>
      {errorBanner(error, history.metrics.error, history.probes.error)}
      <header className="row detail-header">
        <h1>{node.name}{node.country && <>{" "}<CountryBadge code={node.country} /></>}</h1>
        <RangePicker history={history} />
      </header>
      {node.publicRemark && <p className="node-remark">{node.publicRemark}</p>}
      <HistoryCharts history={history} noProbes={<p className="muted">窗口内没有探测结果。</p>} probeFooter={<ProbeLinks history={history} />} />
      {/* 静态信息卡：主机信息从未上报时缺失，费用与到期填了才显示（§10），三者都没有时不画这张卡。 */}
      {(node.facts || price || expiry) && (
        <dl className="card facts">
          {node.facts && (
            <>
              <dt>系统</dt><dd>{node.facts.os}</dd>
              <dt>架构</dt><dd>{node.facts.arch}</dd>
              <dt>CPU</dt><dd>{node.facts.cpuModel} × {node.facts.cpuCores}</dd>
              <dt>虚拟化</dt><dd>{node.facts.virtualization || "无 / 未知"}</dd>
            </>
          )}
          {price && <><dt>费用</dt><dd>{price}</dd></>}
          {expiry && <><dt>到期</dt><dd className={expired(node.billing) ? "error" : undefined}>{expiry}</dd></>}
        </dl>
      )}
    </section>
  );
}
