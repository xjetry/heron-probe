package api

import (
	"bytes"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/metric"
)

// pubResult 是一次原样 HTTP 调用的结果：公开服务的断言常要比较整段响应字节与响应头。
type pubResult struct {
	status int
	header http.Header
	body   []byte
}

// rawClient 不带 cookie，也不替调用方协商压缩：默认 Transport 会自动加 Accept-Encoding: gzip 并解压、
// 删掉 Content-Encoding，测试就看不到服务端实际发了什么。
var rawClient = &http.Client{Transport: &http.Transport{DisableCompression: true}}

func jsonQuery(msg string) string { return "connect=v1&encoding=json&message=" + url.QueryEscape(msg) }

func pubDo(t *testing.T, req *http.Request, header map[string]string) pubResult {
	t.Helper()
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := rawClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return pubResult{status: resp.StatusCode, header: resp.Header, body: body}
}

// pubGet 用 curl 同款的 Connect GET 形态调公开服务，不带任何凭据；query 是已编码的查询串。
func pubGet(t *testing.T, h *harness, method, query string, header map[string]string) pubResult {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.srv.URL+"/probe.v1.PublicService/"+method+"?"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	return pubDo(t, req, header)
}

// pubPost 以 JSON POST 调公开服务；header 里的 Content-Type 覆盖默认值。
func pubPost(t *testing.T, h *harness, method, body string, header map[string]string) pubResult {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.srv.URL+"/probe.v1.PublicService/"+method, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return pubDo(t, req, header)
}

// 未公开与不存在的节点得到同一个响应：状态码与正文逐字节相同，错误里没有 id。
func TestPublicHistoryTreatsPrivateAndMissingNodesAlike(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	pub, _ := h.createNode(t, "pub")
	priv, _ := h.createNode(t, "priv")
	h.setPublic(t, pub, "pub", true)
	window := func(id int64) string { return fmt.Sprintf(`{"nodeId":"%d","from":"0","to":"3600"}`, id) }
	for _, method := range []string{"QueryMetrics", "QueryProbes"} {
		if got := pubGet(t, h, method, jsonQuery(window(pub)), nil); got.status != http.StatusOK {
			t.Fatalf("%s on a public node: %d %s", method, got.status, got.body)
		}
		private := pubGet(t, h, method, jsonQuery(window(priv)), nil)
		missing := pubGet(t, h, method, jsonQuery(window(999999)), nil)
		if private.status != http.StatusNotFound || missing.status != private.status || !bytes.Equal(private.body, missing.body) {
			t.Fatalf("%s: private %d %s, missing %d %s", method, private.status, private.body, missing.status, missing.body)
		}
		if !bytes.Contains(private.body, []byte(`"node_id: no public node has this id"`)) {
			t.Fatalf("%s: body %s", method, private.body)
		}
	}
}

func TestPublicHistorySharesWindowValidation(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	pub, _ := h.createNode(t, "pub")
	priv, _ := h.createNode(t, "priv")
	h.setPublic(t, pub, "pub", true)
	client := h.publicClient()
	for _, tc := range []struct {
		name           string
		node, from, to int64
		max            uint32
		code           connect.Code
		text           string
	}{
		{"negative", pub, -1, 60, 0, connect.CodeInvalidArgument, "from must be a nonnegative Unix timestamp; got -1"},
		{"order", pub, 60, 60, 0, connect.CodeInvalidArgument, "from (60) must be earlier than to (60)"},
		{"span", pub, 0, 401 * 86400, 0, connect.CodeInvalidArgument, "window spans 34646400 seconds; the maximum is 34560000 (400 days)"},
		{"points", pub, 0, 3600, 2001, connect.CodeInvalidArgument, "max_points must be at most 2000; got 2001"},
		{"private", priv, 0, 3600, 0, connect.CodeNotFound, "node_id: no public node has this id"},
		{"missing", 999, 0, 3600, 0, connect.CodeNotFound, "node_id: no public node has this id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, pe := client.QueryProbes(t.Context(), connect.NewRequest(&probev1.QueryProbesRequest{NodeId: tc.node, From: tc.from, To: tc.to, MaxPoints: tc.max}))
			_, me := client.QueryMetrics(t.Context(), connect.NewRequest(&probev1.QueryMetricsRequest{NodeId: tc.node, From: tc.from, To: tc.to, MaxPoints: tc.max}))
			for _, err := range []error{pe, me} {
				if codeOf(err) != tc.code || !strings.Contains(err.Error(), tc.text) {
					t.Errorf("error=%v want=%s %q", err, tc.code, tc.text)
				}
			}
		})
	}
}

