package ingest

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"connectrpc.com/connect"
	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/agentwire"
	"github.com/xjetry/heron-probe/internal/probelimit"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func maxErrorResult() *heronv1.ProbeResult {
	return &heronv1.ProbeResult{TaskId: math.MaxUint64, AgeMs: math.MaxUint32, Outcome: &heronv1.ProbeResult_Error{Error: &heronv1.ProbeError{Message: strings.Repeat("x", probelimit.MaxErrorMessageLen)}}}
}

func TestReportHostStringLimits(t *testing.T) {
	facts := (&heronv1.Facts{}).ProtoReflect().Descriptor().Fields()
	fields := []string{"boot_id"}
	for i := 0; i < facts.Len(); i++ {
		if field := facts.Get(i); field.Kind() == protoreflect.StringKind {
			fields = append(fields, "facts."+string(field.Name()))
		}
	}
	for _, field := range fields {
		for _, value := range []string{strings.Repeat("x", 256), strings.Repeat("x", 257), strings.Repeat("界", 85) + "x", strings.Repeat("界", 86)} {
			size := len(value)
			t.Run(fmt.Sprintf("%s/%d", field, size), func(t *testing.T) {
				h := newHub(t)
				id, tok := h.node(t)
				req := report(tok, &heronv1.Metrics{})
				if field == "boot_id" {
					req.Msg.Metrics.BootId = value
				} else {
					req.Msg.Facts = &heronv1.Facts{}
					fd := facts.ByName(protoreflect.Name(strings.TrimPrefix(field, "facts.")))
					req.Msg.Facts.ProtoReflect().Set(fd, protoreflect.ValueOfString(value))
				}
				_, err := h.client.Report(t.Context(), req)
				if size == 256 {
					if err != nil {
						t.Fatalf("boundary rejected: %v", err)
					}
				} else {
					want := fmt.Sprintf("%s: must be at most 256 bytes; got %d", field, size)
					if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), want) {
						t.Errorf("oversized host field error=%v want %s", err, want)
					}
					if _, ok := h.live.Get(id); ok {
						t.Error("invalid host field changed live state")
					}
				}
			})
		}
	}
}

func TestMaxProbeResultWire(t *testing.T) {
	r := maxErrorResult()
	size := proto.Size(r)
	framed := proto.Size(&heronv1.ReportRequest{ProbeResults: []*heronv1.ProbeResult{r}})
	t.Logf("result=%d framed=%d budget=%d", size, framed, maxResultWire)
	if size > maxResultWire || framed > maxResultWire {
		t.Fatalf("wire size %d/%d exceeds %d", size, framed, maxResultWire)
	}
}

func maxHostReport(t *testing.T) *heronv1.ReportRequest {
	t.Helper()
	r := &heronv1.ReportRequest{Metrics: &heronv1.Metrics{}, Facts: &heronv1.Facts{}, TasksVersion: math.MaxUint64, FactsHash: math.MaxUint64}
	r.Update = &heronv1.UpdateStatus{Supported: true, Reason: strings.Repeat("x", 2048), Version: strings.Repeat("x", 64), Task: &heronv1.UpdateTask{Id: strings.Repeat("x", 64), Version: "v4294967295.4294967295.4294967295", ExpiresAt: math.MaxInt64, State: "downloading", Error: strings.Repeat("x", 2048), UpdatedAt: math.MaxInt64}}
	for _, m := range []proto.Message{r.Metrics, r.Facts} {
		msg := m.ProtoReflect()
		fields := msg.Descriptor().Fields()
		for i := 0; i < fields.Len(); i++ {
			fd := fields.Get(i)
			var value protoreflect.Value
			switch fd.Kind() {
			case protoreflect.MessageKind:
				if fd.FullName() == "heron.v1.Facts.diagnostics" {
					d := &heronv1.AgentDiagnostics{NetInterfacesTotal: math.MaxUint32, ReportIntervalMs: agentwire.ReportIntervalMs(agentwire.MaxTTL)}
					for range agentwire.MaxNetPatterns {
						d.NetInclude = append(d.NetInclude, strings.Repeat("x", agentwire.MaxNetPatternBytes))
					}
					for n := range agentwire.MaxDiagnosticInterfaces {
						d.NetInterfaces = append(d.NetInterfaces, fmt.Sprintf("%03d%s", n, strings.Repeat("x", agentwire.MaxInterfaceNameBytes-3)))
					}
					for part := heronv1.CollectionComponent_COLLECTION_COMPONENT_BOOT_ID; part < heronv1.CollectionComponent_COLLECTION_COMPONENT_NET; part++ {
						d.FailedCollectors = append(d.FailedCollectors, part)
					}
					if err := agentwire.ValidateDiagnostics(d); err != nil {
						t.Fatal(err)
					}
					value = protoreflect.ValueOfMessage(d.ProtoReflect())
					break
				}
				if fd.FullName() != "heron.v1.Facts.network" {
					t.Fatalf("unbounded host message %s", fd.FullName())
				}
				value = protoreflect.ValueOfMessage((&heronv1.NetworkInfo{
					Ipv4: &heronv1.AddressDetection{State: heronv1.AddressDetectionState_ADDRESS_DETECTION_STATE_AVAILABLE, Address: "223.255.255.255", CheckedAt: agentwire.MaxDetectionTime},
					Ipv6: &heronv1.AddressDetection{State: heronv1.AddressDetectionState_ADDRESS_DETECTION_STATE_AVAILABLE, Address: "2606:ffff:ffff:ffff:ffff:ffff:ffff:ffff", CheckedAt: agentwire.MaxDetectionTime},
				}).ProtoReflect())
			case protoreflect.StringKind:
				value = protoreflect.ValueOfString(strings.Repeat("x", maxHostString))
				if fd.FullName() == "heron.v1.Metrics.net_counter_epoch" {
					value = protoreflect.ValueOfString(strings.Repeat("a", 64))
				}
			case protoreflect.Uint32Kind:
				value = protoreflect.ValueOfUint32(math.MaxUint32)
			case protoreflect.Uint64Kind:
				value = protoreflect.ValueOfUint64(math.MaxUint64)
			case protoreflect.BoolKind:
				value = protoreflect.ValueOfBool(true)
			case protoreflect.DoubleKind:
				// double 固定占八字节；100 同时满足 cpu_pct 的准入范围。
				value = protoreflect.ValueOfFloat64(100)
			default:
				t.Fatalf("unbounded host field %s: %s", fd.FullName(), fd.Kind())
			}
			msg.Set(fd, value)
		}
	}
	return r
}

