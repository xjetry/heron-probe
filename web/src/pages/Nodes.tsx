import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, Fragment, type ReactNode, useState } from "react";
import { Link } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
import { ConfirmDelete } from "../components/ConfirmDelete";
import { CountryBadge } from "../components/CountryBadge";
import { Secret } from "../components/Secret";
import { AdminService, CountrySource, type Node } from "../gen/probe/v1/admin_pb";
import { BillingCycle } from "../gen/probe/v1/types_pb";
import { errorText } from "../api/auth";
import { useLatestError } from "../api/useLatestError";
import { graceText } from "../lib/alerts";
import { BILLING_CYCLES, expired, expiryText, priceText } from "../lib/billing";
import { withId } from "../lib/ids";
import { lagsHub } from "../lib/version";

export function Nodes() {
  const qc = useQueryClient();
  const { error, mutationOptions } = useLatestError();
  const nodes = useQuery(AdminService.method.listNodes, {});
  // 只用于落后标记的可选查询：不进页面门控，失败或未就绪时不标，也不卸载列表；失败时在列表上方说明标记不可用，
  // 否则用户会把"没有标记"读成"没有落后的节点"。hub 版本在进程生命周期内不变，不轮询。
  const snapshot = useQuery(AdminService.method.getSnapshot, {});
  const hubVersion = snapshot.data?.hubVersion;
  const refresh = () => qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listNodes, cardinality: "finite" }) });
  // token 只在创建与换 token 的响应里各出现一次，hub 不存明文；展示不经过 isLatest 门控——门控丢弃迟到结果时会把这唯一一份明文一起丢掉。
  // id 记下明文属于哪一行：删除的若正是这一行，卡片必须一起消失，不能继续展示已删对象的凭据。
  const [secret, setSecret] = useState<{ id: bigint; label: string; value: string } | null>(null);
  const [name, setName] = useState("");

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
      {error != null && <p role="alert" className="error">{errorText(error)}</p>}
      <div className="table-scroll" role="region" aria-label="节点管理" tabIndex={0}>
        <table className="nodes">
          <thead><tr><th>排序</th><th>名称</th><th>公开</th><th>国家 / 地区</th><th>备注</th><th>重置日</th><th>离线宽限期</th><th>计费</th><th>创建于</th><th>操作</th></tr></thead>
          <tbody>
            {list.map((n, i) => (
              <NodeEditor key={String(n.id)} node={n} hubVersion={hubVersion}
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

// 宽限期以字符串编辑，0 表示清除（取 hub 的 PROBE_OFFLINE_AFTER）。计费五项与手动指定的国家随整行整体提交（UpdateNode
// 整体替换，缺失即清除），节点没有 billing 时从空值开始；取值约束由 hub 裁决并把错误原文显示在列表上方，页面不另抄一份规则。
const draftOf = (node: Node) => ({
  name: node.name, public: node.public, note: node.note, trafficResetDay: node.trafficResetDay, countryPin: node.countryPin,
  offlineGraceS: String(node.offlineGraceS ?? 0),
  billing: {
    price: node.billing?.price ?? "", currency: node.billing?.currency ?? "", billingCycle: node.billing?.billingCycle ?? BillingCycle.UNSPECIFIED,
    expiresOn: node.billing?.expiresOn ?? "", autoRenew: node.billing?.autoRenew ?? false,
  },
});
type Draft = ReturnType<typeof draftOf>;
type BillingDraft = Draft["billing"];
const validGrace = (s: string) => /^\d+$/.test(s);

function NodeEditor({ node, hubVersion, saving, deleting, rotating, onMoveUp, onMoveDown, onSave, onDelete, onRotate }: {
  node: Node; hubVersion: string | undefined;
  saving: boolean; deleting: boolean; rotating: boolean;
  onMoveUp: () => void; onMoveDown: () => void;
  onSave: (patch: Omit<Draft, "offlineGraceS"> & { offlineGraceS: number }, onSuccess: () => void) => void;
  onDelete: () => void; onRotate: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(draftOf(node));
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
        <td><input aria-label={`备注 ${withId(node.name, node.id)}`} value={draft.note} onChange={(e) => setDraft({ ...draft, note: e.target.value })} /></td>
        <td><input type="number" min={1} max={28} aria-label={`重置日 ${withId(node.name, node.id)}`} value={draft.trafficResetDay} onChange={(e) => setDraft({ ...draft, trafficResetDay: Number(e.target.value) })} /><p className="muted">若从本周期起点算起新的重置日已经过去，本周期用量会立即清零。</p></td>
        <td><input type="number" min={0} aria-label={`离线宽限期（秒） ${withId(node.name, node.id)}`} aria-describedby={`grace-hint-${node.id}`} value={draft.offlineGraceS} onChange={(e) => setDraft({ ...draft, offlineGraceS: e.target.value })} /><p className="muted" id={`grace-hint-${node.id}`}>0 表示取 hub 的 PROBE_OFFLINE_AFTER；非 0 不能小于它。</p></td>
        <td><BillingEditor label={withId(node.name, node.id)} draft={draft.billing} onChange={(patch) => setDraft({ ...draft, billing: { ...draft.billing, ...patch } })} /></td>
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
        <button type="button" className="link" aria-label={`上移 ${withId(node.name, node.id)}`} onClick={onMoveUp}>↑</button>
        <button type="button" className="link" aria-label={`下移 ${withId(node.name, node.id)}`} onClick={onMoveDown}>↓</button>
      </td>
      <td>
        <Link to={`/nodes/${node.id}`} aria-label={withId(node.name, node.id)}>{node.name}</Link>
        {hubVersion !== undefined && lagsHub(node.facts?.agentVersion, hubVersion) && <>{" "}<span className="warn">落后于 hub</span></>}
      </td>
      <td>{node.public ? "是" : "否"}</td>
      <td><CountryCell node={node} /></td>
      <td className="muted">{node.note}</td>
      <td>每月 {node.trafficResetDay} 日</td>
      <td>{graceText(node.offlineGraceS)}</td>
      <td><BillingSummary node={node} /></td>
      <td className="muted">{new Date(Number(node.createdAt) * 1000).toLocaleDateString()}</td>
      <td>
        <button type="button" className="link" aria-label={`编辑 ${withId(node.name, node.id)}`} onClick={() => { setDraft(draftOf(node)); setEditing(true); }}>编辑</button>{" "}
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
