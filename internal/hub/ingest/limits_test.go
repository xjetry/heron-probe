package ingest

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"connectrpc.com/connect"
	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
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
	for _, m := range []proto.Message{r.Metrics, r.Facts} {
		msg := m.ProtoReflect()
		fields := msg.Descriptor().Fields()
		for i := 0; i < fields.Len(); i++ {
			fd := fields.Get(i)
			var value protoreflect.Value
			switch fd.Kind() {
			case protoreflect.StringKind:
				value = protoreflect.ValueOfString(strings.Repeat("x", maxHostString))
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
