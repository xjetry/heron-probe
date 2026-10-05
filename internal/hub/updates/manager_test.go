package updates

import (
	"io"
	"log/slog"
	"maps"
	"path/filepath"
	"strings"
	"testing"
	"time"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/clock"
	"github.com/xjetry/heron-probe/internal/hub/store"
	"google.golang.org/protobuf/proto"
)

// fixtureBound 装配一个绑定指定 agent 版本的 Manager；用例里 Start 的目标是什么版本，绑定就给什么，
// 让拒绝落在被测的原因上而不是先被绑定检查挡下。
func fixtureBound(t *testing.T, bound string) (*Manager, int64, *clock.Fake) {
	t.Helper()
	clk := clock.NewFake(time.Now())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(filepath.Join(t.TempDir(), "db"), clk, log, store.MigrateSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	id, _, err := st.CreateNode(t.Context(), "test", store.Billing{}, []byte("token"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(st, clk, log, bound)
	if err := m.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	m.Observe(id, &heronv1.UpdateStatus{Supported: true, Version: "v0.2.0"})
	m.flush(t.Context())
	return m, id, clk
}

func fixture(t *testing.T) (*Manager, int64, *clock.Fake) {
	t.Helper()
	return fixtureBound(t, "v0.3.0")
}

func TestUpdateDispatchPersistsBeforeDeliveryAndCannotCancel(t *testing.T) {
	m, id, _ := fixture(t)
	task, err := m.Start(t.Context(), id, "v0.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Observe(id, m.Snapshot(id)); got != nil {
		t.Fatalf("undurable dispatch delivered: %v", got)
	}
	m.flush(t.Context())
	got := m.Observe(id, m.Snapshot(id))
	if got == nil || got.State != "dispatched" || got.Id != task.Id {
		t.Fatalf("delivery=%v", got)
	}
	rows, err := m.st.NodeUpdates(t.Context())
	if err != nil || rows[id].Task.State != "dispatched" {
		t.Fatalf("dispatch not durable: %v %v", rows, err)
	}
	if err := m.Cancel(t.Context(), id, task.Id); err == nil {
		t.Fatal("cancelled a dispatched update")
	}
	m2 := New(m.st, m.clk, m.log, "v0.3.0")
	if err := m2.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := m2.Observe(id, m.Snapshot(id)); got == nil || got.Id != task.Id {
		t.Fatalf("restart did not redeliver same authorization: %v", got)
	}
}

func TestUpdateResultRequiresMatchingTaskAndRunningVersion(t *testing.T) {
	for _, mutate := range []struct {
		name, expected string
		f              func(*heronv1.UpdateStatus)
	}{
		{"different_id", "unconfirmed", func(s *heronv1.UpdateStatus) { s.Task.Id = "fedcba9876543210" }},
		{"different_target", "dispatched", func(s *heronv1.UpdateStatus) { s.Task.Version = "v0.4.0" }},
		{"old_process", "dispatched", func(s *heronv1.UpdateStatus) { s.Version = "v0.2.0" }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			m, id, _ := fixture(t)
			task, err := m.Start(t.Context(), id, "v0.3.0")
			if err != nil {
				t.Fatal(err)
			}
			m.Observe(id, m.Snapshot(id))
			m.flush(t.Context())
			reported := &heronv1.UpdateStatus{Supported: true, Version: "v0.3.0", Task: proto.Clone(task).(*heronv1.UpdateTask)}
			reported.Task.State = "succeeded"
			mutate.f(reported)
			m.Observe(id, reported)
			m.flush(t.Context())
			if got := m.Snapshot(id).Task.State; got != mutate.expected {
				t.Fatalf("unrelated success changed task: %s", got)
			}
			reported.Version = "v0.3.0"
			reported.Task = proto.Clone(task).(*heronv1.UpdateTask)
			reported.Task.State = "succeeded"
			m.Observe(id, reported)
			m.flush(t.Context())
			if got := m.Snapshot(id).Task.State; got != "succeeded" {
				t.Fatalf("matching success rejected: %s", got)
			}
		})
	}
}

func TestUpdateQueueExpiryCancellationAndSupport(t *testing.T) {
	// 不比当前版本新的目标被 Newer 拒绝：绑定给成它启动的那个版本，拒绝才落在原来的原因上。
	for _, v := range []string{"v0.1.0", "v0.2.0"} {
		m, id, _ := fixtureBound(t, v)
		if _, err := m.Start(t.Context(), id, v); err == nil {
			t.Fatalf("accepted target %s", v)
		}
	}
	m, id, clk := fixture(t)
	// 预发布永远过不了 ValidVersion，任何绑定下它都到不了 Newer 检查，被目标检查拒绝。
	if _, err := m.Start(t.Context(), id, "v0.3.0-rc1"); err == nil {
		t.Fatal("accepted target v0.3.0-rc1")
	}
	task, err := m.Start(t.Context(), id, "v0.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(t.Context(), id, "v0.3.0"); err == nil {
		t.Fatal("replaced active task")
	}
	if err := m.Cancel(t.Context(), id, task.Id); err != nil {
		t.Fatal(err)
	}
	m.Observe(id, m.Snapshot(id))
	m.flush(t.Context())
	if m.Snapshot(id).Task.State != "cancelled" {
		t.Fatal("cancelled task restarted")
	}
	if _, err := m.Start(t.Context(), id, "v0.3.0"); err != nil {
		t.Fatal(err)
	}
	clk.Advance(24 * time.Hour)
	if got := m.Observe(id, m.Snapshot(id)); got != nil {
		t.Fatal("expired task delivered")
	}
	m.flush(t.Context())
	if m.Snapshot(id).Task.State != "expired" {
		t.Fatal("expired task remains queued")
	}
	m.Observe(id, nil)
	m.flush(t.Context())
	if _, err := m.Start(t.Context(), id, "v0.3.0"); err == nil {
		t.Fatal("accepted unsupported agent")
	}
}

func TestStartRejectsWithoutStableBound(t *testing.T) {
	for _, bound := range []string{"", "dev", "v0.3.0-rc.1"} {
		m, id, _ := fixtureBound(t, bound)
		_, err := m.Start(t.Context(), id, "v0.3.0")
		if err == nil || !strings.Contains(err.Error(), "no stable bound agent version") {
			t.Fatalf("bound %q accepted a node update: %v", bound, err)
		}
		if task := m.Snapshot(id).Task; task != nil {
			t.Fatalf("bound %q wrote a task", bound)
		}
	}
}

func TestStartRejectsVersionOtherThanBound(t *testing.T) {
	m, id, _ := fixture(t)
	_, err := m.Start(t.Context(), id, "v0.4.0")
	if err == nil || !strings.Contains(err.Error(), "bound agent version v0.3.0") {
		t.Fatalf("accepted a target other than the bound: %v", err)
	}
	if task := m.Snapshot(id).Task; task != nil {
		t.Fatal("rejected target wrote a task")
	}
	if _, err := m.Start(t.Context(), id, "v0.3.0"); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateDeletedNodeNeverReappears(t *testing.T) {
	m, id, _ := fixture(t)
	status := m.Snapshot(id)
	m.Observe(id, status)
	if err := m.st.DeleteNode(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	m.Forget(id)
	m.Observe(id, status)
	m.flush(t.Context())
	if m.Snapshot(id).Supported {
		t.Fatal("late report recreated deleted memory state")
	}
	rows, err := m.st.NodeUpdates(t.Context())
	if err != nil || len(rows) != 0 {
		t.Fatalf("deleted state persisted: %v %v", rows, err)
	}
}

func TestUpdateObserveDoesNotWaitForWriter(t *testing.T) {
	m, id, _ := fixture(t)
	m.op.Lock()
	done := make(chan struct{})
	go func() { m.Observe(id, m.Snapshot(id)); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		m.op.Unlock()
		t.Fatal("report waited for background writer")
	}
	m.op.Unlock()
}

func TestUpdateOldProgressCannotRegressAndDuplicatesDoNotChangeTime(t *testing.T) {
	m, id, clk := fixture(t)
	if _, err := m.Start(t.Context(), id, "v0.3.0"); err != nil {
		t.Fatal(err)
	}
	m.Observe(id, m.Snapshot(id))
	m.flush(t.Context())
	report := m.Snapshot(id)
	report.Task.State = "verifying"
	m.Observe(id, report)
	m.flush(t.Context())
	at := m.Snapshot(id).Task.UpdatedAt
	clk.Advance(time.Minute)
	m.Observe(id, report)
	m.flush(t.Context())
	if m.Snapshot(id).Task.UpdatedAt != at {
		t.Fatal("duplicate progress changed transition timestamp")
	}
	report.Task.State = "downloading"
	m.Observe(id, report)
	m.flush(t.Context())
	if got := m.Snapshot(id).Task.State; got != "verifying" {
		t.Fatalf("old progress regressed task: %s", got)
	}
}

func TestLostResultExpiresWithoutClaimingFailureAndAcceptsLateResult(t *testing.T) {
	for _, state := range []string{"dispatched", "downloading", "stopping", "installing", "verifying"} {
		t.Run(state, func(t *testing.T) {
			m, id, clk := fixture(t)
			if _, err := m.Start(t.Context(), id, "v0.3.0"); err != nil {
				t.Fatal(err)
			}
			m.Observe(id, m.Snapshot(id))
			m.flush(t.Context())
			report := m.Snapshot(id)
			report.Task.State = state
			m.Observe(id, report)
			m.flush(t.Context())
			clk.Advance(24 * time.Hour)
			m.Observe(id, &heronv1.UpdateStatus{Supported: true, Version: "v0.2.0"})
			m.flush(t.Context())
			if got := m.Snapshot(id).Task.State; got != "unconfirmed" {
				t.Fatalf("lost outcome should be unconfirmed, got %s", got)
			}
			report.Task.State = "succeeded"
			m.Observe(id, report)
			m.flush(t.Context())
			if m.Snapshot(id).Task.State != "unconfirmed" {
				t.Fatal("old process confirmed success")
			}
			report.Version = "v0.3.0"
			m.Observe(id, report)
			m.flush(t.Context())
			if m.Snapshot(id).Task.State != "succeeded" {
				t.Fatal("matching late success was lost")
			}
		})
	}
}

func TestManualUpgradeReconcilesUnexecutedAuthorization(t *testing.T) {
	for _, version := range []string{"v0.3.0", "v0.4.0"} {
		t.Run(version, func(t *testing.T) {
			m, id, _ := fixture(t)
			if _, err := m.Start(t.Context(), id, "v0.3.0"); err != nil {
				t.Fatal(err)
			}
			m.Observe(id, m.Snapshot(id))
			m.flush(t.Context())
			m.Observe(id, &heronv1.UpdateStatus{Supported: true, Version: version})
			m.flush(t.Context())
			if task := m.Snapshot(id).Task; task.State != "unconfirmed" || task.Error == "" {
				t.Fatalf("manual install must not claim task success or block later updates: %v", task)
			}
			// 目标只能是绑定版本（spec §14.1）：节点已手动装到目标之上时，这个 hub 没有再高的可下发目标。
			if _, err := m.Start(t.Context(), id, "v0.5.0"); err == nil {
				t.Fatal("accepted a target other than the bound agent version")
			}
		})
	}
}

func TestActiveTasksListsOnlyActiveStates(t *testing.T) {
	m := New(nil, clock.NewFake(time.Unix(1000, 0)), slog.Default(), "")
	m.states = map[int64]*heronv1.UpdateStatus{
		1: {Task: &heronv1.UpdateTask{Id: "aaaaaaaaaaaaaaaa", Version: "v1.0.0", State: "dispatched"}},
		2: {Task: &heronv1.UpdateTask{Id: "bbbbbbbbbbbbbbbb", Version: "v1.0.0", State: "succeeded"}},
		3: {Task: &heronv1.UpdateTask{Id: "cccccccccccccccc", Version: "v1.1.0", State: "downloading"}},
		4: {},
	}
	got := m.ActiveTasks()
	want := map[string]string{"aaaaaaaaaaaaaaaa": "v1.0.0", "cccccccccccccccc": "v1.1.0"}
	if !maps.Equal(got, want) {
		t.Fatalf("ActiveTasks = %v, want %v", got, want)
	}
}
