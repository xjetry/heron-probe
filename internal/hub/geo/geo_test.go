package geo

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/alert"
	"github.com/xjetry/probe/internal/hub/metric"
	"github.com/xjetry/probe/internal/hub/store"
)

func TestIsPublic(t *testing.T) {
	for _, c := range []struct {
		class string
		addr  string
		want  bool
	}{
		{"RFC 1918 10/8", "10.1.2.3", false},
		{"RFC 1918 172.16/12", "172.31.255.1", false},
		{"RFC 1918 192.168/16", "192.168.1.1", false},
		{"CGNAT 100.64/10", "100.127.0.1", false},
		{"回环 127/8", "127.8.8.8", false},
		{"回环 ::1", "::1", false},
		{"链路本地 169.254/16", "169.254.1.1", false},
		{"链路本地 fe80::/10", "fe80::1", false},
		{"ULA fc00::/7", "fd12:3456::1", false},
		{"未指定 0.0.0.0", "0.0.0.0", false},
		{"未指定 ::", "::", false},
		{"本网络 0/8", "0.1.2.3", false},
		{"组播 224/4", "224.0.0.251", false},
		{"组播 ff00::/8", "ff02::1", false},
		{"受限广播", "255.255.255.255", false},
		{"保留 240/4", "240.0.0.1", false},
		{"IETF 协议分配 192.0.0/24", "192.0.0.9", false},
		{"文档 192.0.2/24", "192.0.2.1", false},
		{"文档 198.51.100/24", "198.51.100.1", false},
		{"文档 203.0.113/24", "203.0.113.7", false},
		{"文档 2001:db8::/32", "2001:db8::7", false},
		{"基准测试 198.18/15", "198.19.0.1", false},
		{"丢弃 100::/64", "100::1", false},
		{"本地 NAT64 64:ff9b:1::/48", "64:ff9b:1::1", false},
		{"IPv4 映射的私网", "::ffff:10.0.0.1", false},
		{"公网 IPv4", "8.8.8.8", true},
		{"公网 IPv4（CGNAT 段之外）", "100.128.0.1", true},
		{"公网 IPv4（172.16/12 之外）", "172.32.0.1", true},
		{"公网 IPv6", "2606:4700::1111", true},
		{"IPv4 映射的公网", "::ffff:1.1.1.1", true},
	} {
		if got := IsPublic(netip.MustParseAddr(c.addr)); got != c.want {
			t.Errorf("%s: IsPublic(%s) = %v, want %v", c.class, c.addr, got, c.want)
		}
	}
	if IsPublic(netip.Addr{}) {
		t.Error("the zero Addr is public")
	}
}

// fakeService 是 geo.url 指向的假服务：记下每个请求，按 reply 应答。
type fakeService struct {
	srv   *httptest.Server
	mu    sync.Mutex
	reqs  []*http.Request
	reply func(w http.ResponseWriter, r *http.Request)
}

func (f *fakeService) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.reqs {
		out = append(out, r.URL.Path)
	}
	return out
}

type fixture struct {
	t     *testing.T
	clk   *clock.Fake
	st    *store.Store
	svc   *fakeService
	r     *Resolver
	ts    int64
	nodes map[string]int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, clk: clock.NewFake(time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)), nodes: map[string]int64{}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var err error
	f.st, err = store.Open(filepath.Join(t.TempDir(), "hub.db"), f.clk, log, store.MigrateSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.st.Close() })
	f.svc = &fakeService{reply: func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "US\n") }}
	f.svc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.svc.mu.Lock()
		f.svc.reqs = append(f.svc.reqs, r)
		reply := f.svc.reply
		f.svc.mu.Unlock()
		reply(w, r)
	}))
	t.Cleanup(f.svc.srv.Close)
	f.restart()
	return f
}

