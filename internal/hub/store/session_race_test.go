package store_test

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/auth"
	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/testwait"
)

func TestDelayedSessionTouchCannotResurrectLogout(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	path := filepath.Join(t.TempDir(), "hub.db")
	st, err := store.Open(path, clk, log)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	other, err := store.Open(path, clk, log)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	a := auth.New(st, clk, time.UTC, log)
	if err := a.SetPassword(ctx, "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	token, err := a.Login(ctx, "a sufficiently long password", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	release, pending, drain := st.HoldWriterForTest()
	defer release()
	clk.Advance(time.Minute)
	type authentication struct {
		ok  bool
		err error
	}
	authenticated := make(chan authentication, 1)
	go func() { ok, err := a.AuthenticateSession(ctx, token); authenticated <- authentication{ok, err} }()
	var result authentication
	blocked := false
	select {
	case result = <-authenticated:
	case <-time.After(testwait.Bound):
		// 缺陷使刷新变为同步写时也要释放夹具，避免测试自身无限等待。
		blocked = true
		release()
		result = <-authenticated
	}
	if result.err != nil || !result.ok {
		t.Fatalf("live session rejected: %v %v", result.ok, result.err)
	}
	if blocked {
		t.Fatal("authentication waited for asynchronous refresh")
	}
	if n := pending(); n != 1 {
		t.Fatalf("refresh not queued: %d writes", n)
	}
	// 单实例队列是 FIFO；另一连接先撤销才会产生延迟刷新晚于登出的真实交错。
	if err := auth.New(other, clk, time.UTC, log).Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.Session(ctx, auth.HashToken(token)); err != nil || ok {
		t.Fatalf("logout did not remove session before refresh: %v %v", ok, err)
	}
	release()
	if err := drain(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.Session(ctx, auth.HashToken(token)); err != nil || ok {
		t.Fatalf("delayed refresh resurrected logged-out session: %v %v", ok, err)
	}
}
