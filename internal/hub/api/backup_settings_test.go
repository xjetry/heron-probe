package api

import (
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"google.golang.org/protobuf/proto"
)

func fullBackup() *probev1.BackupSettings {
	return &probev1.BackupSettings{Endpoint: "https://account.r2.cloudflarestorage.com", Bucket: "private-backups", Region: "auto", AccessKey: "access", Secret: proto.String("never-echo-this"), Prefix: "hub/", ConfigIntervalS: proto.Uint32(600), MetricsIntervalS: proto.Uint32(7200), ConfigKeep: proto.Uint32(24), MetricsKeep: proto.Uint32(7)}
}

func TestBackupSettingsDefaultsWriteOnlyAndOmission(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	defaults := currentSettings(t, h).GetBackup()
	if defaults == nil || defaults.GetConfigIntervalS() != 300 || defaults.GetMetricsIntervalS() != 86400 || defaults.GetConfigKeep() != 48 || defaults.GetMetricsKeep() != 14 {
		t.Fatalf("backup defaults = %v", defaults)
	}
	in := validSettings()
	in.Backup = fullBackup()
	got := saveSettings(t, h, in)
	want := proto.Clone(in.Backup).(*probev1.BackupSettings)
	want.Secret = nil
	if !proto.Equal(got.Backup, want) {
		t.Errorf("saved backup mismatch or secret echoed: %v", got.Backup)
	}
	if read := currentSettings(t, h); !proto.Equal(read.Backup, want) {
		t.Errorf("read backup mismatch or secret echoed: %v", read.Backup)
	}
	// 缺席 backup 的现有外观消费者不得改备份配置。
	if read := saveSettings(t, h, validSettings()); !proto.Equal(read.Backup, want) {
		t.Fatalf("appearance update changed backup: %v", read.Backup)
	}
	in.Backup.Secret = nil
	in.Backup.ConfigIntervalS, in.Backup.MetricsIntervalS, in.Backup.ConfigKeep, in.Backup.MetricsKeep = nil, nil, nil, nil
	if read := saveSettings(t, h, in); !proto.Equal(read.Backup, want) {
		t.Fatalf("omitted numeric fields changed backup: %v", read.Backup)
	}
	in.Backup.ConfigIntervalS, in.Backup.MetricsIntervalS, in.Backup.ConfigKeep, in.Backup.MetricsKeep = proto.Uint32(300), proto.Uint32(86400), proto.Uint32(48), proto.Uint32(14)
	got = saveSettings(t, h, in)
	if got.Backup.GetConfigIntervalS() != 300 || got.Backup.GetMetricsIntervalS() != 86400 || got.Backup.GetConfigKeep() != 48 || got.Backup.GetMetricsKeep() != 14 {
		t.Fatalf("explicit defaults not saved: %v", got.Backup)
	}
}

func TestBackupSettingsRanges(t *testing.T) {
	for _, tc := range []struct {
		field    string
		min, max uint32
		set      func(*probev1.BackupSettings, *uint32)
	}{
		{"config_interval_s", 60, 86400, func(b *probev1.BackupSettings, v *uint32) { b.ConfigIntervalS = v }},
		{"metrics_interval_s", 3600, 604800, func(b *probev1.BackupSettings, v *uint32) { b.MetricsIntervalS = v }},
		{"config_keep", 1, 1000, func(b *probev1.BackupSettings, v *uint32) { b.ConfigKeep = v }},
		{"metrics_keep", 1, 1000, func(b *probev1.BackupSettings, v *uint32) { b.MetricsKeep = v }},
	} {
		t.Run(tc.field, func(t *testing.T) {
			h := newHarness(t, "")
			h.login(t)
			in := validSettings()
			in.Backup = fullBackup()
			before := saveSettings(t, h, in)
			for _, bad := range []uint32{0, tc.min - 1, tc.max + 1, ^uint32(0)} {
				saveSettings(t, h, before)
				tc.set(in.Backup, &bad)
				in.Title = "must-not-be-saved"
				_, err := h.admin.UpdateSettings(t.Context(), connect.NewRequest(&probev1.UpdateSettingsRequest{Settings: in}))
				want := fmt.Sprintf("backup.%s must be in [%d, %d]", tc.field, tc.min, tc.max)
				if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), want) {
					t.Errorf("%s=%d: want InvalidArgument %q, got %v", tc.field, bad, want, err)
				}
				if got := currentSettings(t, h); !proto.Equal(got, before) {
					t.Errorf("invalid backup update changed settings: %v", got)
				}
			}
			for _, good := range []uint32{tc.min, tc.max} {
				tc.set(in.Backup, &good)
				saveSettings(t, h, in)
			}
		})
	}
}

func TestBackupTargetValidation(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	in := validSettings()
	in.Backup = fullBackup()
	before := saveSettings(t, h, in)
	for _, tc := range []struct {
		field  string
		change func(*probev1.BackupSettings)
	}{
		{"endpoint", func(b *probev1.BackupSettings) { b.Endpoint = "file:///tmp/backup" }},
		{"endpoint", func(b *probev1.BackupSettings) { b.Endpoint = "https://user:secret@host" }},
		{"bucket", func(b *probev1.BackupSettings) { b.Bucket = "../escape" }},
		{"region", func(b *probev1.BackupSettings) { b.Region = "region/escape" }},
		{"access_key", func(b *probev1.BackupSettings) { b.AccessKey = "key\nheader" }},
		{"secret", func(b *probev1.BackupSettings) { b.Secret = proto.String(strings.Repeat("s", 4097)) }},
		{"prefix", func(b *probev1.BackupSettings) { b.Prefix = strings.Repeat("p", 513) }},
		{"channels", func(b *probev1.BackupSettings) { b.Channels = []int64{-1} }},
	} {
		t.Run(tc.field, func(t *testing.T) {
			in.Backup = fullBackup()
			tc.change(in.Backup)
			rejected(t, h, in, "backup."+tc.field, before)
		})
	}
}
