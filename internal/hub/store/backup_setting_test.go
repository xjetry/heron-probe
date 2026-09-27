package store

import (
	"database/sql"
	"reflect"
	"testing"
)

func TestBackupSecretAndDisabled(t *testing.T) {
	s, _ := open(t)
	secret := "secret"
	u := &BackupSettingsUpdate{Endpoint: "https://s3.example", Bucket: "backups", Region: "auto", AccessKey: "access", Secret: &secret}
	_, b, err := s.SaveSettings(t.Context(), SiteSettings{Theme: "auto"}, u)
	if err != nil || !b.Target.Enabled() || b.Target.Secret != secret {
		t.Fatalf("backup not enabled or secret lost: %+v, %v", b, err)
	}
	u.Secret = nil
	_, b, err = s.SaveSettings(t.Context(), SiteSettings{Theme: "dark"}, u)
	if err != nil || b.Target.Secret != secret {
		t.Fatalf("omitted secret overwritten: %+v, %v", b, err)
	}
	for _, field := range []string{"endpoint", "bucket", "access_key", "secret"} {
		t.Run(field, func(t *testing.T) {
			copy := *u
			switch field {
			case "endpoint":
				copy.Endpoint = ""
			case "bucket":
				copy.Bucket = ""
			case "access_key":
				copy.AccessKey = ""
			case "secret":
				empty := ""
				copy.Secret = &empty
			}
			_, _, err := s.SaveSettings(t.Context(), SiteSettings{Theme: "auto"}, &copy)
			if err != nil {
				t.Fatal(err)
			}
			b, err := s.BackupSettings(t.Context())
			if err != nil || b.Target.Enabled() {
				t.Fatalf("stored backup enabled without %s: %+v %v", field, b, err)
			}
		})
	}
}

func TestBackupSettingsAtomicAndChannels(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	channel, err := s.SaveNotifyChannel(ctx, NotifyChannel{Name: "backup", Kind: ChannelTelegram, Config: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	secret := "preserve"
	keep := uint32(24)
	u := &BackupSettingsUpdate{Secret: &secret, ConfigKeep: &keep, Channels: []int64{channel.ID}}
	site, before, err := s.SaveSettings(ctx, SiteSettings{Theme: "auto", Title: "old"}, u)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Channels) != 1 || before.Channels[0] != channel.ID {
		t.Fatalf("channels not saved: %+v", before)
	}
	if err := s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("CREATE TRIGGER reject_backup BEFORE INSERT ON setting WHEN NEW.key = 'backup.config_keep' BEGIN SELECT RAISE(ABORT, 'reject backup'); END")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	newSecret := "must-not-write"
	u.Secret = &newSecret
	if _, _, err := s.SaveSettings(ctx, SiteSettings{Theme: "dark", Title: "new"}, u); err == nil {
		t.Fatal("injected storage failure not returned")
	}
	gotSite, gotBackup, err := s.Settings(ctx)
	if err != nil || gotSite != site || !reflect.DeepEqual(gotBackup, before) {
		t.Fatalf("failed transaction changed settings: %+v %+v %v", gotSite, gotBackup, err)
	}
	if err := s.DeleteNotifyChannel(ctx, channel.ID); err != nil {
		t.Fatal(err)
	}
	b, err := s.BackupSettings(ctx)
	if err != nil || len(b.Channels) != 0 {
		t.Fatalf("deleted channel remains selected: %+v %v", b, err)
	}
	if _, _, err := s.SaveSettings(ctx, site, &BackupSettingsUpdate{Channels: []int64{channel.ID}}); err == nil {
		t.Fatal("nonexistent backup channel accepted")
	}
}
