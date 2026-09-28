package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/hub/store"
)

func TestParseTTL(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{"", 30 * time.Second, false},
		{"45s", 45 * time.Second, false},
		{"10s", 10 * time.Second, false},
		{"180s", 180 * time.Second, false},
		{"181s", 0, true},
		{"9s", 0, true},
		{"banana", 0, true},
	}
	for _, c := range cases {
		got, err := parseTTL(c.in)
		if (err != nil) != c.err || got != c.want {
			t.Fatalf("parseTTL(%q) = %v, %v; want %v, err=%v", c.in, got, err, c.want, c.err)
		}
	}
}

func TestServeUsageShowsRetentionMinima(t *testing.T) {
	output, _ := hubCommand(t, "serve", "--help").CombinedOutput()
	for _, tc := range []struct {
		flag    string
		minimum time.Duration
		want    string
	}{
		{"retention-1m", store.MinRetentionM1, "6h0m0s"},
		{"retention-5m", store.MinRetentionM5, "168h0m0s"},
		{"retention-1h", store.MinRetentionH1, "168h0m0s"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			_, section, _ := strings.Cut(string(output), "-"+tc.flag+" duration\n")
			section, _, _ = strings.Cut(section, "\n  -")
			if !strings.Contains(section, "minimum "+tc.minimum.String()+")") {
				t.Errorf("usage differs from Validate minimum %v: %q", tc.minimum, section)
			}
			// 独立的对外取值防止常量和帮助一起漂移后自证正确。
			if !strings.Contains(section, "minimum "+tc.want+")") {
				t.Errorf("usage minimum changed: got %q, want minimum %s", section, tc.want)
			}
		})
	}
}

func TestIsLoopback(t *testing.T) {
	if !isLoopback("127.0.0.1:8080") || !isLoopback("[::1]:8080") || !isLoopback("localhost:8080") {
		t.Fatal("loopback addresses misclassified")
	}
	if isLoopback("0.0.0.0:8080") || isLoopback(":8080") || isLoopback("10.0.0.1:8080") {
		t.Fatal("non-loopback addresses misclassified")
	}
}

func TestServeRejectsInvalidRetention(t *testing.T) {
	t.Setenv("PROBE_OFFLINE_AFTER", "30s")
	for _, tc := range []struct{ flag, value, want string }{
		{"--retention-1m", "5h59m59s", "minimum is 6h"},
		{"--retention-1m", "40d", `invalid value "40d"`},
		{"--retention-5m", "167h59m59s", "minimum is 168h"},
		{"--retention-1h", "167h", "minimum is 168h"},
		{"--retention-1m", "800h", "must not shrink"},
		{"--retention-5m", "9000h", "must not shrink"},
	} {
		t.Run(tc.flag+"/"+tc.value, func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "t.db")
			err := runServe([]string{"--db", db, "--listen", "127.0.0.1:65536", tc.flag, tc.value})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("retention validation: err=%v want=%q", err, tc.want)
			}
			if _, err := os.Stat(db); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("invalid retention touched database: %v", err)
			}
		})
	}
}

func TestLoadZone(t *testing.T) {
	t.Setenv("TZ", "Asia/Tokyo")
	if loc, fallback, err := loadZone(""); err != nil || fallback || loc.String() != "Asia/Tokyo" {
		t.Fatalf("TZ must select Asia/Tokyo without fallback: %v %v %v", loc, fallback, err)
	}
	if loc, _, err := loadZone("Asia/Shanghai"); err != nil || loc.String() != "Asia/Shanghai" {
		t.Fatalf("Asia/Shanghai: %v %v", loc, err)
	}
	if _, _, err := loadZone("Mars/Olympus"); err == nil || !strings.Contains(err.Error(), "--timezone") {
		t.Fatalf("unknown zone: %v, want an error naming the flag", err)
	}
}

func TestLoadZoneFallsBackPastInvalidTZ(t *testing.T) {
	t.Setenv("TZ", "Mars/Olympus")
	loc, _, err := loadZone("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := time.LoadLocation(loc.String()); err != nil || loc.String() == "Local" {
		t.Fatalf("unusable resolved zone %q: %v", loc, err)
	}
}

