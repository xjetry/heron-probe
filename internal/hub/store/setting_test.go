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
	if _, _, err := s.SaveSettings(t.Context(), SettingsUpdate{Appearance: &full.SiteAppearance, PublicEnabled: &full.PublicEnabled}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SiteSettings(t.Context()); err != nil || got != full {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	// 整体替换：空串写入，表示该项回到默认，不是"不改"。
	if _, _, err := s.SaveSettings(t.Context(), SettingsUpdate{Appearance: &SiteAppearance{Theme: "auto"}}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SiteSettings(t.Context()); err != nil || got != (SiteSettings{Theme: "auto"}) {
		t.Fatalf("replacement kept old values: %+v %v", got, err)
	}
	if n := rowCounts(t, s)["setting"]; n != 6 {
		t.Fatalf("setting rows = %d, want 6", n)
	}
}

// 外观的每个字段都要经 fields 登记才存得进库。逐字段填入各不相同的值（用字段名），保存后必须原样读回：
// 漏登记的字段读回空串，两个字段共用一个键则其中一个读回另一个的值。字段按反射枚举，新增的字段自动纳入。
func TestSiteAppearanceRoundTripsEveryField(t *testing.T) {
	s, _ := open(t)
	var want SiteAppearance
	v := reflect.ValueOf(&want).Elem()
	for i := range v.NumField() {
		v.Field(i).SetString(v.Type().Field(i).Name)
	}
	if _, _, err := s.SaveSettings(t.Context(), SettingsUpdate{Appearance: &want}); err != nil {
		t.Fatal(err)
	}
	got, err := s.SiteSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got.SiteAppearance != want {
		t.Fatalf("appearance did not round-trip: got %+v, want %+v", got.SiteAppearance, want)
	}
}

// 保存中途失败时库里仍是上一套完整设置，内存里的总闸也不变：全部键在同一个写事务里，任一条失败整体回滚，失败不发布。
// 触发器拦写入顺序里最后一个键 geo.url，拆成逐键提交的实现会留下它之前的外观、总闸与查询开关的新值。
func TestSaveSettingsIsAllOrNothing(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	first := SiteSettings{Title: "旧", Theme: "light", AccentColor: "#111111", Logo: "data:image/png;base64,AAAA", CustomCSS: "a{}", PublicEnabled: true}
	firstGeo := GeoSettings{URL: "https://old.example/{ip}"}
	if _, _, err := s.SaveSettings(ctx, SettingsUpdate{Appearance: &first.SiteAppearance, PublicEnabled: &first.PublicEnabled, Geo: GeoUpdate{Enabled: &firstGeo.Enabled, URL: &firstGeo.URL}}); err != nil {
		t.Fatal(err)
	}
	if err := s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("CREATE TRIGGER reject_url BEFORE INSERT ON setting WHEN NEW.key = 'geo.url' BEGIN SELECT RAISE(ABORT, 'url rejected'); END")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	closed, on, url := false, true, "https://new.example/{ip}"
	_, _, err := s.SaveSettings(ctx, SettingsUpdate{
		Appearance:    &SiteAppearance{Title: "新", Theme: "dark", AccentColor: "#222222", CustomCSS: "b{}"},
		PublicEnabled: &closed,
		Geo:           GeoUpdate{Enabled: &on, URL: &url},
	})
	if err == nil || !strings.Contains(err.Error(), "url rejected") {
		t.Fatalf("save error = %v", err)
	}
	if site, geo, err := s.Settings(ctx); err != nil || site != first || geo != firstGeo {
		t.Fatalf("failed save left %+v %+v %v, want %+v %+v", site, geo, err, first, firstGeo)
	}
	if !s.PublicEnabled() {
		t.Fatal("failed save published the closed gate")
	}
}

// 各组彼此独立：只给总闸或只给国家查询的保存不写外观键，库里的外观原样保留；回显的外观在同一个写事务里读回，
// 是库里的值而不是空的外观。
func TestSaveSettingsLeavesAbsentAppearance(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	full := SiteAppearance{Title: "状态", Theme: "dark", AccentColor: "#112233", Logo: "data:image/png;base64,AAAA", CustomCSS: "body{}"}
	if _, _, err := s.SaveSettings(ctx, SettingsUpdate{Appearance: &full}); err != nil {
		t.Fatal(err)
	}
	closed, on := false, true
	for _, c := range []struct {
		name string
		in   SettingsUpdate
	}{
		{"public_enabled only", SettingsUpdate{PublicEnabled: &closed}},
		{"geo only", SettingsUpdate{Geo: GeoUpdate{Enabled: &on}}},
	} {
		site, _, err := s.SaveSettings(ctx, c.in)
		if err != nil || site.SiteAppearance != full {
			t.Fatalf("%s: echoed appearance %+v %v, want %+v", c.name, site.SiteAppearance, err, full)
		}
		if got, err := s.SiteSettings(ctx); err != nil || got.SiteAppearance != full {
			t.Fatalf("%s: stored appearance %+v %v, want %+v", c.name, got.SiteAppearance, err, full)
		}
	}
	if site, geo, err := s.Settings(ctx); err != nil || site.PublicEnabled || !geo.Enabled {
		t.Fatalf("groups not applied: %+v %+v %v", site, geo, err)
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
