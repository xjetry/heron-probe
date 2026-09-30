import { useQuery } from "@connectrpc/connect-query";
import { useState } from "react";
import { errorBanner, queryGate } from "../api/queryGate";
import { PublicService } from "../gen/heron/v1/public_pb";
import { sortByExpiry } from "../lib/billing";
import { POLL_MS } from "../lib/poll";
import { matchesTags, sameTag } from "../lib/tags";
import { FilterBar } from "./FilterBar";
import { ViewOptions } from "./ViewOptions";
import { NodeCard } from "./NodeCard";

export function PublicOverview() {
  const snap = useQuery(PublicService.method.getSnapshot, {}, { refetchInterval: POLL_MS });
  const [selected, setSelected] = useState<string[]>([]);
  const [selectedRegions, setSelectedRegions] = useState<string[]>([]);
  const [offlineOnly, setOfflineOnly] = useState(false);
  const [byExpiry, setByExpiry] = useState(false);
  const gate = queryGate(snap);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const now = Number(gate.data.now);
  const all = gate.data.nodes;
  const regions = [...new Set(all.map((node) => node.country))].sort((a, b) => a === "" ? 1 : b === "" ? -1 : a.localeCompare(b));
  const effectiveRegions = selectedRegions.filter((region) => regions.includes(region));
  // 快照决定可选地区；移除失效选择，防止该地区以后重新出现时悄悄恢复过滤。
  if (effectiveRegions.length !== selectedRegions.length) setSelectedRegions(effectiveRegions);
  // 标签栏的集合与顺序取 hub 下发的并集（按折叠键排序，与面板同序），页面不自己汇总、排序：折叠规则只在 hub 一处。
  const tags = gate.data.tags;
  // 生效的选择集只取当前快照里仍存在的标签，并换成标签栏上的写法：被选的标签在轮询后消失时，页面回到显示全部，
  // 而不是留下一个看不见的过滤条件。状态里存的写法可能与快照当前的写法只差折叠，经这一步统一。
  const effective = tags.filter((t) => selected.some((s) => sameTag(s, t)));
  // 先过滤再排序：排序只重排留下的节点。两个开关默认关，关着时既不过滤也不重排（显示全部、面板的手动顺序）。
  const filtered = all.filter((n) => matchesTags(n.tags, effective) && (effectiveRegions.length === 0 || effectiveRegions.includes(n.country)) && (!offlineOnly || !n.online));
  const nodes = byExpiry ? sortByExpiry(filtered) : filtered;
  const online = nodes.filter((n) => n.online).length;
  return (
    <section className="public-overview">
      <header className="row">
        <h1>节点</h1>
        <span className="muted">{online} / {nodes.length} 在线</span>
      </header>
      {gate.banner}
      {all.length > 0 && (
        <div className="view-bar">
          <FilterBar label="按地区筛选" options={regions.map((value) => ({ value, label: value || "未知" }))} selected={effectiveRegions} onChange={setSelectedRegions} />
          {tags.length > 0 && <FilterBar label="按标签筛选" options={tags.map((value) => ({ value, label: value }))} selected={effective} onChange={setSelected} />}
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
