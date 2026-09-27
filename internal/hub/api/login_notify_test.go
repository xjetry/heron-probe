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
			wantSummary := fmt.Sprintf("管理员登录成功：来源 %s（密码）", wantIP)
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
	if ev.Transition != "login_locked" || ev.RuleId != 0 || ev.NodeId != 0 || ev.Summary != "登录失败达到锁定阈值：来源 2001:db8:1::5（密码）" || len(ev.Deliveries) != 1 || ev.Deliveries[0].ChannelId != c.Id {
		t.Fatalf("lock notification content: %v", ev)
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
