import { useQuery } from "@connectrpc/connect-query";
import { Link } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
import { Bar, Missing, ratio } from "../components/Bar";
import { PublicService, type PublicNode } from "../gen/probe/v1/public_pb";
import { ago, bytes, duration, percent } from "../lib/format";
import { POLL_MS } from "../lib/poll";

export function PublicOverview() {
  const snap = useQuery(PublicService.method.getSnapshot, {}, { refetchInterval: POLL_MS });
  const gate = queryGate(snap);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const now = Number(gate.data.now);
  const online = gate.data.nodes.filter((n) => n.online).length;
  return (
    <section>
      <header className="row">
        <h1>节点</h1>
        <span className="muted">{online} / {gate.data.nodes.length} 在线</span>
      </header>
      {gate.banner}
      {gate.data.nodes.length === 0 && <p className="muted">没有公开的节点。</p>}
      <div className="cards">
        {gate.data.nodes.map((n) => <NodeCard key={String(n.id)} node={n} now={now} />)}
      </div>
    </section>
  );
}

// 卡片内容按 §10：名称、在线、系统与架构、CPU、内存、磁盘、网速、运行时长、本周期流量。
function NodeCard({ node, now }: { node: PublicNode; now: number }) {
  const m = node.metrics;
  const f = node.facts;
  return (
    <article className={`card node-card ${node.online ? "online" : "offline"}`} aria-label={node.name}>
      <h2>
        <span className={`dot ${node.online ? "ok" : "bad"}`} role="img" aria-label={node.online ? "在线" : "离线"} />
        <Link to={`/nodes/${node.id}`}>{node.name}</Link>
      </h2>
      <p className="muted">{f ? [f.os, f.arch].filter(Boolean).join(" · ") : "系统未知"}</p>
      <dl className="facts">
        <dt>CPU</dt>
        <dd>{m?.cpuPct !== undefined ? <Bar value={m.cpuPct} label={percent(m.cpuPct)} /> : <Missing />}</dd>
        <dt>内存</dt>
        <dd>{m?.memUsed !== undefined && m.memTotal ? <Bar value={ratio(m.memUsed, m.memTotal)} label={`${bytes(m.memUsed)} / ${bytes(m.memTotal)}`} /> : <Missing />}</dd>
        <dt>磁盘</dt>
        <dd>{m?.diskUsed !== undefined && m.diskTotal ? <Bar value={ratio(m.diskUsed, m.diskTotal)} label={`${bytes(m.diskUsed)} / ${bytes(m.diskTotal)}`} /> : <Missing />}</dd>
        <dt>网速</dt>
        <dd>{m?.netRxBps !== undefined && m.netTxBps !== undefined ? `↓ ${bytes(m.netRxBps)}/s ↑ ${bytes(m.netTxBps)}/s` : <Missing />}</dd>
        <dt>运行</dt>
        <dd>{m?.uptimeS !== undefined ? duration(m.uptimeS) : <Missing />}</dd>
        <dt>本周期</dt>
        <dd>{node.traffic ? `↓ ${bytes(node.traffic.periodRx)} ↑ ${bytes(node.traffic.periodTx)}` : <Missing />}</dd>
      </dl>
      <p className="muted">{node.lastSeenAt !== undefined ? `最近上报 ${ago(node.lastSeenAt, now)}` : "从未上报"}</p>
    </article>
  );
}
