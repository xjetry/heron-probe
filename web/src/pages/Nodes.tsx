import { createConnectQueryKey, createQueryOptions, useMutation, useQuery, useTransport } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type DragEvent, type FormEvent, type ReactNode, useRef, useState } from "react";
import { Link } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
import { errorText } from "../api/auth";
import { useLatestError } from "../api/useLatestError";
import { useRetained } from "../api/useRetained";
import { type OrderMove, useOrder } from "../api/useOrder";
import { ConfirmDelete } from "../components/ConfirmDelete";
import { Icon } from "../components/Icon";
import { Modal } from "../components/Modal";
import { MixedCheckbox } from "../components/MixedCheckbox";
import { NodeAddresses } from "../components/NodeAddresses";
import { NodeCountry } from "../components/NodeCountry";
import { NodeInstallModal } from "../components/NodeInstallModal";
import { NodeMoveModal } from "../components/NodeMoveModal";
import { NodeOrderControl } from "../components/NodeOrderControl";
import { AdminService, type Node, type NodeStatus, type Tag } from "../gen/heron/v1/admin_pb";
import { BillingEditor, billingDraftSet, emptyBillingDraft } from "../components/BillingEditor";
import { expired, expiryText, priceText } from "../lib/billing";
import { bytes } from "../lib/format";
import { withId } from "../lib/ids";
import { filterNodes } from "../lib/nodeSearch";
import { POLL_MS } from "../lib/poll";
import { sameTag, withoutTag, withTag } from "../lib/tags";
import { lagsHub } from "../lib/version";
import { NodeEditor } from "./NodeEditor";
import { BatchNodeTagsEditor } from "./BatchNodeTagsEditor";

// 标签过滤二选一：按一组标签取交集，或只要无标签节点。空 names 表示不过滤。
// 用判别式联合让"既选了标签又选了无标签"在类型上不可表示——hub 对两个条件同时给出返回 InvalidArgument
// （ListNodesRequest.untagged 注释），页面不该存在能构造出该请求的路径。
type TagFilterState = { kind: "tags"; names: string[] } | { kind: "untagged" };

