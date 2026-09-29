import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { NodeOrderControl } from "./NodeOrderControl";

describe("节点移动控件", () => {
  it("键盘和移动菜单使用同一移动入口，不触发其他行操作", () => {
    const move = vi.fn();
    render(<NodeOrderControl label="a" index={1} count={3} disabled={false} onMove={move} onDragStart={vi.fn()} onDragEnd={vi.fn()} />);
    const handle = screen.getByRole("button", { name: "调整顺序 a" });
    for (const key of ["ArrowUp", "ArrowDown", "Home", "End"]) fireEvent.keyDown(handle, { key });
    expect(move.mock.calls).toEqual([[-1], [1], ["first"], ["last"]]);
    fireEvent.change(screen.getByRole("combobox", { name: "移动 a" }), { target: { value: "last" } });
    expect(move).toHaveBeenLastCalledWith("last");
    expect(screen.getByRole("combobox", { name: "移动 a" })).toHaveValue("");
  });

  it("首尾禁用越界移动，筛选时同时关闭拖拽、键盘与菜单", () => {
    const move = vi.fn();
    const props = { label: "a", count: 3, onMove: move, onDragStart: vi.fn(), onDragEnd: vi.fn() };
    const { rerender } = render(<NodeOrderControl {...props} index={0} disabled={false} />);
    expect(screen.getByRole("option", { name: "上移一位" })).toBeDisabled();
    expect(screen.getByRole("option", { name: "置顶" })).toBeDisabled();
    rerender(<NodeOrderControl {...props} index={2} disabled={false} />);
    expect(screen.getByRole("option", { name: "下移一位" })).toBeDisabled();
    expect(screen.getByRole("option", { name: "置底" })).toBeDisabled();
    rerender(<NodeOrderControl {...props} index={1} disabled />);
    const handle = screen.getByRole("button", { name: "调整顺序 a" });
    expect(handle).toBeDisabled();
    expect(handle).toHaveAttribute("draggable", "false");
    expect(screen.getByRole("combobox", { name: "移动 a" })).toBeDisabled();
    fireEvent.keyDown(handle, { key: "Home" });
    expect(move).not.toHaveBeenCalled();
  });
});
