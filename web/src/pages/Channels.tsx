import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useRef, useState } from "react";
import { errorText } from "../api/auth";
import { errorBanner, queryGate } from "../api/queryGate";
import { useLatestError } from "../api/useLatestError";
import { ConfirmDelete } from "../components/ConfirmDelete";
import { AdminService, ChannelKind, type NotifyChannel } from "../gen/probe/v1/admin_pb";
import { CHANNEL_KINDS, channelTarget, labelOf, methodOf } from "../lib/alerts";

const METHODS = ["POST", "PUT", "PATCH"] as const;
type HeaderRow = { id: number; name: string; value: string };
type Draft = {
  name: string; kind: ChannelKind; botToken: string; chatId: string;
  url: string; method: string; headers: HeaderRow[]; removeHeaders: Set<string>; bodyTemplate: string;
};

const emptyDraft = (): Draft => ({
  name: "", kind: ChannelKind.TELEGRAM, botToken: "", chatId: "",
  url: "", method: "POST", headers: [], removeHeaders: new Set(), bodyTemplate: "",
});
// hub 不回显 token、URL 与头值，编辑草稿里它们恒为空；同种类时留空提交由 hub 保留已存值，换种类必须重填。
const draftOf = (c: NotifyChannel): Draft => ({
  ...emptyDraft(), name: c.name, kind: c.kind, chatId: c.telegram?.chatId ?? "",
  method: methodOf(c.webhook?.method), bodyTemplate: c.webhook?.bodyTemplate ?? "",
});

function toChannel(id: bigint, d: Draft) {
  const base = { id, name: d.name.trim(), kind: d.kind };
  if (d.kind === ChannelKind.TELEGRAM) return { ...base, telegram: { botToken: d.botToken.trim(), chatId: d.chatId.trim() } };
  return { ...base, webhook: {
    url: d.url.trim(), method: d.method, bodyTemplate: d.bodyTemplate,
    headers: Object.fromEntries(d.headers.map((h) => [h.name.trim(), h.value])),
    removeHeaders: [...d.removeHeaders].sort(),
  } };
}

// 完全同名的两行在 map 里合并、后一行静默胜出，只有表单看得到这个丢失；
// 大小写不同的两行 hub 也会拒绝，这里一并拦下，两种重名在提交前得到同一条提示。
function duplicateHeader(rows: HeaderRow[]): string | undefined {
  const seen = new Set<string>();
  for (const r of rows) {
    const key = r.name.trim().toLowerCase();
    if (seen.has(key)) return r.name.trim();
    seen.add(key);
  }
  return undefined;
}

