package store

import (
	"database/sql"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// v16 的完整 DDL：v15 加上公开页主题的两张表与启用唯一索引。
var schemaV16 = append(slices.Clone(schemaV15),
	"CREATE TABLE theme (id TEXT PRIMARY KEY, name TEXT NOT NULL, version TEXT NOT NULL, preview TEXT NOT NULL, uploaded_at INTEGER NOT NULL, enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)))",
	"CREATE UNIQUE INDEX theme_enabled ON theme (enabled) WHERE enabled = 1",
	"CREATE TABLE theme_file (theme_id TEXT NOT NULL, path TEXT NOT NULL, content BLOB NOT NULL, PRIMARY KEY (theme_id, path))")

// 升级前每行各自发送过：旧行各成一批，未终态的仍按原行续投；旧渠道按种类取缺省节奏。
func TestMigrationFromV16AddsBatchesAndChannelRates(t *testing.T) {
	migrated, fresh := migrateFrom(t, schemaV16, 16, func(t *testing.T, db *sql.DB) {
		for _, stmt := range []string{
			"INSERT INTO notify_channel (id, name, kind, config, created_at) VALUES (1, 'tg', 'telegram', '{}', 1), (2, 'hook', 'webhook', '{}', 1)",
			"INSERT INTO alert_event (id, rule_id, node_id, transition, at, summary, value) VALUES (5, 3, 7, 'firing', 1, 's', 0)",
			"INSERT INTO alert_delivery (id, event_id, channel_id, attempts, done) VALUES (10, 5, 1, 1, 0), (11, 5, 2, 3, 1)",
		} {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatal(err)
			}
		}
	})
	if got, want := describe(t, migrated.r), describe(t, fresh.r); !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated schema differs from fresh schema:\n got: %+v\nwant: %+v", got, want)
	}
	if v := userVersion(t, migrated.r); v != schemaVersion {
		t.Fatalf("user_version = %d, want %d", v, schemaVersion)
	}
	channels, err := migrated.ListNotifyChannels(t.Context())
	if err != nil || len(channels) != 2 || channels[0].RatePerMinute != 20 || channels[1].RatePerMinute != 0 {
		t.Fatalf("channels after migration: %+v %v, want telegram 20 and webhook 0", channels, err)
	}
	ev, err := migrated.GetAlertEvent(t.Context(), 5)
	if err != nil || len(ev.Deliveries) != 2 || ev.Deliveries[0].BatchID != 10 || ev.Deliveries[1].BatchID != 11 {
		t.Fatalf("deliveries after migration: %+v %v, want each row its own batch", ev.Deliveries, err)
	}
	if pending, err := migrated.PendingBatches(t.Context()); err != nil || !slices.Equal(pending, []int64{10}) {
		t.Fatalf("pending batches after migration: %v %v, want [10]", pending, err)
	}
}

