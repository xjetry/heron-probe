import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useRef, useState } from "react";
import { errorText } from "../api/auth";
import { errorBanner, queryGate } from "../api/queryGate";
import { useLatestError } from "../api/useLatestError";
import { ConfirmDelete } from "../components/ConfirmDelete";
import { AdminService, type Theme } from "../gen/probe/v1/admin_pb";
import { toBase64 } from "../lib/base64";

// hub 没有配 --theme-origin 时五个主题方法一律 FailedPrecondition：这是配置状态而不是故障，页面给出说明，不当作错误横幅。
function isUnconfigured(err: unknown): boolean {
  return err instanceof ConnectError && err.code === Code.FailedPrecondition;
}

// 预览图经 GetThemePreview 取出后以 data: URL 显示：面板的 CSP 的 img-src 只放行 'self' 与 data:，blob: 不在其列。
function Preview({ theme }: { theme: Theme }) {
  const preview = useQuery(AdminService.method.getThemePreview, { id: theme.id }, { enabled: theme.hasPreview });
  if (!theme.hasPreview) return <span className="muted">无</span>;
  if (preview.error != null) return <span className="error">{errorText(preview.error)}</span>;
  if (!preview.data) return <span className="muted">加载中…</span>;
  return <img className="theme-preview" alt={`${theme.name} 预览图`} src={`data:${preview.data.contentType};base64,${toBase64(preview.data.content)}`} />;
}

