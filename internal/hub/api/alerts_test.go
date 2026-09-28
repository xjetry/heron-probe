package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/alert"
	"github.com/xjetry/probe/internal/hub/store"
	"google.golang.org/protobuf/proto"
)

func offlineRule() *probev1.AlertRule {
	return &probev1.AlertRule{Name: "离线", Kind: probev1.AlertKind_ALERT_KIND_OFFLINE, Enabled: true, AllNodes: true}
}

func saveRule(t *testing.T, h *harness, r *probev1.AlertRule) *probev1.AlertRule {
	t.Helper()
	resp, err := h.admin.SaveAlertRule(t.Context(), connect.NewRequest(&probev1.SaveAlertRuleRequest{Rule: r}))
	if err != nil {
		t.Fatal(err)
	}
	return resp.Msg.Rule
}

func saveChannel(t *testing.T, h *harness, c *probev1.NotifyChannel) *probev1.NotifyChannel {
	t.Helper()
	resp, err := h.admin.SaveNotifyChannel(t.Context(), connect.NewRequest(&probev1.SaveNotifyChannelRequest{Channel: c}))
	if err != nil {
		t.Fatal(err)
	}
	return resp.Msg.Channel
}

func webhook(url string) *probev1.NotifyChannel {
	return &probev1.NotifyChannel{Name: "通知", Kind: probev1.ChannelKind_CHANNEL_KIND_WEBHOOK, Webhook: &probev1.WebhookConfig{Url: url}}
}

