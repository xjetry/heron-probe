import { Link } from "react-router";
import { Bar, Missing, ratio } from "../components/Bar";
import { CountryBadge } from "../components/CountryBadge";
import { StatusBadge } from "../components/StatusBadge";
import type { PublicNode } from "../gen/heron/v1/public_pb";
import { priceText } from "../lib/billing";
import { ago, bytes, duration, percent } from "../lib/format";
import { expiryLevel, nodeStatus } from "../lib/status";
import { trafficDetail, trafficText } from "../lib/traffic";

export function NodeCard({ node }: { node: PublicNode; now: number }) {
  const status = nodeStatus(node);
  const m = node.metrics;
  const f = node.facts;
  const system = [f?.os, f?.virtualization, f?.arch].filter(Boolean).join(" · ") || "系统未知";
  const uptime = m?.uptimeS !== undefined ? `运行 ${duration(m.uptimeS)}` : null;
  const price = priceText(node.billing);
  const b = node.billing;
  const daysLeft = b?.daysLeft;
  return (
    <article className="node-card" aria-label={node.name} data-status={status}>
      <header className="node-card-head">
        <h2><Link to={`/nodes/${node.id}`} title={node.name}>{node.name}</Link></h2>
        {node.country && <CountryBadge code={node.country} />}
        <StatusBadge status={status} />
      </header>
      {node.publicRemark && <p className="node-remark">{node.publicRemark}</p>}
      <p className="node-card-meta muted">{uptime ? `${system} · ${uptime}` : system}</p>
      <div className="node-meters">
        <Meter label="CPU" value={m?.cpuPct} text={m?.cpuPct !== undefined ? percent(m.cpuPct) : undefined} />
        <Meter label="内存" value={m?.memUsed !== undefined && m.memTotal ? ratio(m.memUsed, m.memTotal) : undefined} text={m?.memUsed !== undefined && m.memTotal ? `${bytes(m.memUsed)} / ${bytes(m.memTotal)}` : undefined} />
        <Meter label="磁盘" value={m?.diskUsed !== undefined && m.diskTotal ? ratio(m.diskUsed, m.diskTotal) : undefined} text={m?.diskUsed !== undefined && m.diskTotal ? `${bytes(m.diskUsed)} / ${bytes(m.diskTotal)}` : undefined} />
      </div>
      <p className="node-network num" role="group" aria-label="网络速率">
        ↓ {m?.netRxBps !== undefined ? `${bytes(m.netRxBps)}/s` : <Missing />} ↑ {m?.netTxBps !== undefined ? `${bytes(m.netTxBps)}/s` : <Missing />}
      </p>
      <footer className="node-card-foot">
        <div><span className="muted">本周期{node.traffic && ` · ${trafficDetail(node.traffic)}`}</span><span className="num">{node.traffic ? trafficText(node.traffic) : "–"}</span><span className="muted">费用</span><span className="num">{price || "–"}</span></div>
        <div>
          <span className="muted">到期</span><span className="num">{b?.expiresOn || "–"}</span>
          {daysLeft !== undefined && <span className="num" data-level={expiryLevel(daysLeft)}>{daysLeft < 0 ? `已过期 ${-daysLeft} 天` : `剩 ${daysLeft} 天`}</span>}
        </div>
      </footer>
    </article>
  );
}

// 无读数与 0 是两个事实：缺失不画条，画一条虚线轨。
function Meter({ label, value, text }: { label: string; value?: number; text?: string }) {
  return (
    <div className="node-meter">
      <span className="muted">{label}</span>
      {value !== undefined ? <Bar thin value={value} label={`${label} ${text ?? percent(value)}`} /> : <span className="meter-missing" aria-hidden="true" />}
      <span className="num">{text ?? <Missing />}</span>
    </div>
  );
}

// 卡片网格 + 折叠的离线表。传入的 nodes 已按调用方的排序；折叠表保持同一顺序。
export function CardGrid({ nodes, now }: { nodes: readonly PublicNode[]; now: number }) {
  const live = nodes.filter((n) => { const s = nodeStatus(n); return s === "online" || s === "maintenance"; });
  const rest = nodes.filter((n) => !live.includes(n));
  return (
    <>
      <div className="cards">
        {live.map((n) => <NodeCard key={String(n.id)} node={n} now={now} />)}
      </div>
      {rest.length > 0 && (
        <details className="folded-nodes">
          <summary>离线与从未上报 · {rest.length}</summary>
          <table className="compact-table">
            <thead><tr><th>名称</th><th>地区</th><th>状态</th><th>最后上报</th></tr></thead>
            <tbody>
              {rest.map((n) => (
                <tr key={String(n.id)}>
                  <td><Link to={`/nodes/${n.id}`}>{n.name}</Link></td>
                  <td>{n.country && <CountryBadge code={n.country} />}</td>
                  <td><StatusBadge status={nodeStatus(n)} /></td>
                  <td className="num">{n.lastSeenAt !== undefined ? ago(n.lastSeenAt, now) : "–"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </details>
      )}
    </>
  );
}
