package api

import (
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/hub/auth"
)

func sessionRequest[T any](msg *T, token string) *connect.Request[T] {
	r := connect.NewRequest(msg)
	r.Header().Set("Cookie", SessionCookie+"="+token)
	return r
}

func sessionID(token string) string {
	h := auth.HashToken(token)
	return hex.EncodeToString(h[:])
}

func listSessions(t *testing.T, h *harness, token string) []*probev1.Session {
	t.Helper()
	client := probev1connect.NewAdminServiceClient(h.srv.Client(), h.srv.URL)
	r, err := client.ListSessions(t.Context(), sessionRequest(&probev1.ListSessionsRequest{}, token))
	if err != nil {
		t.Fatal(err)
	}
	return r.Msg.Sessions
}

func TestSessionsListAndRevoke(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	first := strings.TrimPrefix(sessionCookieHeader(t, h), SessionCookie+"=")
	created := h.clk.Now().Unix()
	h.clk.Advance(10 * time.Second)
	second := loginRaw(t, h, "").Cookies()[0].Value
	client := probev1connect.NewAdminServiceClient(h.srv.Client(), h.srv.URL)
	for _, token := range []string{first, second} {
		rows := listSessions(t, h, token)
		want := []*probev1.Session{
			{Id: sessionID(second), CreatedAt: created + 10, LastUsedAt: created + 10, Current: token == second},
			{Id: sessionID(first), CreatedAt: created, LastUsedAt: created, Current: token == first},
		}
		if !proto.Equal(&probev1.ListSessionsResponse{Sessions: rows}, &probev1.ListSessionsResponse{Sessions: want}) {
			t.Errorf("session list metadata/current/order: got %v, want %v", rows, want)
		}
	}
	before := &probev1.ListSessionsResponse{Sessions: listSessions(t, h, first)}
	if _, err := client.RevokeSession(t.Context(), sessionRequest(&probev1.RevokeSessionRequest{Id: strings.Repeat("f", 64)}, first)); err != nil {
		t.Fatalf("revoke unknown session must succeed: %v", err)
	}
	if after := (&probev1.ListSessionsResponse{Sessions: listSessions(t, h, first)}); !proto.Equal(before, after) {
		t.Errorf("unknown revoke changed list: %v", after)
	}
	for range 2 {
		r, err := client.RevokeSession(t.Context(), sessionRequest(&probev1.RevokeSessionRequest{Id: sessionID(second)}, first))
		if err != nil {
			t.Fatalf("revoke other/repeat must succeed: %v", err)
		}
		if r.Header().Get("Set-Cookie") != "" {
			t.Error("revoking another session cleared current cookie")
		}
	}
	if got := rawCall(t, h, "ListNodes", "{}", map[string][]string{"Cookie": {SessionCookie + "=" + second}}); got.status != 401 {
		t.Errorf("revoked cookie still authenticates: %+v", got)
	}
	if rows := listSessions(t, h, first); len(rows) != 1 || rows[0].Id != sessionID(first) || !rows[0].Current {
		t.Errorf("revoking other session changed survivor: %v", rows)
	}
}

func TestSessionIDsCannotAuthenticate(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	current := strings.TrimPrefix(sessionCookieHeader(t, h), SessionCookie+"=")
	loginRaw(t, h, "")
	rows := listSessions(t, h, current)
	if len(rows) != 2 {
		t.Fatalf("expected two sessions for credential probe, got %d", len(rows))
	}
	for _, row := range rows {
		if got := rawCall(t, h, "ListNodes", "{}", map[string][]string{"Cookie": {SessionCookie + "=" + row.Id}}); got.status != 401 {
			t.Errorf("listed session id can impersonate its owner: status=%d", got.status)
		}
	}
}

func TestRevokeCurrentSessionClearsCookie(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			h := newHarness(t, "127.0.0.1/32")
			h.login(t)
			token := strings.TrimPrefix(sessionCookieHeader(t, h), SessionCookie+"=")
			req := sessionRequest(&probev1.RevokeSessionRequest{Id: strings.ToUpper(sessionID(token))}, token)
			req.Header().Set("X-Forwarded-Proto", scheme)
			client := probev1connect.NewAdminServiceClient(h.srv.Client(), h.srv.URL)
			r, err := client.RevokeSession(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			cookies := (&http.Response{Header: r.Header()}).Cookies()
			if len(cookies) != 1 {
				t.Errorf("current revoke must clear exactly one cookie: %v", cookies)
			} else {
				c := cookies[0]
				if c.Name != SessionCookie || c.Value != "" || c.MaxAge != -1 || c.Path != "/" || c.Domain != "" || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Secure != (scheme == "https") {
					t.Errorf("current revoke clearing cookie attributes: %+v", c)
				}
			}
			if got := rawCall(t, h, "ListNodes", "{}", map[string][]string{"Cookie": {SessionCookie + "=" + token}}); got.status != 401 {
				t.Errorf("current session survived revoke: %+v", got)
			}
		})
	}
}

