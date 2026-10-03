import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { errorText } from "../api/auth";
import { errorBanner, queryGateAll } from "../api/queryGate";
import { useLatestError } from "../api/useLatestError";
import { ConfirmDelete } from "../components/ConfirmDelete";
import { NodeSelector, type NodeSelection } from "../components/NodeSelector";
import { AdminService, SilenceKind, type Node, type Silence, type SilenceEntry } from "../gen/heron/v1/admin_pb";
import { liveIds, withId } from "../lib/ids";

type Draft = NodeSelection & {
  name: string; enabled: boolean; kind: SilenceKind;
  startHhmm: string; endHhmm: string; fromAt: string; untilAt: string; reason: string;
};

const emptyDraft = (): Draft => ({
  name: "", enabled: true, kind: SilenceKind.DAILY, allNodes: true, nodeIds: new Set(), selectorTags: [], dynamic: false,
  startHhmm: "22:00", endHhmm: "06:00", fromAt: "", untilAt: "", reason: "",
});
// datetime-local 的值是本地墙钟；from_at/until_at 是 Unix 秒，含起点不含终点。
const toLocalInput = (unix: bigint) => {
  if (unix === 0n) return "";
  const d = new Date(Number(unix) * 1000);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
};
const fromLocalInput = (value: string) => BigInt(Math.floor(new Date(value).getTime() / 1000));
const draftOf = (s: Silence): Draft => ({
  name: s.name, enabled: s.enabled, kind: s.kind, allNodes: s.allNodes, nodeIds: new Set(s.nodeIds),
  selectorTags: s.selectorTags, dynamic: s.selectorTags.length > 0,
  startHhmm: s.startHhmm || "22:00", endHhmm: s.endHhmm || "06:00",
  fromAt: toLocalInput(s.fromAt), untilAt: toLocalInput(s.untilAt), reason: s.reason,
});

// 作用域与告警规则同一形状（§9.5）；只有当前种类的窗口字段随保存发出，hub 拒绝带着另一种类字段的静默。
function toSilence(id: bigint, d: Draft, nodes: Node[]) {
  return {
    id, name: d.name.trim(), enabled: d.enabled, allNodes: d.allNodes,
    nodeIds: d.allNodes || d.dynamic ? [] : liveIds(d.nodeIds, nodes),
    selectorTags: !d.allNodes && d.dynamic ? d.selectorTags : [],
    kind: d.kind,
    startHhmm: d.kind === SilenceKind.DAILY ? d.startHhmm : "",
    endHhmm: d.kind === SilenceKind.DAILY ? d.endHhmm : "",
    fromAt: d.kind === SilenceKind.ONCE ? fromLocalInput(d.fromAt) : 0n,
    untilAt: d.kind === SilenceKind.ONCE ? fromLocalInput(d.untilAt) : 0n,
    reason: d.reason,
  };
}

export function Silences() {
  const qc = useQueryClient();
  const [creation, setCreation] = useState(0);
  const { error, mutationOptions } = useLatestError();
  const nodes = useQuery(AdminService.method.listNodes, {});
  const silences = useQuery(AdminService.method.listSilences, {}, { refetchInterval: 10_000 });
  const refresh = () => qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listSilences, cardinality: "finite" }) });
  const create = useMutation(AdminService.method.saveSilence, { ...mutationOptions, onSuccess: refresh });
  const update = useMutation(AdminService.method.saveSilence, { ...mutationOptions, onSuccess: refresh });
  const remove = useMutation(AdminService.method.deleteSilence, { ...mutationOptions, onSuccess: refresh });
  const gate = queryGateAll(nodes, silences);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const [nodesData, silencesData] = gate.data;
  const nodeList = nodesData.nodes;
  const nodeName = (id: bigint) => nodeList.find((n) => n.id === id)?.name ?? `节点 #${id}`;
  return (
    <section>
      {gate.banner}
      <h1>维护静默</h1>
      <p className="muted">覆盖内的节点在窗口内产生的告警事件照常记录但不投递；恢复是否投递只看配对的触发是否投递过。到期提醒与系统事件不受静默影响。节点的维护开关在节点编辑里设置。</p>
      <SilenceForm key={creation} title="新建维护静默" nodes={nodeList} initial={emptyDraft()} pending={create.isPending}
        onSubmit={(d) => create.mutate({ silence: toSilence(0n, d, nodeList) }, { onSuccess: () => setCreation((k) => k + 1) })} />
      {error != null && <p role="alert" className="error">{errorText(error)}</p>}
      <div className="table-scroll" role="region" aria-label="维护静默管理" tabIndex={0}>
        <table className="nodes">
          <thead><tr><th>名称</th><th>窗口</th><th>作用域</th><th>状态</th><th>操作</th></tr></thead>
          <tbody>
            {silencesData.silences.map((entry) => (
              <SilenceRow key={String(entry.silence?.id)} entry={entry} nodes={nodeList} nodeName={nodeName}
                saving={update.isPending} deleting={remove.isPending}
                onSave={(d, onSuccess) => update.mutate({ silence: toSilence(entry.silence?.id ?? 0n, d, nodeList) }, { onSuccess })}
                onDelete={() => remove.mutate({ id: entry.silence?.id ?? 0n })} />
            ))}
          </tbody>
        </table>
      </div>
      {silencesData.silences.length === 0 && <p className="muted">还没有维护静默。</p>}
    </section>
  );
}

