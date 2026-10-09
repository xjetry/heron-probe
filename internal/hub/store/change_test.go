package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
)

var schemaV25 = append(slices.Clone(schemaV24),
	"ALTER TABLE api_token ADD COLUMN permissions TEXT NOT NULL DEFAULT '[]'",
	"ALTER TABLE api_token ADD COLUMN all_nodes INTEGER NOT NULL DEFAULT 1 CHECK (all_nodes IN (0, 1))",
	`CREATE TABLE api_token_node (token_id INTEGER NOT NULL,node_id INTEGER NOT NULL,PRIMARY KEY(token_id,node_id)) WITHOUT ROWID`,
	`CREATE TABLE operation (owner_key TEXT NOT NULL,owner_id INTEGER NOT NULL,request_id TEXT NOT NULL,request_hash TEXT NOT NULL,action TEXT NOT NULL,resource_id INTEGER NOT NULL,before_json TEXT NOT NULL,after_json TEXT NOT NULL,committed_at INTEGER NOT NULL,PRIMARY KEY(owner_key,request_id)) WITHOUT ROWID`,
	`CREATE INDEX operation_by_owner ON operation(owner_id,committed_at DESC,request_id)`,
	`CREATE INDEX operation_details_by_time ON operation(committed_at) WHERE before_json!='' OR after_json!=''`,
	"ALTER TABLE register_window RENAME TO register_window_old",
	`CREATE TABLE register_window (owner_id INTEGER PRIMARY KEY,key_hash BLOB NOT NULL UNIQUE,expires_at INTEGER NOT NULL,remaining INTEGER NOT NULL)`,
	"INSERT INTO register_window SELECT 0,key_hash,expires_at,remaining FROM register_window_old",
	"DROP TABLE register_window_old",
	"ALTER TABLE node_update ADD COLUMN owner_id INTEGER NOT NULL DEFAULT 0",
)

// permitPolicy 放行一切：本文件的用例验证写边界自身的状态机，授权裁决由 authz 包的用例钉住（store 不能 import
// 实现了策略的 authz）。
type permitPolicy struct{}

func (permitPolicy) Principal(context.Context, ChangeReader, *Change) (*APIToken, error) {
	return nil, nil
}
func (permitPolicy) Scope(context.Context, ChangeReader, *Change, *APIToken) error    { return nil }
func (permitPolicy) Resource(context.Context, ChangeReader, *Change, *APIToken) error { return nil }

func TestAgenticMigrationKeepsLegacyReadOnly(t *testing.T) {
	s := migrateFrom(t, 24, func(t *testing.T, db *sql.DB) {
		if _, err := db.Exec("INSERT INTO api_token(name,token_hash,created_at) VALUES('legacy',?,1)", hash(1)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT INTO register_window(id,key_hash,expires_at,remaining) VALUES(1,?,2000000000,7)", hash(2)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO node(id,name,token_hash,created_at) VALUES(42,'legacy-node',?,1); INSERT INTO node_update(node_id,data) VALUES(42,'{"supported":true,"version":"v0.1.0"}')`, hash(3)); err != nil {
			t.Fatal(err)
		}
	})
	list, err := s.ListAPITokens(t.Context())
	if err != nil || len(list) != 1 || !list[0].AllNodes || len(list[0].Permissions) != 0 {
		t.Fatalf("legacy grant changed: %+v %v", list, err)
	}
	var owner, expires, remaining int64
	var key []byte
	if err := s.r.QueryRow("SELECT owner_id,key_hash,expires_at,remaining FROM register_window").Scan(&owner, &key, &expires, &remaining); err != nil || owner != 0 || !slices.Equal(key, hash(2)) || expires != 2000000000 || remaining != 7 {
		t.Fatalf("legacy window changed: owner=%d key=%x expires=%d remaining=%d err=%v", owner, key, expires, remaining, err)
	}
	var data string
	if err := s.r.QueryRow("SELECT owner_id,data FROM node_update WHERE node_id=42").Scan(&owner, &data); err != nil || owner != 0 || data != `{"supported":true,"version":"v0.1.0"}` {
		t.Fatalf("legacy update changed: owner=%d data=%s err=%v", owner, data, err)
	}
}

