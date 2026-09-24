package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"

	"connectrpc.com/connect"
	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/alert"
	"github.com/xjetry/probe/internal/hub/store"
	"google.golang.org/protobuf/proto"
)

var alertKinds = map[probev1.AlertKind]store.AlertKind{
	probev1.AlertKind_ALERT_KIND_OFFLINE: store.KindOffline,
	probev1.AlertKind_ALERT_KIND_PROBE:   store.KindProbe,
}
var probeMetrics = map[probev1.ProbeMetric]store.ProbeMetric{
	probev1.ProbeMetric_PROBE_METRIC_LOSS_PCT: store.MetricLossPct,
	probev1.ProbeMetric_PROBE_METRIC_RTT_MS:   store.MetricRttMs,
}
var channelKinds = map[probev1.ChannelKind]store.ChannelKind{
	probev1.ChannelKind_CHANNEL_KIND_TELEGRAM: store.ChannelTelegram,
	probev1.ChannelKind_CHANNEL_KIND_WEBHOOK:  store.ChannelWebhook,
}

func enumFor[K comparable, V comparable](values map[K]V, value V) K {
	for key, v := range values {
		if v == value {
			return key
		}
	}
	var zero K
	return zero
}

func ruleProto(r store.AlertRule) *probev1.AlertRule {
	return &probev1.AlertRule{Id: r.ID, Name: r.Name, Kind: enumFor(alertKinds, r.Kind), Enabled: r.Enabled, AllNodes: r.AllNodes, NodeIds: r.NodeIDs, ChannelIds: r.ChannelIDs, TaskId: r.TaskID, Metric: enumFor(probeMetrics, r.Metric), Threshold: r.Threshold, ForMinutes: uint32(r.ForMinutes), CreatedAt: r.CreatedAt.Unix()}
}

func (s *Service) ListAlertRules(_ context.Context, _ *connect.Request[probev1.ListAlertRulesRequest]) (*connect.Response[probev1.ListAlertRulesResponse], error) {
	out := &probev1.ListAlertRulesResponse{}
	for _, r := range s.alerts.Rules() {
		out.Rules = append(out.Rules, ruleProto(r))
	}
	for _, state := range s.alerts.States() {
		out.States = append(out.States, &probev1.AlertStateEntry{RuleId: state.RuleID, NodeId: state.NodeID, State: string(state.State), SinceAt: state.SinceAt.Unix()})
	}
	return connect.NewResponse(out), nil
}

func (s *Service) SaveAlertRule(ctx context.Context, req *connect.Request[probev1.SaveAlertRuleRequest]) (*connect.Response[probev1.SaveAlertRuleResponse], error) {
	r := req.Msg.GetRule()
	if err := checkTaskID(r.GetTaskId(), "rule.task_id"); err != nil {
		return nil, err
	}
	saved, err := s.alerts.SaveRule(ctx, store.AlertRule{ID: r.GetId(), Name: r.GetName(), Kind: alertKinds[r.GetKind()], Enabled: r.GetEnabled(), AllNodes: r.GetAllNodes(), NodeIDs: r.GetNodeIds(), ChannelIDs: r.GetChannelIds(), TaskID: r.GetTaskId(), Metric: probeMetrics[r.GetMetric()], Threshold: r.GetThreshold(), ForMinutes: int(r.GetForMinutes())})
	if err != nil {
		return nil, s.operationError(err, "rule", "saving alert rule failed")
	}
	return connect.NewResponse(&probev1.SaveAlertRuleResponse{Rule: ruleProto(saved)}), nil
}

func (s *Service) DeleteAlertRule(ctx context.Context, req *connect.Request[probev1.DeleteAlertRuleRequest]) (*connect.Response[probev1.DeleteAlertRuleResponse], error) {
	if err := s.alerts.DeleteRule(ctx, req.Msg.GetId()); err != nil {
		return nil, s.operationError(err, "id", "deleting alert rule failed")
	}
	return connect.NewResponse(&probev1.DeleteAlertRuleResponse{}), nil
}

func channelProto(c store.NotifyChannel) (*probev1.NotifyChannel, error) {
	out := &probev1.NotifyChannel{Id: c.ID, Name: c.Name, Kind: enumFor(channelKinds, c.Kind), CreatedAt: c.CreatedAt.Unix()}
	switch c.Kind {
	case store.ChannelTelegram:
		var cfg alert.TelegramConfig
		if err := json.Unmarshal([]byte(c.Config), &cfg); err != nil {
			return nil, err
		}
		// 明文只供 Engine 合并与 Queue 投递；所有 API 回显共用此出口以免泄漏。
		out.Telegram = &probev1.TelegramConfig{HasBotToken: cfg.BotToken != "", ChatId: cfg.ChatID}
	case store.ChannelWebhook:
		var cfg alert.WebhookConfig
		if err := json.Unmarshal([]byte(c.Config), &cfg); err != nil {
			return nil, err
		}
		u, err := url.Parse(cfg.URL)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(cfg.Headers))
		for k := range cfg.Headers {
			names = append(names, k)
		}
		sort.Strings(names)
		out.Webhook = &probev1.WebhookConfig{HasUrl: cfg.URL != "", UrlHost: u.Scheme + "://" + u.Host, HeaderNames: names, Method: cfg.Method, BodyTemplate: cfg.BodyTemplate}
	}
	return out, nil
}

