package alert

import (
	"sync"
	"testing"
	"time"
)

func TestSweepConcurrentRuleWrites(t *testing.T) {
	f := newFixture(t)
	f.clk.Advance(time.Minute)
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; i < 30; i++ {
			if err := f.e.SweepOffline(t.Context()); err != nil {
				t.Error(err)
				return
			}
			f.e.Rules()
			f.e.Channels()
			f.e.States()
		}
	})
	wg.Go(func() {
		for i := 0; i < 30; i++ {
			r, err := f.e.SaveRule(t.Context(), offline())
			if err != nil {
				t.Error(err)
				return
			}
			if err := f.e.DeleteRule(t.Context(), r.ID); err != nil {
				t.Error(err)
				return
			}
		}
	})
	wg.Wait()
}
