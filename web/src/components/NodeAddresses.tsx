import { AddressSource, type NodeAddress, type NodeNetwork } from "../gen/heron/v1/admin_pb";
import { AddressDetectionState, type AddressDetection, type NetworkInfo } from "../gen/heron/v1/types_pb";
import { CopyableText } from "./CopyableText";
import { dateTime } from "../lib/format";
import { Timestamp } from "./Timestamp";

// 显示值（Node.network）没有地址时的文案。DISABLED 出现在显示值里只有一种情形：手填已清空（来源不是手填），agent 还
// 没收到恢复探测的应答，所以写"等待探测"；agent 按手填停用时显示值是手填地址本身。
const stateLabel = (d: NodeAddress | undefined): string => d?.state === AddressDetectionState.UNSUPPORTED ? "不支持" : d?.state === AddressDetectionState.FAILED ? "探测失败" : d?.state === AddressDetectionState.DISABLED ? "等待探测" : "等待上报";

// 一行摘要（节点详情页头）：有地址写地址，否则与地址格同一套文案。
export const addressText = (d: NodeAddress | undefined): string => d?.state === AddressDetectionState.AVAILABLE ? d.address : d?.source === AddressSource.UNSPECIFIED || d === undefined ? "—" : stateLabel(d);

// agent 原报（facts.network）的文案：手填时在详细视图里对照显示，看得出 agent 是否已按手填停用该族探测。
const reportedText = (r: AddressDetection | undefined): string => r?.state === AddressDetectionState.AVAILABLE ? `探测到 ${r.address}` : r?.state === AddressDetectionState.UNSUPPORTED ? "不支持" : r?.state === AddressDetectionState.FAILED ? "探测失败" : r?.state === AddressDetectionState.DISABLED ? "已停用探测" : "尚未上报";

function Address({ family, display, reported, detailed }: { family: string; display?: NodeAddress; reported?: AddressDetection; detailed: boolean }) {
  const available = display?.state === AddressDetectionState.AVAILABLE && !!display.address;
  const manual = display?.source === AddressSource.MANUAL;
  const checked = reported && reported.checkedAt > 0n ? reported.checkedAt : undefined;
  const title = manual ? `管理员手填；agent：${reportedText(reported)}${checked ? `（${dateTime(checked)}）` : ""}`
    : checked ? `最近探测：${dateTime(checked)}${display?.state === AddressDetectionState.UNSUPPORTED ? "；无可用接口地址或路由" : ""}` : "需要支持双栈探测的 agent 上报结果";
  return <div className="node-address" aria-label={family} title={title}>
    <span className="address-family">{family}</span>
    {available ? <CopyableText compact label={`${family} 地址`} value={display.address} copyLabel={`复制 ${family} ${display.address}`} /> : <span className={`address-state${display?.state === AddressDetectionState.FAILED ? " failed" : ""}`}>{stateLabel(display)}</span>}
    {manual && <span className="address-source">手填</span>}
    {detailed && manual && <span className="address-state">agent：{reportedText(reported)}</span>}
    {detailed && checked && <Timestamp at={checked} />}
  </div>;
}

// network 是 hub 算好的显示值（手填优先，否则 agent 探测）；reported 是 agent 原报，只用于最近探测时间与手填时的对照。
export function NodeAddresses({ network, reported, detailed = false }: { network?: NodeNetwork; reported?: NetworkInfo; detailed?: boolean }) {
  return <div className="node-addresses">
    <Address family="IPv4" display={network?.ipv4} reported={reported?.ipv4} detailed={detailed} />
    <Address family="IPv6" display={network?.ipv6} reported={reported?.ipv6} detailed={detailed} />
  </div>;
}