func TestPublicSnapshotListsOnlyPublicNodesWithPublicFields(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	ctx := t.Context()
	a, tokA := h.createNode(t, "a")
	b, _ := h.createNode(t, "b")
	c, _ := h.createNode(t, "c")
	h.setPublic(t, a, "a", true)
	h.setPublic(t, c, "c", true)
	if _, err := h.admin.ReorderNodes(ctx, connect.NewRequest(&probev1.ReorderNodesRequest{Ids: []int64{c, b, a}})); err != nil {
		t.Fatal(err)
	}
	facts := &probev1.Facts{Hostname: "secret-host", Os: "Debian 12", Kernel: "6.1.0-secret", Arch: "amd64", Virtualization: "kvm",
		CpuModel: "EPYC", CpuCores: 4, AgentVersion: "v9.9.9-secret", IcmpAvailable: true}
	if err := h.store.UpsertFacts(ctx, a, 1, facts); err != nil {
		t.Fatal(err)
	}
	if err := h.report(t, tokA, &probev1.Metrics{BootId: "boot-secret", CpuPct: proto.Float64(12.5), MemUsed: proto.Uint64(0), MemTotal: proto.Uint64(1 << 30)}); err != nil {
		t.Fatal(err)
	}
	resp, err := h.publicClient().GetSnapshot(ctx, connect.NewRequest(&probev1.PublicServiceGetSnapshotRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	snap := resp.Msg
	if snap.GetNow() != h.clk.Now().Unix() || snap.GetReportIntervalMs() != 10000 || len(snap.GetNodes()) != 2 {
		t.Fatalf("snapshot = %v", snap)
	}
	first, second := snap.GetNodes()[0], snap.GetNodes()[1]
	if first.GetId() != c || second.GetId() != a || first.GetSortOrder() >= second.GetSortOrder() {
		t.Fatalf("nodes not in panel order: %v", snap.GetNodes())
	}
	if first.GetOnline() || first.LastSeenAt != nil || first.Facts != nil || first.Metrics != nil || first.Traffic == nil {
		t.Fatalf("never-reported node = %v", first)
	}
	wantFacts := &probev1.PublicFacts{Os: "Debian 12", Arch: "amd64", Virtualization: "kvm", CpuModel: "EPYC", CpuCores: 4}
	wantMetrics := &probev1.PublicMetrics{CpuPct: proto.Float64(12.5), MemUsed: proto.Uint64(0), MemTotal: proto.Uint64(1 << 30)}
	if !second.GetOnline() || second.GetLastSeenAt() != h.clk.Now().Unix() || !proto.Equal(second.GetFacts(), wantFacts) ||
		!proto.Equal(second.GetMetrics(), wantMetrics) || second.Traffic == nil {
		t.Fatalf("reported node = %v", second)
	}
	// 正文层面再核一次：不公开的字段与私有节点的名字都不在 JSON 里。
	raw := pubGet(t, h, "GetSnapshot", jsonQuery("{}"), nil)
	for _, leak := range []string{"secret", "hostname", "kernel", "agentVersion", "icmpAvailable", "bootId", `"b"`} {
		if bytes.Contains(raw.body, []byte(leak)) {
			t.Errorf("snapshot JSON contains %s: %s", leak, raw.body)
		}
	}
}

func TestPublicSiteServesSavedSettings(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	client := h.publicClient()
	got, err := client.GetSite(t.Context(), connect.NewRequest(&probev1.GetSiteRequest{}))
	if err != nil || !proto.Equal(got.Msg, &probev1.PublicSite{Theme: "auto"}) {
		t.Fatalf("never saved: %v %v", got, err)
	}
	saveSettings(t, h, validSettings())
	got, err = client.GetSite(t.Context(), connect.NewRequest(&probev1.GetSiteRequest{}))
	want := &probev1.PublicSite{Title: "状态", Theme: "dark", AccentColor: "#112233", Logo: "data:image/png;base64,iVBORw0KGgo=", CustomCss: "body { color: red }"}
	if err != nil || !proto.Equal(got.Msg, want) {
		t.Fatalf("site = %v %v, want %v", got, err, want)
	}
}

// publicFields 是 PublicService 的响应能到达的每个消息的字段全集。往这些消息加字段就是公开给匿名访客，
// 必须同时改这份清单——与 access_test 的 readMethods 同一口径。共用的 Traffic 与历史查询类型同样在列：
// 给它们加字段也会出现在公开页。
var publicFields = map[protoreflect.FullName][]protoreflect.Name{
	"probe.v1.PublicSite":     {"title", "theme", "accent_color", "logo", "custom_css"},
	"probe.v1.PublicSnapshot": {"now", "report_interval_ms", "nodes"},
	"probe.v1.PublicNode":     {"id", "name", "online", "last_seen_at", "sort_order", "facts", "metrics", "traffic"},
	"probe.v1.PublicFacts":    {"os", "arch", "virtualization", "cpu_model", "cpu_cores"},
	"probe.v1.PublicMetrics": {"cpu_pct", "load1", "load5", "load15", "mem_total", "mem_used", "swap_total", "swap_used",
		"disk_total", "disk_used", "net_rx_total", "net_tx_total", "net_rx_bps", "net_tx_bps", "tcp_conns", "udp_conns", "procs", "uptime_s"},
	"probe.v1.Traffic":              {"total_rx", "total_tx", "period_rx", "period_tx", "period_start", "next_reset_at", "reset_day"},
	"probe.v1.QueryMetricsResponse": {"level", "step_s", "ts", "series"},
	"probe.v1.MetricSeries":         {"name", "unit", "samples"},
	"probe.v1.MetricSample":         {"n", "mean", "max", "sum"},
	"probe.v1.QueryProbesResponse":  {"level", "step_s", "series"},
	"probe.v1.ProbeSeries":          {"task_id", "samples", "kind", "target"},
	"probe.v1.ProbeSample":          {"ts", "sent", "lost", "errors", "rtt_mean_us", "rtt_min_us", "rtt_max_us"},
}

func TestPublicResponsesExposeOnlyAllowlistedFields(t *testing.T) {
	svc := probev1.File_probe_v1_public_proto.Services().ByName("PublicService")
	seen := map[protoreflect.FullName]bool{}
	var walk func(md protoreflect.MessageDescriptor)
	walk = func(md protoreflect.MessageDescriptor) {
		if seen[md.FullName()] {
			return
		}
		seen[md.FullName()] = true
		var got []protoreflect.Name
		for i := 0; i < md.Fields().Len(); i++ {
			f := md.Fields().Get(i)
			got = append(got, f.Name())
			if f.Message() != nil {
				walk(f.Message())
			}
		}
		want, ok := publicFields[md.FullName()]
		if !ok {
			t.Errorf("%s is reachable from PublicService but not in publicFields; its fields are %v", md.FullName(), got)
			return
		}
		slices.Sort(got)
		if want = slices.Sorted(slices.Values(want)); !slices.Equal(got, want) {
			t.Errorf("fields of %s = %v, allowlist has %v", md.FullName(), got, want)
		}
	}
	if svc.Methods().Len() == 0 {
		t.Fatal("PublicService has no methods")
	}
	for i := 0; i < svc.Methods().Len(); i++ {
		walk(svc.Methods().Get(i).Output())
	}
	for name := range publicFields {
		if !seen[name] {
			t.Errorf("publicFields lists %s, which no PublicService response reaches", name)
		}
	}
}

// 公开节点的历史与管理端同一来源：对同一个请求两端的应答逐字段相同，公开端只多了"节点必须公开"这一道门。
// 任务仍分配给该节点时探测序列的标签两端也相同；撤下之后的分歧由 TestPublicProbeLabelsOnlyTasksAssignedToTheNode 钉住。
func TestPublicHistoryMatchesAdminForAPublicNode(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	id, _ := h.createNode(t, "pub")
	h.setPublic(t, id, "pub", true)
	saved, err := h.admin.SaveProbeTask(t.Context(), connect.NewRequest(&probev1.SaveProbeTaskRequest{
		Task:    &probev1.ProbeTask{Kind: probev1.ProbeKind_PROBE_KIND_ICMP, Target: "192.0.2.1", IntervalS: 30, TimeoutMs: 1000},
		NodeIds: []int64{id},
	}))
	if err != nil {
		t.Fatal(err)
	}
	base := h.clk.Now().Truncate(time.Hour).Unix()
	b := metric.NewBucket()
	b.Add(&probev1.Metrics{CpuPct: proto.Float64(7)})
	batch := metric.Batch{
		Rows:   []metric.Row{{NodeID: id, TS: base, Bucket: b}},
		Probes: []metric.ProbeRow{{NodeID: id, TS: base, TaskID: saved.Msg.GetTask().GetTask().GetId(), Bucket: &metric.ProbeBucket{Sent: 2, Lost: 1}}},
	}
	if _, err := h.store.WriteMinuteBatch(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	pub := h.publicClient()
	metrics := &probev1.QueryMetricsRequest{NodeId: id, From: base, To: base + 3600}
	pm, err := pub.QueryMetrics(t.Context(), connect.NewRequest(metrics))
	if err != nil {
		t.Fatal(err)
	}
	am, err := h.admin.QueryMetrics(t.Context(), connect.NewRequest(metrics))
	if err != nil {
		t.Fatal(err)
	}
	if len(am.Msg.GetTs()) != 1 || !proto.Equal(pm.Msg, am.Msg) {
		t.Errorf("public QueryMetrics = %v\nadmin QueryMetrics = %v", pm.Msg, am.Msg)
	}
	probes := &probev1.QueryProbesRequest{NodeId: id, From: base, To: base + 3600}
	pp, err := pub.QueryProbes(t.Context(), connect.NewRequest(probes))
	if err != nil {
		t.Fatal(err)
	}
	ap, err := h.admin.QueryProbes(t.Context(), connect.NewRequest(probes))
	if err != nil {
		t.Fatal(err)
	}
	if len(ap.Msg.GetSeries()) != 1 || !proto.Equal(pp.Msg, ap.Msg) {
		t.Errorf("public QueryProbes = %v\nadmin QueryProbes = %v", pp.Msg, ap.Msg)
	}
}

// 公开端只标注当前分配给被查节点的任务：任务从公开节点撤下、目标改成只分配给私有节点的内网地址之后，
// 公开节点历史里的这条序列不带种类与目标；管理端照旧按任务当前的配置标注。
func TestPublicProbeLabelsOnlyTasksAssignedToTheNode(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	pub, _ := h.createNode(t, "pub")
	priv, _ := h.createNode(t, "priv")
	h.setPublic(t, pub, "pub", true)
	save := func(id uint64, target string, node int64) uint64 {
		t.Helper()
		resp, err := h.admin.SaveProbeTask(t.Context(), connect.NewRequest(&probev1.SaveProbeTaskRequest{
			Task:    &probev1.ProbeTask{Id: id, Kind: probev1.ProbeKind_PROBE_KIND_ICMP, Target: target, IntervalS: 30, TimeoutMs: 1000},
			NodeIds: []int64{node},
		}))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg.GetTask().GetTask().GetId()
	}
	kept := save(0, "192.0.2.1", pub)
	moved := save(0, "192.0.2.2", pub)
	base := h.clk.Now().Truncate(time.Hour).Unix()
	rows := []metric.ProbeRow{
		{NodeID: pub, TS: base, TaskID: kept, Bucket: &metric.ProbeBucket{Sent: 1, Lost: 1}},
		{NodeID: pub, TS: base, TaskID: moved, Bucket: &metric.ProbeBucket{Sent: 1, Lost: 1}},
	}
	if _, err := h.store.WriteMinuteBatch(t.Context(), metric.Batch{Probes: rows}); err != nil {
		t.Fatal(err)
	}
	save(moved, "10.0.0.5", priv)
	req := &probev1.QueryProbesRequest{NodeId: pub, From: base, To: base + 3600}
	labels := func(series []*probev1.ProbeSeries) map[uint64]string {
		out := map[uint64]string{}
		for _, s := range series {
			out[s.GetTaskId()] = fmt.Sprintf("%v %q", s.GetKind(), s.GetTarget())
		}
		return out
	}
	public, err := h.publicClient().QueryProbes(t.Context(), connect.NewRequest(req))
	if err != nil {
		t.Fatal(err)
	}
	admin, err := h.admin.QueryProbes(t.Context(), connect.NewRequest(req))
	if err != nil {
		t.Fatal(err)
	}
	wantPublic := map[uint64]string{kept: `PROBE_KIND_ICMP "192.0.2.1"`, moved: `PROBE_KIND_UNSPECIFIED ""`}
	wantAdmin := map[uint64]string{kept: `PROBE_KIND_ICMP "192.0.2.1"`, moved: `PROBE_KIND_ICMP "10.0.0.5"`}
	if got := labels(public.Msg.GetSeries()); !maps.Equal(got, wantPublic) {
		t.Errorf("public labels = %v, want %v", got, wantPublic)
	}
	if got := labels(admin.Msg.GetSeries()); !maps.Equal(got, wantAdmin) {
		t.Errorf("admin labels = %v, want %v", got, wantAdmin)
	}
}
