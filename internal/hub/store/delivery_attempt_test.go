package store

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestBeginDeliveryAttemptAtomicallyCapsStarts(t *testing.T) {
	s, ids, cs, _ := alertFixture(t)
	r := saveRule(t, s, AlertRule{Kind: KindOffline})
	ev := recordEvent(t, s, r.ID, ids[0], []int64{cs[0].ID})
	type result struct {
		d   Delivery
		err error
	}
	results := make(chan result, 20)
	var wg sync.WaitGroup
	for range cap(results) {
		wg.Go(func() { d, err := s.BeginDeliveryAttempt(t.Context(), ev.Deliveries[0].ID); results <- result{d, err} })
	}
	wg.Wait()
	close(results)
	starts := map[int]bool{}
	for got := range results {
		if got.err == nil {
			if starts[got.d.Attempts] || got.d.Attempts < 1 || got.d.Attempts > MaxDeliveryAttempts {
				t.Fatalf("invalid or repeated attempt: %+v", got.d)
			}
			starts[got.d.Attempts] = true
		} else if !errors.Is(got.err, ErrDeliveryExhausted) {
			t.Fatal(got.err)
		}
	}
	if len(starts) != MaxDeliveryAttempts {
		t.Fatalf("starts=%v", starts)
	}
	d, err := s.BeginDeliveryAttempt(t.Context(), ev.Deliveries[0].ID)
	if !errors.Is(err, ErrDeliveryExhausted) || d.Attempts != MaxDeliveryAttempts || d.Done {
		t.Fatalf("exhausted row=%+v err=%v", d, err)
	}
	if err := s.UpdateDelivery(t.Context(), d.ID, false, true, "exhausted", time.Time{}); err != nil {
		t.Fatal(err)
	}
	d, err = s.BeginDeliveryAttempt(t.Context(), d.ID)
	if !errors.Is(err, ErrDeliveryDone) || !d.Done || d.Attempts != MaxDeliveryAttempts {
		t.Fatalf("terminal row=%+v err=%v", d, err)
	}
	_, err = s.BeginDeliveryAttempt(t.Context(), 999)
	assertAlertNotFound(t, err, ObjectAlertDelivery, 999)
}
