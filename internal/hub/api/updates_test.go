package api

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"connectrpc.com/connect"
	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/hub/updates"
	"github.com/xjetry/heron-probe/internal/update"
	"google.golang.org/protobuf/proto"
)

type updateAPIFake struct {
	submitted update.Request
	checks    int
}

func (f *updateAPIFake) Status(context.Context) update.Status {
	return update.Status{Supported: true, Version: "v0.2.0"}
}
func (f *updateAPIFake) Submit(_ context.Context, r update.Request) (update.Job, error) {
	f.submitted = r
	return update.Job{Request: r, State: "queued"}, nil
}
func (f *updateAPIFake) Latest(context.Context) (string, error) { f.checks++; return "v0.3.0", nil }

func TestUpdateAPIUsesExplicitReleaseChecksAndRunningHubVersion(t *testing.T) {
	h := newHarness(t, "", withConfig(func(c *Config) { c.HubVersion = "v0.2.0" }))
	h.login(t)
	f := &updateAPIFake{}
	h.svc.updateLocal = f
	h.svc.updateSource = f
	got, err := h.admin.GetUpdates(t.Context(), connect.NewRequest(&heronv1.GetUpdatesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if f.checks != 0 || len(got.Msg.Targets) != 1 || got.Msg.Targets[0].Status.Version != "v0.2.0" || !got.Msg.Targets[0].Status.Supported {
		t.Fatalf("local status=%v checks=%d", got.Msg, f.checks)
	}
	got, err = h.admin.GetUpdates(t.Context(), connect.NewRequest(&heronv1.GetUpdatesRequest{CheckLatest: true}))
	if err != nil || got.Msg.LatestVersion != "v0.3.0" || f.checks != 1 {
		t.Fatalf("explicit check=%v err=%v checks=%d", got, err, f.checks)
	}
	if _, err := h.admin.StartUpdate(t.Context(), connect.NewRequest(&heronv1.StartUpdateRequest{Version: "v0.2.0"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("same-version install=%v", err)
	}
	if f.submitted.ID != "" {
		t.Fatal("same-version request reached updater")
	}
	started, err := h.admin.StartUpdate(t.Context(), connect.NewRequest(&heronv1.StartUpdateRequest{Version: "v0.3.0"}))
	if err != nil {
		t.Fatal(err)
	}
	if started.Msg.Task.Id != f.submitted.ID || f.submitted.Version != "v0.3.0" {
		t.Fatalf("submission=%+v response=%v", f.submitted, started.Msg)
	}
	if err := f.submitted.Validate(h.clk.Now()); err != nil {
		t.Fatalf("generated authorization=%v", err)
	}
}

func TestUpdateAPIReadTokenCannotAuthorizeUpdates(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	_, tok := createToken(t, h, "read")
	if got := rawCall(t, h, "GetUpdates", "{}", bearer(tok)); got.status != 200 {
		t.Fatalf("read status: %+v", got)
	}
	for _, method := range []string{"StartUpdate", "CancelUpdate"} {
		if got := rawCall(t, h, method, `{"node_id":"1","version":"v0.3.0"}`, bearer(tok)); got.status != 403 {
			t.Fatalf("read token %s: %+v", method, got)
		}
	}
}

func TestUpdateAPIRejectsInvalidTargetsAndBounds(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	for _, v := range []string{"https://example.test/program", "v0.3.0-rc1", strings.Repeat("x", 35)} {
		_, err := h.admin.StartUpdate(t.Context(), connect.NewRequest(&heronv1.StartUpdateRequest{Version: v}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("version %q error=%v", v, err)
		}
	}
	_, err := h.admin.CancelUpdate(t.Context(), connect.NewRequest(&heronv1.CancelUpdateRequest{NodeId: 1, Id: strings.Repeat("x", 65)}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("oversized cancel ID=%v", err)
	}
}

// boundManager 装配一个绑定指定 agent 版本的 Manager 并挂进 harness 的服务。
func boundManager(t *testing.T, h *harness, bound string) *updates.Manager {
	t.Helper()
	m := updates.New(h.store, h.clk, slog.Default(), bound)
	if err := m.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	h.svc.cfg.Updates = m
	return m
}

func TestSnapshotAndUpdatesCarryBoundAgentVersion(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	boundManager(t, h, "v1.2.3")
	f := &updateAPIFake{}
	h.svc.updateSource = f
	snap, err := h.admin.GetSnapshot(t.Context(), connect.NewRequest(&heronv1.GetSnapshotRequest{}))
	if err != nil || snap.Msg.BoundAgentVersion != "v1.2.3" {
		t.Fatalf("snapshot bound=%q err=%v", snap.Msg.BoundAgentVersion, err)
	}
	got, err := h.admin.GetUpdates(t.Context(), connect.NewRequest(&heronv1.GetUpdatesRequest{}))
	if err != nil || got.Msg.BoundAgentVersion != "v1.2.3" {
		t.Fatalf("updates bound=%q err=%v", got.Msg.BoundAgentVersion, err)
	}
	got, err = h.admin.GetUpdates(t.Context(), connect.NewRequest(&heronv1.GetUpdatesRequest{CheckLatest: true}))
	if err != nil || got.Msg.BoundAgentVersion != "v1.2.3" || f.checks != 1 {
		t.Fatalf("check_latest bound=%q err=%v checks=%d", got.Msg.BoundAgentVersion, err, f.checks)
	}
}

func TestSnapshotWithoutUpdatesHasNoBoundAgentVersion(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	snap, err := h.admin.GetSnapshot(t.Context(), connect.NewRequest(&heronv1.GetSnapshotRequest{}))
	if err != nil || snap.Msg.BoundAgentVersion != "" {
		t.Fatalf("snapshot bound=%q err=%v", snap.Msg.BoundAgentVersion, err)
	}
	got, err := h.admin.GetUpdates(t.Context(), connect.NewRequest(&heronv1.GetUpdatesRequest{}))
	if err != nil || got.Msg.BoundAgentVersion != "" {
		t.Fatalf("updates bound=%q err=%v", got.Msg.BoundAgentVersion, err)
	}
}

func TestStartUpdateRejectsNonBoundVersion(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	node, _ := h.createNode(t, "bound")
	if err := h.store.SaveNodeUpdate(t.Context(), node, &heronv1.UpdateStatus{Supported: true, Version: "v1.2.0"}); err != nil {
		t.Fatal(err)
	}
	m := boundManager(t, h, "v1.2.3")
	_, err := h.admin.StartUpdate(t.Context(), connect.NewRequest(&heronv1.StartUpdateRequest{NodeId: node, Version: "v1.2.4"}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "bound agent version v1.2.3") {
		t.Fatalf("non-bound target error=%v", err)
	}
	if task := m.Snapshot(node).Task; task != nil {
		t.Fatalf("rejected target queued a task: %v", task)
	}
}

func TestExecuteChangeStartUpdateRejectsNonBoundVersion(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	node, _ := h.createNode(t, "bound")
	if err := h.store.SaveNodeUpdate(t.Context(), node, &heronv1.UpdateStatus{Supported: true, Version: "v1.2.0"}); err != nil {
		t.Fatal(err)
	}
	boundManager(t, h, "v1.2.3")
	client, _, _ := grantedClient(t, h, &heronv1.TokenGrant{NodeIds: []int64{node}, Permissions: []heronv1.TokenPermission{heronv1.TokenPermission_TOKEN_PERMISSION_UPDATE}})
	bad := &heronv1.ExecuteChangeRequest{RequestId: "update-non-bound", Change: &heronv1.ExecuteChangeRequest_StartUpdate{StartUpdate: &heronv1.StartUpdateRequest{NodeId: node, Version: "v1.2.4"}}}
	// 预览先被业务校验拒绝。
	p := proto.Clone(bad).(*heronv1.ExecuteChangeRequest)
	p.Preview = true
	_, err := client.ExecuteChange(t.Context(), connect.NewRequest(p))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "bound agent version v1.2.3") {
		t.Fatalf("preview error=%v", err)
	}
	// 执行也过不去：先拿合法目标（绑定版本）的预览换 expected_version，再提交非法目标。
	good := &heronv1.ExecuteChangeRequest{RequestId: "update-bound", Change: &heronv1.ExecuteChangeRequest_StartUpdate{StartUpdate: &heronv1.StartUpdateRequest{NodeId: node, Version: "v1.2.3"}}}
	previewChange(t, client, good)
	bad.ExpectedVersion = good.ExpectedVersion
	_, err = client.ExecuteChange(t.Context(), connect.NewRequest(bad))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "bound agent version v1.2.3") {
		t.Fatalf("commit error=%v", err)
	}
	ops, err := client.ListOperations(t.Context(), connect.NewRequest(&heronv1.ListOperationsRequest{}))
	if err != nil || len(ops.Msg.Operations) != 0 {
		t.Fatalf("rejected change left receipts: %v err=%v", ops.Msg, err)
	}
	if task := h.svc.cfg.Updates.Snapshot(node).Task; task != nil {
		t.Fatalf("rejected change queued a task: %v", task)
	}
}