export function Nodes() {
  const qc = useQueryClient();
  const transport = useTransport();
  const { error, mutationOptions } = useLatestError();
  const [tagFilter, setTagFilter] = useState<TagFilterState>({ kind: "tags", names: [] });
  // 请求由状态推出，两条分支在类型上已经互斥（见 TagFilterState）：无标签只带 untagged，标签只带 tags。
  // 过滤切换失败时保留旧列表；弹窗草稿独立于列表，刷新与排序不会卸载正在编辑的节点。
  const nodes = useRetained(useQuery(
    AdminService.method.listNodes,
    tagFilter.kind === "untagged" ? { tags: [], untagged: true } : { tags: tagFilter.names },
    { refetchInterval: 10_000 },
  ));
  // 全部节点总数 N（未过滤）：移动目标位置的合法区间 1..N-k+1 按 N 计算。与 useOrder 的 reload / 未过滤的
  // nodes 查询同一个键 listNodes({ tags: [] })——未过滤时复用同一条查询，过滤时是额外一条轮询。
  const allNodes = useQuery(AdminService.method.listNodes, { tags: [] }, { refetchInterval: 10_000 });
  const tags = useQuery(AdminService.method.listTags, {});
  const snapshot = useQuery(AdminService.method.getSnapshot, {}, { refetchInterval: POLL_MS });
  const hubVersion = snapshot.data?.hubVersion;
  const statusById = new Map(snapshot.data?.nodes.map((node) => [node.id, node]));
  const refresh = (options?: { throwOnError: boolean }) => Promise.all([
    qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listNodes, cardinality: "finite" }) }, options),
    qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listTags, cardinality: "finite" }) }, options),
  ]);
  // 创建与轮换的响应是唯一明文来源，不能丢弃迟到响应；删除同一节点时同步清掉它的凭据弹窗。
  // opener 记下触发元素，弹窗关闭后焦点回到它；节点多了也不会把凭据顶出视口。
  const [secret, setSecret] = useState<{ id: bigint; label: string; value: string; reRegister: boolean; opener: HTMLElement } | null>(null);
  const lastOpener = useRef<HTMLElement | null>(null);
  const [creating, setCreating] = useState<HTMLElement | null>(null);
  const [name, setName] = useState("");
  const [billing, setBilling] = useState(emptyBillingDraft);
  const [search, setSearch] = useState("");
  const [drag, setDrag] = useState<{ id: bigint; members: string } | null>(null);
  const [drop, setDrop] = useState<{ target: bigint; edge: "before" | "after" } | null>(null);
  const [editor, setEditor] = useState<{ node: Node; mode: "general" | "billing"; opener: HTMLElement } | null>(null);
  const [selected, setSelected] = useState<bigint[]>([]);
  const [batchEditor, setBatchEditor] = useState<{ nodes: Node[]; tags: Tag[]; opener: HTMLElement } | null>(null);
  // 「移动到…」的目标：批量多选或行菜单单个节点；opener 是触发元素，弹窗关闭后焦点回到它。
  const [moveTarget, setMoveTarget] = useState<{ nodes: readonly Node[]; opener: HTMLElement } | null>(null);
  const create = useMutation(AdminService.method.createNode, {
    ...mutationOptions,
    onSuccess: (result) => {
      const node = result.node;
      if (node) setSecret({ id: node.id, label: `节点 ${withId(node.name, node.id)} 的 token`, value: result.token, reRegister: false, opener: lastOpener.current ?? document.body });
      setName(""); setBilling(emptyBillingDraft()); setCreating(null);
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
  const batchUpdate = useMutation(AdminService.method.batchUpdateNodeTags, {
    onSuccess: async () => {
      // 服务端已应用增删意图，旧快照不能继续作为草稿基线；回读失败在列表提示，不重开这份草稿。
      setBatchEditor(null);
      setSelected([]);
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
      setSecret({ id, label: `节点 ${withId(name, id)} 的新 token`, value: result.token, reRegister: true, opener: lastOpener.current ?? document.body });
      return refresh();
    },
  });
  const reorder = useMutation(AdminService.method.reorderNodes);
  const moveNodes = useMutation(AdminService.method.moveNodes, {
    onSuccess: () => {
      setMoveTarget(null);
      setSelected([]);
      // 当前过滤结果与完整总数是两条 listNodes 查询，按方法的键一起失效；标签不受影响。
      return qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listNodes, cardinality: "finite" }) });
    },
  });
  const filtered = search !== "" || tagFilter.kind === "untagged" || tagFilter.names.length > 0;
  // narrowed（过滤或沿用旧结果）表示显示的不是当前条件下的完整列表：行首序号改用服务端全序名次，
  // 拖动排序也只在未收窄时开放（它们保存完整排列）。
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
    // 删掉的标签必须从过滤里去掉，否则条件引用一个已不存在的标签，列表会永远为空
    // （ListNodesRequest.tags 注释：有不存在的标签时结果为空）。无标签过滤下没有标签可选，状态不变。
    onSuccess: (_result, request) => {
      setTagFilter((current) => current.kind === "tags" ? { kind: "tags", names: withoutTag(current.names, request.name ?? "") } : current);
      return refresh();
    },
  });
  const onCreate = (event: FormEvent) => { event.preventDefault(); if (name.trim() && !create.isPending) create.mutate(billingDraftSet(billing) ? { name, billing } : { name }); };
  const gate = queryGate(nodes);
  const list = filterNodes(order.items, search);
  const selectedIds = new Set(selected);
  const selectedNodes = list.filter((node) => selectedIds.has(node.id));
  // 创建和编辑由弹窗占用交互；换发响应前尚无弹窗，也要锁住同一批入口，避免并发响应覆盖唯一明文与返回焦点。
  const editing = editor !== null || batchEditor !== null || batchUpdate.isPending || creating !== null || rotate.isPending;
  // MoveNodes 在途时列表即将被重写，拖动 / 方向键的完整排列保存一并停下。
  const sortable = !narrowed && !order.blocked && !editing && list.length > 1 && !moveNodes.isPending;
  // 「移动到…」按全序名次走 MoveNodes，过滤时也可用；编辑弹窗、MoveNodes 在途、排序会话未确认或被阻塞时
  // 关闭。N 来自未过滤的完整列表：未就绪或读取失败时入口关闭并说明原因，不给一个算不出区间的输入框。
  const moveTotal = allNodes.data?.nodes.length;
  const moveReady = moveTotal !== undefined && allNodes.error == null;
  const moveLocked = editing || moveNodes.isPending || order.pending || order.blocked || !moveReady;
  const members = list.map((node) => String(node.id)).sort().join(",");
  const dragging = sortable && drag?.members === members ? drag.id : null;
  const moveNode = (id: bigint, move: OrderMove) => {
    if (!sortable) return;
    order.move(id, move);
  };
  const endDrag = () => { setDrag(null); setDrop(null); };
  const dropPosition = (event: DragEvent<HTMLTableRowElement>, target: bigint): { target: bigint; edge: "before" | "after" } => {
    const bounds = event.currentTarget.getBoundingClientRect();
    return { target, edge: event.clientY < bounds.top + bounds.height / 2 ? "before" : "after" };
  };
  const openEditor = (node: Node, mode: "general" | "billing", opener: HTMLElement) => {
    if (editing) return;
    update.reset();
    setEditor({ node, mode, opener });
  };

  return <section>
    <header className="page-heading">
      <div><div className="eyebrow">Infrastructure</div><h1>节点</h1><p>管理节点资产、网络连接与到期信息。</p></div>
      <button type="button" className="primary-button" disabled={editing} onClick={(event) => { lastOpener.current = event.currentTarget; create.reset(); setCreating(event.currentTarget); }}><Icon name="plus" />添加节点</button>
    </header>
    {!editor && errorBanner(nodes.error)}
    {snapshot.error != null && <p role="alert" className="error">{hubVersion === undefined ? "无法取得 hub 版本，落后标记不可用" : `刷新 hub 版本失败，落后标记按上次取得的 ${hubVersion || "空版本"} 判断`}；在线状态与流量可能不是最新值：{errorText(snapshot.error)}</p>}
    {secret && <NodeInstallModal secretLabel={secret.label} token={secret.value} hubVersion={hubVersion} error={snapshot.error} reRegister={secret.reRegister} opener={secret.opener} onClose={() => setSecret(null)} />}
    <div className="node-filters">
      <label className="node-search">搜索节点<input type="search" placeholder="名称、IP、地区、备注或主机名" value={search} onChange={(event) => { setSearch(event.target.value); setSelected([]); }} /></label>
      <TagFilter tags={tags.data?.tags} error={tags.error} filter={tagFilter} onChange={(value) => { setTagFilter(value); setSelected([]); }} />
    </div>
    {!editing && errorBanner(error)}
    {!batchEditor && errorBanner(batchUpdate.error)}
    {!batchEditor && batchUpdate.isPending && <p role="status" className="muted">标签已保存，正在重新读取…</p>}
    {order.error != null && <p role="alert" className="error">排序未完成：{errorText(order.error)}</p>}
    {order.pending && <p role="status" className="muted">正在保存并确认排序…</p>}
    {order.confirmed && <p className="order-saved" aria-live="polite">顺序已保存</p>}
    {order.blocked && <button type="button" onClick={order.recover} disabled={order.pending}>重新读取排序</button>}
    {filtered && <p className="node-subtext">搜索或按标签过滤时不能用拖动或上下移（它们保存完整排列）；可用「移动到…」按全序名次移动，或清空过滤后再调整。</p>}
    {!filtered && nodes.stale && <p className="node-subtext">列表还不是当前条件下的结果，暂时无法排序。</p>}
    {gate.ready ? <>
      <div className="section-heading"><h2>节点清单 <span className="muted">{list.length}</span></h2><span className="live-caption">双栈出口由 agent 独立探测</span></div>
      <div className="node-batch-toolbar">
        <label><MixedCheckbox label="选择当前结果全部节点" checked={selectedNodes.length === 0 ? false : selectedNodes.length === list.length ? true : "mixed"} disabled={editing || nodes.stale || list.length === 0} onChange={() => setSelected(selectedNodes.length === list.length ? [] : list.map((node) => node.id))} />选择当前结果</label>
        <span className="muted">已选择 {selectedNodes.length} 个节点</span>
        <button type="button" disabled={editing || nodes.stale || nodes.error != null || remove.isPending || selectedNodes.length === 0 || tags.data === undefined || tags.error != null} onClick={(event) => { batchUpdate.reset(); setBatchEditor({ nodes: selectedNodes, tags: tags.data?.tags ?? [], opener: event.currentTarget }); }}>批量编辑标签</button>
        <button type="button" disabled={moveLocked || selectedNodes.length === 0} onClick={(event) => { moveNodes.reset(); setMoveTarget({ nodes: selectedNodes, opener: event.currentTarget }); }}>移动到…</button>
        {!moveReady && allNodes.error == null && <span className="muted">正在读取节点总数…</span>}
        {allNodes.error != null && <span className="error">无法取得节点总数，「移动到…」不可用：{errorText(allNodes.error)}</span>}
        {selectedNodes.length > 0 && <button type="button" className="link" disabled={editing} onClick={() => setSelected([])}>清除选择</button>}
      </div>
      <p className="node-subtext order-help" id="node-order-help">拖动手柄调整顺序，松开后自动保存。也可使用移动菜单，或聚焦手柄后按方向键、Home / End。</p>
      {list.length === 0 && <p className="node-empty" role="status">{narrowed ? "没有匹配的节点。" : "还没有节点，添加节点后安装 agent 即可开始监控。"}</p>}
      <div className="table-scroll" role="region" aria-label="节点管理" tabIndex={0}>
        <table className="nodes node-management"><thead><tr><th data-column="order"><span className="sr-only">排序</span></th><th data-column="name">节点</th><th data-column="addresses">IP 地址</th><th data-column="status">状态</th><th>本周期流量</th><th>计费</th><th>到期</th><th data-column="actions">操作</th></tr></thead>
          <tbody>{list.map((node, index) => <NodeRow key={String(node.id)} node={node} status={statusById.get(node.id)} hubVersion={hubVersion}
            editing={editing} deleting={remove.isPending} rotating={rotate.isPending}
            selection={<MixedCheckbox label={`选择 ${withId(node.name, node.id)}`} checked={selectedIds.has(node.id)} disabled={editing || nodes.stale} onChange={() => setSelected((current) => current.includes(node.id) ? current.filter((id) => id !== node.id) : [...current, node.id])} />}
            orderClass={dragging === node.id ? "is-dragging" : dragging !== null && drop?.target === node.id ? `drop-${drop.edge}` : undefined}
            onDragOver={(event) => {
              if (dragging === null || dragging === node.id) return;
              event.preventDefault(); event.dataTransfer.dropEffect = "move";
              setDrop(dropPosition(event, node.id));
            }}
            onDrop={(event) => {
              if (dragging !== null && dragging !== node.id) {
                event.preventDefault(); moveNode(dragging, dropPosition(event, node.id));
              }
              endDrag();
            }}
            orderControl={<NodeOrderControl label={withId(node.name, node.id)} index={index} count={list.length}
              position={narrowed ? node.position : index + 1} reorderDisabled={!sortable} moveDisabled={moveLocked}
              onMove={(move) => moveNode(node.id, move)} onDragEnd={endDrag}
              onMoveTo={(opener) => { moveNodes.reset(); setMoveTarget({ nodes: [node], opener }); }}
              onDragStart={(event) => {
                if (!sortable) { event.preventDefault(); return; }
                event.dataTransfer.effectAllowed = "move";
                event.dataTransfer.setData("text/plain", String(node.id));
                setDrag({ id: node.id, members }); setDrop(null);
              }} />}
            onEdit={(mode, opener) => openEditor(node, mode, opener)} onDelete={() => remove.mutate({ id: node.id })} onRotate={(opener) => { lastOpener.current = opener; rotate.mutate({ id: node.id }); }} />)}</tbody>
        </table>
      </div>
    </> : gate.loading}
    <TagManager tags={tags.data?.tags} pending={removeTag.isPending} onDelete={(name) => removeTag.mutate({ name })} />
    {batchEditor && <BatchNodeTagsEditor nodes={batchEditor.nodes} knownTags={batchEditor.tags} opener={batchEditor.opener} saving={batchUpdate.isPending} error={batchUpdate.error} onClose={() => setBatchEditor(null)}
      onSave={(changes) => batchUpdate.mutate(changes)} />}
    {moveTarget && <NodeMoveModal nodes={moveTarget.nodes} total={moveTotal ?? 0} pending={moveNodes.isPending} error={moveNodes.error} opener={moveTarget.opener}
      onClose={() => { setMoveTarget(null); moveNodes.reset(); }}
      onConfirm={(position) => moveNodes.mutate({ ids: moveTarget.nodes.map((node) => node.id), position })} />}
    {editor && <NodeEditor key={String(editor.node.id)} node={editor.node} mode={editor.mode} opener={editor.opener} knownTags={tags.data?.tags ?? []}
      saving={update.isPending} error={update.error} listError={nodes.error} onClose={() => setEditor(null)}
      onSave={(patch) => update.mutate({ id: editor.node.id, ...patch }, { onSuccess: () => setEditor(null) })} />}
    {creating && <Modal title="添加节点" description="创建后将显示安装凭据 token，仅用于注册 agent，不能上报指标。计费可留空，稍后在节点的计费设置中补。" busy={create.isPending} opener={creating} onClose={() => setCreating(null)}>
      <form onSubmit={onCreate}><div className="modal-body">{errorBanner(create.error)}<label>新节点名称<input data-autofocus value={name} onChange={(event) => setName(event.target.value)} placeholder="例如 tokyo-01" disabled={create.isPending} /></label>
        <section className="form-section" aria-label="计费（选填）"><BillingEditor label="新节点" draft={billing} onChange={(patch) => setBilling({ ...billing, ...patch })} /></section></div>
        <footer className="modal-footer"><button type="button" disabled={create.isPending} onClick={() => setCreating(null)}>取消</button><button type="submit" className="primary-button" disabled={create.isPending || name.trim() === ""}>创建</button></footer>
      </form>
    </Modal>}
  </section>;
}

