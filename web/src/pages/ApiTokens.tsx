import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { errorText } from "../api/auth";
import { errorBanner, queryGate } from "../api/queryGate";
import { useLatestError } from "../api/useLatestError";
import { ConfirmDelete } from "../components/ConfirmDelete";
import { Secret } from "../components/Secret";
import { AdminService } from "../gen/probe/v1/admin_pb";
import { withId } from "../lib/ids";

// 卡片内容以 hub 下发的为准：与 hub 同版本，面板不另存一份。
function download(filename: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "text/markdown" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.click();
  URL.revokeObjectURL(url);
}

export function ApiTokens() {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  // id 记下明文属于哪一行：吊销的若正是这一行，卡片必须一起消失。
  const [secret, setSecret] = useState<{ id: bigint; label: string; value: string } | null>(null);
  const { error, mutationOptions } = useLatestError();
  const list = useQuery(AdminService.method.listApiTokens, {});
  const refresh = () => qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listApiTokens, cardinality: "finite" }) });
  const create = useMutation(AdminService.method.createApiToken, {
    ...mutationOptions,
    onSuccess: (r) => {
      const tok = r.apiToken;
      if (tok) setSecret({ id: tok.id, label: `API token ${withId(tok.name, tok.id)}`, value: r.token });
      setName("");
      return refresh();
    },
  });
  const remove = useMutation(AdminService.method.deleteApiToken, {
    ...mutationOptions,
    onSuccess: (_r, req) => {
      setSecret((cur) => (cur?.id === req.id ? null : cur));
      return refresh();
    },
  });
  const reference = useMutation(AdminService.method.getApiReference, { ...mutationOptions, onSuccess: (r) => download("SKILL.md", r.guide) });
  const gate = queryGate(list);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!e.currentTarget.checkValidity()) return;
    create.mutate({ name: name.trim() });
  };
  return (
    <section>
      {gate.banner}
      <h1>API token</h1>
      <p className="muted">
        只读凭据，供 agent 与脚本调用：以 <code>Authorization: Bearer &lt;token&gt;</code> 调用只读方法，写操作仍需在面板上完成。
        把入口卡片放进 agent 的 skills 目录，并设置 <code>PROBE_HUB={window.location.origin}</code> 与 <code>PROBE_TOKEN</code>。
      </p>
      <p>
        <button type="button" onClick={() => reference.mutate({})} disabled={reference.isPending}>下载入口卡片</button>
      </p>
      <form className="card edit-form" aria-label="新建 API token" onSubmit={submit}>
        <div className="row">
          <label>名称<input required maxLength={64} value={name} onChange={(e) => setName(e.target.value)} /></label>
          <button type="submit" disabled={create.isPending}>创建</button>
        </div>
      </form>
      {secret && <Secret label={secret.label} value={secret.value} />}
      {error != null && <p role="alert" className="error">{errorText(error)}</p>}
      <div className="table-scroll" role="region" aria-label="API token 管理" tabIndex={0}>
        <table className="nodes">
          <thead><tr><th>名称</th><th>创建于</th><th>最后使用</th><th>操作</th></tr></thead>
          <tbody>
            {gate.data.tokens.map((t) => (
              <tr key={String(t.id)}>
                <td>{t.name}</td>
                <td className="muted">{new Date(Number(t.createdAt) * 1000).toLocaleDateString()}</td>
                <td className="muted">{t.lastUsedAt == null ? "从未使用" : new Date(Number(t.lastUsedAt) * 1000).toLocaleString()}</td>
                <td>
                  <ConfirmDelete label={`吊销 ${withId(t.name, t.id)}`} confirm={`确认吊销 ${withId(t.name, t.id)}`} verb="吊销"
                    note="用它的请求立即失效" pending={remove.isPending} onDelete={() => remove.mutate({ id: t.id })} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {gate.data.tokens.length === 0 && <p className="muted">还没有 API token。</p>}
    </section>
  );
}
