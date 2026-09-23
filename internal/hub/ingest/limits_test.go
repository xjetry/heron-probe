package ingest

import (
	"math"
	"strings"
	"testing"

	"connectrpc.com/connect"
	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/probelimit"
	"google.golang.org/protobuf/proto"
)

func maxErrorResult() *probev1.ProbeResult {
	return &probev1.ProbeResult{TaskId: math.MaxUint64, AgeMs: math.MaxUint32, Outcome: &probev1.ProbeResult_Error{Error: &probev1.ProbeError{Message: strings.Repeat("x", probelimit.MaxErrorMessageLen)}}}
}

func TestMaxProbeResultWire(t *testing.T) {
	r := maxErrorResult()
	size := proto.Size(r)
	framed := proto.Size(&probev1.ReportRequest{ProbeResults: []*probev1.ProbeResult{r}})
	t.Logf("result=%d framed=%d budget=%d", size, framed, maxResultWire)
	if size > maxResultWire || framed > maxResultWire {
		t.Fatalf("wire size %d/%d exceeds %d", size, framed, maxResultWire)
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
			req := report(tok, &probev1.Metrics{})
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
