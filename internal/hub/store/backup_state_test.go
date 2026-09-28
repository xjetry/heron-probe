package store

import (
	"database/sql"
	"errors"
	"testing"
)

func TestBackupMarkerAndEventAtomic(t *testing.T) {
	for _, transition := range []Transition{TransitionFiring, TransitionRecovered} {
		t.Run(string(transition), func(t *testing.T) {
			s, _ := open(t)
			if transition == TransitionRecovered {
				if _, err := s.RecordBackupEvent(t.Context(), TransitionFiring, "failed", s.clk.Now()); err != nil {
					t.Fatal(err)
				}
			}
			before, err := s.BackupFailingSince(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			err = s.write(t.Context(), func(tx *sql.Tx) error {
				_, err := tx.Exec(`CREATE TRIGGER reject_backup_event BEFORE INSERT ON alert_event BEGIN SELECT RAISE(ABORT,'event rejected'); END`)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := s.RecordBackupEvent(t.Context(), transition, "transition", s.clk.Now())
			after, err := s.BackupFailingSince(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if writeErr == nil || !after.Equal(before) {
				t.Errorf("failed event changed marker: before=%s after=%s err=%v", before, after, writeErr)
			}
			err = s.write(t.Context(), func(tx *sql.Tx) error { _, err := tx.Exec("DROP TRIGGER reject_backup_event"); return err })
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.RecordBackupEvent(t.Context(), transition, "transition", s.clk.Now()); err != nil {
				t.Fatal(err)
			}
			after, err = s.BackupFailingSince(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if after.IsZero() != (transition == TransitionRecovered) {
				t.Errorf("committed event marker mismatch: transition=%s since=%s", transition, after)
			}
			var value string
			err = s.r.QueryRow("SELECT value FROM setting WHERE key='backup.config_failing_since'").Scan(&value)
			if transition == TransitionRecovered && !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("recovery did not delete marker: value=%s err=%v", value, err)
			}
		})
	}
}

func TestBackupEventsUseCurrentChannelsWithoutRuleState(t *testing.T) {
	s, _ := open(t)
	c, err := s.SaveNotifyChannel(t.Context(), NotifyChannel{Name: "backup", Kind: ChannelWebhook, Config: `{"url":"https://example.test"}`})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.SaveSettings(t.Context(), SiteSettingsUpdate{}, &BackupSettingsUpdate{Channels: &[]int64{c.ID}})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := s.RecordBackupEvent(t.Context(), TransitionFiring, "config 失败", s.clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if ev.RuleID != 0 || ev.NodeID != 0 || len(ev.Deliveries) != 1 || ev.Deliveries[0].ChannelID != c.ID {
		t.Fatalf("backup event=%+v", ev)
	}
	var states int
	if err := s.r.QueryRow("SELECT count(*) FROM alert_state").Scan(&states); err != nil {
		t.Fatal(err)
	}
	if states != 0 {
		t.Fatalf("backup created %d rule states", states)
	}
	if err := s.DeleteNotifyChannel(t.Context(), c.ID); err != nil {
		t.Fatal(err)
	}
	ev, err = s.RecordBackupEvent(t.Context(), TransitionRecovered, "config 已恢复", s.clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(ev.Deliveries) != 0 {
		t.Fatalf("backup referenced deleted channel: %+v", ev.Deliveries)
	}
	stored, err := s.GetAlertEvent(t.Context(), ev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Transition != TransitionRecovered || stored.Summary != "config 已恢复" {
		t.Fatalf("recovery not stored: %+v", stored)
	}
}

func TestBackupSuccessRejectsUnknownLayer(t *testing.T) {
	s, _ := open(t)
	if err := s.RecordBackupSuccess(t.Context(), "other"); err == nil {
		t.Fatal("unknown backup layer accepted")
	}
	times, err := s.BackupSuccessTimes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(times) != 0 {
		t.Fatalf("unknown layer created success: %+v", times)
	}
}
