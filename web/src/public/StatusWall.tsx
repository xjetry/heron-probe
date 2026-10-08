import type { MouseEvent } from "react";
import { Link } from "react-router";
import { Bar, ratio } from "../components/Bar";
import type { PublicNode } from "../gen/heron/v1/public_pb";
import { ago, bytes, percent } from "../lib/format";
import { nodeStatus, STATUS_LABEL, usageLevel } from "../lib/status";
import { useMediaQuery, WIDE_QUERY } from "../lib/useMediaQuery";
import { DetailPanel } from "./DetailPanel";
import { groupByRegion, tileLevel, type ColorBy } from "./filters";

// 状态墙（设计 §3.1）：左侧按地区分组的方块，右侧固定详情面板随点选切换。方块是节点页的链接：宽屏上不带修饰键的左键点击
// 只切换详情（面板里有「查看完整历史」去节点页），其余情况（窄屏、中键、⌘ / Ctrl / Shift）都是普通链接。
// 选中的节点不在当前列表里（被筛掉或转私有）时退回墙上第一个节点。
export function StatusWall({ nodes, now, colorBy, selectedId, onSelect }: {
  nodes: readonly PublicNode[]; now: number; colorBy: ColorBy; selectedId: bigint | null; onSelect: (id: bigint) => void;
}) {
  const wide = useMediaQuery(WIDE_QUERY);
  const groups = groupByRegion(nodes);
  const selected = nodes.find((n) => n.id === selectedId) ?? groups[0]?.nodes[0];
  const pick = (event: MouseEvent, id: bigint) => {
    if (!wide || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    onSelect(id);
  };
  return (
    <div className="wall-layout">
      <div className="wall">
        {groups.map((group) => (
          <details className="wall-group" role="group" aria-label={`${group.name} ${group.online} / ${group.nodes.length} 在线`} key={group.code} open>
            <summary>{group.name} · {group.online} / {group.nodes.length} 在线</summary>
            <ul className="tiles">
              {group.nodes.map((n) => {
                const status = nodeStatus(n);
                const m = n.metrics;
                const live = status === "online" || status === "maintenance";
                const attention = live && (usageLevel(m?.cpuPct ?? 0) !== "neutral" || (m?.memUsed !== undefined && !!m.memTotal && usageLevel(ratio(m.memUsed, m.memTotal)) !== "neutral"));
                return (
                  <li key={String(n.id)} className="tile" data-status={status} data-level={tileLevel(n, colorBy)} aria-current={selected?.id === n.id ? "true" : undefined}>
                    <Link to={`/nodes/${n.id}`} aria-label={n.name} onClick={(event) => pick(event, n.id)}>
                      <span className="tile-name" title={n.name}>
                        {n.name}
                        {!wide && status === "maintenance" && <span className="tile-maintenance">维护中</span>}
                        {!wide && attention && <span className="tile-attention">需关注</span>}
                      </span>
                      <span className="status-dot" data-status={status} aria-hidden="true" />
                      {status === "offline" && n.lastSeenAt !== undefined && <span className="tile-note">离线 · {ago(n.lastSeenAt, now)}</span>}
                      {(status === "never" || (wide && status === "maintenance")) && <span className="tile-note">{STATUS_LABEL[status]}</span>}
                      {live && m && (
                        <span className="tile-meters">
                          {m.cpuPct !== undefined && <><Bar thin value={m.cpuPct} label={`CPU ${percent(m.cpuPct)}`} /><span>{!wide && "CPU "}<span className="num">{percent(m.cpuPct)}</span></span></>}
                          {m.memUsed !== undefined && m.memTotal ? <><Bar thin value={ratio(m.memUsed, m.memTotal)} label={`内存 ${percent(ratio(m.memUsed, m.memTotal))}`} /><span>{!wide && "内存 "}<span className="num">{percent(ratio(m.memUsed, m.memTotal))}</span></span></> : null}
                          {!wide && m.netRxBps !== undefined && <span className="num">↓ {bytes(m.netRxBps)}/s</span>}
                        </span>
                      )}
                      <span className="tile-chevron" aria-hidden="true">›</span>
                    </Link>
                  </li>
                );
              })}
            </ul>
          </details>
        ))}
      </div>
      {wide && selected && <DetailPanel node={selected} now={now} />}
    </div>
  );
}
