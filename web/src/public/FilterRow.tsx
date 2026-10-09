import { Fragment, useCallback, useId, useRef, useState } from "react";
import { Icon } from "../components/Icon";
import { SearchHint } from "../components/SearchHint";
import { Select } from "../components/Select";
import type { TagMatch } from "../lib/tags";
import { SEARCH_KEYSHORTCUTS, useSearchShortcut } from "../lib/useSearchShortcut";
import { FacetPanel, FacetTrigger } from "./Facet";
import { CARD_SORTS, COLOR_BYS, GROUP_BYS, type CardSort, type ColorBy, type FacetOption, type GroupBy, type PublicFilters } from "./filters";
import type { Facet, FacetMode, View } from "./prefs";

// 筛选行（设计 §3.1）：搜索、地区与标签两个筛选入口、只看在线、视图切换；状态墙多一个分组依据（有标签时）与着色依据，
// 卡片多一个排序。地区与标签的选项由调用方从当前快照算出并带计数。
export function FilterRow({ filters, onFilters, regions, tags, modes, onMode, onTagMatch, view, onView, groupBy, onGroupBy, colorBy, onColorBy, sort, onSort }: {
  filters: PublicFilters; onFilters: (next: PublicFilters) => void;
  regions: readonly FacetOption[]; tags: readonly FacetOption[];
  modes: Readonly<Record<Facet, FacetMode>>; onMode: (facet: Facet, mode: FacetMode) => void; onTagMatch: (match: TagMatch) => void;
  view: View; onView: (view: View) => void;
  groupBy: GroupBy; onGroupBy: (by: GroupBy) => void;
  colorBy: ColorBy; onColorBy: (by: ColorBy) => void;
  sort: CardSort; onSort: (sort: CardSort) => void;
}) {
  const search = useRef<HTMLInputElement>(null);
  useSearchShortcut(useCallback(() => search.current?.focus(), []));
  const [openFacet, setOpenFacet] = useState<Facet | null>(null);
  const triggers = useRef<Partial<Record<Facet, HTMLButtonElement | null>>>({});
  const panelId = useId();
  // 只有标签有匹配方式：一个节点只有一个地区，地区多选只能取并集。
  const facets = [
    { key: "region" as const, label: "地区", options: regions, selected: filters.regions, set: (next: string[]) => onFilters({ ...filters, regions: next }), hint: "显示属于任一所选地区的节点。" },
    ...(tags.length > 0 ? [{
      key: "tag" as const, label: "标签", options: tags, selected: filters.tags, set: (next: string[]) => onFilters({ ...filters, tags: next }),
      match: { value: filters.tagMatch, onChange: onTagMatch },
      hint: filters.tagMatch === "all" ? "只显示同时带有全部所选标签的节点。" : "显示带有任一所选标签的节点。",
    }] : []),
  ];
  // 没有任何标签时不出标签入口。展开着的标签面板随之消失时也把展开状态清掉，否则标签在后续轮询里重新出现时面板会自己弹开。
  if (openFacet !== null && !facets.some((facet) => facet.key === openFacet)) setOpenFacet(null);
  const close = (facet: Facet) => { setOpenFacet(null); triggers.current[facet]?.focus(); };
  return (
    <div className="filter-row" role="group" aria-label="筛选">
      <div className="search-field">
        <Icon name="search" />
        <input ref={search} type="search" aria-label="搜索节点" aria-keyshortcuts={SEARCH_KEYSHORTCUTS} value={filters.search} onChange={(event) => onFilters({ ...filters, search: event.target.value })} />
        {filters.search === "" && <SearchHint text="搜索名称、标签、备注" />}
      </div>
      {/* 面板在 DOM 里紧跟自己的入口按钮，Tab 从入口直接进面板；视觉上由 CSS order 排到筛选行最后、独占一行。 */}
      {facets.map((facet) => (
        <Fragment key={facet.key}>
          <FacetTrigger ref={(el) => { triggers.current[facet.key] = el; }} label={facet.label} options={facet.options} selected={facet.selected}
            open={openFacet === facet.key} panelId={panelId} onToggle={() => setOpenFacet(openFacet === facet.key ? null : facet.key)} onClose={() => close(facet.key)} />
          {openFacet === facet.key && (
            <FacetPanel id={panelId} label={facet.label} options={facet.options} selected={facet.selected} onChange={facet.set}
              mode={modes[facet.key]} onMode={(mode) => onMode(facet.key, mode)} match={"match" in facet ? facet.match : undefined} hint={facet.hint} onClose={() => close(facet.key)} />
          )}
        </Fragment>
      ))}
      <button type="button" className="toggle" aria-pressed={filters.onlineOnly} onClick={() => onFilters({ ...filters, onlineOnly: !filters.onlineOnly })}>只看在线</button>
      <div className="view-switch" role="group" aria-label="视图">
        <button type="button" aria-pressed={view === "wall"} onClick={() => onView("wall")}>状态墙</button>
        <button type="button" aria-pressed={view === "cards"} onClick={() => onView("cards")}>卡片</button>
      </div>
      {view === "wall" && tags.length > 0 && <Select label="分组" value={groupBy} options={GROUP_BYS} onChange={onGroupBy} />}
      {view === "wall" && <Select label="着色依据" value={colorBy} options={COLOR_BYS} onChange={onColorBy} />}
      {view === "cards" && <Select label="排序" value={sort} options={CARD_SORTS} onChange={onSort} />}
    </div>
  );
}
