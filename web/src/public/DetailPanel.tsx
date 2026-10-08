import { useQuery } from "@connectrpc/connect-query";
import type { ReactNode } from "react";
import { Link } from "react-router";
import { Bar, Missing, ratio } from "../components/Bar";
import { CountryBadge } from "../components/CountryBadge";
import { Expiry } from "../components/Expiry";
import { Sparkline } from "../components/Sparkline";
import { StatusBadge } from "../components/StatusBadge";
import { PublicService, type PublicNode } from "../gen/heron/v1/public_pb";
import { priceText } from "../lib/billing";
import { ago, bytes, duration, percent } from "../lib/format";
import { toAligned } from "../lib/series";
import { nodeStatus } from "../lib/status";
import { trafficDetail, trafficText } from "../lib/traffic";

const HOUR_S = 3600;
// 请求最多 60 点；边界对齐 hub 时钟的整分钟，使同一分钟内的快照共用查询键。
const SPARK_POINTS = 60;

function Row({ label, children }: { label: string; children: ReactNode }) {
  return <div className="detail-row"><dt>{label}</dt><dd>{children}</dd></div>;
}

// 与墙上方块、卡片同一种细条：条只表示比例，读数写在条右侧，不压在条上。读屏名仍是「标签 读数」。
function Meter({ value, label, text }: { value: number; label: string; text: string }) {
  return <span className="detail-meter"><Bar thin value={value} label={`${label} ${text}`} /><span className="num">{text}</span></span>;
}

function Capacity({ label, used, total }: { label: string; used?: bigint; total?: bigint }) {
  if (used === undefined || total === undefined || total === 0n) return <Row label={label}><Missing /></Row>;
  return <Row label={label}><Meter value={ratio(used, total)} label={label} text={`${bytes(used)} / ${bytes(total)}`} /></Row>;
}

// 详情面板（设计 §3.1）：选中节点的全部公开字段；网络速率带最近 1 小时迷你线——这是公开总览唯一的历史查询，
// 只为选中的一个节点发（公开限流桶 60、每秒补 10，按卡片各发一次会把访客自己限掉）。
export function DetailPanel({ node, now }: { node: PublicNode; now: number }) {
  const status = nodeStatus(node);
  const m = node.metrics;
  const f = node.facts;
  const minute = Math.floor(now / 60) * 60;
  const from = minute - HOUR_S;
  const to = minute + 60;
  const history = useQuery(PublicService.method.queryMetrics, { nodeId: node.id, from: BigInt(from), to: BigInt(to), maxPoints: SPARK_POINTS }, { staleTime: 60_000 });
  const spark = history.data ? toAligned(history.data, [{ name: "rx_bytes", value: "sum-rate", label: "下行" }, { name: "tx_bytes", value: "sum-rate", label: "上行" }], from, to) : null;
  const rate = spark ? (spark[1] as (number | null)[]).map((rx, i) => {
    const tx = (spark[2] as (number | null)[])[i];
    return rx === null && tx === null ? null : (rx ?? 0) + (tx ?? 0);
  }) : null;
  const price = priceText(node.billing);
  return (
    <aside className="detail-panel" aria-label="节点详情">
      <header>
        <h2>{node.name}</h2>
        {node.country && <CountryBadge code={node.country} />}
        <StatusBadge status={status} detail={node.lastSeenAt !== undefined ? `最近上报 ${ago(node.lastSeenAt, now)}` : undefined} />
      </header>
      {node.publicRemark && <p className="node-remark">{node.publicRemark}</p>}
      {node.tags.length > 0 && <ul className="tag-chips">{node.tags.map((tag) => <li key={tag} className="chip">{tag}</li>)}</ul>}
      <dl className="detail-list">
        {f && <Row label="系统">{[f.os, f.virtualization, f.arch].filter(Boolean).join(" · ") || <Missing />}</Row>}
        {f && f.cpuModel && <Row label="CPU">{f.cpuModel} × {f.cpuCores}</Row>}
        <Row label="运行时长">{m?.uptimeS !== undefined ? `运行 ${duration(m.uptimeS)}` : <Missing />}</Row>
        <Row label="CPU 使用">{m?.cpuPct !== undefined ? <Meter value={m.cpuPct} label="CPU" text={percent(m.cpuPct)} /> : <Missing />}</Row>
        <Capacity label="内存" used={m?.memUsed} total={m?.memTotal} />
        <Capacity label="交换" used={m?.swapUsed} total={m?.swapTotal} />
        <Capacity label="磁盘" used={m?.diskUsed} total={m?.diskTotal} />
        <Row label="负载">{m?.load1 !== undefined && m.load5 !== undefined && m.load15 !== undefined ? <span className="num">{m.load1.toFixed(2)} / {m.load5.toFixed(2)} / {m.load15.toFixed(2)}</span> : <Missing />}</Row>
        <Row label="网络">
          {m?.netRxBps !== undefined && m.netTxBps !== undefined ? <span className="num">↓ {bytes(m.netRxBps)}/s ↑ {bytes(m.netTxBps)}/s</span> : <Missing />}
          {rate && <Sparkline values={rate} label="最近 1 小时网络速率" />}
        </Row>
        <Row label="本周期">{node.traffic ? <><span className="num">{trafficText(node.traffic)}</span> <span className="muted">{trafficDetail(node.traffic)}</span></> : <Missing />}</Row>
        <Row label="磁盘读写">{m?.diskReadBps !== undefined && m.diskWriteBps !== undefined ? <span className="num">读 {bytes(m.diskReadBps)}/s 写 {bytes(m.diskWriteBps)}/s</span> : <Missing />}</Row>
        <Row label="连接">{m?.tcpConns !== undefined && m.udpConns !== undefined && m.procs !== undefined ? <span className="num">TCP {m.tcpConns} · UDP {m.udpConns} · 进程 {m.procs}</span> : <Missing />}</Row>
        {price && <Row label="费用"><span className="num">{price}</span></Row>}
        {node.billing?.expiresOn && <Row label="到期"><Expiry billing={node.billing} /></Row>}
      </dl>
      <Link to={`/nodes/${node.id}`} className="detail-more">查看完整历史 →</Link>
    </aside>
  );
}
