// 无读数与 0 是两个事实：缺失的字段显示为破折号，不画成 0。
export function Missing() {
  return <span className="muted" aria-label="无读数">–</span>;
}

export function Bar({ value, label }: { value: number; label: string }) {
  const v = Math.max(0, Math.min(100, value));
  return (
    <div className="bar" role="meter" aria-valuenow={Math.round(v)} aria-valuemin={0} aria-valuemax={100} aria-label={label}>
      <div className="fill" style={{ width: `${v}%` }} />
      <span>{label}</span>
    </div>
  );
}

export function ratio(used: bigint, total: bigint): number {
  return (Number(used) / Number(total)) * 100;
}
