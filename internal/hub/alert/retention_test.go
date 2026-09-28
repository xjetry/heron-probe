package alert

import (
	"testing"

	"github.com/xjetry/probe/internal/hub/store"
)

func TestAlertRetentionCoversFullQueueDelivery(t *testing.T) {
	worst := QueueCap * (store.MaxDeliveryAttempts*NotifyTimeout + DeliveryRetryWait())
	if store.MinRetentionAlertEvents <= worst {
		t.Fatalf("minimum retention %v must exceed full queue delivery %v", store.MinRetentionAlertEvents, worst)
	}
}
