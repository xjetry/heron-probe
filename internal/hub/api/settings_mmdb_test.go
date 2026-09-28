package api

import (
	"path/filepath"
	"testing"
	"time"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/geo"
	"github.com/xjetry/probe/internal/hub/store"
)

// 回显的后端与路径取自装配给 api 的后端对象，UpdateSettings 对两项的缺席、给出、伪造与未知枚举一律忽略。
func TestSettingsGeoBackendIsReadOnly(t *testing.T) {
	mmdbPath := filepath.Join("..", "geo", "testdata", "country.mmdb")
	local, err := geo.OpenMMDB(mmdbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name    string
		backend geo.Backend // nil 即夹具装配的 HTTP 后端
		want    probev1.GeoBackend
		path    string
	}{
		{"http", nil, probev1.GeoBackend_GEO_BACKEND_HTTP, ""},
		{"mmdb", local, probev1.GeoBackend_GEO_BACKEND_MMDB, mmdbPath},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, "")
			if c.backend != nil {
				h.svc.cfg.Geo = c.backend
			}
			h.login(t)
			check := func(s *probev1.Settings) {
				t.Helper()
				if s.GeoBackend != c.want || s.GeoMmdbPath != c.path {
					t.Errorf("backend = %v path = %q, want %v %q", s.GeoBackend, s.GeoMmdbPath, c.want, c.path)
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

// 回显的后端取自 Config.Geo；缺了它 GetSettings 要到请求时才空指针，装配时就拒绝。
func TestNewRequiresTheGeoBackend(t *testing.T) {
	defer func() {
		if r := recover(); r != "api.Config.Geo must be set" {
			t.Errorf("panic = %v, want api.Config.Geo must be set", r)
		}
	}()
	New(Config{TTL: time.Second, Location: time.UTC, Retention: store.DefaultRetention}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
}
