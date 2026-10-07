// 迷你趋势线（设计 §2）：只给网络速率用。null 是无读数，线在这里断开而不是连过去（与 Chart 的 spanGaps 关闭同一规则）；
// 恒为 0 是一条贴底的线，不是无读数。y 按整组有限值的最大值归一，没有坐标轴，所以只表达走势不表达量级。
export function Sparkline({ values, label, width = 120, height = 28 }: {
  values: readonly (number | null | undefined)[]; label: string; width?: number; height?: number;
}) {
  const finite = values.filter((v): v is number => typeof v === "number" && Number.isFinite(v));
  const max = finite.length > 0 ? Math.max(...finite) : 0;
  const step = values.length > 1 ? width / (values.length - 1) : 0;
  const segments: string[] = [];
  let pen = false;
  values.forEach((v, i) => {
    if (typeof v !== "number" || !Number.isFinite(v)) {
      pen = false;
      return;
    }
    const x = (i * step).toFixed(1);
    const y = (height - 1 - (max > 0 ? (v / max) * (height - 2) : 0)).toFixed(1);
    segments.push(`${pen ? "L" : "M"}${x} ${y}`);
    pen = true;
  });
  return (
    <svg className="sparkline" role="img" aria-label={label} width={width} height={height} viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none">
      {segments.length > 0
        ? <path d={segments.join(" ")} fill="none" stroke="currentColor" strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
        : <text x="2" y={height - 8} fontSize="10">无读数</text>}
    </svg>
  );
}