func TestAgenticDeferredCommitFailure(t *testing.T) {
	s, _ := open(t)
	for _, stmt := range []string{
		"PRAGMA foreign_keys=ON",
		"CREATE TABLE commit_parent(id INTEGER PRIMARY KEY)",
		"CREATE TABLE commit_guard(id INTEGER REFERENCES commit_parent(id) DEFERRABLE INITIALLY DEFERRED)",
		"CREATE TRIGGER reject_commit AFTER INSERT ON operation BEGIN INSERT INTO commit_guard VALUES(1); END",
	} {
		if _, err := s.w.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	var enabled int
	if err := s.w.QueryRow("PRAGMA foreign_keys").Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("deferred constraint not enabled: %d %v", enabled, err)
	}
	probe, err := s.w.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := probe.Exec("INSERT INTO commit_guard VALUES(1)"); err != nil {
		probe.Rollback()
		t.Fatalf("constraint failed before commit: %v", err)
	}
	if err := probe.Commit(); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("constraint did not fail at commit: %v", err)
	}
	newChange := func() *Change {
		t.Helper()
		c := &Change{Operation: Operation{RequestID: "commit-retry", RequestHash: "same"}, Action: ActionCreateNode, Policy: permitPolicy{}}
		var err error
		c.ExpectedVersion, err = s.ChangeVersion(t.Context(), c)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	c := newChange()
	if _, _, err := s.CreateNode(WithChange(t.Context(), c), "pending", Billing{}, hash(1)); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("deferred commit constraint did not fail: %v", err)
	}
	if c.ID == "" || c.CommittedAt != 0 || c.completed {
		t.Fatalf("failed commit reported applied change: %+v", c)
	}
	for _, table := range []string{"node", "operation", "commit_guard"} {
		var count int
		if err := s.r.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("failed commit retained %s rows: %d %v", table, count, err)
		}
	}
	if _, err := s.w.Exec("DROP TRIGGER reject_commit"); err != nil {
		t.Fatal(err)
	}
	c = newChange()
	if _, _, err := s.CreateNode(WithChange(t.Context(), c), "pending", Billing{}, hash(1)); err != nil || c.CommittedAt == 0 {
		t.Fatalf("retry after commit failure did not recover: %+v %v", c, err)
	}
	retry := newChange()
	if _, _, err := s.CreateNode(WithChange(t.Context(), retry), "pending", Billing{}, hash(1)); !errors.Is(err, ErrReplay) || retry.ID != c.ID {
		t.Fatalf("recovered commit did not replay: %+v %v", retry, err)
	}
}

func TestQueuedUpdateRevocationAndRegisterOwnership(t *testing.T) {
	s, clk := open(t)
	p, err := s.CreateAPIToken(t.Context(), "writer", sha256.Sum256([]byte("writer")), clk.Now(), 100, &TokenGrant{Permissions: []Permission{PermissionRegister, PermissionUpdate}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithPrincipal(t.Context(), p)
	if err := s.SetRegisterWindow(ctx, hash(2), clk.Now().Add(60000000000), 2); err != nil {
		t.Fatal(err)
	}
	node, _, err := s.RegisterNode(t.Context(), hash(2), "registered", hash(3))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListNodes(ctx)
	if err != nil || len(rows) != 1 || rows[0].ID != node {
		t.Fatalf("registration did not grant node: %+v %v", rows, err)
	}
	c := &Change{Operation: Operation{OwnerID: p.ID, RequestID: "update", RequestHash: "x", ResourceID: node}, Action: ActionStartUpdate, Policy: permitPolicy{}}
	c.ExpectedVersion, err = s.ChangeVersion(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	status := &heronv1.UpdateStatus{Supported: true, Task: &heronv1.UpdateTask{Id: "update", State: "queued"}}
	if err := s.SaveNodeUpdate(WithChange(ctx, c), node, status); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteAPIToken(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}
	status.Task.State = "dispatched"
	if err := s.SaveNodeUpdate(t.Context(), node, status); err != nil {
		t.Fatal(err)
	}
	if status.Task.State != "cancelled" {
		t.Fatalf("revoked update dispatched: %v", status)
	}
	if _, _, err := s.RegisterNode(t.Context(), hash(2), "late", hash(4)); !errors.Is(err, ErrNoWindow) {
		t.Fatalf("revoked registration allowed: %v", err)
	}
}

func TestAgenticReceiptsSurviveRestartRestoreAndRetention(t *testing.T) {
	s, clk := open(t)
	ctx := t.Context()
	create := func(id string, b byte) *Change {
		t.Helper()
		c := &Change{Operation: Operation{RequestID: id, RequestHash: id}, Action: ActionCreateNode, Policy: permitPolicy{}}
		var err error
		c.ExpectedVersion, err = s.ChangeVersion(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.CreateNode(WithChange(ctx, c), id, Billing{}, hash(b)); err != nil {
			t.Fatal(err)
		}
		return c
	}
	first := create("first", 1)
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if err := s.SnapshotConfig(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	clk.Advance(91 * 24 * time.Hour)
	create("after-snapshot", 2)
	old, err := s.FindOperation(ctx, 0, "first")
	if err != nil || old.BeforeJSON != "" || old.AfterJSON != "" {
		t.Fatalf("details retained beyond retention: %+v %v", old, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(ctx, s.path, snapshot, "", "", clk.Now(), slog.Default()); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.path, clk, slog.Default(), RequireCurrentSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	operations, err := reopened.ListOperations(ctx, 0, "", 100)
	if err != nil || len(operations) != 2 {
		t.Fatalf("restore erased existing receipts: %+v %v", operations, err)
	}
	old, err = reopened.FindOperation(ctx, 0, "first")
	if err != nil || old.BeforeJSON != "" || old.AfterJSON != "" {
		t.Fatalf("restore resurrected expired audit details: %+v %v", old, err)
	}
	retry := &Change{Operation: Operation{RequestID: "first", RequestHash: "first"}, Action: ActionCreateNode, Policy: permitPolicy{}, ExpectedVersion: first.ExpectedVersion}
	if _, _, err := reopened.CreateNode(WithChange(ctx, retry), "duplicate", Billing{}, hash(3)); !errors.Is(err, ErrReplay) {
		t.Fatalf("durable retry was executed: %v", err)
	}
	nodes, err := reopened.ListNodes(ctx)
	if err != nil || len(nodes) != 1 || nodes[0].Name != "first" {
		t.Fatalf("retry mutated restored configuration: %+v %v", nodes, err)
	}
}
