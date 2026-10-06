import { useEffect, useRef } from "react";
import uPlot, { type AlignedData, type Options } from "uplot";
import "uplot/dist/uPlot.min.css";
import { formatUnit } from "../lib/format";
import { axisValues } from "../lib/axis";
import { formatChartTimes, isolatedPointIndices, readingGap, type ReadingGap } from "../lib/chartMarks";
import { resolveColor, useColorScheme } from "../lib/colorScheme";

// 一个节点常有多条探测线，八色减少颜色重复；超过八条时循环使用。
const palette = ["#3b82f6", "#f59e0b", "#10b981", "#ef4444", "#8b5cf6", "#06b6d4", "#84cc16", "#ec4899"];

function yColumns(data: AlignedData): (readonly (number | null | undefined)[])[] {
  const cols: (readonly (number | null | undefined)[])[] = [];
  for (let i = 1; i < data.length; i++) cols.push(data[i] as readonly (number | null | undefined)[]);
  return cols;
}

// 文案与「窗口内没有探测结果」同一语气。图级与序列级分开：全部没有读数时不把每条序列的名字再列一遍。
function gapMessage(gap: ReadingGap): string | null {
  if (gap.kind === "chart") return "窗口内没有读数";
  if (gap.kind === "series") return `${gap.labels.join("、")}：窗口内没有读数`;
  return null;
}

// spanGaps 关闭：null 是无读数，线在这里必须断开而不是把两侧连起来。
// 时间不传时区：formatChartTimes 与 uPlot 的刻度对齐都用浏览器本地时区，不引入 hub 时区。
export function Chart({ data, labels, unit, height = 180 }: { data: AlignedData; labels: string[]; unit: string; height?: number }) {
  const el = useRef<HTMLDivElement>(null);
  const plot = useRef<uPlot | null>(null);
  const initialData = useRef(data);
  const key = labels.join("|");
  const scheme = useColorScheme();
  const gap = gapMessage(readingGap(yColumns(data), labels));
  useEffect(() => {
    const host = el.current;
    if (!host) return;
    const axisColor = resolveColor(host, "var(--muted)");
    const gridColor = resolveColor(host, "var(--line)");
    const axisStyle = { stroke: axisColor, grid: { stroke: gridColor }, ticks: { stroke: axisColor } };
    const opts: Options = {
      width: host.clientWidth || 600,
      height,
      // 不设置 legend.show：uPlot 默认显示图例，点击一项切换该条线。对比图一条线一个节点，靠的就是这个。
      scales: { x: { time: true }, y: unit === "percent" ? { range: [0, 100] } : {} },
      axes: [
        { ...axisStyle, values: (_u, splits, _axisIdx, _foundSpace, foundIncr) => formatChartTimes(splits, { incrSec: foundIncr }) },
        { ...axisStyle, size: 80, values: (_u, vals) => axisValues(vals, unit) },
      ],
      series: [
        {
          label: "时间",
          value: (_u: uPlot, v: number | null) => (v == null || !Number.isFinite(v) ? "–" : formatChartTimes([v])[0]),
        },
        ...labels.map((label, i) => ({
          label,
          stroke: palette[i % palette.length],
          width: 1.5,
          spanGaps: false,
          // 默认点填充是白色，浅色背景上只剩细描边。填成与线相同的颜色，孤立读数才是一粒看得见的实心点。
          // filter 必须返回数组：返回 null 时 uPlot 会把可见范围内的每个点都画出来。
          points: {
            show: true,
            fill: palette[i % palette.length],
            filter: (u: uPlot, seriesIdx: number) => isolatedPointIndices(u.data[seriesIdx] as readonly (number | null | undefined)[]),
          },
          value: (_u: uPlot, v: number | null) => (v === null ? "–" : formatUnit(v, unit)),
        })),
      ],
    };
    plot.current = new uPlot(opts, initialData.current, host);
    const ro = new ResizeObserver(() => plot.current?.setSize({ width: host.clientWidth, height }));
    ro.observe(host);
    return () => {
      ro.disconnect();
      plot.current?.destroy();
      plot.current = null;
    };
    // 标签、单位、尺寸或明暗改变才重建；下面的数据 effect 维护最近提交的数据快照并应用当前数据。
  }, [key, unit, height, scheme]);
  useEffect(() => {
    initialData.current = data;
    plot.current?.setData(data);
  }, [data]);
  // uPlot 把自己的根节点 append 进宿主。提示若也放在宿主里，图建好之后才出现的提示会被 React 追加到
  // uPlot 根之后，位置就随提示与图谁先出现而变；所以宿主只交给 uPlot，提示放在宿主之前。
  return (
    <div className="chart">
      {gap && <p className="muted chart-gap">{gap}</p>}
      <div ref={el} />
    </div>
  );
}
