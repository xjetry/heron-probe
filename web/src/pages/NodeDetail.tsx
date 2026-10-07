import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, type ReactNode, useState } from "react";
import { Link, useParams } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
import { HistoryCharts, RangePicker, useHistory, type HistoryMethods } from "../components/History";
import { AdminService, type GetTrafficResponse } from "../gen/heron/v1/admin_pb";
import { bytes } from "../lib/format";
import { errorText } from "../api/auth";
import { NodeAddresses } from "../components/NodeAddresses";
import { AgentDiagnostics } from "../components/AgentDiagnostics";
import { ExecutionScope } from "../components/ExecutionScope";
import { trafficText } from "../lib/traffic";
import { TRAFFIC_MS } from "../lib/poll";

const ADMIN_HISTORY: HistoryMethods = { queryMetrics: AdminService.method.queryMetrics, queryProbes: AdminService.method.queryProbes };

export function NodeDetail() {
  const { id } = useParams();
  const validId = /^\d+$/.test(id ?? "");
  const nodeId = validId ? BigInt(id!) : 0n;
  const nodes = useQuery(AdminService.method.listNodes, {}, { enabled: validId, refetchInterval: TRAFFIC_MS });
  // 历史窗口依赖流量响应里的 hub now；校正操作仍在卡片内保留自己的错误槽位。
  const traffic = useQuery(AdminService.method.getTraffic, {}, { enabled: validId, refetchInterval: TRAFFIC_MS });

  if (!validId) return <p role="alert" className="error">节点 {id} 不存在。<Link to="/">返回总览</Link></p>;
  const gate = queryGate(nodes);
  const time = queryGate(traffic);
  // history 与 traffic 不依赖 listNodes；listNodes 挂起或首次失败时它们完全可能
  // 已经就绪，不能因为 listNodes 的门控挡住这些已经就绪、不依赖它的内容。是否"不存在"只有 listNodes
  // 真的到达后才能判断——它挂起或首次失败时无法区分"节点被删了"与"这次还没拿到列表"，按后者处理。
  const node = gate.ready ? gate.data.nodes.find((n) => n.id === nodeId) : undefined;
  if (gate.ready && !node) return <p role="alert" className="error">节点 {id} 不存在。<Link to="/">返回总览</Link></p>;
  return (
    <section>
      {time.ready ? <NodeHistory nodeId={nodeId} name={node?.name} traffic={time.data} errors={[nodes.error, traffic.error]} /> : <>
        {errorBanner(nodes.error, ...time.errors)}
        <DetailHeader nodeId={nodeId} name={node?.name} />
        {time.loading}
      </>}
      {gate.ready ? node?.facts && (
        <dl className="card facts">
          {/* 来源地址是 hub 在上报上看到的对端，不是 agent 自报；只在管理端显示，公开页没有这个字段。 */}
          <dt>主机名</dt><dd>{node.facts.hostname}{node.lastSource && <span className="muted">（来源 {node.lastSource}）</span>}</dd>
          <dt>双栈出口</dt><dd><NodeAddresses network={node.facts.network} detailed /></dd>
          <dt>系统</dt><dd>{node.facts.os}</dd>
          <dt>内核</dt><dd>{node.facts.kernel}</dd>
          <dt>架构</dt><dd>{node.facts.arch}</dd>
          <dt>CPU</dt><dd>{node.facts.cpuModel} × {node.facts.cpuCores}</dd>
          <dt>虚拟化</dt><dd>{node.facts.virtualization || "无 / 未知"}</dd>
          <dt>agent</dt><dd>{node.facts.agentVersion}</dd>
          <dt>ICMP 探测</dt><dd>{node.facts.icmpAvailable ? "可用" : "不可用"}</dd>
        </dl>
      ) : gate.loading}
      {node && <ExecutionScope execution={node.facts?.execution} updatedAt={node.factsUpdatedAt} />}
      {node && <AgentDiagnostics diagnostics={node.facts?.diagnostics} updatedAt={node.factsUpdatedAt} />}
    </section>
  );
}

