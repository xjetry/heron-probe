import { AddressDetectionState, type AddressDetection, type NetworkInfo } from "../gen/heron/v1/types_pb";
import { CopyableText } from "./CopyableText";

function Address({ family, detection, detailed }: { family: string; detection?: AddressDetection; detailed: boolean }) {
  const state = detection?.state;
  const available = state === AddressDetectionState.AVAILABLE && !!detection?.address;
  const label = state === AddressDetectionState.UNSUPPORTED ? "不支持" : state === AddressDetectionState.FAILED ? "探测失败" : "等待上报";
  const checked = detection && detection.checkedAt > 0n ? new Date(Number(detection.checkedAt) * 1000) : undefined;
  return <div className="node-address" aria-label={family} title={checked ? `最近探测：${checked.toLocaleString()}${state === AddressDetectionState.UNSUPPORTED ? "；无可用接口地址或路由" : ""}` : "需要支持双栈探测的 agent 上报结果"}>
    <span className="address-family">{family}</span>
    {available ? <CopyableText compact label={`${family} 地址`} value={detection.address} copyLabel={`复制 ${family} ${detection.address}`} /> : <span className={`address-state${state === AddressDetectionState.FAILED ? " failed" : ""}`}>{label}</span>}
    {detailed && checked && <time dateTime={checked.toISOString()}>{checked.toLocaleString()}</time>}
  </div>;
}

export function NodeAddresses({ network, detailed = false }: { network?: NetworkInfo; detailed?: boolean }) {
  return <div className="node-addresses"><Address family="IPv4" detection={network?.ipv4} detailed={detailed} /><Address family="IPv6" detection={network?.ipv6} detailed={detailed} /></div>;
}
