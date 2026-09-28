package alert

import (
	"testing"
	"time"

	"github.com/xjetry/probe/internal/hub/store"
)

// 满队列（QueueCap 个批次、每批至多 MaxDeliveryAttempts 次请求）排空之前，任一时刻至少处于三种情形之一：
//   - worker 正在发送：每个请求至多占一个客户端超时；
//   - 有就绪批次但它的渠道节奏已满：渠道在 t 时刻已满，意味着 (t−rateWindow, t] 里至少有 RatePerMinute 个请求，
//     每个请求只落在 rateWindow 长的一段窗口里，所以一个渠道已满的总时长不超过它的请求数 × rateWindow ÷ 节奏，
//     节奏最小为 1，各渠道合计不超过全部请求数 × rateWindow；
//   - 全部未完成的批次都在等重试间隔：最后完成的那个批次此刻也在等，它自己的等待至多 (MaxDeliveryAttempts−1) 次，
//     每次至多 MaxRetryAfter（固定退避都短于它）。
//
// 等待不占用 worker（Queue 的 waiting），否则第三项要按批次累加，远超最短保留期。存储故障时的 worker 级退避没有
// 总量上界，不在其内。
func TestAlertRetentionCoversFullQueueDelivery(t *testing.T) {
	requests := QueueCap * store.MaxDeliveryAttempts
	worst := MaxRetryAfter*time.Duration(store.MaxDeliveryAttempts-1) + NewHTTPClient().Timeout*time.Duration(requests) + rateWindow*time.Duration(requests)
	if store.MinRetentionAlertEvents <= worst {
		t.Fatalf("minimum retention %v must exceed full queue delivery %v", store.MinRetentionAlertEvents, worst)
	}
	for _, d := range backoff {
		if d > MaxRetryAfter {
			t.Fatalf("fixed backoff %v exceeds MaxRetryAfter %v, the bound above assumes every retry wait is at most MaxRetryAfter", d, MaxRetryAfter)
		}
	}
}
