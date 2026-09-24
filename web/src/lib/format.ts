const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];

// 二进制单位：内存与磁盘的读数都是字节，运维习惯 GiB 而非 GB。
export function bytes(n: number | bigint): string {
  let x = Number(n);
  let i = 0;
  while (x >= 1024 && i < units.length - 1) {
    x /= 1024;
    i++;
  }
  const digits = i > 0 && x < 10 ? 1 : 0;
  return `${x.toFixed(digits)} ${units[i]}`;
}

export function percent(v: number): string {
  return `${v.toFixed(v < 10 ? 1 : 0)}%`;
}

export function duration(seconds: number | bigint): string {
  let s = Number(seconds);
  const d = Math.floor(s / 86400);
  s -= d * 86400;
  const h = Math.floor(s / 3600);
  s -= h * 3600;
  const m = Math.floor(s / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

// 用 hub 给的 now 而不是浏览器时钟：客户机时钟偏了也不会显示"负几秒前"。
export function ago(unixSeconds: number | bigint, now: number | bigint): string {
  const diff = Math.max(0, Number(now) - Number(unixSeconds));
  if (diff < 5) return "刚刚";
  if (diff < 60) return `${diff} 秒前`;
  if (diff < 3600) return `${Math.floor(diff / 60)} 分钟前`;
  if (diff < 86400) return `${Math.floor(diff / 3600)} 小时前`;
  return `${Math.floor(diff / 86400)} 天前`;
}

// 单位随数据来（MetricSeries.unit），显示层不查表。
export function formatUnit(v: number, unit: string): string {
  switch (unit) {
    case "ms":
      return `${v.toFixed(v < 10 ? 2 : v < 100 ? 1 : 0)} ms`;
    case "percent":
      return percent(v);
    case "bytes":
      return bytes(v);
    case "bytes/s":
      return `${bytes(v)}/s`;
    case "count":
      return String(Math.round(v));
    default:
      return v.toFixed(2);
  }
}
