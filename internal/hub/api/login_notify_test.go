package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/testwait"
	"google.golang.org/protobuf/proto"
)

func putNotifySettings(t *testing.T, h *harness, body string, status int) map[string]any {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.srv.URL+"/probe.v1.AdminService/UpdateSettings", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != status {
		t.Fatalf("settings HTTP status=%d body=%s, want %d", resp.StatusCode, raw, status)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func chooseLoginChannels(t *testing.T, h *harness, ids ...int64) *probev1.Settings {
	t.Helper()
	return saveSettings(t, h, &probev1.Settings{LoginNotify: &probev1.LoginNotify{ChannelIds: ids}})
}

func TestLoginNotifyFieldNumberAndPresence(t *testing.T) {
	field := (&probev1.Settings{}).ProtoReflect().Descriptor().Fields().ByName("login_notify")
	if field == nil || field.Number() != 12 || !field.HasPresence() || field.Message() == nil {
		t.Fatalf("settings.login_notify must be a message with presence at field 12: %v", field)
	}
}

func loginEvents(t *testing.T, h *harness) []*probev1.AlertEvent {
	t.Helper()
	r, err := h.admin.ListAlertEvents(t.Context(), connect.NewRequest(&probev1.ListAlertEventsRequest{Limit: 100}))
	if err != nil {
		t.Fatal(err)
	}
	return r.Msg.Events
}

func TestLoginNotifySettingsPresenceReferencesAndDeletion(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	a := saveChannel(t, h, webhook("https://example.invalid/a")).Id
	b := saveChannel(t, h, webhook("https://example.invalid/b")).Id
	chooseLoginChannels(t, h, b, a, b)
	appearance := validSettings()
	got := saveSettings(t, h, appearance)
	if !slices.Equal(got.GetLoginNotify().GetChannelIds(), []int64{a, b}) || !slices.Equal(currentSettings(t, h).GetLoginNotify().GetChannelIds(), []int64{a, b}) {
		t.Fatalf("appearance update cleared or failed to canonicalize login channels: %v", got)
	}
	for _, id := range []int64{0, -1, 987654} {
		bad := proto.Clone(appearance).(*probev1.Settings)
		bad.Title = "不得保存"
		bad.LoginNotify = &probev1.LoginNotify{ChannelIds: []int64{a, id}}
		rejected(t, h, bad, fmt.Sprintf("settings.login_notify.channel_ids: channel %d does not exist", id), got)
	}
	if _, err := h.admin.DeleteNotifyChannel(t.Context(), connect.NewRequest(&probev1.DeleteNotifyChannelRequest{Id: a})); err != nil {
		t.Fatal(err)
	}
	if ids := currentSettings(t, h).GetLoginNotify().GetChannelIds(); !slices.Equal(ids, []int64{b}) {
		t.Fatalf("deleted channel remains in login settings: %v", ids)
	}
	chooseLoginChannels(t, h)
	if ids := currentSettings(t, h).GetLoginNotify().GetChannelIds(); len(ids) != 0 {
		t.Fatalf("explicit empty login channels did not disable notifications: %v", ids)
	}
}

// 列表按请求里的原始条数计，重复也算：满上限的重复合法并合并成一个，多一条整次拒绝、什么都不写。
func TestLoginNotifyChannelListCountsRawEntries(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	a := saveChannel(t, h, webhook("https://example.invalid/a")).Id
	full := make([]int64, maxChannelIDs)
	for i := range full {
		full[i] = a
	}
	before := chooseLoginChannels(t, h, full...)
	if ids := before.GetLoginNotify().GetChannelIds(); !slices.Equal(ids, []int64{a}) {
		t.Fatalf("%d copies of one channel saved as %v, want [%d]", len(full), ids, a)
	}
	over := proto.Clone(before).(*probev1.Settings)
	over.Title = "不得保存"
	over.LoginNotify = &probev1.LoginNotify{ChannelIds: append(full, a)}
	rejected(t, h, over, fmt.Sprintf("settings.login_notify.channel_ids must list at most %d channel IDs, duplicates included; got %d", maxChannelIDs, maxChannelIDs+1), before)
}

func TestLoginNotifySuccessDeliversAndUsesTrustedSource(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		t.Run(fmt.Sprint(trusted), func(t *testing.T) {
			prefix := ""
			wantIP := "127.0.0.1"
			if trusted {
				prefix, wantIP = "127.0.0.0/8", "203.0.113.7"
			}
			h := newHarness(t, prefix)
			h.login(t)
			var mu sync.Mutex
			var received []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				mu.Lock()
				received = append(received, string(b))
				mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()
			var ids []int64
			for range 2 {
				c := webhook(srv.URL)
				c.Webhook.BodyTemplate = "{{.Rule}}|{{.Node}}|{{.Kind}}|{{.Transition}}|{{.Summary}}|{{.At.Unix}}"
				ids = append(ids, saveChannel(t, h, c).Id)
			}
			chooseLoginChannels(t, h, ids...)
			req := connect.NewRequest(&probev1.LoginRequest{Password: password})
			req.Header().Set("X-Forwarded-For", "203.0.113.7")
			if _, err := h.admin.Login(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			events := loginEvents(t, h)
			wantSummary := fmt.Sprintf("管理员登录成功：来源 %s（密码），时间 %s", wantIP, h.clk.Now().Format(time.RFC3339))
			if len(events) != 1 {
				t.Fatalf("successful login wrote %d events, want 1", len(events))
			}
			ev := events[0]
			if ev.RuleId != 0 || ev.NodeId != 0 || ev.Transition != "login_success" || ev.At != h.clk.Now().Unix() || ev.Summary != wantSummary || len(ev.Deliveries) != 2 {
				t.Fatalf("login event envelope or deliveries: %v", ev)
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() { defer close(done); h.svc.notifier.Run(ctx) }()
			defer func() { cancel(); <-done }()
			testwait.Until(t, time.Millisecond, func() bool {
				evs := loginEvents(t, h)
				return len(evs) == 1 && len(evs[0].Deliveries) == 2 && evs[0].Deliveries[0].Done && evs[0].Deliveries[1].Done
			}, "login deliveries did not reach terminal state")
			var delivered []int64
			for _, d := range loginEvents(t, h)[0].Deliveries {
				if !d.Ok || d.Attempts != 1 || d.GetDeliveredAt() != h.clk.Now().Unix() {
					t.Errorf("login delivery result: %v", d)
				}
				delivered = append(delivered, d.ChannelId)
			}
			slices.Sort(delivered)
			mu.Lock()
			bodies := slices.Clone(received)
			mu.Unlock()
			body := fmt.Sprintf("系统事件|Hub|login|login_success|%s|%d", wantSummary, h.clk.Now().Unix())
			if !slices.Equal(delivered, ids) || !reflect.DeepEqual(bodies, []string{body, body}) {
				t.Fatalf("webhook delivery mismatch: channels=%v bodies=%q", delivered, bodies)
			}
		})
	}
}

func TestLoginNotifyLockThresholdOnlyOnce(t *testing.T) {
	h := newHarness(t, "127.0.0.0/8")
	h.login(t)
	c := saveChannel(t, h, webhook("https://example.invalid/hook"))
	chooseLoginChannels(t, h, c.Id)
	for i := 1; i <= 8; i++ {
		req := connect.NewRequest(&probev1.LoginRequest{Password: "wrong password"})
		req.Header().Set("X-Forwarded-For", "2001:db8:1::"+fmt.Sprint(i))
		_, err := h.admin.Login(t.Context(), req)
		if codeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("failed login %d: %v", i, err)
		}
		evs := loginEvents(t, h)
		want := 0
		if i >= 5 {
			want = 1
		}
		if len(evs) != want {
			t.Fatalf("attempt %d wrote %d lock events, want %d", i, len(evs), want)
		}
	}
	ev := loginEvents(t, h)[0]
	if ev.Transition != "login_locked" || ev.RuleId != 0 || ev.NodeId != 0 || ev.Summary != "登录失败达到锁定阈值：来源 2001:db8:1::5（密码），时间 "+h.clk.Now().Format(time.RFC3339) || len(ev.Deliveries) != 1 || ev.Deliveries[0].ChannelId != c.Id {
		t.Fatalf("lock notification content: %v", ev)
	}
}

// 两条摘要都写进事件时刻，按 hub 的 --timezone 写成带偏移的 RFC 3339：投递会重试、重启后续投，接收方
// 看到的送达时刻不是登录时刻，而 Telegram 只发摘要。从登录入口一直看到 Telegram 收到的正文。
func TestLoginNotifySummaryCarriesZonedTime(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var texts []string
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		texts = append(texts, body.Text)
		mu.Unlock()
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer tg.Close()
	h := newZonedHarness(t, "127.0.0.0/8", shanghai, withTelegramBase(tg.URL))
	h.login(t)
	c := saveChannel(t, h, &probev1.NotifyChannel{Name: "tg", Kind: probev1.ChannelKind_CHANNEL_KIND_TELEGRAM, Telegram: &probev1.TelegramConfig{BotToken: "1:x", ChatId: "chat"}})
	chooseLoginChannels(t, h, c.Id)
	at := time.Date(2026, 9, 28, 2, 5, 7, 0, time.UTC)
	h.clk.SetWall(at)
	const stamp = "2026-09-28T10:05:07+08:00"
	login := func(pw, from string) error {
		req := connect.NewRequest(&probev1.LoginRequest{Password: pw})
		req.Header().Set("X-Forwarded-For", from)
		_, err := h.admin.Login(t.Context(), req)
		return err
	}
	if err := login(password, "203.0.113.7"); err != nil {
		t.Fatal(err)
	}
	// 5 是 auth 的登录失败上限，第 5 次失败设下锁定。
	for range 5 {
		if err := login("wrong password", "198.51.100.9"); codeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("wrong password: %v", err)
		}
	}
	want := []string{
		"登录失败达到锁定阈值：来源 198.51.100.9（密码），时间 " + stamp,
		"管理员登录成功：来源 203.0.113.7（密码），时间 " + stamp,
	}
	var summaries []string
	for _, ev := range loginEvents(t, h) {
		if ev.At != at.Unix() {
			t.Errorf("event %d at %d, want %d", ev.Id, ev.At, at.Unix())
		}
		summaries = append(summaries, ev.Summary)
	}
	if !slices.Equal(summaries, want) {
		t.Errorf("login summaries = %q, want %q", summaries, want)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); h.svc.notifier.Run(ctx) }()
	defer func() { cancel(); <-done }()
	testwait.Until(t, time.Millisecond, func() bool {
		evs := loginEvents(t, h)
		return len(evs) == 2 && evs[0].Deliveries[0].Done && evs[1].Deliveries[0].Done
	}, "telegram deliveries did not reach terminal state")
	mu.Lock()
	got := slices.Sorted(slices.Values(texts))
	mu.Unlock()
	if !slices.Equal(got, want) {
		t.Fatalf("telegram texts = %q, want %q", got, want)
	}
}

