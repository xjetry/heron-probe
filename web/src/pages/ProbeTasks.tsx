import { createConnectQueryKey, createQueryOptions, useMutation, useQuery, useTransport } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { errorText } from "../api/auth";
import { errorBanner, queryGateAll } from "../api/queryGate";
import { useLatestError } from "../api/useLatestError";
import { useOrder } from "../api/useOrder";
import { ConfirmDelete } from "../components/ConfirmDelete";
import { NodeSelector, type NodeSelection } from "../components/NodeSelector";
import { AdminService, type Node, type ProbeTaskDetail } from "../gen/heron/v1/admin_pb";
import { ProbeKind, type ProbeTask } from "../gen/heron/v1/types_pb";
import { ascending, withId } from "../lib/ids";
import { PROBE_KINDS, kindLabel } from "../lib/probes";

type Draft = NodeSelection & { kind: ProbeKind; target: string; intervalS: string; timeoutMs: string };
type TaskEntry = { task: ProbeTask; allNodes: boolean; nodeIds: bigint[]; selectorTags: string[] };

const emptyDraft = (): Draft => ({ kind: ProbeKind.ICMP, target: "", intervalS: "60", timeoutMs: "1000", allNodes: false, nodeIds: new Set(), selectorTags: [], dynamic: false });
const taskEntries = (tasks: readonly ProbeTaskDetail[]): TaskEntry[] => tasks.flatMap((d) => d.task ? [{ task: d.task, allNodes: d.allNodes, nodeIds: d.nodeIds, selectorTags: d.selectorTags }] : []);
// all_nodes 任务的 nodeIds 是 hub 展开的当前全部节点；编辑时取消"全部节点"即以它们作为显式分配的起点，
// 覆盖不会因为取消勾选而一下子清空。
const draftOf = ({ task, allNodes, nodeIds, selectorTags }: TaskEntry): Draft => ({
  kind: task.kind, target: task.target, intervalS: String(task.intervalS),
  timeoutMs: String(task.timeoutMs), allNodes, nodeIds: new Set(nodeIds), selectorTags, dynamic: selectorTags.length > 0,
});

export function ProbeTasks() {
  const qc = useQueryClient();
  const transport = useTransport();
  const [creation, setCreation] = useState(0);
  const { error, mutationOptions } = useLatestError();
  const nodes = useQuery(AdminService.method.listNodes, {});
  const list = useQuery(AdminService.method.listProbeTasks, {});
  // 任务与分配的每次修改都让列表重新拉取；节点列表没有变化，不失效它。
  const refresh = () => qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listProbeTasks, cardinality: "finite" }) });
  const create = useMutation(AdminService.method.saveProbeTask, { ...mutationOptions, onSuccess: refresh });
  // 各行共用一个 mutation observer，重叠的 mutate 只回调最后一次；因此任一行保存挂起时禁用全部行的保存，退出编辑的才是保存的那一行。
  // 返回刷新 promise，编辑态在列表显示已保存值之后才关闭。
  const update = useMutation(AdminService.method.saveProbeTask, { ...mutationOptions, onSuccess: refresh });
  const remove = useMutation(AdminService.method.deleteProbeTask, { ...mutationOptions, onSuccess: refresh });
  const reorder = useMutation(AdminService.method.reorderProbeTasks);
  const order = useOrder({
    items: list.data?.tasks ?? [], id: (entry) => entry.task?.id ?? 0n,
    enabled: list.data !== undefined && !list.isError,
    save: (ids) => reorder.mutateAsync({ ids }),
    reload: async () => {
      const options = createQueryOptions(AdminService.method.listProbeTasks, {}, { transport });
      await qc.cancelQueries({ queryKey: options.queryKey, exact: true });
      return (await qc.fetchQuery({ ...options, staleTime: 0 })).tasks;
    },
  });
  // 分配求交依赖节点列表已到达；未到达前不渲染任何可提交的表单。
  const gate = queryGateAll(nodes, list);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const [nodesData] = gate.data;
  const nodeList = nodesData.nodes;
  const availableNodeIds = new Set(nodeList.map((n) => n.id));
  // 当前节点列表不再包含的分配自然掉出，避免已删除节点让 hub 以 NotFound 拒绝整次保存。显式分配因此变空时照常
  // 提交：空集不覆盖任何节点（spec §8.1，与告警规则的 all_nodes 同一语义），不会被读成全部节点。
  const submit = (m: typeof create, id: bigint, d: Draft, onSuccess?: () => void) =>
    m.mutate({ task: { id, kind: d.kind, target: d.target.trim(), intervalS: Number(d.intervalS), timeoutMs: Number(d.timeoutMs) },
      allNodes: d.allNodes, nodeIds: d.allNodes || d.dynamic ? [] : ascending([...d.nodeIds].filter((id) => availableNodeIds.has(id))),
      selectorTags: !d.allNodes && d.dynamic ? d.selectorTags : [] }, { onSuccess });
  const tasks = taskEntries(order.items);
  return (
    <section>
      {gate.banner}
      <h1>探测任务</h1>
      <TaskForm key={creation} title="新建探测任务" nodes={nodeList} initial={emptyDraft()} pending={create.isPending}
        onSubmit={(d) => submit(create, 0n, d, () => setCreation((key) => key + 1))} />
      {error != null && <p role="alert" className="error">{errorText(error)}</p>}
      {order.error != null && <p role="alert" className="error">排序未完成：{errorText(order.error)}</p>}
      {order.pending && <p role="status" className="muted">正在保存并确认排序…</p>}
      {order.blocked && <button type="button" onClick={order.recover} disabled={order.pending}>重新读取排序</button>}
      <div className="table-scroll" role="region" aria-label="探测任务管理" tabIndex={0}>
        <table className="nodes">
          <thead><tr><th>排序</th><th>类型</th><th>目标</th><th>间隔 (s)</th><th>超时 (ms)</th><th>节点</th><th>操作</th></tr></thead>
          <tbody>
            {tasks.map((entry) => (
              <TaskRow key={String(entry.task.id)} entry={entry} nodes={nodeList} saving={update.isPending} deleting={remove.isPending}
                onMove={order.blocked || list.isError ? undefined : (direction) => order.move(entry.task.id, direction)}
                onSave={(draft, onSuccess) => submit(update, entry.task.id, draft, onSuccess)} onDelete={() => remove.mutate({ id: entry.task.id })} />
            ))}
          </tbody>
        </table>
      </div>
      {tasks.length === 0 && <p className="muted">还没有探测任务。</p>}
    </section>
  );
}

