import type { ReactNode } from "react";
import { Link } from "react-router";
import { Bar, Missing, ratio } from "../components/Bar";
import { CountryBadge } from "../components/CountryBadge";
import type { PublicNode } from "../gen/heron/v1/public_pb";
import { expired, expiryText, priceText } from "../lib/billing";
import { ago, bytes, duration, percent } from "../lib/format";

export function NodeCard({ node, now }: { node: PublicNode; now: number }) {
  const m = node.metrics;
  const f = node.facts;
  const price = priceText(node.billing);
  const expiry = expiryText(node.billing);
  const system = [f?.os, f?.virtualization, f?.arch].filter(Boolean).join(" · ") || "系统未知";
  return (
    <article className={`card node-card ${node.online ? "online" : "offline"}`} aria-label={node.name}>
      <header className="node-card-heading">
        <div className="node-card-identity">
          <h2><Link to={`/nodes/${node.id}`} title={node.name}>{node.name}</Link></h2>
          {node.country && <CountryBadge code={node.country} />}
        </div>
        <span className={`node-status ${node.online ? "is-online" : "is-offline"}`}>
          <span className={`dot ${node.online ? "ok" : "bad"}`} role="img" aria-label={node.online ? "在线" : "离线"} />
          <span aria-hidden="true">{node.online ? "在线" : "离线"}</span>
        </span>
      </header>
      <div className="node-card-meta">
        <span title={system}>{system}</span>
        <span>{m?.uptimeS !== undefined ? `运行 ${duration(m.uptimeS)}` : "运行时长未知"}</span>
      </div>
      <div className="node-resources">
        <Resource label={f?.cpuCores ? `CPU ${f.cpuCores} 核` : "CPU"} value={m?.cpuPct}
          meterLabel={m?.cpuPct !== undefined ? percent(m.cpuPct) : undefined}
          detail={<span aria-label="负载 1 / 5 / 15 分钟">{[m?.load1, m?.load5, m?.load15].map((value, index) => <span key={index}>{index > 0 && " / "}{value === undefined ? <Missing /> : value.toFixed(2)}</span>)}</span>} />
        <CapacityResource label="内存" used={m?.memUsed} total={m?.memTotal} />
        <CapacityResource label="磁盘" used={m?.diskUsed} total={m?.diskTotal} />
        <div className="node-resource node-traffic">
          <span className="node-resource-label">本周期流量</span>
          <strong>{node.traffic ? bytes(node.traffic.periodRx + node.traffic.periodTx) : <Missing />}</strong>
          <span className="node-resource-detail">下载 + 上传</span>
        </div>
      </div>
      <div className="node-network">
        <Network direction="下载" arrow="↓" rate={m?.netRxBps} total={node.traffic?.periodRx} />
        <Network direction="上传" arrow="↑" rate={m?.netTxBps} total={node.traffic?.periodTx} />
      </div>
      {(price || expiry) && <dl className="node-billing">
        {price && <div><dt>费用</dt><dd>{price}</dd></div>}
        {expiry && <div><dt>到期</dt><dd className={expired(node.billing) ? "error" : node.billing?.daysLeft !== undefined && node.billing.daysLeft <= 7 ? "warn" : undefined}>{expiry}</dd></div>}
      </dl>}
      <footer className="node-card-footer">
        <span>{node.lastSeenAt !== undefined ? `最近上报 ${ago(node.lastSeenAt, now)}` : "从未上报"}</span>
        {!node.online && node.metrics && <span className="stale-reading">最后读数</span>}
        <span className="node-details-hint" aria-hidden="true">查看详情 ↗</span>
      </footer>
    </article>
  );
}

function Resource({ label, value, meterLabel, detail }: { label: string; value?: number; meterLabel?: string; detail: ReactNode }) {
  return <div className="node-resource">
    <div className="node-resource-heading"><span className="node-resource-label">{label}</span><strong>{value !== undefined ? percent(value) : <Missing />}</strong></div>
    {value !== undefined ? <Bar value={value} label={meterLabel ?? percent(value)} /> : <div className="resource-missing-track" aria-hidden="true" />}
    <div className="node-resource-detail">{detail}</div>
  </div>;
}

function CapacityResource({ label, used, total }: { label: string; used?: bigint; total?: bigint }) {
  const measured = used !== undefined && total !== undefined && total > 0n;
  const detail = measured ? `${bytes(used)} / ${bytes(total)}` : undefined;
  return <Resource label={label} value={measured ? ratio(used, total) : undefined} meterLabel={detail} detail={detail ?? <Missing />} />;
}

function Network({ direction, arrow, rate, total }: { direction: string; arrow: string; rate?: bigint; total?: bigint }) {
  return <div className="network-direction" role="group" aria-label={direction}>
    <div className="network-rate"><span className="network-arrow" aria-hidden="true">{arrow}</span><span className="network-label">{direction}</span><strong>{rate !== undefined ? `${bytes(rate)}/s` : <Missing />}</strong></div>
    <div className="network-total">本周期 {total !== undefined ? bytes(total) : <Missing />}</div>
  </div>;
}
