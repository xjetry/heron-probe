import type { WalFileObservation } from "../gen/heron/v1/admin_pb";
import { bytes } from "./format";

// WAL 文件观测的显示口径，与 WalFileObservation / GetStorageStatsResponse.wal 的注释一致：
// - wal 缺席：对端是还没有这一项的旧 hub。缺席是“未提供”，不是无文件，也不是 0 字节，整行不显示。
// - bytes：stat 成功时的实际文件长度。0 表示文件存在且长度为 0，不得显示成无文件。
// - absent 且值为 true：stat 得到 ENOENT，文案是“无 WAL 文件”，与旧 hub 的不显示不是同一句。
// - error：stat 的其他失败，大小未知；附上 hub 给的错误文本只说明原因，不得换成 0 或无文件。
// - 没有 result、absent 不是 true、或出现未识别的分支：显示“未知”，不补零。
// 长度只是 -wal 文件的字节数，不是未检查点的数据量；这里不附加阈值或危险判断。
export type WalObservationView =
  | { kind: "omitted" }
  | { kind: "bytes"; observedAt: bigint; text: string }
  | { kind: "absent"; observedAt: bigint; text: string }
  | { kind: "error"; observedAt: bigint; text: string }
  | { kind: "unknown"; observedAt: bigint; text: string };

export function walObservationView(wal: WalFileObservation | undefined): WalObservationView {
  if (!wal) return { kind: "omitted" };
  const observedAt = wal.observedAt;
  const result = wal.result;
  switch (result?.case) {
    case "bytes":
      return { kind: "bytes", observedAt, text: bytes(result.value) };
    case "absent":
      // 协议只把 true 定义为 ENOENT。false 不是“无文件”，也不是 0。
      if (result.value !== true) return { kind: "unknown", observedAt, text: "未知" };
      return { kind: "absent", observedAt, text: "无 WAL 文件" };
    case "error":
      return { kind: "error", observedAt, text: result.value ? `大小未知（${result.value}）` : "大小未知" };
    default:
      return { kind: "unknown", observedAt, text: "未知" };
  }
}
