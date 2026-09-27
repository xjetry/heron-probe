package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/theme"
	. "github.com/xjetry/probe/internal/hub/theme/themetest"
)

// newThemeHarness 是配了 --theme-origin 的 hub；newHarness 与 serve 的默认一样没有配。
func newThemeHarness(t *testing.T) *harness {
	t.Helper()
	h := newConfiguredHarness(t, "", time.UTC, func(c *Config) { c.ThemeOrigin = true })
	h.login(t)
	return h
}

func (h *harness) upload(t *testing.T, pkg []byte, expect string) (*probev1.Theme, error) {
	t.Helper()
	resp, err := h.admin.UploadTheme(t.Context(), connect.NewRequest(&probev1.UploadThemeRequest{Package: pkg, ExpectId: expect}))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetTheme(), nil
}

func (h *harness) themes(t *testing.T) []*probev1.Theme {
	t.Helper()
	resp, err := h.admin.ListThemes(t.Context(), connect.NewRequest(&probev1.ListThemesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	return resp.Msg.GetThemes()
}

func (h *harness) enabledThemes(t *testing.T) []string {
	t.Helper()
	out := []string{}
	for _, th := range h.themes(t) {
		if th.GetEnabled() {
			out = append(out, th.GetId())
		}
	}
	return out
}

func (h *harness) themeRows(t *testing.T) [2]int64 {
	t.Helper()
	rows := rowCounts(t, h.store)
	return [2]int64{rows["theme"], rows["theme_file"]}
}

// 上传 → 列出 → 启用 → 预览 → 删除 → 回落：删掉启用中的主题后没有启用的主题，库里不留它的任何行。
func TestThemeRoundTrip(t *testing.T) {
	h := newThemeHarness(t)
	ctx := t.Context()
	h.clk.Advance(time.Hour)
	pkg := Zip(t,
		Manifest(t, "night", "Night", "1.2.0", "preview.webp"),
		File("index.html", "<!doctype html>night"),
		Entry{Name: "assets/", Mode: fs.ModeDir | 0o755},
		File("assets/app.js", "fetch('/probe.v1.PublicService/GetSnapshot')"),
		File("assets/empty.css", ""),
		Entry{Name: "preview.webp", Content: []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")},
	)
	got, err := h.upload(t, pkg, "")
	if err != nil {
		t.Fatal(err)
	}
	want := &probev1.Theme{Id: "night", Name: "Night", Version: "1.2.0", UploadedAt: h.clk.Now().Unix(), HasPreview: true}
	if !proto.Equal(got, want) {
		t.Fatalf("UploadTheme = %v, want %v", got, want)
	}
	if list := h.themes(t); len(list) != 1 || !proto.Equal(list[0], want) {
		t.Fatalf("ListThemes = %v, want [%v]", list, want)
	}
	if rows := h.themeRows(t); rows != [2]int64{1, 5} {
		t.Fatalf("theme, theme_file rows = %v, want [1 5] (directories are not stored)", rows)
	}
	if _, err := h.admin.EnableTheme(ctx, connect.NewRequest(&probev1.EnableThemeRequest{Id: "night"})); err != nil {
		t.Fatal(err)
	}
	if got := h.enabledThemes(t); !slices.Equal(got, []string{"night"}) {
		t.Fatalf("enabled = %v, want [night]", got)
	}
	preview, err := h.admin.GetThemePreview(ctx, connect.NewRequest(&probev1.GetThemePreviewRequest{Id: "night"}))
	if err != nil {
		t.Fatal(err)
	}
	if preview.Msg.GetContentType() != "image/webp" || !bytes.HasPrefix(preview.Msg.GetContent(), []byte("RIFF")) {
		t.Fatalf("GetThemePreview = %q %q", preview.Msg.GetContentType(), preview.Msg.GetContent())
	}
	if _, err := h.admin.DeleteTheme(ctx, connect.NewRequest(&probev1.DeleteThemeRequest{Id: "night"})); err != nil {
		t.Fatal(err)
	}
	if list := h.themes(t); len(list) != 0 {
		t.Fatalf("ListThemes after delete = %v", list)
	}
	if rows := h.themeRows(t); rows != [2]int64{0, 0} {
		t.Fatalf("theme, theme_file rows after delete = %v, want none", rows)
	}
	for _, c := range []struct {
		name string
		call func() error
	}{
		{"preview", func() error {
			_, err := h.admin.GetThemePreview(ctx, connect.NewRequest(&probev1.GetThemePreviewRequest{Id: "night"}))
			return err
		}},
		{"enable", func() error {
			_, err := h.admin.EnableTheme(ctx, connect.NewRequest(&probev1.EnableThemeRequest{Id: "night"}))
			return err
		}},
		{"delete", func() error {
			_, err := h.admin.DeleteTheme(ctx, connect.NewRequest(&probev1.DeleteThemeRequest{Id: "night"}))
			return err
		}},
	} {
		if err := c.call(); codeOf(err) != connect.CodeNotFound || !strings.Contains(err.Error(), `"night" is not installed`) {
			t.Errorf("%s of a deleted theme: %v, want NotFound naming it", c.name, err)
		}
	}
	if _, err := h.upload(t, Minimal(t, "plain"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.admin.GetThemePreview(ctx, connect.NewRequest(&probev1.GetThemePreviewRequest{Id: "plain"})); codeOf(err) != connect.CodeNotFound || !strings.Contains(err.Error(), "no preview") {
		t.Fatalf("preview of a theme without one: %v, want NotFound saying it has no preview", err)
	}
}

// 每种被拒的包：InvalidArgument，错误写明位置与原因，库里的主题与文件行数不变（校验先于写入，写入是单事务）。
func TestUploadThemeRejectsBadPackagesWithoutResidue(t *testing.T) {
	h := newThemeHarness(t)
	if _, err := h.upload(t, Minimal(t, "kept", File("kept.js", "1")), ""); err != nil {
		t.Fatal(err)
	}
	before := h.themeRows(t)
	zeros := func(n int) []byte { return make([]byte, n) }
	base := func(extra ...Entry) []byte {
		return Zip(t, append([]Entry{Manifest(t, "bad", "Bad", "1", ""), File("index.html", "x")}, extra...)...)
	}
	many := []Entry{Manifest(t, "bad", "Bad", "1", ""), File("index.html", "x")}
	for i := len(many); i <= theme.MaxEntries; i++ {
		many = append(many, File(fmt.Sprintf("f%04d", i), "x"))
	}
	for _, c := range []struct {
		name, want string
		pkg        []byte
	}{
		{"symlink", `entry "link": Unix mode 0120777 marks a symbolic link`, base(Entry{Name: "link", Content: []byte("../kept/index.html"), Mode: fs.ModeSymlink | 0o777})},
		{"parent", `entry "../x": ".." segment`, base(File("../x", "x"))},
		{"absolute", `entry "/x": absolute path`, base(File("/x", "x"))},
		{"2001 entries", "package: 2001 entries; at most 2000", Zip(t, many...)},
		{"declared total", "package: central directory declares", base(
			Entry{Name: "a", Content: zeros(13 << 20)}, Entry{Name: "b", Content: zeros(13 << 20)}, Entry{Name: "c", Content: zeros(13 << 20)},
			Entry{Name: "d", Content: zeros(13 << 20)}, Entry{Name: "e", Content: zeros(13 << 20)})},
		{"lying central directory", `entry "a": expands past the 1024 bytes the central directory declares`, base(
			Entry{Name: "a", Content: zeros(15 << 20), Declared: 1 << 10}, Entry{Name: "b", Content: zeros(15 << 20), Declared: 1 << 10},
			Entry{Name: "c", Content: zeros(15 << 20), Declared: 1 << 10}, Entry{Name: "d", Content: zeros(15 << 20), Declared: 1 << 10},
			Entry{Name: "e", Content: zeros(15 << 20), Declared: 1 << 10})},
		{"single file", `entry "big": central directory declares 16777217 bytes uncompressed; at most 16777216`, base(Entry{Name: "big", Content: zeros(theme.MaxFileBytes + 1)})},
		{"builtin", `theme.json id: "builtin" is reserved`, Zip(t, Manifest(t, "builtin", "B", "1", ""), File("index.html", "x"))},
		{"no manifest", "package: no theme.json", Zip(t, File("index.html", "x"))},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := h.upload(t, c.pkg, "")
			if codeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("UploadTheme = %v, want InvalidArgument containing %q", err, c.want)
			}
			if rows := h.themeRows(t); rows != before {
				t.Fatalf("theme, theme_file rows = %v, want %v unchanged", rows, before)
			}
		})
	}
}

// expect_id：与包里的 id 不符即拒绝；目标不在即 NotFound；相符则替换，旧包独有的文件消失，启用状态沿用。
func TestUploadThemeExpectIDAndReplacement(t *testing.T) {
	h := newThemeHarness(t)
	ctx := t.Context()
	if _, err := h.upload(t, Minimal(t, "a", File("old.js", "old"), File("more.js", "old")), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.admin.EnableTheme(ctx, connect.NewRequest(&probev1.EnableThemeRequest{Id: "a"})); err != nil {
		t.Fatal(err)
	}
	before := h.themeRows(t)
	if _, err := h.upload(t, Minimal(t, "b"), "a"); codeOf(err) != connect.CodeInvalidArgument ||
		!strings.Contains(err.Error(), `expect_id: "a" does not match the package's theme.json id "b"`) {
		t.Fatalf("mismatched expect_id: %v", err)
	}
	if _, err := h.upload(t, Minimal(t, "c"), "c"); codeOf(err) != connect.CodeNotFound || !strings.Contains(err.Error(), `expect_id: theme "c" is not installed`) {
		t.Fatalf("expect_id of a theme that is not installed: %v", err)
	}
	if rows := h.themeRows(t); rows != before {
		t.Fatalf("rejected uploads changed rows: %v, want %v", rows, before)
	}
	h.clk.Advance(time.Minute)
	got, err := h.upload(t, Zip(t, Manifest(t, "a", "A v2", "2.0.0", "p.png"), File("index.html", "v2"), Entry{Name: "p.png", Content: PNG}), "a")
	if err != nil {
		t.Fatal(err)
	}
	want := &probev1.Theme{Id: "a", Name: "A v2", Version: "2.0.0", UploadedAt: h.clk.Now().Unix(), Enabled: true, HasPreview: true}
	if !proto.Equal(got, want) {
		t.Fatalf("replacement = %v, want %v", got, want)
	}
	if rows := h.themeRows(t); rows != [2]int64{1, 3} {
		t.Fatalf("theme, theme_file rows after replacement = %v, want [1 3]: the old package's files are gone", rows)
	}
	if list := h.themes(t); len(list) != 1 || !proto.Equal(list[0], want) {
		t.Fatalf("ListThemes after replacement = %v", list)
	}
}

// 至多 20 个主题；第 21 个 id 被拒且不留行，已装的 id 仍可替换。
func TestUploadThemeLimit(t *testing.T) {
	h := newThemeHarness(t)
	for i := range theme.MaxThemes {
		if _, err := h.upload(t, Minimal(t, fmt.Sprintf("t%02d", i)), ""); err != nil {
			t.Fatal(err)
		}
	}
	before := h.themeRows(t)
	if _, err := h.upload(t, Minimal(t, "t20"), ""); codeOf(err) != connect.CodeResourceExhausted || !strings.Contains(err.Error(), "at most 20 themes") {
		t.Fatalf("21st theme: %v, want ResourceExhausted", err)
	}
	if rows := h.themeRows(t); rows != before {
		t.Fatalf("rejected 21st theme changed rows: %v, want %v", rows, before)
	}
	if _, err := h.upload(t, Minimal(t, "t07"), "t07"); err != nil {
		t.Fatalf("replacing at the limit: %v", err)
	}
}

// 启用至多一个：启用 B 之后 A 不再启用；空 id 不启用任何主题；不存在的 id 被拒且不改状态。
func TestEnableThemeIsExclusive(t *testing.T) {
	h := newThemeHarness(t)
	ctx := t.Context()
	for _, id := range []string{"a", "b"} {
		if _, err := h.upload(t, Minimal(t, id), ""); err != nil {
			t.Fatal(err)
		}
	}
	enable := func(id string) error {
		_, err := h.admin.EnableTheme(ctx, connect.NewRequest(&probev1.EnableThemeRequest{Id: id}))
		return err
	}
	if err := enable("a"); err != nil {
		t.Fatal(err)
	}
	if err := enable("b"); err != nil {
		t.Fatal(err)
	}
	if got := h.enabledThemes(t); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("enabled after enabling b = %v, want [b]", got)
	}
	if err := enable("zz"); codeOf(err) != connect.CodeNotFound {
		t.Fatalf("enabling a missing theme: %v", err)
	}
	if got := h.enabledThemes(t); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("enabled after a failed enable = %v, want [b]", got)
	}
	if err := enable(""); err != nil {
		t.Fatal(err)
	}
	if got := h.enabledThemes(t); len(got) != 0 {
		t.Fatalf("enabled after enabling none = %v", got)
	}
}

// 没有配 --theme-origin 时每个主题方法都 FailedPrecondition，文案写明差的是一个单独的主机名。方法按描述符枚举（名字
// 含 Theme 的全部方法），以后加的主题方法自动进这张表；枚举数与已知的五个核对，防止枚举本身漏空。
func TestThemeMethodsRequireThemeOrigin(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	cookie := map[string][]string{"Cookie": {sessionCookieHeader(t, h)}}
	svc := adminService()
	var names []string
	for i := 0; i < svc.Methods().Len(); i++ {
		name := string(svc.Methods().Get(i).Name())
		if !strings.Contains(name, "Theme") {
			continue
		}
		names = append(names, name)
		got := rawCall(t, h, name, "{}", cookie)
		if got.status != http.StatusBadRequest || got.code != "failed_precondition" ||
			!strings.Contains(got.message, "--theme-origin") || !strings.Contains(got.message, "separate hostname") {
			t.Errorf("%s without a theme origin: %+v, want failed_precondition naming --theme-origin and a separate hostname", name, got)
		}
	}
	slices.Sort(names)
	if want := []string{"DeleteTheme", "EnableTheme", "GetThemePreview", "ListThemes", "UploadTheme"}; !slices.Equal(names, want) {
		t.Fatalf("theme methods = %v, want %v", names, want)
	}
}

// 解码预算装得下满额的包在 JSON 里的 base64 与最坏转义的 expect_id：请求到得了方法体（包的校验给出 InvalidArgument），
// 而不是在解码时被 ResourceExhausted 截下。多一个字节的包同样解码得到，由包的上限拒绝并写明字节数。
func TestUploadThemeBudgetFitsAFullPackage(t *testing.T) {
	h := newThemeHarness(t)
	cookie := map[string][]string{"Cookie": {sessionCookieHeader(t, h)}}
	for _, c := range []struct {
		size int
		want string
	}{
		{theme.MaxPackageBytes, "package: not a readable zip archive"},
		{theme.MaxPackageBytes + 1, "package: 8388609 bytes; at most 8388608"},
	} {
		body, err := json.Marshal(map[string]string{
			"package":   base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xff}, c.size)),
			"expect_id": strings.Repeat("\x01", 32),
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(body) < 4*((c.size+2)/3)+6*32 {
			t.Fatalf("request is %d bytes; the worst case was not constructed", len(body))
		}
		t.Logf("request %d bytes, budget %d", len(body), maxBody)
		got := rawCall(t, h, "UploadTheme", string(body), cookie)
		if got.code != "invalid_argument" || !strings.Contains(got.message, c.want) {
			t.Fatalf("%d-byte package: %+v, want invalid_argument containing %q", c.size, got, c.want)
		}
	}
}