func recordTargets(t *testing.T, s *Store, rule, node int64, targets ...DeliveryTarget) AlertEvent {
	t.Helper()
	ev, err := s.RecordTransition(t.Context(), rule, node, StateFiring, "", time.Time{}, AlertEvent{Transition: TransitionFiring, At: s.clk.Now(), Summary: "down"}, targets)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

// 新行只加入还没开始尝试、且发往同一渠道的批次；其余情形新开一批（批次号即自己的 id）。
func TestRecordTransitionJoinsOnlyOpenBatchOfSameChannel(t *testing.T) {
	s, ids, cs, _ := alertFixture(t)
	r := saveRule(t, s, AlertRule{Kind: KindOffline})
	first := recordTargets(t, s, r.ID, ids[0], DeliveryTarget{ChannelID: cs[0].ID})
	batch := first.Deliveries[0].BatchID
	if batch != first.Deliveries[0].ID {
		t.Fatalf("new batch id = %d, want the row id %d", batch, first.Deliveries[0].ID)
	}
	joined := recordTargets(t, s, r.ID, ids[1], DeliveryTarget{ChannelID: cs[0].ID, Batch: batch}, DeliveryTarget{ChannelID: cs[1].ID, Batch: batch})
	if got := joined.Deliveries[0].BatchID; got != batch {
		t.Fatalf("same-channel row joined batch %d, want %d", got, batch)
	}
	if d := joined.Deliveries[1]; d.BatchID != d.ID {
		t.Fatalf("other-channel row joined batch %d, want its own batch %d", d.BatchID, d.ID)
	}
	if ghost := recordTargets(t, s, r.ID, ids[0], DeliveryTarget{ChannelID: cs[0].ID, Batch: 999}).Deliveries[0]; ghost.BatchID != ghost.ID {
		t.Fatalf("row asking for a missing batch got batch %d, want its own %d", ghost.BatchID, ghost.ID)
	}

	ds, err := s.BeginBatchAttempt(t.Context(), batch)
	if err != nil || len(ds) != 2 || ds[0].ID != first.Deliveries[0].ID || ds[1].ID != joined.Deliveries[0].ID || ds[0].Attempts != 1 || ds[1].Attempts != 1 {
		t.Fatalf("batch attempt rows=%+v err=%v", ds, err)
	}
	if late := recordTargets(t, s, r.ID, ids[0], DeliveryTarget{ChannelID: cs[0].ID, Batch: batch}).Deliveries[0]; late.BatchID != late.ID {
		t.Fatalf("row joining a started batch got batch %d, want its own %d", late.BatchID, late.ID)
	}

	// 终态的批次同样不再接纳新行：它已不会再发送。
	ended := recordTargets(t, s, r.ID, ids[1], DeliveryTarget{ChannelID: cs[1].ID}).Deliveries[0]
	if err := s.UpdateBatch(t.Context(), ended.BatchID, DeliveryResult{Done: true, Failure: FailureChannelInvalid, Error: "bad"}); err != nil {
		t.Fatal(err)
	}
	if after := recordTargets(t, s, r.ID, ids[1], DeliveryTarget{ChannelID: cs[1].ID, Batch: ended.BatchID}).Deliveries[0]; after.BatchID != after.ID {
		t.Fatalf("row joining a finished batch got batch %d, want its own %d", after.BatchID, after.ID)
	}
}

// 一次尝试与它的结果写到整批的每一行，别的批次不受影响；批次读出的行与事件一一对应。
func TestBatchAttemptAndResultCoverWholeBatch(t *testing.T) {
	s, ids, cs, _ := alertFixture(t)
	r := saveRule(t, s, AlertRule{Kind: KindOffline})
	a := recordTargets(t, s, r.ID, ids[0], DeliveryTarget{ChannelID: cs[0].ID})
	batch := a.Deliveries[0].BatchID
	b := recordTargets(t, s, r.ID, ids[1], DeliveryTarget{ChannelID: cs[0].ID, Batch: batch})
	other := recordTargets(t, s, r.ID, ids[0], DeliveryTarget{ChannelID: cs[0].ID})
	if _, err := s.BeginBatchAttempt(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateBatch(t.Context(), batch, DeliveryResult{Failure: FailureHTTPStatus, HTTPStatus: 503, Error: "busy"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDeliveryBatch(t.Context(), batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Deliveries) != 2 || len(got.Events) != 2 || got.ID != batch {
		t.Fatalf("batch=%+v", got)
	}
	for i, d := range got.Deliveries {
		if d.Attempts != 1 || d.Done || d.Failure != FailureHTTPStatus || d.HTTPStatus != 503 || d.LastError != "busy" || d.BatchID != batch {
			t.Fatalf("row %d after a failed attempt=%+v", i, d)
		}
		if got.Events[i].ID != d.EventID || got.Events[i].Summary != "down" || got.Events[i].Deliveries != nil {
			t.Fatalf("event %d=%+v does not belong to row %+v", i, got.Events[i], d)
		}
	}
	if got.Events[0].ID != a.ID || got.Events[1].ID != b.ID {
		t.Fatalf("batch events=%d,%d want %d,%d in row order", got.Events[0].ID, got.Events[1].ID, a.ID, b.ID)
	}
	at := s.clk.Now()
	if _, err := s.BeginBatchAttempt(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateBatch(t.Context(), batch, DeliveryResult{OK: true, Done: true, DeliveredAt: at}); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetDeliveryBatch(t.Context(), batch)
	if err != nil {
		t.Fatal(err)
	}
	for i, d := range got.Deliveries {
		if d.Attempts != 2 || !d.OK || !d.Done || d.Failure != FailureNone || d.HTTPStatus != 0 || d.LastError != "" || !d.DeliveredAt.Equal(at) {
			t.Fatalf("row %d after success=%+v", i, d)
		}
	}
	untouched, err := s.GetAlertEvent(t.Context(), other.ID)
	if err != nil || !reflect.DeepEqual(untouched.Deliveries, other.Deliveries) {
		t.Fatalf("other batch=%+v err=%v, want untouched %+v", untouched.Deliveries, err, other.Deliveries)
	}
}

// 同批各行次数不一致说明不变式已被破坏：报错并回滚，不按其中某行的次数发送。
func TestBeginBatchAttemptRejectsNonUniformBatch(t *testing.T) {
	s, ids, cs, _ := alertFixture(t)
	r := saveRule(t, s, AlertRule{Kind: KindOffline})
	a := recordTargets(t, s, r.ID, ids[0], DeliveryTarget{ChannelID: cs[0].ID})
	batch := a.Deliveries[0].BatchID
	b := recordTargets(t, s, r.ID, ids[1], DeliveryTarget{ChannelID: cs[0].ID, Batch: batch})
	if err := s.write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE alert_delivery SET attempts = 1 WHERE id = ?", b.Deliveries[0].ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginBatchAttempt(t.Context(), batch); err == nil || !strings.Contains(err.Error(), "not uniform") {
		t.Fatalf("non-uniform batch attempt err=%v", err)
	}
	got, err := s.GetDeliveryBatch(t.Context(), batch)
	if err != nil || got.Deliveries[0].Attempts != 0 || got.Deliveries[1].Attempts != 1 {
		t.Fatalf("rows after refused attempt=%+v err=%v, want the attempt rolled back", got.Deliveries, err)
	}
}

// 批次号必须指向一行（正数）；节奏上限不能为负。两条约束由列上的 CHECK 承载，绕开写入口也写不进去。
func TestDeliveryBatchAndRateColumnsRejectInvalidValues(t *testing.T) {
	s, ids, cs, _ := alertFixture(t)
	r := saveRule(t, s, AlertRule{Kind: KindOffline})
	ev := recordTargets(t, s, r.ID, ids[0], DeliveryTarget{ChannelID: cs[0].ID})
	for _, v := range []int64{0, -1} {
		err := s.write(t.Context(), func(tx *sql.Tx) error {
			_, err := tx.Exec("UPDATE alert_delivery SET batch_id = ? WHERE id = ?", v, ev.Deliveries[0].ID)
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
			t.Fatalf("batch_id %d accepted: %v", v, err)
		}
	}
	if _, err := s.SaveNotifyChannel(t.Context(), NotifyChannel{Name: "neg", Kind: ChannelTelegram, Config: `{}`, RatePerMinute: -1}); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("negative rate accepted: %v", err)
	}
	saved, err := s.SaveNotifyChannel(t.Context(), NotifyChannel{Name: "tg", Kind: ChannelTelegram, Config: `{}`, RatePerMinute: 7})
	if err != nil {
		t.Fatal(err)
	}
	saved.RatePerMinute = 0
	if _, err := s.SaveNotifyChannel(t.Context(), saved); err != nil {
		t.Fatal(err)
	}
	channels, err := s.ListNotifyChannels(t.Context())
	if err != nil || channels[len(channels)-1].RatePerMinute != 0 || channels[len(channels)-1].ID != saved.ID {
		t.Fatalf("rate after update=%+v %v, want 0", channels, err)
	}
}
