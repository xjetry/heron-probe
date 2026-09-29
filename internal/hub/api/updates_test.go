package api

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/update"
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
