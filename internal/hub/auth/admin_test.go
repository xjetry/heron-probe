package auth

import (
	"context"
	"encoding/base64"
	"fmt"

	"errors"
	"golang.org/x/crypto/argon2"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/testwait"
)

const goodPassword = "correct horse battery"

func TestPasswordHashRoundTrip(t *testing.T) {
	phc, err := HashPassword(goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(phc, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatalf("PHC = %q", phc)
	}
	if ok, err := VerifyPassword(phc, goodPassword); err != nil || !ok {
		t.Fatalf("verify = %v %v", ok, err)
	}
	if ok, err := VerifyPassword(phc, goodPassword+"x"); err != nil || ok {
		t.Fatalf("wrong password verified: %v %v", ok, err)
	}
	other, _ := HashPassword(goodPassword)
	if other == phc {
		t.Fatal("two hashes of the same password share a salt")
	}
	for _, bad := range []string{"", "plain", "$argon2i$v=19$m=1,t=1,p=1$YQ$YQ", "$argon2id$v=19$m=1,t=1,p=1$!!$YQ"} {
		t.Run(bad, func(t *testing.T) {
			if _, err := VerifyPassword(bad, goodPassword); err == nil {
				t.Fatalf("malformed hash %q accepted", bad)
			}
		})
	}
}

func TestSetPasswordRejectsShortOnes(t *testing.T) {
	a, _, _ := setup(t)
	if err := a.SetPassword(context.Background(), strings.Repeat("密", MinPasswordLen-1)); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("11 runes accepted: %v", err)
	}
	if err := a.SetPassword(context.Background(), strings.Repeat("密", MinPasswordLen)); err != nil {
		t.Fatalf("12 runes rejected: %v", err)
	}
}

func TestLoginWithoutAdminAlwaysFails(t *testing.T) {
	a, _, _ := setup(t)
	for _, password := range []string{"", goodPassword} {
		t.Run(password, func(t *testing.T) {
			if _, err := a.Login(context.Background(), password, netip.MustParseAddr("10.0.0.1")); !errors.Is(err, ErrNoAdmin) {
				t.Fatalf("empty admin table: %v, want ErrNoAdmin", err)
			}
		})
	}
}

func TestLoginWithoutAdminLocksAfterFailures(t *testing.T) {
	a, _, _ := setup(t)
	from := netip.MustParseAddr("10.0.0.1")
	for i := 0; i < failLimit; i++ {
		if token, err := a.Login(context.Background(), goodPassword, from); !errors.Is(err, ErrNoAdmin) || token != "" {
			t.Fatalf("no-admin attempt %d: token=%q err=%v", i+1, token, err)
		}
	}
	if _, err := a.Login(context.Background(), goodPassword, from); !errors.Is(err, ErrLocked) {
		t.Fatalf("no-admin failures did not lock: %v", err)
	}
}

