import { useMutation, useQuery } from "@connectrpc/connect-query";
import { useState } from "react";
import { errorText } from "../api/auth";
import { errorBanner, queryGateAll } from "../api/queryGate";
import { Modal } from "../components/Modal";
import { AdminService } from "../gen/heron/v1/admin_pb";
import type { UpdateStatus } from "../gen/heron/v1/update_pb";
import { lagsHub } from "../lib/version";

const labels: Record<string, string> = {
  queued: "等待节点上线", dispatched: "已下发", downloading: "下载并校验", stopping: "停止旧服务",
  installing: "替换程序", verifying: "验证新版本", rolling_back: "正在回滚", succeeded: "更新成功", failed: "更新失败",
  rolled_back: "已回滚", cancelled: "已取消", expired: "已过期", unconfirmed: "结果未确认",
};
const activeStates = new Set(["queued", "dispatched", "downloading", "stopping", "installing", "verifying", "rolling_back"]);
function eligible(status: UpdateStatus | undefined, version: string) {
  return !!status?.supported && !!version && lagsHub(status.version, version) && !activeStates.has(status.task?.state ?? "");
}
function Progress({ status }: { status?: UpdateStatus }) {
  if (!status) return <span className="muted">尚未收到更新能力，请先升级安装器与 agent。</span>;
  return <div className="update-progress">
    {!status.supported && <span className="muted">不支持在线更新：{status.reason || "本机更新器不可用"}</span>}
    {status.task && <><strong>{labels[status.task.state] ?? status.task.state}</strong><span className="muted">目标 {status.task.version}</span>
      {status.task.state === "unconfirmed" && <span className="muted">等待超时或本机任务记录缺失，无法确认执行结果；后续上报仍会校正。重试由节点本机更新器检查是否可执行。</span>}
      {status.task.error && <span className="error">{status.task.error}</span>}</>}
    {status.supported && !status.task && <span className="muted">可以在线更新</span>}
  </div>;
}

