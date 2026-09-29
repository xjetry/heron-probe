import { createConnectQueryKey, createQueryOptions, useMutation, useQuery, useTransport } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { Link } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
import { errorText } from "../api/auth";
import { useLatestError } from "../api/useLatestError";
import { useRetained } from "../api/useRetained";
import { useOrder } from "../api/useOrder";
import { ConfirmDelete } from "../components/ConfirmDelete";
import { Icon } from "../components/Icon";
import { Modal } from "../components/Modal";
import { NodeAddresses } from "../components/NodeAddresses";
import { NodeCountry } from "../components/NodeCountry";
import { Secret } from "../components/Secret";
import { AdminService, type Node, type NodeStatus, type Tag } from "../gen/heron/v1/admin_pb";
import { expired, expiryText, priceText } from "../lib/billing";
import { bytes } from "../lib/format";
import { withId } from "../lib/ids";
import { filterNodes } from "../lib/nodeSearch";
import { POLL_MS } from "../lib/poll";
import { sameTag, withoutTag, withTag } from "../lib/tags";
import { lagsHub } from "../lib/version";
import { NodeEditor } from "./NodeEditor";

export function Nodes() {
  const qc = useQueryClient();
  const transport = useTransport();
  const { error, mutationOptions } = useLatestError();
  const [tagFilter, setTagFilter] = useState<string[]>([]);
  // 过滤切换失败时保留旧列表；弹窗草稿独立于列表，刷新与排序不会卸载正在编辑的节点。
  const nodes = useRetained(useQuery(AdminService.method.listNodes, { tags: tagFilter }, { refetchInterval: 10_000 }));
  const tags = useQuery(AdminService.method.listTags, {});
  const snapshot = useQuery(AdminService.method.getSnapshot, {}, { refetchInterval: POLL_MS });
  const hubVersion = snapshot.data?.hubVersion;
  const statusById = new Map(snapshot.data?.nodes.map((node) => [node.id, node]));
  const refresh = (options?: { throwOnError: boolean }) => Promise.all([
    qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listNodes, cardinality: "finite" }) }, options),
    qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listTags, cardinality: "finite" }) }, options),
  ]);
  // 创建与轮换的响应是唯一明文来源，不能丢弃迟到响应；删除同一节点时同步清掉它的凭据卡片。
  const [secret, setSecret] = useState<{ id: bigint; label: string; value: string } | null>(null);
  const [creating, setCreating] = useState<HTMLElement | null>(null);
  const [name, setName] = useState("");
  const [search, setSearch] = useState("");
  const [editor, setEditor] = useState<{ node: Node; mode: "general" | "billing"; opener: HTMLElement } | null>(null);
  const create = useMutation(AdminService.method.createNode, {
    ...mutationOptions,
    onSuccess: (result) => {
      const node = result.node;
      if (node) setSecret({ id: node.id, label: `节点 ${withId(node.name, node.id)} 的 token`, value: result.token });
      setName(""); setCreating(null);
      void refresh();
    },
  });
  // 单一弹窗承载当前目标；保存期间不可关闭或切换，回读完成后才解除编辑态。
  const update = useMutation(AdminService.method.updateNode, {
    ...mutationOptions,
    onSuccess: async () => {
      try { await refresh({ throwOnError: true }); }
      catch (error) { throw new Error(`已保存，但回读失败：${errorText(error)}`); }
    },
  });
  const remove = useMutation(AdminService.method.deleteNode, {
    ...mutationOptions,
    onSuccess: (_result, request) => { setSecret((current) => current?.id === request.id ? null : current); return refresh(); },
  });
  const rotate = useMutation(AdminService.method.rotateNodeToken, {
    ...mutationOptions,
    onSuccess: (result, request) => {
      if (request.id == null) return refresh();
      const id = request.id;
      const name = nodes.data?.nodes.find((node) => node.id === id)?.name ?? String(id);
      setSecret({ id, label: `节点 ${withId(name, id)} 的新 token`, value: result.token });
      return refresh();
    },
  });
  const reorder = useMutation(AdminService.method.reorderNodes);
  const filtered = search !== "" || tagFilter.length > 0;
  const narrowed = filtered || nodes.stale;
  const order = useOrder({
    items: nodes.data?.nodes ?? [], id: (node) => node.id, enabled: !narrowed && nodes.data !== undefined,
    save: (ids) => reorder.mutateAsync({ ids }),
    reload: async () => {
      // 排序要求完整排列，回读固定空标签，不受当前过滤器影响。
      const options = createQueryOptions(AdminService.method.listNodes, { tags: [] }, { transport });
      await qc.cancelQueries({ queryKey: options.queryKey, exact: true });
      return (await qc.fetchQuery({ ...options, staleTime: 0 })).nodes;
    },
  });
  const removeTag = useMutation(AdminService.method.deleteTag, {
    ...mutationOptions,
    onSuccess: (_result, request) => { setTagFilter((current) => withoutTag(current, request.name ?? "")); return refresh(); },
  });
  const onCreate = (event: FormEvent) => { event.preventDefault(); if (name.trim() && !create.isPending) create.mutate({ name }); };
  const gate = queryGate(nodes);
  const list = filterNodes(order.items, search);
  const editing = editor !== null || creating !== null;
  const openEditor = (node: Node, mode: "general" | "billing", opener: HTMLElement) => {
    if (editing) return;
    update.reset();
    setEditor({ node, mode, opener });
  };

  return <section>
    <header className="page-heading">
      <div><div className="eyebrow">Infrastructure</div><h1>节点</h1><p>管理节点资产、网络连接与到期信息。</p></div>
      <button type="button" className="primary-button" disabled={editing} onClick={(event) => { create.reset(); setCreating(event.currentTarget); }}><Icon name="plus" />添加节点</button>
    </header>
    {!editor && errorBanner(nodes.error)}
    {snapshot.error != null && <p role="alert" className="error">{hubVersion === undefined ? "无法取得 hub 版本，落后标记不可用" : `刷新 hub 版本失败，落后标记按上次取得的 ${hubVersion || "空版本"} 判断`}；在线状态与流量可能不是最新值：{errorText(snapshot.error)}</p>}
    {secret && <Secret label={secret.label} value={secret.value} />}
    <div className="node-filters">
      <label className="node-search">搜索节点<input type="search" placeholder="名称、IP、地区、备注或主机名" value={search} onChange={(event) => setSearch(event.target.value)} /></label>
      <TagFilter tags={tags.data?.tags} error={tags.error} selected={tagFilter} onChange={setTagFilter} />
    </div>
    {!editing && errorBanner(error)}
    {order.error != null && <p role="alert" className="error">排序未完成：{errorText(order.error)}</p>}
    {order.pending && <p role="status" className="muted">正在保存并确认排序…</p>}
    {order.blocked && <button type="button" onClick={order.recover} disabled={order.pending}>重新读取排序</button>}
    {filtered && <p className="node-subtext">搜索或按标签过滤时无法排序，请清空搜索与标签过滤后调整完整节点顺序。</p>}
    {!filtered && nodes.stale && <p className="node-subtext">列表还不是当前条件下的结果，暂时无法排序。</p>}
    {gate.ready ? <>
      <div className="section-heading"><h2>节点清单 <span className="muted">{list.length}</span></h2><span className="live-caption">双栈出口由 agent 独立探测</span></div>
      {list.length === 0 && <p className="node-empty" role="status">{narrowed ? "没有匹配的节点。" : "还没有节点，添加节点后安装 agent 即可开始监控。"}</p>}
      <div className="table-scroll" role="region" aria-label="节点管理" tabIndex={0}>
        <table className="nodes node-management"><thead><tr><th data-column="order"><span className="sr-only">排序</span></th><th data-column="name">节点</th><th data-column="addresses">IP 地址</th><th data-column="status">状态</th><th>本周期流量</th><th>计费</th><th>到期</th><th data-column="actions">操作</th></tr></thead>
          <tbody>{list.map((node) => <NodeRow key={String(node.id)} node={node} status={statusById.get(node.id)} hubVersion={hubVersion}
            editing={editing} deleting={remove.isPending} rotating={rotate.isPending}
            onMoveUp={narrowed || order.blocked ? undefined : () => order.move(node.id, -1)}
            onMoveDown={narrowed || order.blocked ? undefined : () => order.move(node.id, 1)}
            onEdit={(mode, opener) => openEditor(node, mode, opener)} onDelete={() => remove.mutate({ id: node.id })} onRotate={() => rotate.mutate({ id: node.id })} />)}</tbody>
        </table>
      </div>
    </> : gate.loading}
    <TagManager tags={tags.data?.tags} pending={removeTag.isPending} onDelete={(name) => removeTag.mutate({ name })} />
    {editor && <NodeEditor key={String(editor.node.id)} node={editor.node} mode={editor.mode} opener={editor.opener} knownTags={tags.data?.tags ?? []}
      saving={update.isPending} error={update.error} listError={nodes.error} onClose={() => setEditor(null)}
      onSave={(patch) => update.mutate({ id: editor.node.id, ...patch }, { onSuccess: () => setEditor(null) })} />}
    {creating && <Modal title="添加节点" description="创建后将显示一次性 token，用于安装 agent。" busy={create.isPending} opener={creating} onClose={() => setCreating(null)}>
      <form onSubmit={onCreate}><div className="modal-body">{errorBanner(create.error)}<label>新节点名称<input data-autofocus value={name} onChange={(event) => setName(event.target.value)} placeholder="例如 tokyo-01" disabled={create.isPending} /></label></div>
        <footer className="modal-footer"><button type="button" disabled={create.isPending} onClick={() => setCreating(null)}>取消</button><button type="submit" className="primary-button" disabled={create.isPending || name.trim() === ""}>创建</button></footer>
      </form>
    </Modal>}
  </section>;
}

