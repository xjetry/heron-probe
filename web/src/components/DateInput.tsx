import { useId, useRef, useState } from "react";
import { Icon } from "./Icon";

function validDate(value: string) {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value) || value.startsWith("0000")) return false;
  const date = new Date(`${value}T00:00:00Z`);
  return Number.isFinite(date.getTime()) && date.toISOString().slice(0, 10) === value;
}

const partsOf = (value: string) => value ? value.split("-") : ["", "", ""];

export function DateInput({ label, value, onChange }: { label: string; value: string; onChange: (value: string) => void }) {
  const [draft, setDraft] = useState(() => ({ value, parts: partsOf(value) }));
  const inputs = useRef<(HTMLInputElement | null)[]>([]);
  const calendar = useRef<HTMLInputElement>(null);
  const hint = useId();
  if (draft.value !== value) setDraft({ value, parts: partsOf(value) });
  const invalid = value !== "" && !validDate(value);
  const change = (index: number, text: string) => {
    const limit = index === 0 ? 4 : 2;
    const digits = text.replace(/\D/g, "").slice(0, limit);
    const parts = draft.parts.map((part, i) => i === index ? digits : part);
    // 编辑中的短月份和日期保留原样显示，完整值才以协议要求的 YYYY-MM-DD 输出。
    const next = parts.every(part => part === "") ? "" : parts.map((part, i) => part ? part.padStart(i === 0 ? 4 : 2, "0") : "").join("-");
    const complete = parts[0].length === 4 && parts.every(Boolean);
    const emitted = complete || parts.every(part => part === "") ? next : parts.join("-");
    setDraft({ value: emitted, parts });
    onChange(emitted);
    if (digits.length === limit && index < 2) {
      inputs.current[index + 1]?.focus();
      inputs.current[index + 1]?.select();
    }
  };
  return <div className="date-field" role="group" aria-label={label}>
    <span>到期日</span>
    <div className="date-input">
      {draft.parts.map((part, index) => <label key={index}>
        <span className="muted">{["年", "月", "日"][index]}</span>
        <input ref={element => { inputs.current[index] = element; element?.setCustomValidity(invalid ? "请输入四位年份和有效的完整日期" : ""); }}
          aria-label={`${label} ${["年", "月", "日"][index]}`} aria-describedby={hint}
          inputMode="numeric" autoComplete="off" maxLength={index === 0 ? 4 : 2}
          placeholder={["YYYY", "MM", "DD"][index]} value={part}
          onFocus={event => event.currentTarget.select()}
          onChange={event => change(index, event.target.value)} />
      </label>)}
      <button type="button" className="icon-button" aria-label={`选择${label}`} title="选择日期" onClick={() => calendar.current?.showPicker()}><Icon name="calendar" /></button>
      <button type="button" className="icon-button" aria-label={`清除${label}`} title="清除日期" disabled={!value} onClick={() => onChange("")}><Icon name="close" /></button>
      <input ref={calendar} className="date-picker-native" type="date" tabIndex={-1} aria-hidden="true" aria-label={label}
        min="0001-01-01" max="9999-12-31" value={validDate(value) ? value : ""} onChange={event => onChange(event.target.value)} />
    </div>
    <small id={hint} className={invalid ? "error" : "muted"}>{invalid ? "请输入四位年份和有效的完整日期" : "年满四位、月满两位自动跳转；可留空"}</small>
  </div>;
}