// 创建与编辑共用；字段约束用原生属性表达，hub 的 probelimit 是最终裁决，错误原文回到页面上。
function TaskForm({ title, nodes, initial, pending, onSubmit, onCancel }: {
  title: string; nodes: Node[]; initial: Draft; pending: boolean; onSubmit: (d: Draft) => void; onCancel?: () => void;
}) {
  // initial 只在挂载时读取；编辑期间的列表刷新不覆盖草稿，节点列表以 props 实时更新，提交时与当前列表求交。
  const [draft, setDraft] = useState(initial);
  const handle = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!e.currentTarget.checkValidity()) return;
    onSubmit(draft);
  };
  return (
    <form className="card edit-form" aria-label={title} onSubmit={handle}>
      <div className="row">
        <label>类型
          <select value={draft.kind} onChange={(e) => setDraft({ ...draft, kind: Number(e.target.value) as ProbeKind })}>
            {PROBE_KINDS.map(({ kind, label }) => <option key={kind} value={kind}>{label}</option>)}
          </select>
        </label>
        <label>目标<input required maxLength={253} placeholder={draft.kind === ProbeKind.TCP ? "host:port" : "IP 或主机名"} value={draft.target}
          onChange={(e) => setDraft({ ...draft, target: e.target.value })} /></label>
        <label>间隔 (s)<input type="number" required min={5} max={3600} value={draft.intervalS} onChange={(e) => setDraft({ ...draft, intervalS: e.target.value })} /></label>
        <label>超时 (ms)<input type="number" required min={100} max={5000} value={draft.timeoutMs} onChange={(e) => setDraft({ ...draft, timeoutMs: e.target.value })} /></label>
      </div>
      <NodeSelector nodes={nodes} value={draft} onChange={(patch) => setDraft({ ...draft, ...patch })} legend="分配到节点" />
      <div className="row">
        <button type="submit" disabled={pending}>{onCancel ? "保存" : "创建"}</button>
        {onCancel && <button type="button" className="link" onClick={onCancel}>取消</button>}
      </div>
    </form>
  );
}

function TaskRow({ entry, nodes, saving, deleting, onSave, onDelete, onMove }: {
  entry: TaskEntry; nodes: Node[]; saving: boolean; deleting: boolean; onSave: (d: Draft, onSuccess: () => void) => void; onDelete: () => void;
  onMove?: (direction: -1 | 1) => void;
}) {
  const { task: t, allNodes, nodeIds } = entry;
  const [editing, setEditing] = useState(false);
  const names = nodeIds.map((id) => nodes.find((n) => n.id === id)?.name ?? `#${id}`).join("、");
  // 显式分配为空显示"未分配"：它不覆盖任何节点，与"全部节点"区分开。
  const coverage = allNodes ? `全部节点：${names || "暂无节点"}` : entry.selectorTags.length ? `动态标签：${entry.selectorTags.join(" ∩ ")}；当前：${names || "无匹配"}` : names;
  if (editing) {
    return (
      <tr><td colSpan={7}>
        <TaskForm title={`编辑 ${withId(t.target, t.id)}`} nodes={nodes} initial={draftOf(entry)} pending={saving}
          onSubmit={(d) => onSave(d, () => setEditing(false))} onCancel={() => setEditing(false)} />
      </td></tr>
    );
  }
  return (
    <tr>
      <td>
        <button type="button" className="link" aria-label={`上移 ${withId(t.target, t.id)}`} disabled={!onMove} onClick={() => onMove?.(-1)}>↑</button>
        <button type="button" className="link" aria-label={`下移 ${withId(t.target, t.id)}`} disabled={!onMove} onClick={() => onMove?.(1)}>↓</button>
      </td>
      <td>{kindLabel(t.kind)}</td>
      <td>{t.target}</td>
      <td>{t.intervalS}</td>
      <td>{t.timeoutMs}</td>
      <td>{coverage || <span className="muted">未分配</span>}</td>
      <td>
        <button type="button" className="link" aria-label={`编辑 ${withId(t.target, t.id)}`} onClick={() => setEditing(true)}>编辑</button>{" "}
        <ConfirmDelete label={`删除 ${withId(t.target, t.id)}`} confirm={`确认删除 ${withId(t.target, t.id)}`} note="历史保留至到期清理" pending={deleting} onDelete={onDelete} />
      </td>
    </tr>
  );
}
