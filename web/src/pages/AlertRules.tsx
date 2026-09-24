import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { errorText } from "../api/auth";
import { useLatestError } from "../api/useLatestError";
import { ConfirmDelete } from "../components/ConfirmDelete";
import { Picks } from "../components/Picks";
import { AdminService, AlertKind, ProbeMetric, type AlertRule, type Node, type NotifyChannel, type ProbeTaskDetail } from "../gen/probe/v1/admin_pb";
import { ALERT_KINDS, PROBE_METRICS, labelOf, ruleCondition, statesOf, type RuleStates } from "../lib/alerts";
import { ascending } from "../lib/ids";
import { taskLabel } from "../lib/probes";

type Draft = {
  name: string; kind: AlertKind; enabled: boolean; allNodes: boolean; nodeIds: Set<bigint>; channelIds: Set<bigint>;
  taskId: string; metric: ProbeMetric; threshold: string; forMinutes: string;
};

const emptyDraft = (): Draft => ({
  name: "", kind: AlertKind.OFFLINE, enabled: true, allNodes: true, nodeIds: new Set(), channelIds: new Set(),
  taskId: "", metric: ProbeMetric.LOSS_PCT, threshold: "", forMinutes: "3",
});
const draftOf = (r: AlertRule): Draft => {
  const probe = r.kind === AlertKind.PROBE;
  return {
    name: r.name, kind: r.kind, enabled: r.enabled, allNodes: r.allNodes, nodeIds: new Set(r.nodeIds), channelIds: new Set(r.channelIds),
    taskId: probe ? String(r.taskId) : "", metric: probe ? r.metric : ProbeMetric.LOSS_PCT,
    threshold: probe ? String(r.threshold) : "", forMinutes: probe ? String(r.forMinutes) : "3",
  };
};

// 当前列表不再包含的节点与渠道自然掉出，避免已删除对象让 hub 拒绝整次保存；
// 显式作用域因此变空时由 hub 拒绝（spec §6.6：空集不等于全部节点），不会悄悄放宽成全部。
function toRule(id: bigint, d: Draft, nodes: Node[], channels: NotifyChannel[]) {
  const live = (ids: ReadonlySet<bigint>, items: { id: bigint }[]) => ascending(items.filter((it) => ids.has(it.id)).map((it) => it.id));
  const probe = d.kind === AlertKind.PROBE
    ? { taskId: BigInt(d.taskId), metric: d.metric, threshold: Number(d.threshold), forMinutes: Number(d.forMinutes) }
    : {};
  return {
    id, name: d.name.trim(), kind: d.kind, enabled: d.enabled, allNodes: d.allNodes,
    nodeIds: d.allNodes ? [] : live(d.nodeIds, nodes), channelIds: live(d.channelIds, channels), ...probe,
  };
}

