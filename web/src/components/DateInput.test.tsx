import { useState } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { DateInput } from "./DateInput";

function Fixture({ initial = "", changed = (_value: string) => {} }) {
  const [value, setValue] = useState(initial);
  return <form aria-label="计费"><DateInput label="到期日" value={value} onChange={value => { setValue(value); changed(value); }} /></form>;
}
const segment = (name: string) => screen.getByRole("textbox", { name: `到期日 ${name}` });

it("四位年份进入月份，两位月份进入日期，输出标准日期", () => {
  const changed = vi.fn();
  render(<Fixture initial="2026-09-29" changed={changed} />);
  fireEvent.change(segment("年"), { target: { value: "203" } });
  expect(segment("月")).not.toHaveFocus();
  expect(screen.getByRole("form")).toBeInvalid();
  fireEvent.change(segment("年"), { target: { value: "2031" } });
  expect(segment("月")).toHaveFocus();
  fireEvent.change(segment("月"), { target: { value: "12" } });
  expect(segment("日")).toHaveFocus();
  fireEvent.change(segment("日"), { target: { value: "25" } });
  expect(changed).toHaveBeenLastCalledWith("2031-12-25");
  expect(screen.getByRole("form")).toBeValid();
});

it("年份不能超过四位，月日不能超过两位", () => {
  render(<Fixture />);
  for (const [name, input, result, limit] of [["年", "203012", "2030", "4"], ["月", "123", "12", "2"], ["日", "251", "25", "2"]]) {
    fireEvent.change(segment(name), { target: { value: input } });
    expect(segment(name)).toHaveValue(result);
    expect(segment(name)).toHaveAttribute("maxlength", limit);
  }
});

it.each(["2031-02-29", "2032-04-31", "0000-01-01", "2032-13-01", "2032-01-00", "203-01-01", "2032--01"])("不允许保存无效日期 %s", initial => {
  render(<Fixture initial={initial} />);
  expect(screen.getByRole("form")).toBeInvalid();
});

it.each(["2032-02-29", "0001-01-01", "9999-12-31", ""])("允许保存日期 %s", initial => {
  render(<Fixture initial={initial} />);
  expect(screen.getByRole("form")).toBeValid();
});

it("清空全部分段输出空值，而不是残缺日期", () => {
  const changed = vi.fn();
  render(<Fixture initial="2032-02-29" changed={changed} />);
  for (const name of ["年", "月", "日"]) fireEvent.change(segment(name), { target: { value: "" } });
  expect(changed).toHaveBeenLastCalledWith("");
  expect(screen.getByRole("form")).toBeValid();
});

it("日历选择与清除同步全部分段", () => {
  const changed = vi.fn();
  render(<Fixture changed={changed} />);
  fireEvent.change(screen.getByLabelText("到期日", { selector: "input" }), { target: { value: "2032-02-29" } });
  expect(["年", "月", "日"].map(name => (segment(name) as HTMLInputElement).value)).toEqual(["2032", "02", "29"]);
  fireEvent.click(screen.getByRole("button", { name: "清除到期日" }));
  expect(["年", "月", "日"].map(name => (segment(name) as HTMLInputElement).value)).toEqual(["", "", ""]);
  expect(changed).toHaveBeenLastCalledWith("");
});
