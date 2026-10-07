import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { ThemeToggle } from "./ThemeToggle";

it("一个按钮循环三态，title 写明当前与下一档", () => {
  const onChange = vi.fn();
  render(<ThemeToggle choice="light" onChange={onChange} />);
  const button = screen.getByRole("button", { name: "明暗切换" });
  expect(button).toHaveAttribute("title", "当前：浅色，点击切到深色");
  fireEvent.click(button);
  expect(onChange).toHaveBeenCalledWith("dark");
});
