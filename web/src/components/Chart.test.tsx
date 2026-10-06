import { act, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import uPlot, { type AlignedData, type Options } from "uplot";
import { formatChartTimes, isolatedPointIndices } from "../lib/chartMarks";
import { currentScheme } from "../lib/colorScheme";
import { Chart } from "./Chart";

const plots = vi.hoisted(() => [] as { options: Options; data: AlignedData; setData: ReturnType<typeof vi.fn>; destroy: ReturnType<typeof vi.fn> }[]);
// 与真实 uPlot 一样：构造时把根节点 append 进宿主，destroy 时移除，DOM 里的相对位置才与浏览器一致。
vi.mock("uplot", () => ({ default: class {
  setData = vi.fn();
  setSize = vi.fn();
  root = Object.assign(document.createElement("div"), { className: "uplot" });
  destroy = vi.fn(() => this.root.remove());
  constructor(public options: Options, public data: AlignedData, host?: HTMLElement) {
    host?.appendChild(this.root);
    plots.push(this);
  }
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
  // 断言失败时也先卸载：挂着的订阅会在 afterEach 撤掉 matchMedia 替身后再读一次系统设置，抛出的未捕获异常盖住真正的失败原因。
  try {
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
  } finally {
    unmount();
  }
});

function mountChart(props: { data: AlignedData; labels: string[]; unit: string }) {
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }));
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  return render(<Chart {...props} />);
}

it("全是 null 时给出图级无读数提示，恒为 0 不提示", () => {
  const { rerender, unmount } = mountChart({
    data: [[0, 60], [null, null], [null, null]],
    labels: ["CPU 均值", "CPU 峰值"],
    unit: "percent",
  });
  try {
    expect(screen.getByText("窗口内没有读数")).toBeInTheDocument();
    rerender(<Chart data={[[0, 60], [0, 0], [0, 0]]} labels={["CPU 均值", "CPU 峰值"]} unit="percent" />);
    expect(screen.queryByText(/没有读数/)).not.toBeInTheDocument();
  } finally {
    unmount();
  }
});

it("只有部分序列全是 null 时点名这些序列，有读数的序列不出现在提示里", () => {
  const { unmount } = mountChart({
    data: [[0, 60], [1, null], [null, null]],
    labels: ["CPU 均值", "CPU 峰值"],
    unit: "percent",
  });
  try {
    expect(screen.getByText("CPU 峰值：窗口内没有读数")).toBeInTheDocument();
    expect(screen.queryByText("窗口内没有读数")).not.toBeInTheDocument();
    expect(screen.queryByText(/CPU 均值/)).not.toBeInTheDocument();
  } finally {
    unmount();
  }
});

it("无读数提示在图的上方，与提示和图谁先出现无关", () => {
  const { container, rerender, unmount } = mountChart({ data: [[0, 60], [1, 2]], labels: ["cpu"], unit: "count" });
  const hintPrecedesPlot = () => {
    const root = container.querySelector(".uplot");
    if (!root) throw new Error("图没有挂进宿主");
    return Boolean(screen.getByText("窗口内没有读数").compareDocumentPosition(root) & Node.DOCUMENT_POSITION_FOLLOWING);
  };
  try {
    // 图先建好，提示后出现。
    rerender(<Chart data={[[0, 60], [null, null]]} labels={["cpu"]} unit="count" />);
    expect(hintPrecedesPlot()).toBe(true);
    // 提示已在，图因标签变化重建。
    rerender(<Chart data={[[0, 60], [null, null]]} labels={["mem"]} unit="count" />);
    expect(plots).toHaveLength(2);
    expect(hintPrecedesPlot()).toBe(true);
  } finally {
    unmount();
  }
});

it("横轴与图例走本地 24 小时格式，点过滤只留下孤立读数", () => {
  const { unmount } = mountChart({ data: [[0, 60], [1, 2]], labels: ["cpu"], unit: "count" });
  try {
    const opts = plots.at(-1)!.options;
    expect(opts.series?.[0]?.label).toBe("时间");
    const legend = opts.series?.[0]?.value;
    if (typeof legend !== "function") throw new Error("图例 value 不是函数");
    const ts = Date.parse("2026-10-06T14:05:00+08:00") / 1000;
    expect(legend({} as uPlot, ts, 0, 0)).toBe(formatChartTimes([ts])[0]);
    expect(String(legend({} as uPlot, ts, 0, 0))).not.toMatch(/am|pm/);
    expect(legend({} as uPlot, null as unknown as number, 0, null)).toBe("–");
    const axis = opts.axes?.[0]?.values;
    if (typeof axis !== "function") throw new Error("横轴 values 不是函数");
    const ticks = [ts, ts + 600];
    const labels = axis({} as uPlot, ticks, 0, 40, 600);
    expect(labels).toEqual(formatChartTimes(ticks, { incrSec: 600 }));
    expect(labels.join(" ")).not.toMatch(/am|pm|\d\/\d\/\d/);
    const filter = opts.series?.[1]?.points?.filter;
    if (typeof filter !== "function") throw new Error("points.filter 不是函数");
    const sandwiched = [1, 2, null, 5, null, 3, 4];
    expect(filter({ data: [[], sandwiched] } as unknown as uPlot, 1, true)).toEqual(isolatedPointIndices(sandwiched));
    expect(filter({ data: [[], [1, 2, 3, 4]] } as unknown as uPlot, 1, true)).toEqual([]);
  } finally {
    unmount();
  }
});
