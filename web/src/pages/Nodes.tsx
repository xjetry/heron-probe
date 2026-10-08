import { createConnectQueryKey, createQueryOptions, useMutation, useQuery, useTransport } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type DragEvent, type ReactNode, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
import { errorText } from "../api/auth";
import { useLatestError } from "../api/useLatestError";
import { useRetained } from "../api/useRetained";
import { type OrderMove, useOrder } from "../api/useOrder";
import { ConfirmDelete } from "../components/ConfirmDelete";
import { Expiry } from "../components/Expiry";
import { MixedCheckbox } from "../components/MixedCheckbox";
import { NodeAddresses } from "../components/NodeAddresses";
import { NodeCountry } from "../components/NodeCountry";
import { NodeCreateDrawer } from "../components/NodeCreateDrawer";
import { NodeCredentialsDrawer } from "../components/NodeCredentialsDrawer";
import { NodeMoveModal } from "../components/NodeMoveModal";
import { NodeOrderControl } from "../components/NodeOrderControl";
import { AdminService, type Node, type NodeStatus, type Tag } from "../gen/heron/v1/admin_pb";
import { priceText } from "../lib/billing";
import { withId } from "../lib/ids";
import { POLL_MS } from "../lib/poll";
import { sameTag, withoutTag } from "../lib/tags";
import { olderThan } from "../lib/version";
import { Missing } from "../components/Bar";
import { MultiSelect } from "../components/MultiSelect";
import { PageHeader } from "../components/PageHeader";
import { RowMenu } from "../components/RowMenu";
import { StatusBadge } from "../components/StatusBadge";
import { liveById, liveStatus } from "../lib/adminStatus";
import { applyScope, isScoped, paramsWithScope, scopeFromParams, STATUS_OPTIONS, type ScopeFilters } from "../lib/nodeFilters";
import { NodeEditor } from "./NodeEditor";
import { BatchNodeTagsEditor } from "./BatchNodeTagsEditor";
import { trafficParts } from "../lib/traffic";

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
  // 落后标记只看 hub 绑定的 agent 版本（spec §14.1）：hub 自己升到 v0.5.6 不代表节点落后。
  const boundAgentVersion = snapshot.data?.boundAgentVersion;
  const live = liveById(snapshot.data?.nodes);
  const refresh = (options?: { throwOnError: boolean }) => Promise.all([
    qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listNodes, cardinality: "finite" }) }, options),
    qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listTags, cardinality: "finite" }) }, options),
  ]);
  // 创建与轮换的响应是唯一明文来源，不能丢弃迟到响应；删除同一节点时同步清掉它的凭据弹窗。
  // opener 记下触发元素，弹窗关闭后焦点回到它；节点多了也不会把凭据顶出视口。
  const [secret, setSecret] = useState<{ id: bigint; title: string; label: string; value: string; reRegister: boolean; opener: HTMLElement } | null>(null);
  const lastOpener = useRef<HTMLElement | null>(null);
  const [creating, setCreating] = useState<HTMLElement | null>(null);
  const [search, setSearch] = useState("");
  const [drag, setDrag] = useState<{ id: bigint; members: string } | null>(null);
  const [drop, setDrop] = useState<{ target: bigint; edge: "before" | "after" } | null>(null);
  const [editor, setEditor] = useState<{ node: Node; opener: HTMLElement } | null>(null);
  const [selected, setSelected] = useState<bigint[]>([]);
  const [batchEditor, setBatchEditor] = useState<{ nodes: Node[]; tags: Tag[]; opener: HTMLElement } | null>(null);
  // 「移动到…」的目标：批量多选或行菜单单个节点；opener 是触发元素，弹窗关闭后焦点回到它。
  const [moveTarget, setMoveTarget] = useState<{ nodes: readonly Node[]; opener: HTMLElement } | null>(null);
  const create = useMutation(AdminService.method.createNode, {
    ...mutationOptions,
    onSuccess: (result) => {
      const node = result.node;
      setCreating(null);
      if (node) setSecret({ title: "节点已创建", id: node.id, label: `节点 ${withId(node.name, node.id)} 的 token`, value: result.token, reRegister: false, opener: lastOpener.current ?? document.body });
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
      setSecret({ title: "节点凭据", id, label: `节点 ${withId(name, id)} 的新 token`, value: result.token, reRegister: true, opener: lastOpener.current ?? document.body });
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
  const [params, setParams] = useSearchParams();
  const scope = scopeFromParams(params);
  // 筛选由 URL 持有；切换时清空选择，避免批量操作修改已经不可见的节点。
  const setScope = (next: ScopeFilters) => { setParams(paramsWithScope(params, next), { replace: true }); setSelected([]); };
  const clearFilters = () => { setSearch(""); setTagFilter({ kind: "tags", names: [] }); setScope({ status: null, expiring: false, lagging: false }); };
  const filtered = search !== "" || tagFilter.kind === "untagged" || tagFilter.names.length > 0 || isScoped(scope);
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
  const gate = queryGate(nodes);
  const list = applyScope(order.items, live, boundAgentVersion, scope, search);
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
  const openEditor = (node: Node, opener: HTMLElement) => {
    if (editing) return;
    update.reset();
    setEditor({ node, opener });
  };

  const tagOptions = [...(tags.data?.tags ?? []).map((tag) => ({ value: tag.name, label: tag.name, count: tag.nodeCount })),
    // 已选却从清单消失的标签仍须可取消，否则用户会困在无法清空的过滤条件里。
    ...(tagFilter.kind === "tags" ? tagFilter.names : []).filter((name) => !(tags.data?.tags ?? []).some((tag) => sameTag(tag.name, name))).map((name) => ({ value: name, label: name }))];
  const untagged = tagFilter.kind === "untagged";
  return <section>
    <PageHeader title="节点" actions={<button type="button" className="primary-button" disabled={editing} onClick={(event) => { lastOpener.current = event.currentTarget; create.reset(); setCreating(event.currentTarget); }}>添加节点</button>} />
    {!editor && errorBanner(nodes.error)}
    {snapshot.error != null && <p role="alert" className="error">{boundAgentVersion === undefined ? "无法取得 hub 绑定的 agent 版本，落后标记不可用" : `刷新失败，落后标记按上次取得的绑定版本 ${boundAgentVersion || "空"} 判断`}；在线状态与流量可能不是最新值：{errorText(snapshot.error)}</p>}
    {secret && <NodeCredentialsDrawer title={secret.title} secretLabel={secret.label} token={secret.value} hubVersion={hubVersion} boundAgentVersion={boundAgentVersion} error={snapshot.error} reRegister={secret.reRegister} opener={secret.opener} onClose={() => setSecret(null)} />}
    <div className="filter-row" role="group" aria-label="筛选">
      <input type="search" aria-label="搜索节点" placeholder="名称、IP、地区、备注或主机名" value={search} onChange={(event) => { setSearch(event.target.value); setSelected([]); }} />
      <MultiSelect label="标签" searchable options={tagOptions} selected={tagFilter.kind === "tags" ? tagFilter.names : []} onChange={(names) => { setTagFilter({ kind: "tags", names }); setSelected([]); }} />
      {/* "无标签"不依赖 ListTags：清单加载中、为空或失败都照常可勾。它的可访问名称与标签项分开命名——用户可能真的建一个叫"无标签"的标签。 */}
      <label className="check"><input type="checkbox" aria-label="只看没有标签的节点" checked={untagged} onChange={() => { setTagFilter(untagged ? { kind: "tags", names: [] } : { kind: "untagged" }); setSelected([]); }} />无标签</label>
      <select aria-label="状态" value={scope.status ?? ""} onChange={(event) => setScope({ ...scope, status: (event.target.value || null) as ScopeFilters["status"] })}>
        <option value="">全部状态</option>{STATUS_OPTIONS.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
      </select>
      <label className="check"><input type="checkbox" aria-label="只看 30 天内到期" checked={scope.expiring} onChange={() => setScope({ ...scope, expiring: !scope.expiring })} />30 天内到期</label>
      <label className="check"><input type="checkbox" aria-label="只看 agent 版本落后" checked={scope.lagging} onChange={() => setScope({ ...scope, lagging: !scope.lagging })} />agent 版本落后</label>
      {filtered && <button type="button" className="link" onClick={clearFilters}>清除筛选</button>}
      {tags.error != null && <span role="alert" className="error">无法取得标签清单：{errorText(tags.error)}</span>}
      <span className="muted num">{list.length} / {order.items.length}</span>
    </div>
    {!editing && errorBanner(error)}
    {!batchEditor && errorBanner(batchUpdate.error)}
    {!batchEditor && batchUpdate.isPending && <p role="status" className="muted">标签已保存，正在重新读取…</p>}
    {order.error != null && <p role="alert" className="error">排序未完成：{errorText(order.error)}</p>}
    {order.pending && <p role="status" className="muted">正在保存并确认排序…</p>}
    {order.confirmed && <p className="order-saved" aria-live="polite">顺序已保存</p>}
    {order.blocked && <button type="button" onClick={order.recover} disabled={order.pending}>重新读取排序</button>}
    {filtered && <p className="node-subtext">筛选时不能用拖动或上下移（它们保存完整排列）；可用行菜单的「移动到…」按全序名次移动，或清除筛选后再调整。</p>}
    {!filtered && nodes.stale && <p className="node-subtext">列表还不是当前条件下的结果，暂时无法排序。</p>}
    {gate.ready ? <>
      {selectedNodes.length > 0 && <div className="batch-toolbar" role="toolbar" aria-label="批量操作">
        <span>已选择 {selectedNodes.length} 个节点</span>
        <button type="button" disabled={editing || nodes.stale || nodes.error != null || remove.isPending || tags.data === undefined || tags.error != null} onClick={(event) => { batchUpdate.reset(); setBatchEditor({ nodes: selectedNodes, tags: tags.data?.tags ?? [], opener: event.currentTarget }); }}>批量编辑标签</button>
        <button type="button" disabled={moveLocked} onClick={(event) => { moveNodes.reset(); setMoveTarget({ nodes: selectedNodes, opener: event.currentTarget }); }}>移动到…</button>
        {!moveReady && allNodes.error == null && <span className="muted">正在读取节点总数…</span>}
        {allNodes.error != null && <span className="error">无法取得节点总数，「移动到…」不可用：{errorText(allNodes.error)}</span>}
        <button type="button" className="link" disabled={editing} onClick={() => setSelected([])}>清除选择</button>
      </div>}
      <p className="node-subtext order-help" id="node-order-help">拖动手柄调整顺序，松开后自动保存。也可使用移动菜单，或聚焦手柄后按方向键、Home / End。</p>
      <div className="table-scroll" role="region" aria-label="节点管理" tabIndex={0}>
        <table className="nodes node-management"><thead><tr>
          <th data-column="select"><MixedCheckbox label="选择当前结果全部节点" checked={selectedNodes.length === 0 ? false : selectedNodes.length === list.length ? true : "mixed"} disabled={editing || nodes.stale || list.length === 0} onChange={() => setSelected(selectedNodes.length === list.length ? [] : list.map((node) => node.id))} /></th>
          <th data-column="order"><span className="sr-only">排序</span></th><th data-column="name">节点</th><th data-column="addresses">IPv4 / IPv6</th><th data-column="status">状态</th><th data-column="traffic">本周期</th><th data-column="billing">费用</th><th data-column="expiry">到期</th><th data-column="actions"><span className="sr-only">操作</span></th>
        </tr></thead>
          <tbody>{list.map((node, index) => {
            const label = withId(node.name, node.id);
            return <NodeRow key={String(node.id)} node={node} live={live.get(node.id)} boundAgentVersion={boundAgentVersion}
              selection={<MixedCheckbox label={`选择 ${label}`} checked={selectedIds.has(node.id)} disabled={editing || nodes.stale} onChange={() => setSelected((current) => current.includes(node.id) ? current.filter((id) => id !== node.id) : [...current, node.id])} />}
              orderClass={dragging === node.id ? "is-dragging" : dragging !== null && drop?.target === node.id ? `drop-${drop.edge}` : undefined}
              onDragOver={(event) => { if (dragging === null || dragging === node.id) return; event.preventDefault(); event.dataTransfer.dropEffect = "move"; setDrop(dropPosition(event, node.id)); }}
              onDrop={(event) => { if (dragging !== null && dragging !== node.id) { event.preventDefault(); moveNode(dragging, dropPosition(event, node.id)); } endDrag(); }}
              orderControl={<NodeOrderControl label={label} count={list.length} position={narrowed ? node.position : index + 1} reorderDisabled={!sortable}
                onMove={(move) => moveNode(node.id, move)} onDragEnd={endDrag}
                onDragStart={(event) => { if (!sortable) { event.preventDefault(); return; } event.dataTransfer.effectAllowed = "move"; event.dataTransfer.setData("text/plain", String(node.id)); setDrag({ id: node.id, members }); setDrop(null); }} />}
              menu={<RowMenu label={label} items={[
                { label: "编辑", disabled: editing, onSelect: (trigger) => openEditor(node, trigger) },
                { label: "查看详情", to: `/nodes/${node.id}` },
                // 上下移与置顶置底和排序手柄同一入口（完整排列），可用条件也相同：筛选或排序会话阻塞时整组禁用。
                { label: "上移一位", disabled: !sortable || index === 0, onSelect: () => moveNode(node.id, -1) },
                { label: "下移一位", disabled: !sortable || index === list.length - 1, onSelect: () => moveNode(node.id, 1) },
                { label: "置顶", disabled: !sortable || index === 0, onSelect: () => moveNode(node.id, "first") },
                { label: "置底", disabled: !sortable || index === list.length - 1, onSelect: () => moveNode(node.id, "last") },
                { label: "移动到…", disabled: moveLocked, onSelect: (trigger) => { moveNodes.reset(); setMoveTarget({ nodes: [node], opener: trigger }); } },
                { label: "换 token", disabled: rotate.isPending || editing, onSelect: (trigger) => { lastOpener.current = trigger; rotate.mutate({ id: node.id }); } },
                { label: "删除", danger: true, confirm: `确认删除 ${label}`, disabled: remove.isPending || editing, onSelect: () => remove.mutate({ id: node.id }) },
              ]}/>} />;
          })}</tbody>
        </table>
      </div>
      {list.length === 0 && (scope.lagging && boundAgentVersion === undefined
        ? <p className="node-empty" role="status">无法取得 hub 绑定的 agent 版本，「agent 版本落后」筛选暂时没有结果。</p>
        : <p className="node-empty" role="status">{narrowed ? "没有匹配的节点。" : "还没有节点，添加节点后安装 agent 即可开始监控。"}</p>)}
    </> : gate.loading}
    <TagManager tags={tags.data?.tags} pending={removeTag.isPending} onDelete={(name) => removeTag.mutate({ name })} />
    {batchEditor && <BatchNodeTagsEditor nodes={batchEditor.nodes} knownTags={batchEditor.tags} opener={batchEditor.opener} saving={batchUpdate.isPending} error={batchUpdate.error} onClose={() => setBatchEditor(null)}
      onSave={(changes) => batchUpdate.mutate(changes)} />}
    {moveTarget && <NodeMoveModal nodes={moveTarget.nodes} total={moveTotal ?? 0} pending={moveNodes.isPending} error={moveNodes.error} opener={moveTarget.opener}
      onClose={() => { setMoveTarget(null); moveNodes.reset(); }}
      onConfirm={(position) => moveNodes.mutate({ ids: moveTarget.nodes.map((node) => node.id), position })} />}
    {editor && <NodeEditor key={String(editor.node.id)} node={editor.node} opener={editor.opener} knownTags={tags.data?.tags ?? []}
      saving={update.isPending} error={update.error} listError={nodes.error} onClose={() => setEditor(null)}
      onSave={(patch) => update.mutate({ id: editor.node.id, ...patch }, { onSuccess: () => setEditor(null) })} />}
    {creating && <NodeCreateDrawer opener={creating} pending={create.isPending} error={create.error} onClose={() => setCreating(null)} onCreate={(request) => create.mutate(request)} />}
  </section>;
}

function NodeRow({ node, live, boundAgentVersion, selection, orderControl, orderClass, onDragOver, onDrop, menu }: {
  node: Node; live: NodeStatus | undefined; boundAgentVersion?: string; selection: ReactNode; orderControl: ReactNode; menu: ReactNode;
  orderClass?: string; onDragOver: (event: DragEvent<HTMLTableRowElement>) => void; onDrop: (event: DragEvent<HTMLTableRowElement>) => void;
}) {
  const label = withId(node.name, node.id);
  const status = liveStatus(node, live);
  const lagging = boundAgentVersion !== undefined && olderThan(node.facts?.agentVersion, boundAgentVersion);
  return <tr className={orderClass} aria-label={node.name} data-status={status ?? "unknown"} onDragOver={onDragOver} onDrop={onDrop}>
    <td data-column="select">{selection}</td>
    <td data-column="order" data-label="排序">{orderControl}</td>
    <td data-column="name" data-label="节点">
      <div className="node-name-line"><Link to={`/nodes/${node.id}`} aria-label={label}>{node.name}</Link><NodeCountry node={node} />
        {node.public && <span className="chip chip-public">公开</span>}</div>
      {(lagging || node.tags.length > 0 || node.note) && <div className="node-secondary">
        {lagging && <span className="badge-attention" title={`低于 hub 绑定的 agent 版本 ${boundAgentVersion}`}>agent 低于 {boundAgentVersion}</span>}
        {node.tags.length > 0 && <ul className="tag-chips" aria-label={`标签 ${label}`} title={node.tags.join("、")}>{node.tags.map((tag) => <li key={tag} className="chip">{tag}</li>)}</ul>}
        {node.note && <p className="node-note muted" title={node.note}>{node.note}</p>}
      </div>}
    </td>
    <td data-column="addresses" data-label="IPv4 / IPv6"><NodeAddresses network={node.facts?.network} /></td>
    <td data-column="status" data-label="状态">{status ? <StatusBadge status={status} /> : <span className="muted">状态未知</span>}</td>
    <td data-column="traffic" data-label="本周期">{live?.traffic ? <TwoLine main={trafficParts(live.traffic).amount} note={trafficParts(live.traffic).percent} mono /> : <Missing />}</td>
    <td data-column="billing" data-label="费用"><TwoLine main={priceText(node.billing) || "—"} note={node.billing?.autoRenew ? "自动续期" : undefined} /></td>
    <td data-column="expiry" data-label="到期">{node.billing?.expiresOn ? <Expiry billing={node.billing} /> : "—"}</td>
    <td data-column="actions">{menu}</td>
  </tr>;
}

// 节点表的窄列固定写成两行：主读数不折行，附注（配额百分比、自动续期）另起一行，不在主读数中间随列宽断开。
// 两段之间的空格是给读屏与单元格文字用的分隔，视觉上由 .cell-note 换行。
function TwoLine({ main, note, mono = false }: { main: string; note?: string; mono?: boolean }) {
  return <><span className="num cell-main">{main}</span>{note && <>{" "}<span className={mono ? "num cell-note" : "cell-note"}>{note}</span></>}</>;
}

function TagManager({ tags, pending, onDelete }: { tags: readonly Tag[] | undefined; pending: boolean; onDelete: (name: string) => void }) {
  if (tags === undefined) return null;
  return <details className="tag-management" open><summary>标签管理 <span className="muted">{tags.length}</span></summary><section aria-label="标签">
    {tags.length === 0 ? <p className="node-subtext">还没有标签；在节点的编辑里添加。</p> : <ul className="tag-list">{tags.map((tag) => <li key={tag.name}><span className="tag">{tag.name}</span> <span className="muted">{tag.nodeCount} 个节点</span>{" "}<span className="tag-delete"><ConfirmDelete label={`删除标签 ${tag.name}`} confirm={`确认删除标签 ${tag.name}`} note="只从节点上解除，节点不受影响" pending={pending} onDelete={() => onDelete(tag.name)} /></span></li>)}</ul>}
  </section></details>;
}
