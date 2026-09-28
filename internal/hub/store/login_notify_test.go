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

// 系统事件的种类由 transition 决定；写侧各收各的 transition，被拒时什么都不写。库里 0/0 的行因此都带系统
// 事件的 transition、规则事件的行都不带，投递队列按 transition 推出的标签才与 0/0 一致。
func TestSystemEventTransitionsStayWithTheirWriter(t *testing.T) {
	for tr, want := range map[Transition]string{TransitionLoginSuccess: SystemKindLogin, TransitionLoginLocked: SystemKindLogin, TransitionFiring: "", TransitionRecovered: "", "": ""} {
		if kind, ok := SystemEventKind(tr); kind != want || ok != (want != "") {
			t.Errorf("SystemEventKind(%q) = %q, %v; want %q", tr, kind, ok, want)
		}
	}
	s, ids, cs, _ := alertFixture(t)
	channels := []int64{cs[0].ID}
	if _, err := s.UpdateSettings(t.Context(), nil, &channels); err != nil {
		t.Fatal(err)
	}
	r := saveRule(t, s, AlertRule{Name: "offline", Kind: KindOffline, AllNodes: true})
	for _, tr := range []Transition{TransitionFiring, TransitionRecovered, ""} {
		if _, err := s.RecordLoginEvent(t.Context(), AlertEvent{At: s.clk.Now(), Transition: tr}); err == nil {
			t.Errorf("login event accepted transition %q", tr)
		}
	}
	for _, tr := range []Transition{TransitionLoginSuccess, TransitionLoginLocked} {
		if _, err := s.RecordTransition(t.Context(), r.ID, ids[0], StateFiring, "", AlertEvent{At: s.clk.Now(), Transition: tr}, channels); err == nil {
			t.Errorf("rule event accepted the system transition %q", tr)
		}
	}
	assertAlertRows(t, s, "alert_event", "1=1", 0)
	assertAlertRows(t, s, "alert_state", "1=1", 0)
}