func TestLoginNotifyTokenReadsAndCleanup(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	node, _ := h.createNode(t, "remove me")
	rule := saveRule(t, h, offlineRule())
	c := saveChannel(t, h, webhook("https://example.invalid/hook"))
	_, token := createToken(t, h, "reader")
	chooseLoginChannels(t, h, c.Id)
	req := connect.NewRequest(&probev1.GetSettingsRequest{})
	req.Header().Set("Authorization", "Bearer "+token)
	if _, err := h.admin.GetSettings(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if evs := loginEvents(t, h); len(evs) != 0 {
		t.Fatalf("API token read emitted login events: %v", evs)
	}
	if _, err := h.admin.Login(t.Context(), connect.NewRequest(&probev1.LoginRequest{Password: password})); err != nil {
		t.Fatal(err)
	}
	before := loginEvents(t, h)
	if len(before) != 1 {
		t.Fatalf("cleanup fixture has %d login events, want 1", len(before))
	}
	if _, err := h.admin.DeleteNode(t.Context(), connect.NewRequest(&probev1.DeleteNodeRequest{Id: node})); err != nil {
		t.Fatal(err)
	}
	if after := loginEvents(t, h); !reflect.DeepEqual(after, before) {
		t.Fatalf("DeleteNode/Forget changed system events: %v", after)
	}
	if _, err := h.admin.DeleteAlertRule(t.Context(), connect.NewRequest(&probev1.DeleteAlertRuleRequest{Id: rule.Id})); err != nil {
		t.Fatal(err)
	}
	if after := loginEvents(t, h); !reflect.DeepEqual(after, before) {
		t.Fatalf("DeleteAlertRule changed system events: %v", after)
	}
	states, err := h.store.ListAlertStates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 0 {
		t.Fatalf("login created rule/node state: %v", states)
	}
	filtered, err := h.store.ListAlertEvents(t.Context(), node, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 0 {
		t.Fatalf("node filter included system events: %v", filtered)
	}
	// 不依赖 API 的零 ID 映射，直接回读表确认真实存储也保留系统事件。
	stored, err := h.store.GetAlertEvent(t.Context(), before[0].Id)
	if err != nil || stored.Transition != store.TransitionLoginSuccess {
		t.Fatalf("stored system event missing: %+v, %v", stored, err)
	}
}

func TestLoginNotifyOnlyUpdatePreservesAppearance(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	before := saveSettings(t, h, validSettings())
	putNotifySettings(t, h, `{"settings":{"loginNotify":{}}}`, http.StatusOK)
	after := currentSettings(t, h)
	if after.GetTitle() != before.GetTitle() || after.GetTheme() != before.GetTheme() || after.GetAccentColor() != before.GetAccentColor() || after.GetLogo() != before.GetLogo() || after.GetCustomCss() != before.GetCustomCss() {
		t.Fatalf("login-only update changed appearance: got %v, before %v", after, before)
	}
	_, err := h.admin.Login(t.Context(), connect.NewRequest(&probev1.LoginRequest{Password: password}))
	if err != nil {
		t.Fatal(err)
	}
	events, err := h.store.ListAlertEvents(t.Context(), 0, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("disabled login notification wrote %d events", len(events))
	}
}