export function Updates() {
  const updates = useQuery(AdminService.method.getUpdates, {}, { refetchInterval: 3000 });
  const nodes = useQuery(AdminService.method.listNodes, {});
  const [latest, setLatest] = useState("");
  const [selected, setSelected] = useState<Set<bigint>>(new Set());
  const [confirmation, setConfirmation] = useState<{ ids: bigint[]; version: string; opener: HTMLElement } | null>(null);
  const [busy, setBusy] = useState(false);
  const [results, setResults] = useState<string[]>([]);
  const check = useMutation(AdminService.method.getUpdates, { onSuccess: (data) => setLatest(data.latestVersion) });
  const start = useMutation(AdminService.method.startUpdate);
  const cancel = useMutation(AdminService.method.cancelUpdate, { onSuccess: () => { void updates.refetch(); } });
  const gate = queryGateAll(updates, nodes);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const targets = new Map(updates.data!.targets.map((target) => [target.nodeId, target.status]));
  const hub = targets.get(0n);
  const chosen = [...selected].filter((id) => eligible(targets.get(id), latest));
  const nameOf = (id: bigint) => id === 0n ? "Hub" : nodes.data!.nodes.find((node) => node.id === id)?.name ?? `节点 #${id}`;
  const execute = async () => {
    if (!confirmation || busy) return;
    setBusy(true); setResults([]);
    for (const id of confirmation.ids) {
      try {
        await start.mutateAsync({ nodeId: id, version: confirmation.version });
        setResults((old) => [...old, `${nameOf(id)}：更新任务已提交，最终结果以状态回读为准。`]);
      } catch (error) {
        setResults((old) => [...old, `${nameOf(id)}：${errorText(error)}${id === 0n ? "。若连接已断开，请等待状态回读，不能据此判定更新成功或失败。" : ""}`]);
      }
    }
    setBusy(false); setConfirmation(null); setSelected(new Set()); void updates.refetch();
  };
  return <section>
    <div className="page-heading"><div><h1>在线更新</h1><p>官方正式发行版 · Linux systemd</p></div>
      <button type="button" disabled={check.isPending || busy} onClick={() => check.mutate({ checkLatest: true })}>{check.isPending ? "正在检查…" : "检查官方新版本"}</button>
    </div>
    {gate.banner}
    {updates.error && <p role="status">暂时无法连接 Hub，正在重试。更新结果尚未确认，请勿重复提交。</p>}
    {check.error && <p role="alert" className="error">{errorText(check.error)}</p>}
    {check.data?.checkError && <p role="alert" className="error">检查官方版本失败：{check.data.checkError}</p>}
    {cancel.error && <p role="alert" className="error">{errorText(cancel.error)}</p>}
    <div className="update-summary">
      <div className="card"><span className="muted">Hub 当前版本</span><h2>{hub?.version || "未知"}</h2><Progress status={hub} />
        <button type="button" disabled={!eligible(hub, latest) || busy} onClick={(event) => setConfirmation({ ids: [0n], version: latest, opener: event.currentTarget })}>更新 Hub</button>
      </div>
      <div className="card"><span className="muted">官方最新正式版</span><h2>{latest || "尚未检查"}</h2>
        <p className="muted">仅从 xjetry/heron-probe 的正式 Release 下载并校验产物，不执行远程命令。</p>
        <p className="muted">首次启用需用新版安装器安装本机更新服务。Docker、OpenRC 与 macOS 请使用各自安装方式。</p>
      </div>
    </div>
    {results.length > 0 && <div className="card" role="status">{results.map((result, i) => <p key={i}>{result}</p>)}</div>}
    <div className="page-heading"><div><h2>节点 Agent</h2><p>离线任务最多等待 24 小时；新版本成功上报后才算完成。</p></div>
      <button type="button" disabled={chosen.length === 0 || busy} onClick={(event) => setConfirmation({ ids: chosen, version: latest, opener: event.currentTarget })}>更新选中节点（{chosen.length}）</button>
    </div>
    <div className="table-scroll" role="region" aria-label="节点更新" tabIndex={0}><table className="nodes">
      <thead><tr><th>选择</th><th>节点</th><th>当前版本</th><th>更新状态</th><th>操作</th></tr></thead>
      <tbody>{nodes.data!.nodes.map((node) => {
        const status = targets.get(node.id);
        return <tr key={String(node.id)}><td><input type="checkbox" aria-label={`选择 ${node.name}（#${node.id}）`} checked={selected.has(node.id)} disabled={!eligible(status, latest) || busy} onChange={(event) => setSelected((old) => {
          const next = new Set(old); if (event.target.checked) next.add(node.id); else next.delete(node.id); return next;
        })} /></td><td>{node.name}<small className="muted"> #{String(node.id)}</small></td><td><code>{status?.version || "未知"}</code></td><td><Progress status={status} /></td><td>
          {status?.task?.state === "queued" && <button type="button" disabled={cancel.isPending || busy} onClick={() => cancel.mutate({ nodeId: node.id, id: status.task!.id })}>取消排队</button>}
        </td></tr>;
      })}</tbody>
    </table></div>
    {nodes.data!.nodes.length === 0 && <p className="muted">还没有节点。</p>}
    {confirmation && <Modal title={confirmation.ids[0] === 0n ? "确认更新 Hub" : "确认更新节点"} opener={confirmation.opener} busy={busy} onClose={() => setConfirmation(null)}>
      <div className="modal-body"><p>将 {confirmation.ids.map(nameOf).join("、")} 更新到 <strong>{confirmation.version}</strong>。</p>
        <p className="muted">{confirmation.ids[0] === 0n ? "Hub 将短暂断连。更新器会在停服后备份数据库，启动验证失败时恢复程序和数据库。请等待重新连接后的任务结果。" : "更新会短暂中断节点上报。已经下发的任务无法取消；失败时由本机更新器恢复旧程序。"}</p>
      </div><div className="modal-footer"><button type="button" disabled={busy} onClick={() => setConfirmation(null)}>返回</button><button type="button" className="primary" disabled={busy} onClick={() => { void execute(); }}>{busy ? "正在提交…" : "确认更新"}</button></div>
    </Modal>}
  </section>;
}