function NodeRow({ node, status, hubVersion, editing, deleting, rotating, onMoveUp, onMoveDown, onEdit, onDelete, onRotate }: {
  node: Node; status?: NodeStatus; hubVersion?: string; editing: boolean; deleting: boolean; rotating: boolean;
  onMoveUp?: () => void; onMoveDown?: () => void; onEdit: (mode: "general" | "billing", opener: HTMLElement) => void; onDelete: () => void; onRotate: () => void;
}) {
  const label = withId(node.name, node.id);
  return <tr>
    <td data-column="order" data-label="排序"><button type="button" className="icon-button" aria-label={`上移 ${label}`} onClick={onMoveUp} disabled={!onMoveUp}><Icon name="chevronUp" /></button><button type="button" className="icon-button" aria-label={`下移 ${label}`} onClick={onMoveDown} disabled={!onMoveDown}><Icon name="chevronDown" /></button></td>
    <td data-column="name" data-label="节点"><div className="node-name-line"><Link to={`/nodes/${node.id}`} aria-label={label}>{node.name}</Link><NodeCountry node={node} /></div>
      <div aria-label={`标签 ${label}`}>{node.tags.map((tag) => <span key={tag} className="tag">{tag}</span>)}</div>
      {node.note && <p className="node-subtext node-note" title={node.note}>{node.note}</p>}
      <span className="node-subtext">{node.public ? "公开" : "仅管理端"}</span>
      {hubVersion !== undefined && lagsHub(node.facts?.agentVersion, hubVersion) && <span className="node-subtext warn">落后于 hub</span>}
    </td>
    <td data-column="addresses" data-label="IP 地址"><NodeAddresses network={node.facts?.network} /></td>
    <td data-column="status" data-label="状态"><span className={`status-pill ${status ? status.online ? "is-online" : "is-offline" : ""}`}>{status && <span className={`dot ${status.online ? "ok" : "bad"}`} />}{status ? status.online ? "在线" : "离线" : "状态未知"}</span></td>
    <td data-column="traffic" data-label="本周期流量">{status?.traffic ? <div className="node-traffic"><span>↓ {bytes(status.traffic.periodRx)}</span><span className="muted">↑ {bytes(status.traffic.periodTx)}</span></div> : <span className="muted">暂无读数</span>}</td>
    <td data-column="billing" data-label="计费"><span className="node-price">{priceText(node.billing) || "未设置"}</span>{node.billing?.autoRenew && <div className="node-subtext">自动续期</div>}</td>
    <td data-column="expiry" data-label="到期"><span className={expired(node.billing) ? "error" : undefined}>{expiryText(node.billing) || "未设置"}</span></td>
    <td data-column="actions" data-label="操作"><div className="node-actions">
      <button type="button" className="icon-button" title="编辑节点" aria-label={`编辑 ${label}`} disabled={editing} onClick={(event) => onEdit("general", event.currentTarget)}><Icon name="edit" /></button>
      <button type="button" className="icon-button" title="计费设置" aria-label={`计费 ${label}`} disabled={editing} onClick={(event) => onEdit("billing", event.currentTarget)}><Icon name="calendar" /></button>
      <button type="button" className="link" aria-label={`换 token ${label}`} disabled={rotating || editing} onClick={onRotate}>换 token</button>
      {!editing && <ConfirmDelete label={`删除 ${label}`} confirm={`确认删除 ${label}`} pending={deleting} onDelete={onDelete} />}
    </div></td>
  </tr>;
}

