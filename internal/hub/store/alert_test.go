package store

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func alertFixture(t *testing.T) (*Store, []int64, []NotifyChannel, uint64) {
	t.Helper()
	s, _ := open(t)
	var ids []int64
	var channels []NotifyChannel
	for i := range 2 {
		id, err := s.CreateNode(t.Context(), fmt.Sprint(i), hash(byte(i+1)))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		c, err := s.SaveNotifyChannel(t.Context(), NotifyChannel{Name: fmt.Sprint(i), Kind: ChannelWebhook, Config: `{}`})
		if err != nil {
			t.Fatal(err)
		}
		channels = append(channels, c)
	}
	task, _, err := s.SaveProbeTask(t.Context(), taskForTest(), []int64{ids[1], ids[0]})
	if err != nil {
		t.Fatal(err)
	}
	return s, ids, channels, task.Id
}

func saveRule(t *testing.T, s *Store, r AlertRule) AlertRule {
	t.Helper()
	r, err := s.SaveAlertRule(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func recordEvent(t *testing.T, s *Store, rule, node int64, channels []int64) AlertEvent {
	t.Helper()
	ev, err := s.RecordTransition(t.Context(), rule, node, StateFiring, AlertEvent{
		Transition: TransitionFiring, At: s.clk.Now(), Summary: "offline", Value: 42,
	}, channels)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func assertAlertRows(t *testing.T, s *Store, table, where string, want int) {
	t.Helper()
	var got int
	if err := s.r.QueryRow("SELECT COUNT(*) FROM " + table + " WHERE " + where).Scan(&got); err != nil || got != want {
		t.Fatalf("%s WHERE %s count=%d err=%v, want %d", table, where, got, err, want)
	}
}

func TestAlertRuleRoundTripAndScope(t *testing.T) {
	s, ids, cs, task := alertFixture(t)
	a := saveRule(t, s, AlertRule{Name: "offline", Kind: KindOffline, Enabled: true})
	b := saveRule(t, s, AlertRule{Name: "probe", Kind: KindProbe, Enabled: true, TaskID: task,
		Metric: MetricLossPct, Threshold: 25.5, ForMinutes: 3,
		NodeIDs: []int64{ids[1], ids[0], ids[1]}, ChannelIDs: []int64{cs[1].ID, cs[0].ID, cs[1].ID}})
	wantA := AlertRule{ID: 1, Name: "offline", Kind: KindOffline, Enabled: true, CreatedAt: s.clk.Now()}
	wantB := AlertRule{ID: 2, Name: "probe", Kind: KindProbe, Enabled: true, TaskID: task,
		Metric: MetricLossPct, Threshold: 25.5, ForMinutes: 3, NodeIDs: ids,
		ChannelIDs: []int64{cs[0].ID, cs[1].ID}, CreatedAt: s.clk.Now()}
	if !reflect.DeepEqual(a, wantA) || !reflect.DeepEqual(b, wantB) {
		t.Fatalf("saved rules=%+v/%+v, want %+v/%+v", a, b, wantA, wantB)
	}
	got, err := s.ListAlertRules(t.Context())
	if err != nil || !reflect.DeepEqual(got, []AlertRule{wantA, wantB}) {
		t.Fatalf("listed rules=%+v err=%v", got, err)
	}
	wantB.Name, wantB.Enabled, wantB.Metric, wantB.Threshold, wantB.ForMinutes = "changed", false, MetricRttMs, 100, 5
	wantB.NodeIDs, wantB.ChannelIDs = []int64{ids[1]}, []int64{cs[1].ID}
	b = saveRule(t, s, wantB)
	got, err = s.ListAlertRules(t.Context())
	if err != nil || !reflect.DeepEqual(got, []AlertRule{wantA, wantB}) || !reflect.DeepEqual(b, wantB) {
		t.Fatalf("replaced rules=%+v saved=%+v err=%v", got, b, err)
	}
	wantB.Kind, wantB.TaskID, wantB.Metric, wantB.Threshold, wantB.ForMinutes = KindOffline, 0, "", 0, 0
	wantB.NodeIDs, wantB.ChannelIDs = nil, nil
	saveRule(t, s, wantB)
	got, err = s.ListAlertRules(t.Context())
	if err != nil || !reflect.DeepEqual(got, []AlertRule{wantA, wantB}) {
		t.Fatalf("cleared scope=%+v err=%v", got, err)
	}
}

func TestSaveAlertRuleRejectsMissingReferences(t *testing.T) {
	s, _, _, task := alertFixture(t)
	for _, tc := range []struct {
		kind string
		r    AlertRule
	}{
		{"node", AlertRule{Kind: KindOffline, NodeIDs: []int64{999}}},
		{"notify channel", AlertRule{Kind: KindOffline, ChannelIDs: []int64{999}}},
		{"probe task", AlertRule{Kind: KindProbe, TaskID: 999}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			_, err := s.SaveAlertRule(t.Context(), tc.r)
			var missing NotFoundError
			if !errors.Is(err, ErrNotFound) || !errors.As(err, &missing) || missing.Kind != tc.kind || missing.ID != 999 {
				t.Fatalf("reference error=%v, detail=%+v", err, missing)
			}
			assertAlertRows(t, s, "alert_rule", "1", 0)
		})
	}
	r := saveRule(t, s, AlertRule{Name: "keep", Kind: KindProbe, TaskID: task})
	bad := r
	bad.Name, bad.ChannelIDs = "lost", []int64{999}
	if _, err := s.SaveAlertRule(t.Context(), bad); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad update=%v", err)
	}
	got, err := s.ListAlertRules(t.Context())
	if err != nil || !reflect.DeepEqual(got, []AlertRule{r}) {
		t.Fatalf("rejected save changed rule: %+v %v", got, err)
	}
}

func TestDeleteAlertRuleCascadesButKeepsEvents(t *testing.T) {
	s, ids, cs, _ := alertFixture(t)
	r := saveRule(t, s, AlertRule{Kind: KindOffline, NodeIDs: ids, ChannelIDs: []int64{cs[0].ID}})
	ev := recordEvent(t, s, r.ID, ids[0], []int64{cs[0].ID})
	if err := s.DeleteAlertRule(t.Context(), r.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"alert_rule", "alert_rule_node", "alert_rule_channel", "alert_state"} {
		assertAlertRows(t, s, table, "1", 0)
	}
	got, err := s.GetAlertEvent(t.Context(), ev.ID)
	if err != nil || !reflect.DeepEqual(got, ev) {
		t.Fatalf("event not retained: %+v %v", got, err)
	}
}

func TestDeleteNotifyChannelInUse(t *testing.T) {
	s, _, cs, _ := alertFixture(t)
	r := saveRule(t, s, AlertRule{Name: "bound", Kind: KindOffline, ChannelIDs: []int64{cs[0].ID}})
	assertInUse(t, s.DeleteNotifyChannel(t.Context(), cs[0].ID), "notify channel", cs[0].ID)
	r.ChannelIDs = nil
	saveRule(t, s, r)
	if err := s.DeleteNotifyChannel(t.Context(), cs[0].ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListNotifyChannels(t.Context())
	if err != nil || !reflect.DeepEqual(got, cs[1:]) {
		t.Fatalf("remaining channels=%+v err=%v", got, err)
	}
}

func assertInUse(t *testing.T, err error, kind string, id int64) {
	t.Helper()
	var used InUseError
	if !errors.Is(err, ErrInUse) || !errors.As(err, &used) || used.Kind != kind || used.ID != id || !reflect.DeepEqual(used.Rules, []string{"bound"}) || err.Error() != fmt.Sprintf("%s %d is referenced by alert rules: bound", kind, id) {
		t.Fatalf("reference deletion error=%v, detail=%+v", err, used)
	}
}

func TestDeleteProbeTaskInUseByRule(t *testing.T) {
	s, _, _, task := alertFixture(t)
	r := saveRule(t, s, AlertRule{Name: "bound", Kind: KindProbe, TaskID: task})
	_, err := s.DeleteProbeTask(t.Context(), task)
	assertInUse(t, err, "probe task", int64(task))
	if err := s.DeleteAlertRule(t.Context(), r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteProbeTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
}

func TestRecordTransitionWritesStateEventAndDeliveries(t *testing.T) {
	s, ids, cs, _ := alertFixture(t)
	r := saveRule(t, s, AlertRule{Kind: KindOffline})
	ev := recordEvent(t, s, r.ID, ids[0], []int64{cs[0].ID, cs[1].ID})
	want := AlertEvent{ID: 1, RuleID: r.ID, NodeID: ids[0], Transition: TransitionFiring,
		At: s.clk.Now(), Summary: "offline", Value: 42, Deliveries: []Delivery{
			{ID: 1, EventID: 1, ChannelID: cs[0].ID}, {ID: 2, EventID: 1, ChannelID: cs[1].ID},
		}}
	if !reflect.DeepEqual(ev, want) {
		t.Fatalf("transition event=%+v want=%+v", ev, want)
	}
	got, err := s.GetAlertEvent(t.Context(), ev.ID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted event=%+v err=%v", got, err)
	}
	states, err := s.ListAlertStates(t.Context())
	if err != nil || !reflect.DeepEqual(states, []StateRow{{RuleID: r.ID, NodeID: ids[0], State: StateFiring, SinceAt: ev.At}}) {
		t.Fatalf("transition states=%+v err=%v", states, err)
	}
}

func TestRecordTransitionRollsBackOnDeliveryFailure(t *testing.T) {
	s, ids, cs, _ := alertFixture(t)
	r := saveRule(t, s, AlertRule{Kind: KindOffline})
	if err := s.write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec("CREATE TRIGGER reject_delivery BEFORE INSERT ON alert_delivery WHEN NEW.channel_id = 2 BEGIN SELECT RAISE(ABORT, 'delivery rejected'); END")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, err := s.RecordTransition(t.Context(), r.ID, ids[0], StateFiring, AlertEvent{At: s.clk.Now()}, []int64{cs[0].ID, cs[1].ID})
	if err == nil {
		t.Fatal("delivery failure was accepted")
	}
	for _, table := range []string{"alert_state", "alert_event", "alert_delivery"} {
		assertAlertRows(t, s, table, "1", 0)
	}
}

func TestUpdateDeliveryAndPending(t *testing.T) {
	s, ids, cs, _ := alertFixture(t)
	r := saveRule(t, s, AlertRule{Kind: KindOffline})
	ev := recordEvent(t, s, r.ID, ids[0], []int64{cs[0].ID, cs[1].ID})
	for _, attempts := range []int{1, 2, 3} {
		if err := s.UpdateDelivery(t.Context(), ev.Deliveries[0].ID, attempts, false, "failed", time.Time{}); err != nil {
			t.Fatal(err)
		}
		got, err := s.PendingDeliveries(t.Context(), 3)
		want := []Delivery{{ID: 1, EventID: ev.ID, ChannelID: cs[0].ID, Attempts: attempts, LastError: "failed"}, ev.Deliveries[1]}
		if attempts == 3 {
			want = want[1:]
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("pending attempt %d=%+v err=%v, want %+v", attempts, got, err, want)
		}
	}
	at := s.clk.Now().Add(time.Minute)
	if err := s.UpdateDelivery(t.Context(), ev.Deliveries[1].ID, 1, true, "", at); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingDeliveries(t.Context(), 3)
	if err != nil || len(pending) != 0 {
		t.Fatalf("successful delivery still pending: %+v %v", pending, err)
	}
	got, err := s.GetAlertEvent(t.Context(), ev.ID)
	want := Delivery{ID: 2, EventID: ev.ID, ChannelID: cs[1].ID, Attempts: 1, OK: true, DeliveredAt: at}
	if err != nil || len(got.Deliveries) != 2 || got.Deliveries[1] != want {
		t.Fatalf("delivered event=%+v err=%v", got, err)
	}
}

func TestListAlertEventsCursor(t *testing.T) {
	s, ids, cs, _ := alertFixture(t)
	r := saveRule(t, s, AlertRule{Kind: KindOffline})
	var events []AlertEvent
	for i := range 5 {
		events = append(events, recordEvent(t, s, r.ID, ids[i%2], []int64{cs[0].ID}))
	}
	for _, tc := range []struct {
		node, before int64
		want         []AlertEvent
	}{
		{0, 0, []AlertEvent{events[4], events[3]}},
		{0, events[3].ID, []AlertEvent{events[2], events[1]}},
		{ids[0], 0, []AlertEvent{events[4], events[2]}},
	} {
		got, err := s.ListAlertEvents(t.Context(), tc.node, tc.before, 2)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("cursor node=%d before=%d: got=%+v err=%v want=%+v", tc.node, tc.before, got, err, tc.want)
		}
	}
}

func TestDeleteNodeCleansAlertScopeAndState(t *testing.T) {
	s, ids, cs, _ := alertFixture(t)
	r := saveRule(t, s, AlertRule{Kind: KindOffline, NodeIDs: ids})
	ev := recordEvent(t, s, r.ID, ids[0], []int64{cs[0].ID})
	if err := s.SetAlertState(t.Context(), r.ID, ids[1], StatePending, s.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNode(t.Context(), ids[0]); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"alert_rule_node", "alert_state"} {
		assertAlertRows(t, s, table, fmt.Sprintf("node_id = %d", ids[0]), 0)
		assertAlertRows(t, s, table, fmt.Sprintf("node_id = %d", ids[1]), 1)
	}
	assertAlertRows(t, s, "alert_rule", "1", 1)
	got, err := s.GetAlertEvent(t.Context(), ev.ID)
	if err != nil || !reflect.DeepEqual(got, ev) {
		t.Fatalf("node deletion erased event: %+v %v", got, err)
	}
}

func TestNodeOfflineGraceRoundTrip(t *testing.T) {
	s, ids, _, _ := alertFixture(t)
	for _, grace := range []int{90, 0} {
		if err := s.UpdateNode(t.Context(), ids[0], "n", false, "", 1, grace); err != nil {
			t.Fatal(err)
		}
		var raw sql.NullInt64
		if err := s.r.QueryRow("SELECT offline_grace_s FROM node WHERE id = ?", ids[0]).Scan(&raw); err != nil || raw.Valid != (grace != 0) || raw.Int64 != int64(grace) {
			t.Fatalf("grace column=%v err=%v want=%d", raw, err, grace)
		}
		n, err := s.GetNode(t.Context(), ids[0])
		if err != nil || n.OfflineGraceS != grace {
			t.Fatalf("grace node=%+v err=%v want=%d", n, err, grace)
		}
	}
}

func TestCountsIncludesAlertTables(t *testing.T) {
	s, _ := open(t)
	got, err := s.Counts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"alert_rule", "alert_rule_node", "alert_rule_channel", "notify_channel", "alert_state", "alert_event", "alert_delivery"} {
		if n, ok := got[table]; !ok || n != 0 {
			t.Fatalf("Counts[%s]=%d exists=%v", table, n, ok)
		}
	}
}

func TestDeleteAlertStatesKeepsOnlyRequestedNodes(t *testing.T) {
	s, ids, _, _ := alertFixture(t)
	a := saveRule(t, s, AlertRule{Kind: KindOffline})
	b := saveRule(t, s, AlertRule{Kind: KindOffline})
	for _, rule := range []int64{a.ID, b.ID} {
		for _, node := range ids {
			if err := s.SetAlertState(t.Context(), rule, node, StateOK, s.clk.Now()); err != nil {
				t.Fatal(err)
			}
			if err := s.SetAlertState(t.Context(), rule, node, StatePending, s.clk.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.DeleteAlertStates(t.Context(), a.ID, []int64{ids[1], ids[1]}); err != nil {
		t.Fatal(err)
	}
	want := []StateRow{{a.ID, ids[1], StatePending, s.clk.Now().Add(time.Second)}, {b.ID, ids[0], StatePending, s.clk.Now().Add(time.Second)}, {b.ID, ids[1], StatePending, s.clk.Now().Add(time.Second)}}
	got, err := s.ListAlertStates(t.Context())
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("kept states=%+v err=%v want=%+v", got, err, want)
	}
	if err := s.DeleteAlertStates(t.Context(), a.ID, nil); err != nil {
		t.Fatal(err)
	}
	got, err = s.ListAlertStates(t.Context())
	if err != nil || !reflect.DeepEqual(got, want[1:]) {
		t.Fatalf("clear rule states=%+v err=%v", got, err)
	}
	assertAlertRows(t, s, "alert_event", "1", 0)
}

func TestProbeTaskNodeIDs(t *testing.T) {
	s, ids, _, task := alertFixture(t)
	got, err := s.ProbeTaskNodeIDs(t.Context(), task)
	if err != nil || !reflect.DeepEqual(got, ids) {
		t.Fatalf("assigned nodes=%v err=%v want=%v", got, err, ids)
	}
	got, err = s.ProbeTaskNodeIDs(t.Context(), task+1)
	if err != nil || len(got) != 0 {
		t.Fatalf("missing task nodes=%v err=%v", got, err)
	}
}

func TestNotifyChannelRoundTrip(t *testing.T) {
	s, _, cs, _ := alertFixture(t)
	want := []NotifyChannel{
		{ID: cs[0].ID, Name: "0", Kind: ChannelWebhook, Config: `{}`, CreatedAt: s.clk.Now()},
		{ID: cs[1].ID, Name: "1", Kind: ChannelWebhook, Config: `{}`, CreatedAt: s.clk.Now()},
	}
	got, err := s.ListNotifyChannels(t.Context())
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("channels=%+v err=%v", got, err)
	}
	want[0].Name, want[0].Kind, want[0].Config = "changed", ChannelTelegram, `{"chat_id":"42"}`
	updated, err := s.SaveNotifyChannel(t.Context(), want[0])
	if err != nil || updated != want[0] {
		t.Fatalf("updated channel=%+v err=%v", updated, err)
	}
	got, err = s.ListNotifyChannels(t.Context())
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("updated channels=%+v err=%v", got, err)
	}
}
