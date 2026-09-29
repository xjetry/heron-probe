// 公开页标签栏。选择集与点击语义在 lib/tags.ts 的 nextSelection，这里只负责呈现与转发点击；selected 为空表示"全部"。
// selected 里的写法必须取自 tags（Overview 的 effective 由 tags 过滤而来），aria-pressed 才能按字面相等判定。
export function TagBar({ tags, selected, onSelect, onClear }: {
  tags: readonly string[];
  selected: readonly string[];
  onSelect: (tag: string, shift: boolean) => void;
  onClear: () => void;
}) {
  return (
    <div className="tag-bar" role="group" aria-label="按标签筛选">
      <button type="button" className="chip" aria-pressed={selected.length === 0} onClick={onClear}>全部</button>
      {tags.map((t) => (
        <button key={t} type="button" className="chip" aria-pressed={selected.includes(t)} onClick={(e) => onSelect(t, e.shiftKey)}>{t}</button>
      ))}
    </div>
  );
}
