import { useCallback, useRef } from "react";
import { Icon } from "../components/Icon";
import { MultiSelect } from "../components/MultiSelect";
import { SearchHint } from "../components/SearchHint";
import { SEARCH_KEYSHORTCUTS, useSearchShortcut } from "../lib/useSearchShortcut";
import { CARD_SORTS, COLOR_BYS, GROUP_BYS, type CardSort, type ColorBy, type GroupBy, type PublicFilters, type RegionOption } from "./filters";
import type { View } from "./prefs";

// 筛选行（设计 §3.1）：搜索、地区多选、标签多选（可搜索）、只看在线、视图切换；状态墙多一个分组依据（有标签时）与着色依据，
// 卡片多一个排序。
// 地区与标签是动态集合（设计 §2），选项由调用方从当前快照算出并带计数。
export function FilterRow({ filters, onFilters, regions, tags, view, onView, groupBy, onGroupBy, colorBy, onColorBy, sort, onSort }: {
  filters: PublicFilters; onFilters: (next: PublicFilters) => void;
  regions: RegionOption[]; tags: readonly { value: string; label: string; count: number }[];
  view: View; onView: (view: View) => void;
  groupBy: GroupBy; onGroupBy: (by: GroupBy) => void;
  colorBy: ColorBy; onColorBy: (by: ColorBy) => void;
  sort: CardSort; onSort: (sort: CardSort) => void;
}) {
  const search = useRef<HTMLInputElement>(null);
  useSearchShortcut(useCallback(() => search.current?.focus(), []));
  return (
    <div className="filter-row" role="group" aria-label="筛选">
      <div className="search-field">
        <Icon name="search" />
        <input ref={search} type="search" aria-label="搜索节点" aria-keyshortcuts={SEARCH_KEYSHORTCUTS} value={filters.search} onChange={(event) => onFilters({ ...filters, search: event.target.value })} />
        {filters.search === "" && <SearchHint text="搜索名称、标签、备注" />}
      </div>
      <MultiSelect label="地区" options={regions} selected={filters.regions} onChange={(next) => onFilters({ ...filters, regions: next })} />
      {tags.length > 0 && <MultiSelect label="标签" options={tags} selected={filters.tags} onChange={(next) => onFilters({ ...filters, tags: next })} searchable />}
      <button type="button" className="toggle" aria-pressed={filters.onlineOnly} onClick={() => onFilters({ ...filters, onlineOnly: !filters.onlineOnly })}>只看在线</button>
      <div className="view-switch" role="group" aria-label="视图">
        <button type="button" aria-pressed={view === "wall"} onClick={() => onView("wall")}>状态墙</button>
        <button type="button" aria-pressed={view === "cards"} onClick={() => onView("cards")}>卡片</button>
      </div>
      {view === "wall" && tags.length > 0 && (
        <label className="inline">分组
          <select aria-label="分组" value={groupBy} onChange={(event) => onGroupBy(event.target.value as GroupBy)}>
            {GROUP_BYS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
          </select>
        </label>
      )}
      {view === "wall" && (
        <label className="inline">着色依据
          <select aria-label="着色依据" value={colorBy} onChange={(event) => onColorBy(event.target.value as ColorBy)}>
            {COLOR_BYS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
          </select>
        </label>
      )}
      {view === "cards" && (
        <label className="inline">排序
          <select aria-label="排序" value={sort} onChange={(event) => onSort(event.target.value as CardSort)}>
            {CARD_SORTS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
          </select>
        </label>
      )}
    </div>
  );
}
