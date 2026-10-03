import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { errorText } from "../api/auth";
import { errorBanner, queryGateAll } from "../api/queryGate";
import { useLatestError } from "../api/useLatestError";
import { ConfirmDelete } from "../components/ConfirmDelete";
import { Picks } from "../components/Picks";
import { NodeSelector, type NodeSelection } from "../components/NodeSelector";
import { AdminService, AlertKind, ProbeMetric, ResourceMetric, type AlertRule, type Node, type NotifyChannel, type ProbeTaskDetail } from "../gen/heron/v1/admin_pb";
import { ProbeKind } from "../gen/heron/v1/types_pb";
import { ALERT_KINDS, MBPS_TO_BYTES_PER_S, PROBE_METRICS, RESOURCE_METRICS, labelOf, resourceThresholdMax, resourceUnit, ruleCondition, statesOf, taskLabels, type RuleStates } from "../lib/alerts";
import { liveIds, withId } from "../lib/ids";

type Draft = NodeSelection & {
  name: string; kind: AlertKind; enabled: boolean; allNodes: boolean; nodeIds: Set<bigint>; channelIds: Set<bigint>;
  taskId: string; metric: ProbeMetric; threshold: string; forMinutes: string; daysBefore: string;
  resourceMetric: ResourceMetric; recoveryThreshold: string;
};

// 种类专用字段里有默认值的是指标（丢包率）、连续分钟 3 与提前天数 7；探测任务与阈值留空，切到探测时由表单的 required
// 挡住提交，要用户自己选填。种类、启用与"全部节点"另有各自的默认值。只有当前类型的那几项随保存发出（toRule）。
const emptyDraft = (): Draft => ({
  name: "", kind: AlertKind.OFFLINE, enabled: true, allNodes: true, nodeIds: new Set(), channelIds: new Set(),
  taskId: "", metric: ProbeMetric.LOSS_PCT, threshold: "", forMinutes: "3", daysBefore: "7",
  selectorTags: [], dynamic: false, resourceMetric: ResourceMetric.MEMORY_USED_PCT, recoveryThreshold: "80",
});
const draftOf = (r: AlertRule): Draft => {
  const probe = r.kind === AlertKind.PROBE;
  // 证书到期与探测一样引用任务，与到期一样带提前天数。
  const withTask = probe || r.kind === AlertKind.CERT_EXPIRY;
  // 速率指标的协议值是 bytes/s，草稿按 Mbps 展示（RESOURCE_METRICS 的 unit 决定换算方向）。
  const scale = r.kind === AlertKind.RESOURCE && resourceUnit(r.resourceMetric) === "mbps" ? MBPS_TO_BYTES_PER_S : 1;
  return {
    name: r.name, kind: r.kind, enabled: r.enabled, allNodes: r.allNodes, nodeIds: new Set(r.nodeIds), channelIds: new Set(r.channelIds),
    taskId: withTask ? String(r.taskId) : "", metric: probe ? r.metric : ProbeMetric.LOSS_PCT,
    threshold: probe ? String(r.threshold) : r.kind === AlertKind.RESOURCE ? String(r.threshold / scale) : "", forMinutes: probe || r.kind === AlertKind.RESOURCE ? String(r.forMinutes) : "3",
    daysBefore: r.kind === AlertKind.EXPIRY || r.kind === AlertKind.CERT_EXPIRY ? String(r.daysBefore) : "7",
    selectorTags: r.selectorTags, dynamic: r.selectorTags.length > 0,
    resourceMetric: r.kind === AlertKind.RESOURCE ? r.resourceMetric : ResourceMetric.MEMORY_USED_PCT,
    recoveryThreshold: r.kind === AlertKind.RESOURCE ? String(r.recoveryThreshold / scale) : "80",
  };
};

