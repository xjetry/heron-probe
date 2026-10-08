import { useQuery } from "@connectrpc/connect-query";
import { useState } from "react";
import { Link } from "react-router";
import { errorBanner, queryGateAll } from "../api/queryGate";
import { Bar, Missing, ratio } from "../components/Bar";
import { PageHeader } from "../components/PageHeader";
import { AdminService, type Node, type NodeStatus as LiveNode } from "../gen/heron/v1/admin_pb";
import { liveById, liveStatus } from "../lib/adminStatus";
import { attentionCards } from "../lib/attention";
import { ago, bytes, percent } from "../lib/format";
import { withId } from "../lib/ids";
import { filterNodes } from "../lib/nodeSearch";
import { POLL_MS } from "../lib/poll";
import { trafficText } from "../lib/traffic";
import { STATUS_LABEL } from "../lib/status";

// ListNodes 提供维护状态、到期与 agent 版本，GetSnapshot 提供在线裁决与读数；
// queryGateAll 等待两者首次到达，避免缺少维护状态时误判节点状态。
const DETAILS_MS = 10_000;

export function Overview() {
  const [search, setSearch] = useState("");
  const snap = useQuery(AdminService.method.getSnapshot, {}, { refetchInterval: POLL_MS });
  const details = useQuery(AdminService.method.listNodes, {}, { refetchInterval: DETAILS_MS });
  const rules = useQuery(AdminService.method.listAlertRules, {}, { refetchInterval: DETAILS_MS });
  const gate = queryGateAll(snap, details);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const [snapshot, listed] = gate.data;
  const now = Number(snapshot.now);
  const live = liveById(snapshot.nodes);
  const cards = attentionCards({ nodes: listed.nodes, live, boundAgentVersion: snapshot.boundAgentVersion, states: rules.data?.states });
  const rows = filterNodes(listed.nodes, search);
  return (
    <section>
      <PageHeader title="总览" />
      {gate.banner}
      {errorBanner(rules.error)}
      <ul className="attention" aria-label="需要处理">
        {cards.map((card) => (
          <li key={card.key} data-key={card.key} data-zero={card.count === 0 || undefined}>
            <Link to={card.to}><strong className="num">{card.count === null ? "—" : card.count}</strong><span>{card.label}</span>{card.note && <small className="muted">{card.note}</small>}</Link>
          </li>
        ))}
      </ul>
      <div className="filter-row" role="group" aria-label="筛选">
        <input type="search" aria-label="搜索节点" placeholder="名称、IP、地区、备注或主机名" value={search} onChange={(event) => setSearch(event.target.value)} />
        <span className="muted">实时 · 每 {POLL_MS / 1000} 秒</span>
      </div>
      {listed.nodes.length === 0 && <p className="muted">还没有节点。去 <Link to="/nodes">节点</Link> 页创建，或开一个 <Link to="/register">注册窗口</Link>。</p>}
      {listed.nodes.length > 0 && rows.length === 0 && <p className="muted" role="status">没有匹配的节点。</p>}
      {rows.length > 0 && (
        <div className="table-scroll" role="region" aria-label="节点实时读数" tabIndex={0}>
          <table className="nodes overview-table">
            <thead><tr><th>状态</th><th>节点</th><th>CPU</th><th>内存</th><th>磁盘</th><th>负载</th><th>网络</th><th>本周期</th><th>最近上报</th></tr></thead>
            <tbody>{rows.map((node) => <NodeRow key={String(node.id)} node={node} live={live.get(node.id)} now={now} />)}</tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function Meter({ label, value }: { label: string; value: number | undefined }) {
  if (value === undefined) return <Missing />;
  return <><Bar thin value={value} label={`${label} ${percent(value)}`} /><span className="num">{percent(value)}</span></>;
}

function NodeRow({ node, live, now }: { node: Node; live: LiveNode | undefined; now: number }) {
  const status = liveStatus(node, live);
  const m = live?.metrics;
  return (
    <tr aria-label={node.name} data-status={status ?? "unknown"}>
      <td data-label="状态"><span className="status-dot" data-status={status} role="img" aria-label={status ? STATUS_LABEL[status] : "状态未知"} /></td>
      <td data-label="节点"><Link to={`/nodes/${node.id}`} aria-label={withId(node.name, node.id)}>{node.name}</Link></td>
      <td data-label="CPU"><Meter label="CPU" value={m?.cpuPct} /></td>
      <td data-label="内存"><Meter label="内存" value={m?.memUsed !== undefined && m.memTotal ? ratio(m.memUsed, m.memTotal) : undefined} /></td>
      <td data-label="磁盘"><Meter label="磁盘" value={m?.diskUsed !== undefined && m.diskTotal ? ratio(m.diskUsed, m.diskTotal) : undefined} /></td>
      <td data-label="负载" className="num">{m?.load1 !== undefined && m.load5 !== undefined && m.load15 !== undefined ? `${m.load1.toFixed(2)} / ${m.load5.toFixed(2)} / ${m.load15.toFixed(2)}` : <Missing />}</td>
      <td data-label="网络" className="num">{m?.netRxBps !== undefined && m.netTxBps !== undefined ? `↓ ${bytes(m.netRxBps)}/s ↑ ${bytes(m.netTxBps)}/s` : <Missing />}</td>
      <td data-label="本周期" className="num">{live?.traffic ? trafficText(live.traffic) : <Missing />}</td>
      <td data-label="最近上报" className="muted num">{live?.lastSeenAt !== undefined ? ago(live.lastSeenAt, now) : STATUS_LABEL.never}</td>
    </tr>
  );
}
