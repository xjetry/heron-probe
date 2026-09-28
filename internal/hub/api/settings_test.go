package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
)

func validSettings() *probev1.Settings {
	return &probev1.Settings{Title: "状态", Theme: "dark", AccentColor: "#112233", Logo: "data:image/png;base64,iVBORw0KGgo=", CustomCss: "body { color: red }", PublicEnabled: proto.Bool(true)}
}

// defaultBackup 是从未保存过备份时的回显（§6.7 的默认值）：GetSettings 与 UpdateSettings 的响应总带 backup。
func defaultBackup() *probev1.BackupSettings {
	return &probev1.BackupSettings{Region: "auto", ConfigIntervalS: proto.Uint32(300), MetricsIntervalS: proto.Uint32(86400), ConfigKeep: proto.Uint32(48), MetricsKeep: proto.Uint32(14), Notify: &probev1.BackupNotify{}}
}

func withSettings(change func(*probev1.Settings)) *probev1.Settings {
	s := validSettings()
	change(s)
	return s
}

func saveSettings(t *testing.T, h *harness, in *probev1.Settings) *probev1.Settings {
	t.Helper()
	resp, err := h.admin.UpdateSettings(t.Context(), connect.NewRequest(&probev1.UpdateSettingsRequest{Settings: in}))
	if err != nil {
		t.Fatalf("UpdateSettings(%v): %v", in, err)
	}
	return resp.Msg.GetSettings()
}

