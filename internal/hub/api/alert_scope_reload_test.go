package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/hub/alert"
	"github.com/xjetry/probe/internal/hub/store"
)

func TestDeletedAlertScopeRemainsListedAfterReload(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	id, _ := h.createNode(t, "scoped")
	r := offlineRule()
	r.AllNodes, r.NodeIds = false, []int64{id}
	r = saveRule(t, h, r)
	if _, err := h.admin.DeleteNode(t.Context(), connect.NewRequest(&probev1.DeleteNodeRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	e := alert.New(alert.Config{TTL: 30 * time.Second, Location: time.UTC}, h.store, h.live, h.clk, slog.Default())
	if err := e.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	svc := New(Config{TTL: 30 * time.Second, Location: time.UTC, Retention: store.DefaultRetention, Geo: h.svc.cfg.Geo}, h.store, h.auth, h.live, h.ingest, h.book, h.reg, e, nil, h.clk, slog.Default())
	mux := http.NewServeMux()
	mux.Handle(svc.Handler())
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := probev1connect.NewAdminServiceClient(h.http, srv.URL)
	list, err := client.ListAlertRules(t.Context(), connect.NewRequest(&probev1.ListAlertRulesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Msg.Rules) != 1 || list.Msg.Rules[0].Id != r.Id || list.Msg.Rules[0].AllNodes || len(list.Msg.Rules[0].NodeIds) != 0 || len(list.Msg.States) != 0 {
		t.Fatalf("explicit empty rule missing or widened: %v", list.Msg)
	}
	_, err = client.SaveAlertRule(t.Context(), connect.NewRequest(&probev1.SaveAlertRuleRequest{Rule: list.Msg.Rules[0]}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || err.Error() != "invalid_argument: rule.node_ids must not be empty unless all_nodes is true" {
		t.Fatalf("empty save scope error=%v", err)
	}
}