func (s *Service) ListNotifyChannels(_ context.Context, _ *connect.Request[probev1.ListNotifyChannelsRequest]) (*connect.Response[probev1.ListNotifyChannelsResponse], error) {
	out := &probev1.ListNotifyChannelsResponse{}
	for _, c := range s.alerts.Channels() {
		p, err := channelProto(c)
		if err != nil {
			return nil, s.operationError(err, "channel", "reading notify channel failed")
		}
		out.Channels = append(out.Channels, p)
	}
	return connect.NewResponse(out), nil
}

func (s *Service) SaveNotifyChannel(ctx context.Context, req *connect.Request[probev1.SaveNotifyChannelRequest]) (*connect.Response[probev1.SaveNotifyChannelResponse], error) {
	c := req.Msg.GetChannel()
	var config any
	switch c.GetKind() {
	case probev1.ChannelKind_CHANNEL_KIND_TELEGRAM:
		t := c.GetTelegram()
		config = alert.TelegramConfig{BotToken: t.GetBotToken(), ChatID: t.GetChatId()}
	case probev1.ChannelKind_CHANNEL_KIND_WEBHOOK:
		w := c.GetWebhook()
		config = alert.WebhookConfig{URL: w.GetUrl(), Method: w.GetMethod(), Headers: w.GetHeaders(), BodyTemplate: w.GetBodyTemplate(), RemoveHeaders: w.GetRemoveHeaders()}
	}
	b, err := json.Marshal(config)
	if err != nil {
		return nil, s.operationError(err, "channel", "encoding notify channel failed")
	}
	saved, err := s.alerts.SaveChannel(ctx, store.NotifyChannel{ID: c.GetId(), Name: c.GetName(), Kind: channelKinds[c.GetKind()], Config: string(b)})
	if err != nil {
		return nil, s.operationError(err, "channel", "saving notify channel failed")
	}
	out, err := channelProto(saved)
	if err != nil {
		return nil, s.operationError(err, "channel", "reading saved notify channel failed")
	}
	return connect.NewResponse(&probev1.SaveNotifyChannelResponse{Channel: out}), nil
}

func (s *Service) DeleteNotifyChannel(ctx context.Context, req *connect.Request[probev1.DeleteNotifyChannelRequest]) (*connect.Response[probev1.DeleteNotifyChannelResponse], error) {
	if err := s.alerts.DeleteChannel(ctx, req.Msg.GetId()); err != nil {
		return nil, s.operationError(err, "id", "deleting notify channel failed")
	}
	return connect.NewResponse(&probev1.DeleteNotifyChannelResponse{}), nil
}

func (s *Service) TestNotifyChannel(ctx context.Context, req *connect.Request[probev1.TestNotifyChannelRequest]) (*connect.Response[probev1.TestNotifyChannelResponse], error) {
	for _, c := range s.alerts.Channels() {
		if c.ID == req.Msg.GetId() {
			if err := s.notifier.SendTest(ctx, c); err != nil {
				code := connect.CodeUnavailable
				var retry alert.Retryable
				if errors.As(err, &retry) && !retry.Retryable() {
					code = connect.CodeFailedPrecondition
				}
				return nil, connect.NewError(code, err)
			}
			return connect.NewResponse(&probev1.TestNotifyChannelResponse{}), nil
		}
	}
	return nil, s.operationError(store.NotFoundError{Kind: "notify channel", ID: req.Msg.GetId()}, "id", "reading notify channel failed")
}

func (s *Service) ListAlertEvents(ctx context.Context, req *connect.Request[probev1.ListAlertEventsRequest]) (*connect.Response[probev1.ListAlertEventsResponse], error) {
	limit := req.Msg.GetLimit()
	if limit == 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	events, err := s.store.ListAlertEvents(ctx, req.Msg.GetNodeId(), req.Msg.GetBeforeId(), int(limit))
	if err != nil {
		return nil, s.operationError(err, "", "listing alert events failed")
	}
	out := &probev1.ListAlertEventsResponse{}
	for _, ev := range events {
		p := &probev1.AlertEvent{Id: ev.ID, RuleId: ev.RuleID, NodeId: ev.NodeID, Transition: string(ev.Transition), At: ev.At.Unix(), Summary: ev.Summary, Value: ev.Value}
		for _, d := range ev.Deliveries {
			v := &probev1.AlertDelivery{ChannelId: d.ChannelID, Attempts: uint32(d.Attempts), Ok: d.OK, Done: d.Done, LastError: d.LastError}
			if !d.DeliveredAt.IsZero() {
				v.DeliveredAt = proto.Int64(d.DeliveredAt.Unix())
			}
			p.Deliveries = append(p.Deliveries, v)
		}
		out.Events = append(out.Events, p)
	}
	return connect.NewResponse(out), nil
}
