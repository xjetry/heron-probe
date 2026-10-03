package ingest

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/hub/store"
	"github.com/xjetry/heron-probe/internal/testwait"
	"google.golang.org/protobuf/proto"
)

func (h *hub) httpTask(t *testing.T, nodeID int64, target string) uint64 {
	t.Helper()
	d, _, err := h.reg.Save(context.Background(), &heronv1.ProbeTask{Kind: heronv1.ProbeKind_PROBE_KIND_HTTP, Target: target, IntervalS: 30, TimeoutMs: 1000}, store.NodeSelector{AllNodes: false, NodeIDs: []int64{nodeID}})
	if err != nil {
		t.Fatal(err)
	}
	return d.Task.Id
}

func certReport(tok string, r *heronv1.ProbeResult) *connect.Request[heronv1.ReportRequest] {
	req := report(tok, &heronv1.Metrics{})
	req.Msg.ProbeResults = []*heronv1.ProbeResult{r}
	return req
}

// cert_not_after_s 只允许由 https:// 的 HTTP 任务携带：ICMP、http:// 与清单外的任务携带时整批
// InvalidArgument，点名结果索引与任务（agent-first：不静默丢弃）。
func TestReportRejectsCertFromNonHTTPSTasks(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	icmp := h.task(t, id)
	plainHTTP := h.httpTask(t, id, "http://example.com/")
	cert := proto.Int64(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC).Unix())
	for _, c := range []struct {
		name string
		task uint64
		want string
	}{
		{"icmp", icmp, "cert_not_after_s: task"},
		{"http", plainHTTP, "must be an https:// HTTP probe task"},
		{"unknown", 999, "is not in the task list"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h.clk.Advance(time.Minute) // 速率令牌按半间隔补充；每个用例推进一分钟，互不吃限
			r := rtt(c.task, 0, 1200)
			r.CertNotAfterS = cert
			_, err := h.client.Report(t.Context(), certReport(tok, r))
			if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "probe_results[0].cert_not_after_s") || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err=%v, want InvalidArgument naming probe_results[0].cert_not_after_s and %q", err, c.want)
			}
		})
	}
}

// 证书到期时刻来自成功的 TLS 握手：与 timeout 或 error 同现、非正值，都是非法形状，整批拒绝。
func TestReportRejectsCertWithoutSuccess(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	task := h.httpTask(t, id, "https://example.com/")
	for _, c := range []struct {
		name string
		r    *heronv1.ProbeResult
		want string
	}{
		{"timeout", &heronv1.ProbeResult{TaskId: task, Outcome: &heronv1.ProbeResult_Timeout{Timeout: &heronv1.Timeout{}}, CertNotAfterS: proto.Int64(1)}, "must only accompany a successful (rtt_us) result"},
		{"error", &heronv1.ProbeResult{TaskId: task, Outcome: &heronv1.ProbeResult_Error{Error: &heronv1.ProbeError{Message: "x"}}, CertNotAfterS: proto.Int64(1)}, "must only accompany a successful (rtt_us) result"},
		{"zero", &heronv1.ProbeResult{TaskId: task, Outcome: &heronv1.ProbeResult_RttUs{RttUs: 1}, CertNotAfterS: proto.Int64(0)}, "must be positive"},
		{"negative", &heronv1.ProbeResult{TaskId: task, Outcome: &heronv1.ProbeResult_RttUs{RttUs: 1}, CertNotAfterS: proto.Int64(-5)}, "must be positive"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h.clk.Advance(time.Minute) // 速率令牌按半间隔补充；每个用例推进一分钟，互不吃限
			_, err := h.client.Report(t.Context(), certReport(tok, c.r))
			if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "probe_results[0].cert_not_after_s: "+c.want) {
				t.Fatalf("err=%v, want %s", err, c.want)
			}
		})
	}
}

// 合法携带覆盖写 probe_cert：两次上报后者胜出；同任务别的节点、别的任务互不影响。
func TestReportWritesProbeCert(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	other, otherTok := h.node(t)
	task := h.httpTask(t, id, "https://example.com/")
	d, _, err := h.reg.Save(context.Background(), &heronv1.ProbeTask{Kind: heronv1.ProbeKind_PROBE_KIND_HTTP, Target: "https://example.com/", IntervalS: 30, TimeoutMs: 1000}, store.NodeSelector{AllNodes: false, NodeIDs: []int64{other}})
	if err != nil {
		t.Fatal(err)
	}
	otherTask := d.Task.Id
	reportCert := func(tok string, task uint64, notAfter int64) {
		t.Helper()
		r := rtt(task, 0, 1200)
		r.CertNotAfterS = proto.Int64(notAfter)
		if _, err := h.client.Report(t.Context(), certReport(tok, r)); err != nil {
			t.Fatal(err)
		}
	}
	first := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC).Unix()
	second := time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC).Unix()
	reportCert(tok, task, first)
	reportCert(otherTok, otherTask, first+100)
	certs := func() map[int64]int64 {
		c, err := h.store.ProbeCertsByTask(context.Background(), task)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	testwait.Until(t, 5*time.Millisecond, func() bool { return certs()[id] == first }, "first cert not written: %v", certs())
	reportCert(tok, task, second)
	testwait.Until(t, 5*time.Millisecond, func() bool { return certs()[id] == second }, "cert not overwritten: %v", certs())
	others, err := h.store.ProbeCertsByTask(context.Background(), otherTask)
	if err != nil || others[other] != first+100 {
		t.Fatalf("other task's cert row = %v %v", others, err)
	}
}

// not_after 变化（含首次写入）触发 CertObserved——装配方据此立即评估证书到期规则；
// 同值重写不触发。写协程串行处理：第三次的回调计数到 2 时，第二次的同值写已经处理完且没有触发。
func TestReportCertObservedFiresOnlyOnChange(t *testing.T) {
	var fired atomic.Int32
	h := newHubWith(t, t.TempDir()+"/t.db", Config{TTL: 30 * time.Second, CertObserved: func() { fired.Add(1) }})
	id, tok := h.node(t)
	task := h.httpTask(t, id, "https://example.com/")
	reportCert := func(notAfter int64) {
		t.Helper()
		r := rtt(task, 0, 1200)
		r.CertNotAfterS = proto.Int64(notAfter)
		if _, err := h.client.Report(t.Context(), certReport(tok, r)); err != nil {
			t.Fatal(err)
		}
	}
	reportCert(100)
	testwait.Until(t, 5*time.Millisecond, func() bool { return fired.Load() == 1 }, "first write did not fire CertObserved")
	reportCert(100) // 同值：不触发
	reportCert(200)
	testwait.Until(t, 5*time.Millisecond, func() bool { return fired.Load() >= 2 }, "changed write did not fire CertObserved")
	if n := fired.Load(); n != 2 {
		t.Fatalf("CertObserved fired %d times, want 2 (same-value rewrite must not fire)", n)
	}
}
