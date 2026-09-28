package store

import (
	"database/sql"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestSiteSettingsDefaultAndWholeReplacement(t *testing.T) {
	s, _ := open(t)
	got, err := s.SiteSettings(t.Context())
	if err != nil || got != (SiteSettings{Theme: DefaultTheme}) {
		t.Fatalf("never saved: %+v %v", got, err)
	}
	full := SiteSettings{Title: "状态", Theme: "dark", AccentColor: "#112233", Logo: "data:image/png;base64,AAAA", CustomCSS: "body{}"}
	if _, err := s.UpdateSettings(t.Context(), &full, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SiteSettings(t.Context()); err != nil || got != full {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	// 整体替换：空串写入，表示该项回到默认，不是"不改"。
	if _, err := s.UpdateSettings(t.Context(), &SiteSettings{Theme: "auto"}, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SiteSettings(t.Context()); err != nil || got != (SiteSettings{Theme: "auto"}) {
		t.Fatalf("replacement kept old values: %+v %v", got, err)
	}
	if n := rowCounts(t, s)["setting"]; n != 5 {
		t.Fatalf("setting rows = %d, want 5", n)
	}
}

// 保存中途失败时库里仍是上一套完整外观：五个键在同一个写事务里，任一条失败整体回滚。
// 触发器只拦最后一个键，拆成逐键提交的实现会留下前四个新值。
func TestUpdateSettingsAppearanceIsAllOrNothing(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	first := SiteSettings{Title: "旧", Theme: "light", AccentColor: "#111111", Logo: "data:image/png;base64,AAAA", CustomCSS: "a{}"}
	if _, err := s.UpdateSettings(ctx, &first, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("CREATE TRIGGER reject_css BEFORE INSERT ON setting WHEN NEW.key = 'site.custom_css' BEGIN SELECT RAISE(ABORT, 'css rejected'); END")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, err := s.UpdateSettings(ctx, &SiteSettings{Title: "新", Theme: "dark", AccentColor: "#222222", CustomCSS: "b{}"}, nil)
	if err == nil || !strings.Contains(err.Error(), "css rejected") {
		t.Fatalf("save error = %v", err)
	}
	if got, err := s.SiteSettings(ctx); err != nil || got != first {
		t.Fatalf("failed save left %+v %v, want %+v", got, err, first)
	}
}

// 公开页只读外观，不读登录通知的渠道列表：列表的值损坏时管理设置报错并点名键，公开页照常。
func TestSiteSettingsDoNotReadLoginChannels(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	site := SiteSettings{Title: "状态", Theme: "dark"}
	if _, err := s.UpdateSettings(ctx, &site, &[]int64{}); err != nil {
		t.Fatal(err)
	}
	if err := s.write(ctx, func(tx *sql.Tx) error { return saveSetting(tx, loginChannelsKey, "not json") }); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SiteSettings(ctx); err != nil || got != site {
		t.Fatalf("public appearance with a corrupt login channel list: %+v %v", got, err)
	}
	if _, err := s.Settings(ctx); err == nil || !strings.Contains(err.Error(), loginChannelsKey) {
		t.Fatalf("admin settings with a corrupt login channel list: err = %v, want one naming %s", err, loginChannelsKey)
	}
}

// v7 的完整 DDL：v6 加上 migrateDeliveryFailure 的两条 ALTER。
var schemaV7 = append(slices.Clone(schemaV6),
	"ALTER TABLE alert_delivery ADD COLUMN failure TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE alert_delivery ADD COLUMN http_status INTEGER")

func TestMigrationFromV7MatchesFreshSchemaAndKeepsRows(t *testing.T) {
	migrated, fresh := migrateFrom(t, schemaV7, 7, seedMinuteRow)
	if got, want := describe(t, migrated.r), describe(t, fresh.r); !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated schema differs from fresh schema:\n got: %+v\nwant: %+v", got, want)
	}
	if v := userVersion(t, migrated.r); v != schemaVersion {
		t.Fatalf("user_version = %d, want %d", v, schemaVersion)
	}
	rows, err := migrated.ReadMinuteRows(t.Context(), 7, 0, 120)
	if err != nil || len(rows) != 1 {
		t.Fatalf("minute row lost across migration: %v %v", rows, err)
	}
	if st, err := migrated.SiteSettings(t.Context()); err != nil || st != (SiteSettings{Theme: DefaultTheme}) {
		t.Fatalf("settings after migration: %+v %v", st, err)
	}
}
