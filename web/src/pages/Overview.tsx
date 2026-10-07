import { useQuery } from "@connectrpc/connect-query";
import { useState } from "react";
import { Link } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
import { AdminService, type NodeStatus } from "../gen/heron/v1/admin_pb";
import { ago, bytes, percent } from "../lib/format";
import { withId } from "../lib/ids";
import { filterNodes } from "../lib/nodeSearch";
import { POLL_MS } from "../lib/poll";
import { Bar, Missing, ratio } from "../components/Bar";
import { Icon } from "../components/Icon";

export function Overview() {
  const [search, setSearch] = useState("");
  const snap = useQuery(AdminService.method.getSnapshot, {}, { refetchInterval: POLL_MS });
  const gate = queryGate(snap);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const now = Number(gate.data.now);
  const online = gate.data.nodes.filter((n) => n.online).length;
  return (
    <section>
      <header className="page-heading">
        <div><div className="eyebrow">Overview</div><h1>总览</h1><p>基础设施运行概况，关键指标尽在眼前。</p></div>
        <span className="status-pill"><span className="dot ok" />{online} / {gate.data.nodes.length} 在线</span>
      </header>
      {gate.banner}
      <OverviewSummary nodes={gate.data.nodes} />
      <div className="section-heading"><h2>实时节点</h2><span className="live-caption">每 2 秒刷新 · 以 hub 上报状态为准</span></div>
      <label className="node-search">搜索节点<input type="search" placeholder="名称、IP、地区、备注或主机名" value={search} onChange={(e) => setSearch(e.target.value)} /></label>
      {gate.data.nodes.length === 0 && (
        <p className="muted">
          还没有节点。去 <Link to="/nodes">节点</Link> 页创建，或开一个 <Link to="/register">注册窗口</Link>。
        </p>
      )}
      <OverviewNodes nodes={gate.data.nodes} now={now} search={search} />
    </section>
  );
}

function OverviewSummary({ nodes }: { nodes: NodeStatus[] }) {
  const online = nodes.filter((node) => node.online);
  const cpus = online.flatMap((node) => node.metrics?.cpuPct === undefined ? [] : [node.metrics.cpuPct]);
  const links = online.filter((node) => node.metrics?.netRxBps !== undefined && node.metrics?.netTxBps !== undefined);
  const rx = links.reduce((total, node) => total + node.metrics!.netRxBps!, 0n);
  const tx = links.reduce((total, node) => total + node.metrics!.netTxBps!, 0n);
  return <div className="stats-grid">
    <dl className="metric-card"><dt>节点总数<Icon name="server" /></dt><dd>{nodes.length}</dd><small>{online.length} 在线 / {nodes.length - online.length} 离线</small></dl>
    <dl className="metric-card"><dt>平均 CPU<Icon name="activity" /></dt><dd>{cpus.length ? percent(cpus.reduce((total, value) => total + value, 0) / cpus.length) : "暂无读数"}</dd><small>{cpus.length} 个在线节点有读数</small></dl>
    <dl className="metric-card"><dt>实时下行<Icon name="arrowDown" /></dt><dd>{links.length ? `${bytes(rx)}/s` : "暂无读数"}</dd><small>{links.length} 个在线节点合计</small></dl>
    <dl className="metric-card"><dt>实时上行<Icon name="arrowUp" /></dt><dd>{links.length ? `${bytes(tx)}/s` : "暂无读数"}</dd><small>{links.length} 个在线节点合计</small></dl>
  </div>;
}

function OverviewNodes({ nodes, now, search }: { nodes: NodeStatus[]; now: number; search: string }) {
  // 快照没有备注与主机名，搜索时按 id 关联 ListNodes；空输入不依赖资料查询，仍可直接查看实时读数。
  const details = useQuery(AdminService.method.listNodes, {}, { enabled: search !== "", refetchInterval: POLL_MS });
  if (search === "") return <NodeTable nodes={nodes} now={now} />;
  const gate = queryGate(details);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const byId = new Map(gate.data.nodes.map((node) => [node.id, node]));
  const matches = filterNodes(nodes.map((node) => ({ ...node, note: byId.get(node.id)?.note, facts: byId.get(node.id)?.facts, country: byId.get(node.id)?.country, lastSource: byId.get(node.id)?.lastSource })), search);
  return <>
    {gate.banner}
    {matches.length === 0 && <p className="muted" role="status">没有匹配的节点。</p>}
    <NodeTable nodes={matches} now={now} />
  </>;
}

function NodeTable({ nodes, now }: { nodes: NodeStatus[]; now: number }) {
  return (
    <div className="table-scroll" role="region" aria-label="节点实时读数" tabIndex={0}>
      <table className="nodes">
        <thead>
          <tr><th>节点</th><th>CPU</th><th>内存</th><th>磁盘</th><th>负载</th><th>网络</th><th>本周期 ↓/↑</th><th>最近上报</th></tr>
        </thead>
        <tbody>
          {nodes.map((n) => <NodeRow key={String(n.id)} node={n} now={now} />)}
        </tbody>
      </table>
    </div>
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
      <td>{node.traffic ? trafficText(node.traffic) : <Missing />}</td>
      <td className="muted">{node.lastSeenAt !== undefined ? ago(node.lastSeenAt, now) : "从未"}</td>
    </tr>
  );
}
import { trafficText } from "../lib/traffic";
