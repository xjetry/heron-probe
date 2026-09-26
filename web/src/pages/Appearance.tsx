import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type ChangeEvent, type FormEvent, useState } from "react";
import { errorText } from "../api/auth";
import { errorBanner, queryGate } from "../api/queryGate";
import { AdminService, type Settings } from "../gen/probe/v1/admin_pb";
import { LOGO_TYPES, MAX_TITLE_CHARS, THEMES, sizeProblems, type Theme } from "../lib/appearance";
import { BUILT_IN_ACCENT } from "../lib/palette";
import { DEFAULT_TITLE } from "../public/site";

const THEME_LABELS: Record<Theme, string> = { auto: "跟随访客的系统设置", light: "浅色", dark: "深色" };

type Draft = { title: string; theme: string; accentColor: string; logo: string; customCss: string };

const toDraft = (s: Settings | undefined): Draft => ({
  title: s?.title ?? "", theme: s?.theme || "auto", accentColor: s?.accentColor ?? "", logo: s?.logo ?? "", customCss: s?.customCss ?? "",
});

// 公开页的外观：UpdateSettings 整体替换五项，表单因此总是提交全部字段。
//
// 保存成功时 onSuccess 用 hub 的回显替换草稿；它不判断"是不是最新一次"，靠的是"有未结请求"与"草稿还能被改"互斥。
// 草稿的改动来自两处：用户改字段（同步），与读 logo 文件的回调（异步，读完才改）。互斥由两处承载：
//   - 保存进行中，fieldset 的 disabled={update.isPending} 禁用整个表单，含文件输入：保存期间既改不了字段，也开始不了读取；
//   - 读取进行中，文件输入的 disabled={reading} 让至多一个读者在飞，reading 因此恰好等于"有读者在飞"；
//     submit 守卫（!reading）与保存按钮的禁用拒绝在这时保存。
// 所以回显覆盖的总是这次提交自己送出的内容。reading 若不对应唯一的读者（文件输入在读取中仍可用），先读完的那个
// 把它置回 false，保存得以发出，后读完的在保存进行中改草稿，迟到的回显再把它改回去并误报"已保存"。
export function Appearance() {
  const qc = useQueryClient();
  const settings = useQuery(AdminService.method.getSettings, {});
  // draft 为空时表单显示 hub 的当前值；保存成功后改为 hub 回显的实际保存值（标题已清洗、主色已转小写）。
  const [draft, setDraft] = useState<Draft | null>(null);
  const [saved, setSaved] = useState(false);
  const [fileError, setFileError] = useState<string | null>(null);
  const [reading, setReading] = useState(false);
  const update = useMutation(AdminService.method.updateSettings, {
    onSuccess: (r) => {
      setDraft(toDraft(r.settings));
      setSaved(true);
      return qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.getSettings, cardinality: "finite" }) });
    },
  });
  const gate = queryGate(settings);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const form = draft ?? toDraft(gate.data.settings);
  // 用户侧对草稿的改动都经 edit：按最新的草稿合并（读 logo 的回调在读完时才调用它，选文件时的快照可能已过时），
  // 并清掉上一次保存的"已保存"与错误——改了草稿，它们就不再描述当前内容。保存成功的回显由 onSuccess
  // 直接替换草稿，那是 hub 已保存的值，"已保存"正是要留给它显示的。
  const edit = (patch: Partial<Draft>) => {
    setDraft((d) => ({ ...(d ?? toDraft(gate.data.settings)), ...patch }));
    setSaved(false);
    update.reset();
  };
  const problems = sizeProblems(form.title, form.logo, form.customCss);
  const pickLogo = (e: ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = "";
    if (!file) return;
    const reader = new FileReader();
    reader.onload = () => {
      setReading(false);
      setFileError(null);
      edit({ logo: String(reader.result) });
    };
    reader.onerror = () => { setReading(false); setFileError(`读取 ${file.name} 失败：${String(reader.error)}`); };
    setReading(true);
    reader.readAsDataURL(file);
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (problems.length === 0 && !reading && !update.isPending) update.mutate({ settings: form });
  };
  return (
    <section>
      {gate.banner}
      <h1>外观</h1>
      <p className="muted">
        公开页（站点根路径 /）的标题、明暗、主色、logo 与自定义 CSS。保存后，访客刷新公开页才看到新外观；浏览器还可能再用最多 5 分钟的缓存。
        要改页面结构，用 hub 的 --public-dir 换掉整个公开页。
      </p>
      <form className="card edit-form" aria-label="公开页外观" onSubmit={submit}>
        <fieldset className="bare" disabled={update.isPending}>
          <label>
            标题
            <input value={form.title} placeholder={DEFAULT_TITLE} onChange={(e) => edit({ title: e.target.value })} />
          </label>
          <p className="muted">最多 {MAX_TITLE_CHARS} 个字符；留空用「{DEFAULT_TITLE}」。</p>
          <label>
            明暗
            <select value={form.theme} onChange={(e) => edit({ theme: e.target.value })}>
              {THEMES.map((t) => <option key={t} value={t}>{THEME_LABELS[t]}</option>)}
            </select>
          </label>
          <div className="row">
            <label>
              主色
              <input value={form.accentColor} placeholder="#rrggbb，留空用内置配色" onChange={(e) => edit({ accentColor: e.target.value })} />
            </label>
            {/* 取色器必须有值，主色为空时显示内置浅色主题的主色。它只在用户选了与显示值不同的颜色时才进草稿：
                React 的 onChange 只在输入的值与渲染的值不同时触发，打开取色器又原样关上不会把兜底色存成设置。 */}
            <label>
              取色
              <input type="color" value={form.accentColor || BUILT_IN_ACCENT} onChange={(e) => edit({ accentColor: e.target.value })} />
            </label>
            <button type="button" className="link" onClick={() => edit({ accentColor: "" })} disabled={form.accentColor === ""}>用内置配色</button>
          </div>
          <div className="row">
            <label>
              logo
              <input type="file" accept={LOGO_TYPES.join(",")} onChange={pickLogo} disabled={reading} />
            </label>
            {form.logo && <img src={form.logo} alt="logo 预览" className="logo-preview" />}
            <button type="button" className="link" onClick={() => edit({ logo: "" })} disabled={form.logo === ""}>移除 logo</button>
          </div>
          <label>
            自定义 CSS
            <textarea value={form.customCss} onChange={(e) => edit({ customCss: e.target.value })} spellCheck={false} />
          </label>
          <p className="muted">排在公开页内置样式之后。只接受 CSS，不能含 &lt;/。</p>
          {fileError && <p role="alert" className="error">{fileError}</p>}
          {problems.map((p) => <p key={p} role="alert" className="error">{p}</p>)}
          {update.error != null && <p role="alert" className="error">{errorText(update.error)}</p>}
          {saved && <p role="status">已保存。</p>}
          <button type="submit" disabled={reading || problems.length > 0}>保存</button>
        </fieldset>
      </form>
    </section>
  );
}
