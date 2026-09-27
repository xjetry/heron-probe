package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/auth"
	"github.com/xjetry/probe/internal/hub/store"
)

func TestServePublicSwitchBothSources(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "embedded", true: "directory"}[custom], func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "hub.db")
			clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			st, err := store.Open(db, clk, slog.Default(), store.MigrateSchema)
			if err != nil {
				t.Fatal(err)
			}
			if err := auth.New(st, clk, slog.Default()).SetPassword(t.Context(), "public-switch-password"); err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			var flags []string
			if custom {
				dir := t.TempDir()
				for name, content := range map[string]string{"index.html": "custom public page", "theme.js": "secret theme script"} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Mkdir(filepath.Join(dir, "assets"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("public script"), 0600); err != nil {
					t.Fatal(err)
				}
				flags = []string{"--public-dir", dir}
			}
			base, _, _ := startTestHub(t, db, clk, flags...)
			jar, _ := cookiejar.New(nil)
			client := &http.Client{Jar: jar}
			admin := probev1connect.NewAdminServiceClient(client, base)
			if _, err := admin.Login(t.Context(), connect.NewRequest(&probev1.LoginRequest{Password: "public-switch-password"})); err != nil {
				t.Fatal(err)
			}
			fetch := func(path string) (int, string, http.Header) {
				t.Helper()
				r, err := client.Get(base + path)
				if err != nil {
					t.Fatal(err)
				}
				defer r.Body.Close()
				b, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				return r.StatusCode, string(b), r.Header
			}
			rootCode, rootBody, _ := fetch("/")
			adminCode, adminBody, _ := fetch("/admin/")
			for _, enabled := range []bool{false, true} {
				if _, err := admin.UpdateSettings(t.Context(), connect.NewRequest(&probev1.UpdateSettingsRequest{Settings: &probev1.Settings{Theme: "auto", PublicEnabled: enabled}})); err != nil {
					t.Fatal(err)
				}
				code, body, headers := fetch("/")
				if !enabled {
					if code != 200 || !strings.Contains(body, "公开页已关闭") || headers.Get("Content-Type") != "text/html; charset=utf-8" || headers.Get("Cache-Control") != "no-store" {
						t.Fatalf("closed root: %d %s %v", code, body, headers)
					}
					for _, path := range []string{"/assets/app.js", "/theme.js", "/index.html", "/nodes/1"} {
						if code, body, _ := fetch(path); code != 404 {
							t.Errorf("closed resource %s: %d %s", path, code, body)
						}
					}
				} else if code != rootCode || body != rootBody {
					t.Fatalf("reopened root differs: %d %s", code, body)
				}
				if code, body, _ := fetch("/admin/"); code != adminCode || body != adminBody {
					t.Fatalf("admin affected: %d %s", code, body)
				}
				if _, err := admin.GetSettings(t.Context(), connect.NewRequest(&probev1.GetSettingsRequest{})); err != nil {
					t.Fatalf("admin RPC affected: %v", err)
				}
			}
		})
	}
}
