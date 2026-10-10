import type { PublicNetworkInfo } from "../gen/heron/v1/public_pb";
import { AddressDetectionState } from "../gen/heron/v1/types_pb";

// 公开页的双栈标记：只为确认有公网出口（AVAILABLE）的地址族画一个小标。AVAILABLE 由 hub 按显示值给出：agent 探测
// 确认，或管理员手填了该族的公网地址（PublicAddressDetection.state）；这里不区分两者。不支持、探测失败与还没探测都不画：
// 探测失败可能只是这一轮外部服务不通，画成"没有 IPv6"会误导访客；三者的区分只在管理端（NodeAddresses）显示。
// 两个族都没有确认时不渲染。外观与国家徽章相同（styles.css）。
export function AddressFamilies({ network }: { network: PublicNetworkInfo | undefined }) {
  const families = ([["IPv4", network?.ipv4], ["IPv6", network?.ipv6]] as const)
    .filter(([, detection]) => detection?.state === AddressDetectionState.AVAILABLE)
    .map(([family]) => family);
  if (families.length === 0) return null;
  return (
    <span className="address-families" role="group" aria-label="公网出口">
      {families.map((family) => <span key={family} className="address-family" title={`有 ${family} 公网出口`}>{family}</span>)}
    </span>
  );
}
