import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { NodeOrderControl } from "./NodeOrderControl";

describe("节点移动控件", () => {
  it("键盘和移动菜单使用同一移动入口，不触发其他行操作", () => {
    const move = vi.fn();
    render(<NodeOrderControl label="a" index={1} count={3} position={2} reorderDisabled={false} onMove={move} onDragStart={vi.fn()} onDragEnd={vi.fn()} />);
    const handle = screen.getByRole("button", { name: "调整顺序 a" });
    for (const key of ["ArrowUp", "ArrowDown", "Home", "End"]) fireEvent.keyDown(handle, { key });
    expect(move.mock.calls).toEqual([[-1], [1], ["first"], ["last"]]);
    fireEvent.change(screen.getByRole("combobox", { name: "移动 a" }), { target: { value: "last" } });
    expect(move).toHaveBeenLastCalledWith("last");
    expect(screen.getByRole("combobox", { name: "移动 a" })).toHaveValue("");
  });

  it("首尾禁用越界移动；序号显示传入的全序名次", () => {
    const props = { label: "a", onMove: vi.fn(), onDragStart: vi.fn(), onDragEnd: vi.fn(), reorderDisabled: false };
    const { rerender } = render(<NodeOrderControl {...props} index={0} count={3} position={1} />);
    expect(screen.getByRole("button", { name: "调整顺序 a" })).toHaveTextContent("1");
    expect(screen.getByRole("option", { name: "上移一位" })).toBeDisabled();
    expect(screen.getByRole("option", { name: "置顶" })).toBeDisabled();
    rerender(<NodeOrderControl {...props} index={2} count={3} position={3} />);
    expect(screen.getByRole("button", { name: "调整顺序 a" })).toHaveTextContent("3");
    expect(screen.getByRole("option", { name: "下移一位" })).toBeDisabled();
    expect(screen.getByRole("option", { name: "置底" })).toBeDisabled();
  });


  it("过滤时拖动与排序菜单关闭", () => {
    const move = vi.fn();
    render(<NodeOrderControl label="a" index={1} count={3} position={2} reorderDisabled onMove={move} onDragStart={vi.fn()} onDragEnd={vi.fn()} />);
    const handle = screen.getByRole("button", { name: "调整顺序 a" });
    expect(handle).toBeDisabled();
    expect(handle).toHaveAttribute("draggable", "false");
    const menu = screen.getByRole("combobox", { name: "移动 a" });
    expect(menu).toBeDisabled();
    for (const name of ["上移一位", "下移一位", "置顶", "置底"]) expect(screen.getByRole("option", { name })).toBeDisabled();
    fireEvent.keyDown(handle, { key: "Home" });
    fireEvent.change(menu, { target: { value: "up" } });
    expect(move).not.toHaveBeenCalled();
  });


  it("排序禁用时整个菜单关闭", () => {
    render(<NodeOrderControl label="a" index={1} count={3} position={2} reorderDisabled onMove={vi.fn()} onDragStart={vi.fn()} onDragEnd={vi.fn()} />);
    expect(screen.getByRole("button", { name: "调整顺序 a" })).toBeDisabled();
    expect(screen.getByRole("combobox", { name: "移动 a" })).toBeDisabled();
  });
});