func TestHostPayloadFitsMetricsBudget(t *testing.T) {
	size := proto.Size(maxHostReport(t))
	t.Logf("host payload=%d budget=%d", size, metricsBudget)
	if size > metricsBudget {
		t.Fatalf("host payload=%d exceeds budget=%d", size, metricsBudget)
	}
}

func TestReportResultLimits(t *testing.T) {
	for _, tc := range []struct {
		name          string
		count, length int
		field         string
	}{
		{"boundary", 1024, 128, ""}, {"count", 1025, 128, "probe_results: must contain at most 1024"}, {"message", 1, 129, "error.message: must be at most 128 bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHub(t)
			_, tok := h.node(t)
			req := connect.NewRequest(maxHostReport(t))
			req.Header().Set("Authorization", "Bearer "+tok)
			for range tc.count {
				r := maxErrorResult()
				r.GetError().Message = strings.Repeat("x", tc.length)
				req.Msg.ProbeResults = append(req.Msg.ProbeResults, r)
			}
			_, err := h.client.Report(t.Context(), req)
			if tc.field == "" {
				if err != nil {
					t.Fatalf("maximum valid batch rejected: %v", err)
				}
			} else if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("invalid batch error=%v want %s", err, tc.field)
			}
		})
	}
}

// maxReportResponse 按每个字段的准入上界填满 ReportResponse。上界不明的字段（新加的字符串、repeated）让测试失败，
// 逼新增字段的人先给出上界，而不是让它默默落在 agent 的读取上限之外。
func maxReportResponse(t *testing.T) *heronv1.ReportResponse {
	t.Helper()
	strLen := map[protoreflect.FullName]int{"heron.v1.ProbeTask.target": probelimit.MaxTargetLen, "heron.v1.UpdateTask.id": 64, "heron.v1.UpdateTask.version": 34, "heron.v1.UpdateTask.state": 11, "heron.v1.UpdateTask.error": 2048}
	count := map[protoreflect.FullName]int{"heron.v1.ProbeTasks.tasks": probelimit.MaxTasksPerNode}
	var fill func(msg protoreflect.Message)
	scalar := func(msg protoreflect.Message, fd protoreflect.FieldDescriptor) protoreflect.Value {
		switch fd.Kind() {
		case protoreflect.StringKind:
			n, ok := strLen[fd.FullName()]
			if !ok {
				t.Fatalf("string field %s has no known bound", fd.FullName())
			}
			return protoreflect.ValueOfString(strings.Repeat("x", n))
		case protoreflect.Uint32Kind:
			return protoreflect.ValueOfUint32(math.MaxUint32)
		case protoreflect.Uint64Kind:
			return protoreflect.ValueOfUint64(math.MaxUint64)
		case protoreflect.Int64Kind:
			return protoreflect.ValueOfInt64(math.MaxInt64)
		case protoreflect.BoolKind:
			return protoreflect.ValueOfBool(true)
		case protoreflect.EnumKind:
			values := fd.Enum().Values()
			return protoreflect.ValueOfEnum(values.Get(values.Len() - 1).Number())
		case protoreflect.MessageKind:
			v := msg.NewField(fd)
			fill(v.Message())
			return v
		}
		t.Fatalf("field %s of kind %s has no known bound", fd.FullName(), fd.Kind())
		return protoreflect.Value{}
	}
	fill = func(msg protoreflect.Message) {
		fields := msg.Descriptor().Fields()
		for i := 0; i < fields.Len(); i++ {
			fd := fields.Get(i)
			if !fd.IsList() {
				msg.Set(fd, scalar(msg, fd))
				continue
			}
			n, ok := count[fd.FullName()]
			if !ok {
				t.Fatalf("repeated field %s has no known bound", fd.FullName())
			}
			list := msg.Mutable(fd).List()
			for j := 0; j < n; j++ {
				if fd.Kind() == protoreflect.MessageKind {
					e := list.NewElement()
					fill(e.Message())
					list.Append(e)
				} else {
					list.Append(scalar(msg, fd))
				}
			}
		}
	}
	r := &heronv1.ReportResponse{}
	fill(r.ProtoReflect())
	return r
}

// hub 能下发的最大 ReportResponse 必须在 agent 的读取上限之内，否则满载节点每次收到清单都失败、永远拿不到任务。
func TestMaxReportResponseFitsAgentLimit(t *testing.T) {
	r := maxReportResponse(t)
	if len(r.GetTasks().GetTasks()) != probelimit.MaxTasksPerNode {
		t.Fatalf("filled %d tasks, want %d", len(r.GetTasks().GetTasks()), probelimit.MaxTasksPerNode)
	}
	size := proto.Size(r)
	t.Logf("max ReportResponse=%d limit=%d", size, agentwire.MaxResponseBytes)
	if size > agentwire.MaxResponseBytes {
		t.Fatalf("max ReportResponse=%d exceeds the agent read limit %d", size, agentwire.MaxResponseBytes)
	}
}