export function AlertRules() {
  const qc = useQueryClient();
  const [creation, setCreation] = useState(0);
  const { error, mutationOptions } = useLatestError();
  const nodes = useQuery(AdminService.method.listNodes, {});
  const channels = useQuery(AdminService.method.listNotifyChannels, {});
  const tasks = useQuery(AdminService.method.listProbeTasks, {});
  // 离线巡检每 10 秒一轮（spec §9.2）；按同一周期刷新，触发中的节点不用手动刷新就能看到。
  const rules = useQuery(AdminService.method.listAlertRules, {}, { refetchInterval: 10_000 });
  const refresh = () => qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listAlertRules, cardinality: "finite" }) });
  const create = useMutation(AdminService.method.saveAlertRule, { ...mutationOptions, onSuccess: refresh });
  // 各行共用一个 mutation observer，重叠的 mutate 只回调最后一次；任一行保存挂起时禁用全部行的保存。
  // 返回刷新 promise，编辑态在列表显示已保存值之后才关闭。
  const update = useMutation(AdminService.method.saveAlertRule, { ...mutationOptions, onSuccess: refresh });
  const remove = useMutation(AdminService.method.deleteAlertRule, { ...mutationOptions, onSuccess: refresh });
  // 作用域、渠道与任务的求交依赖三张列表都已到达；任一未到达时不渲染可提交的表单。
  if (!nodes.data || !channels.data || !tasks.data) {
    const failed = nodes.error ?? channels.error ?? tasks.error;
    return failed ? <p role="alert" className="error">{errorText(failed)}</p> : <p className="muted">加载中…</p>;
  }
  const nodeList = nodes.data.nodes;
  const channelList = channels.data.channels;
  const taskList = tasks.data.tasks;
  if (rules.error) return <p role="alert" className="error">{errorText(rules.error)}</p>;
  const byRule = statesOf(rules.data?.states ?? []);
  const nodeName = (id: bigint) => nodeList.find((n) => n.id === id)?.name ?? `节点 #${id}`;
  const channelName = (id: bigint) => channelList.find((c) => c.id === id)?.name ?? `渠道 #${id}`;
  const lists = { nodes: nodeList, channels: channelList, tasks: taskList };
  return (
    <section>
      <h1>告警规则</h1>
      <RuleForm key={creation} title="新建告警规则" {...lists} initial={emptyDraft()} pending={create.isPending}
        onSubmit={(d) => create.mutate({ rule: toRule(0n, d, nodeList, channelList) }, { onSuccess: () => setCreation((k) => k + 1) })} />
      {error != null && <p role="alert" className="error">{errorText(error)}</p>}
      <div className="table-scroll" role="region" aria-label="告警规则管理" tabIndex={0}>
        <table className="nodes">
          <thead><tr><th>名称</th><th>类型</th><th>条件</th><th>作用域</th><th>通知</th><th>状态</th><th>操作</th></tr></thead>
          <tbody>
            {(rules.data?.rules ?? []).map((r) => (
              <RuleRow key={String(r.id)} rule={r} states={byRule.get(r.id)} {...lists} nodeName={nodeName} channelName={channelName}
                saving={update.isPending} deleting={remove.isPending}
                onSave={(d, onSuccess) => update.mutate({ rule: toRule(r.id, d, nodeList, channelList) }, { onSuccess })}
                onDelete={() => remove.mutate({ id: r.id })} />
            ))}
          </tbody>
        </table>
      </div>
      {rules.data && rules.data.rules.length === 0 && <p className="muted">还没有告警规则。</p>}
    </section>
  );
}

type Lists = { nodes: Node[]; channels: NotifyChannel[]; tasks: ProbeTaskDetail[] };

function RuleForm({ title, nodes, channels, tasks, initial, pending, onSubmit, onCancel }: Lists & {
  title: string; initial: Draft; pending: boolean; onSubmit: (d: Draft) => void; onCancel?: () => void;
}) {
  // initial 只在挂载时读取；列表的周期刷新不覆盖草稿。
  const [draft, setDraft] = useState(initial);
  const set = (patch: Partial<Draft>) => setDraft({ ...draft, ...patch });
  const probe = draft.kind === AlertKind.PROBE;
  const loss = draft.metric === ProbeMetric.LOSS_PCT;
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
          <select value={draft.kind} onChange={(e) => set({ kind: Number(e.target.value) as AlertKind })}>
            {ALERT_KINDS.map(({ value, label }) => <option key={value} value={value}>{label}</option>)}
          </select>
        </label>
        <label className="inline"><input type="checkbox" checked={draft.enabled} onChange={(e) => set({ enabled: e.target.checked })} />启用</label>
      </div>
      {probe ? (
        <div className="row">
          <label>探测任务
            <select required value={draft.taskId} onChange={(e) => set({ taskId: e.target.value })}>
              <option value="">选择任务</option>
              {tasks.flatMap((d) => (d.task ? [<option key={String(d.task.id)} value={String(d.task.id)}>{taskLabel(d.task.id, tasks)}</option>] : []))}
            </select>
          </label>
          <label>指标
            <select value={draft.metric} onChange={(e) => set({ metric: Number(e.target.value) as ProbeMetric })}>
              {PROBE_METRICS.map(({ value, label }) => <option key={value} value={value}>{label}</option>)}
            </select>
          </label>
          <label>阈值（{loss ? "%" : "ms"}）<input type="number" required step="any" min={0} max={loss ? 100 : undefined}
            value={draft.threshold} onChange={(e) => set({ threshold: e.target.value })} /></label>
          <label>连续分钟<input type="number" required min={1} max={60} value={draft.forMinutes} onChange={(e) => set({ forMinutes: e.target.value })} /></label>
        </div>
      ) : (
        <p className="muted">节点超过离线宽限期未上报即触发，收到上报即恢复；宽限期在节点页按节点设置，未设置时取 hub 的 PROBE_OFFLINE_AFTER。</p>
      )}
      <label className="inline"><input type="checkbox" checked={draft.allNodes} onChange={(e) => set({ allNodes: e.target.checked })} />全部节点（含以后新建的节点）</label>
      {!draft.allNodes && <Picks legend="作用域节点" items={nodes} selected={draft.nodeIds} onChange={(nodeIds) => set({ nodeIds })} />}
      {probe && <p className="muted">探测规则只在既属于作用域、又分配了该任务的节点上评估。</p>}
      {channels.length > 0
        ? <Picks legend="通知渠道" items={channels} selected={draft.channelIds} onChange={(channelIds) => set({ channelIds })} />
        : <p className="muted">还没有通知渠道；规则只记录事件，不发送通知。</p>}
      <div className="row">
        <button type="submit" disabled={pending}>{onCancel ? "保存" : "创建"}</button>
        {onCancel && <button type="button" className="link" onClick={onCancel}>取消</button>}
      </div>
    </form>
  );
}

