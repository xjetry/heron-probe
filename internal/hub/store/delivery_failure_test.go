package store

import (
	"database/sql"
	"reflect"
	"slices"
	"testing"
)

func TestUpdateDeliveryRejectsInconsistentResult(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    DeliveryResult
	}{
		{"ok_with_failure", DeliveryResult{OK: true, Done: true, Failure: FailureTransport}},
		{"ok_with_status", DeliveryResult{OK: true, Done: true, HTTPStatus: 500}},
		{"ok_with_error", DeliveryResult{OK: true, Done: true, Error: "leftover"}},
		{"failure_without_category", DeliveryResult{Done: true, Error: "HTTP 500"}},
		{"http_status_without_status", DeliveryResult{Done: true, Failure: FailureHTTPStatus, Error: "body"}},
		{"http_status_below_range", DeliveryResult{Done: true, Failure: FailureHTTPStatus, HTTPStatus: 99}},
		{"http_status_above_range", DeliveryResult{Done: true, Failure: FailureHTTPStatus, HTTPStatus: 1000}},
		{"status_without_http_status", DeliveryResult{Done: true, Failure: FailureTransport, HTTPStatus: 502}},
		{"channel_deleted_with_error", DeliveryResult{Done: true, Failure: FailureChannelDeleted, Error: "channel deleted"}},
		{"result_unrecorded_with_error", DeliveryResult{Done: true, Failure: FailureResultUnrecorded, Error: "lost"}},
		{"unknown_category", DeliveryResult{Done: true, Failure: "timeout", Error: "late"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ids, cs, _ := alertFixture(t)
			r := saveRule(t, s, AlertRule{Kind: KindOffline})
			ev := recordEvent(t, s, r.ID, ids[0], []int64{cs[0].ID})
			if err := s.UpdateDelivery(t.Context(), ev.Deliveries[0].ID, tc.r); err == nil {
				t.Fatalf("inconsistent result accepted: %+v", tc.r)
			}
			got, err := s.GetAlertEvent(t.Context(), ev.ID)
			if err != nil || !reflect.DeepEqual(got.Deliveries, ev.Deliveries) {
				t.Fatalf("rejected result was written: %+v err=%v", got.Deliveries, err)
			}
		})
	}
}

func TestUpdateDeliveryStoresFailureAndClearsItOnSuccess(t *testing.T) {
	s, ids, cs, _ := alertFixture(t)
	r := saveRule(t, s, AlertRule{Kind: KindOffline})
	ev := recordEvent(t, s, r.ID, ids[0], []int64{cs[0].ID})
	id := ev.Deliveries[0].ID
	if err := s.UpdateDelivery(t.Context(), id, DeliveryResult{Failure: FailureHTTPStatus, HTTPStatus: 503, Error: "busy"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAlertEvent(t.Context(), ev.ID)
	if d := got.Deliveries[0]; err != nil || d.Failure != FailureHTTPStatus || d.HTTPStatus != 503 || d.LastError != "busy" || d.Done {
		t.Fatalf("failed attempt=%+v err=%v", d, err)
	}
	at := s.clk.Now()
	if err := s.UpdateDelivery(t.Context(), id, DeliveryResult{OK: true, Done: true, DeliveredAt: at}); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetAlertEvent(t.Context(), ev.ID)
	want := Delivery{ID: id, EventID: ev.ID, ChannelID: cs[0].ID, OK: true, Done: true, DeliveredAt: at}
	if err != nil || got.Deliveries[0] != want {
		t.Fatalf("delivered=%+v err=%v, want %+v", got.Deliveries[0], err, want)
	}
	var status sql.NullInt64
	if err := s.r.QueryRow("SELECT http_status FROM alert_delivery WHERE id = ?", id).Scan(&status); err != nil || status.Valid {
		t.Fatalf("http_status after success=%v err=%v, want NULL", status, err)
	}
}

func TestGetDeliveryError(t *testing.T) {
	s, ids, cs, _ := alertFixture(t)
	r := saveRule(t, s, AlertRule{Kind: KindOffline})
	ev := recordEvent(t, s, r.ID, ids[0], []int64{cs[0].ID})
	if err := s.UpdateDelivery(t.Context(), ev.Deliveries[0].ID, DeliveryResult{Done: true, Failure: FailureHTTPStatus, HTTPStatus: 401, Error: "echo secret"}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetDeliveryError(t.Context(), ev.Deliveries[0].ID); err != nil || got != "echo secret" {
		t.Fatalf("raw error=%q err=%v", got, err)
	}
	_, err := s.GetDeliveryError(t.Context(), 999)
	assertAlertNotFound(t, err, ObjectAlertDelivery, 999)
}

// schemaV6 冻结加入投递失败类别之前的完整 DDL。
var schemaV6 = append(slices.Clone(schemaV5), "CREATE TABLE api_token (\n  -- AUTOINCREMENT：id 永不复用。吊销按 id 进行，复用会让针对旧 token 的吊销落到新 token 上。\n  id INTEGER PRIMARY KEY AUTOINCREMENT,\n  name TEXT NOT NULL,\n  -- 整串明文（含前缀）的 SHA-256；明文不落库。\n  token_hash BLOB NOT NULL UNIQUE,\n  created_at INTEGER NOT NULL,\n  -- NULL 表示从未使用。只供展示：距已落库值满一分钟才刷新。\n  last_used_at INTEGER\n)")

func TestMigrationFromV6ClassifiesDeliveryFailures(t *testing.T) {
	seed := func(t *testing.T, db *sql.DB) {
		seedMinuteRow(t, db)
		for _, row := range []struct {
			id        int64
			ok        bool
			lastError string
		}{
			{1, false, "channel deleted"},
			{2, false, "attempts exhausted but last result was not recorded"},
			{3, false, "HTTP 401 Unauthorized: echoed {\"token\":\"x: y\"}"},
			{4, false, "Post: dial tcp: connection refused"},
			{5, true, ""},
			{6, false, ""},
		} {
			if _, err := db.Exec("INSERT INTO alert_delivery (id, event_id, channel_id, attempts, ok, done, last_error) VALUES (?, 1, 1, 1, ?, ?, ?)", row.id, row.ok, row.lastError != "" || row.ok, row.lastError); err != nil {
				t.Fatal(err)
			}
		}
	}
	migrated, fresh := migrateFrom(t, schemaV6, 6, seed)
	if got, want := describe(t, migrated.r), describe(t, fresh.r); !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated schema differs from fresh schema:\n got: %+v\nwant: %+v", got, want)
	}
	if v := userVersion(t, migrated.r); v != schemaVersion {
		t.Fatalf("user_version = %d, want %d", v, schemaVersion)
	}
	type row struct {
		failure   DeliveryFailure
		status    sql.NullInt64
		lastError string
	}
	for id, want := range map[int64]row{
		1: {FailureChannelDeleted, sql.NullInt64{}, ""},
		2: {FailureResultUnrecorded, sql.NullInt64{}, ""},
		3: {FailureHTTPStatus, sql.NullInt64{Int64: 401, Valid: true}, "echoed {\"token\":\"x: y\"}"},
		4: {FailureUnclassified, sql.NullInt64{}, "Post: dial tcp: connection refused"},
		5: {FailureNone, sql.NullInt64{}, ""},
		6: {FailureNone, sql.NullInt64{}, ""},
	} {
		var got row
		if err := migrated.r.QueryRow("SELECT failure, http_status, last_error FROM alert_delivery WHERE id = ?", id).Scan(&got.failure, &got.status, &got.lastError); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("delivery %d = %+v, want %+v", id, got, want)
		}
	}
}
