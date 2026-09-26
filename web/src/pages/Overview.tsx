import { useQuery } from "@connectrpc/connect-query";
import { Link } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
import { AdminService, type NodeStatus } from "../gen/probe/v1/admin_pb";
import { ago, bytes, percent } from "../lib/format";
import { withId } from "../lib/ids";
import { POLL_MS } from "../lib/poll";
import { Bar, Missing, ratio } from "../components/Bar";

export function Overview() {
  const snap = useQuery(AdminService.method.getSnapshot, {}, { refetchInterval: POLL_MS });
  const gate = queryGate(snap);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const now = Number(gate.data.now);
  const online = gate.data.nodes.filter((n) => n.online).length;
  return (
    <section>
      <header className="row">
        <h1>总览</h1>
        <span className="muted">{online} / {gate.data.nodes.length} 在线</span>
      </header>
      {gate.banner}
      {gate.data.nodes.length === 0 && (
        <p className="muted">
          还没有节点。去 <Link to="/nodes">节点</Link> 页创建，或开一个 <Link to="/register">注册窗口</Link>。
        </p>
      )}
      <div className="table-scroll" role="region" aria-label="节点实时读数" tabIndex={0}>
        <table className="nodes">
          <thead>
            <tr><th>节点</th><th>CPU</th><th>内存</th><th>磁盘</th><th>负载</th><th>网络</th><th>本周期 ↓/↑</th><th>最近上报</th></tr>
          </thead>
          <tbody>
            {gate.data.nodes.map((n) => <NodeRow key={String(n.id)} node={n} now={now} />)}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function NodeRow({ node, now }: { node: NodeStatus; now: number }) {
  const m = node.metrics;
  return (
    <tr className={node.online ? "online" : "offline"}>
      <td>
        <span className={`dot ${node.online ? "ok" : "bad"}`} role="img" aria-label={node.online ? "在线" : "离线"} />
        <Link to={`/nodes/${node.id}`} aria-label={withId(node.name, node.id)}>{node.name}</Link>
      </td>
      <td>{m?.cpuPct !== undefined ? <Bar value={m.cpuPct} label={percent(m.cpuPct)} /> : <Missing />}</td>
      <td>{m?.memUsed !== undefined && m.memTotal ? <Bar value={ratio(m.memUsed, m.memTotal)} label={`${bytes(m.memUsed)} / ${bytes(m.memTotal)}`} /> : <Missing />}</td>
      <td>{m?.diskUsed !== undefined && m.diskTotal ? <Bar value={ratio(m.diskUsed, m.diskTotal)} label={`${bytes(m.diskUsed)} / ${bytes(m.diskTotal)}`} /> : <Missing />}</td>
      <td>{m?.load1 !== undefined ? `${m.load1.toFixed(2)} / ${m.load5?.toFixed(2) ?? "–"} / ${m.load15?.toFixed(2) ?? "–"}` : <Missing />}</td>
      <td>{m?.netRxBps !== undefined && m.netTxBps !== undefined ? `↓ ${bytes(m.netRxBps)}/s ↑ ${bytes(m.netTxBps)}/s` : <Missing />}</td>
      <td>{node.traffic ? `↓ ${bytes(node.traffic.periodRx)} ↑ ${bytes(node.traffic.periodTx)}` : <Missing />}</td>
      <td className="muted">{node.lastSeenAt !== undefined ? ago(node.lastSeenAt, now) : "从未"}</td>
    </tr>
  );
}
