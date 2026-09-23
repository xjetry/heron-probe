// 写协程钩子只对 store 的测试构建可见；把真实注册表的并发回归放在这里，避免向生产代码暴露钩子。
package store_test

import (
	"io"
	"log/slog"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/probe"
	"github.com/xjetry/probe/internal/hub/store"
)

func TestRegistryForgetCannotBeRevivedByEarlierSave(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"), clock.NewFake(time.Unix(0, 0)), log)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := st.CreateNode(t.Context(), "node", []byte("token"))
	if err != nil {
		t.Fatal(err)
	}
	r := probe.New(st, log)
	if err := r.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	release, pending, drain := st.HoldWriterForTest()
	defer release()
	type result struct {
		detail probe.Detail
		err    error
	}
	saved := make(chan result, 1)
	go func() {
		d, _, err := r.Save(t.Context(), &probev1.ProbeTask{Kind: probev1.ProbeKind_PROBE_KIND_ICMP, Target: "localhost", IntervalS: 5, TimeoutMs: 1000}, []int64{id})
		saved <- result{d, err}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for pending() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("Save did not reach writer queue")
		}
		runtime.Gosched()
	}
	forgotten := make(chan struct{})
	go func() { r.Forget(id); close(forgotten) }()
	// 队列里已有 Save，此时它持有 writeMu；给未加锁的 Forget 留出先完成的机会。
	select {
	case <-forgotten:
	case <-time.After(100 * time.Millisecond):
	}
	release()
	res := <-saved
	if res.err != nil {
		t.Fatal(res.err)
	}
	<-forgotten
	if err := drain(); err != nil {
		t.Fatal(err)
	}
	if r.Assigned(id, res.detail.Task.Id) {
		t.Fatal("earlier Save revived forgotten node assignment")
	}
}
