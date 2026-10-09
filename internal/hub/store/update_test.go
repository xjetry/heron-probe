package store

import (
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
)

var schemaV24 = append(slices.Clone(schemaV23), `CREATE TABLE node_update (node_id INTEGER PRIMARY KEY, data TEXT NOT NULL)`)

func TestUpdateMigrationAndDeletedNode(t *testing.T) {
	t.Parallel()
	s := migrateFrom(t, 23, seedMinuteRow)
	status := &heronv1.UpdateStatus{Supported: true, Version: "v0.2.0", Task: &heronv1.UpdateTask{Id: "0123456789abcdef", Version: "v0.3.0", State: "queued", ExpiresAt: 1}}
	if err := s.SaveNodeUpdate(t.Context(), 7, status); err != nil {
		t.Fatal(err)
	}
	rows, err := s.NodeUpdates(t.Context())
	if err != nil || rows[7].GetTask().GetState() != "queued" {
		t.Fatalf("persisted update=%v err=%v", rows, err)
	}
	if err := s.DeleteNode(t.Context(), 7); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveNodeUpdate(t.Context(), 7, status); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted node recreated update: %v", err)
	}
	var count int
	if err := s.r.QueryRow("SELECT count(*) FROM node_update").Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted update rows=%d err=%v", count, err)
	}
}

func TestRestoreDoesNotReplayUpdates(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	id, _, err := s.CreateNode(t.Context(), "test", Billing{}, []byte("token"))
	if err != nil {
		t.Fatal(err)
	}
	status := &heronv1.UpdateStatus{Supported: true, Version: "v0.2.0", Task: &heronv1.UpdateTask{Id: "0123456789abcdef", Version: "v0.3.0", State: "queued", ExpiresAt: 1}}
	if err := s.SaveNodeUpdate(t.Context(), id, status); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "config.db")
	if err := s.SnapshotConfig(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(t.Context(), s.path, config, "", "", s.clk.Now(), slog.Default()); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(s.path, s.clk, slog.Default(), RequireCurrentSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	rows, err := restored.NodeUpdates(t.Context())
	if err != nil || len(rows) != 0 {
		t.Fatalf("restored update authorization: %v err=%v", rows, err)
	}
}
