package api

import (
	"testing"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"google.golang.org/protobuf/proto"
)

func TestSettingsGroupsPreserveAbsentAppearance(t *testing.T) {
	for _, group := range []string{"backup", "public", "geo_enabled", "geo_url"} {
		t.Run(group, func(t *testing.T) {
			h := newHarness(t, "")
			h.login(t)
			before := saveSettings(t, h, validSettings())
			want := proto.Clone(before).(*probev1.Settings)
			in := &probev1.Settings{}
			switch group {
			case "backup":
				in.Backup = fullBackup()
				want.Backup = echoOf(in.Backup, true)
			case "public":
				in.PublicEnabled, want.PublicEnabled = proto.Bool(false), proto.Bool(false)
			case "geo_enabled":
				in.GeoEnabled, want.GeoEnabled = proto.Bool(true), proto.Bool(true)
			case "geo_url":
				in.GeoUrl, want.GeoUrl = proto.String("https://example.test/{ip}"), proto.String("https://example.test/{ip}")
			}
			got := saveSettings(t, h, in)
			if !proto.Equal(got, want) || !proto.Equal(currentSettings(t, h), want) {
				t.Fatalf("independent %s update changed absent groups: got=%v want=%v", group, got, want)
			}
		})
	}
}