func currentSettings(t *testing.T, h *harness) *probev1.Settings {
	t.Helper()
	resp, err := h.admin.GetSettings(t.Context(), connect.NewRequest(&probev1.GetSettingsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	return resp.Msg.GetSettings()
}

// rejected 断言更新被拒、错误含 want，且库里的外观仍是 before：一项不合约束，整次更新什么都不写。
// 先把库复位到 before，每个子用例都从同一状态开始：前一个子用例被错误接受时，红只落在它自己身上。
func rejected(t *testing.T, h *harness, in *probev1.Settings, want string, before *probev1.Settings) {
	t.Helper()
	saveSettings(t, h, before)
	_, err := h.admin.UpdateSettings(t.Context(), connect.NewRequest(&probev1.UpdateSettingsRequest{Settings: in}))
	if codeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want InvalidArgument containing %q", err, want)
	}
	if got := currentSettings(t, h); !proto.Equal(got, before) {
		t.Fatalf("rejected update changed settings to %v", got)
	}
}

func TestUpdateSettingsValidatesTitleThemeAndAccent(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	before := saveSettings(t, h, validSettings())
	for _, c := range []struct {
		name string
		in   *probev1.Settings
		want string
	}{
		{"title", withSettings(func(s *probev1.Settings) { s.Title = strings.Repeat("字", 65) }), "settings.title must be at most 64 characters after removing control characters and surrounding whitespace; got 65"},
		{"title raw bytes", withSettings(func(s *probev1.Settings) { s.Title = strings.Repeat(" ", maxTitleBytes) + "a" }), "settings.title must be at most 1024 bytes before cleaning; got 1025"},
		{"theme empty", withSettings(func(s *probev1.Settings) { s.Theme = "" }), `settings.theme must be one of auto, light, dark; got ""`},
		{"theme case", withSettings(func(s *probev1.Settings) { s.Theme = "Dark" }), `settings.theme must be one of auto, light, dark; got "Dark"`},
		{"settings missing", nil, `settings must provide an appearance group (theme required), public_enabled, geo_enabled, geo_url or backup`},
		{"accent short", withSettings(func(s *probev1.Settings) { s.AccentColor = "#12345" }), `settings.accent_color must be empty (the default color) or #rrggbb with six hex digits; got "#12345"`},
		{"accent long", withSettings(func(s *probev1.Settings) { s.AccentColor = "#1234567" }), `got "#1234567"`},
		{"accent without hash", withSettings(func(s *probev1.Settings) { s.AccentColor = "123456" }), `got "123456"`},
		{"accent not hex", withSettings(func(s *probev1.Settings) { s.AccentColor = "#gggggg" }), `got "#gggggg"`},
	} {
		t.Run(c.name, func(t *testing.T) { rejected(t, h, c.in, c.want, before) })
	}
}

func TestUpdateSettingsCleansTitleAndAccentAndEchoes(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	// 总闸、国家查询两项与 backup 没有提交，回显的是从未保存过时的值；后端回显夹具装配的 HTTP 后端。
	want := &probev1.Settings{Title: "运行状态", Theme: "light", AccentColor: "#abcdef", PublicEnabled: proto.Bool(true), GeoEnabled: proto.Bool(false), GeoUrl: proto.String("https://ipinfo.io/{ip}/country"),
		GeoBackend: probev1.GeoBackend_GEO_BACKEND_HTTP}
	want.Backup = defaultBackup()
	if got := saveSettings(t, h, &probev1.Settings{Title: " ‮\x07运行状态 \t", Theme: "light", AccentColor: "#AbCdEf"}); !proto.Equal(got, want) {
		t.Fatalf("echo = %v, want %v", got, want)
	}
	if got := currentSettings(t, h); !proto.Equal(got, want) {
		t.Fatalf("stored = %v, want %v", got, want)
	}
	// 控制字符不计入 64 个字符；清洗前的字节上限恰好用满也照常保存。
	saveSettings(t, h, &probev1.Settings{Title: strings.Repeat("字", 64) + "\x00\x01", Theme: "auto"})
	if got := saveSettings(t, h, &probev1.Settings{Title: strings.Repeat(" ", maxTitleBytes-1) + "a", Theme: "auto"}); got.GetTitle() != "a" {
		t.Fatalf("title at the raw byte limit: echo %q, want \"a\"", got.GetTitle())
	}
}

// 标题与节点名是同一种显示文字，清洗结果必须一致；任何一边换成自己的写法，这里就会分叉。
func TestTitleAndNodeNameCleanAlike(t *testing.T) {
	for _, raw := range []string{" \x01 状态\u202e 页 \x7f\t", "\u0085a\u200cb", "\u2066x\u2069 "} {
		name, err := cleanName(raw)
		if err != nil {
			t.Fatal(err)
		}
		st, err := cleanSettings(&probev1.Settings{Title: raw, Theme: "auto"})
		if err != nil {
			t.Fatal(err)
		}
		if st.Title != name {
			t.Fatalf("title %q, node name %q from %q", st.Title, name, raw)
		}
	}
}

func TestUpdateSettingsLogoAcceptsOnlyOneShape(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	before := saveSettings(t, h, validSettings())
	shape := "settings.logo must be empty or data:<type>;base64,<data> with <type> one of image/png, image/jpeg, image/webp, image/svg+xml"
	data := "settings.logo: the data after ;base64, must be non-empty standard base64"
	for _, c := range []struct{ name, logo, want string }{
		{"scheme case", "DATA:image/png;base64,aGk=", shape},
		{"type case", "data:IMAGE/PNG;base64,aGk=", shape},
		{"parameter", "data:image/png;charset=utf-8;base64,aGk=", shape},
		{"not base64", "data:image/svg+xml,<svg/>", shape},
		{"not an image", "data:text/html;base64,PGgxPg==", shape},
		{"type prefix", "data:image/pngx;base64,aGk=", shape},
		{"javascript", "javascript:alert(1)", shape},
		{"leading space", " data:image/png;base64,aGk=", shape},
		{"empty data", "data:image/png;base64,", data},
		{"trailing newline", "data:image/png;base64,aGk=\n", data},
		{"inner newline", "data:image/png;base64,aG\nk=", data},
		{"url-safe alphabet", "data:image/png;base64,-_8=", data},
		{"unpadded", "data:image/png;base64,aGk", data},
		{"fragment", "data:image/png;base64,aGk=#x", data},
		{"too large", "data:image/png;base64," + strings.Repeat("A", 131052), "settings.logo must be at most 131072 bytes as a data: URL; got 131074"},
		// 大小检查排在形态检查之前，报哪一条就说明边界落在哪：恰好 131072 字节的值过了大小检查、错在 base64 长度，
		// 多一个字节就是大小错误。
		{"at the limit", "data:image/png;base64," + strings.Repeat("A", 131050), data},
		{"one byte over", "data:image/png;base64," + strings.Repeat("A", 131051), "settings.logo must be at most 131072 bytes as a data: URL; got 131073"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rejected(t, h, withSettings(func(s *probev1.Settings) { s.Logo = c.logo }), c.want, before)
		})
	}
	for _, logo := range []string{
		"data:image/png;base64," + strings.Repeat("A", 131048), // 131070 字节：base64 长度须为 4 的倍数，这是不超过上限的最大值
		"data:image/jpeg;base64,/9j/4A==",
		"data:image/webp;base64,UklGRg==",
		"data:image/svg+xml;base64,PHN2Zy8+",
		longestLogo(),
		"",
	} {
		if got := saveSettings(t, h, withSettings(func(s *probev1.Settings) { s.Logo = logo })); got.GetLogo() != logo {
			t.Fatalf("logo not stored verbatim: %.40q", got.GetLogo())
		}
	}
}

