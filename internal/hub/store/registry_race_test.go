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

func registryRaceStore(t *testing.T) (*probe.Registry, *store.Store, int64) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"), clock.NewFake(time.Unix(0, 0)), log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	id, err := st.CreateNode(t.Context(), "node", []byte("token"))
	if err != nil {
		t.Fatal(err)
	}
	r := probe.New(st, log)
	if err := r.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	return r, st, id
}

func waitRegistryWrite(t *testing.T, pending func() int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for pending() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("registry write did not reach writer queue")
		}
		runtime.Gosched()
	}
}

func TestRegistryForgetCannotBeRevivedByEarlierSave(t *testing.T) {
	r, st, id := registryRaceStore(t)
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
	waitRegistryWrite(t, pending)
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

func TestRegistryDeleteSerializesFollowingSave(t *testing.T) {
	r, st, id := registryRaceStore(t)
	task := &probev1.ProbeTask{Kind: probev1.ProbeKind_PROBE_KIND_ICMP, Target: "localhost", IntervalS: 5, TimeoutMs: 1000}
	d, _, err := r.Save(t.Context(), task, []int64{id})
	if err != nil {
		t.Fatal(err)
	}
	release, pending, drain := st.HoldWriterForTest()
	defer release()
	deleted := make(chan error, 1)
	go func() { _, err := r.Delete(t.Context(), d.Task.Id); deleted <- err }()
	waitRegistryWrite(t, pending)
	saved := make(chan error, 1)
	go func() { _, _, err := r.Save(t.Context(), task, []int64{id}); saved <- err }()
	// Delete 入队时已持 writeMu，后来的 Save 在删除发布之前不能进入写队列。
	deadline := time.Now().Add(100 * time.Millisecond)
	n := pending()
	for n == 1 && time.Now().Before(deadline) {
		runtime.Gosched()
		n = pending()
	}
	release()
	deleteErr, saveErr := <-deleted, <-saved
	if deleteErr != nil || saveErr != nil {
		t.Fatalf("delete error=%v save error=%v", deleteErr, saveErr)
	}
	if err := drain(); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pending writes while Delete blocked=%d; want 1", n)
	}
}
