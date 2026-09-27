import { useQuery } from "@connectrpc/connect-query";
import { useState } from "react";
import { errorBanner } from "../api/queryGate";
import { AdminService, type BackupSettings } from "../gen/probe/v1/admin_pb";

export type BackupDraft = Omit<BackupSettings, "$typeName" | "$unknown">;

// 凭据不从读侧复制进草稿；成功保存后重新建草稿，清除刚提交的 secret。
export const backupDraft = (b?: BackupSettings): BackupDraft => ({
  endpoint: b?.endpoint ?? "", bucket: b?.bucket ?? "", region: b?.region || "auto", accessKey: b?.accessKey ?? "", prefix: b?.prefix ?? "",
  configIntervalS: b?.configIntervalS ?? 300, metricsIntervalS: b?.metricsIntervalS ?? 86400,
  configKeep: b?.configKeep ?? 48, metricsKeep: b?.metricsKeep ?? 14, channels: b?.channels ?? [], secret: undefined,
});

export function BackupFields({ value, onChange }: { value?: BackupDraft; onChange: (value: BackupDraft) => void }) {
  const [open, setOpen] = useState(false);
  const channels = useQuery(AdminService.method.listNotifyChannels, {}, { enabled: open });
  const form = value ?? backupDraft();
  const edit = (patch: Partial<BackupDraft>) => onChange({ ...form, ...patch });
  return (
    <details open={open} onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>备份到 S3</summary>
      <p className="muted">目标使用 path-style，兼容 R2。Bucket 必须为私有；建议使用 HTTPS。清空 Endpoint 并保存可整体关闭备份。缺少 Bucket、Access key 或 Secret 时也不会启动备份。</p>
      <label>Endpoint<input type="url" value={form.endpoint} maxLength={2048} placeholder="https://account.r2.cloudflarestorage.com" onChange={(e) => edit({ endpoint: e.target.value })} /></label>
      <div className="row">
        <label>Bucket<input value={form.bucket} maxLength={63} onChange={(e) => edit({ bucket: e.target.value })} /></label>
        <label>区域<input value={form.region} maxLength={64} placeholder="auto" onChange={(e) => edit({ region: e.target.value })} /></label>
      </div>
      <label>Access key<input autoComplete="off" value={form.accessKey} maxLength={128} onChange={(e) => edit({ accessKey: e.target.value })} /></label>
      <label>Secret<input type="password" autoComplete="new-password" value={form.secret ?? ""} maxLength={4096} placeholder="只写不读；留空保留已存值" onChange={(e) => edit({ secret: e.target.value || undefined })} /></label>
      <label>对象键前缀<input value={form.prefix} onChange={(e) => edit({ prefix: e.target.value })} /></label>
      <div className="row">
        <label>配置周期（秒）<input type="number" required min={60} max={86400} step={1} value={form.configIntervalS ?? 300} onChange={(e) => edit({ configIntervalS: Number(e.target.value) })} /></label>
        <label>指标周期（秒）<input type="number" required min={3600} max={604800} step={1} value={form.metricsIntervalS ?? 86400} onChange={(e) => edit({ metricsIntervalS: Number(e.target.value) })} /></label>
      </div>
      <div className="row">
        <label>配置保留份数<input type="number" required min={1} max={1000} step={1} value={form.configKeep ?? 48} onChange={(e) => edit({ configKeep: Number(e.target.value) })} /></label>
        <label>指标保留份数<input type="number" required min={1} max={1000} step={1} value={form.metricsKeep ?? 14} onChange={(e) => edit({ metricsKeep: Number(e.target.value) })} /></label>
      </div>
      <p className="muted">配置周期 60 至 86400 秒，指标周期 3600 至 604800 秒；保留份数均为 1 至 1000。回到默认值请填写 300、86400、48、14。</p>
      <fieldset className="picks">
        <legend>备份通知渠道</legend>
        {open && errorBanner(channels.error)}
        {channels.data?.channels.map((c) => <label key={String(c.id)}><input type="checkbox" checked={form.channels.includes(c.id)} onChange={(e) => edit({ channels: e.target.checked ? [...form.channels, c.id] : form.channels.filter((id) => id !== c.id) })} />{c.name}</label>)}
      </fieldset>
    </details>
  );
}
