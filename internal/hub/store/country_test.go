package store

import (
	"database/sql"
	"reflect"
	"slices"
	"testing"

	"github.com/xjetry/probe/internal/hub/metric"
)

// v13 的完整 DDL：v12 加上节点的来源地址。
var schemaV13 = append(slices.Clone(schemaV12), "ALTER TABLE node ADD COLUMN last_source TEXT NOT NULL DEFAULT ''")

// 旧库的节点升级后三列都是空串：没有国家，已有的来源地址原样保留，查询开启后按它补查。
func TestMigrationFromV13AddsEmptyCountry(t *testing.T) {
	migrated, fresh := migrateFrom(t, schemaV13, 13, func(t *testing.T, db *sql.DB) {
		if _, err := db.Exec("INSERT INTO node (id, name, token_hash, created_at, last_source) VALUES (7, 'kept', x'00', 1, '8.8.8.8')"); err != nil {
			t.Fatal(err)
		}
	})
	if got, want := describe(t, migrated.r), describe(t, fresh.r); !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated schema differs from fresh schema:\n got: %+v\nwant: %+v", got, want)
	}
	if v := userVersion(t, migrated.r); v != schemaVersion {
		t.Fatalf("user_version = %d, want %d", v, schemaVersion)
	}
	n, err := migrated.GetNode(t.Context(), 7)
	if err != nil || n.LastSource != "8.8.8.8" || n.Country != "" || n.CountryIP != "" || n.CountryPin != "" {
		t.Fatalf("node after migration: %+v %v", n, err)
	}
}

// SetLookupCountry 只在 last_source 仍是所查地址时写入：地址已变、节点已删除都不写并返回 false。
func TestSetLookupCountryIsConditionalOnTheAddress(t *testing.T) {
	s, clk := open(t)
	ctx := t.Context()
	id, _, _ := s.CreateNode(ctx, "n", hash(1))
	row := metric.Row{NodeID: id, TS: 600, Bucket: metric.NewBucket(), LastSeen: clk.Now(), Source: "1.1.1.1"}
	if _, err := s.WriteMinuteBatch(ctx, metric.Batch{Rows: []metric.Row{row}}); err != nil {
		t.Fatal(err)
	}
	if set, err := s.SetLookupCountry(ctx, id, "8.8.8.8", "US"); err != nil || set {
		t.Fatalf("answer for another address: set = %v %v", set, err)
	}
	if n, err := s.GetNode(ctx, id); err != nil || n.Country != "" || n.CountryIP != "" {
		t.Fatalf("answer for another address was written: %+v %v", n, err)
	}
	if set, err := s.SetLookupCountry(ctx, id, "1.1.1.1", "AU"); err != nil || !set {
		t.Fatalf("answer for the current address: set = %v %v", set, err)
	}
	if n, err := s.GetNode(ctx, id); err != nil || n.Country != "AU" || n.CountryIP != "1.1.1.1" {
		t.Fatalf("after a matching answer: %+v %v", n, err)
	}
	if set, err := s.SetLookupCountry(ctx, id+1, "1.1.1.1", "AU"); err != nil || set {
		t.Fatalf("missing node: set = %v %v", set, err)
	}
}

// 显示值：pin 非空取 pin（manual），否则查得值（lookup），都空为 none。
func TestDisplayCountry(t *testing.T) {
	for _, c := range []struct {
		n       Node
		country string
		source  CountrySource
	}{
		{Node{}, "", CountryNone},
		{Node{Country: "US", CountryIP: "8.8.8.8"}, "US", CountryLookup},
		{Node{CountryPin: "JP"}, "JP", CountryManual},
		{Node{Country: "US", CountryIP: "8.8.8.8", CountryPin: "JP"}, "JP", CountryManual},
	} {
		if country, source := c.n.DisplayCountry(); country != c.country || source != c.source {
			t.Errorf("DisplayCountry(%+v) = %q %v, want %q %v", c.n, country, source, c.country, c.source)
		}
	}
}

// 国家查询设置：从未保存过为关、默认服务地址；GeoUpdate 里缺席的项不改；外观整体替换不碰它们。
func TestGeoSettingsDefaultsAndPartialUpdate(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	if g, err := s.GeoSettings(ctx); err != nil || g != (GeoSettings{URL: "https://ipinfo.io/{ip}/country"}) {
		t.Fatalf("never saved: %+v %v", g, err)
	}
	on, url := true, "https://geo.example/{ip}"
	g, err := s.SaveSettings(ctx, SiteSettings{Theme: "dark"}, GeoUpdate{Enabled: &on, URL: &url})
	if want := (GeoSettings{Enabled: true, URL: url}); err != nil || g != want {
		t.Fatalf("save returned %+v %v, want %+v", g, err, want)
	}
	g, err = s.SaveSettings(ctx, SiteSettings{Theme: "light", Title: "t"}, GeoUpdate{})
	if want := (GeoSettings{Enabled: true, URL: url}); err != nil || g != want {
		t.Fatalf("appearance-only save returned %+v %v, want %+v", g, err, want)
	}
	off := false
	if _, err := s.SaveSettings(ctx, SiteSettings{Theme: "light"}, GeoUpdate{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	site, g, err := s.Settings(ctx)
	if want := (GeoSettings{URL: url}); err != nil || g != want || site != (SiteSettings{Theme: "light"}) {
		t.Fatalf("settings = %+v %+v %v, want %+v", site, g, err, want)
	}
}

// 开关只认 "true"：库里出现别的文本（手工改库、以后的写法变化）按关，出网只在显式开启时发生。
func TestGeoEnabledReadsAnythingButTrueAsOff(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	for _, v := range []string{"1", "TRUE", "yes", ""} {
		if err := s.write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec("INSERT INTO setting (key, value) VALUES ('geo.enabled', ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value", v)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if g, err := s.GeoSettings(ctx); err != nil || g.Enabled {
			t.Fatalf("geo.enabled = %q read as %+v %v", v, g, err)
		}
	}
}
