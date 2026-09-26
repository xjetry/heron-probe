import { useEffect, useRef } from "react";
import uPlot, { type AlignedData, type Options } from "uplot";
import "uplot/dist/uPlot.min.css";
import { formatUnit } from "../lib/format";
import { axisValues } from "../lib/axis";
import { resolveColor, useColorScheme } from "../lib/colorScheme";

// 一个节点常有多条探测线，八色减少颜色重复；超过八条时循环使用。
const palette = ["#3b82f6", "#f59e0b", "#10b981", "#ef4444", "#8b5cf6", "#06b6d4", "#84cc16", "#ec4899"];

// spanGaps 关闭：null 是无读数，线在这里必须断开而不是把两侧连起来。
export function Chart({ data, labels, unit, height = 180 }: { data: AlignedData; labels: string[]; unit: string; height?: number }) {
  const el = useRef<HTMLDivElement>(null);
  const plot = useRef<uPlot | null>(null);
  const initialData = useRef(data);
  const key = labels.join("|");
  const scheme = useColorScheme();
  useEffect(() => {
    const host = el.current;
    if (!host) return;
    const axisColor = resolveColor(host, "var(--muted)");
    const gridColor = resolveColor(host, "var(--line)");
    const axisStyle = { stroke: axisColor, grid: { stroke: gridColor }, ticks: { stroke: axisColor } };
    const opts: Options = {
      width: host.clientWidth || 600,
      height,
      scales: { x: { time: true }, y: unit === "percent" ? { range: [0, 100] } : {} },
      axes: [{ ...axisStyle }, { ...axisStyle, size: 80, values: (_u, vals) => axisValues(vals, unit) }],
      series: [
        {},
        ...labels.map((label, i) => ({
          label,
          stroke: palette[i % palette.length],
          width: 1.5,
          spanGaps: false,
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
  return <div ref={el} className="chart" />;
}
