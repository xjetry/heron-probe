package ingest

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
)

func TestReportRejectsUnboundedUpdateBeforeObservingMetrics(t *testing.T) {
	for _, field := range []string{"reason", "version", "task_error", "task_id", "task_state", "task_version"} {
		t.Run(field, func(t *testing.T) {
			h := newHub(t)
			id, tok := h.node(t)
			req := report(tok, &heronv1.Metrics{})
			req.Msg.Update = &heronv1.UpdateStatus{Supported: true, Version: "v0.2.0", Task: &heronv1.UpdateTask{Id: "0123456789abcdef", Version: "v0.3.0", State: "downloading", ExpiresAt: 1}}
			s := req.Msg.Update
			switch field {
			case "reason":
				s.Reason = strings.Repeat("x", 2049)
			case "version":
				s.Version = strings.Repeat("x", 65)
			case "task_error":
				s.Task.Error = strings.Repeat("x", 2049)
			case "task_id":
				s.Task.Id = strings.Repeat("x", 65)
			case "task_state":
				s.Task.State = "arbitrary"
			case "task_version":
				s.Task.Version = "v0.3.0-rc1"
			}
			_, err := h.client.Report(t.Context(), req)
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("invalid update %s accepted: %v", field, err)
			}
			if _, ok := h.live.Get(id); ok {
				t.Fatal("invalid update altered live metrics")
			}
		})
	}
}
