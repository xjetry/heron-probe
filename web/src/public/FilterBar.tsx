// selected 由调用方按当前选项规范化；空数组表示全部，空字符串仍可作为“未知”等选项的独立标识。
export function FilterBar({ label, options, selected, onChange }: {
  label: string;
  options: readonly { value: string; label: string }[];
  selected: readonly string[];
  onChange: (selected: string[]) => void;
}) {
  return (
    <div className="filter-bar" role="group" aria-label={label}>
      <button type="button" className="chip" aria-pressed={selected.length === 0} onClick={() => onChange([])}>全部</button>
      {options.map(({ value, label }) => {
        const on = selected.includes(value);
        return <button key={value} type="button" className="chip" aria-pressed={on} onClick={(event) => {
          if (event.shiftKey) onChange(on ? selected.filter((item) => item !== value) : [...selected, value]);
          else onChange(on && selected.length === 1 ? [] : [value]);
        }}>{label}</button>;
      })}
    </div>
  );
}