func TestListSessionsExpiryMatchesAuthentication(t *testing.T) {
	for _, kind := range []string{"idle", "absolute"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t, "")
			h.login(t)
			active := strings.TrimPrefix(sessionCookieHeader(t, h), SessionCookie+"=")
			old, hash := auth.NewToken()
			now := h.clk.Now()
			phc, _, err := h.store.AdminPasswordHash(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			created, expires := now, now.Add(auth.SessionAbsolute)
			if kind == "absolute" {
				created = now.Add(-auth.SessionAbsolute + time.Hour)
				expires = now.Add(time.Hour)
			}
			if err := h.store.CreateSession(t.Context(), hash, created, expires, phc); err != nil {
				t.Fatal(err)
			}
			// 绝对过期独立于空闲过期：保持旧会话的最近使用时刻为现在。
			touched := make(chan error, 1)
			h.store.TouchSessionAsync(hash, now, func(err error) { touched <- err })
			if err := <-touched; err != nil {
				t.Fatal(err)
			}
			duration := auth.SessionIdle
			if kind == "absolute" {
				duration = time.Hour
			}
			h.clk.Advance(duration - time.Second)
			if rows := listSessions(t, h, active); len(rows) != 2 {
				t.Errorf("unexpired %s session missing: %v", kind, rows)
			}
			// 鉴权的异步 touch 落库后再推进，调用者本身不会在边界失效。
			if _, err := h.store.DeleteExpiredSessions(t.Context(), now); err != nil {
				t.Fatal(err)
			}
			h.clk.Advance(time.Second)
			rows := listSessions(t, h, active)
			if len(rows) != 1 || rows[0].Id != sessionID(active) {
				t.Errorf("expired %s session leaked into list: %v", kind, rows)
			}
			// 先观察列表再鉴权，避免鉴权删掉过期行替列表的过滤兜底。
			if got := rawCall(t, h, "ListNodes", "{}", map[string][]string{"Cookie": {SessionCookie + "=" + old}}); got.status != 401 {
				t.Errorf("expired %s cookie authenticates: %+v", kind, got)
			}
			client := probev1connect.NewAdminServiceClient(h.srv.Client(), h.srv.URL)
			// 鉴权已清掉旧行，重新写入同一过期会话，覆盖仍在库中的过期 hash 撤销。
			if err := h.store.CreateSession(t.Context(), hash, created, expires, phc); err != nil {
				t.Fatal(err)
			}
			if _, err := client.RevokeSession(t.Context(), sessionRequest(&probev1.RevokeSessionRequest{Id: sessionID(old)}, active)); err != nil {
				t.Errorf("expired revoke must succeed: %v", err)
			}
			if _, found, err := h.store.Session(t.Context(), hash); err != nil || found {
				t.Errorf("expired session remains after revoke: found=%v err=%v", found, err)
			}
		})
	}
}

func TestSessionMethodsRejectAPITokens(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	_, token := createToken(t, h, "session-test")
	for _, method := range []string{"ListSessions", "RevokeSession"} {
		if got := rawCall(t, h, method, `{"id":"`+strings.Repeat("f", 64)+`"}`, bearer(token)); got.status != 403 || got.code != "permission_denied" {
			t.Errorf("%s token must be PermissionDenied: %+v", method, got)
		}
	}
}

func TestRevokeSessionRejectsMalformedID(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	for _, id := range []string{"", strings.Repeat("a", 62), strings.Repeat("a", 66), strings.Repeat("z", 64)} {
		_, err := h.admin.RevokeSession(t.Context(), connect.NewRequest(&probev1.RevokeSessionRequest{Id: id}))
		if codeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "id must be 64 hexadecimal characters") {
			t.Errorf("malformed id %q accepted or unexplained: %v", id, err)
		}
	}
}
