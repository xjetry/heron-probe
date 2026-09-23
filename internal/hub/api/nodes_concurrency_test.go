package api

import (
	"sync"
	"testing"

	"connectrpc.com/connect"
	probev1 "github.com/xjetry/probe/gen/probe/v1"
)

func TestConcurrentUpdatesKeepResetDayConsistent(t *testing.T) {
	h := newHarness(t, "")
	id, _, err := h.auth.CreateNode(t.Context(), "n")
	if err != nil {
		t.Fatal(err)
	}
	for round := range 2000 {
		start := make(chan struct{})
		errs := make(chan error, 2)
		for _, day := range []uint32{15, 20} {
			go func() {
				<-start
				_, err := h.svc.UpdateNode(t.Context(), connect.NewRequest(&probev1.UpdateNodeRequest{Id: id, Name: "n", TrafficResetDay: day}))
				errs <- err
			}()
		}
		close(start)
		for range 2 {
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
		}
		n, err := h.store.GetNode(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if day := h.book.View(id).ResetDay; day != n.TrafficResetDay {
			t.Fatalf("round %d: memory reset day %d differs from stored %d", round, day, n.TrafficResetDay)
		}
	}
}

func TestConcurrentUpdateCannotReviveDeletedResetDay(t *testing.T) {
	h := newHarness(t, "")
	for round := range 2000 {
		id, _, err := h.auth.CreateNode(t.Context(), "n")
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() {
			<-start
			// 删除可以先提交，更新此时无须成功；最终内存状态不能因更新复活。
			_, _ = h.svc.UpdateNode(t.Context(), connect.NewRequest(&probev1.UpdateNodeRequest{Id: id, Name: "n", TrafficResetDay: 20}))
		})
		wg.Go(func() {
			<-start
			if _, err := h.svc.DeleteNode(t.Context(), connect.NewRequest(&probev1.DeleteNodeRequest{Id: id})); err != nil {
				t.Error(err)
			}
		})
		close(start)
		wg.Wait()
		if day := h.book.View(id).ResetDay; day != 1 {
			t.Fatalf("round %d: deleted node retained reset day %d", round, day)
		}
	}
}
