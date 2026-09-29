import { type FormEvent, useState } from "react";
import type { Node, Tag } from "../gen/heron/v1/admin_pb";
import { BillingCycle } from "../gen/heron/v1/types_pb";
import { errorBanner } from "../api/queryGate";
import { BILLING_CYCLES } from "../lib/billing";
import { withId } from "../lib/ids";
import { withTag, withoutTag } from "../lib/tags";
import { Modal } from "../components/Modal";
import { DateInput } from "../components/DateInput";
import { NodeAddresses } from "../components/NodeAddresses";
import { lookupText, NodeCountry } from "../components/NodeCountry";

const BILLING_CURRENCIES = ["CNY", "USD", "HKD", "CAD", "EUR", "GBP"];

// UpdateNode 整体替换全部可编辑字段；独立计费入口也必须保留未编辑字段，不能用缺席表达“不变”。
const draftOf = (node: Node) => ({
  name: node.name, public: node.public, note: node.note, trafficResetDay: node.trafficResetDay, countryPin: node.countryPin,
  tags: [...node.tags], offlineGraceS: String(node.offlineGraceS ?? 0),
  billing: {
    price: node.billing?.price ?? "", currency: node.billing?.currency ?? "", billingCycle: node.billing?.billingCycle ?? BillingCycle.UNSPECIFIED,
    expiresOn: node.billing?.expiresOn ?? "", autoRenew: node.billing?.autoRenew ?? false,
  },
});
type Draft = ReturnType<typeof draftOf>;
export type NodePatch = Omit<Draft, "offlineGraceS"> & { offlineGraceS: number };

export function NodeEditor({ node, mode, knownTags, saving, error, listError, onClose, onSave, opener }: {
  node: Node; mode: "general" | "billing"; knownTags: readonly Tag[]; saving: boolean; error: unknown; listError: unknown;
  onClose: () => void; onSave: (patch: NodePatch) => void; opener: HTMLElement;
}) {
  const [draft, setDraft] = useState(() => draftOf(node));
  const [pendingTag, setPendingTag] = useState("");
  const label = withId(node.name, node.id);
  const valid = draft.name.trim() !== "" && Number.isInteger(draft.trafficResetDay) && draft.trafficResetDay >= 1 && draft.trafficResetDay <= 28 && /^\d+$/.test(draft.offlineGraceS);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!saving && valid) onSave({ ...draft, tags: withTag(draft.tags, pendingTag), offlineGraceS: Number(draft.offlineGraceS) });
  };
  return <Modal title={`${mode === "billing" ? "计费设置" : "编辑节点"} · ${label}`} description={mode === "billing" ? "管理价格、付款周期和到期提醒。" : "更新节点资料、公开范围与监控配置。"} busy={saving} onClose={onClose} opener={opener}>
    <form onSubmit={submit}>
      <div className="modal-body">
        {errorBanner(error, listError)}
        <fieldset className="bare" disabled={saving}>
          {mode === "general" ? <>
            <section className="form-section">
              <h3>基础信息</h3>
              <div className="form-grid">
                <label>节点名称<input data-autofocus aria-label={`名称 ${label}`} value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} /></label>
                <label>备注<textarea aria-label={`备注 ${label}`} placeholder="商家、用途或其他内部备注" value={draft.note} onChange={(e) => setDraft({ ...draft, note: e.target.value })} /></label>
                <label className="switch-field full-width"><span>公开显示<small>关闭后仅在管理后台可见</small></span><input type="checkbox" aria-label={`公开 ${label}`} checked={draft.public} onChange={(e) => setDraft({ ...draft, public: e.target.checked })} /></label>
                <div className="full-width"><label htmlFor={`tag-input-${node.id}`}>节点标签</label><TagsEditor id={node.id} label={label} isPublic={draft.public} tags={draft.tags} known={knownTags} pending={pendingTag} onPending={setPendingTag} onChange={(tags) => setDraft({ ...draft, tags })} /></div>
              </div>
            </section>
            <section className="form-section">
              <h3>流量与在线判定</h3>
              <div className="form-grid">
                <label>每月流量重置日<input type="number" min={1} max={28} aria-label={`重置日 ${label}`} value={draft.trafficResetDay} onChange={(e) => setDraft({ ...draft, trafficResetDay: Number(e.target.value) })} /><span className="muted">若从本周期起点算起新的重置日已经过去，本周期用量会立即清零。</span></label>
                <label>离线宽限期（秒）<input type="number" min={0} aria-label={`离线宽限期（秒） ${label}`} aria-describedby={`grace-hint-${node.id}`} value={draft.offlineGraceS} onChange={(e) => setDraft({ ...draft, offlineGraceS: e.target.value })} /><span className="muted" id={`grace-hint-${node.id}`}>0 表示取 hub 的 HERON_OFFLINE_AFTER；非 0 不能小于它。</span></label>
              </div>
            </section>
            <section className="form-section">
              <h3>地址与地区</h3>
              <div className="address-diagnostics"><NodeAddresses network={node.facts?.network} detailed /><p className="muted">agent 探测出口，每 5 分钟更新。无可用地址或路由标记为不支持；超时与服务异常标记为探测失败。</p></div>
              <div className="form-grid">
                <div><label>上报来源 IP</label><p>{node.lastSource ? <code>{node.lastSource}</code> : <span className="muted">尚未记录来源</span>}</p><p className="muted">hub 实际观察到的来源，经过反代时依赖可信代理配置；与 agent 探测结果独立。</p></div>
                <label>手动指定国家 / 地区<input aria-label={`手动指定国家 / 地区 ${label}`} aria-describedby={`country-hint-${node.id}`} placeholder="例如 JP，留空自动查询" value={draft.countryPin} onChange={(e) => setDraft({ ...draft, countryPin: e.target.value.toUpperCase() })} /><span className="muted" id={`country-hint-${node.id}`}>两个字母（ISO 3166-1），优先于查得值；留空用查得值：{node.countryLookup ? lookupText(node) : "尚无查得值"}。</span></label>
              </div>
              <p className="muted"><NodeCountry node={node} /> · 创建于 {new Date(Number(node.createdAt) * 1000).toLocaleDateString()}</p>
            </section>
          </> : <section className="form-section"><BillingEditor label={label} draft={draft.billing} onChange={(patch) => setDraft({ ...draft, billing: { ...draft.billing, ...patch } })} /></section>}
        </fieldset>
      </div>
      <footer className="modal-footer"><button type="button" onClick={onClose} disabled={saving}>取消</button><button type="submit" className="primary-button" disabled={saving || !valid} aria-busy={saving}>保存</button></footer>
    </form>
  </Modal>;
}