func TestAlertRuleCRUD(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	n1, _ := h.createNode(t, "a")
	n2, _ := h.createNode(t, "b")
	r := offlineRule()
	r.NodeIds = []int64{999}
	created := saveRule(t, h, r)
	want := offlineRule()
	want.Id = 1
	want.CreatedAt = h.clk.Now().Unix()
	if !proto.Equal(created, want) {
		t.Fatalf("created=%v want=%v", created, want)
	}
	list, err := h.admin.ListAlertRules(t.Context(), connect.NewRequest(&probev1.ListAlertRulesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Msg.Rules) != 1 || !proto.Equal(list.Msg.Rules[0], created) || len(list.Msg.States) != 0 {
		t.Fatalf("list=%v", list.Msg)
	}
	c := saveChannel(t, h, webhook("http://127.0.0.1"))
	created.AllNodes = false
	created.NodeIds = []int64{n2, n1, n1}
	created.ChannelIds = []int64{c.Id, c.Id}
	updated := saveRule(t, h, created)
	want.AllNodes = false
	want.NodeIds = []int64{n1, n2}
	want.ChannelIds = []int64{c.Id}
	if !proto.Equal(updated, want) {
		t.Fatalf("updated=%v want=%v", updated, want)
	}
	h.clk.Advance(31 * time.Second)
	if err := h.alerts.SweepOffline(t.Context()); err != nil {
		t.Fatal(err)
	}
	list, err = h.admin.ListAlertRules(t.Context(), connect.NewRequest(&probev1.ListAlertRulesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Msg.Rules) != 1 || !proto.Equal(list.Msg.Rules[0], updated) || len(list.Msg.States) != 2 {
		t.Fatalf("updated list=%v", list.Msg)
	}
	for i, id := range []int64{n1, n2} {
		state := &probev1.AlertStateEntry{RuleId: created.Id, NodeId: id, State: "firing", SinceAt: h.clk.Now().Unix()}
		if !proto.Equal(list.Msg.States[i], state) {
			t.Fatalf("state=%v want=%v", list.Msg.States[i], state)
		}
	}
	if _, err := h.admin.DeleteAlertRule(t.Context(), connect.NewRequest(&probev1.DeleteAlertRuleRequest{Id: created.Id})); err != nil {
		t.Fatal(err)
	}
	list, err = h.admin.ListAlertRules(t.Context(), connect.NewRequest(&probev1.ListAlertRulesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Msg.Rules) != 0 || len(list.Msg.States) != 0 {
		t.Fatalf("deleted list=%v", list.Msg)
	}
}

func TestNotifyChannelRejectsUnknownKind(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	c := webhook("http://127.0.0.1")
	c.Kind = 7
	_, err := h.admin.SaveNotifyChannel(t.Context(), connect.NewRequest(&probev1.SaveNotifyChannelRequest{Channel: c}))
	if codeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), `channel.kind must be`) || !strings.Contains(err.Error(), `got "7"`) {
		t.Fatalf("unknown kind error=%v", err)
	}
}

func TestSaveAlertRuleValidationTexts(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	task, _, err := h.reg.Save(t.Context(), validProbeTask(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := &probev1.AlertRule{Name: "探测", Kind: probev1.AlertKind_ALERT_KIND_PROBE, AllNodes: true, TaskId: task.Task.Id, Metric: probev1.ProbeMetric_PROBE_METRIC_LOSS_PCT, Threshold: 100, ForMinutes: 1}
	for _, tc := range []struct {
		name   string
		change func(*probev1.AlertRule)
		code   connect.Code
		text   string
	}{
		{"name", func(r *probev1.AlertRule) { r.Name = "" }, connect.CodeInvalidArgument, "rule.name must contain"},
		{"kind", func(r *probev1.AlertRule) { r.Kind = 0 }, connect.CodeInvalidArgument, "rule.kind must be"},
		{"unknown_kind", func(r *probev1.AlertRule) { r.Kind = 99 }, connect.CodeInvalidArgument, `got "99"`},
		{"task_id", func(r *probev1.AlertRule) { r.TaskId = 0 }, connect.CodeInvalidArgument, "rule.task_id must not be 0"},
		{"metric", func(r *probev1.AlertRule) { r.Metric = 0 }, connect.CodeInvalidArgument, "rule.metric must be"},
		{"unknown_metric", func(r *probev1.AlertRule) { r.Metric = 99 }, connect.CodeInvalidArgument, `got "99"`},
		{"threshold", func(r *probev1.AlertRule) { r.Threshold = 101 }, connect.CodeInvalidArgument, "rule.threshold must be between 0 and 100"},
		{"minutes0", func(r *probev1.AlertRule) { r.ForMinutes = 0 }, connect.CodeInvalidArgument, "rule.for_minutes must be between 1 and 60"},
		{"minutes61", func(r *probev1.AlertRule) { r.ForMinutes = 61 }, connect.CodeInvalidArgument, "rule.for_minutes must be between 1 and 60"},
		{"empty_scope", func(r *probev1.AlertRule) { r.AllNodes = false }, connect.CodeInvalidArgument, "rule.node_ids must not be empty"},
		{"node", func(r *probev1.AlertRule) { r.AllNodes = false; r.NodeIds = []int64{9} }, connect.CodeNotFound, "rule.node_ids: node 9 does not exist"},
		{"channel", func(r *probev1.AlertRule) { r.ChannelIds = []int64{9} }, connect.CodeNotFound, "rule.channel_ids: notify channel 9 does not exist"},
		{"task", func(r *probev1.AlertRule) { r.TaskId = 9 }, connect.CodeNotFound, "rule.task_id: probe task 9 does not exist"},
		{"id", func(r *probev1.AlertRule) { r.Id = 9 }, connect.CodeNotFound, "rule.id: alert rule 9 does not exist"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := proto.Clone(base).(*probev1.AlertRule)
			tc.change(r)
			_, err := h.admin.SaveAlertRule(t.Context(), connect.NewRequest(&probev1.SaveAlertRuleRequest{Rule: r}))
			if codeOf(err) != tc.code || !strings.Contains(err.Error(), tc.text) {
				t.Fatalf("err=%v want=%s %s", err, tc.code, tc.text)
			}
		})
	}
}

func TestProbeAlertRuleRoundTrip(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	task, _, err := h.reg.Save(t.Context(), validProbeTask(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, metric := range []probev1.ProbeMetric{probev1.ProbeMetric_PROBE_METRIC_LOSS_PCT, probev1.ProbeMetric_PROBE_METRIC_RTT_MS} {
		want := &probev1.AlertRule{Name: "延迟或丢包", Kind: probev1.AlertKind_ALERT_KIND_PROBE, AllNodes: true, Enabled: true, TaskId: task.Task.Id, Metric: metric, Threshold: 12.5, ForMinutes: 3}
		got := saveRule(t, h, want)
		want.Id = got.Id
		want.CreatedAt = h.clk.Now().Unix()
		if got.Id == 0 || !proto.Equal(got, want) {
			t.Fatalf("rule=%v want=%v", got, want)
		}
	}
}

func TestAlertErrorsNameRequestFields(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	for _, tc := range []struct {
		name string
		call func() error
		code connect.Code
		text string
	}{
		{"missing_rule", func() error {
			_, err := h.admin.SaveAlertRule(t.Context(), connect.NewRequest(&probev1.SaveAlertRuleRequest{}))
			return err
		}, connect.CodeInvalidArgument, "rule.kind"},
		{"missing_channel", func() error {
			_, err := h.admin.SaveNotifyChannel(t.Context(), connect.NewRequest(&probev1.SaveNotifyChannelRequest{}))
			return err
		}, connect.CodeInvalidArgument, "channel.kind"},
		{"channel_kind", func() error {
			_, err := h.admin.SaveNotifyChannel(t.Context(), connect.NewRequest(&probev1.SaveNotifyChannelRequest{Channel: &probev1.NotifyChannel{Name: "n"}}))
			return err
		}, connect.CodeInvalidArgument, "channel.kind"},
		{"channel_config", func() error {
			_, err := h.admin.SaveNotifyChannel(t.Context(), connect.NewRequest(&probev1.SaveNotifyChannelRequest{Channel: webhook("ftp://host")}))
			return err
		}, connect.CodeInvalidArgument, "channel.webhook.url"},
		{"channel_id", func() error {
			c := webhook("http://host")
			c.Id = 9
			_, err := h.admin.SaveNotifyChannel(t.Context(), connect.NewRequest(&probev1.SaveNotifyChannelRequest{Channel: c}))
			return err
		}, connect.CodeNotFound, "channel.id: notify channel 9 does not exist"},
		{"delete_rule", func() error {
			_, err := h.admin.DeleteAlertRule(t.Context(), connect.NewRequest(&probev1.DeleteAlertRuleRequest{Id: 9}))
			return err
		}, connect.CodeNotFound, "id: alert rule 9 does not exist"},
		{"delete_channel", func() error {
			_, err := h.admin.DeleteNotifyChannel(t.Context(), connect.NewRequest(&probev1.DeleteNotifyChannelRequest{Id: 9}))
			return err
		}, connect.CodeNotFound, "id: notify channel 9 does not exist"},
		{"test_channel", func() error {
			_, err := h.admin.TestNotifyChannel(t.Context(), connect.NewRequest(&probev1.TestNotifyChannelRequest{Id: 9}))
			return err
		}, connect.CodeNotFound, "id: notify channel 9 does not exist"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if codeOf(err) != tc.code || !strings.Contains(err.Error(), tc.text) {
				t.Fatalf("err=%v want=%s %s", err, tc.code, tc.text)
			}
		})
	}
}

func TestWebhookConfigRoundTrip(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	want := webhook("https://example.test/notify")
	want.Webhook.Method = "PATCH"
	want.Webhook.Headers = map[string]string{"X-Notify": "yes"}
	want.Webhook.BodyTemplate = `{"summary":{{json .Summary}}}`
	got := saveChannel(t, h, want)
	want.Id = got.Id
	want.CreatedAt = h.clk.Now().Unix()
	want.Webhook.Url = ""
	want.Webhook.Headers = nil
	want.Webhook.HasUrl = true
	want.Webhook.UrlHost = "https://example.test"
	want.Webhook.HeaderNames = []string{"X-Notify"}
	want.RatePerMinute = proto.Uint32(0)
	if got.Id == 0 || !proto.Equal(got, want) {
		t.Fatalf("channel=%v want=%v", got, want)
	}
	list, err := h.admin.ListNotifyChannels(t.Context(), connect.NewRequest(&probev1.ListNotifyChannelsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Msg.Channels) != 1 || !proto.Equal(list.Msg.Channels[0], want) {
		t.Fatalf("list=%v", list.Msg)
	}
}

func TestNotifyChannelCRUDHidesToken(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	paths := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { paths <- r.URL.Path; w.WriteHeader(200) }))
	defer srv.Close()
	h.svc.notifier = alert.NewQueue(h.store, h.alerts.Channels, alert.NewHTTPClient(), srv.URL, h.clk, nil, h.svc.log)
	c := saveChannel(t, h, &probev1.NotifyChannel{Name: "tg", Kind: probev1.ChannelKind_CHANNEL_KIND_TELEGRAM, Telegram: &probev1.TelegramConfig{BotToken: "secret", ChatId: "chat"}})
	want := &probev1.NotifyChannel{Id: 1, Name: "tg", Kind: probev1.ChannelKind_CHANNEL_KIND_TELEGRAM, Telegram: &probev1.TelegramConfig{HasBotToken: true, ChatId: "chat"}, CreatedAt: h.clk.Now().Unix(), RatePerMinute: proto.Uint32(20)}
	if !proto.Equal(c, want) {
		t.Fatalf("saved=%v want=%v", c, want)
	}
	list, err := h.admin.ListNotifyChannels(t.Context(), connect.NewRequest(&probev1.ListNotifyChannelsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Msg.Channels) != 1 || !proto.Equal(list.Msg.Channels[0], want) {
		t.Fatalf("list=%v", list.Msg)
	}
	c.Name = "renamed"
	c.Telegram.ChatId = "new chat"
	c = saveChannel(t, h, c)
	want.Name = c.Name
	want.Telegram.ChatId = "new chat"
	if !proto.Equal(c, want) {
		t.Fatalf("updated=%v want=%v", c, want)
	}
	if _, err := h.admin.TestNotifyChannel(t.Context(), connect.NewRequest(&probev1.TestNotifyChannelRequest{Id: c.Id})); err != nil {
		t.Fatal(err)
	}
	if path := <-paths; path != "/botsecret/sendMessage" {
		t.Fatalf("path=%q", path)
	}
	if _, err := h.admin.DeleteNotifyChannel(t.Context(), connect.NewRequest(&probev1.DeleteNotifyChannelRequest{Id: c.Id})); err != nil {
		t.Fatal(err)
	}
	list, err = h.admin.ListNotifyChannels(t.Context(), connect.NewRequest(&probev1.ListNotifyChannelsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Msg.Channels) != 0 {
		t.Fatalf("deleted=%v", list.Msg)
	}
}

// 节奏上限省略时按种类取默认值（Telegram 20、Webhook 0），给出时原样保存（含 0 = 不限），响应与列表里恒有值。
// 省略是"取默认"而不是"保留旧值"：保存是整体替换，只有只写不读的凭据才按省略保留。
func TestNotifyChannelRatePerMinute(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	tg := &probev1.NotifyChannel{Name: "tg", Kind: probev1.ChannelKind_CHANNEL_KIND_TELEGRAM, Telegram: &probev1.TelegramConfig{BotToken: "secret", ChatId: "chat"}}
	saved := saveChannel(t, h, tg)
	if saved.RatePerMinute == nil || saved.GetRatePerMinute() != 20 {
		t.Fatalf("telegram default rate=%d (present %v), want 20", saved.GetRatePerMinute(), saved.RatePerMinute != nil)
	}
	if hook := saveChannel(t, h, webhook("https://example.test/notify")); hook.RatePerMinute == nil || hook.GetRatePerMinute() != 0 {
		t.Fatalf("webhook default rate=%d (present %v), want 0", hook.GetRatePerMinute(), hook.RatePerMinute != nil)
	}
	for _, rate := range []uint32{5, 0} {
		saved.RatePerMinute = proto.Uint32(rate)
		saved = saveChannel(t, h, saved)
		if saved.GetRatePerMinute() != rate {
			t.Fatalf("explicit rate %d saved as %d", rate, saved.GetRatePerMinute())
		}
		list, err := h.admin.ListNotifyChannels(t.Context(), connect.NewRequest(&probev1.ListNotifyChannelsRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		if got := list.Msg.Channels[0]; got.RatePerMinute == nil || got.GetRatePerMinute() != rate {
			t.Fatalf("listed rate=%d (present %v), want %d", got.GetRatePerMinute(), got.RatePerMinute != nil, rate)
		}
	}
	saved.RatePerMinute = nil
	if saved = saveChannel(t, h, saved); saved.GetRatePerMinute() != 20 {
		t.Fatalf("rate after omitting it on update=%d, want the telegram default 20", saved.GetRatePerMinute())
	}
}

func TestDeleteNotifyChannelInUse(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	c := saveChannel(t, h, webhook("http://127.0.0.1"))
	r := offlineRule()
	r.ChannelIds = []int64{c.Id}
	saveRule(t, h, r)
	_, err := h.admin.DeleteNotifyChannel(t.Context(), connect.NewRequest(&probev1.DeleteNotifyChannelRequest{Id: c.Id}))
	want := fmt.Sprintf("failed_precondition: id: notify channel %d is referenced by alert rules: 离线 (id 1)", c.Id)
	if codeOf(err) != connect.CodeFailedPrecondition || err.Error() != want {
		t.Fatalf("err=%v want=%q", err, want)
	}
	list, err := h.admin.ListNotifyChannels(t.Context(), connect.NewRequest(&probev1.ListNotifyChannelsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Msg.Channels) != 1 || !proto.Equal(list.Msg.Channels[0], c) {
		t.Fatalf("rejected deletion changed channels=%v", list.Msg)
	}
}

func TestDeleteProbeTaskInUse(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	task, _, err := h.reg.Save(t.Context(), validProbeTask(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	saveRule(t, h, &probev1.AlertRule{Name: "丢包", Kind: probev1.AlertKind_ALERT_KIND_PROBE, AllNodes: true, TaskId: task.Task.Id, Metric: probev1.ProbeMetric_PROBE_METRIC_LOSS_PCT, Threshold: 100, ForMinutes: 1})
	_, err = h.admin.DeleteProbeTask(t.Context(), connect.NewRequest(&probev1.DeleteProbeTaskRequest{Id: task.Task.Id}))
	want := fmt.Sprintf("failed_precondition: id: probe task %d is referenced by alert rules: 丢包 (id 1)", task.Task.Id)
	if codeOf(err) != connect.CodeFailedPrecondition || err.Error() != want {
		t.Fatalf("err=%v want=%q", err, want)
	}
	list, err := h.admin.ListProbeTasks(t.Context(), connect.NewRequest(&probev1.ListProbeTasksRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Msg.Tasks) != 1 || !proto.Equal(list.Msg.Tasks[0].Task, task.Task) {
		t.Fatalf("rejected deletion changed tasks=%v", list.Msg)
	}
}

func TestTestNotifyChannel(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	for _, status := range []int{200, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status); fmt.Fprint(w, "response") }))
			defer srv.Close()
			c := saveChannel(t, h, webhook(srv.URL))
			if c.Webhook.Method != "POST" {
				t.Fatalf("method=%q", c.Webhook.Method)
			}
			_, err := h.admin.TestNotifyChannel(t.Context(), connect.NewRequest(&probev1.TestNotifyChannelRequest{Id: c.Id}))
			if status == 200 {
				if err != nil {
					t.Fatal(err)
				}
			} else if codeOf(err) != connect.CodeUnavailable || err.Error() != "unavailable: HTTP 500 Internal Server Error: response" {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestListAlertEventsPaging(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	n, _ := h.createNode(t, "n")
	n2, _ := h.createNode(t, "other")
	r := saveRule(t, h, offlineRule())
	c := saveChannel(t, h, webhook("http://127.0.0.1"))
	var events []store.AlertEvent
	for i := 0; i < 501; i++ {
		id := n
		if i == 0 {
			id = n2
		}
		ev, err := h.store.RecordTransition(t.Context(), r.Id, id, store.StateFiring, "", time.Time{}, store.AlertEvent{Transition: store.TransitionFiring, At: h.clk.Now(), Summary: fmt.Sprint(i), Value: float64(i)}, []store.DeliveryTarget{{ChannelID: c.Id}})
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, ev)
	}
	last := events[500]
	if _, err := h.store.BeginBatchAttempt(t.Context(), last.Deliveries[0].BatchID); err != nil {
		t.Fatal(err)
	}
	if err := h.store.UpdateBatch(t.Context(), last.Deliveries[0].BatchID, store.DeliveryResult{OK: true, Done: true, DeliveredAt: h.clk.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.BeginBatchAttempt(t.Context(), events[499].Deliveries[0].BatchID); err != nil {
		t.Fatal(err)
	}
	if err := h.store.UpdateBatch(t.Context(), events[499].Deliveries[0].BatchID, store.DeliveryResult{Done: true, Failure: store.FailureHTTPStatus, HTTPStatus: 400, Error: "bad request body"}); err != nil {
		t.Fatal(err)
	}
	query := func(node, before int64, limit uint32) *probev1.ListAlertEventsResponse {
		t.Helper()
		resp, err := h.admin.ListAlertEvents(t.Context(), connect.NewRequest(&probev1.ListAlertEventsRequest{NodeId: node, BeforeId: before, Limit: limit}))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg
	}
	page := query(0, 0, 2)
	want := &probev1.AlertEvent{Id: last.ID, RuleId: r.Id, NodeId: n, Transition: "firing", At: h.clk.Now().Unix(), Summary: "500", Value: 500, Deliveries: []*probev1.AlertDelivery{{Id: last.Deliveries[0].ID, ChannelId: c.Id, Attempts: 1, Ok: true, Done: true, DeliveredAt: proto.Int64(h.clk.Now().Unix())}}}
	if len(page.Events) != 2 || !proto.Equal(page.Events[0], want) || page.Events[1].Id != events[499].ID || page.Events[1].Deliveries[0].DeliveredAt != nil {
		t.Fatalf("page=%v want first=%v", page, want)
	}
	failed := &probev1.AlertDelivery{Id: events[499].Deliveries[0].ID, ChannelId: c.Id, Attempts: 1, Done: true, Failure: probev1.DeliveryFailure_DELIVERY_FAILURE_HTTP_STATUS, HttpStatus: proto.Uint32(400)}
	if !proto.Equal(page.Events[1].Deliveries[0], failed) {
		t.Fatalf("failed delivery=%v want=%v", page.Events[1].Deliveries[0], failed)
	}
	if got := query(0, page.Events[1].Id, 2); len(got.Events) != 2 || got.Events[0].Id != events[498].ID {
		t.Fatalf("before=%v", got)
	}
	if got := query(0, 0, 0); len(got.Events) != 100 {
		t.Fatalf("default count=%d", len(got.Events))
	}
	if got := query(0, 0, 1000); len(got.Events) != 500 {
		t.Fatalf("capped count=%d", len(got.Events))
	}
	if got := query(n2, 0, 1000); len(got.Events) != 1 || got.Events[0].Id != events[0].ID {
		t.Fatalf("node filter=%v", got)
	}
}

func TestUpdateNodeOfflineGraceFloor(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	id, _ := h.createNode(t, "n")
	for _, grace := range []uint32{29, 30, 0} {
		resp, err := h.admin.UpdateNode(t.Context(), connect.NewRequest(&probev1.UpdateNodeRequest{Id: id, Name: "n", TrafficResetDay: 1, OfflineGraceS: proto.Uint32(grace)}))
		if grace == 29 {
			if codeOf(err) != connect.CodeInvalidArgument || err.Error() != "invalid_argument: offline_grace_s: must be 0 or at least 30 seconds (PROBE_OFFLINE_AFTER); got 29" {
				t.Fatalf("err=%v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		list, err := h.admin.ListNodes(t.Context(), connect.NewRequest(&probev1.ListNodesRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		for _, node := range []*probev1.Node{resp.Msg.Node, list.Msg.Nodes[0]} {
			if node.GetOfflineGraceS() != grace || (node.OfflineGraceS == nil) != (grace == 0) {
				t.Fatalf("grace=%d node=%v", grace, node)
			}
		}
	}
}

func TestDeleteNodeClearsAlertScopeAndStates(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	id, _ := h.createNode(t, "n")
	r := offlineRule()
	r.AllNodes, r.NodeIds = false, []int64{id}
	saveRule(t, h, r)
	h.clk.Advance(31 * time.Second)
	if err := h.alerts.SweepOffline(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, err := h.admin.ListAlertRules(t.Context(), connect.NewRequest(&probev1.ListAlertRulesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Msg.States) != 1 {
		t.Fatalf("before=%v", before.Msg)
	}
	if _, err := h.admin.DeleteNode(t.Context(), connect.NewRequest(&probev1.DeleteNodeRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	after, err := h.admin.ListAlertRules(t.Context(), connect.NewRequest(&probev1.ListAlertRulesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Msg.Rules) != 1 || after.Msg.Rules[0].AllNodes || len(after.Msg.Rules[0].NodeIds) != 0 || len(after.Msg.States) != 0 {
		t.Fatalf("deleted node left alert cache=%v", after.Msg)
	}
}

// alertKinds 是协议种类与存储种类之间的翻译表。协议加了种类而这张表漏配时，编译照过；新种类若还没有协议层的用例，
// 别的用例也不会红，功能却静默失效：
// SaveAlertRule 在 parseEnum 处以 rule.kind 拒绝这个种类，列表回显时 enumFor 找不到它而给出 UNSPECIFIED。这里按协议
// 枚举的全集核对：UNSPECIFIED 之外的每个值都有映射且往返一致，表里没有多出的项；反过来，映射出的每个存储种类都要被
// alert.CheckRule 接受，免得表配上了、校验却不认这个种类。
func TestAlertKindsMapEveryValue(t *testing.T) {
	values := probev1.AlertKind(0).Descriptor().Values()
	for i := 0; i < values.Len(); i++ {
		v := probev1.AlertKind(values.Get(i).Number())
		if v == probev1.AlertKind_ALERT_KIND_UNSPECIFIED {
			continue
		}
		k, ok := alertKinds[v]
		if !ok {
			t.Errorf("%s has no stored kind in alertKinds", v)
			continue
		}
		if enumFor(alertKinds, k) != v {
			t.Errorf("%s does not round-trip through %q", v, k)
		}
	}
	if len(alertKinds) != values.Len()-1 {
		t.Errorf("alertKinds has %d entries, want one per protocol kind except UNSPECIFIED (%d)", len(alertKinds), values.Len()-1)
	}
	// 每个存储种类一条最小的合法规则：离线不带专用字段，探测带任务、指标、阈值与持续分钟，到期带提前天数。
	minimal := map[store.AlertKind]store.AlertRule{
		store.KindOffline: {Name: "离线", Kind: store.KindOffline, AllNodes: true},
		store.KindProbe:   {Name: "探测", Kind: store.KindProbe, AllNodes: true, TaskID: 1, Metric: store.MetricLossPct, Threshold: 10, ForMinutes: 3},
		store.KindExpiry:  {Name: "到期", Kind: store.KindExpiry, AllNodes: true, DaysBefore: 7},
	}
	for v, k := range alertKinds {
		r, ok := minimal[k]
		if !ok {
			t.Errorf("no minimal valid rule for stored kind %q (%s); add one so CheckRule is checked for it", k, v)
			continue
		}
		if err := alert.CheckRule(r); err != nil {
			t.Errorf("alert.CheckRule rejects the stored kind %q of %s: %v", k, v, err)
		}
	}
}
