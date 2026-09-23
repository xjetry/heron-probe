import { useQuery } from "@connectrpc/connect-query";
import { Link } from "react-router";
import { AdminService, type NodeStatus } from "../gen/probe/v1/admin_pb";
import { ago, bytes, percent } from "../lib/format";
import { errorText } from "../api/auth";

// 实时视图靠轮询；hub 的上报间隔不会更短，2 秒是让"刚上报"尽快可见的取值。
export const POLL_MS = 2000;

export function Overview() {
  const snap = useQuery(AdminService.method.getSnapshot, {}, { refetchInterval: POLL_MS });
  if (snap.isPending) return <p className="muted">加载中…</p>;
  if (!snap.data) return <p role="alert" className="error">{errorText(snap.error)}</p>;
  const now = Number(snap.data.now);
  const online = snap.data.nodes.filter((n) => n.online).length;
  return (
    <section>
      <header className="row">
        <h1>总览</h1>
        <span className="muted">{online} / {snap.data.nodes.length} 在线</span>
      </header>
      {snap.error && <p role="alert" className="error">{errorText(snap.error)}</p>}
      {snap.data.nodes.length === 0 && (
        <p className="muted">
          还没有节点。去 <Link to="/nodes">节点</Link> 页创建，或开一个 <Link to="/register">注册窗口</Link>。
        </p>
      )}
      <div className="table-scroll" role="region" aria-label="节点实时读数" tabIndex={0}>
        <table className="nodes">
          <thead>
            <tr><th>节点</th><th>CPU</th><th>内存</th><th>磁盘</th><th>负载</th><th>网络</th><th>最近上报</th></tr>
          </thead>
          <tbody>
            {snap.data.nodes.map((n) => <NodeRow key={String(n.id)} node={n} now={now} />)}
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
        <Link to={`/nodes/${node.id}`}>{node.name}</Link>
      </td>
      <td>{m?.cpuPct !== undefined ? <Bar value={m.cpuPct} label={percent(m.cpuPct)} /> : <Missing />}</td>
      <td>{m?.memUsed !== undefined && m.memTotal ? <Bar value={ratio(m.memUsed, m.memTotal)} label={`${bytes(m.memUsed)} / ${bytes(m.memTotal)}`} /> : <Missing />}</td>
      <td>{m?.diskUsed !== undefined && m.diskTotal ? <Bar value={ratio(m.diskUsed, m.diskTotal)} label={`${bytes(m.diskUsed)} / ${bytes(m.diskTotal)}`} /> : <Missing />}</td>
      <td>{m?.load1 !== undefined ? `${m.load1.toFixed(2)} / ${m.load5?.toFixed(2) ?? "–"} / ${m.load15?.toFixed(2) ?? "–"}` : <Missing />}</td>
      <td>{m?.netRxBps !== undefined && m.netTxBps !== undefined ? `↓ ${bytes(m.netRxBps)}/s ↑ ${bytes(m.netTxBps)}/s` : <Missing />}</td>
      <td className="muted">{node.lastSeenAt !== undefined ? ago(node.lastSeenAt, now) : "从未"}</td>
    </tr>
  );
}

function ratio(used: bigint, total: bigint): number {
  return (Number(used) / Number(total)) * 100;
}

// 无读数与 0 是两个事实：缺失的字段显示为破折号，不画成 0。
function Missing() {
  return <span className="muted" aria-label="无读数">–</span>;
}

export function Bar({ value, label }: { value: number; label: string }) {
  const v = Math.max(0, Math.min(100, value));
  return (
    <div className="bar" role="meter" aria-valuenow={Math.round(v)} aria-valuemin={0} aria-valuemax={100} aria-label={label}>
      <div className="fill" style={{ width: `${v}%` }} />
      <span>{label}</span>
    </div>
  );
}
