package store

import (
	"database/sql"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/xjetry/probe/internal/sqlitetest"
)

func TestSiteSettingsDefaultAndWholeReplacement(t *testing.T) {
	s, _ := open(t)
	got, err := s.SiteSettings(t.Context())
	if err != nil || got != (SiteSettings{Theme: DefaultTheme, PublicEnabled: true}) {
		t.Fatalf("never saved: %+v %v", got, err)
	}
	full := SiteSettings{Title: "状态", Theme: "dark", AccentColor: "#112233", Logo: "data:image/png;base64,AAAA", CustomCSS: "body{}"}
	if _, err := s.SaveSettings(t.Context(), SettingsUpdate{SiteAppearance: full.SiteAppearance, PublicEnabled: &full.PublicEnabled}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SiteSettings(t.Context()); err != nil || got != full {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	// 整体替换：空串写入，表示该项回到默认，不是"不改"。
	if _, err := s.SaveSettings(t.Context(), SettingsUpdate{Theme: "auto"}); err != nil {
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
	if _, err := s.SaveSettings(t.Context(), SettingsUpdate{SiteAppearance: want}); err != nil {
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
// 触发器拦写入顺序里最后一个键 notify.backup_channels（SaveSettings 先写外观、总闸与国家查询，再由 saveBackup 写备份，
// 渠道列表在最后），失败时其余键都已写过：拆成多个事务提交的实现会把其中已提交的新值留在库里。
func TestSaveSettingsIsAllOrNothing(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	var channels []int64
	for _, name := range []string{"a", "b"} {
		c, err := s.SaveNotifyChannel(ctx, NotifyChannel{Name: name, Kind: ChannelTelegram, Config: `{}`})
		if err != nil {
			t.Fatal(err)
		}
		channels = append(channels, c.ID)
	}
	gateOpen, geoOff, oldURL, oldSecret, oldKeep := true, false, "https://old.example/{ip}", "old-secret", uint32(10)
	before, err := s.SaveSettings(ctx, SettingsUpdate{
		SiteAppearance: SiteAppearance{Title: "旧", Theme: "light", AccentColor: "#111111", Logo: "data:image/png;base64,AAAA", CustomCSS: "a{}"},
		PublicEnabled:  &gateOpen,
		Geo:            GeoUpdate{Enabled: &geoOff, URL: &oldURL},
		Backup: &BackupSettingsUpdate{Endpoint: "https://old.example", Bucket: "old-bucket", Region: "auto", AccessKey: "old", Prefix: "old",
			Secret: &oldSecret, ConfigKeep: &oldKeep, Channels: &[]int64{channels[0]}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("CREATE TRIGGER reject_channels BEFORE INSERT ON setting WHEN NEW.key = 'notify.backup_channels' BEGIN SELECT RAISE(ABORT, 'channels rejected'); END")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	gateClosed, geoOn, newURL, newSecret, newKeep := false, true, "https://new.example/{ip}", "new-secret", uint32(20)
	_, err = s.SaveSettings(ctx, SettingsUpdate{
		SiteAppearance: SiteAppearance{Title: "新", Theme: "dark", AccentColor: "#222222", CustomCSS: "b{}"},
		PublicEnabled:  &gateClosed,
		Geo:            GeoUpdate{Enabled: &geoOn, URL: &newURL},
		Backup: &BackupSettingsUpdate{Endpoint: "https://new.example", Bucket: "new-bucket", Region: "us-east-1", AccessKey: "new", Prefix: "new",
			Secret: &newSecret, ConfigKeep: &newKeep, Channels: &[]int64{channels[0], channels[1]}},
	})
	if err == nil || !strings.Contains(err.Error(), "channels rejected") {
		t.Fatalf("save error = %v", err)
	}
	if got, err := s.Settings(ctx); err != nil || !reflect.DeepEqual(got, before) {
		t.Fatalf("failed save left %+v %v, want %+v", got, err, before)
	}
	if !s.PublicEnabled() {
		t.Fatal("failed save published the closed gate")
	}
}

// v7 的完整 DDL：v6 加上 migrateDeliveryFailure 的两条 ALTER。
var schemaV7 = append(slices.Clone(schemaV6),
	"ALTER TABLE alert_delivery ADD COLUMN failure TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE alert_delivery ADD COLUMN http_status INTEGER")

func TestMigrationFromV7MatchesFreshSchemaAndKeepsRows(t *testing.T) {
	migrated, fresh := migrateFrom(t, schemaV7, 7, seedMinuteRow)
	if got, want := sqlitetest.Describe(t, migrated.r), sqlitetest.Describe(t, fresh.r); !reflect.DeepEqual(got, want) {
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
