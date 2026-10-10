package agentwire

import (
	"fmt"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/netaddr"
)

// MaxDetectionTime 将持久化探测时间限制在 RFC 3339 的四位年份范围，所有管理消费者都可安全格式化。
const MaxDetectionTime int64 = 253402300799

// ValidateNetwork 保持状态、地址族与探测时间一致；旧 agent 省略信息不等于不支持。
func ValidateNetwork(n *heronv1.NetworkInfo) error {
	for i, result := range []*heronv1.AddressDetection{n.GetIpv4(), n.GetIpv6()} {
		if result == nil {
			continue
		}
		name := []string{"ipv4", "ipv6"}[i]
		if result.State == heronv1.AddressDetectionState_ADDRESS_DETECTION_STATE_UNSPECIFIED {
			if result.Address != "" || result.CheckedAt != 0 {
				return fmt.Errorf("network.%s: unknown state must have no address or timestamp", name)
			}
			continue
		}
		if result.CheckedAt <= 0 || result.CheckedAt > MaxDetectionTime {
			return fmt.Errorf("network.%s: checked_at must be between 1 and %d", name, MaxDetectionTime)
		}
		switch result.State {
		case heronv1.AddressDetectionState_ADDRESS_DETECTION_STATE_AVAILABLE:
			if _, valid := netaddr.ParsePublicFamily(result.Address, i == 0); !valid {
				return fmt.Errorf("network.%s: available address must be a public address of this family", name)
			}
		// DISABLED 是 agent 按 hub 的要求停用了该族探测：同样没有地址，checked_at 是停用生效的时刻。
		case heronv1.AddressDetectionState_ADDRESS_DETECTION_STATE_UNSUPPORTED, heronv1.AddressDetectionState_ADDRESS_DETECTION_STATE_FAILED,
			heronv1.AddressDetectionState_ADDRESS_DETECTION_STATE_DISABLED:
			if result.Address != "" {
				return fmt.Errorf("network.%s: unavailable state must have no address", name)
			}
		default:
			return fmt.Errorf("network.%s: invalid state", name)
		}
	}
	return nil
}
