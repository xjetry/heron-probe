import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { isDate } from "../lib/format";
import { Icon } from "./Icon";

const WEEKDAYS = ["一", "二", "三", "四", "五", "六", "日"];

// 日期运算都在 UTC 上做：只关心日历日，不经过本地时区，夏令时切换日不会少一天或多一天。
// 用 setUTCFullYear 而不是 Date.UTC：后者把 0–99 年当成 1900–1999 年，而 isDate 收 0001 年起的日期。
function utc(year: number, monthIndex: number, day: number): number {
  const date = new Date(0);
  date.setUTCFullYear(year, monthIndex, day);
  return date.getTime();
}
const toUtc = (ymd: string) => { const [y, m, d] = ymd.split("-").map(Number); return utc(y, m - 1, d); };
const fromUtc = (ms: number) => new Date(ms).toISOString().slice(0, 10);
const addDays = (ymd: string, n: number) => fromUtc(toUtc(ymd) + n * 86_400_000);
// 跨月时日子夹到目标月的最后一天：1 月 31 日的下个月是 2 月 28 / 29 日，不溢出到 3 月。
function addMonths(ymd: string, n: number): string {
  const [y, m, d] = ymd.split("-").map(Number);
  const first = new Date(utc(y, m - 1 + n, 1));
  const last = new Date(utc(first.getUTCFullYear(), first.getUTCMonth() + 1, 0)).getUTCDate();
  return fromUtc(utc(first.getUTCFullYear(), first.getUTCMonth(), Math.min(d, last)));
}
const weekdayOf = (ymd: string) => (new Date(toUtc(ymd)).getUTCDay() + 6) % 7;

// 「今天」取浏览器本地日期：选日期是给人看的日历，与 hub 时区无关；需要 hub 日界的地方（到期剩余天数）由 hub 自己算。
function today(): string {
  const now = new Date();
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${String(now.getFullYear()).padStart(4, "0")}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`;
}

// 自绘月历：周一开头，6 行 42 格，前后月份的日子淡显但可选。方向键按日 / 周移动，PageUp / PageDown 换月，
// Home / End 到本周首尾，Esc 关闭并把焦点还给控件；点到弹层与所属控件之外也关闭，焦点留在用户点到的地方。
// value 不是合法日期时从今天开始。
export function Calendar({ label, value, onPick, onClose }: { label: string; value: string; onPick: (ymd: string) => void; onClose: (returnFocus: boolean) => void }) {
  const selected = isDate(value) ? value : "";
  const [focus, setFocus] = useState(() => selected || today());
  const keyboard = useRef(true);
  const root = useRef<HTMLDivElement>(null);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  useEffect(() => {
    if (!keyboard.current) return;
    root.current?.querySelector<HTMLButtonElement>(`[data-ymd="${focus}"]`)?.focus();
  }, [focus]);
  useEffect(() => {
    // 所属控件（含打开弹层的按钮）之内的点击由控件自己处理，否则按钮的 mousedown 先关、click 又开。
    const owner = root.current?.closest(".date-box") ?? root.current;
    const onPointer = (event: PointerEvent) => { if (owner && !owner.contains(event.target as Node)) closeRef.current(false); };
    document.addEventListener("pointerdown", onPointer, true);
    return () => document.removeEventListener("pointerdown", onPointer, true);
  }, []);
  // 0001–9999 年之外 toISOString 会写成六位年份；翻到范围外时停在原地。
  const move = (next: string, byKeyboard: boolean) => { if (!isDate(next)) return; keyboard.current = byKeyboard; setFocus(next); };
  const onKeyDown = (event: KeyboardEvent) => {
    const steps: Record<string, () => string> = {
      ArrowLeft: () => addDays(focus, -1), ArrowRight: () => addDays(focus, 1),
      ArrowUp: () => addDays(focus, -7), ArrowDown: () => addDays(focus, 7),
      PageUp: () => addMonths(focus, -1), PageDown: () => addMonths(focus, 1),
      Home: () => addDays(focus, -weekdayOf(focus)), End: () => addDays(focus, 6 - weekdayOf(focus)),
    };
    if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); onClose(true); return; }
    const step = steps[event.key];
    if (!step || !(event.target instanceof HTMLButtonElement) || !event.target.dataset.ymd) return;
    event.preventDefault();
    move(step(), true);
  };
  const [year, month] = focus.split("-");
  const first = `${year}-${month}-01`;
  const start = addDays(first, -weekdayOf(first));
  const days = Array.from({ length: 42 }, (_, i) => addDays(start, i));
  const now = today();
  return (
    <div ref={root} className="calendar" role="dialog" aria-label={`选择${label}`} onKeyDown={onKeyDown}>
      <div className="calendar-head">
        <button type="button" className="icon-button" aria-label="上个月" onClick={() => move(addMonths(focus, -1), false)}><Icon name="chevronLeft" /></button>
        <span className="calendar-title" aria-live="polite">{Number(year)} 年 {Number(month)} 月</span>
        <button type="button" className="icon-button" aria-label="下个月" onClick={() => move(addMonths(focus, 1), false)}><Icon name="chevronRight" /></button>
      </div>
      <table className="calendar-grid">
        <thead><tr>{WEEKDAYS.map((w) => <th key={w} scope="col" abbr={`星期${w}`}>{w}</th>)}</tr></thead>
        <tbody>
          {Array.from({ length: 6 }, (_, row) => (
            <tr key={row}>
              {days.slice(row * 7, row * 7 + 7).map((ymd) => (
                <td key={ymd}>
                  {isDate(ymd) && <button type="button" data-ymd={ymd} aria-label={ymd} tabIndex={ymd === focus ? 0 : -1}
                    aria-pressed={ymd === selected} aria-current={ymd === now ? "date" : undefined}
                    data-outside={ymd.slice(0, 7) !== focus.slice(0, 7) || undefined}
                    onClick={() => onPick(ymd)}>{Number(ymd.slice(8))}</button>}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      <div className="calendar-foot"><button type="button" className="link" onClick={() => onPick(now)}>今天</button></div>
    </div>
  );
}