// 资源阈值在表单里按指标单位输入，协议值统一为 bytes/s 或原值：Mbps 在这里换算回 bytes/s。
function resourceRule(d: Draft) {
  const scale = resourceUnit(d.resourceMetric) === "mbps" ? MBPS_TO_BYTES_PER_S : 1;
  return { resourceMetric: d.resourceMetric, threshold: Number(d.threshold) * scale, recoveryThreshold: Number(d.recoveryThreshold) * scale, forMinutes: Number(d.forMinutes) };
}

// 当前列表不再包含的节点与渠道自然掉出，避免已删除对象让 hub 拒绝整次保存；
// 显式作用域因此变空时由 hub 拒绝（spec §6.6：空集不等于全部节点），不会悄悄放宽成全部。
// 种类专用字段只发当前类型的：hub 拒绝带着别的种类字段的规则（alert.CheckRule）。
function toRule(id: bigint, d: Draft, nodes: Node[], channels: NotifyChannel[]) {
  const own = d.kind === AlertKind.PROBE
    ? { taskId: BigInt(d.taskId), metric: d.metric, threshold: Number(d.threshold), forMinutes: Number(d.forMinutes) }
    : d.kind === AlertKind.EXPIRY ? { daysBefore: Number(d.daysBefore) }
      : d.kind === AlertKind.CERT_EXPIRY ? { taskId: BigInt(d.taskId), daysBefore: Number(d.daysBefore) }
        : d.kind === AlertKind.RESOURCE ? resourceRule(d) : {};
  return {
    id, name: d.name.trim(), kind: d.kind, enabled: d.enabled, allNodes: d.allNodes,
    nodeIds: d.allNodes || d.dynamic ? [] : liveIds(d.nodeIds, nodes), selectorTags: !d.allNodes && d.dynamic ? d.selectorTags : [], channelIds: liveIds(d.channelIds, channels), ...own,
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
  // 节点与渠道求交需要相应列表，任务列表用于任务选择与标签；依赖未到达时不渲染可提交表单。
  const gate = queryGateAll(nodes, channels, tasks, rules);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const [nodesData, channelsData, tasksData, rulesData] = gate.data;
  const nodeList = nodesData.nodes;
  const channelList = channelsData.channels;
  const taskList = tasksData.tasks;
  const byRule = statesOf(rulesData.states);
  const nodeName = (id: bigint) => nodeList.find((n) => n.id === id)?.name ?? `节点 #${id}`;
  const channelName = (id: bigint) => channelList.find((c) => c.id === id)?.name ?? `渠道 #${id}`;
  const lists = { nodes: nodeList, channels: channelList, tasks: taskList };
  return (
    <section>
      {gate.banner}
      <h1>告警规则</h1>
      <RuleForm key={creation} title="新建告警规则" {...lists} initial={emptyDraft()} pending={create.isPending}
        onSubmit={(d) => create.mutate({ rule: toRule(0n, d, nodeList, channelList) }, { onSuccess: () => setCreation((k) => k + 1) })} />
      {error != null && <p role="alert" className="error">{errorText(error)}</p>}
      <div className="table-scroll" role="region" aria-label="告警规则管理" tabIndex={0}>
        <table className="nodes">
          <thead><tr><th>名称</th><th>类型</th><th>条件</th><th>作用域</th><th>通知</th><th>状态</th><th>操作</th></tr></thead>
          <tbody>
            {rulesData.rules.map((r) => (
              <RuleRow key={String(r.id)} rule={r} states={byRule.get(r.id)} {...lists} nodeName={nodeName} channelName={channelName}
                saving={update.isPending} deleting={remove.isPending}
                onSave={(d, onSuccess) => update.mutate({ rule: toRule(r.id, d, nodeList, channelList) }, { onSuccess })}
                onDelete={() => remove.mutate({ id: r.id })} />
            ))}
          </tbody>
        </table>
      </div>
      {rulesData.rules.length === 0 && <p className="muted">还没有告警规则。</p>}
    </section>
  );
}

type Lists = { nodes: Node[]; channels: NotifyChannel[]; tasks: ProbeTaskDetail[] };

// 同类型同目标没有唯一约束。只在这份下拉里碰撞的标签追加编号，否则两个任务无法区分，选错会盯住另一个任务。
// httpsOnly 只列 https:// 的 HTTP 任务：证书到期规则只能挂在它们上面（hub 的 requireHTTPSProbeTask 同样裁决）。
function taskOptions(tasks: ProbeTaskDetail[], httpsOnly = false) {
  const listed = tasks.flatMap((d) => (d.task ? [d.task] : []))
    .filter((t) => !httpsOnly || (t.kind === ProbeKind.HTTP && t.target.startsWith("https://")));
  const labels = taskLabels(listed.map((t) => t.id), tasks);
  return listed.map((t, i) => <option key={String(t.id)} value={String(t.id)}>{labels[i]}</option>);
}

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
      {draft.kind === AlertKind.EXPIRY ? (
        <>
          <div className="row">
            <label>提前天数<input type="number" required min={1} max={365} step={1} value={draft.daysBefore} onChange={(e) => set({ daysBefore: e.target.value })} /></label>
          </div>
          <p className="muted">节点到期日距今不超过提前天数即触发（已过期的也算）；到期日改到这个范围之外、清除到期日，或调小提前天数使它落到范围之外，即恢复。续期后的到期日仍在范围内时不恢复。到期日在节点页设置。保存后立即评估，此后在 hub 启动时、hub 时区的每个日界（零点不存在的日子取新一天的第一个时刻）与修改节点计费时评估。</p>
        </>
      ) : draft.kind === AlertKind.CERT_EXPIRY ? (
        <>
          <div className="row">
            <label>探测任务
              <select required value={draft.taskId} onChange={(e) => set({ taskId: e.target.value })}>
                <option value="">选择任务</option>
                {taskOptions(tasks, true)}
              </select>
            </label>
            <label>提前天数<input type="number" required min={1} max={365} step={1} value={draft.daysBefore} onChange={(e) => set({ daysBefore: e.target.value })} /></label>
          </div>
          <p className="muted">任务的证书到期日距今不超过提前天数即触发（已过期的也算）；证书更换使到期日落到范围之外，或调小提前天数使它落到范围之外，即恢复。证书到期日由 HTTPS 探测握手成功时顺带带回（每任务每小时至多一次）。保存后立即评估，此后在 hub 启动时、hub 时区的每个日界与证书观测变化时评估。</p>
        </>
      ) : draft.kind === AlertKind.RESOURCE ? (
        <ResourceFields draft={draft} set={set} />
      ) : probe ? (
        <div className="row">
          <label>探测任务
            <select required value={draft.taskId} onChange={(e) => set({ taskId: e.target.value })}>
              <option value="">选择任务</option>
              {taskOptions(tasks)}
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
        <p className="muted">节点超过离线宽限期未上报即触发，收到上报即恢复；宽限期在节点页按节点设置，未设置时取 hub 的 HERON_OFFLINE_AFTER。</p>
      )}
      <NodeSelector nodes={nodes} value={draft} onChange={set} legend="作用域节点" />
      {probe && <p className="muted">探测规则只在既属于作用域、又分配了该任务的节点上评估。</p>}
      {draft.kind === AlertKind.CERT_EXPIRY && <p className="muted">证书到期规则只在既属于作用域、又分配了该任务的节点上评估；没有证书观测的节点不评估。</p>}
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

// 阈值输入的单位与范围随指标切换（RESOURCE_METRICS 的 unit），提示文案与 hub CheckRule 的取值范围一致；
// 速率按 Mbps 输入，提交时换算成 bytes/s（resourceRule）。
function ResourceFields({ draft, set }: { draft: Draft; set: (patch: Partial<Draft>) => void }) {
  const unit = resourceUnit(draft.resourceMetric);
  const unitLabel = unit === "mbps" ? "Mbps" : unit === "per-core" ? "每核" : "%";
  const max = resourceThresholdMax(draft.resourceMetric);
  const source = unit === "mbps" ? "网卡速率的每分钟均值" : unit === "per-core" ? "load1 每分钟均值 ÷ 节点核数；节点尚未上报核数时该分钟按缺失读数处理" : "同次采样的使用量与总量之比（CPU 为采样占比）的每分钟均值";
  return (
    <>
      <div className="row">
        <label>资源指标<select value={draft.resourceMetric} onChange={(e) => set({ resourceMetric: Number(e.target.value) as ResourceMetric })}>
          {RESOURCE_METRICS.map(({ value, label }) => <option key={value} value={value}>{label}</option>)}
        </select></label>
        <label>触发阈值（{unitLabel}）<input type="number" required step="any" min={0} max={max} value={draft.threshold} onChange={(e) => set({ threshold: e.target.value })} /></label>
        <label>恢复阈值（{unitLabel}）<input type="number" required step="any" min={0} max={max} value={draft.recoveryThreshold} onChange={(e) => set({ recoveryThreshold: e.target.value })} /></label>
        <label>连续分钟<input type="number" required min={1} max={60} value={draft.forMinutes} onChange={(e) => set({ forMinutes: e.target.value })} /></label>
      </div>
      <p className="muted">触发阈值大于 0 且不超过 {max} {unitLabel}，恢复阈值必须低于触发阈值。数据源：{source}；触发与恢复均需完整连续窗口，缺失读数不会恢复。</p>
    </>
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
      <RuleForm title={`编辑 ${withId(r.name, r.id)}`} nodes={nodes} channels={channels} tasks={tasks} initial={draftOf(r)} pending={saving}
          onSubmit={(d) => onSave(d, () => setEditing(false))} onCancel={() => setEditing(false)} />
      </td></tr>
    );
  }
  return (
    <tr>
      <td>{r.name}</td>
      <td>{labelOf(ALERT_KINDS, r.kind)}</td>
      <td>{ruleCondition(r, tasks)}</td>
      <td>{r.allNodes ? "全部节点" : r.selectorTags.length ? `动态标签：${r.selectorTags.join(" ∩ ")}；当前 ${r.nodeIds.length} 个节点` : r.nodeIds.map(nodeName).join("、") || "无节点"}</td>
      <td>{r.channelIds.map(channelName).join("、") || "只记事件"}</td>
      <td><RuleState enabled={r.enabled} states={states} nodeName={nodeName} /></td>
      <td>
        <button type="button" className="link" aria-label={`编辑 ${withId(r.name, r.id)}`} onClick={() => setEditing(true)}>编辑</button>{" "}
        <ConfirmDelete label={`删除 ${withId(r.name, r.id)}`} confirm={`确认删除 ${withId(r.name, r.id)}`} note="事件记录保留" pending={deleting} onDelete={onDelete} />
      </td>
    </tr>
  );
}

// 停用的规则不评估，hub 保存停用时已清除其状态行；显示"已停用"而不是"正常"，避免读成一切无事。
function RuleState({ enabled, states, nodeName }: { enabled: boolean; states: RuleStates | undefined; nodeName: (id: bigint) => string }) {
  if (!enabled) return <span className="muted">已停用</span>;
  if (!states) return <span className="muted">正常</span>;
  // flapping 由 hub 的离线巡检判定（pending 且只因抖动抑制而未触发），silenced 由事件生成处标记
  // （firing 进入时处于维护静默覆盖内，本次触发与它的恢复都不投递），面板只标出，不重算。
  const names = (list: RuleStates["firing"]) => list.map((s) => nodeName(s.nodeId) + (s.flapping ? "（抖动中）" : "") + (s.state === "firing" && s.silenced ? "（已静默）" : "")).join("、");
  return (
    <>
      {states.firing.length > 0 && <span className="error">触发：{names(states.firing)}</span>}
      {states.firing.length > 0 && states.pending.length > 0 && " "}
      {states.pending.length > 0 && <span>待定：{names(states.pending)}</span>}
    </>
  );
}
