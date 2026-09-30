import type { DragEventHandler } from "react";
import type { OrderMove } from "../api/useOrder";
import { Icon } from "./Icon";

export function NodeOrderControl({ label, index, count, disabled, onMove, onDragStart, onDragEnd }: {
  label: string; index: number; count: number; disabled: boolean;
  onMove: (move: OrderMove) => void;
  onDragStart: DragEventHandler<HTMLButtonElement>; onDragEnd: DragEventHandler<HTMLButtonElement>;
}) {
  const locked = disabled || count < 2;
  return <div className="node-order-control">
    <button type="button" className="icon-button order-handle" aria-label={`调整顺序 ${label}`} aria-describedby="node-order-help"
      title="拖动调整顺序；方向键上移/下移，Home 置顶，End 置底" disabled={locked} draggable={!locked}
      onDragStart={onDragStart} onDragEnd={onDragEnd}
      onKeyDown={(event) => {
        if (locked) return;
        const move = { ArrowUp: -1, ArrowDown: 1, Home: "first", End: "last" }[event.key] as OrderMove | undefined;
        if (move !== undefined) { event.preventDefault(); onMove(move); }
      }}><Icon name="grip" /><span className="order-position">{index + 1}</span></button>
    <select className="order-menu" aria-label={`移动 ${label}`} disabled={locked} value="" onChange={(event) => {
      const value = event.target.value;
      if (locked) return;
      if (value === "first" || value === "last") onMove(value);
      else if (value === "up") onMove(-1);
      else if (value === "down") onMove(1);
    }}>
      <option value="" disabled>移动</option>
      <option value="up" disabled={index === 0}>上移一位</option>
      <option value="down" disabled={index === count - 1}>下移一位</option>
      <option value="first" disabled={index === 0}>置顶</option>
      <option value="last" disabled={index === count - 1}>置底</option>
    </select>
  </div>;
}