func TestLoginIssuesSessionAndLocksOutAfterFailures(t *testing.T) {
	a, _, clk := setup(t)
	ctx := context.Background()
	from := netip.MustParseAddr("10.0.0.1")
	if err := a.SetPassword(ctx, goodPassword); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < failLimit; i++ {
		if _, err := a.Login(ctx, "wrong password here", from); !errors.Is(err, ErrBadPassword) {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	if _, err := a.Login(ctx, goodPassword, from); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked address logged in with the right password: %v", err)
	}
	other := netip.MustParseAddr("10.0.0.2")
	tok, err := a.Login(ctx, goodPassword, other)
	if err != nil {
		t.Fatalf("another address must not be affected: %v", err)
	}
	if ok, err := a.AuthenticateSession(ctx, tok); err != nil || !ok {
		t.Fatalf("fresh session rejected: %v %v", ok, err)
	}
	if ok, _ := a.AuthenticateSession(ctx, "not a token"); ok {
		t.Fatal("unknown token authenticated")
	}
	clk.Advance(failWindow)
	if _, err := a.Login(ctx, goodPassword, from); err != nil {
		t.Fatalf("lockout must lift after the window: %v", err)
	}
}

func TestSessionExpiresIdleAndAbsolute(t *testing.T) {
	a, _, clk := setup(t)
	ctx := context.Background()
	from := netip.MustParseAddr("10.0.0.1")
	if err := a.SetPassword(ctx, goodPassword); err != nil {
		t.Fatal(err)
	}
	idle, _ := a.Login(ctx, goodPassword, from)
	clk.Advance(SessionIdle)
	if ok, _ := a.AuthenticateSession(ctx, idle); ok {
		t.Fatal("idle session survived SessionIdle")
	}
	active, _ := a.Login(ctx, goodPassword, from)
	step := 12 * time.Hour
	for elapsed := time.Duration(0); elapsed+step < SessionAbsolute; elapsed += step {
		clk.Advance(step)
		if ok, err := a.AuthenticateSession(ctx, active); err != nil || !ok {
			t.Fatalf("session used every %v died after %v: %v %v", step, elapsed+step, ok, err)
		}
		waitTouch(t, a, active) // 等异步记录落库，下一次空闲判定才以它为基准
	}
	clk.Advance(step)
	if ok, _ := a.AuthenticateSession(ctx, active); ok {
		t.Fatal("session survived SessionAbsolute although it was kept active")
	}
}

// waitTouch 等待 last_used_at 追上当前时刻：TouchSessionAsync 投递即返回，
// 测试要观察它的结果就得等写协程。
func waitTouch(t *testing.T, a *Auth, token string) {
	t.Helper()
	testwait.Until(t, time.Millisecond, func() bool {
		sess, ok, err := a.store.Session(context.Background(), HashToken(token))
		if err != nil {
			t.Fatal(err)
		}
		return ok && sess.LastUsedAt.Equal(a.clk.Now())
	}, "last_used_at was not recorded")
}

func TestPasswordChangeRevokesSessions(t *testing.T) {
	a, _, _ := setup(t)
	ctx := context.Background()
	from := netip.MustParseAddr("10.0.0.1")
	if err := a.SetPassword(ctx, goodPassword); err != nil {
		t.Fatal(err)
	}
	tok, _ := a.Login(ctx, goodPassword, from)
	if err := a.SetPassword(ctx, "another long password"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := a.AuthenticateSession(ctx, tok); ok {
		t.Fatal("session survived a password change")
	}
	if _, err := a.Login(ctx, goodPassword, from); !errors.Is(err, ErrBadPassword) {
		t.Fatalf("old password still works: %v", err)
	}
}

func TestLogoutEndsSession(t *testing.T) {
	a, _, _ := setup(t)
	ctx := context.Background()
	if err := a.SetPassword(ctx, goodPassword); err != nil {
		t.Fatal(err)
	}
	tok, _ := a.Login(ctx, goodPassword, netip.MustParseAddr("10.0.0.1"))
	if err := a.Logout(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if ok, _ := a.AuthenticateSession(ctx, tok); ok {
		t.Fatal("session survived logout")
	}
}

func TestMalformedPasswordHashReturnsError(t *testing.T) {
	salt := "MDEyMzQ1Njc4OWFiY2RlZg"
	digest := strings.Repeat("A", 43)
	base := "$argon2id$v=19$m=65536,t=3,p=4$" + salt + "$" + digest
	cases := map[string]string{
		"salt-encoding":        strings.Replace(base, salt, "!!", 1),
		"large-memory":         strings.Replace(base, "m=65536", "m=262145", 1),
		"small-memory":         strings.Replace(base, "m=65536", "m=0", 1),
		"large-time":           strings.Replace(base, "t=3", "t=11", 1),
		"large-threads":        strings.Replace(base, "p=4", "p=17", 1),
		"unknown-version":      strings.Replace(base, "v=19", "v=18", 1),
		"malformed-parameters": strings.Replace(base, "t=3", "t=x", 1),
		"zero-time":            strings.Replace(base, "t=3", "t=0", 1),
		"zero-threads":         strings.Replace(base, "p=4", "p=0", 1),
		"empty-digest":         strings.TrimSuffix(base, digest),
		"empty-salt":           strings.Replace(base, salt, "", 1),
		"version-suffix":       strings.Replace(base, "v=19", "v=19junk", 1),
		"parameter-suffix":     strings.Replace(base, "p=4", "p=4junk", 1),
		"short-digest":         strings.TrimSuffix(base, digest) + "YQ",
		"short-salt":           strings.Replace(base, salt, "YQ", 1),
		"digest-encoding":      strings.TrimSuffix(base, digest) + "!!",
	}
	for name, phc := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("malformed PHC panicked: %v", p)
				}
			}()
			if ok, err := VerifyPassword(phc, goodPassword); err == nil || ok {
				t.Fatalf("malformed PHC accepted: ok=%v err=%v", ok, err)
			}
		})
	}
}

