package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/xjetry/probe/internal/hub/auth"
)

// 这些用例钉住会话读取的不变式：请求里多出来的 cookie（兄弟主机写进浏览器的父域同名 cookie、语法不合的邻居）
// 不能让有效会话失效；通过校验的那个 token 进 ctx，Logout 与撤销当前会话作用在它上面。

type cookieResult struct {
	status     int
	body       []byte
	setCookies []*http.Cookie
}

// cookieCall 用纯 HTTP+JSON 调一个 AdminService 方法，每个 line 作为一个 Cookie 字段行原样发出；不带 cookie jar。
func cookieCall(t *testing.T, h *harness, method, body string, lines ...string) cookieResult {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.srv.URL+"/probe.v1.AdminService/"+method, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, l := range lines {
		req.Header.Add("Cookie", l)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return cookieResult{status: resp.StatusCode, body: b, setCookies: resp.Cookies()}
}

func pair(value string) string { return SessionCookie + "=" + value }

func pairs(values ...string) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = pair(v)
	}
	return strings.Join(parts, "; ")
}

// forgedToken 是 NewToken 形状、库里没有的值：它会走到查库那一步，形状过滤挡不掉它。
func forgedToken(t *testing.T) string {
	t.Helper()
	plain, _ := auth.NewToken()
	return plain
}

// twoSessions 登录两次，返回两个有效会话的 token，第一个是 harness jar 里的那个。
func twoSessions(t *testing.T, h *harness) (string, string) {
	t.Helper()
	h.login(t)
	first := strings.TrimPrefix(sessionCookieHeader(t, h), SessionCookie+"=")
	second := loginRaw(t, h, "").Cookies()[0].Value
	if first == second {
		t.Fatal("two logins issued the same token")
	}
	return first, second
}

