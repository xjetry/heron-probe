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
	if err != nil || got != (SiteSettings{Theme: DefaultTheme, PublicEnabled: true}) {
		t.Fatalf("never saved: %+v %v", got, err)
	}
	full := SiteSettings{Title: "状态", Theme: "dark", AccentColor: "#112233", Logo: "data:image/png;base64,AAAA", CustomCSS: "body{}"}
	if err := s.SaveSiteSettings(t.Context(), full); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SiteSettings(t.Context()); err != nil || got != full {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	// 整体替换：空串写入，表示该项回到默认，不是"不改"。
	if err := s.SaveSiteSettings(t.Context(), SiteSettings{Theme: "auto"}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SiteSettings(t.Context()); err != nil || got != (SiteSettings{Theme: "auto"}) {
		t.Fatalf("replacement kept old values: %+v %v", got, err)
	}
	if n := rowCounts(t, s)["setting"]; n != 6 {
		t.Fatalf("setting rows = %d, want 6", n)
	}
}

// 保存中途失败时库里仍是上一套完整设置：全部键在同一个写事务里，任一条失败整体回滚。
// 触发器拦 CSS 键，拆成逐键提交的实现会留下前四个新值。
func TestSaveSiteSettingsIsAllOrNothing(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	first := SiteSettings{Title: "旧", Theme: "light", AccentColor: "#111111", Logo: "data:image/png;base64,AAAA", CustomCSS: "a{}"}
	if err := s.SaveSiteSettings(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("CREATE TRIGGER reject_css BEFORE INSERT ON setting WHEN NEW.key = 'site.custom_css' BEGIN SELECT RAISE(ABORT, 'css rejected'); END")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	err := s.SaveSiteSettings(ctx, SiteSettings{Title: "新", Theme: "dark", AccentColor: "#222222", CustomCSS: "b{}"})
	if err == nil || !strings.Contains(err.Error(), "css rejected") {
		t.Fatalf("save error = %v", err)
	}
	if got, err := s.SiteSettings(ctx); err != nil || got != first {
		t.Fatalf("failed save left %+v %v, want %+v", got, err, first)
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
	if st, err := migrated.SiteSettings(t.Context()); err != nil || st != (SiteSettings{Theme: DefaultTheme, PublicEnabled: true}) {
		t.Fatalf("settings after migration: %+v %v", st, err)
	}
}
