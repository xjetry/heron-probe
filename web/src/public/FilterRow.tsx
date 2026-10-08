import { MultiSelect } from "../components/MultiSelect";
import { CARD_SORTS, COLOR_BYS, type CardSort, type ColorBy, type PublicFilters, type RegionOption } from "./filters";
import type { View } from "./view";

// 筛选行（设计 §3.1）：搜索、地区多选、标签多选（可搜索）、只看在线、视图切换；状态墙多一个着色依据，卡片多一个排序。
// 地区与标签是动态集合（设计 §2），选项由调用方从当前快照算出并带计数。
export function FilterRow({ filters, onFilters, regions, tags, view, onView, colorBy, onColorBy, sort, onSort }: {
  filters: PublicFilters; onFilters: (next: PublicFilters) => void;
  regions: RegionOption[]; tags: readonly { value: string; label: string; count: number }[];
  view: View; onView: (view: View) => void;
  colorBy: ColorBy; onColorBy: (by: ColorBy) => void;
  sort: CardSort; onSort: (sort: CardSort) => void;
}) {
  return (
    <div className="filter-row" role="group" aria-label="筛选">
      <input type="search" aria-label="搜索节点" placeholder="搜索名称、标签、备注" value={filters.search} onChange={(event) => onFilters({ ...filters, search: event.target.value })} />
      <MultiSelect label="地区" options={regions} selected={filters.regions} onChange={(next) => onFilters({ ...filters, regions: next })} />
      {tags.length > 0 && <MultiSelect label="标签" options={tags} selected={filters.tags} onChange={(next) => onFilters({ ...filters, tags: next })} searchable />}
      <button type="button" className="toggle" aria-pressed={filters.onlineOnly} onClick={() => onFilters({ ...filters, onlineOnly: !filters.onlineOnly })}>只看在线</button>
      <div className="view-switch" role="group" aria-label="视图">
        <button type="button" aria-pressed={view === "wall"} onClick={() => onView("wall")}>状态墙</button>
        <button type="button" aria-pressed={view === "cards"} onClick={() => onView("cards")}>卡片</button>
      </div>
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