function TagsEditor({ id, label, isPublic, tags, known, pending, onPending, onChange }: {
  id: bigint; label: string; isPublic: boolean; tags: readonly string[]; known: readonly Tag[]; pending: string; onPending: (text: string) => void; onChange: (tags: string[]) => void;
}) {
  const add = () => { onChange(withTag(tags, pending)); onPending(""); };
  return <div className="tags-edit">
    <div>{tags.map((tag) => <span key={tag} className="tag">{tag}<button type="button" className="link" aria-label={`移除标签 ${tag} ${label}`} onClick={() => onChange(withoutTag(tags, tag))}>×</button></span>)}</div>
    <div className="tags-input-row"><input id={`tag-input-${id}`} aria-label={`新标签 ${label}`} list={`known-tags-${id}`} placeholder="选择已有标签，或输入新标签" value={pending} onChange={(e) => onPending(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); add(); } }} />
    <button type="button" aria-label={`添加标签 ${label}`} onClick={add}>添加</button></div>
    <datalist id={`known-tags-${id}`}>{known.map((tag) => <option key={tag.name} value={tag.name} />)}</datalist>
    <p className="muted">大小写不敏感，已有的标签沿用先建的写法；每个节点至多 16 个。</p>
    {isPublic && <p className="muted">公开节点的标签在公开页对访客可见。</p>}
  </div>;
}

function BillingEditor({ label, draft, onChange }: { label: string; draft: Draft["billing"]; onChange: (patch: Partial<Draft["billing"]>) => void }) {
  return <div className="billing-edit">
    <label>价格<input data-autofocus aria-label={`价格 ${label}`} inputMode="decimal" placeholder="12.50" value={draft.price} onChange={(e) => onChange({ price: e.target.value })} /></label>
    <label>币种<select aria-label={`币种 ${label}`} value={draft.currency} onChange={(e) => onChange({ currency: e.target.value })}>
      <option value="">未设置</option>
      {draft.currency && !BILLING_CURRENCIES.includes(draft.currency) && <option value={draft.currency} disabled>{draft.currency}（当前值）</option>}
      {BILLING_CURRENCIES.map((currency) => <option key={currency} value={currency}>{currency}</option>)}
    </select></label>
    <label>付款周期<select aria-label={`周期 ${label}`} value={draft.billingCycle} onChange={(e) => onChange({ billingCycle: Number(e.target.value) as BillingCycle })}>
      <option value={BillingCycle.UNSPECIFIED}>无周期</option>{BILLING_CYCLES.map(({ value, label: cycle }) => <option key={value} value={value}>每{cycle}</option>)}
    </select></label>
    <DateInput label={`到期日 ${label}`} value={draft.expiresOn} onChange={(expiresOn) => onChange({ expiresOn })} />
    <label className="switch-field"><span>自动续期<small>到期后自动按付款周期推后日期</small></span><input type="checkbox" aria-label={`自动续期 ${label}`} checked={draft.autoRenew} onChange={(e) => onChange({ autoRenew: e.target.checked })} /></label>
    <p className="muted">只用于展示与到期提醒。开着自动续期时，到期日过了 hub 按周期推后；需要周期与到期日。</p>
  </div>;
}
