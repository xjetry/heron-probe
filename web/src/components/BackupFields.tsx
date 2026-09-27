import { useQuery } from "@connectrpc/connect-query";
import { useState } from "react";
import { errorBanner } from "../api/queryGate";
import { AdminService, type BackupSettings } from "../gen/probe/v1/admin_pb";

// hasSecret 只来自 hub 的读侧，表单不改它；secret 只写，不从读侧复制进草稿，成功保存后随草稿重建而清空。
export type BackupDraft = {
  endpoint: string; bucket: string; region: string; accessKey: string; prefix: string;
  configIntervalS: number; metricsIntervalS: number; configKeep: number; metricsKeep: number;
  channelIds: bigint[]; secret?: string; hasSecret: boolean;
};

export const backupDraft = (b?: BackupSettings): BackupDraft => ({
  endpoint: b?.endpoint ?? "", bucket: b?.bucket ?? "", region: b?.region || "auto", accessKey: b?.accessKey ?? "", prefix: b?.prefix ?? "",
  configIntervalS: b?.configIntervalS ?? 300, metricsIntervalS: b?.metricsIntervalS ?? 86400,
  configKeep: b?.configKeep ?? 48, metricsKeep: b?.metricsKeep ?? 14, channelIds: b?.notify?.channelIds ?? [], secret: undefined, hasSecret: b?.hasSecret ?? false,
});

// 提交的 backup：notify 恒给出（表单显示的就是完整的渠道选择），secret 留空即缺席、保留 hub 已存的值；hasSecret 不提交。
export const backupRequest = ({ channelIds, hasSecret: _, ...rest }: BackupDraft) => ({ ...rest, notify: { channelIds } });

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
      <label>Secret<input type="password" autoComplete="new-password" value={form.secret ?? ""} maxLength={4096} placeholder="只写不读；留空保留已存值" aria-describedby="backup-secret-state" onChange={(e) => edit({ secret: e.target.value || undefined })} /></label>
      <p className="muted" id="backup-secret-state">{form.hasSecret ? "Secret 已保存" : "Secret 未设置"}</p>
      <label>对象键前缀<input value={form.prefix} maxLength={512} placeholder="例如 hub；不以 / 开头或结尾" onChange={(e) => edit({ prefix: e.target.value })} /></label>
      <div className="row">
        <label>配置周期（秒）<input type="number" required min={60} max={86400} step={1} value={form.configIntervalS} onChange={(e) => edit({ configIntervalS: Number(e.target.value) })} /></label>
        <label>指标周期（秒）<input type="number" required min={3600} max={604800} step={1} value={form.metricsIntervalS} onChange={(e) => edit({ metricsIntervalS: Number(e.target.value) })} /></label>
      </div>
      <div className="row">
        <label>配置保留份数<input type="number" required min={1} max={1000} step={1} value={form.configKeep} onChange={(e) => edit({ configKeep: Number(e.target.value) })} /></label>
        <label>指标保留份数<input type="number" required min={1} max={1000} step={1} value={form.metricsKeep} onChange={(e) => edit({ metricsKeep: Number(e.target.value) })} /></label>
      </div>
      <p className="muted">配置周期 60 至 86400 秒，指标周期 3600 至 604800 秒；保留份数均为 1 至 1000。回到默认值请填写 300、86400、48、14。</p>
      <fieldset className="picks">
        <legend>备份通知渠道</legend>
        {open && errorBanner(channels.error)}
        {channels.data?.channels.map((c) => <label key={String(c.id)}><input type="checkbox" checked={form.channelIds.includes(c.id)} onChange={(e) => edit({ channelIds: e.target.checked ? [...form.channelIds, c.id] : form.channelIds.filter((id) => id !== c.id) })} />{c.name}</label>)}
      </fieldset>
    </details>
  );
}
