import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { errorText } from "../api/auth";
import { AdminService, type Node, type ProbeTaskDetail } from "../gen/probe/v1/admin_pb";
import { ProbeKind } from "../gen/probe/v1/types_pb";

type Draft = { kind: ProbeKind; target: string; intervalS: string; timeoutMs: string; nodeIds: Set<bigint> };

const emptyDraft = (): Draft => ({ kind: ProbeKind.ICMP, target: "", intervalS: "60", timeoutMs: "1000", nodeIds: new Set() });
const draftOf = (d: ProbeTaskDetail): Draft => ({
  kind: d.task?.kind ?? ProbeKind.ICMP, target: d.task?.target ?? "", intervalS: String(d.task?.intervalS ?? 60),
  timeoutMs: String(d.task?.timeoutMs ?? 1000), nodeIds: new Set(d.nodeIds),
});
// hub 的 Registry.Save 经 probelimit.CheckTask 只接受 ICMP/TCP，列表标签依赖该准入。
const kindName = (k: ProbeKind) => (k === ProbeKind.TCP ? "TCP" : "ICMP");

export function ProbeTasks() {
  const qc = useQueryClient();
  const nodes = useQuery(AdminService.method.listNodes, {});
  const list = useQuery(AdminService.method.listProbeTasks, {});
  // 任务与分配的每次修改都让列表重新拉取；节点列表没有变化，不失效它。
  const refresh = () => qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listProbeTasks, cardinality: "finite" }) });
  const create = useMutation(AdminService.method.saveProbeTask, { onSuccess: refresh });
  const update = useMutation(AdminService.method.saveProbeTask, { onSuccess: refresh });
  const remove = useMutation(AdminService.method.deleteProbeTask, { onSuccess: refresh });
  const submit = (m: typeof create, id: bigint, d: Draft, onSuccess?: () => void) =>
    m.mutate({ task: { id, kind: d.kind, target: d.target.trim(), intervalS: Number(d.intervalS), timeoutMs: Number(d.timeoutMs) },
      nodeIds: [...d.nodeIds].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0)) }, { onSuccess });
  const anyError = create.error ?? update.error ?? remove.error;
  if (list.error) return <p role="alert" className="error">{errorText(list.error)}</p>;
  return (
    <section>
      <h1>探测任务</h1>
      <TaskForm title="新建探测任务" nodes={nodes.data?.nodes ?? []} initial={emptyDraft()} pending={create.isPending}
        onSubmit={(d) => submit(create, 0n, d)} />
      {anyError && <p role="alert" className="error">{errorText(anyError)}</p>}
      <div className="table-scroll">
        <table className="nodes">
          <thead><tr><th>类型</th><th>目标</th><th>间隔 (s)</th><th>超时 (ms)</th><th>节点</th><th /></tr></thead>
          <tbody>
            {list.data?.tasks.map((d) => (
              <TaskRow key={String(d.task?.id)} detail={d} nodes={nodes.data?.nodes ?? []} saving={update.isPending} deleting={remove.isPending}
                onSave={(draft, onSuccess) => submit(update, d.task?.id ?? 0n, draft, onSuccess)} onDelete={() => remove.mutate({ id: d.task?.id ?? 0n })} />
            ))}
          </tbody>
        </table>
      </div>
      {list.data && list.data.tasks.length === 0 && <p className="muted">还没有探测任务。</p>}
    </section>
  );
}

// 创建与编辑共用；字段约束用原生属性表达，hub 的 probelimit 是最终裁决，错误原文回到页面上。
function TaskForm({ title, nodes, initial, pending, onSubmit, onCancel }: {
  title: string; nodes: Node[]; initial: Draft; pending: boolean; onSubmit: (d: Draft) => void; onCancel?: () => void;
}) {
  const [draft, setDraft] = useState(initial);
  const toggle = (id: bigint) => {
    const next = new Set(draft.nodeIds);
    if (next.has(id)) next.delete(id); else next.add(id);
    setDraft({ ...draft, nodeIds: next });
  };
  const handle = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!e.currentTarget.checkValidity()) return;
    onSubmit(draft);
  };
  return (
    <form className="card task-form" aria-label={title} onSubmit={handle}>
      <div className="row">
        <label>类型
          <select value={draft.kind} onChange={(e) => setDraft({ ...draft, kind: Number(e.target.value) as ProbeKind })}>
            <option value={ProbeKind.ICMP}>ICMP</option>
            <option value={ProbeKind.TCP}>TCP</option>
          </select>
        </label>
        <label>目标<input required maxLength={253} placeholder={draft.kind === ProbeKind.TCP ? "host:port" : "IP 或主机名"} value={draft.target}
          onChange={(e) => setDraft({ ...draft, target: e.target.value })} /></label>
        <label>间隔 (s)<input type="number" required min={5} max={3600} value={draft.intervalS} onChange={(e) => setDraft({ ...draft, intervalS: e.target.value })} /></label>
        <label>超时 (ms)<input type="number" required min={100} max={5000} value={draft.timeoutMs} onChange={(e) => setDraft({ ...draft, timeoutMs: e.target.value })} /></label>
      </div>
      <fieldset className="node-picks">
        <legend>分配到节点</legend>
        {nodes.map((n) => (
          <label key={String(n.id)}><input type="checkbox" checked={draft.nodeIds.has(n.id)} onChange={() => toggle(n.id)} />{n.name}</label>
        ))}
      </fieldset>
      <div className="row">
        <button type="submit" disabled={pending}>{onCancel ? "保存" : "创建"}</button>
        {onCancel && <button type="button" className="link" onClick={onCancel}>取消</button>}
      </div>
    </form>
  );
}

function TaskRow({ detail, nodes, saving, deleting, onSave, onDelete }: {
  detail: ProbeTaskDetail; nodes: Node[]; saving: boolean; deleting: boolean; onSave: (d: Draft, onSuccess: () => void) => void; onDelete: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const t = detail.task!;
  const names = detail.nodeIds.map((id) => nodes.find((n) => n.id === id)?.name ?? `#${id}`).join("、");
  if (editing) {
    return (
      <tr><td colSpan={6}>
        <TaskForm title="编辑探测任务" nodes={nodes} initial={draftOf(detail)} pending={saving}
          onSubmit={(d) => onSave(d, () => setEditing(false))} onCancel={() => setEditing(false)} />
      </td></tr>
    );
  }
  return (
    <tr>
      <td>{kindName(t.kind)}</td>
      <td>{t.target}</td>
      <td>{t.intervalS}</td>
      <td>{t.timeoutMs}</td>
      <td>{names || <span className="muted">未分配</span>}</td>
      <td>
        <button type="button" className="link" aria-label={`编辑 ${t.target}`} onClick={() => setEditing(true)}>编辑</button>{" "}
        {confirming ? (
          <>
            <button type="button" className="danger" disabled={deleting} onClick={onDelete}>确认删除 {t.target}</button>{" "}
            <button type="button" className="link" onClick={() => setConfirming(false)}>取消</button>
          </>
        ) : (
          <button type="button" className="link danger" aria-label={`删除 ${t.target}`} onClick={() => setConfirming(true)}>删除</button>
        )}
      </td>
    </tr>
  );
}