func TestLoadZoneResolutionSources(t *testing.T) {
	for _, tc := range []struct {
		name, explicit, tz, target, want string
		fallback, twoHops                bool
	}{
		{name: "environment wins", tz: "Asia/Tokyo", target: "UTC", want: "Asia/Tokyo"},
		{name: "invalid environment uses link", tz: "Mars/Olympus", target: "Asia/Shanghai", want: "Asia/Shanghai"},
		{name: "local environment uses link", tz: "Local", target: "Asia/Shanghai", want: "Asia/Shanghai"},
		{name: "missing sources", want: "UTC", fallback: true},
		{name: "invalid link", target: "Mars/Olympus", want: "UTC", fallback: true},
		{name: "explicit right UTC", explicit: "right/UTC", want: "UTC"},
		{name: "posix environment", tz: "posix/Asia/Tokyo", want: "Asia/Tokyo"},
		{name: "right link", target: "right/UTC", want: "UTC"},
		{name: "two hops", target: "Asia/Tokyo", want: "Asia/Tokyo", twoHops: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TZ", tc.tz)
			old := localtimePath
			localtimePath = filepath.Join(t.TempDir(), "localtime")
			t.Cleanup(func() { localtimePath = old })
			if tc.target != "" {
				target := filepath.Join(filepath.Dir(localtimePath), "zoneinfo", tc.target)
				if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if tc.twoHops {
					mid := filepath.Join(filepath.Dir(localtimePath), "mid")
					if err := os.Symlink(target, mid); err != nil {
						t.Fatal(err)
					}
					target = mid
				}
				if err := os.Symlink(target, localtimePath); err != nil {
					t.Fatal(err)
				}
			}
			loc, fallback, err := loadZone(tc.explicit)
			if err != nil || fallback != tc.fallback || loc.String() != tc.want {
				t.Fatalf("zone=%v fallback=%v err=%v, want %s/%v", loc, fallback, err, tc.want, tc.fallback)
			}
		})
	}
}

func TestLoadZoneRejectsExplicitLocal(t *testing.T) {
	if _, _, err := loadZone("Local"); err == nil || !strings.Contains(err.Error(), "--timezone") {
		t.Fatalf("Local must be rejected with a flag error: %v", err)
	}
}

