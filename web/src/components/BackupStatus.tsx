import { useQuery } from "@connectrpc/connect-query";
import { errorBanner, queryGate } from "../api/queryGate";
import { AdminService, type BackupLayerStatus } from "../gen/probe/v1/admin_pb";

const at = (unix: bigint) => new Date(Number(unix) * 1000).toLocaleString();

// 故障按后端给的原样显示，不按 enabled 过滤：停用后后端已不再报旧故障（GetBackupStatusResponse.enabled），
// 而设置读不出时 enabled 同样为 false，配置层的 settings 故障仍须可见。
function Layer({ name, status }: { name: string; status: BackupLayerStatus | undefined }) {
  return <article className="card" aria-label={name}>
    <h3>{name}</h3>
    <p>上次成功：{status?.lastSuccessAt === undefined ? "从未成功" : at(status.lastSuccessAt)}</p>
    {status?.failure
      ? <p className="error">当前故障：{status.failure.category}{status.failure.statusCode ? `（HTTP ${status.failure.statusCode}）` : ""}；自 {at(status.failure.sinceAt)} 起</p>
      : <p className="muted">无当前故障</p>}
  </article>;
}

export function BackupStatus() {
  const status = useQuery(AdminService.method.getBackupStatus, {}, { refetchInterval: 5000 });
  const gate = queryGate(status);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  return <section aria-label="备份状态">
    {gate.banner}
    <h2>备份状态</h2>
    <p>{gate.data.enabled ? "自动备份已启用" : "自动备份未启用"}</p>
    <Layer name="配置与凭据" status={gate.data.config} />
    <Layer name="指标与探测历史" status={gate.data.metrics} />
    <p className="muted">配置层连续故障只在首次失败与恢复时通知，未恢复标记跨 hub 重启保留；重启后故障类别重新观察。指标层故障不通知，重启后重新观察。停用备份即结束两层的故障跟踪，配置层已通知的故障以一条停用事件收尾。</p>
  </section>;
}
