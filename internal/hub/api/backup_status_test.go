package api

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/hub/store"
)

func TestBackupStatusReadAccessAndState(t *testing.T) {
	h := newHarness(t, "")
	client := probev1connect.NewAdminServiceClient(h.srv.Client(), h.srv.URL)
	if _, err := client.GetBackupStatus(t.Context(), connect.NewRequest(&probev1.GetBackupStatusRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous status=%v", err)
	}
	h.login(t)
	_, token := createToken(t, h, "reader")
	read := func() *probev1.GetBackupStatusResponse {
		t.Helper()
		req := connect.NewRequest(&probev1.GetBackupStatusRequest{})
		req.Header().Set("Authorization", "Bearer "+token)
		r, err := client.GetBackupStatus(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg
	}
	initial := read()
	if initial.Enabled || initial.Config.LastSuccessAt != nil || initial.Metrics.LastSuccessAt != nil || initial.Config.Failure != nil || initial.Metrics.Failure != nil {
		t.Fatalf("initial status=%v", initial)
	}
	if err := h.store.RecordBackupSuccess(t.Context(), "config"); err != nil {
		t.Fatal(err)
	}
	wantConfig := h.clk.Now().Unix()
	h.clk.Advance(time.Minute)
	if err := h.store.RecordBackupSuccess(t.Context(), "metrics"); err != nil {
		t.Fatal(err)
	}
	secret := "secret"
	_, _, err := h.store.SaveSettings(t.Context(), store.SiteSettingsUpdate{}, &store.BackupSettingsUpdate{Endpoint: "://invalid", Bucket: "backup", Region: "auto", AccessKey: "key", Secret: &secret})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.cfg.Backups.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := read()
	if !got.Enabled || got.Config.GetLastSuccessAt() != wantConfig || got.Metrics.GetLastSuccessAt() != h.clk.Now().Unix() {
		t.Fatalf("persisted API status=%v", got)
	}
	for _, layer := range []*probev1.BackupLayerStatus{got.Config, got.Metrics} {
		if layer.Failure == nil || layer.Failure.Category != "client" || layer.Failure.SinceAt != h.clk.Now().Unix() {
			t.Fatalf("failure API status=%v", layer)
		}
	}
	session, err := h.admin.GetBackupStatus(t.Context(), connect.NewRequest(&probev1.GetBackupStatusRequest{}))
	if err != nil || session.Msg.Config.GetLastSuccessAt() != wantConfig {
		t.Fatalf("session status=%v err=%v", session, err)
	}
}