function windowText(s: Silence): string {
  if (s.kind === SilenceKind.DAILY) return `每日 ${s.startHhmm}–${s.endHhmm}（hub 时区）`;
  const at = (unix: bigint) => new Date(Number(unix) * 1000).toLocaleString();
  return `${at(s.fromAt)} – ${at(s.untilAt)}`;
}

function scopeText(s: Silence, nodeName: (id: bigint) => string): string {
  if (s.allNodes) return "全部节点";
  if (s.selectorTags.length > 0) return `动态标签：${s.selectorTags.join(" ∩ ")}；当前 ${s.nodeIds.length} 个节点`;
  return s.nodeIds.map(nodeName).join("、") || "无节点";
}

function SilenceForm({ title, nodes, initial, pending, onSubmit, onCancel }: {
  title: string; nodes: Node[]; initial: Draft; pending: boolean; onSubmit: (d: Draft) => void; onCancel?: () => void;
}) {
  // initial 只在挂载时读取；列表的周期刷新不覆盖草稿。
  const [draft, setDraft] = useState(initial);
  const set = (patch: Partial<Draft>) => setDraft({ ...draft, ...patch });
  const handle = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!e.currentTarget.checkValidity()) return;
    onSubmit(draft);
  };
  return (
    <form className="card edit-form" aria-label={title} onSubmit={handle}>
      <div className="row">
        <label>名称<input required value={draft.name} onChange={(e) => set({ name: e.target.value })} /></label>
        <label>类型
          <select value={draft.kind} onChange={(e) => set({ kind: Number(e.target.value) as SilenceKind })}>
            <option value={SilenceKind.DAILY}>每日重复</option>
            <option value={SilenceKind.ONCE}>一次性</option>
          </select>
        </label>
        <label className="inline"><input type="checkbox" checked={draft.enabled} onChange={(e) => set({ enabled: e.target.checked })} />启用</label>
      </div>
      {draft.kind === SilenceKind.DAILY ? (
        <div className="row">
          <label>开始（HH:MM）<input required type="time" value={draft.startHhmm} onChange={(e) => set({ startHhmm: e.target.value })} /></label>
          <label>结束（HH:MM）<input required type="time" value={draft.endHhmm} onChange={(e) => set({ endHhmm: e.target.value })} /></label>
          <p className="muted">按 hub 时区判定，允许跨午夜（如 22:00–06:00）；开始含、结束不含。</p>
        </div>
      ) : (
        <div className="row">
          <label>开始<input required type="datetime-local" value={draft.fromAt} onChange={(e) => set({ fromAt: e.target.value })} /></label>
          <label>结束<input required type="datetime-local" value={draft.untilAt} onChange={(e) => set({ untilAt: e.target.value })} /></label>
          <p className="muted">含开始、不含结束。到期后保留供审计，随告警事件的保留期清理。</p>
        </div>
      )}
      <NodeSelector nodes={nodes} value={draft} onChange={set} legend="作用域节点" />
      <div className="row">
        <label>原因（选填）<input value={draft.reason} maxLength={256} onChange={(e) => set({ reason: e.target.value })} /></label>
      </div>
      <div className="row">
        <button type="submit" disabled={pending}>{onCancel ? "保存" : "创建"}</button>
        {onCancel && <button type="button" className="link" onClick={onCancel}>取消</button>}
      </div>
    </form>
  );
}

function SilenceRow({ entry, nodes, nodeName, saving, deleting, onSave, onDelete }: {
  entry: SilenceEntry; nodes: Node[]; nodeName: (id: bigint) => string;
  saving: boolean; deleting: boolean; onSave: (d: Draft, onSuccess: () => void) => void; onDelete: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const s = entry.silence;
  if (!s) return null;
  if (editing) {
    return (
      <tr><td colSpan={5}>
        <SilenceForm title={`编辑 ${withId(s.name, s.id)}`} nodes={nodes} initial={draftOf(s)} pending={saving}
          onSubmit={(d) => onSave(d, () => setEditing(false))} onCancel={() => setEditing(false)} />
      </td></tr>
    );
  }
  return (
    <tr>
      <td>{s.name}{s.reason && <p className="node-subtext node-note" title={s.reason}>{s.reason}</p>}</td>
      <td>{windowText(s)}</td>
      <td>{scopeText(s, nodeName)}</td>
      <td>{!s.enabled ? <span className="muted">已停用</span> : entry.active ? <span>生效中</span> : <span className="muted">窗口外</span>}</td>
      <td>
        <button type="button" className="link" aria-label={`编辑 ${withId(s.name, s.id)}`} onClick={() => setEditing(true)}>编辑</button>{" "}
        <ConfirmDelete label={`删除 ${withId(s.name, s.id)}`} confirm={`确认删除 ${withId(s.name, s.id)}`} note="已产生的告警事件保留" pending={deleting} onDelete={onDelete} />
      </td>
    </tr>
  );
}