func TestParseThemeOrigin(t *testing.T) {
	for _, tc := range []struct{ in, want, err string }{
		{"", "", ""},
		{"https://Status.Example.com", "https://status.example.com", ""},
		{"https://status.example.com/", "https://status.example.com", ""},
		{"http://127.0.0.1:18180", "http://127.0.0.1:18180", ""},
		// 主机名规范成浏览器放进 Host 头的形态：非 ASCII 转 punycode（非过渡处理，ß 不折成 ss；下划线与 ab-- 这样的标签
		// 浏览器接受，这里也接受），去一个尾点，IP 字面量经 netip。
		{"https://状态.test", "https://xn--t7t692b.test", ""},
		{"https://Straße.de:8443", "https://xn--strae-oqa.de:8443", ""},
		{"https://ＳＴＡＴＵＳ.example.com", "https://status.example.com", ""},
		{"https://my_host.ab--cd.test", "https://my_host.ab--cd.test", ""},
		{"https://status.example.com.", "https://status.example.com", ""},
		{"http://[0:0::1]", "http://[::1]", ""},
		{"http://[0:0::1]:8080", "http://[::1]:8080", ""},
		{"http://[2001:DB8::1]", "http://[2001:db8::1]", ""},
		{"http://[::FFFF:102:304]", "http://[::ffff:1.2.3.4]", ""},
		{"https://xn--zz.test", "", "not a valid domain name"},
		// net/http 对 Host 里的 " < > 回 400；~ ! $ 与 % 不是主机名字符。
		{`https://a"b.test`, "", "only letters, digits"},
		{"https://a<b.test", "", "only letters, digits"},
		{"https://a>b.test", "", "only letters, digits"},
		{"https://a~b.test", "", "only letters, digits"},
		{"https://a!b.test", "", "only letters, digits"},
		{"https://a$b.test", "", "only letters, digits"},
		{"https://a%25b.test", "", "only letters, digits"},
		{"https://.", "", "needs a hostname"},
		{"http://[fe80::1%25en0]", "", "IPv6 zone"},
		{"http://127.1", "", "ends in a number"},
		{"http://0x7f.0.0.1", "", "ends in a number"},
		{"http://127.000.0.1", "", "ends in a number"},
		{"http://status.example.123", "", "ends in a number"},
		{"status.example.com", "", "scheme must be https or http"},
		{"ftp://status.example.com", "", "scheme must be https or http"},
		{"https://", "", "needs a hostname"},
		{"https:status.example.com", "", "needs a hostname"},
		{"https://u:p@status.example.com", "", "must not carry credentials"},
		{"https://example.com/theme", "", "without a path, query or fragment"},
		{"https://example.com/?x=1", "", "without a path, query or fragment"},
		{"https://example.com/#x", "", "without a path, query or fragment"},
	} {
		got, err := parseThemeOrigin(tc.in)
		if tc.err == "" {
			if err != nil || got != tc.want {
				t.Errorf("parseThemeOrigin(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.err) || !strings.Contains(err.Error(), "--theme-origin") {
			t.Errorf("parseThemeOrigin(%q) = %q, %v; want an error naming --theme-origin and %q", tc.in, got, err, tc.err)
		}
	}
}

// parseThemeOrigin 接受的主机名，放进 Host 头都被 net/http 交给处理器（不回 400），到达时 hostname 规范的结果与
// parseThemeOrigin 的一致：启动成功的配置，带着它的请求一定到得了分流器。把每个可见 ASCII 字节与几个非 ASCII 字符放进主机名
// 中间逐个试。请求用原始连接发出：Go 的客户端自己会拒绝一部分 Host，照不到服务端。另钉住 hostname 注释依赖的 net/http
// 行为：非 ASCII 的 Host 以 400 拒绝。
func TestParseThemeOriginAcceptsOnlyHostsNetHTTPServes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, hostname(r.Host)) }))
	t.Cleanup(srv.Close)
	get := func(host string) (int, string) {
		t.Helper()
		conn, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", host)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatalf("Host %q: %v", host, err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(b)
	}
	var hosts []string
	for c := byte(0x21); c < 0x7f; c++ {
		hosts = append(hosts, "a"+string(c)+"b.test")
	}
	hosts = append(hosts, "a%25b.test", "a状b.test", "aßb.test", "Ａb.test")
	accepted := 0
	for _, h := range hosts {
		origin, err := parseThemeOrigin("https://" + h)
		if err != nil {
			continue
		}
		accepted++
		u, err := url.Parse(origin)
		if err != nil {
			t.Fatal(err)
		}
		if code, got := get(u.Host); code != http.StatusOK || got != hostname(u.Host) {
			t.Errorf("--theme-origin https://%s is accepted as %s, but a request with Host %q gets %d %q", h, origin, u.Host, code, got)
		}
	}
	if accepted < 36 {
		t.Fatalf("only %d of %d candidate hostnames were accepted; letters and digits alone should be", accepted, len(hosts))
	}
	if code, _ := get("状态.test"); code != http.StatusBadRequest {
		t.Errorf("non-ASCII Host: %d, want net/http to reject it with 400", code)
	}
}

// 写错的 --theme-origin 在打开数据库之前拒绝：配置有误时 hub 不留下任何副作用。
func TestServeRejectsInvalidThemeOriginBeforeOpeningTheDatabase(t *testing.T) {
	t.Setenv("PROBE_OFFLINE_AFTER", "30s")
	db := filepath.Join(t.TempDir(), "t.db")
	err := runServe([]string{"--db", db, "--listen", "127.0.0.1:65536", "--theme-origin", "https://example.com/themes"})
	if err == nil || !strings.Contains(err.Error(), "--theme-origin") {
		t.Fatalf("err = %v, want a --theme-origin error", err)
	}
	if _, err := os.Stat(db); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid --theme-origin touched the database: %v", err)
	}
}
