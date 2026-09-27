import { useQuery } from "@connectrpc/connect-query";
import { Link, useParams } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
import { HistoryCharts, RangePicker, useHistory, type HistoryMethods } from "../components/History";
import { PublicService } from "../gen/probe/v1/public_pb";
import { expired, expiryText, priceText } from "../lib/billing";
import { POLL_MS } from "../lib/poll";

const PUBLIC_HISTORY: HistoryMethods = { queryMetrics: PublicService.method.queryMetrics, queryProbes: PublicService.method.queryProbes };

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
  const price = priceText(node.billing);
  const expiry = expiryText(node.billing);
  return (
    <section>
      {errorBanner(snap.error, history.metrics.error, history.probes.error)}
      <header className="row detail-header">
        <h1>{node.name}</h1>
        <RangePicker history={history} />
      </header>
      <HistoryCharts history={history} noProbes={<p className="muted">窗口内没有探测结果。</p>} />
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