function DetailHeader({ nodeId, name, children }: { nodeId: bigint; name?: string; children?: ReactNode }) {
  return <header className="row detail-header">
    <h1>{name ?? `节点 #${nodeId}`}</h1>
    <Link to={`/events?node=${nodeId}`}>告警事件</Link>
    {children}
  </header>;
}

function NodeHistory({ nodeId, name, traffic, errors }: { nodeId: bigint; name?: string; traffic: GetTrafficResponse; errors: unknown[] }) {
  const history = useHistory(ADMIN_HISTORY, nodeId, Number(traffic.now));
  return <>
    {errorBanner(...errors, history.metrics.error, history.probes.error)}
    <DetailHeader nodeId={nodeId} name={name}><RangePicker history={history} /></DetailHeader>
    <TrafficCard nodeId={nodeId} data={traffic} />
    <HistoryCharts history={history} showCoverage noProbes={<p className="muted">窗口内没有探测结果。<Link to="/probes">管理探测任务</Link></p>} />
  </>;
}

const GIB = 2 ** 30;
const toGiB = (v: bigint) => (Number(v) / GIB).toFixed(2);
// 校正值以 GiB 输入，四舍五入到整字节；只有有限且落在 uint64 范围内的非负值才能提交。
const toBytes = (s: string): bigint | null => {
  const v = Number(s);
  const rounded = Math.round(v * GIB);
  if (!(s.trim() !== "" && Number.isFinite(v) && v >= 0 && Number.isFinite(rounded))) return null;
  const result = BigInt(rounded);
  return result <= (1n << 64n) - 1n ? result : null;
};

function TrafficCard({ nodeId, data }: { nodeId: bigint; data: GetTrafficResponse | undefined }) {
  const qc = useQueryClient();
  const [draft, setDraft] = useState<{ rx: string; tx: string } | null>(null);
  const adjust = useMutation(AdminService.method.adjustTraffic, {
    onSuccess: () => {
      setDraft(null);
      void qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.getTraffic, cardinality: "finite" }) });
      void qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.getSnapshot, cardinality: "finite" }) });
    },
  });
  const t = data?.nodes.find((n) => n.nodeId === nodeId)?.traffic;
  const timeZone = data?.timezone;
  if (!t) return null;
  // 编辑框首次出现时预填当前值；之后由用户输入驱动。
  const form = draft ?? { rx: toGiB(t.periodRx), tx: toGiB(t.periodTx) };
  const rx = toBytes(form.rx);
  const tx = toBytes(form.tx);
  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (rx !== null && tx !== null) adjust.mutate({ nodeId, periodRx: rx, periodTx: tx });
  };
  return (
    <div className="card">
      <h2>流量</h2>
      <dl className="facts">
        <dt>本周期</dt><dd>{trafficText(t)}</dd>
        <dt>总量</dt><dd>↓ {bytes(t.totalRx)} ↑ {bytes(t.totalTx)}</dd>
        <dt>周期起点</dt><dd>{new Date(Number(t.periodStart) * 1000).toLocaleString(undefined, { timeZone })}</dd>
        <dt>下次重置</dt><dd>{new Date(Number(t.nextResetAt) * 1000).toLocaleString(undefined, { timeZone })}（每月 {t.resetDay} 日，{timeZone}）</dd>
      </dl>
      <form onSubmit={onSubmit} className="row">
        <label>本周期下行 (GiB)<input value={form.rx} onChange={(e) => setDraft({ ...form, rx: e.target.value })} inputMode="decimal" /></label>
        <label>本周期上行 (GiB)<input value={form.tx} onChange={(e) => setDraft({ ...form, tx: e.target.value })} inputMode="decimal" /></label>
        <button type="submit" disabled={adjust.isPending || rx === null || tx === null}>校正本周期</button>
      </form>
      {adjust.error && <p role="alert" className="error">{errorText(adjust.error)}</p>}
    </div>
  );
}
