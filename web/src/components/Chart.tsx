import { useEffect, useRef } from "react";
import uPlot, { type AlignedData, type Options } from "uplot";
import "uplot/dist/uPlot.min.css";
import { formatUnit } from "../lib/format";

const palette = ["#3b82f6", "#f59e0b", "#10b981", "#ef4444"];

// spanGaps 关闭：null 是无读数，线在这里必须断开而不是把两侧连起来。
export function Chart({ data, labels, unit, height = 180 }: { data: AlignedData; labels: string[]; unit: string; height?: number }) {
  const el = useRef<HTMLDivElement>(null);
  const plot = useRef<uPlot | null>(null);
  const initialData = useRef(data);
  const key = labels.join("|");
  useEffect(() => {
    const host = el.current;
    if (!host) return;
    const opts: Options = {
      width: host.clientWidth || 600,
      height,
      scales: { x: { time: true }, y: unit === "percent" ? { range: [0, 100] } : {} },
      axes: [{}, { size: 80, values: (_u, vals) => vals.map((v) => formatUnit(v, unit)) }],
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
    // data 的更新走 setData，不重建图表。
  }, [key, unit, height]);
  useEffect(() => {
    initialData.current = data;
    plot.current?.setData(data);
  }, [data]);
  return <div ref={el} className="chart" />;
}