export function Channels() {
  const qc = useQueryClient();
  const [creation, setCreation] = useState(0);
  const [notice, setNotice] = useState<string | null>(null);
  const { error, isLatest, mutationOptions } = useLatestError();
  // 任何新操作开始时，旧的成功提示与失败一并清除；在途请求的迟到结果由 isLatest 挡回，成功与失败同口径。
  const tracked = { ...mutationOptions, onMutate: () => { setNotice(null); return mutationOptions.onMutate(); } };
  const list = useQuery(AdminService.method.listNotifyChannels, {});
  const refresh = () => qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listNotifyChannels, cardinality: "finite" }) });
  const create = useMutation(AdminService.method.saveNotifyChannel, { ...tracked, onSuccess: refresh });
  // 各行共用一个 mutation observer，重叠的 mutate 只回调最后一次；任一行保存挂起时禁用全部行的保存。
  // 返回刷新 promise，编辑态在列表显示已保存值之后才关闭。
  const update = useMutation(AdminService.method.saveNotifyChannel, { ...tracked, onSuccess: refresh });
  const remove = useMutation(AdminService.method.deleteNotifyChannel, { ...tracked, onSuccess: refresh });
  const test = useMutation(AdminService.method.testNotifyChannel, tracked);
  const gate = queryGate(list);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const channels = gate.data.channels;
  return (
    <section>
      {gate.banner}
      <h1>通知渠道</h1>
      <ChannelForm key={creation} title="新建通知渠道" initial={emptyDraft()} pending={create.isPending}
        onSubmit={(d) => create.mutate({ channel: toChannel(0n, d) }, { onSuccess: () => setCreation((k) => k + 1) })} />
      {error != null && <p role="alert" className="error">{errorText(error)}</p>}
      <p role="status">{notice ?? ""}</p>
      <div className="table-scroll" role="region" aria-label="通知渠道管理" tabIndex={0}>
        <table className="nodes">
          <thead><tr><th>名称</th><th>类型</th><th>目标</th><th>创建于</th><th>操作</th></tr></thead>
          <tbody>
            {channels.map((c) => (
              <ChannelRow key={String(c.id)} channel={c} saving={update.isPending} deleting={remove.isPending} testing={test.isPending}
                onSave={(d, onSuccess) => update.mutate({ channel: toChannel(c.id, d) }, { onSuccess })}
                onTest={() => test.mutate({ id: c.id }, { onSuccess: (_r, _v, op) => { if (isLatest(op)) setNotice(`已向 ${c.name} 发送测试消息`); } })}
                onDelete={() => remove.mutate({ id: c.id })} />
            ))}
          </tbody>
        </table>
      </div>
      {channels.length === 0 && <p className="muted">还没有通知渠道。</p>}
    </section>
  );
}

