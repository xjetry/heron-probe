import { act, render, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { AlignedData, Options } from "uplot";
import { currentScheme } from "../lib/colorScheme";
import { Chart } from "./Chart";

const plots = vi.hoisted(() => [] as { options: Options; data: AlignedData; setData: ReturnType<typeof vi.fn>; destroy: ReturnType<typeof vi.fn> }[]);
vi.mock("uplot", () => ({ default: class {
  setData = vi.fn();
  setSize = vi.fn();
  destroy = vi.fn();
  constructor(public options: Options, public data: AlignedData) { plots.push(this); }
} }));

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  delete document.documentElement.dataset.theme;
  plots.length = 0;
});

// jsdom 不解析 var() 与 light-dark()：按探针元素上写的值，给出浏览器在当前明暗下会解析出的颜色。
function stubResolvedColors() {
  const real = window.getComputedStyle.bind(window);
  vi.spyOn(window, "getComputedStyle").mockImplementation((el, pseudo) => {
    const dark = currentScheme() === "dark";
    const resolved: Record<string, string> = {
      "var(--muted)": dark ? "rgb(156, 163, 175)" : "rgb(107, 114, 128)",
      "var(--line)": dark ? "rgb(42, 47, 58)" : "rgb(229, 231, 235)",
    };
    const value = (el as HTMLElement).style?.color;
    return value in resolved ? ({ color: resolved[value] } as CSSStyleDeclaration) : real(el, pseudo);
  });
}

it("数据原地更新；系统或 data-theme 改变明暗时，用最新数据和解析后的配色重建", async () => {
  let dark = false;
  let changed = () => {};
  vi.stubGlobal("matchMedia", () => ({ get matches() { return dark; }, addEventListener: (_: string, listener: () => void) => { changed = listener; }, removeEventListener: vi.fn() }));
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  stubResolvedColors();
  const light = ["rgb(107, 114, 128)", "rgb(229, 231, 235)", "rgb(107, 114, 128)"];
  const darkColors = ["rgb(156, 163, 175)", "rgb(42, 47, 58)", "rgb(156, 163, 175)"];
  const { rerender, unmount } = render(<Chart data={[[0], [1]]} labels={["cpu"]} unit="count" />);
  const colors = () => plots.at(-1)!.options.axes!.map((axis) => [axis.stroke, axis.grid?.stroke, axis.ticks?.stroke]);
  expect(colors()).toEqual([light, light]);
  const next: AlignedData = [[0], [2]];
  rerender(<Chart data={next} labels={["cpu"]} unit="count" />);
  expect(plots).toHaveLength(1);
  expect(plots[0].setData).toHaveBeenLastCalledWith(next);
  act(() => { dark = true; changed(); });
  expect(colors()).toEqual([darkColors, darkColors]);
  expect(plots.at(-1)!.data).toEqual(next);
  // 站点设置强制浅色：html 的 data-theme 压过系统的深色。
  await act(async () => { document.documentElement.dataset.theme = "light"; });
  await waitFor(() => expect(colors()).toEqual([light, light]));
  expect(plots.at(-1)!.data).toEqual(next);
  unmount();
});
