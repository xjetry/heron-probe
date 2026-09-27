package store

import (
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/clock"
)

func TestPublicSwitchPersistenceAndMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	clk := clock.NewFake(time.Now())
	s, err := Open(path, clk, slog.Default(), MigrateSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if !s.PublicEnabled() {
		t.Fatal("new store gate is closed")
	}
	for _, enabled := range []bool{false, true, false} {
		if err := s.SaveSiteSettings(t.Context(), SiteSettings{Theme: "auto", PublicEnabled: enabled}); err != nil {
			t.Fatal(err)
		}
		var raw string
		if err := s.r.QueryRow("SELECT value FROM setting WHERE key = 'site.public_enabled'").Scan(&raw); err != nil {
			t.Fatal(err)
		}
		want := "0"
		if enabled {
			want = "1"
		}
		if raw != want || s.PublicEnabled() != enabled {
			t.Fatalf("saved gate: raw=%q memory=%v, want %s/%v", raw, s.PublicEnabled(), want, enabled)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		s, err = Open(path, clk, slog.Default(), RequireCurrentSchema)
		if err != nil {
			t.Fatal(err)
		}
		if s.PublicEnabled() != enabled {
			t.Fatalf("reopened gate=%v, want %v", s.PublicEnabled(), enabled)
		}
	}
}

func TestPublicSwitchFailedSaveKeepsMemory(t *testing.T) {
	s, _ := open(t)
	if err := s.write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec("CREATE TRIGGER reject_gate BEFORE INSERT ON setting WHEN NEW.key = 'site.public_enabled' BEGIN SELECT RAISE(ABORT, 'gate rejected'); END")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	err := s.SaveSiteSettings(t.Context(), SiteSettings{Theme: "auto"})
	if err == nil || !strings.Contains(err.Error(), "gate rejected") {
		t.Fatalf("save err=%v", err)
	}
	if !s.PublicEnabled() {
		t.Fatal("failed save published closed gate")
	}
	var count int
	if err := s.r.QueryRow("SELECT COUNT(*) FROM setting").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed save left %d rows", count)
	}
}

func TestPublicSwitchInvalidStoredValueRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	s, err := Open(path, clock.NewFake(time.Now()), slog.Default(), MigrateSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO setting (key, value) VALUES ('site.public_enabled', 'invalid')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, clock.NewFake(time.Now()), slog.Default(), RequireCurrentSchema)
	if reopened != nil {
		reopened.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "site.public_enabled must be 0 or 1") {
		t.Fatalf("invalid gate accepted: %v", err)
	}
}
