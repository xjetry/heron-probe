package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/alert"
	"github.com/xjetry/probe/internal/hub/metric"
	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/testwait"
)

func startAlertHub(t *testing.T, clk clock.Clock, seed func(*store.Store), flags ...string) (probev1connect.AdminServiceClient, serveEvents, func()) {
	t.Helper()
	db := filepath.Join(t.TempDir(), "hub.db")
	password := "alert delivery sufficiently long password"
	if err := runPasswdWith([]string{"--db", db}, pipeWith(t, password+"\n"), io.Discard); err != nil {
		t.Fatal(err)
	}
	if seed != nil {
		st, err := store.Open(db, clk, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		seed(st)
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
	url, events, stop := startTestHubWithTTL(t, db, clk, minTTL.String(), flags...)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := probev1connect.NewAdminServiceClient(&http.Client{Jar: jar, Timeout: testwait.Bound}, url)
	if _, err := client.Login(t.Context(), connect.NewRequest(&probev1.LoginRequest{Password: password})); err != nil {
		t.Fatal(err)
	}
	return client, events, stop
}

func alertReceiver(t *testing.T) (string, <-chan string) {
	t.Helper()
	bodies := make(chan string, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		bodies <- string(body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, bodies
}

func awaitDelivered(t *testing.T, client probev1connect.AdminServiceClient, bodies <-chan string, timeout time.Duration) {
	t.Helper()
	started := time.Now()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		events, err := client.ListAlertEvents(t.Context(), connect.NewRequest(&probev1.ListAlertEventsRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		if len(events.Msg.Events) == 1 {
			ev := events.Msg.Events[0]
			if ev.Transition == "firing" && len(ev.Deliveries) == 1 && ev.Deliveries[0].Ok {
				select {
				case body := <-bodies:
					var payload struct {
						Transition string `json:"transition"`
					}
					if err := json.Unmarshal([]byte(body), &payload); err != nil || payload.Transition != "firing" {
						t.Fatalf("webhook body=%s err=%v", body, err)
					}
				default:
					t.Fatal("delivery marked ok without webhook body")
				}
				t.Logf("firing delivered after %s", time.Since(started))
				return
			}
		}
		select {
		case <-deadline.C:
			t.Fatalf("firing was not delivered within %s: %v", timeout, events.Msg)
		case <-ticker.C:
		}
	}
}

func TestServeDeliversOfflineAlerts(t *testing.T) {
	url, bodies := alertReceiver(t)
	// 巡检 ticker 使用真实时间；真实 Mono 同步推进，才能从未上报走到 TTL。
	client, _, _ := startAlertHub(t, clock.Real(), nil)
	if _, err := client.CreateNode(t.Context(), connect.NewRequest(&probev1.CreateNodeRequest{Name: "unseen"})); err != nil {
		t.Fatal(err)
	}
	c, err := client.SaveNotifyChannel(t.Context(), connect.NewRequest(&probev1.SaveNotifyChannelRequest{Channel: &probev1.NotifyChannel{Name: "hook", Kind: probev1.ChannelKind_CHANNEL_KIND_WEBHOOK, Webhook: &probev1.WebhookConfig{Url: url}}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.SaveAlertRule(t.Context(), connect.NewRequest(&probev1.SaveAlertRuleRequest{Rule: &probev1.AlertRule{Name: "offline", Kind: probev1.AlertKind_ALERT_KIND_OFFLINE, Enabled: true, AllNodes: true, ChannelIds: []int64{c.Msg.Channel.Id}}}))
	if err != nil {
		t.Fatal(err)
	}
	// 节点要先过 PROBE_OFFLINE_AFTER（minTTL），再赶上 OfflineSweepEvery 的巡检才会投递。
	// 上界只覆盖这两段之后的挂死，不把“多久内必须送达”当成被测性质。
	awaitDelivered(t, client, bodies, minTTL+alert.OfflineSweepEvery+testwait.Bound)
}

func seedAlertChannel(t *testing.T, st *store.Store, url string) (int64, int64) {
	t.Helper()
	id, err := st.CreateNode(t.Context(), "n", []byte("token hash"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]string{"url": url, "method": "POST"})
	c, err := st.SaveNotifyChannel(t.Context(), store.NotifyChannel{Name: "hook", Kind: store.ChannelWebhook, Config: string(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	return id, c.ID
}

func TestServeRequeuesPendingNotifications(t *testing.T) {
	url, bodies := alertReceiver(t)
	client, _, _ := startAlertHub(t, clock.Real(), func(st *store.Store) {
		id, channel := seedAlertChannel(t, st, url)
		r, err := st.SaveAlertRule(t.Context(), store.AlertRule{Name: "offline", Kind: store.KindOffline, AllNodes: true})
		if err != nil {
			t.Fatal(err)
		}
		_, err = st.RecordTransition(t.Context(), r.ID, id, store.StateFiring, store.AlertEvent{Transition: store.TransitionFiring, At: time.Now()}, []int64{channel})
		if err != nil {
			t.Fatal(err)
		}
	})
	awaitDelivered(t, client, bodies, testwait.Bound)
}

func TestServeEvaluatesProbeAlerts(t *testing.T) {
	url, bodies := alertReceiver(t)
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 59, 999000000, time.UTC))
	client, _, _ := startAlertHub(t, clk, func(st *store.Store) {
		id, channel := seedAlertChannel(t, st, url)
		task, _, err := st.SaveProbeTask(t.Context(), &probev1.ProbeTask{Kind: probev1.ProbeKind_PROBE_KIND_ICMP, Target: "127.0.0.1", IntervalS: 5, TimeoutMs: 1000}, []int64{id})
		if err != nil {
			t.Fatal(err)
		}
		_, err = st.SaveAlertRule(t.Context(), store.AlertRule{Name: "loss", Kind: store.KindProbe, Enabled: true, AllNodes: true, TaskID: task.Id, Metric: store.MetricLossPct, Threshold: 100, ForMinutes: 1, ChannelIDs: []int64{channel}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = st.WriteMinuteBatch(t.Context(), metric.Batch{Probes: []metric.ProbeRow{{NodeID: id, TS: clk.Now().Truncate(time.Minute).Add(-time.Minute).Unix(), TaskID: task.Id, Bucket: &metric.ProbeBucket{Sent: 1, Lost: 1}}}})
		if err != nil {
			t.Fatal(err)
		}
	})
	// 墙钟固定，评估分钟由它决定；真实计时器只提供 3.001s 等待。
	// 首次读钟与测试线程拨钟没有同步点，拨钟可能让协程等下一分钟，所以不推进墙钟。
	awaitDelivered(t, client, bodies, testwait.Bound)
}

func TestServePrunesAlertEvents(t *testing.T) {
	for _, tc := range []struct {
		name      string
		retention time.Duration
		flags     []string
	}{
		{"default", 90 * 24 * time.Hour, nil},
		{"configured", 3 * 24 * time.Hour, []string{"--retention-alert-events", "72h"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 59, 999000000, time.UTC))
			client, events, _ := startAlertHub(t, clk, func(st *store.Store) {
				id, channel := seedAlertChannel(t, st, "http://127.0.0.1:1")
				r, err := st.SaveAlertRule(t.Context(), store.AlertRule{Name: "offline", Kind: store.KindOffline, AllNodes: true})
				if err != nil {
					t.Fatal(err)
				}
				for _, age := range []time.Duration{tc.retention + 24*time.Hour, tc.retention - 24*time.Hour} {
					ev, err := st.RecordTransition(t.Context(), r.ID, id, store.StateFiring, store.AlertEvent{Transition: store.TransitionFiring, At: clk.Now().Add(-age)}, []int64{channel})
					if err != nil {
						t.Fatal(err)
					}
					if _, err := st.BeginDeliveryAttempt(t.Context(), ev.Deliveries[0].ID); err != nil {
						t.Fatal(err)
					}
					if err := st.UpdateDelivery(t.Context(), ev.Deliveries[0].ID, store.DeliveryResult{OK: true, Done: true, DeliveredAt: clk.Now()}); err != nil {
						t.Fatal(err)
					}
				}
			}, tc.flags...)
			deadline := time.NewTimer(testwait.Bound)
			defer deadline.Stop()
			for {
				select {
				case event := <-events:
					if string(event["msg"]) != `"pruned expired alert events"` {
						continue
					}
					got, err := client.ListAlertEvents(t.Context(), connect.NewRequest(&probev1.ListAlertEventsRequest{}))
					if err != nil || len(got.Msg.Events) != 1 || got.Msg.Events[0].Id != 2 || len(got.Msg.Events[0].Deliveries) != 1 {
						t.Fatalf("retained events=%v err=%v", got, err)
					}
					return
				case <-deadline.C:
					t.Fatal("maintenance did not prune alert events")
				}
			}
		})
	}
}