function TagFilter({ tags, error, selected, onChange }: {
  tags: readonly Tag[] | undefined; error: unknown; selected: readonly string[]; onChange: (next: string[]) => void;
}) {
  const listed = (tags ?? []).map((tag) => tag.name);
  // 已选却从清单消失的标签仍须可取消，否则用户会困在无法清空的过滤条件里。
  const names = [...listed, ...selected.filter((name) => !listed.some((tag) => sameTag(tag, name)))];
  const count = (name: string) => tags?.find((tag) => tag.name === name)?.nodeCount;
  return <fieldset className="picks tag-filter"><legend>按标签过滤（同时带有所选全部标签）</legend>
    {error != null && <span role="alert" className="error">无法取得标签清单：{errorText(error)}</span>}
    {error == null && tags !== undefined && names.length === 0 && <span className="muted">还没有标签。</span>}
    {names.map((name) => {
      const checked = selected.some((tag) => sameTag(tag, name));
      const n = count(name);
      return <label key={name}><input type="checkbox" aria-label={`按标签过滤 ${name}`} checked={checked} onChange={() => onChange(checked ? withoutTag(selected, name) : withTag(selected, name))} />{name}{n !== undefined && <span className="muted">（{n}）</span>}</label>;
    })}
    {selected.length > 0 && <button type="button" className="link" onClick={() => onChange([])}>清除标签过滤</button>}
  </fieldset>;
}

function TagManager({ tags, pending, onDelete }: { tags: readonly Tag[] | undefined; pending: boolean; onDelete: (name: string) => void }) {
  if (tags === undefined) return null;
  return <details className="tag-management" open><summary>标签管理 <span className="muted">{tags.length}</span></summary><section aria-label="标签">
    {tags.length === 0 ? <p className="node-subtext">还没有标签；在节点的编辑里添加。</p> : <ul className="tag-list">{tags.map((tag) => <li key={tag.name}><span className="tag">{tag.name}</span> <span className="muted">{tag.nodeCount} 个节点</span>{" "}<ConfirmDelete label={`删除标签 ${tag.name}`} confirm={`确认删除标签 ${tag.name}`} note="只从节点上解除，节点不受影响" pending={pending} onDelete={() => onDelete(tag.name)} /></li>)}</ul>}
  </section></details>;
}