function NodeRow({ node, status, hubVersion, editing, deleting, rotating, selection, orderControl, orderClass, onDragOver, onDrop, onEdit, onDelete, onRotate }: {
  node: Node; status?: NodeStatus; hubVersion?: string; editing: boolean; deleting: boolean; rotating: boolean;
  selection: ReactNode;
  orderControl: ReactNode; orderClass?: string; onDragOver: (event: DragEvent<HTMLTableRowElement>) => void; onDrop: (event: DragEvent<HTMLTableRowElement>) => void;
  onEdit: (mode: "general" | "billing", opener: HTMLElement) => void; onDelete: () => void; onRotate: (opener: HTMLElement) => void;
}) {
  const label = withId(node.name, node.id);
  return <tr className={orderClass} onDragOver={onDragOver} onDrop={onDrop}>
    <td data-column="order" data-label="排序">{orderControl}</td>
    <td data-column="name" data-label="节点"><div className="node-name-line">{selection}<Link to={`/nodes/${node.id}`} aria-label={label}>{node.name}</Link><NodeCountry node={node} /></div>
      <div aria-label={`标签 ${label}`}>{node.tags.map((tag) => <span key={tag} className="tag">{tag}</span>)}</div>
      {node.note && <p className="node-subtext node-note" title={node.note}>{node.note}</p>}
      <span className="node-subtext">{node.public ? "公开" : "仅管理端"}</span>
      {node.maintenance && <span className="node-subtext warn">维护中</span>}
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
      <button type="button" className="link" aria-label={`换 token ${label}`} disabled={rotating || editing} onClick={(event) => onRotate(event.currentTarget)}>换 token</button>
      {!editing && <ConfirmDelete label={`删除 ${label}`} confirm={`确认删除 ${label}`} pending={deleting} onDelete={onDelete} />}
    </div></td>
  </tr>;
}

