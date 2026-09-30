import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { errorText } from "../api/auth";
import { errorBanner, queryGate } from "../api/queryGate";
import { useLatestError } from "../api/useLatestError";
import { ConfirmDelete } from "../components/ConfirmDelete";
import { Secret } from "../components/Secret";
import { Picks } from "../components/Picks";
import { AdminService, TokenPermission } from "../gen/heron/v1/admin_pb";
import { withId } from "../lib/ids";

const permissionChoices = [
  [TokenPermission.CONFIGURE, "监控配置"],
  [TokenPermission.CREATE, "创建节点"],
  [TokenPermission.REGISTER, "注册入口"],
  [TokenPermission.ROTATE, "轮换节点凭据"],
  [TokenPermission.DELETE, "删除节点及历史"],
  [TokenPermission.UPDATE, "官方节点更新"],
] as const;

function Operations({ ownerId }: { ownerId: bigint }) {
  const list = useQuery(AdminService.method.listOperations, { ownerId, limit: 100 });
  const gate = queryGate(list);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  return <section className="card" aria-label="操作记录">
    {gate.banner}
    <h2>最近操作</h2>
    {gate.data.operations.length === 0 && <p className="muted">暂无已提交操作。</p>}
    {gate.data.operations.map((o) => <details key={o.id}>
      <summary>{o.action} #{String(o.resourceId)} · {new Date(Number(o.committedAt) * 1000).toLocaleString()}</summary>
      <p>请求 ID：<code>{o.requestId}</code></p>
      <p>操作 ID：<code>{o.id}</code></p>
      <p>修改前</p><pre style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>{o.beforeJson || "详细记录已过保留期"}</pre>
      <p>修改后</p><pre style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>{o.afterJson || "详细记录已过保留期"}</pre>
    </details>)}
  </section>;
}

// 卡片内容以 hub 下发的为准：与 hub 同版本，面板不另存一份。
function download(filename: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "text/markdown" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.click();
  // 下载何时读取 URL 由浏览器决定，不在同一轮释放。
  setTimeout(() => URL.revokeObjectURL(url), 60_000);
}

export function ApiTokens() {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [permissions, setPermissions] = useState<TokenPermission[]>([]);
  const [allNodes, setAllNodes] = useState(true);
  const [selected, setSelected] = useState<Set<bigint>>(new Set());
  const [auditOwner, setAuditOwner] = useState<bigint | null>(null);
  const nodes = useQuery(AdminService.method.listNodes, {}, { enabled: !allNodes });
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
      setPermissions([]);
      setAllNodes(true);
      setSelected(new Set());
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
    if (!allNodes && !nodes.data) return;
    create.mutate({ name: name.trim(), grant: { permissions, allNodes, nodeIds: allNodes ? [] : (nodes.data?.nodes ?? []).filter((n) => selected.has(n.id)).map((n) => n.id) } });
  };
  return (
    <section>
      {gate.banner}
      <h1>API token</h1>
      <p className="muted">
        供 agent 与脚本使用的预授权凭据：以 <code>Authorization: Bearer &lt;token&gt;</code> 调用 API。默认只读，勾选的操作可在授权节点范围内自主执行，无需逐次审批。
        保存为 agent 的 skills 目录下的 heron-hub/SKILL.md（Claude Code 为 ~/.claude/skills/heron-hub/SKILL.md），并设置 <code>HERON_HUB={window.location.origin}</code> 与 <code>HERON_TOKEN</code>。
      </p>
      <p>
        <button type="button" onClick={() => reference.mutate({})} disabled={reference.isPending}>下载入口卡片</button>
      </p>
      <form className="card edit-form" aria-label="新建 API token" onSubmit={submit}>
        <div className="row">
          <label>名称<input required maxLength={64} value={name} onChange={(e) => setName(e.target.value)} /></label>
          <button type="submit" disabled={create.isPending || (!allNodes && !nodes.data)}>创建</button>
        </div>
        <fieldset className="picks"><legend>允许的写操作</legend>
          {permissionChoices.map(([value, label]) => <label key={value}><input type="checkbox" checked={permissions.includes(value)} onChange={(e) => setPermissions((current) => e.target.checked ? [...current, value] : current.filter((p) => p !== value))} />{label}</label>)}
        </fieldset>
        <label>节点范围<select value={allNodes ? "all" : "selected"} onChange={(e) => setAllNodes(e.target.value === "all")}>
          <option value="all">全站（包含未来节点）</option><option value="selected">指定节点</option>
        </select></label>
        {!allNodes && <>
          {nodes.error && errorBanner(nodes.error)}
          <Picks legend="授权节点" items={nodes.data?.nodes ?? []} selected={selected} onChange={setSelected} />
          <p className="muted">未选择节点表示不能访问现有节点。此凭据创建或注册的节点自动纳入范围；标签不改变授权。</p>
        </>}
        <p className="muted">写入支持预览、版本检查和安全重试。不能更新 Hub、执行远程命令、管理 API 凭据或修改通知渠道密钥。</p>
      </form>
      {secret && <Secret label={secret.label} value={secret.value} />}
      {error != null && <p role="alert" className="error">{errorText(error)}</p>}
      <div className="table-scroll" role="region" aria-label="API token 管理" tabIndex={0}>
        <table className="nodes">
          <thead><tr><th>名称</th><th>权限 / 范围</th><th>创建于</th><th>最后使用</th><th>操作</th></tr></thead>
          <tbody>
            {gate.data.tokens.map((t) => (
              <tr key={String(t.id)}>
                <td>{t.name}</td>
                <td>{t.grant?.permissions.length ? t.grant.permissions.map((p) => permissionChoices.find(([v]) => v === p)?.[1] ?? "未知权限").join("、") : "只读"}<br />
                  {t.grant?.allNodes !== false ? "全站" : t.grant.nodeIds.length ? t.grant.nodeIds.map((id) => `#${id}`).join("、") : "无现有节点"}</td>
                <td className="muted">{new Date(Number(t.createdAt) * 1000).toLocaleDateString()}</td>
                <td className="muted">{t.lastUsedAt == null ? "从未使用" : new Date(Number(t.lastUsedAt) * 1000).toLocaleString()}</td>
                <td>
                  <button type="button" onClick={() => setAuditOwner(t.id)}>查看 {t.name} 操作记录</button>
                  <ConfirmDelete label={`吊销 ${withId(t.name, t.id)}`} confirm={`确认吊销 ${withId(t.name, t.id)}`} verb="吊销"
                    note="用它的请求立即失效" pending={remove.isPending} onDelete={() => remove.mutate({ id: t.id })} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {gate.data.tokens.length === 0 && <p className="muted">还没有 API token。</p>}
      {auditOwner !== null && <Operations ownerId={auditOwner} />}
    </section>
  );
}