func TestUpdateSettingsCustomCSSRejectsOnlyLiteralEndTagOpen(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	before := saveSettings(t, h, validSettings())
	for _, c := range []struct {
		css string
		at  int
	}{{"</style>", 0}, {"a{}</STYLE>", 3}, {"</ style>", 0}, {"a</b", 1}, {"</", 0}} {
		t.Run(c.css, func(t *testing.T) {
			want := fmt.Sprintf(`settings.custom_css must not contain "</" (it could end the page's <style> element); found at byte %d`, c.at)
			rejected(t, h, withSettings(func(s *probev1.Settings) { s.CustomCss = c.css }), want, before)
		})
	}
	rejected(t, h, withSettings(func(s *probev1.Settings) { s.CustomCss = strings.Repeat("a", 65537) }), "settings.custom_css must be at most 65536 bytes; got 65537", before)
	// 到不了 HTML 标记化器的写法：CSS 转义与 HTML 实体在 <style> 的 RAWTEXT 里都不被解码。
	for _, css := range []string{`a::before { content: "\3c/style>" }`, "/* &lt;/style> */", `a::after { content: "<\/style>" }`, "/* ＜/style> */", "a < /style {}", strings.Repeat("a", 65536)} {
		if got := saveSettings(t, h, withSettings(func(s *probev1.Settings) { s.CustomCss = css })); got.GetCustomCss() != css {
			t.Fatalf("css not stored verbatim: %.40q", got.GetCustomCss())
		}
	}
}

// longestLogo 是不超过 maxLogoBytes 的最长合法 logo：对每个类型，base64 数据取不超过余量的最大 4 的倍数，
// 再在类型之间取总长最大的。前缀最长的类型不一定最长，取整会吃掉差额。
func longestLogo() string {
	var out string
	for _, typ := range logoTypes {
		prefix := "data:" + typ + ";base64,"
		logo := prefix + strings.Repeat("A", (maxLogoBytes-len(prefix))/4*4)
		if len(logo) > len(out) {
			out = logo
		}
	}
	return out
}

// worstCaseSettings 里用例要核对保存结果的几项。服务地址与 endpoint 带固定开头才是合法值：服务地址的 {ip} 在路径里、
// < 在查询串里；endpoint 的 & 在路径里。
const (
	worstGeoPrefix      = "https://geo.example/{ip}?"
	worstEndpointPrefix = "https://s3.example/"
)

var (
	worstGeoURL       = worstGeoPrefix + strings.Repeat("<", maxGeoURLBytes-len(worstGeoPrefix))
	worstEndpoint     = worstEndpointPrefix + strings.Repeat("&", maxEndpointBytes-len(worstEndpointPrefix))
	worstBackupPrefix = strings.Repeat("<", maxPrefixBytes)
	worstBackupSecret = strings.Repeat("\x01", maxSecretBytes)
)

