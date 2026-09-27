package api

import (
	"testing"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
)

func TestSettingsGeoBackendIsReadOnly(t *testing.T) {
	for _, path := range []string{"", "/data/country.mmdb"} {
		t.Run(path, func(t *testing.T) {
			h := newHarness(t, "")
			h.svc.cfg.GeoMMDBPath = path
			h.login(t)
			backend := probev1.GeoBackend_GEO_BACKEND_HTTP
			if path != "" {
				backend = probev1.GeoBackend_GEO_BACKEND_MMDB
			}
			check := func(s *probev1.Settings) {
				t.Helper()
				if s.GeoBackend != backend || s.GeoMmdbPath != path {
					t.Errorf("backend = %v path = %q, want %v %q", s.GeoBackend, s.GeoMmdbPath, backend, path)
				}
			}
			check(currentSettings(t, h))
			for _, value := range []struct {
				backend probev1.GeoBackend
				path    string
			}{
				{}, {probev1.GeoBackend_GEO_BACKEND_HTTP, ""}, {probev1.GeoBackend_GEO_BACKEND_MMDB, "/other.mmdb"}, {999, "\x00invalid"},
			} {
				in := validSettings()
				in.GeoBackend, in.GeoMmdbPath = value.backend, value.path
				check(saveSettings(t, h, in))
				check(currentSettings(t, h))
			}
		})
	}
}