function RuleRow({ rule: r, states, nodes, channels, tasks, nodeName, channelName, saving, deleting, onSave, onDelete }: Lists & {
  rule: AlertRule; states: RuleStates | undefined; nodeName: (id: bigint) => string; channelName: (id: bigint) => string;
  saving: boolean; deleting: boolean; onSave: (d: Draft, onSuccess: () => void) => void; onDelete: () => void;
}) {
  const [editing, setEditing] = useState(false);
  if (editing) {
    return (
      <tr><td colSpan={7}>
        <RuleForm title={`编辑 ${r.name}`} nodes={nodes} channels={channels} tasks={tasks} initial={draftOf(r)} pending={saving}
          onSubmit={(d) => onSave(d, () => setEditing(false))} onCancel={() => setEditing(false)} />
      </td></tr>
    );
  }
  return (
    <tr>
      <td>{r.name}</td>
      <td>{labelOf(ALERT_KINDS, r.kind)}</td>
      <td>{ruleCondition(r, tasks)}</td>
      <td>{r.allNodes ? "全部节点" : r.nodeIds.map(nodeName).join("、") || "无节点"}</td>
      <td>{r.channelIds.map(channelName).join("、") || "只记事件"}</td>
      <td><RuleState enabled={r.enabled} states={states} nodeName={nodeName} /></td>
      <td>
        <button type="button" className="link" aria-label={`编辑 ${r.name}`} onClick={() => setEditing(true)}>编辑</button>{" "}
        <ConfirmDelete label={`删除 ${r.name}`} confirm={`确认删除 ${r.name}`} note="事件记录保留" pending={deleting} onDelete={onDelete} />
      </td>
    </tr>
  );
}

// 停用的规则不评估，hub 保存停用时已清除其状态行；显示"已停用"而不是"正常"，避免读成一切无事。
function RuleState({ enabled, states, nodeName }: { enabled: boolean; states: RuleStates | undefined; nodeName: (id: bigint) => string }) {
  if (!enabled) return <span className="muted">已停用</span>;
  if (!states) return <span className="muted">正常</span>;
  const names = (list: RuleStates["firing"]) => list.map((s) => nodeName(s.nodeId)).join("、");
  return (
    <>
      {states.firing.length > 0 && <span className="error">触发：{names(states.firing)}</span>}
      {states.firing.length > 0 && states.pending.length > 0 && " "}
      {states.pending.length > 0 && <span>待定：{names(states.pending)}</span>}
    </>
  );
}
