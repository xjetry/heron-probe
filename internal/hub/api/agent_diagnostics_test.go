package api

import (
	"bytes"
	"fmt"
	"log/slog"
	"slices"
	"testing"
	"testing/fstest"
	"time"

	"connectrpc.com/connect"
	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/gen/heron/v1/heronv1connect"
	"github.com/xjetry/heron-probe/internal/agent/client"
	"github.com/xjetry/heron-probe/internal/agent/collect"
	"github.com/xjetry/heron-probe/internal/hub/traffic"
	"github.com/xjetry/heron-probe/internal/testwait"
	"google.golang.org/protobuf/proto"
)

func TestAgentScopeAndDiagnosticsThroughHTTP(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	id, token := h.createNode(t, "scope")
	h.setPublic(t, id, "scope", true)
	_, readerToken := createToken(t, h, "diagnostics-reader")
	fsys := fstest.MapFS{"proc/sys/kernel/random/boot_id": {Data: []byte("boot")}}
	put := func(name string, n uint64) {
		for direction, value := range map[string]uint64{"rx": n, "tx": n * 2} {
			fsys[fmt.Sprintf("sys/class/net/%s/statistics/%s_bytes", name, direction)] = &fstest.MapFile{Data: fmt.Appendf(nil, "%d", value)}
		}
	}
	col := &collect.Collector{Host: &collect.ProcFS{FS: fsys, DiskUsage: func(string) (uint64, uint64, error) { return 100, 50, nil }}, Clock: h.clk, Version: "private-diagnostic-version", NetInclude: []string{"private-uplink-*"}}
	base := h.clk.Now().Truncate(time.Hour).Unix()
	var latest *heronv1.Facts
	var epoch string
	for i := range 5 {
		switch i {
		case 0:
			put("private-uplink-a", 100)
		case 1:
			put("private-uplink-a", 110)
		case 2:
			put("private-uplink-a", 120)
			put("private-uplink-b", 5000)
		case 3:
			put("private-uplink-a", 130)
			put("private-uplink-b", 5020)
		case 4:
			col.NetInclude = []string{"private-uplink-b"}
		}
		snap := col.Identify()
		m, _ := col.Metrics(snap)
		if (i == 0 || i == 2 || i == 4) && (m.NetRxBps != nil || m.NetTxBps != nil) {
			t.Fatalf("scope transition has a measured rate: %v", m)
		}
		latest = col.Facts(snap)
		latest.Diagnostics.ReportIntervalMs = 10000
		req := connect.NewRequest(&heronv1.ReportRequest{Metrics: m, Facts: latest, FactsHash: client.FactsHash(latest)})
		req.Header().Set("Authorization", "Bearer "+token)
		if _, err := h.agent.Report(t.Context(), req); err != nil {
			t.Fatal(err)
		}
		epoch = m.NetCounterEpoch
		h.clk.Advance(10 * time.Second)
	}
	testwait.Until(t, time.Millisecond, func() bool {
		n, err := h.store.GetNode(t.Context(), id)
		// node_facts 还没有 execution 列：hub 只持久化它有列的 Facts 字段，往返比较除去 execution。
		// execution 入库的提交把这里改回整份比较。
		want := proto.Clone(latest).(*heronv1.Facts)
		want.Execution = nil
		return err == nil && proto.Equal(n.Facts, want)
	}, "latest diagnostics were not persisted")
	h.ingest.Flush(t.Context(), true)
	if err := h.book.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	reloaded := traffic.New(h.store, h.clk, h.book.Zone(), slog.Default())
	if err := reloaded.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, ok := reloaded.Get(id)
	if !ok || state.TotalRx != 40 || state.TotalTx != 80 || state.NetCounterEpoch != epoch {
		t.Fatalf("persisted scope baseline or totals: %+v", state)
	}
	if delta, ok := reloaded.Account(id, &heronv1.Metrics{BootId: "boot", NetCounterEpoch: epoch, NetRxTotal: proto.Uint64(5025), NetTxTotal: proto.Uint64(10050)}); !ok || delta.Rx != 5 || delta.Tx != 10 {
		t.Fatalf("same-scope restart delta = %v %v", delta, ok)
	}
	reader := heronv1connect.NewAdminServiceClient(h.srv.Client(), h.srv.URL)
	req := connect.NewRequest(&heronv1.ListNodesRequest{})
	req.Header().Set("Authorization", "Bearer "+readerToken)
	nodes, err := reader.ListNodes(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	d := nodes.Msg.Nodes[0].GetFacts().GetDiagnostics()
	if !proto.Equal(d, latest.Diagnostics) || !slices.Equal(d.NetInterfaces, []string{"private-uplink-b"}) {
		t.Fatalf("management diagnostics missing: %v", d)
	}
	public := pubGet(t, h, "GetSnapshot", jsonQuery("{}"), nil)
	if !bytes.Contains(public.body, []byte(`"name":"scope"`)) {
		t.Fatalf("public positive control missing: %s", public.body)
	}
	for _, secret := range []string{"private-uplink", "private-diagnostic-version", "diagnostics", "netCounterEpoch", epoch, token} {
		if bytes.Contains(public.body, []byte(secret)) {
			t.Fatalf("private diagnostic field leaked: %q in %s", secret, public.body)
		}
	}
	q := &heronv1.QueryMetricsRequest{NodeId: id, From: base, To: base + 3600, MaxPoints: 2000}
	adminHistory, err := h.admin.QueryMetrics(t.Context(), connect.NewRequest(q))
	if err != nil {
		t.Fatal(err)
	}
	publicHistory, err := h.publicClient().QueryMetrics(t.Context(), connect.NewRequest(q))
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(adminHistory.Msg, publicHistory.Msg) {
		t.Fatal("management/public history disagree")
	}
	var byteSum float64
	var rateN uint32
	var peak float64
	for _, series := range adminHistory.Msg.Series {
		for _, sample := range series.Samples {
			if series.Name == "rx_bytes" {
				byteSum += sample.GetSum()
			}
			if series.Name == "net_rx_bps" {
				rateN += sample.N
				peak = max(peak, sample.GetMax())
			}
		}
	}
	if byteSum != 40 || rateN != 2 || peak != 3 {
		t.Fatalf("scope changes corrupted history: bytes=%v n=%v peak=%v", byteSum, rateN, peak)
	}
}
