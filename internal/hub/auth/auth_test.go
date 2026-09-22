package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/store"
)

func setup(t *testing.T) (*Auth, *store.Store, *clock.Fake) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), clk, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a := New(st, clk, slog.Default())
	if err := a.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return a, st, clk
}

func TestTokenIs32RandomBytesHex(t *testing.T) {
	p1, h1 := NewToken()
	p2, _ := NewToken()
	if len(p1) != 64 || p1 == p2 {
		t.Fatalf("token %q / %q", p1, p2)
	}
	if HashToken(p1) != h1 {
		t.Fatal("hash must be derived from the plain token")
	}
}

func TestCreateNodeMakesTokenAuthenticate(t *testing.T) {
	a, _, _ := setup(t)
	id, plain, err := a.CreateNode(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := a.Authenticate(plain); !ok || got != id {
		t.Fatalf("authenticate = %d,%v want %d,true", got, ok, id)
	}
	if _, ok := a.Authenticate("nope"); ok {
		t.Fatal("unknown token authenticated")
	}
}

func TestRotateRemovesOldTokenImmediately(t *testing.T) {
	a, _, _ := setup(t)
	ctx := context.Background()
	id, old, _ := a.CreateNode(ctx, "a")
	fresh, err := a.RotateToken(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Authenticate(old); ok {
		t.Fatal("old token still authenticates after rotation")
	}
	if got, ok := a.Authenticate(fresh); !ok || got != id {
		t.Fatal("new token does not authenticate")
	}
}

func TestFailedStoreWriteLeavesMapUntouched(t *testing.T) {
	a, _, _ := setup(t)
	ctx := context.Background()
	_, plain, _ := a.CreateNode(ctx, "a")
	if _, err := a.RotateToken(ctx, 9999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if _, ok := a.Authenticate(plain); !ok {
		t.Fatal("a failed rotation must not disturb existing tokens")
	}
	a.mu.RLock()
	n := len(a.byHash)
	a.mu.RUnlock()
	if n != 1 {
		t.Fatalf("map has %d entries, want 1", n)
	}
}

func TestLoadRebuildsMapFromStore(t *testing.T) {
	a, st, clk := setup(t)
	ctx := context.Background()
	id, plain, _ := a.CreateNode(ctx, "a")
	b := New(st, clk, slog.Default())
	if _, ok := b.Authenticate(plain); ok {
		t.Fatal("fresh Auth must not know tokens before Load")
	}
	if err := b.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got, ok := b.Authenticate(plain); !ok || got != id {
		t.Fatal("Load did not rebuild the map")
	}
}

func TestRegisterDeniedWithoutWindowAndDoesNotCount(t *testing.T) {
	a, _, _ := setup(t)
	ctx := context.Background()
	from := netip.MustParseAddr("203.0.113.5")
	for i := 0; i < 10; i++ {
		if _, _, err := a.Register(ctx, "anything", "n", from); !errors.Is(err, ErrDenied) {
			t.Fatalf("err = %v", err)
		}
	}
	key, _ := a.OpenWindow(ctx, time.Hour, 1)
	if _, _, err := a.Register(ctx, key, "n", from); err != nil {
		t.Fatalf("closed-window attempts must not have locked the IP: %v", err)
	}
}

func TestRegisterWrongKeyOnOpenWindowLocksIP(t *testing.T) {
	a, _, clk := setup(t)
	ctx := context.Background()
	from := netip.MustParseAddr("203.0.113.5")
	key, _ := a.OpenWindow(ctx, time.Hour, 5)
	for i := 0; i < failLimit; i++ {
		if _, _, err := a.Register(ctx, "wrong", "n", from); !errors.Is(err, ErrDenied) {
			t.Fatalf("err = %v", err)
		}
	}
	if _, _, err := a.Register(ctx, key, "n", from); !errors.Is(err, ErrDenied) {
		t.Fatal("locked IP must be denied even with the right key")
	}
	other := netip.MustParseAddr("203.0.113.6")
	if _, _, err := a.Register(ctx, key, "n", other); err != nil {
		t.Fatalf("lockout is per IP: %v", err)
	}
	clk.Advance(failWindow)
	if _, _, err := a.Register(ctx, key, "n", from); err != nil {
		t.Fatalf("lockout must expire: %v", err)
	}
}

func TestRegisterIssuesWorkingToken(t *testing.T) {
	a, _, _ := setup(t)
	ctx := context.Background()
	key, _ := a.OpenWindow(ctx, time.Hour, 1)
	id, plain, err := a.Register(ctx, key, "n", netip.MustParseAddr("10.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := a.Authenticate(plain); !ok || got != id {
		t.Fatal("registered token does not authenticate")
	}
	if _, _, err := a.Register(ctx, key, "m", netip.MustParseAddr("10.0.0.2")); !errors.Is(err, ErrDenied) {
		t.Fatal("window with max 1 must be exhausted")
	}
}

func TestDeleteNodeRevokesToken(t *testing.T) {
	a, _, _ := setup(t)
	ctx := context.Background()
	id, plain, _ := a.CreateNode(ctx, "a")
	if err := a.DeleteNode(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Authenticate(plain); ok {
		t.Fatal("deleted node's token still authenticates")
	}
}
