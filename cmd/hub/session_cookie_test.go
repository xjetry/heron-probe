package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/clock"
)

// 会话读取把同名 cookie 的全部 token 形状的值合成一条 SQL 查询、不设候选数上限，它不超出 SQLite 的变量上限
// 靠两件事（推导见 auth.AuthenticateSession）：不是 token 形状的值在查库前丢弃，成形的值每个至少占 79 字节；
// serve 的 http.Server 限制请求头大小。这里在真实 serve 上用互不相同的伪造值把 Cookie 头填到服务端还肯读的最大长度，
// 有效值排在最后，断言它仍然 200。两组伪造值各钉一件事：
//   - token 形状：每个都走进 IN 查询。调高 MaxHeaderBytes 让候选数越过变量上限时，这一组变成 500。
//   - 短值：同样大小的头能装下远超变量上限的个数。形状过滤失效时它们全进 IN 查询，这一组变成 500。
//
// 探测用不复用连接的客户端：复用的连接上，设读取上限之前已进读缓冲区的字节数由那次读取实际拿到多少决定
// （至多 4096），不受测试控制；新连接上没有这部分，边界是确定的，比推导里的最坏情形少一个 4096 字节缓冲区，
// 约 52 个 token 形状的候选。
func TestServeSessionCookieFilledToHeaderLimit(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hub.db")
	const pw = "a sufficiently long password"
	if err := runPasswdWith([]string{"--db", db}, pipeWith(t, pw+"\n"), io.Discard); err != nil {
		t.Fatal(err)
	}
	url, _, _ := startTestHub(t, db, clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	logged, err := probev1connect.NewAdminServiceClient(http.DefaultClient, url).Login(context.Background(), connect.NewRequest(&probev1.LoginRequest{Password: pw}))
	if err != nil {
		t.Fatal(err)
	}
	cookies := (&http.Response{Header: logged.Header()}).Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookies: %v", cookies)
	}
	valid := cookies[0].Name + "=" + cookies[0].Value
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	for _, tc := range []struct {
		name   string
		forged func(i int) string
		// hi 大于这组伪造值在默认头部上限下能装下的个数，也大于 SQLite 的变量上限 32766：头部上限若大到装得下 hi 个，
		// 搜索停在 hi，下面的断言落在装得最多的那一次上。
		hi int
	}{
		{"token-shaped", func(i int) string { return fmt.Sprintf("%064x", i) }, 40000},
		{"short", func(i int) string { return strconv.FormatInt(int64(i), 36) }, 80000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// call 发 n 个伪造值加一个有效值；服务端拒读（431，或写到一半断开连接）时返回 0。
			call := func(n int) int {
				var b strings.Builder
				for i := range n {
					b.WriteString("probe_session=" + tc.forged(i) + ";")
				}
				b.WriteString(valid)
				req, err := http.NewRequest(http.MethodPost, url+"/probe.v1.AdminService/ListNodes", strings.NewReader("{}"))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Cookie", b.String())
				resp, err := client.Do(req)
				if err != nil {
					return 0
				}
				defer resp.Body.Close()
				io.Copy(io.Discard, resp.Body)
				if resp.StatusCode == http.StatusRequestHeaderFieldsTooLarge {
					return 0
				}
				return resp.StatusCode
			}
			lo, hi := 0, tc.hi
			for lo < hi {
				mid := (lo + hi + 1) / 2
				if call(mid) != 0 {
					lo = mid
				} else {
					hi = mid - 1
				}
			}
			if got := call(lo); got != http.StatusOK {
				t.Fatalf("Cookie header filled to the server's limit with %d forged values ahead of the valid one: status %d, want 200", lo, got)
			}
			if lo < tc.hi {
				if got := call(lo + 1); got != 0 {
					t.Fatalf("%d forged values were accepted too (status %d): the search did not reach the header limit", lo+1, got)
				}
			}
			t.Logf("header limit reached at %d forged values plus the valid one", lo)
		})
	}
}