function TagFilter({ tags, error, filter, onChange }: {
  tags: readonly Tag[] | undefined; error: unknown; filter: TagFilterState; onChange: (next: TagFilterState) => void;
}) {
  const listed = (tags ?? []).map((tag) => tag.name);
  const selected = filter.kind === "tags" ? filter.names : [];
  // 已选却从清单消失的标签仍须可取消，否则用户会困在无法清空的过滤条件里。
  const names = [...listed, ...selected.filter((name) => !listed.some((tag) => sameTag(tag, name)))];
  const count = (name: string) => tags?.find((tag) => tag.name === name)?.nodeCount;
  const untagged = filter.kind === "untagged";
  return <fieldset className="picks tag-filter"><legend>按标签过滤（多选为同时满足）</legend>
    {error != null && <span role="alert" className="error">无法取得标签清单：{errorText(error)}</span>}
    {/* "无标签"不依赖 ListTags：清单加载中、为空或失败都照常可勾。它的可访问名称与标签项分开命名——
        用户可能真的建一个叫"无标签"的标签，两者撞名就选不中标签了。 */}
    <label><input type="checkbox" aria-label="只看没有标签的节点" checked={untagged} onChange={() => onChange(untagged ? { kind: "tags", names: [] } : { kind: "untagged" })} />无标签</label>
    {error == null && tags !== undefined && names.length === 0 && <span className="muted">还没有标签。</span>}
    {names.map((name) => {
      const checked = selected.some((tag) => sameTag(tag, name));
      const n = count(name);
      // 无标签状态下 selected 为空：勾任一标签经 withTag 自然替换为只选这一个（互斥由状态形状保证，
      // untagged 分支不携带标签名）；标签状态下则在已选里增删。
      return <label key={name}><input type="checkbox" aria-label={`按标签过滤 ${name}`} checked={checked} onChange={() => onChange({ kind: "tags", names: checked ? withoutTag(selected, name) : withTag(selected, name) })} />{name}{n !== undefined && <span className="muted">（{n}）</span>}</label>;
    })}
    {(untagged || selected.length > 0) && <button type="button" className="link" onClick={() => onChange({ kind: "tags", names: [] })}>清除标签过滤</button>}
  </fieldset>;
}

function TagManager({ tags, pending, onDelete }: { tags: readonly Tag[] | undefined; pending: boolean; onDelete: (name: string) => void }) {
  if (tags === undefined) return null;
  return <details className="tag-management" open><summary>标签管理 <span className="muted">{tags.length}</span></summary><section aria-label="标签">
    {tags.length === 0 ? <p className="node-subtext">还没有标签；在节点的编辑里添加。</p> : <ul className="tag-list">{tags.map((tag) => <li key={tag.name}><span className="tag">{tag.name}</span> <span className="muted">{tag.nodeCount} 个节点</span>{" "}<ConfirmDelete label={`删除标签 ${tag.name}`} confirm={`确认删除标签 ${tag.name}`} note="只从节点上解除，节点不受影响" pending={pending} onDelete={() => onDelete(tag.name)} /></li>)}</ul>}
  </section></details>;
}
