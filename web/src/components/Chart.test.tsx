import { act, render } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { AlignedData, Options } from "uplot";
import { Chart } from "./Chart";

const plots = vi.hoisted(() => [] as { options: Options; data: AlignedData; setData: ReturnType<typeof vi.fn>; destroy: ReturnType<typeof vi.fn> }[]);
vi.mock("uplot", () => ({ default: class {
  setData = vi.fn();
  setSize = vi.fn();
  destroy = vi.fn();
  constructor(public options: Options, public data: AlignedData) { plots.push(this); }
} }));

afterEach(() => { vi.unstubAllGlobals(); document.documentElement.removeAttribute("style"); plots.length = 0; });

it("数据原地更新，主题变化用最新数据和 CSS 配色重建", () => {
  let dark = false;
  let changed = () => {};
  vi.stubGlobal("matchMedia", () => ({ get matches() { return dark; }, addEventListener: (_: string, listener: () => void) => { changed = listener; }, removeEventListener: vi.fn() }));
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  const css = document.documentElement.style;
  css.setProperty("--muted", "#111111"); css.setProperty("--line", "#222222");
  const { rerender, unmount } = render(<Chart data={[[0], [1]]} labels={["cpu"]} unit="count" />);
  const colors = () => plots.at(-1)!.options.axes!.map((axis) => [axis.stroke, axis.grid?.stroke, axis.ticks?.stroke]);
  expect(colors()).toEqual(Array(2).fill(["#111111", "#222222", "#111111"]));
  const next: AlignedData = [[0], [2]];
  rerender(<Chart data={next} labels={["cpu"]} unit="count" />);
  expect(plots).toHaveLength(1);
  expect(plots[0].setData).toHaveBeenLastCalledWith(next);
  css.setProperty("--muted", "#eeeeee"); css.setProperty("--line", "#333333");
  act(() => { dark = true; changed(); });
  expect(colors()).toEqual(Array(2).fill(["#eeeeee", "#333333", "#eeeeee"]));
  expect(plots.at(-1)!.data).toEqual(next);
  unmount();
});
