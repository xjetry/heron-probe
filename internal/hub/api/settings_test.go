package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
)

func validSettings() *probev1.Settings {
	return &probev1.Settings{Title: "状态", Theme: "dark", AccentColor: "#112233", Logo: "data:image/png;base64,iVBORw0KGgo=", CustomCss: "body { color: red }"}
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
		{"settings missing", nil, `settings.theme must be one of auto, light, dark; got ""`},
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
	want := &probev1.Settings{Title: "运行状态", Theme: "light", AccentColor: "#abcdef"}
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

// 解码预算不够时，connect 在方法体之前就以 ResourceExhausted 拒绝，校验根本到不了。
// 解码预算装得下满额设置在最坏转义下的 JSON（service.go 的 maxBody 写了推导）：logo 取 longestLogo；
// 标题与 CSS 用控制字符填满，json.Marshal 把每个控制字符写成 6 字节的 \u00XX，标题的控制字符清洗后不计入
// 64 个字符，所以这仍是合法的外观；明暗取最长的值，字段名用比 camelCase 长的 proto 原名（connect 两种都收）。
// 登录通知的渠道列表带满 maxChannelIDs 条、每条都是最长的 int64：这些渠道不存在，请求在方法体里被引用检查
// 拒绝，而引用检查在外观校验之后，所以得到点名渠道的 InvalidArgument 就说明整个请求解码成功、外观也通过了校验。
// 最坏请求按条数上限构造，前提是多一条就不合法，用例一并钉住。
func TestUpdateSettingsBudgetFitsFullSettingsWithWorstCaseEscaping(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	existing := saveChannel(t, h, webhook("https://example.invalid/a")).Id
	elem := strconv.Quote(strconv.FormatInt(math.MinInt64, 10))
	if one, err := protojson.Marshal(&probev1.LoginNotify{ChannelIds: []int64{math.MinInt64, math.MaxInt64}}); err != nil || !bytes.Contains(one, []byte(elem)) || len(elem) != maxInt64JSONBytes {
		t.Fatalf("protojson writes int64 as %s (%v); maxInt64JSONBytes = %d, longest quoted int64 is %d bytes", one, err, maxInt64JSONBytes, len(elem))
	}
	post := func(ids []string) (int, []byte, int) {
		t.Helper()
		body, err := json.Marshal(map[string]any{"settings": map[string]any{
			"title": strings.Repeat("\x01", maxTitleBytes), "theme": slices.MaxFunc(themes, func(a, b string) int { return len(a) - len(b) }),
			"accent_color": "#112233", "logo": longestLogo(), "custom_css": strings.Repeat("\x01", maxCSSBytes),
			"login_notify": map[string]any{"channel_ids": ids},
		}})
		if err != nil {
			t.Fatal(err)
		}
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
		return resp.StatusCode, b, len(body)
	}
	worst := make([]string, maxChannelIDs)
	for i := range worst {
		worst[i] = strconv.FormatInt(math.MinInt64+int64(i), 10)
	}
	status, b, n := post(worst)
	if n < len(longestLogo())+6*maxCSSBytes+6*maxTitleBytes+maxChannelIDs*maxInt64JSONBytes {
		t.Fatalf("request is %d bytes; the worst case was not constructed", n)
	}
	t.Logf("worst-case request: %d bytes, budget %d", n, maxBody)
	if want := `settings.login_notify.channel_ids: channel -9223372036854775808 does not exist`; status != http.StatusBadRequest || !strings.Contains(string(b), want) {
		t.Fatalf("full settings escaped worst case (%d bytes): %d %s, want 400 containing %q", n, status, b, want)
	}
	over := make([]string, maxChannelIDs+1)
	for i := range over {
		over[i] = strconv.FormatInt(existing, 10)
	}
	status, b, _ = post(over)
	if want := fmt.Sprintf("settings.login_notify.channel_ids must list at most %d channel IDs, duplicates included; got %d", maxChannelIDs, maxChannelIDs+1); status != http.StatusBadRequest || !strings.Contains(string(b), want) {
		t.Fatalf("%d copies of an existing channel: %d %s, want 400 containing %q; the worst case above is not the worst legal request", len(over), status, b, want)
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
