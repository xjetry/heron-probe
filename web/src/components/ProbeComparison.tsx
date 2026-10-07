import type { DescMethodUnary } from "@bufbuild/protobuf";
import { Code, ConnectError, type Transport } from "@connectrpc/connect";
import { useTransport } from "@connectrpc/connect-query";
import { useEffect, useMemo, useState } from "react";
import { errorText } from "../api/auth";
import type { ListProbeComparisonNodesRequestSchema, ListProbeComparisonNodesResponseSchema, QueryProbeComparisonRequestSchema, QueryProbeComparisonResponseSchema } from "../gen/heron/v1/query_pb";
import { ProbeKind } from "../gen/heron/v1/types_pb";
import { assembleComparison, chunkNodeIds, type ComparisonChart, type ComparisonChunk } from "../lib/probeComparison";
import { kindLabel } from "../lib/probes";
import { Chart } from "./Chart";
import { HISTORY_MAX_POINTS, RangeButtons, RangeStatus, useTimeWindow } from "./History";

// 同时在飞的分块数。这不是块的大小：块的大小只来自 List 的 max_nodes_per_query。
const MAX_IN_FLIGHT_CHUNKS = 2;
// 短名单直接展开；再长就折叠，避免几十个节点把图撑出屏幕。
const FOLD_AT = 8;

const LIMIT_ERROR = "对比协议错误：服务端返回的分块上限无效。";
const ALIGN_ERROR = "对比协议错误：分块结果无法对齐成一张图。";
export const NO_COMPARISON_NODES = "没有可对比的节点。";

export type ProbeComparisonMethods = {
  list: DescMethodUnary<typeof ListProbeComparisonNodesRequestSchema, typeof ListProbeComparisonNodesResponseSchema>;
  query: DescMethodUnary<typeof QueryProbeComparisonRequestSchema, typeof QueryProbeComparisonResponseSchema>;
};

type Hold = {
  nodeIds: bigint[];
  kind: ProbeKind;
  target: string;
  level: string;
  stepS: number;
  chunks: ComparisonChunk[];
  from: number;
  to: number;
  rangeLabel: string;
};

function gaveUp(err: unknown, signal: AbortSignal): boolean {
  return signal.aborted || (err instanceof ConnectError && err.code === Code.Canceled);
}

async function queryChunks(
  transport: Transport,
  method: ProbeComparisonMethods["query"],
  taskId: bigint,
  chunks: readonly (readonly bigint[])[],
  from: number,
  to: number,
  signal: AbortSignal,
): Promise<{ chunks: ComparisonChunk[]; level: string }> {
  const results: (ComparisonChunk & { level: string })[] = new Array(chunks.length);
  let next = 0;
  let failure: unknown;
  const worker = async () => {
    for (;;) {
      // next++ 与 failure 的读写都在 await 之前，单线程下不会交叉。失败后不再发后面的块。
      if (signal.aborted || failure !== undefined) return;
      const i = next++;
      if (i >= chunks.length) return;
      try {
        const resp = await transport.unary(method, signal, undefined, undefined, {
          taskId,
          nodeIds: [...chunks[i]],
          from: BigInt(from),
          to: BigInt(to),
          maxPoints: HISTORY_MAX_POINTS,
        });
        const message = resp.message;
        results[i] = { level: message.level, stepS: message.stepS, series: message.series, unavailableNodeIds: message.unavailableNodeIds };
      } catch (err) {
        if (failure === undefined) failure = err;
        return;
      }
    }
  };
  await Promise.all(Array.from({ length: Math.min(MAX_IN_FLIGHT_CHUNKS, chunks.length) }, () => worker()));
  if (failure !== undefined) throw failure;
  if (signal.aborted) throw new ConnectError("canceled", Code.Canceled);
  return { chunks: results, level: results[0]?.level ?? "" };
}

function NameList({ title, names }: { title: string; names: readonly string[] }) {
  if (names.length === 0) return null;
  const summary = `${title}（${names.length} 个）`;
  const items = (
    <ul>
      {names.map((name, i) => <li key={`${i}:${name}`}>{name}</li>)}
    </ul>
  );
  if (names.length <= FOLD_AT) {
    return <div className="compare-nodes"><p className="muted">{summary}</p>{items}</div>;
  }
  return <details className="compare-nodes"><summary>{summary}</summary>{items}</details>;
}

function ChartBlock({ title, unit, chart }: { title: string; unit: string; chart: ComparisonChart }) {
  return (
    <div className="card">
      <h2>{title}</h2>
      {chart.labels.length > 0 && <Chart data={chart.data} labels={chart.labels} unit={unit} />}
      <NameList title="该图窗口内没有读数的节点" names={chart.missing} />
    </div>
  );
}

function taskHeading(taskId: bigint, hold: Hold | null): string {
  if (!hold || hold.kind === ProbeKind.UNSPECIFIED || hold.target === "") return `任务 #${taskId}`;
  return `${kindLabel(hold.kind)} ${hold.target}`;
}