// currentSessions 用给定的 Cookie 字段行调 ListSessions，返回标为当前的会话 id。
func currentSessions(t *testing.T, h *harness, lines ...string) []string {
	t.Helper()
	r := cookieCall(t, h, "ListSessions", "{}", lines...)
	if r.status != http.StatusOK {
		t.Fatalf("ListSessions with %q: status %d body %s", lines, r.status, r.body)
	}
	var out struct {
		Sessions []struct {
			ID      string `json:"id"`
			Current bool   `json:"current"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatal(err)
	}
	var cur []string
	for _, s := range out.Sessions {
		if s.Current {
			cur = append(cur, s.ID)
		}
	}
	return cur
}

func TestForgedSessionCookieDoesNotShadowValidOne(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	valid := strings.TrimPrefix(sessionCookieHeader(t, h), SessionCookie+"=")
	for _, forged := range []string{"bogus", forgedToken(t)} {
		for _, tc := range []struct {
			name  string
			lines []string
			want  int
		}{
			{"forged first", []string{pairs(forged, valid)}, 200},
			{"valid first", []string{pairs(valid, forged)}, 200},
			{"forged on an earlier line", []string{pair(forged), pair(valid)}, 200},
			{"forged only", []string{pair(forged)}, 401},
			{"two forged", []string{pairs(forged, forgedToken(t))}, 401},
		} {
			if got := cookieCall(t, h, "ListNodes", "{}", tc.lines...); got.status != tc.want {
				t.Errorf("forged %q, %s: status %d, want %d (body %s)", forged, tc.name, got.status, tc.want, got.body)
			}
		}
	}
}

// 候选数不设上限：设了上限，写 cookie 的一方用更多的值就能把有效值挤出去。
func TestManyForgedSessionCookiesDoNotCrowdOutValidOne(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	valid := strings.TrimPrefix(sessionCookieHeader(t, h), SessionCookie+"=")
	values := make([]string, 0, 1001)
	for range 1000 {
		values = append(values, forgedToken(t))
	}
	values = append(values, valid)
	if got := cookieCall(t, h, "ListNodes", "{}", pairs(values...)); got.status != 200 {
		t.Errorf("1000 forged values ahead of the valid one: status %d, want 200 (body %s)", got.status, got.body)
	}
}

// 同一行里别的 cookie 语法不合（无名 cookie、带引号的 JSON、非 ASCII 值、名字不是 token）或个数超过
// net/http 解析器的 3000 上限，都不能连带丢掉会话。前四种是实测中 Chromium 或 WebKit 会把兄弟主机写的 cookie
// 与会话放进同一行发出的形状（WebKit 不发无名 cookie）。
func TestMalformedNeighbourCookiesDoNotHideSession(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	valid := pair(strings.TrimPrefix(sessionCookieHeader(t, h), SessionCookie+"="))
	for _, line := range []string{
		valid + "; x",
		`j={"a":1}; ` + valid,
		"u=é; " + valid,
		"a:b=1; " + valid,
		strings.Repeat("x=1; ", 3000) + valid,
	} {
		if got := cookieCall(t, h, "ListNodes", "{}", line); got.status != 200 {
			shown := line
			if len(shown) > 80 {
				shown = shown[:80] + "..."
			}
			t.Errorf("Cookie %q: status %d, want 200 (body %s)", shown, got.status, got.body)
		}
	}
}

// 多个值都有效时取顺序上第一个有效的；会话列表的"当前"读的就是进 ctx 的那个。
func TestTwoValidSessionCookiesPickFirstValid(t *testing.T) {
	h := newHarness(t, "")
	a, b := twoSessions(t, h)
	forged := forgedToken(t)
	for _, tc := range []struct {
		line string
		want string
	}{
		{pairs(a, b), sessionID(a)},
		{pairs(b, a), sessionID(b)},
		{pairs(forged, b, a), sessionID(b)},
	} {
		if got := currentSessions(t, h, tc.line); len(got) != 1 || got[0] != tc.want {
			t.Errorf("Cookie %q: current sessions %v, want [%s]", tc.line, got, tc.want)
		}
	}
}

func TestLogoutWithForgedCookieRevokesTheValidSession(t *testing.T) {
	h := newHarness(t, "")
	a, b := twoSessions(t, h)
	r := cookieCall(t, h, "Logout", "{}", pairs(forgedToken(t), a))
	if r.status != 200 {
		t.Fatalf("Logout: status %d body %s", r.status, r.body)
	}
	if len(r.setCookies) != 1 || r.setCookies[0].Name != SessionCookie || r.setCookies[0].MaxAge != -1 {
		t.Errorf("Logout must clear the session cookie: %v", r.setCookies)
	}
	if got := cookieCall(t, h, "ListNodes", "{}", pair(a)); got.status != 401 {
		t.Errorf("session presented alongside the forged value survived Logout: status %d", got.status)
	}
	if got := cookieCall(t, h, "ListNodes", "{}", pair(b)); got.status != 200 {
		t.Errorf("Logout revoked an unrelated session: status %d", got.status)
	}
}

func TestRevokeCurrentSessionWithForgedCookie(t *testing.T) {
	h := newHarness(t, "")
	a, b := twoSessions(t, h)
	r := cookieCall(t, h, "RevokeSession", `{"id":"`+sessionID(a)+`"}`, pairs(forgedToken(t), a))
	if r.status != 200 {
		t.Fatalf("RevokeSession: status %d body %s", r.status, r.body)
	}
	if len(r.setCookies) != 1 || r.setCookies[0].Name != SessionCookie || r.setCookies[0].MaxAge != -1 {
		t.Errorf("revoking the current session must clear its cookie: %v", r.setCookies)
	}
	if got := cookieCall(t, h, "ListNodes", "{}", pair(a)); got.status != 401 {
		t.Errorf("revoked current session still authenticates: status %d", got.status)
	}
	if got := cookieCall(t, h, "ListNodes", "{}", pair(b)); got.status != 200 {
		t.Errorf("revoking the current session revoked another one: status %d", got.status)
	}
}

// 会话校验失败不是登录失败。先发一批只带伪造值、以及伪造值与有效值混在一起的请求，再从同一来源输错 4 次密码：
// 锁定阈值是 5 次（auth.failLimit），伪造值只要被记了一次，加上 4 次输错就满 5 次，随后的正确密码被锁在外面。
func TestForgedSessionCookiesDoNotCountAsLoginFailures(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	valid := strings.TrimPrefix(sessionCookieHeader(t, h), SessionCookie+"=")
	for i := range 10 {
		if got := cookieCall(t, h, "ListNodes", "{}", pairs("bogus", forgedToken(t))); got.status != 401 {
			t.Fatalf("forged-only request %d: status %d", i, got.status)
		}
		if got := cookieCall(t, h, "ListNodes", "{}", pairs(forgedToken(t), valid)); got.status != 200 {
			t.Fatalf("forged+valid request %d: status %d", i, got.status)
		}
	}
	for i := range 4 {
		r := cookieCall(t, h, "Login", `{"password":"incorrect"}`)
		if r.status != 401 || !strings.Contains(string(r.body), "wrong password") {
			t.Fatalf("wrong password %d after forged cookies: status %d body %s", i+1, r.status, r.body)
		}
	}
	if r := cookieCall(t, h, "Login", fmt.Sprintf(`{"password":%q}`, password)); r.status != 200 {
		t.Errorf("forged session cookies counted as login failures: status %d body %s", r.status, r.body)
	}
}
