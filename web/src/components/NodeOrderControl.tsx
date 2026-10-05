import { type DragEvent, type KeyboardEvent, useCallback } from "react";
import { type OrderMove } from "../api/useOrder";
import { Icon } from "./Icon";

// 排序入口分两种可用性：reorderDisabled 关闭拖动 / 键盘 / 上下移（它们要保存完整排列，需要未过滤的完整列表），
// moveDisabled 只关闭「移动到…」（它按全序名次走 MoveNodes，过滤时也可用）。两者都不可用时整个菜单才关闭。
// position 是行首序号：未过滤时是期望排列里的位次，过滤时是服务端 Node.position（全序名次，不随过滤变化）。
export function NodeOrderControl({ label, index, count, position, reorderDisabled, moveDisabled, onMove, onMoveTo, onDragStart, onDragEnd }: {
  label: string; index: number; count: number; position: number; reorderDisabled: boolean; moveDisabled: boolean;
  onMove: (move: OrderMove) => void; onMoveTo: (opener: HTMLElement) => void;
  onDragStart: (event: DragEvent<HTMLButtonElement>) => void; onDragEnd: (event: DragEvent<HTMLButtonElement>) => void;
}) {
  const locked = reorderDisabled || count < 2;
  const onKeyDown = useCallback((event: KeyboardEvent<HTMLButtonElement>) => {
    if (locked) return;
    if (event.key === "ArrowUp") { event.preventDefault(); onMove(-1); }
    else if (event.key === "ArrowDown") { event.preventDefault(); onMove(1); }
    else if (event.key === "Home") { event.preventDefault(); onMove("first"); }
    else if (event.key === "End") { event.preventDefault(); onMove("last"); }
  }, [locked, onMove]);
  return <div className="node-order-control">
    <button type="button" className="icon-button order-handle" aria-label={`调整顺序 ${label}`} aria-describedby="node-order-help"
      title="拖动调整顺序；方向键上移/下移，Home 置顶，End 置底" disabled={locked} draggable={!locked}
      onKeyDown={onKeyDown} onDragStart={onDragStart} onDragEnd={onDragEnd}>
      <Icon name="grip" /><span className="order-position">{position}</span>
    </button>
    <select className="order-menu" aria-label={`移动 ${label}`} disabled={locked && moveDisabled} value="" onChange={(event) => {
      const value = event.target.value;
      if (value === "move") {
        // 菜单在「移动到…」可用时保持打开；触发元素（本菜单）交给弹窗，关闭后焦点回到它。
        if (!moveDisabled) onMoveTo(event.target);
        return;
      }
      if (locked) return;
      // 越界的上下移选项虽然 disabled，仍在这里再拦一道。
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