// 管理端与公开页同一份。节点名由调用方给：管理端来自 ListNodes，公开端来自 GetSnapshot。
// 一次查询是 (taskId, from, to, maxPoints)。List 的 max_nodes_per_query 决定块大小；全部块到齐才替换画面。
// 刷新、换窗口、换任务或离开页面都会取消还没回来的块。刷新期间留着上一张完整的图，并标成更新中。
export function ProbeComparison({ taskId, methods, nodes, now }: { taskId: bigint; methods: ProbeComparisonMethods; nodes: readonly { id: bigint; name: string }[]; now: number }) {
  const transport = useTransport();
  const { range, setRange, from, to } = useTimeWindow(now);
  const rangeLabel = range.label;
  const [hold, setHold] = useState<Hold | null>(null);
  const [holdTask, setHoldTask] = useState(taskId);
  const [error, setError] = useState<string | null>(null);
  const [notFound, setNotFound] = useState(false);
  const [updating, setUpdating] = useState(true);
  const [attempt, setAttempt] = useState(0);
  // 换任务不能沿用上一张图：那是另一个任务的节点。换窗口则留着，直到新窗口自己的块到齐。
  if (holdTask !== taskId) {
    setHoldTask(taskId);
    setHold(null);
    setError(null);
    setNotFound(false);
    setUpdating(true);
  }
  const shown = holdTask === taskId ? hold : null;
  const names = useMemo(() => new Map(nodes.map((node) => [node.id, node.name])), [nodes]);
  const view = useMemo(
    () => (shown ? assembleComparison(shown.nodeIds, names, shown.chunks, shown.from, shown.to) : null),
    [shown, names],
  );

  useEffect(() => {
    const ac = new AbortController();
    let active = true;
    setUpdating(true);
    setError(null);
    setNotFound(false);
    const fail = (message: string) => {
      if (!active) return;
      setError(message);
      setUpdating(false);
    };
    (async () => {
      let listed;
      try {
        listed = await transport.unary(methods.list, ac.signal, undefined, undefined, { taskId });
      } catch (err) {
        if (!active || gaveUp(err, ac.signal)) return;
        // NotFound 只属于 List：任务不存在，或一个可见的分配节点都没有。两种原因同一句说明，不展示服务端原文。
        if (err instanceof ConnectError && err.code === Code.NotFound) {
          setHold(null);
          setNotFound(true);
          setUpdating(false);
          return;
        }
        fail(errorText(err));
        return;
      }
      if (!active || ac.signal.aborted) return;
      const message = listed.message;
      const plan = chunkNodeIds(message.nodeIds, message.maxNodesPerQuery);
      if (!plan.ok) {
        fail(LIMIT_ERROR);
        return;
      }
      // 空候选在协议里是 NotFound。若响应仍带着空列表，没有节点可请求；空的 node_ids 会被服务端拒绝。
      if (plan.chunks.length === 0) {
        if (!active) return;
        setHold(null);
        setNotFound(true);
        setUpdating(false);
        return;
      }
      let fetched;
      try {
        fetched = await queryChunks(transport, methods.query, taskId, plan.chunks, from, to, ac.signal);
      } catch (err) {
        if (!active || gaveUp(err, ac.signal)) return;
        fail(errorText(err));
        return;
      }
      if (!active || ac.signal.aborted) return;
      // 完整性不依赖节点名。名字只在绘制时再套上，节点改名不必重新取数。
      const assembled = assembleComparison(message.nodeIds, new Map(), fetched.chunks, from, to);
      if (!assembled.complete) {
        fail(ALIGN_ERROR);
        return;
      }
      setHold({
        nodeIds: [...message.nodeIds],
        kind: message.kind,
        target: message.target,
        level: fetched.level,
        stepS: assembled.stepS,
        chunks: fetched.chunks,
        from,
        to,
        rangeLabel,
      });
      setNotFound(false);
      setUpdating(false);
    })();
    return () => {
      active = false;
      ac.abort();
    };
  }, [attempt, from, methods.list, methods.query, rangeLabel, taskId, to, transport]);

  const stale = shown !== null && shown.rangeLabel !== rangeLabel;
  return (
    <section aria-busy={updating}>
      <header className="row detail-header">
        <h1>{taskHeading(taskId, shown)}</h1>
        <RangeButtons range={range} setRange={setRange} />
        <RangeStatus shown={shown} stale={stale} rangeLabel={rangeLabel} updating={updating} />
      </header>
      {error && (
        <p role="alert" className="error">
          {error} <button type="button" onClick={() => setAttempt((n) => n + 1)}>重试</button>
        </p>
      )}
      {!shown && updating && !error && !notFound && <p className="muted">加载中…</p>}
      {notFound && !updating && <p className="muted">{NO_COMPARISON_NODES}</p>}
      {view?.complete && !notFound && (
        <>
          <div className="grid">
            <ChartBlock title="丢包率" unit="percent" chart={view.loss} />
            <ChartBlock title="RTT 均值" unit="ms" chart={view.rtt} />
          </div>
          <NameList title="已不可见的节点" names={view.unavailable} />
        </>
      )}
    </section>
  );
}