// worstCaseSettings 是满额设置按 encoding/json 默认写法编码的最坏请求体（service.go 的 maxSettingsBody 写了推导），渠道 ID 由
// 调用方给出：它们存不存在决定这次保存能否写入。logo 取 longestLogo；标题、CSS 与备份的 secret 用控制字符填满，
// json.Marshal 把每个控制字符写成 6 字节的 \u00XX，标题的控制字符清洗后不计入 64 个字符，所以这仍是合法的设置；服务
// 地址、endpoint、区域、access key 与前缀不收控制字符，用 < 或 & 填满，json.Marshal 按 HTML 安全规则把它们同样写成
// 6 字节；bucket 取最长；明暗取最长的值，总闸、国家查询开关与 has_secret 取较长的 false，四个数值取各自的上限；本地库路径是回显字段，
// 请求里的值被忽略，这里按份额用 < 填满，模拟把回显整份送回的客户端；字段名用比 camelCase 长的 proto 原名（connect 两种都收）。
func worstCaseSettings(t *testing.T, channelIDs []string) []byte {
	t.Helper()
	logo := longestLogo()
	body, err := json.Marshal(map[string]any{"settings": map[string]any{
		"title": strings.Repeat("\x01", maxTitleBytes), "theme": slices.MaxFunc(themes, func(a, b string) int { return len(a) - len(b) }), "accent_color": "#112233",
		"logo":           logo,
		"custom_css":     strings.Repeat("\x01", maxCSSBytes),
		"public_enabled": false,
		"geo_enabled":    false, "geo_url": worstGeoURL, "geo_backend": "GEO_BACKEND_MMDB", "geo_mmdb_path": strings.Repeat("<", maxMMDBPathBytes),
		"backup": map[string]any{
			"endpoint": worstEndpoint, "bucket": strings.Repeat("b", maxBucketBytes),
			"region": strings.Repeat("<", maxRegionBytes), "access_key": strings.Repeat("<", maxAccessKeyBytes),
			"secret": worstBackupSecret, "prefix": worstBackupPrefix,
			"config_interval_s": 86400, "metrics_interval_s": 604800, "config_keep": 1000, "metrics_keep": 1000,
			"notify": map[string]any{"channel_ids": channelIDs}, "has_secret": false,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// 每一项都按各自的预算项写满，才是这份预算要装下的最坏情况；任一项没有按 6 倍写出，下限就不成立。
	floor := len(logo) + 6*maxCSSBytes + 6*maxTitleBytes + 6*(len(worstGeoURL)-len(worstGeoPrefix)) + 6*maxMMDBPathBytes +
		6*(len(worstEndpoint)-len(worstEndpointPrefix)+maxRegionBytes+maxAccessKeyBytes+maxSecretBytes+maxPrefixBytes)
	for _, id := range channelIDs {
		floor += len(id) + len(`"",`)
	}
	if len(body) < floor {
		t.Fatalf("request is %d bytes, below the %d bytes of its fields at their budgeted worst case", len(body), floor)
	}
	t.Logf("worst-case request: %d bytes, logo %d bytes, budget %d", len(body), len(logo), maxSettingsBody)
	return body
}

// postUpdateSettings 以 JSON 调 UpdateSettings：解码预算按线上的字节计，只有 JSON 请求才测得到它。
func postUpdateSettings(t *testing.T, h *harness, body []byte) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.srv.URL+"/probe.v1.AdminService/UpdateSettings", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// 解码预算不够时，connect 在方法体之前就以 ResourceExhausted 拒绝，校验根本到不了。解码预算装得下满额设置在最坏转义下
// 的 JSON（worstCaseSettings）。渠道取满额个数的已存在渠道，这次保存才能写入；它们的 ID 很短，满额长度的 ID 由
// TestUpdateSettingsBudgetFitsFullBackupWithWorstCaseEscaping 覆盖。connect 丢弃不认识的字段，保存后总闸、国家查询两项与
// 备份各项确实变成请求里的值，才说明它们按字段被解码、这是一份全字段的设置；查询开关从未保存过时就是 false，所以先把
// 它打开。
func TestUpdateSettingsBudgetFitsFullSettingsWithWorstCaseEscaping(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	saveSettings(t, h, &probev1.Settings{Theme: "auto", GeoEnabled: proto.Bool(true)})
	var ids []int64
	var idTexts []string
	for i := range maxBackupChannels {
		id := saveChannel(t, h, webhook(fmt.Sprintf("https://hooks.example/%d", i))).Id
		ids = append(ids, id)
		idTexts = append(idTexts, fmt.Sprint(id))
	}
	if status, b := postUpdateSettings(t, h, worstCaseSettings(t, idTexts)); status != http.StatusOK {
		t.Fatalf("full settings escaped worst case: %d %s", status, b)
	}
	if h.store.PublicEnabled() {
		t.Fatal("public_enabled in the worst-case request was not applied")
	}
	st, err := h.store.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.Geo.Enabled || st.Geo.URL != worstGeoURL {
		t.Fatalf("geo_enabled or geo_url in the worst-case request was not applied: enabled=%v url=%.40q", st.Geo.Enabled, st.Geo.URL)
	}
	if b := st.Backup; b.Target.Endpoint != worstEndpoint || b.Target.Secret != worstBackupSecret || b.Prefix != worstBackupPrefix ||
		b.MetricsIntervalS != 604800 || b.MetricsKeep != 1000 || !slices.Equal(b.Channels, ids) {
		t.Fatalf("backup in the worst-case request was not applied: endpoint=%.40q prefix=%.40q secret %d bytes, metrics %ds/%d, channels %v",
			b.Target.Endpoint, b.Prefix, len(b.Target.Secret), b.MetricsIntervalS, b.MetricsKeep, b.Channels)
	}
}

func TestGetStorageStatsMatchesTheStore(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	h.createNode(t, "n")
	resp, err := h.admin.GetStorageStats(t.Context(), connect.NewRequest(&probev1.GetStorageStatsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	counts := rowCounts(t, h.store)
	var names []string
	for _, tr := range resp.Msg.GetTables() {
		names = append(names, tr.GetName())
		if int64(tr.GetRows()) != counts[tr.GetName()] {
			t.Errorf("%s: %d rows, store says %d", tr.GetName(), tr.GetRows(), counts[tr.GetName()])
		}
	}
	if resp.Msg.GetDbBytes() == 0 || len(names) != len(counts) || !slices.IsSorted(names) || counts["node"] != 1 {
		t.Fatalf("stats = %v, store = %v", resp.Msg, counts)
	}
}
