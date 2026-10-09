import { useState, type Ref } from "react";
import { Icon } from "../components/Icon";
import { literalPattern } from "../lib/fold";
import type { TagMatch } from "../lib/tags";
import type { FacetOption } from "./filters";
import type { FacetMode } from "./prefs";

// 地区与标签是动态集合（设计 §2）：筛选行上只放入口按钮，点开后在筛选行下方平铺这一项的全部选项，同一时间只展开一项。
// 选项多于 FACET_SEARCH_AT 个时面板里出现搜索框，几十个标签也能先收窄再点。选项与选择集由调用方按当前快照给出，
// 选择集只含仍在快照里的值（Overview 负责剔除），本文件不存选择。
export const FACET_SEARCH_AT = 12;

// 单选模式下选择集至多一项：点一个就只看它，再点当前选中的那一个回到全部。多选模式逐个翻转。
// 「至多一项」靠每个写入口维持：初始选择为空，这里的单选分支只产出 0 或 1 项，切回单选时 FacetPanel 用 narrowToSingle
// 收窄，Overview 剔除已消失的值只会减少项数。新增写选择集的入口必须同样守住它。
export function pickOption(mode: FacetMode, selected: readonly string[], value: string): string[] {
  if (mode === "single") return selected.length === 1 && selected[0] === value ? [] : [value];
  return selected.includes(value) ? selected.filter((v) => v !== value) : [...selected, value];
}

// 从多选切回单选时只留按选项顺序排在最前的已选项，与面板上看到的先后一致，不取决于勾选的先后。
export function narrowToSingle(options: readonly FacetOption[], selected: readonly string[]): string[] {
  const first = options.find((option) => selected.includes(option.value));
  return first ? [first.value] : [];
}

export function facetSummary(options: readonly FacetOption[], selected: readonly string[]): string {
  if (selected.length === 0) return "全部";
  if (selected.length > 1) return `已选 ${selected.length} 个`;
  return options.find((option) => option.value === selected[0])?.label ?? selected[0];
}

export function FacetTrigger({ ref, label, options, selected, open, panelId, onToggle, onClose }: {
  ref: Ref<HTMLButtonElement>;
  label: string; options: readonly FacetOption[]; selected: readonly string[];
  open: boolean; panelId: string; onToggle: () => void; onClose: () => void;
}) {
  return (
    <button ref={ref} type="button" className="select-trigger facet-trigger" data-active={selected.length > 0 || undefined} aria-expanded={open} aria-controls={open ? panelId : undefined}
      onClick={onToggle} onKeyDown={(event) => { if (event.key === "Escape" && open) onClose(); }}>
      <span className="select-label">{label}</span>{" "}<span className="select-value">{facetSummary(options, selected)}</span>
      <Icon name="chevronDown" className="select-caret" width={14} height={14} />
    </button>
  );
}

// match 给出时（只有标签），多选模式下多一个匹配方式切换；文案写成「满足任一 / 同时满足」，不写逻辑运算符。
export function FacetPanel({ id, label, options, selected, onChange, mode, onMode, match, hint, onClose }: {
  id: string; label: string; options: readonly FacetOption[]; selected: readonly string[]; onChange: (next: string[]) => void;
  mode: FacetMode; onMode: (mode: FacetMode) => void;
  match?: { value: TagMatch; onChange: (match: TagMatch) => void };
  hint: string; onClose: () => void;
}) {
  const [query, setQuery] = useState("");
  const pattern = query === "" ? null : literalPattern(query, false);
  const shown = pattern ? options.filter((option) => pattern.test(option.label)) : options;
  const chooseMode = (next: FacetMode) => {
    onMode(next);
    if (next === "single" && selected.length > 1) onChange(narrowToSingle(options, selected));
  };
  return (
    <div id={id} className="facet-panel" role="group" aria-label={label} onKeyDown={(event) => { if (event.key === "Escape") onClose(); }}>
      <div className="facet-panel-head">
        <div className="segmented" role="group" aria-label="选择方式">
          <button type="button" aria-pressed={mode === "single"} onClick={() => chooseMode("single")}>单选</button>
          <button type="button" aria-pressed={mode === "multi"} onClick={() => chooseMode("multi")}>多选</button>
        </div>
        {mode === "multi" && match && (
          <div className="segmented" role="group" aria-label="匹配方式">
            <button type="button" aria-pressed={match.value === "any"} onClick={() => match.onChange("any")}>满足任一</button>
            <button type="button" aria-pressed={match.value === "all"} onClick={() => match.onChange("all")}>同时满足</button>
          </div>
        )}
        {options.length > FACET_SEARCH_AT && <input type="search" aria-label={`搜索${label}`} value={query} onChange={(event) => setQuery(event.target.value)} />}
        {mode === "multi" && <p className="facet-hint">{hint}</p>}
      </div>
      <ul className="facet-options">
        <li><button type="button" className="facet-chip" aria-pressed={selected.length === 0} onClick={() => onChange([])}>全部</button></li>
        {shown.map((option) => {
          const on = selected.includes(option.value);
          return (
            <li key={option.value}>
              <button type="button" className="facet-chip" aria-pressed={on} onClick={() => onChange(pickOption(mode, selected, option.value))}>
                {mode === "multi" && <span className="facet-box" aria-hidden="true">{on && <Icon name="check" width={12} height={12} strokeWidth={2.5} />}</span>}
                <span className="facet-chip-label">{option.label}</span>{" "}<span className="num">{option.count}</span>
              </button>
            </li>
          );
        })}
        {shown.length === 0 && <li className="muted">没有匹配的选项</li>}
      </ul>
    </div>
  );
}
