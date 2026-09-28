package store

import (
	"testing"
)

func TestBackupEventsUseCurrentChannelsWithoutRuleState(t *testing.T) {
	s, _ := open(t)
	c, err := s.SaveNotifyChannel(t.Context(), NotifyChannel{Name: "backup", Kind: ChannelWebhook, Config: `{"url":"https://example.test"}`})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.SaveSettings(t.Context(), SiteSettings{}, &BackupSettingsUpdate{Channels: []int64{c.ID}})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := s.RecordBackupEvent(t.Context(), TransitionFiring, "config 失败")
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
	ev, err = s.RecordBackupEvent(t.Context(), TransitionRecovered, "config 已恢复")
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
