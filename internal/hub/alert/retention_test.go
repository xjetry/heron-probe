package alert

import (
	"testing"
	"time"

	"github.com/xjetry/probe/internal/hub/store"
)

func TestAlertRetentionCoversFullQueueDelivery(t *testing.T) {
	var retries time.Duration
	for _, delay := range backoff {
		retries += delay
	}
	worst := QueueCap * (store.MaxDeliveryAttempts*NewHTTPClient().Timeout + retries)
	if store.MinRetentionAlertEvents <= worst {
		t.Fatalf("minimum retention %v must exceed full queue delivery %v", store.MinRetentionAlertEvents, worst)
	}
}
