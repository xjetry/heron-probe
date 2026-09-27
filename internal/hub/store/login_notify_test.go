package store

import (
	"database/sql"
	"slices"
	"testing"
)

func TestLoginNotifyDeleteIsAtomic(t *testing.T) {
	s, _, cs, _ := alertFixture(t)
	ids := []int64{cs[0].ID, cs[1].ID}
	if _, err := s.UpdateSettings(t.Context(), nil, &ids); err != nil {
		t.Fatal(err)
	}
	// 设置写失败时，渠道删除和列表摘除必须一起回滚。
	if err := s.write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TRIGGER reject_login_setting BEFORE UPDATE ON setting WHEN NEW.key='notify.login_channels' BEGIN SELECT RAISE(ABORT,'blocked setting update'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNotifyChannel(t.Context(), cs[0].ID); err == nil {
		t.Fatal("channel deletion succeeded despite failed settings update")
	}
	settings, err := s.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	channels, err := s.ListNotifyChannels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(settings.LoginChannelIDs, ids) || len(channels) != 2 {
		t.Fatalf("channel/settings transaction partially committed: %+v %+v", settings, channels)
	}
}

func TestLoginNotifyEventTransaction(t *testing.T) {
	s, _, cs, _ := alertFixture(t)
	ids := []int64{cs[0].ID, cs[1].ID}
	if _, err := s.UpdateSettings(t.Context(), nil, &ids); err != nil {
		t.Fatal(err)
	}
	if err := s.write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TRIGGER reject_login_delivery BEFORE INSERT ON alert_delivery WHEN NEW.channel_id=2 BEGIN SELECT RAISE(ABORT,'blocked delivery'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, err := s.RecordLoginEvent(t.Context(), AlertEvent{At: s.clk.Now(), Transition: TransitionLoginSuccess})
	if err == nil {
		t.Fatal("event insertion succeeded despite failed delivery insert")
	}
	assertAlertRows(t, s, "alert_event", "1=1", 0)
	assertAlertRows(t, s, "alert_delivery", "1=1", 0)
}