// restart 换一个新的查询器，与 hub 重启后一样没有任何内存状态；库与假服务不变。
func (f *fixture) restart() {
	f.r = New(f.st, alert.NewHTTPClient(), f.clk, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func (f *fixture) enable(on bool) {
	f.t.Helper()
	url := f.svc.srv.URL + "/{ip}/country"
	if _, err := f.st.SaveSettings(f.t.Context(), store.SiteSettings{Theme: store.DefaultTheme}, store.GeoUpdate{Enabled: &on, URL: &url}); err != nil {
		f.t.Fatal(err)
	}
}

// report 让节点以 addr 为来源上报一次并刷出：与 ingest 的分钟刷出走同一个写入口。
func (f *fixture) report(name, addr string) int64 {
	f.t.Helper()
	id, ok := f.nodes[name]
	if !ok {
		var err error
		id, _, err = f.st.CreateNode(f.t.Context(), name, []byte(name))
		if err != nil {
			f.t.Fatal(err)
		}
		f.nodes[name] = id
	}
	f.ts += 60
	row := metric.Row{NodeID: id, TS: f.ts, Bucket: metric.NewBucket(), LastSeen: f.clk.Now(), Source: addr}
	if _, err := f.st.WriteMinuteBatch(f.t.Context(), metric.Batch{Rows: []metric.Row{row}}); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) sweep() {
	f.t.Helper()
	if err := f.r.Sweep(f.t.Context()); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) country(id int64) (country, ip string) {
	f.t.Helper()
	n, err := f.st.GetNode(f.t.Context(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return n.Country, n.CountryIP
}

func (f *fixture) wantRequests(want ...string) {
	f.t.Helper()
	if got := f.svc.paths(); !slices.Equal(got, want) {
		f.t.Fatalf("requests = %q, want %q", got, want)
	}
}

func (f *fixture) wantCountry(id int64, country, ip string) {
	f.t.Helper()
	if c, i := f.country(id); c != country || i != ip {
		f.t.Fatalf("node %d country = %q for %q, want %q for %q", id, c, i, country, ip)
	}
}

// 开启后公网地址被查且只查一次：应答去掉换行后写入，与所查地址成对；有了答案之后不再查，退避期过了也不查。
// 请求只带地址：GET、无请求体、没有凭据头。
func TestLookupQueriesAPublicAddressOnce(t *testing.T) {
	f := newFixture(t)
	f.enable(true)
	id := f.report("a", "8.8.8.8")
	f.sweep()
	f.wantRequests("/8.8.8.8/country")
	f.wantCountry(id, "US", "8.8.8.8")
	r := f.svc.reqs[0]
	if r.Method != http.MethodGet || r.ContentLength != 0 || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
		t.Fatalf("request carried more than the address: %s %v length %d", r.Method, r.Header, r.ContentLength)
	}
	f.sweep()
	f.clk.Advance(2 * time.Hour)
	f.report("a", "8.8.8.8")
	f.sweep()
	f.wantRequests("/8.8.8.8/country")
	f.wantCountry(id, "US", "8.8.8.8")
}

// 只接受 200 且去掉首尾空白后恰为两个大写字母的应答，其余按失败：不写国家，记一小时退避。一小时写字面值，
// 不引用常量：常量改了而 spec 没改，这里要红。302 不跟随：跳转目标即使应答 US 也不被请求。
func TestLookupFailuresBackOffForAnHour(t *testing.T) {
	for _, c := range []struct {
		name  string
		reply func(w http.ResponseWriter, r *http.Request)
	}{
		{"三个字母", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "usa") }},
		{"小写", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "us") }},
		{"HTML", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "<html><body>US</body></html>") }},
		{"302", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/moved" {
				io.WriteString(w, "US")
				return
			}
			http.Redirect(w, r, "/moved", http.StatusFound)
		}},
		{"500 带合法正文", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "US", http.StatusInternalServerError) }},
		{"超长", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "US"+string(make([]byte, 100))) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.svc.reply = c.reply
			f.enable(true)
			id := f.report("a", "8.8.8.8")
			f.sweep()
			f.wantRequests("/8.8.8.8/country")
			f.wantCountry(id, "", "")
			f.clk.Advance(time.Hour - time.Second)
			f.sweep()
			f.wantRequests("/8.8.8.8/country")
			f.clk.Advance(time.Second)
			f.sweep()
			f.wantRequests("/8.8.8.8/country", "/8.8.8.8/country")
		})
	}
}

// 地址变了：刷出的同一条语句清空查得的国家（不等查询器），下一轮按新地址重查。同一地址再次刷出、或那次取不到
// 对端（空串）不清空。
func TestAddressChangeClearsAndRequeries(t *testing.T) {
	f := newFixture(t)
	f.enable(true)
	id := f.report("a", "8.8.8.8")
	f.sweep()
	f.wantCountry(id, "US", "8.8.8.8")
	f.report("a", "8.8.8.8")
	f.report("a", "")
	f.wantCountry(id, "US", "8.8.8.8")
	f.report("a", "1.1.1.1")
	f.wantCountry(id, "", "")
	f.svc.reply = func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, " AU \r\n") }
	f.sweep()
	f.wantRequests("/8.8.8.8/country", "/1.1.1.1/country")
	f.wantCountry(id, "AU", "1.1.1.1")
}