func TestFailureWindowAndFullLockDuration(t *testing.T) {
	for _, kind := range []string{"login", "register"} {
		t.Run(kind, func(t *testing.T) {
			a, _, clk := setup(t)
			ctx := context.Background()
			if err := a.SetPassword(ctx, goodPassword); err != nil {
				t.Fatal(err)
			}
			key, _, err := a.OpenWindow(ctx, time.Hour, 10)
			if err != nil {
				t.Fatal(err)
			}
			from := netip.MustParseAddr("10.0.0.1")
			attempt := func(valid bool) error {
				if kind == "login" {
					password := "wrong"
					if valid {
						password = goodPassword
					}
					_, err := a.Login(ctx, password, from)
					return err
				}
				password := "wrong"
				if valid {
					password = key
				}
				_, _, err := a.Register(ctx, password, "n", from)
				return err
			}
			// 最早失败过期后，后面的四次仍位于滑动窗口内。
			_ = attempt(false)
			clk.Advance(10 * time.Minute)
			_ = attempt(false)
			clk.Advance(6 * time.Minute)
			for i := 0; i < 4; i++ {
				_ = attempt(false)
			}
			if err := attempt(true); err == nil {
				t.Fatal("rolling failures did not lock address")
			}
			clk.Advance(10 * time.Minute)
			if err := attempt(true); err == nil {
				t.Fatal("lock ended before fifteen minutes after threshold")
			}
			clk.SetWall(clk.Now().Add(failWindow + time.Second))
			if err := attempt(true); err == nil {
				t.Fatal("wall clock jump lifted lock")
			}
			clk.Advance(5 * time.Minute)
			if err := attempt(true); err != nil {
				t.Fatalf("full lock duration elapsed: %v", err)
			}
		})
	}
}

func TestPasswordChangeDuringLoginDoesNotIssueSession(t *testing.T) {
	a, st, clk := setup(t)
	ctx := context.Background()
	if err := a.SetPassword(ctx, goodPassword); err != nil {
		t.Fatal(err)
	}
	gate := &observationClock{Fake: clk, entered: make(chan struct{}), release: make(chan struct{})}
	gate.block.Store(true)
	a.clk = gate
	type result struct {
		token string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		tok, err := a.Login(ctx, goodPassword, netip.MustParseAddr("10.0.0.1"))
		done <- result{tok, err}
	}()
	<-gate.entered
	// 另一个 Auth 不共享锁，代表 passwd 独立于服务进程的修改。
	other := New(st, clk, a.log)
	err := other.SetPassword(ctx, "another long password")
	close(gate.release)
	got := <-done
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(got.err, ErrBadPassword) || got.token != "" {
		t.Fatalf("stale password issued session: token=%q err=%v", got.token, got.err)
	}
}

func TestConcurrentLoginFailuresCannotBypassLock(t *testing.T) {
	a, _, _ := setup(t)
	ctx := context.Background()
	if err := a.SetPassword(ctx, goodPassword); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	done := make(chan error, failLimit+3)
	for i := 0; i < cap(done); i++ {
		go func() {
			<-start
			_, err := a.Login(ctx, "wrong", netip.MustParseAddr("10.0.0.1"))
			done <- err
		}()
	}
	close(start)
	bad, locked := 0, 0
	for i := 0; i < cap(done); i++ {
		switch err := <-done; {
		case errors.Is(err, ErrBadPassword):
			bad++
		case errors.Is(err, ErrLocked):
			locked++
		default:
			t.Fatalf("unexpected login result: %v", err)
		}
	}
	if bad != failLimit || locked != 3 {
		t.Fatalf("concurrent attempts bypassed lock: bad=%d locked=%d", bad, locked)
	}
}

func TestNodeMutationsDoNotWaitForLogin(t *testing.T) {
	for _, operation := range []string{"Register", "CreateNode", "RotateToken", "DeleteNode", "Load"} {
		t.Run(operation, func(t *testing.T) {
			a, _, clk := setup(t)
			ctx := context.Background()
			if err := a.SetPassword(ctx, goodPassword); err != nil {
				t.Fatal(err)
			}
			id, _, err := a.CreateNode(ctx, "existing")
			if err != nil {
				t.Fatal(err)
			}
			key, _, err := a.OpenWindow(ctx, time.Hour, 1)
			if err != nil {
				t.Fatal(err)
			}
			// 只暂停 Login 在签发前的墙钟读取，存储仍用原时钟，排除写队列阻塞。
			gate := &observationClock{Fake: clk, entered: make(chan struct{}), release: make(chan struct{})}
			gate.block.Store(true)
			a.clk = gate
			release := sync.OnceFunc(func() { close(gate.release) })
			loginDone := make(chan error, 1)
			go func() {
				_, err := a.Login(ctx, goodPassword, netip.MustParseAddr("10.0.0.1"))
				loginDone <- err
			}()
			defer func() {
				release()
				if err := <-loginDone; err != nil {
					t.Errorf("login failed after release: %v", err)
				}
			}()
			select {
			case <-gate.entered:
			case <-time.After(testwait.Bound):
				t.Fatal("Login did not reach session issuance")
			}
			done := make(chan error, 1)
			go func() {
				var err error
				switch operation {
				case "Register":
					_, _, err = a.Register(ctx, key, "registered", netip.MustParseAddr("10.0.0.2"))
				case "CreateNode":
					_, _, err = a.CreateNode(ctx, "new")
				case "RotateToken":
					_, err = a.RotateToken(ctx, id)
				case "DeleteNode":
					err = a.DeleteNode(ctx, id)
				case "Load":
					err = a.Load(ctx)
				}
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("%s failed while Login was pending: %v", operation, err)
				}
			case <-time.After(testwait.Bound):
				// 先释放登录并等待节点操作退出，避免失败路径留下访问存储的协程。
				release()
				<-done
				t.Fatalf("%s waited for unrelated Login", operation)
			}
		})
	}
}