function ChannelForm({ title, initial, original, pending, onSubmit, onCancel }: {
  title: string; initial: Draft; original?: NotifyChannel; pending: boolean; onSubmit: (d: Draft) => void; onCancel?: () => void;
}) {
  // initial 只在挂载时读取；编辑期间的列表刷新不覆盖草稿。
  const [draft, setDraft] = useState(initial);
  const [problem, setProblem] = useState<string | null>(null);
  // 行号随删除前移，key 必须跟随行本身而不是位置，焦点才不会落到错位的节点上。
  const nextHeaderId = useRef(0);
  // hub 只在种类未变时用已存凭据补全空值；新建或换种类时凭据必须重新填写。
  const keeps = original !== undefined && original.kind === draft.kind;
  const saved = keeps ? original?.webhook?.headerNames ?? [] : [];
  const set = (patch: Partial<Draft>) => setDraft({ ...draft, ...patch });
  const setHeader = (i: number, patch: Partial<HeaderRow>) => set({ headers: draft.headers.map((h, j) => (j === i ? { ...h, ...patch } : h)) });
  const addHeader = () => {
    nextHeaderId.current += 1;
    set({ headers: [...draft.headers, { id: nextHeaderId.current, name: "", value: "" }] });
  };
  const toggleRemove = (name: string) => {
    const next = new Set(draft.removeHeaders);
    if (next.has(name)) next.delete(name); else next.add(name);
    set({ removeHeaders: next });
  };
  const handle = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!e.currentTarget.checkValidity()) return;
    const dup = draft.kind === ChannelKind.WEBHOOK ? duplicateHeader(draft.headers) : undefined;
    setProblem(dup === undefined ? null : `请求头 ${dup} 填写了两次`);
    if (dup === undefined) onSubmit(draft);
  };
  return (
    <form className="card edit-form" aria-label={title} onSubmit={handle}>
      <div className="row">
        <label>名称<input required value={draft.name} onChange={(e) => set({ name: e.target.value })} /></label>
        <label>类型
          <select value={draft.kind} onChange={(e) => set({ kind: Number(e.target.value) as ChannelKind })}>
            {CHANNEL_KINDS.map(({ value, label }) => <option key={value} value={value}>{label}</option>)}
          </select>
        </label>
      </div>
      {draft.kind === ChannelKind.TELEGRAM ? (
        <div className="row">
          <label>Bot token<input type="password" autoComplete="new-password" required={!keeps} placeholder={keeps ? "已保存，留空保持不变" : ""}
            value={draft.botToken} onChange={(e) => set({ botToken: e.target.value })} /></label>
          <label>Chat ID<input required value={draft.chatId} onChange={(e) => set({ chatId: e.target.value })} /></label>
        </div>
      ) : (
        <>
          <div className="row">
            <label>URL<input type="url" autoComplete="off" required={!keeps}
              placeholder={keeps ? `已保存 ${original?.webhook?.urlHost ?? ""}，留空保持不变` : "https://"}
              value={draft.url} onChange={(e) => set({ url: e.target.value })} /></label>
            <label>方法
              <select value={draft.method} onChange={(e) => set({ method: e.target.value })}>
                {METHODS.map((m) => <option key={m} value={m}>{m}</option>)}
              </select>
            </label>
          </div>
          {saved.length > 0 && (
            <fieldset className="picks">
              <legend>已保存的请求头（值不回显）</legend>
              {saved.map((name) => (
                <label key={name}><input type="checkbox" checked={draft.removeHeaders.has(name)} onChange={() => toggleRemove(name)} />删除 {name}</label>
              ))}
            </fieldset>
          )}
          {draft.headers.map((h, i) => (
            <div className="row" role="group" aria-label={`请求头 ${i + 1}`} key={h.id}>
              <label>请求头名<input required value={h.name} onChange={(e) => setHeader(i, { name: e.target.value })} /></label>
              <label>请求头值<input type="password" autoComplete="new-password" value={h.value} onChange={(e) => setHeader(i, { value: e.target.value })} /></label>
              <button type="button" className="link" aria-label={`移除请求头 ${i + 1}`} onClick={() => set({ headers: draft.headers.filter((_, j) => j !== i) })}>移除</button>
            </div>
          ))}
          <button type="button" className="link" onClick={addHeader}>添加请求头</button>
          <label>请求体模板<textarea value={draft.bodyTemplate} placeholder="留空使用默认 JSON 模板" onChange={(e) => set({ bodyTemplate: e.target.value })} /></label>
        </>
      )}
      {problem && <p role="alert" className="error">{problem}</p>}
      <div className="row">
        <button type="submit" disabled={pending}>{onCancel ? "保存" : "创建"}</button>
        {onCancel && <button type="button" className="link" onClick={onCancel}>取消</button>}
      </div>
    </form>
  );
}

function ChannelRow({ channel: c, saving, deleting, testing, onSave, onTest, onDelete }: {
  channel: NotifyChannel; saving: boolean; deleting: boolean; testing: boolean;
  onSave: (d: Draft, onSuccess: () => void) => void; onTest: () => void; onDelete: () => void;
}) {
  const [editing, setEditing] = useState(false);
  if (editing) {
    return (
      <tr><td colSpan={5}>
        <ChannelForm title={`编辑 ${c.name}`} initial={draftOf(c)} original={c} pending={saving}
          onSubmit={(d) => onSave(d, () => setEditing(false))} onCancel={() => setEditing(false)} />
      </td></tr>
    );
  }
  return (
    <tr>
      <td>{c.name}</td>
      <td>{labelOf(CHANNEL_KINDS, c.kind)}</td>
      <td>{channelTarget(c)}</td>
      <td className="muted">{new Date(Number(c.createdAt) * 1000).toLocaleDateString()}</td>
      <td>
        <button type="button" className="link" aria-label={`编辑 ${c.name}`} onClick={() => setEditing(true)}>编辑</button>{" "}
        <button type="button" className="link" aria-label={`发送测试 ${c.name}`} disabled={testing} onClick={onTest}>发送测试</button>{" "}
        <ConfirmDelete label={`删除 ${c.name}`} confirm={`确认删除 ${c.name}`} pending={deleting} onDelete={onDelete} />
      </td>
    </tr>
  );
}
