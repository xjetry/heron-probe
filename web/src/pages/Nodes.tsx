import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, Fragment, type ReactNode, useState } from "react";
import { Link } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
import { ConfirmDelete } from "../components/ConfirmDelete";
import { CountryBadge } from "../components/CountryBadge";
import { Secret } from "../components/Secret";
import { AdminService, CountrySource, type Node, type Tag } from "../gen/probe/v1/admin_pb";
import { BillingCycle } from "../gen/probe/v1/types_pb";
import { errorText } from "../api/auth";
import { useLatestError } from "../api/useLatestError";
import { useRetained } from "../api/useRetained";
import { graceText } from "../lib/alerts";
import { BILLING_CYCLES, expired, expiryText, priceText } from "../lib/billing";
import { withId } from "../lib/ids";
import { filterNodes } from "../lib/nodeSearch";
import { sameTag, withoutTag, withTag } from "../lib/tags";
import { lagsHub } from "../lib/version";

export function Nodes() {
  const qc = useQueryClient();
  const { error, mutationOptions } = useLatestError();
  // 所选标签交给 hub 过滤（交集，§10），搜索在返回的结果上再做一次，两者取交集。换选择即换查询键：新条件的请求挂起
  // 或失败时沿用上一份结果（useRetained），列表与其中未保存的草稿不卸载，失败只加横幅；沿用期间列表不是当前条件的
  // 结果，排序入口同样关闭（见 narrowed）。搜索框与过滤器是这次查询的输入，渲染在列表的门控之外：请求失败时它们
  // 必须还在，用户才能把条件改回去。
  const [tagFilter, setTagFilter] = useState<string[]>([]);
  const nodes = useRetained(useQuery(AdminService.method.listNodes, { tags: tagFilter }));
  // 标签清单只供过滤器与标签管理用，不进页面门控：取不到时节点列表照常显示，过滤器处说明原因。
  const tags = useQuery(AdminService.method.listTags, {});
  // 只用于落后标记的可选查询：不进页面门控，失败或未就绪时不标，也不卸载列表；失败时在列表上方说明标记不可用，
  // 否则用户会把"没有标记"读成"没有落后的节点"。hub 版本在进程生命周期内不变，不轮询。
  const snapshot = useQuery(AdminService.method.getSnapshot, {});
  const hubVersion = snapshot.data?.hubVersion;
  // 节点的增删改都可能改变各标签的节点数，节点列表（全部过滤条件下的缓存）与标签清单一起刷新。
  const refresh = () => Promise.all([
    qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listNodes, cardinality: "finite" }) }),
    qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listTags, cardinality: "finite" }) }),
  ]);
  // token 只在创建与换 token 的响应里各出现一次，hub 不存明文；展示不经过 isLatest 门控——门控丢弃迟到结果时会把这唯一一份明文一起丢掉。
  // id 记下明文属于哪一行：删除的若正是这一行，卡片必须一起消失，不能继续展示已删对象的凭据。
  const [secret, setSecret] = useState<{ id: bigint; label: string; value: string } | null>(null);
  const [name, setName] = useState("");
  const [search, setSearch] = useState("");

  const create = useMutation(AdminService.method.createNode, {
    ...mutationOptions,
    onSuccess: (r) => {
      const n = r.node;
      if (n) setSecret({ id: n.id, label: `节点 ${withId(n.name, n.id)} 的 token`, value: r.token });
      setName("");
      void refresh();
    },
  });
  // 各行共用一个 mutation observer，重叠的 mutate 只回调最后一次；因此任一行保存挂起时禁用全部行的保存，退出编辑的才是保存的那一行。
  // 返回刷新 promise，编辑态在列表显示已保存值之后才关闭。
  const update = useMutation(AdminService.method.updateNode, { ...mutationOptions, onSuccess: refresh });
  const remove = useMutation(AdminService.method.deleteNode, {
    ...mutationOptions,
    onSuccess: (_r, req) => {
      setSecret((cur) => (cur?.id === req.id ? null : cur));
      return refresh();
    },
  });
  const rotate = useMutation(AdminService.method.rotateNodeToken, {
    ...mutationOptions,
    onSuccess: (r, req) => {
      // 请求里的 id 由调用方保证；缺失时没有可归属的卡片。
      if (req.id == null) return refresh();
      const id = req.id;
      const name = nodes.data?.nodes.find((n) => n.id === id)?.name ?? String(id);
      setSecret({ id, label: `节点 ${withId(name, id)} 的新 token`, value: r.token });
      return refresh();
    },
  });
  const reorder = useMutation(AdminService.method.reorderNodes, { ...mutationOptions, onSuccess: refresh });
  // 删除标签是本页的显式动作。被删的名字若留在过滤条件里，hub 只会返回空结果（不存在的标签匹配不到任何节点），
  // 所以删除后把它从条件里去掉，列表回到其余条件下的样子。
  const removeTag = useMutation(AdminService.method.deleteTag, {
    ...mutationOptions,
    onSuccess: (_r, req) => {
      setTagFilter((cur) => withoutTag(cur, req.name ?? ""));
      return refresh();
    },
  });

  const onCreate = (e: FormEvent) => { e.preventDefault(); create.mutate({ name }); };
  // 排序接口要求全部 id 的完整排列：搜索与标签过滤的结果是子集，沿用的结果属于上一个条件（当前条件为空时也可能
  // 只是子集），这两种情况都不开放排序入口。
  const move = (list: Node[], i: number, dir: -1 | 1) => {
    const ids = list.map((n) => n.id);
    const j = i + dir;
    if (j < 0 || j >= ids.length) return;
    [ids[i], ids[j]] = [ids[j], ids[i]];
    reorder.mutate({ ids });
  };

  const gate = queryGate(nodes);
  const filtered = search !== "" || tagFilter.length > 0;
  const narrowed = filtered || nodes.stale;
  const table = (list: Node[]) => (
    <>
      {filtered && <p className="muted">搜索或按标签过滤时无法排序，请清空搜索与标签过滤后调整完整节点顺序。</p>}
      {!filtered && nodes.stale && <p className="muted">列表还不是当前条件下的结果，暂时无法排序。</p>}
      {narrowed && list.length === 0 && <p className="muted" role="status">没有匹配的节点。</p>}
      <div className="table-scroll" role="region" aria-label="节点管理" tabIndex={0}>
        <table className="nodes">
          <thead><tr><th>排序</th><th>名称</th><th>公开</th><th>国家 / 地区</th><th>标签</th><th>备注</th><th>重置日</th><th>离线宽限期</th><th>计费</th><th>创建于</th><th>操作</th></tr></thead>
          <tbody>
            {list.map((n, i) => (
              <NodeEditor key={String(n.id)} node={n} hubVersion={hubVersion} knownTags={tags.data?.tags ?? []}
                saving={update.isPending} deleting={remove.isPending} rotating={rotate.isPending}
                onMoveUp={narrowed ? undefined : () => move(list, i, -1)}
                onMoveDown={narrowed ? undefined : () => move(list, i, 1)}
                onSave={(patch, onSuccess) => update.mutate({ id: n.id, ...patch }, { onSuccess })}
                onDelete={() => remove.mutate({ id: n.id })}
                onRotate={() => rotate.mutate({ id: n.id })} />
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
  return (
    <section>
      {errorBanner(nodes.error)}
      {snapshot.error != null && (
        <p role="alert" className="error">
          {hubVersion === undefined ? "无法取得 hub 版本，落后标记不可用" : `刷新 hub 版本失败，落后标记按上次取得的 ${hubVersion || "空版本"} 判断`}：{errorText(snapshot.error)}
        </p>
      )}
      <h1>节点</h1>
      {secret && <Secret label={secret.label} value={secret.value} />}
      <form onSubmit={onCreate} className="row">
        <label>新节点名称<input value={name} onChange={(e) => setName(e.target.value)} /></label>
        <button type="submit" disabled={create.isPending || name.trim() === ""}>创建</button>
      </form>
      <div className="node-filters">
        <label className="node-search">搜索节点<input type="search" placeholder="名称、备注或主机名" value={search} onChange={(e) => setSearch(e.target.value)} /></label>
        <TagFilter tags={tags.data?.tags} error={tags.error} selected={tagFilter} onChange={setTagFilter} />
      </div>
      {error != null && <p role="alert" className="error">{errorText(error)}</p>}
      {gate.ready ? table(filterNodes(gate.data.nodes, search)) : gate.loading}
      <TagManager tags={tags.data?.tags} pending={removeTag.isPending} onDelete={(name) => removeTag.mutate({ name })} />
    </section>
  );
}

// 多选过滤取交集：只列同时带有所选全部标签的节点。已选却不在清单里的标签（在别处被删了）照样列出并保持勾选，
// 否则结果为空而找不到可以取消的勾选。
function TagFilter({ tags, error, selected, onChange }: {
  tags: readonly Tag[] | undefined; error: unknown; selected: readonly string[]; onChange: (next: string[]) => void;
}) {
  const listed = (tags ?? []).map((t) => t.name);
  const names = [...listed, ...selected.filter((s) => !listed.some((t) => sameTag(t, s)))];
  const count = (name: string) => tags?.find((t) => t.name === name)?.nodeCount;
  return (
    <fieldset className="picks tag-filter">
      <legend>按标签过滤（同时带有所选全部标签）</legend>
      {error != null && <span role="alert" className="error">无法取得标签清单：{errorText(error)}</span>}
      {error == null && tags !== undefined && names.length === 0 && <span className="muted">还没有标签。</span>}
      {names.map((name) => {
        const checked = selected.some((s) => sameTag(s, name));
        const n = count(name);
        return (
          <label key={name}>
            <input type="checkbox" aria-label={`按标签过滤 ${name}`} checked={checked} onChange={() => onChange(checked ? withoutTag(selected, name) : withTag(selected, name))} />
            {name}{n !== undefined && <span className="muted">（{n}）</span>}
          </label>
        );
      })}
      {selected.length > 0 && <button type="button" className="link" onClick={() => onChange([])}>清除标签过滤</button>}
    </fieldset>
  );
}

// 删除标签只从节点上解除它，节点本身不受影响。
function TagManager({ tags, pending, onDelete }: { tags: readonly Tag[] | undefined; pending: boolean; onDelete: (name: string) => void }) {
  if (tags === undefined) return null;
  return (
    <section aria-label="标签">
      <h2>标签</h2>
      {tags.length === 0 ? <p className="muted">还没有标签；在节点的编辑里添加。</p> : (
        <ul className="tag-list">
          {tags.map((t) => (
            <li key={t.name}>
              <span className="tag">{t.name}</span> <span className="muted">{t.nodeCount} 个节点</span>{" "}
              <ConfirmDelete label={`删除标签 ${t.name}`} confirm={`确认删除标签 ${t.name}`} note="只从节点上解除，节点不受影响" pending={pending} onDelete={() => onDelete(t.name)} />
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

const validResetDay = (day: number) => Number.isInteger(day) && day >= 1 && day <= 28;

// 宽限期以字符串编辑，0 表示清除（取 hub 的 PROBE_OFFLINE_AFTER）。计费五项、手动指定的国家与标签随整行整体提交（UpdateNode
// 整体替换，缺失即清除，标签不带就是清空），节点没有 billing 时从空值开始；取值约束由 hub 裁决并把错误原文显示在列表上方，
// 页面不另抄一份规则。
const draftOf = (node: Node) => ({
  name: node.name, public: node.public, note: node.note, trafficResetDay: node.trafficResetDay, countryPin: node.countryPin,
  tags: [...node.tags],
  offlineGraceS: String(node.offlineGraceS ?? 0),
  billing: {
    price: node.billing?.price ?? "", currency: node.billing?.currency ?? "", billingCycle: node.billing?.billingCycle ?? BillingCycle.UNSPECIFIED,
    expiresOn: node.billing?.expiresOn ?? "", autoRenew: node.billing?.autoRenew ?? false,
  },
});
type Draft = ReturnType<typeof draftOf>;
type BillingDraft = Draft["billing"];
const validGrace = (s: string) => /^\d+$/.test(s);

function NodeEditor({ node, hubVersion, knownTags, saving, deleting, rotating, onMoveUp, onMoveDown, onSave, onDelete, onRotate }: {
  node: Node; hubVersion: string | undefined; knownTags: readonly Tag[];
  saving: boolean; deleting: boolean; rotating: boolean;
  onMoveUp?: () => void; onMoveDown?: () => void;
  onSave: (patch: Omit<Draft, "offlineGraceS"> & { offlineGraceS: number }, onSuccess: () => void) => void;
  onDelete: () => void; onRotate: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(draftOf(node));
  // 标签输入框里还没按添加的文字在保存时一并提交：用户输完直接点保存，不应丢掉刚输入的标签。
  const [pendingTag, setPendingTag] = useState("");
  if (editing) {
    return (
      <tr>
        <td />
        <td><input aria-label={`名称 ${withId(node.name, node.id)}`} value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} /></td>
        <td><input type="checkbox" aria-label={`公开 ${withId(node.name, node.id)}`} checked={draft.public} onChange={(e) => setDraft({ ...draft, public: e.target.checked })} /></td>
        <td>
          {/* 国家码都是大写，输入时就转成大写；其余取值原样交给 hub 校验。 */}
          <input aria-label={`手动指定国家 / 地区 ${withId(node.name, node.id)}`} aria-describedby={`country-hint-${node.id}`} placeholder="US" value={draft.countryPin} onChange={(e) => setDraft({ ...draft, countryPin: e.target.value.toUpperCase() })} />
          <p className="muted" id={`country-hint-${node.id}`}>两个字母（ISO 3166-1），优先于查得值；留空用查得值：{node.countryIp ? `查得于 ${node.countryIp}` : "尚无查得值"}。</p>
        </td>
        <td><TagsEditor id={node.id} label={withId(node.name, node.id)} tags={draft.tags} known={knownTags} pending={pendingTag} onPending={setPendingTag} onChange={(tags) => setDraft({ ...draft, tags })} /></td>
        <td><input aria-label={`备注 ${withId(node.name, node.id)}`} value={draft.note} onChange={(e) => setDraft({ ...draft, note: e.target.value })} /></td>
        <td><input type="number" min={1} max={28} aria-label={`重置日 ${withId(node.name, node.id)}`} value={draft.trafficResetDay} onChange={(e) => setDraft({ ...draft, trafficResetDay: Number(e.target.value) })} /><p className="muted">若从本周期起点算起新的重置日已经过去，本周期用量会立即清零。</p></td>
        <td><input type="number" min={0} aria-label={`离线宽限期（秒） ${withId(node.name, node.id)}`} aria-describedby={`grace-hint-${node.id}`} value={draft.offlineGraceS} onChange={(e) => setDraft({ ...draft, offlineGraceS: e.target.value })} /><p className="muted" id={`grace-hint-${node.id}`}>0 表示取 hub 的 PROBE_OFFLINE_AFTER；非 0 不能小于它。</p></td>
        <td><BillingEditor label={withId(node.name, node.id)} draft={draft.billing} onChange={(patch) => setDraft({ ...draft, billing: { ...draft.billing, ...patch } })} /></td>
        <td />
        <td>
          <button type="button" disabled={saving || !validResetDay(draft.trafficResetDay) || !validGrace(draft.offlineGraceS)} onClick={() => onSave({ ...draft, tags: withTag(draft.tags, pendingTag), offlineGraceS: Number(draft.offlineGraceS) }, () => setEditing(false))}>保存</button>{" "}
          <button type="button" className="link" onClick={() => setEditing(false)}>取消</button>
        </td>
      </tr>
    );
  }
  return (
    <tr>
      <td>
        <button type="button" className="link" aria-label={`上移 ${withId(node.name, node.id)}`} onClick={onMoveUp} disabled={!onMoveUp}>↑</button>
        <button type="button" className="link" aria-label={`下移 ${withId(node.name, node.id)}`} onClick={onMoveDown} disabled={!onMoveDown}>↓</button>
      </td>
      <td>
        <Link to={`/nodes/${node.id}`} aria-label={withId(node.name, node.id)}>{node.name}</Link>
        {hubVersion !== undefined && lagsHub(node.facts?.agentVersion, hubVersion) && <>{" "}<span className="warn">落后于 hub</span></>}
      </td>
      <td>{node.public ? "是" : "否"}</td>
      <td><CountryCell node={node} /></td>
      <td>{node.tags.length === 0 ? "—" : node.tags.map((t) => <span key={t} className="tag">{t}</span>)}</td>
      <td className="muted">{node.note}</td>
      <td>每月 {node.trafficResetDay} 日</td>
      <td>{graceText(node.offlineGraceS)}</td>
      <td><BillingSummary node={node} /></td>
      <td className="muted">{new Date(Number(node.createdAt) * 1000).toLocaleDateString()}</td>
      <td>
        <button type="button" className="link" aria-label={`编辑 ${withId(node.name, node.id)}`} onClick={() => { setDraft(draftOf(node)); setPendingTag(""); setEditing(true); }}>编辑</button>{" "}
        <button type="button" className="link" aria-label={`换 token ${withId(node.name, node.id)}`} onClick={onRotate} disabled={rotating}>换 token</button>{" "}
        <ConfirmDelete label={`删除 ${withId(node.name, node.id)}`} confirm={`确认删除 ${withId(node.name, node.id)}`} pending={deleting} onDelete={onDelete} />
      </td>
    </tr>
  );
}

// 显示值、来源与查得于哪个地址："🇺🇸 US 查得于 8.8.8.8"、"🇯🇵 JP 手动指定"；没有国家是"—"。手动指定时查询照常进行，
// 查得于哪个地址也照写：清空手动值即回落到它。
function CountryCell({ node }: { node: Node }) {
  if (node.countrySource === CountrySource.UNSPECIFIED) return <>—</>;
  const lookup = node.countryIp ? `查得于 ${node.countryIp}` : "";
  const source = node.countrySource === CountrySource.MANUAL ? ["手动指定", lookup].filter(Boolean).join("；") : lookup;
  return <><CountryBadge code={node.country} />{" "}<span className="muted">{source}</span></>;
}

// "USD 12.50 / 月 · 2026-10-01（剩 4 天） · 自动续期"；已过期的那一段用告警红；全没填是"—"。
function BillingSummary({ node }: { node: Node }) {
  const parts: ReactNode[] = [];
  const price = priceText(node.billing);
  if (price) parts.push(price);
  const expiry = expiryText(node.billing);
  if (expiry) parts.push(<span className={expired(node.billing) ? "error" : undefined}>{expiry}</span>);
  if (node.billing?.autoRenew) parts.push("自动续期");
  if (parts.length === 0) return <>—</>;
  return <>{parts.map((p, i) => <Fragment key={i}>{i > 0 && " · "}{p}</Fragment>)}</>;
}

// 标签可新建：输入已有标签以外的名字即在保存时新建。大小写不敏感，已有的标签沿用先建的写法（由 hub 裁决）；
// 候选来自标签清单。
function TagsEditor({ id, label, tags, known, pending, onPending, onChange }: {
  id: bigint; label: string; tags: readonly string[]; known: readonly Tag[]; pending: string; onPending: (text: string) => void; onChange: (tags: string[]) => void;
}) {
  const add = () => { onChange(withTag(tags, pending)); onPending(""); };
  return (
    <div className="tags-edit">
      {tags.map((t) => (
        <span key={t} className="tag">{t}<button type="button" className="link" aria-label={`移除标签 ${t} ${label}`} onClick={() => onChange(withoutTag(tags, t))}>×</button></span>
      ))}
      <input aria-label={`新标签 ${label}`} list={`known-tags-${id}`} value={pending} onChange={(e) => onPending(e.target.value)}
        onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); add(); } }} />
      <datalist id={`known-tags-${id}`}>{known.map((t) => <option key={t.name} value={t.name} />)}</datalist>
      <button type="button" className="link" aria-label={`添加标签 ${label}`} onClick={add}>添加</button>
      <p className="muted">大小写不敏感，已有的标签沿用先建的写法；每个节点至多 16 个。</p>
    </div>
  );
}

function BillingEditor({ label, draft, onChange }: { label: string; draft: BillingDraft; onChange: (patch: Partial<BillingDraft>) => void }) {
  return (
    <div className="billing-edit">
      <input aria-label={`价格 ${label}`} inputMode="decimal" placeholder="12.50" value={draft.price} onChange={(e) => onChange({ price: e.target.value })} />
      {/* ISO 4217 代码都是大写，输入时就转成大写；其余取值原样交给 hub 校验。 */}
      <input aria-label={`币种 ${label}`} placeholder="USD" value={draft.currency} onChange={(e) => onChange({ currency: e.target.value.toUpperCase() })} />
      <select aria-label={`周期 ${label}`} value={draft.billingCycle} onChange={(e) => onChange({ billingCycle: Number(e.target.value) as BillingCycle })}>
        <option value={BillingCycle.UNSPECIFIED}>无周期</option>
        {BILLING_CYCLES.map(({ value, label: cycle }) => <option key={value} value={value}>每{cycle}</option>)}
      </select>
      <input type="date" aria-label={`到期日 ${label}`} value={draft.expiresOn} onChange={(e) => onChange({ expiresOn: e.target.value })} />
      <label className="inline"><input type="checkbox" aria-label={`自动续期 ${label}`} checked={draft.autoRenew} onChange={(e) => onChange({ autoRenew: e.target.checked })} />自动续期</label>
      <p className="muted">只用于展示与到期提醒。开着自动续期时，到期日过了 hub 按周期推后；需要周期与到期日。</p>
    </div>
  );
}
