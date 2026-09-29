import { useQuery } from "@connectrpc/connect-query";
import { useState } from "react";
import { Link } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
import { Bar, Missing, ratio } from "../components/Bar";
import { CountryBadge } from "../components/CountryBadge";
import { PublicService, type PublicNode } from "../gen/probe/v1/public_pb";
import { expired, expiryText, priceText, sortByExpiry } from "../lib/billing";
import { ago, bytes, duration, percent } from "../lib/format";
import { POLL_MS } from "../lib/poll";
import { matchesTags, nextSelection, presentTags, sameTag } from "../lib/tags";
import { TagBar } from "./TagBar";
import { ViewOptions } from "./ViewOptions";

export function PublicOverview() {
  const snap = useQuery(PublicService.method.getSnapshot, {}, { refetchInterval: POLL_MS });
  const [selected, setSelected] = useState<string[]>([]);
  const [offlineOnly, setOfflineOnly] = useState(false);
  const [byExpiry, setByExpiry] = useState(false);
  const gate = queryGate(snap);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const now = Number(gate.data.now);
  const all = gate.data.nodes;
  const tags = presentTags(all);
  // 生效的选择集只取当前快照里仍存在的标签，并换成标签栏上的写法：被选的标签在轮询后消失时，页面回到显示全部，
  // 而不是留下一个看不见的过滤条件。状态里存的写法可能与快照当前的写法只差折叠，经这一步统一。
  const effective = tags.filter((t) => selected.some((s) => sameTag(s, t)));
  // 先过滤再排序：排序只重排留下的节点。两个开关默认关，关着时既不过滤也不重排（显示全部、面板的手动顺序）。
  const filtered = all.filter((n) => matchesTags(n.tags, effective) && (!offlineOnly || !n.online));
  const nodes = byExpiry ? sortByExpiry(filtered) : filtered;
  const online = nodes.filter((n) => n.online).length;
  return (
    <section>
      <header className="row">
        <h1>节点</h1>
        <span className="muted">{online} / {nodes.length} 在线</span>
      </header>
      {gate.banner}
      {all.length > 0 && (
        <div className="view-bar">
          {tags.length > 0 && <TagBar tags={tags} selected={effective} onSelect={(t, shift) => setSelected(nextSelection(effective, t, shift))} onClear={() => setSelected([])} />}
          <ViewOptions offlineOnly={offlineOnly} byExpiry={byExpiry} onOfflineOnly={setOfflineOnly} onByExpiry={setByExpiry} />
        </div>
      )}
      {all.length === 0 && <p className="muted">没有公开的节点。</p>}
      {all.length > 0 && nodes.length === 0 && <p className="muted">没有符合筛选条件的节点。</p>}
      <div className="cards">
        {nodes.map((n) => <NodeCard key={String(n.id)} node={n} now={now} />)}
      </div>
    </section>
  );
}

// 卡片内容按 §10：名称、国家 / 地区徽章、在线、系统与架构、CPU、内存、磁盘、网速、运行时长、本周期流量，以及填了才显示的费用与到期。
function NodeCard({ node, now }: { node: PublicNode; now: number }) {
  const m = node.metrics;
  const f = node.facts;
  const price = priceText(node.billing);
  const expiry = expiryText(node.billing);
  return (
    <article className={`card node-card ${node.online ? "online" : "offline"}`} aria-label={node.name}>
      <h2>
        <span className={`dot ${node.online ? "ok" : "bad"}`} role="img" aria-label={node.online ? "在线" : "离线"} />
        <Link to={`/nodes/${node.id}`}>{node.name}</Link>
        {node.country && <>{" "}<CountryBadge code={node.country} /></>}
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
        {price && <><dt>费用</dt><dd>{price}</dd></>}
        {expiry && <><dt>到期</dt><dd className={expired(node.billing) ? "error" : undefined}>{expiry}</dd></>}
      </dl>
      <p className="muted">{node.lastSeenAt !== undefined ? `最近上报 ${ago(node.lastSeenAt, now)}` : "从未上报"}</p>
    </article>
  );
}
