import { useQuery } from "@connectrpc/connect-query";
import { useState } from "react";
import { errorBanner, queryGate } from "../api/queryGate";
import { PublicService } from "../gen/heron/v1/public_pb";
import { POLL_MS } from "../lib/poll";
import { sameTag } from "../lib/tags";
import { FilterRow } from "./FilterRow";
import { filterPublicNodes, NO_FILTERS, regionOptions, sortCards, type CardSort, type ColorBy, type PublicFilters } from "./filters";
import { StatusSummary } from "./StatusSummary";
import { CardGrid } from "./NodeCard";
import { StatusWall } from "./StatusWall";
import { readPublicView, writePublicView, type View } from "./view";

export function PublicOverview() {
  const snap = useQuery(PublicService.method.getSnapshot, {}, { refetchInterval: POLL_MS });
  const [filters, setFilters] = useState<PublicFilters>(NO_FILTERS);
  const [view, setView] = useState<View>(readPublicView);
  const chooseView = (next: View) => { setView(next); writePublicView(next); };
  const [colorBy, setColorBy] = useState<ColorBy>("status");
  const [sort, setSort] = useState<CardSort>("default");
  const [selectedId, setSelectedId] = useState<bigint | null>(null);
  const gate = queryGate(snap);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const all = gate.data.nodes;
  const now = Number(gate.data.now);
  const regions = regionOptions(all);
  // 标签的集合与顺序取 hub 下发的并集（按折叠键排序，与面板同序），页面不自己汇总、排序：折叠规则只在 hub 一处。
  const tagCounts = new Map(gate.data.tags.map((tag) => [tag, all.filter((n) => n.tags.some((t) => sameTag(t, tag))).length]));
  const tags = gate.data.tags.map((tag) => ({ value: tag, label: tag, count: tagCounts.get(tag) ?? 0 }));
  // 生效的选择集只取当前快照里仍存在的地区与标签（标签按折叠比较，换成标签栏上的写法）：被选的项在轮询后消失时
  // 从选择集里移除，而不是留下一个看不见的过滤条件。
  const liveRegions = filters.regions.filter((code) => regions.some((r) => r.value === code));
  const liveTags = gate.data.tags.filter((tag) => filters.tags.some((s) => sameTag(s, tag)));
  if (liveRegions.length !== filters.regions.length || liveTags.length !== filters.tags.length) setFilters({ ...filters, regions: liveRegions, tags: liveTags });
  const effective: PublicFilters = { ...filters, regions: liveRegions, tags: liveTags };
  const nodes = filterPublicNodes(all, effective);
  return (
    <section className="public-overview">
      {gate.banner}
      {all.length === 0 && <p className="muted">没有公开的节点。</p>}
      {all.length > 0 && (
        <>
          <StatusSummary nodes={nodes} />
          <FilterRow filters={effective} onFilters={setFilters} regions={regions} tags={tags} view={view} onView={chooseView} colorBy={colorBy} onColorBy={setColorBy} sort={sort} onSort={setSort} />
          {nodes.length === 0 && <p className="muted">没有符合筛选条件的节点。</p>}
          {nodes.length > 0 && view === "wall" && <StatusWall nodes={nodes} now={now} colorBy={colorBy} selectedId={selectedId} onSelect={setSelectedId} />}
          {nodes.length > 0 && view === "cards" && <CardGrid nodes={sortCards(nodes, sort)} now={now} />}
        </>
      )}
    </section>
  );
}
