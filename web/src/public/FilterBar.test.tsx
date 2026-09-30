import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { expect, it } from "vitest";
import { FilterBar } from "./FilterBar";

it.each([
  { label: "按标签筛选", values: ["家宽", "机房"] },
  { label: "按地区筛选", values: ["HK", ""] },
])("$label 使用相同的单选、多选和全部交互", ({ label, values }) => {
  function Harness() {
    const [selected, setSelected] = useState<string[]>([]);
    return <FilterBar label={label} options={values.map(value => ({ value, label: value || "未知" }))} selected={selected} onChange={setSelected} />;
  }
  render(<Harness />);
  const [all, first, second] = screen.getAllByRole("button");
  const selected = () => screen.getAllByRole("button").filter(button => button.getAttribute("aria-pressed") === "true");
  expect(selected()).toEqual([all]);
  fireEvent.click(first);
  expect(selected()).toEqual([first]);
  fireEvent.click(second);
  expect(selected()).toEqual([second]);
  fireEvent.click(second);
  expect(selected()).toEqual([all]);
  fireEvent.click(first);
  fireEvent.click(second, { shiftKey: true });
  expect(selected()).toEqual([first, second]);
  fireEvent.click(second, { shiftKey: true });
  expect(selected()).toEqual([first]);
  fireEvent.click(second, { shiftKey: true });
  fireEvent.click(first);
  expect(selected()).toEqual([first]);
  fireEvent.click(second, { shiftKey: true });
  fireEvent.click(all);
  expect(selected()).toEqual([all]);
  fireEvent.click(second, { shiftKey: true });
  expect(selected()).toEqual([second]);
  fireEvent.click(second, { shiftKey: true });
  expect(selected()).toEqual([all]);
});
