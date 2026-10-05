import type { DragEventHandler } from "react";
import type { OrderMove } from "../api/useOrder";
import { Icon } from "./Icon";

// position 是行首序号：显示的是当前条件的完整列表（未收窄）时用期望排列的位次，否则用服务端
// position（全序名次，过滤与沿用旧结果都不重排）。排序四项走 ReorderNodes 的完整排列，
// 「移动到…」走 MoveNodes 的名次移动，两种可用性分开由 reorderDisabled / moveDisabled 表达。
export function NodeOrderControl({ label, index, count, position, reorderDisabled, moveDisabled, onMove, onMoveTo, onDragStart, onDragEnd }: {
  label: string; index: number; count: number; position: number; reorderDisabled: boolean; moveDisabled: boolean;
  onMove: (move: OrderMove) => void;
  onMoveTo: (opener: HTMLElement) => void;
  onDragStart: DragEventHandler<HTMLButtonElement>; onDragEnd: DragEventHandler<HTMLButtonElement>;
}) {
  const locked = reorderDisabled || count < 2;
  // 菜单只要有一项可用就保持可用；排序四项各自按 locked 禁用。越界或原地不动的移动由 useOrder.move 丢弃，
  // 这里的分派不另做判断。
  const menuLocked = locked && moveDisabled;
  return <div className="node-order-control">
    <button type="button" className="icon-button order-handle" aria-label={`调整顺序 ${label}`} aria-describedby="node-order-help"
      title="拖动调整顺序；方向键上移/下移，Home 置顶，End 置底" disabled={locked} draggable={!locked}
      onDragStart={onDragStart} onDragEnd={onDragEnd}
      onKeyDown={(event) => {
        if (locked) return;
        const move = { ArrowUp: -1, ArrowDown: 1, Home: "first", End: "last" }[event.key] as OrderMove | undefined;
        if (move !== undefined) { event.preventDefault(); onMove(move); }
      }}><Icon name="grip" /><span className="order-position">{position}</span></button>
    <select className="order-menu" aria-label={`移动 ${label}`} disabled={menuLocked} value="" onChange={(event) => {
      const value = event.target.value;
      if (value === "move") { if (!moveDisabled) onMoveTo(event.target); return; }
      if (locked) return;
      if (value === "first" || value === "last") onMove(value);
      else if (value === "up") onMove(-1);
      else if (value === "down") onMove(1);
    }}>
      <option value="" disabled>移动</option>
      <option value="up" disabled={locked || index === 0}>上移一位</option>
      <option value="down" disabled={locked || index === count - 1}>下移一位</option>
      <option value="first" disabled={locked || index === 0}>置顶</option>
      <option value="last" disabled={locked || index === count - 1}>置底</option>
      <option value="move" disabled={moveDisabled}>移动到…</option>
    </select>
  </div>;
}
