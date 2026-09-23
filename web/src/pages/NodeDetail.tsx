import { useQuery } from "@connectrpc/connect-query";
import { keepPreviousData } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { Link, useParams } from "react-router";
import { Chart } from "../components/Chart";
import { AdminService } from "../gen/probe/v1/admin_pb";
import { toAligned, unitOf } from "../lib/series";
import { errorText } from "../api/auth";

export const RANGES = [
  { label: "1h", seconds: 3600 },
  { label: "6h", seconds: 6 * 3600 },
  { label: "24h", seconds: 86400 },
  { label: "7d", seconds: 7 * 86400 },
  { label: "30d", seconds: 30 * 86400 },
];

// 每个面板画哪些指标；名字与 hub 的描述表一致，单位随数据来。
const PANELS: { title: string; names: string[] }[] = [
  { title: "CPU", names: ["cpu"] },
  { title: "内存 / 交换", names: ["mem_used", "swap_used"] },
  { title: "磁盘", names: ["disk_used"] },
  { title: "负载（1 分钟）", names: ["load1"] },
  { title: "连接数", names: ["tcp", "udp"] },
  { title: "进程数", names: ["procs"] },
];

const REFRESH_MS = 60_000;

export function NodeDetail() {
  const { id } = useParams();
  const validId = /^\d+$/.test(id ?? "");
  const nodeId = validId ? BigInt(id!) : 0n;
  const [range, setRange] = useState(RANGES[2]);
  // 窗口右端每分钟前进一次：历史行本来就按分钟产生，更频繁的刷新看不到新东西。
  const [to, setTo] = useState(() => Math.floor(Date.now() / 1000) + 60);
  useEffect(() => {
    const t = setInterval(() => setTo(Math.floor(Date.now() / 1000) + 60), REFRESH_MS);
    return () => clearInterval(t);
  }, []);
  const from = to - range.seconds;

  const nodes = useQuery(AdminService.method.listNodes, {}, { enabled: validId });
  const node = nodes.data?.nodes.find((n) => n.id === nodeId);
  const history = useQuery(AdminService.method.queryMetrics, { nodeId, from: BigInt(from), to: BigInt(to), maxPoints: 1000 }, {
    enabled: validId, placeholderData: keepPreviousData,
  });
  const charts = useMemo(
    () => history.data ? PANELS.map((p) => ({ ...p, data: toAligned(history.data, p.names, from, to), unit: unitOf(history.data, p.names[0]) })) : [],
    [history.data, from, to],
  );

  if (!validId || (nodes.data && !node)) return <p role="alert" className="error">节点 {id} 不存在。<Link to="/">返回总览</Link></p>;
  if (nodes.error) return <p role="alert" className="error">{errorText(nodes.error)}</p>;
  return (
    <section>
      <header className="row detail-header">
        <h1>{node?.name ?? "…"}</h1>
        <nav aria-label="时间窗口">
          {RANGES.map((r) => (
            <button key={r.label} type="button" className={r.label === range.label ? "active" : "link"} onClick={() => setRange(r)} aria-pressed={r.label === range.label}>
              {r.label}
            </button>
          ))}
        </nav>
        {history.data && <span className="muted">级别 {history.data.level}，每点 {history.data.stepS}s</span>}
      </header>
      {history.error && <p role="alert" className="error">{errorText(history.error)}</p>}
      <div className="grid">
        {charts.map((c) => (
          <div className="card" key={c.title}>
            <h2>{c.title}</h2>
            <Chart data={c.data} labels={c.names} unit={c.unit} />
          </div>
        ))}
      </div>
      {node?.facts && (
        <dl className="card facts">
          <dt>主机名</dt><dd>{node.facts.hostname}</dd>
          <dt>系统</dt><dd>{node.facts.os}</dd>
          <dt>内核</dt><dd>{node.facts.kernel}</dd>
          <dt>架构</dt><dd>{node.facts.arch}</dd>
          <dt>CPU</dt><dd>{node.facts.cpuModel} × {node.facts.cpuCores}</dd>
          <dt>虚拟化</dt><dd>{node.facts.virtualization || "无 / 未知"}</dd>
          <dt>agent</dt><dd>{node.facts.agentVersion}</dd>
        </dl>
      )}
    </section>
  );
}
