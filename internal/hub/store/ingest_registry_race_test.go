// 写协程阻塞钩子只对 store 的测试构建可见；在这里验证真实上报入口，避免向生产代码增加时序钩子。
package store_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/auth"
	"github.com/xjetry/probe/internal/hub/ingest"
	"github.com/xjetry/probe/internal/hub/live"
	"github.com/xjetry/probe/internal/hub/probe"
	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/hub/traffic"
)

type notifiedTasks struct {
	*probe.Registry
	entered chan struct{}
}

func (r *notifiedTasks) Forget(id int64) {
	close(r.entered)
	r.Registry.Forget(id)
}

func TestIngestForgetWaitsForRegistryOutsideIngestLocks(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"), clk, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	a := auth.New(st, clk, log)
	l := live.New(clk, 30*time.Second)
	book := traffic.New(st, clk, time.UTC, log)
	reg := probe.New(st, log)
	tasks := &notifiedTasks{Registry: reg, entered: make(chan struct{})}
	svc, err := ingest.New(ingest.Config{TTL: 30 * time.Second}, l, st, a, book, tasks, clk, log)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(a.Load(ctx), book.Load(ctx), reg.Load(ctx), svc.Load(ctx)); err != nil {
		t.Fatal(err)
	}
	deleted, _, err := a.CreateNode(ctx, "delete")
	if err != nil {
		t.Fatal(err)
	}
	keep, token, err := a.CreateNode(ctx, "keep")
	if err != nil {
		t.Fatal(err)
	}
	task := &probev1.ProbeTask{Kind: probev1.ProbeKind_PROBE_KIND_ICMP, Target: "localhost", IntervalS: 5, TimeoutMs: 1000}
	d, _, err := reg.Save(ctx, task, []int64{deleted})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteNode(ctx, deleted); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(svc.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := probev1connect.NewAgentServiceClient(srv.Client(), srv.URL)
	release, pending, drain := st.HoldWriterForTest()
	defer release()
	saved := make(chan error, 1)
	go func() { _, _, err := reg.Save(ctx, task, []int64{keep}); saved <- err }()
	waitRegistryWrite(t, pending)
	forgotten := make(chan struct{})
	go func() { svc.Forget(deleted); close(forgotten) }()
	select {
	case <-tasks.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Forget did not reach registry")
	}
	reported := make(chan error, 1)
	go func() {
		req := connect.NewRequest(&probev1.ReportRequest{Metrics: &probev1.Metrics{}})
		req.Header().Set("Authorization", "Bearer "+token)
		_, err := client.Report(ctx, req)
		reported <- err
	}()
	var reportErr error
	reportBlocked := false
	select {
	case reportErr = <-reported:
	case <-time.After(200 * time.Millisecond):
		reportBlocked = true
	}
	// 当前分钟没有闭合桶，Flush 不需要存储往返；它仍须能取得 pendingMu。
	flushed := make(chan struct{})
	go func() { svc.Flush(ctx, false); close(flushed) }()
	flushBlocked := false
	select {
	case <-flushed:
	case <-time.After(200 * time.Millisecond):
		flushBlocked = true
	}
	release()
	if reportBlocked {
		reportErr = <-reported
	}
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
	<-forgotten
	<-flushed
	if err := drain(); err != nil {
		t.Fatal(err)
	}
	if reportBlocked || reportErr != nil {
		t.Errorf("Report waited for registry write: blocked=%v err=%v", reportBlocked, reportErr)
	}
	if flushBlocked {
		t.Error("Flush waited for registry write while no closed buckets existed")
	}
	if reg.Assigned(deleted, d.Task.Id) {
		t.Error("deleted assignment survived Forget")
	}
}
