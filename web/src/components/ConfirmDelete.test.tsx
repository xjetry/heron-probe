import { expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { ConfirmDelete } from "./ConfirmDelete";

it("首击只进入确认态，确认才执行", () => {
  const onDelete = vi.fn();
  render(<ConfirmDelete label="删除 东京" confirm="确认删除 东京" pending={false} onDelete={onDelete} />);
  fireEvent.click(screen.getByRole("button", { name: "删除 东京" }));
  expect(onDelete).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "确认删除 东京" }));
  expect(onDelete).toHaveBeenCalledTimes(1);
});

it("执行挂起时确认按钮禁用", () => {
  render(<ConfirmDelete label="删除 东京" confirm="确认删除 东京" pending={true} onDelete={() => {}} />);
  fireEvent.click(screen.getByRole("button", { name: "删除 东京" }));
  expect(screen.getByRole("button", { name: "确认删除 东京" })).toBeDisabled();
});

it("取消回到首击按钮，全程不执行", () => {
  const onDelete = vi.fn();
  render(<ConfirmDelete label="删除 东京" confirm="确认删除 东京" pending={false} onDelete={onDelete} />);
  fireEvent.click(screen.getByRole("button", { name: "删除 东京" }));
  fireEvent.click(screen.getByRole("button", { name: "取消" }));
  expect(screen.getByRole("button", { name: "删除 东京" })).toBeInTheDocument();
  expect(onDelete).not.toHaveBeenCalled();
});
