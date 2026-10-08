import { type FormEvent, useState } from "react";
import { errorBanner } from "../api/queryGate";
import { BillingEditor, type BillingDraft, billingDraftSet, emptyBillingDraft } from "./BillingEditor";
import { Drawer } from "./Modal";

// 添加节点只要名称；计费选填，没填时请求不带 billing（hub 把缺席当"未设置"，空对象会被当成"全空的计费"）。
// 草稿属于抽屉：关闭即丢弃，重新打开从空开始。
export function NodeCreateDrawer({ opener, pending, error, onClose, onCreate }: {
  opener: HTMLElement; pending: boolean; error: unknown; onClose: () => void; onCreate: (request: { name: string; billing?: BillingDraft }) => void;
}) {
  const [name, setName] = useState("");
  const [billing, setBilling] = useState(emptyBillingDraft);
  const submit = (event: FormEvent) => { event.preventDefault(); if (name.trim() && !pending) onCreate(billingDraftSet(billing) ? { name, billing } : { name }); };
  return <Drawer title="添加节点" description="创建后在这里直接给出安装命令；token 仅用于注册 agent，不能上报指标。计费可留空，稍后在编辑里补。" busy={pending} opener={opener} onClose={onClose}>
    <form onSubmit={submit}>
      <div className="modal-body">{errorBanner(error)}
        <label>新节点名称<input data-autofocus value={name} onChange={(event) => setName(event.target.value)} placeholder="例如 tokyo-01" disabled={pending} /></label>
        <fieldset className="bare" disabled={pending}><section className="form-section" aria-label="计费（选填）"><h3>计费（选填）</h3><BillingEditor label="新节点" draft={billing} onChange={(patch) => setBilling({ ...billing, ...patch })} /></section></fieldset>
      </div>
      <footer className="modal-footer"><button type="button" disabled={pending} onClick={onClose}>取消</button><button type="submit" className="primary-button" disabled={pending || name.trim() === ""}>创建</button></footer>
    </form>
  </Drawer>;
}
