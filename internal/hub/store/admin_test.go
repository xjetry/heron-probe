package store

import (
	"context"
	"testing"
	"time"
)

func TestSetAdminPasswordRevokesEverySession(t *testing.T) {
	t.Parallel()
	s, clk := open(t)
	ctx := context.Background()
	if err := s.SetAdminPassword(ctx, "original"); err != nil {
		t.Fatal(err)
	}
	now := clk.Now()
	var h1, h2 [32]byte
	h1[0], h2[0] = 1, 2
	for _, h := range [][32]byte{h1, h2} {
		if err := s.CreateSession(ctx, h, now, now.Add(time.Hour), "original"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetAdminPassword(ctx, "$argon2id$dummy"); err != nil {
		t.Fatal(err)
	}
	phc, ok, err := s.AdminPasswordHash(ctx)
	if err != nil || !ok || phc != "$argon2id$dummy" {
		t.Fatalf("AdminPasswordHash = %q %v %v", phc, ok, err)
	}
	for _, h := range [][32]byte{h1, h2} {
		if _, ok := lookupSession(t, s, h); ok {
			t.Fatal("session survived a password change")
		}
	}
}

// lookupSession 从会话表里取出 hash 对应的那一行；没有时第二个返回值为 false。
func lookupSession(t *testing.T, s *Store, h [32]byte) (Session, bool) {
	t.Helper()
	rows, err := s.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, sess := range rows {
		if sess.TokenHash == h {
			return sess, true
		}
	}
	return Session{}, false
}

func TestNoAdminIsReportedExplicitly(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	if _, ok, err := s.AdminPasswordHash(context.Background()); ok || err != nil {
		t.Fatalf("empty admin table: ok=%v err=%v, want false nil", ok, err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	t.Parallel()
	s, clk := open(t)
	ctx := context.Background()
	if err := s.SetAdminPassword(ctx, "original"); err != nil {
		t.Fatal(err)
	}
	var h [32]byte
	h[0] = 9
	now := clk.Now()
	if err := s.CreateSession(ctx, h, now, now.Add(30*24*time.Hour), "original"); err != nil {
		t.Fatal(err)
	}
	got, ok := lookupSession(t, s, h)
	if !ok || !got.CreatedAt.Equal(now) || !got.LastUsedAt.Equal(now) || !got.ExpiresAt.Equal(now.Add(30*24*time.Hour)) {
		t.Fatalf("Session = %+v %v", got, ok)
	}
	clk.Advance(2 * time.Hour)
	touched := make(chan error, 1)
	s.TouchSessionAsync(h, clk.Now(), func(err error) { touched <- err })
	if err := <-touched; err != nil {
		t.Fatal(err)
	}
	got, _ = lookupSession(t, s, h)
	if !got.LastUsedAt.Equal(clk.Now()) {
		t.Fatalf("last_used_at = %v, want %v", got.LastUsedAt, clk.Now())
	}
	n, err := s.DeleteExpiredSessions(ctx, now.Add(30*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("DeleteExpiredSessions = %d %v, want 1 nil", n, err)
	}
	if _, ok := lookupSession(t, s, h); ok {
		t.Fatal("expired session still readable")
	}
	if err := s.DeleteSession(ctx, h); err != nil {
		t.Fatalf("deleting an absent session must be a no-op: %v", err)
	}
}