func TestPasswordVerificationUsesStoredParameters(t *testing.T) {
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte(goodPassword), salt, 1, 32, 1, 32)
	enc := base64.RawStdEncoding
	phc := fmt.Sprintf("$argon2id$v=19$m=32,t=1,p=1$%s$%s", enc.EncodeToString(salt), enc.EncodeToString(key))
	if ok, err := VerifyPassword(phc, goodPassword); err != nil || !ok {
		t.Fatalf("stored parameters ignored: %v %v", ok, err)
	}
}

func TestLoginAndRegisterFailuresAreIndependent(t *testing.T) {
	for _, lockedKind := range []string{"login", "register"} {
		t.Run(lockedKind, func(t *testing.T) {
			a, _, _ := setup(t)
			ctx := context.Background()
			if err := a.SetPassword(ctx, goodPassword); err != nil {
				t.Fatal(err)
			}
			key, _, err := a.OpenWindow(ctx, time.Hour, 10)
			if err != nil {
				t.Fatal(err)
			}
			from := netip.MustParseAddr("10.0.0.1")
			attempt := func(kind string, valid bool) error {
				if kind == "login" {
					p := "wrong"
					if valid {
						p = goodPassword
					}
					_, err := a.Login(ctx, p, from)
					return err
				}
				p := "wrong"
				if valid {
					p = key
				}
				_, _, err := a.Register(ctx, p, "n", from)
				return err
			}
			for i := 0; i < failLimit; i++ {
				_ = attempt(lockedKind, false)
			}
			other := "login"
			if lockedKind == "login" {
				other = "register"
			}
			if err := attempt(other, true); err != nil {
				t.Fatalf("failure counter leaked into %s: %v", other, err)
			}
			if err := attempt(lockedKind, true); err == nil {
				t.Fatal("success in other flow cleared lock")
			}
		})
	}
}

func TestSuccessfulLoginClearsFailures(t *testing.T) {
	a, _, _ := setup(t)
	ctx := context.Background()
	from := netip.MustParseAddr("10.0.0.1")
	if err := a.SetPassword(ctx, goodPassword); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 2; round++ {
		for i := 0; i < failLimit-1; i++ {
			_, _ = a.Login(ctx, "wrong", from)
		}
		if _, err := a.Login(ctx, goodPassword, from); err != nil {
			t.Fatalf("success did not clear consecutive failures: %v", err)
		}
	}
}

func TestSessionTouchThreshold(t *testing.T) {
	a, st, clk := setup(t)
	ctx := context.Background()
	if err := a.SetPassword(ctx, goodPassword); err != nil {
		t.Fatal(err)
	}
	tok, err := a.Login(ctx, goodPassword, netip.MustParseAddr("10.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	initial := clk.Now()
	for _, elapsed := range []time.Duration{touchEvery - time.Second, time.Second} {
		clk.Advance(elapsed)
		if ok, err := a.AuthenticateSession(ctx, tok); err != nil || !ok {
			t.Fatalf("live session rejected: %v %v", ok, err)
		}
		// 同步写排在异步刷新之后，完成时才读回，以免用空队列竞态证明未刷新。
		if _, err := st.DeleteExpiredSessions(ctx, initial); err != nil {
			t.Fatal(err)
		}
		sess, ok, err := st.Session(ctx, HashToken(tok))
		want := initial
		if clk.Now().Sub(initial) >= touchEvery {
			want = clk.Now()
		}
		if err != nil || !ok || !sess.LastUsedAt.Equal(want) {
			t.Fatalf("touch threshold: last=%v want=%v ok=%v err=%v", sess.LastUsedAt, want, ok, err)
		}
	}
}
