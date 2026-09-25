import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { Link } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
import { ConfirmDelete } from "../components/ConfirmDelete";
import { Secret } from "../components/Secret";
import { AdminService, type Node } from "../gen/probe/v1/admin_pb";
import { errorText } from "../api/auth";
import { useLatestError } from "../api/useLatestError";
import { graceText } from "../lib/alerts";

export function Nodes() {
  const qc = useQueryClient();
  const { error, mutationOptions } = useLatestError();
  const nodes = useQuery(AdminService.method.listNodes, {});
  const refresh = () => qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listNodes, cardinality: "finite" }) });
  // token 只在创建与换 token 的响应里各出现一次，hub 不存明文；展示不经过 isLatest 门控——门控丢弃迟到结果时会把这唯一一份明文一起丢掉。
  const [secret, setSecret] = useState<{ label: string; value: string } | null>(null);
  const [name, setName] = useState("");

  const create = useMutation(AdminService.method.createNode, {
    ...mutationOptions,
    onSuccess: (r) => { setSecret({ label: `节点 ${r.node?.name} 的 token`, value: r.token }); setName(""); void refresh(); },
  });
  // 各行共用一个 mutation observer，重叠的 mutate 只回调最后一次；因此任一行保存挂起时禁用全部行的保存，退出编辑的才是保存的那一行。
  // 返回刷新 promise，编辑态在列表显示已保存值之后才关闭。
  const update = useMutation(AdminService.method.updateNode, { ...mutationOptions, onSuccess: refresh });
  const remove = useMutation(AdminService.method.deleteNode, { ...mutationOptions, onSuccess: refresh });
  const rotate = useMutation(AdminService.method.rotateNodeToken, {
    ...mutationOptions,
    onSuccess: (r, req) => { setSecret({ label: `节点 ${nodes.data?.nodes.find((n) => n.id === req.id)?.name ?? req.id} 的新 token`, value: r.token }); return refresh(); },
  });
  const reorder = useMutation(AdminService.method.reorderNodes, { ...mutationOptions, onSuccess: refresh });

  const onCreate = (e: FormEvent) => { e.preventDefault(); create.mutate({ name }); };
  // 排序接口要求给出全部 id 的完整排列：交换相邻两项后整表提交。
  const move = (list: Node[], i: number, dir: -1 | 1) => {
    const ids = list.map((n) => n.id);
    const j = i + dir;
    if (j < 0 || j >= ids.length) return;
    [ids[i], ids[j]] = [ids[j], ids[i]];
    reorder.mutate({ ids });
  };

  const gate = queryGate(nodes);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const list = gate.data.nodes;
  return (
    <section>
      {gate.banner}
      <h1>节点</h1>
      {secret && <Secret label={secret.label} value={secret.value} />}
      <form onSubmit={onCreate} className="row">
        <label>新节点名称<input value={name} onChange={(e) => setName(e.target.value)} /></label>
        <button type="submit" disabled={create.isPending || name.trim() === ""}>创建</button>
      </form>
      {error != null && <p role="alert" className="error">{errorText(error)}</p>}
      <div className="table-scroll" role="region" aria-label="节点管理" tabIndex={0}>
        <table className="nodes">
          <thead><tr><th>排序</th><th>名称</th><th>公开</th><th>备注</th><th>重置日</th><th>离线宽限期</th><th>创建于</th><th>操作</th></tr></thead>
          <tbody>
            {list.map((n, i) => (
              <NodeEditor key={String(n.id)} node={n}
                saving={update.isPending} deleting={remove.isPending} rotating={rotate.isPending}
                onMoveUp={() => move(list, i, -1)} onMoveDown={() => move(list, i, 1)}
                onSave={(patch, onSuccess) => update.mutate({ id: n.id, ...patch }, { onSuccess })}
                onDelete={() => remove.mutate({ id: n.id })}
                onRotate={() => rotate.mutate({ id: n.id })} />
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

const validResetDay = (day: number) => Number.isInteger(day) && day >= 1 && day <= 28;

// 宽限期以字符串编辑，0 表示清除（取 hub 的 PROBE_OFFLINE_AFTER）。
const draftOf = (node: Node) => ({
  name: node.name, public: node.public, note: node.note, trafficResetDay: node.trafficResetDay,
  offlineGraceS: String(node.offlineGraceS ?? 0),
});
const validGrace = (s: string) => /^\d+$/.test(s);

function NodeEditor({ node, saving, deleting, rotating, onMoveUp, onMoveDown, onSave, onDelete, onRotate }: {
  node: Node;
  saving: boolean; deleting: boolean; rotating: boolean;
  onMoveUp: () => void; onMoveDown: () => void;
  onSave: (patch: { name: string; public: boolean; note: string; trafficResetDay: number; offlineGraceS: number }, onSuccess: () => void) => void;
  onDelete: () => void; onRotate: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(draftOf(node));
  if (editing) {
    return (
      <tr>
        <td />
        <td><input aria-label="名称" value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} /></td>
        <td><input type="checkbox" aria-label="公开" checked={draft.public} onChange={(e) => setDraft({ ...draft, public: e.target.checked })} /></td>
        <td><input aria-label="备注" value={draft.note} onChange={(e) => setDraft({ ...draft, note: e.target.value })} /></td>
        <td><input type="number" min={1} max={28} aria-label="重置日" value={draft.trafficResetDay} onChange={(e) => setDraft({ ...draft, trafficResetDay: Number(e.target.value) })} /><p className="muted">若从本周期起点算起新的重置日已经过去，本周期用量会立即清零。</p></td>
        <td><input type="number" min={0} aria-label="离线宽限期（秒）" aria-describedby={`grace-hint-${node.id}`} value={draft.offlineGraceS} onChange={(e) => setDraft({ ...draft, offlineGraceS: e.target.value })} /><p className="muted" id={`grace-hint-${node.id}`}>0 表示取 hub 的 PROBE_OFFLINE_AFTER；非 0 不能小于它。</p></td>
        <td />
        <td>
          <button type="button" disabled={saving || !validResetDay(draft.trafficResetDay) || !validGrace(draft.offlineGraceS)} onClick={() => onSave({ ...draft, offlineGraceS: Number(draft.offlineGraceS) }, () => setEditing(false))}>保存</button>{" "}
          <button type="button" className="link" onClick={() => setEditing(false)}>取消</button>
        </td>
      </tr>
    );
  }
  return (
    <tr>
      <td>
        <button type="button" className="link" aria-label={`上移 ${node.name}`} onClick={onMoveUp}>↑</button>
        <button type="button" className="link" aria-label={`下移 ${node.name}`} onClick={onMoveDown}>↓</button>
      </td>
      <td><Link to={`/nodes/${node.id}`}>{node.name}</Link></td>
      <td>{node.public ? "是" : "否"}</td>
      <td className="muted">{node.note}</td>
      <td>每月 {node.trafficResetDay} 日</td>
      <td>{graceText(node.offlineGraceS)}</td>
      <td className="muted">{new Date(Number(node.createdAt) * 1000).toLocaleDateString()}</td>
      <td>
        <button type="button" className="link" onClick={() => { setDraft(draftOf(node)); setEditing(true); }}>编辑</button>{" "}
        <button type="button" className="link" onClick={onRotate} disabled={rotating}>换 token</button>{" "}
        <ConfirmDelete label="删除" confirm={`确认删除 ${node.name}`} pending={deleting} onDelete={onDelete} />
      </td>
    </tr>
  );
}
