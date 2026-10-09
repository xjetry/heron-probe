package auth

import (
	"context"
	"log/slog"
	"net/netip"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/xjetry/heron-probe/internal/clock"
	"github.com/xjetry/heron-probe/internal/hub/probe"
	"github.com/xjetry/heron-probe/internal/hub/store"
)

// hubAndOffline 在同一个库上建运行中 hub 的 Auth 与离线子命令的 Auth（库外写者，与 cmd/hub 的 openOffline 同形）。
func hubAndOffline(t *testing.T) (hub, offline *Auth) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.db")
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	open := func(opts ...store.OpenOption) *Auth {
		st, err := store.Open(path, clk, slog.Default(), store.MigrateSchema, opts...)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		a := New(st, probe.New(st, slog.Default()), nil, clk, time.UTC, slog.Default())
		if err := a.Load(t.Context()); err != nil {
			t.Fatal(err)
		}
		return a
	}
	hub = open()
	return hub, open(store.ExternalWriter())
}

// Reload 返回的恰是库外删除的节点：库外建节点、换发不算；重建之后旧映射里的被删节点不再鉴权，库外新建的认得。
func TestReloadReturnsNodesDeletedOutsideTheProcess(t *testing.T) {
	hub, offline := hubAndOffline(t)
	ctx := t.Context()
	from := netip.MustParseAddr("127.0.0.1")
	deleted, deletedToken, err := runningNode(t, hub, "deleted")
	if err != nil {
		t.Fatal(err)
	}
	rotated, _, err := runningNode(t, hub, "rotated")
	if err != nil {
		t.Fatal(err)
	}
	if err := offline.DeleteNode(ctx, deleted); err != nil {
		t.Fatal(err)
	}
	install, err := offline.RotateToken(ctx, rotated)
	if err != nil {
		t.Fatal(err)
	}
	created, createdInstall, err := offline.CreateNode(ctx, "created", store.Billing{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := hub.Authenticate(deletedToken); !ok {
		t.Fatal("fixture: the hub's map must still hold the offline-deleted node before reload")
	}
	removed, err := hub.Reload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(removed, []int64{deleted}) {
		t.Fatalf("removed = %v, want only the offline-deleted node %d", removed, deleted)
	}
	if _, ok := hub.Authenticate(deletedToken); ok {
		t.Fatal("offline-deleted node still authenticates after reload")
	}
	for _, c := range []struct {
		id      int64
		install string
	}{{rotated, install}, {created, createdInstall}} {
		if got, _, err := hub.Register(ctx, c.install, "", from); err != nil || got != c.id {
			t.Fatalf("register with offline-issued credential: id %d, %v; want %d", got, err, c.id)
		}
	}
	if removed, err := hub.Reload(ctx); err != nil || len(removed) != 0 {
		t.Fatalf("second reload removed %v, %v; want nothing", removed, err)
	}
}

// 读库失败时映射不变，删除留给下一次重建算出。
func TestReloadFailureLeavesMapAndDeletionsForNextTime(t *testing.T) {
	hub, offline := hubAndOffline(t)
	deleted, token, err := runningNode(t, hub, "deleted")
	if err != nil {
		t.Fatal(err)
	}
	if err := offline.DeleteNode(t.Context(), deleted); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := hub.Reload(canceled); err == nil {
		t.Fatal("reload with a canceled context succeeded")
	}
	if _, ok := hub.Authenticate(token); !ok {
		t.Fatal("failed reload changed the map")
	}
	if removed, err := hub.Reload(t.Context()); err != nil || !slices.Equal(removed, []int64{deleted}) {
		t.Fatalf("reload after failure removed %v, %v; want [%d]", removed, err, deleted)
	}
}