export function Themes() {
  const qc = useQueryClient();
  const list = useQuery(AdminService.method.listThemes, {});
  const backup = useQuery(AdminService.method.getBackupStatus, {}, { refetchInterval: 5000 });
  const fileInput = useRef<HTMLInputElement>(null);
  const [file, setFile] = useState<File | null>(null);
  // 空串表示按包里的 id 安装（已装同 id 即替换）；非空即 expect_id：包的 id 必须是它且它必须已安装。
  const [expectId, setExpectId] = useState("");
  const [reading, setReading] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const { error, mutationOptions } = useLatestError();
  // 替换同一个 id 会换掉预览图，而预览的查询键只有 id：变更成功后预览与列表一起失效。
  const refresh = () => Promise.all([
    qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listThemes, cardinality: "finite" }) }),
    qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.getThemePreview, cardinality: "finite" }) }),
    qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.getBackupStatus, cardinality: "finite" }) }),
  ]);
  const upload = useMutation(AdminService.method.uploadTheme, {
    ...mutationOptions,
    onSuccess: (r) => {
      setNotice(`已上传 ${r.theme?.name ?? ""}（${r.theme?.id ?? ""}）版本 ${r.theme?.version ?? ""}`);
      setFile(null);
      setExpectId("");
      if (fileInput.current) fileInput.current.value = "";
      return refresh();
    },
  });
  const enable = useMutation(AdminService.method.enableTheme, { ...mutationOptions, onSuccess: () => { setNotice(null); return refresh(); } });
  const remove = useMutation(AdminService.method.deleteTheme, { ...mutationOptions, onSuccess: () => { setNotice(null); return refresh(); } });
  if (isUnconfigured(list.error)) {
    return (
      <section>
        <h1>主题</h1>
        <div className="card" role="note" aria-label="主题未开启">
          <p>这个 hub 没有配置主题 origin，主题的上传与托管不开启。</p>
          <p className="muted">
            主题是第三方的前端代码，必须放在与面板不同的主机名上：与面板同源的主题脚本能带着来访管理员的会话调管理接口。
            把第二个主机名（例如 status.example.com）也指向 hub，并以 <code>probe-hub serve --theme-origin https://status.example.com</code> 启动。
            只换端口不算另一个主机名：cookie 不隔离端口，hub 按主机名分流，会把面板的请求一起分到主题那边。
          </p>
          <p className="muted">hub 的原文：{errorText(list.error)}</p>
        </div>
      </section>
    );
  }
  const gate = queryGate(list);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const { themes, themeOrigin, publicDir } = gate.data;
  const enabled = themes.find((t) => t.enabled);
  const busy = reading || upload.isPending;
  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!file || busy) return;
    setNotice(null);
    setReading(true);
    let bytes: Uint8Array;
    try {
      bytes = new Uint8Array(await file.arrayBuffer());
    } catch (err) {
      setNotice(`读取 ${file.name} 失败：${String(err)}`);
      return;
    } finally {
      setReading(false);
    }
    upload.mutate({ package: bytes, expectId });
  };
  return (
    <section>
      {gate.banner}
      <h1>主题</h1>
      {backup.error != null && errorBanner(backup.error)}
      {backup.data?.themesWithoutPackage.map((id) => <p key={id}>主题 {id} 未备份：请重新上传原包</p>)}
      <p className="muted">
        主题是只调公开接口（PublicService）的静态前端，托管在 <a href={themeOrigin} target="_blank" rel="noreferrer">{themeOrigin}</a>：
        {enabled ? <>那里现在是主题 {enabled.name}（{enabled.id}）。</> : <>现在没有启用的主题，那里是内置公开页。</>}
        包的布局、清单字段与上限见 hub 仓库的 docs/theme-guide.md。
      </p>
      {publicDir && (
        <p role="note" className="card">
          面板所在 origin 的公开页（{window.location.origin}/）由 --public-dir 的目录接管，与主题无关；启用主题只改变 {themeOrigin} 上的页面。
        </p>
      )}
      <form className="card edit-form" aria-label="上传主题" onSubmit={(e) => void submit(e)}>
        <div className="row">
          <label>主题包（zip，至多 8 MiB）
            <input ref={fileInput} type="file" accept=".zip,application/zip" disabled={busy}
              onChange={(e) => { setFile(e.target.files?.[0] ?? null); setNotice(null); upload.reset(); }} />
          </label>
          <label>用途
            <select value={expectId} disabled={busy} onChange={(e) => setExpectId(e.target.value)}>
              <option value="">安装（已装同 id 的主题即整体替换）</option>
              {themes.map((t) => <option key={t.id} value={t.id}>更新 {t.name}（{t.id}）：包的 id 必须是 {t.id}</option>)}
            </select>
          </label>
          <button type="submit" disabled={!file || busy}>{busy ? "上传中…" : "上传"}</button>
        </div>
        <p className="muted">上传须在 30 秒内传完（hub 的请求读取时限）：8 MiB 的包约需 3 Mbit/s 的上行。</p>
      </form>
      {notice != null && <p role="status">{notice}</p>}
      {error != null && <p role="alert" className="error">{errorText(error)}</p>}
      <div className="table-scroll" role="region" aria-label="主题管理" tabIndex={0}>
        <table className="nodes">
          <thead><tr><th>预览</th><th>名称</th><th>id</th><th>版本</th><th>上传于</th><th>状态</th><th>操作</th></tr></thead>
          <tbody>
            {themes.map((t) => (
              <tr key={t.id}>
                <td><Preview theme={t} /></td>
                <td>{t.name}</td>
                <td><code>{t.id}</code></td>
                <td>{t.version}</td>
                <td className="muted">{new Date(Number(t.uploadedAt) * 1000).toLocaleString()}</td>
                <td>{t.enabled ? "启用中" : <span className="muted">未启用</span>}</td>
                <td>
                  {t.enabled
                    ? <button type="button" className="link" aria-label={`停用 ${t.name}（${t.id}）`} disabled={enable.isPending} onClick={() => enable.mutate({ id: "" })}>停用</button>
                    : <button type="button" className="link" aria-label={`启用 ${t.name}（${t.id}）`} disabled={enable.isPending} onClick={() => enable.mutate({ id: t.id })}>启用</button>}{" "}
                  <ConfirmDelete label={`删除 ${t.name}（${t.id}）`} confirm={`确认删除 ${t.name}（${t.id}）`}
                    note={t.enabled ? `删除后 ${themeOrigin} 回落内置公开页` : undefined}
                    pending={remove.isPending} onDelete={() => remove.mutate({ id: t.id })} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {themes.length === 0 && <p className="muted">还没有主题。</p>}
    </section>
  );
}
