import { toggled } from "../lib/ids";

// 节点与渠道的多选共用；列表刷新后已删除对象的勾选仍留在集合里，由调用方提交时与当前列表求交。
export function Picks({ legend, items, selected, onChange }: {
  legend: string; items: readonly { id: bigint; name: string }[]; selected: ReadonlySet<bigint>; onChange: (next: Set<bigint>) => void;
}) {
  return (
    <fieldset className="picks">
      <legend>{legend}</legend>
      {items.map((it) => (
        <label key={String(it.id)}><input type="checkbox" checked={selected.has(it.id)} onChange={() => onChange(toggled(selected, it.id))} />{it.name}</label>
      ))}
    </fieldset>
  );
}
