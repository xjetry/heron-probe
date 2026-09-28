package api

import (
	"database/sql"
	"testing"

	"connectrpc.com/connect"
	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"google.golang.org/protobuf/proto"
)

func TestCorruptBackupNumbersRecoverThroughSettingsAPI(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	in := validSettings()
	in.Backup = fullBackup()
	before := saveSettings(t, h, in)
	db, err := sql.Open("sqlite", h.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, key := range []string{"backup.config_interval_s", "backup.metrics_interval_s", "backup.config_keep", "backup.metrics_keep"} {
		if _, err := db.Exec("UPDATE setting SET value = '0' WHERE key = ?", key); err != nil {
			t.Fatal(err)
		}
	}
	var bad int
	if err := db.QueryRow("SELECT count(*) FROM setting WHERE key IN ('backup.config_interval_s','backup.metrics_interval_s','backup.config_keep','backup.metrics_keep') AND value = '0'").Scan(&bad); err != nil || bad != 4 {
		t.Fatalf("corrupt fixture not installed: count=%d err=%v", bad, err)
	}
	if _, err := h.admin.GetSettings(t.Context(), connect.NewRequest(&probev1.GetSettingsRequest{})); connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("corrupt GetSettings: %v, want Internal", err)
	}
	in.Title = "must-not-commit"
	in.PublicEnabled = proto.Bool(false)
	in.Backup.ConfigKeep = nil
	if _, err := h.admin.UpdateSettings(t.Context(), connect.NewRequest(&probev1.UpdateSettingsRequest{Settings: in})); connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("incomplete repair: %v, want Internal", err)
	}
	if site, err := h.store.SiteSettings(t.Context()); err != nil || site.Title != before.Title || !site.PublicEnabled || !h.store.PublicEnabled() {
		t.Fatalf("failed repair committed appearance or public switch: %+v err=%v memory=%v", site, err, h.store.PublicEnabled())
	}
	in = proto.Clone(before).(*probev1.Settings)
	in.Backup.ConfigIntervalS, in.Backup.MetricsIntervalS = proto.Uint32(300), proto.Uint32(86400)
	in.Backup.ConfigKeep, in.Backup.MetricsKeep = proto.Uint32(48), proto.Uint32(14)
	got := saveSettings(t, h, in)
	if !proto.Equal(got, in) || !proto.Equal(currentSettings(t, h), in) {
		t.Fatalf("complete API repair did not restore readable settings: got=%v want=%v", got, in)
	}
}