// 迟到的应答：查询发出之后、应答到达之前节点换了地址，对旧地址的答案不写；下一轮按新地址查得并写入。
func TestLateAnswerForTheOldAddressIsNotWritten(t *testing.T) {
	f := newFixture(t)
	f.enable(true)
	id := f.report("a", "8.8.8.8")
	f.svc.reply = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/8.8.8.8/country" {
			f.report("a", "1.1.1.1")
			io.WriteString(w, "US")
			return
		}
		io.WriteString(w, "AU")
	}
	f.sweep()
	f.wantRequests("/8.8.8.8/country")
	f.wantCountry(id, "", "")
	f.sweep()
	f.wantRequests("/8.8.8.8/country", "/1.1.1.1/country")
	f.wantCountry(id, "AU", "1.1.1.1")
}

// 查询关闭（从未开启、开启后又关闭）时不出网，假服务一个请求都收不到。开启的那一轮先证明这个节点确实会被查。
func TestDisabledLookupMakesNoRequests(t *testing.T) {
	f := newFixture(t)
	f.report("a", "8.8.8.8")
	f.sweep()
	f.wantRequests()
	f.enable(true)
	f.report("b", "1.1.1.1")
	f.sweep()
	f.wantRequests("/8.8.8.8/country", "/1.1.1.1/country")
	f.enable(false)
	f.report("a", "9.9.9.9")
	f.report("b", "2606:4700::1111")
	f.clk.Advance(2 * time.Hour)
	f.sweep()
	f.wantRequests("/8.8.8.8/country", "/1.1.1.1/country")
}

// 非公网地址不发出查询。同一轮里的公网节点被查，证明这一轮确实跑了、零请求不是因为查询器根本没运行。
func TestNonPublicAddressesMakeNoRequests(t *testing.T) {
	f := newFixture(t)
	f.enable(true)
	var private []int64
	for _, addr := range []string{"10.0.0.1", "172.16.0.1", "192.168.1.1", "100.64.0.1", "127.0.0.1", "::1", "169.254.0.1", "fe80::1", "fd00::1", "203.0.113.7", "2001:db8::1"} {
		private = append(private, f.report(addr, addr))
	}
	f.report("public", "8.8.8.8")
	f.sweep()
	f.wantRequests("/8.8.8.8/country")
	for _, id := range private {
		f.wantCountry(id, "", "")
	}
}

// hub 重启后尚无答案的地址重查一次（退避只在内存），已有答案的不查；重查仍失败则重新退避。
func TestRestartRequeriesUnansweredAddresses(t *testing.T) {
	f := newFixture(t)
	f.enable(true)
	f.svc.reply = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/1.1.1.1/country" {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, "US")
	}
	answered := f.report("a", "8.8.8.8")
	f.report("b", "1.1.1.1")
	f.sweep()
	f.wantRequests("/8.8.8.8/country", "/1.1.1.1/country")
	f.sweep()
	f.wantRequests("/8.8.8.8/country", "/1.1.1.1/country")
	f.restart()
	f.sweep()
	f.wantRequests("/8.8.8.8/country", "/1.1.1.1/country", "/1.1.1.1/country")
	f.sweep()
	f.wantRequests("/8.8.8.8/country", "/1.1.1.1/country", "/1.1.1.1/country")
	f.wantCountry(answered, "US", "8.8.8.8")
}

// 手动指定的国家与查得值互不覆盖：查询只写查得的两列，pin 原样保留；UpdateNode 设或清 pin 不动查得的两列。
func TestLookupAndPinDoNotOverwriteEachOther(t *testing.T) {
	f := newFixture(t)
	f.enable(true)
	id := f.report("a", "8.8.8.8")
	edit := func(pin string) {
		t.Helper()
		if _, err := f.st.UpdateNode(t.Context(), id, store.NodeEdit{Name: "a", TrafficResetDay: 1, CountryPin: pin}); err != nil {
			t.Fatal(err)
		}
	}
	edit("JP")
	f.sweep()
	n, err := f.st.GetNode(t.Context(), id)
	if err != nil || n.CountryPin != "JP" || n.Country != "US" || n.CountryIP != "8.8.8.8" {
		t.Fatalf("after lookup with a pin: %+v %v", n, err)
	}
	edit("")
	f.wantCountry(id, "US", "8.8.8.8")
}
