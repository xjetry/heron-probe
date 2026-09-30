package agentwire

import (
	"strings"
	"testing"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"google.golang.org/protobuf/proto"
)

func TestDiagnosticsBoundedWhitelist(t *testing.T) {
	good := &heronv1.AgentDiagnostics{NetExclude: []string{"lo", "veth*"}, NetInterfaces: []string{"eth0"}, NetInterfacesTotal: 1, ReportIntervalMs: 10000}
	if err := ValidateDiagnostics(good); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDiagnostics(nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*heronv1.AgentDiagnostics)
	}{
		{"unknown field", func(d *heronv1.AgentDiagnostics) { d.ProtoReflect().SetUnknown([]byte{0x3a, 0x01, 'x'}) }},
		{"both policies", func(d *heronv1.AgentDiagnostics) { d.NetInclude = []string{"eth*"} }},
		{"too many patterns", func(d *heronv1.AgentDiagnostics) {
			d.NetExclude = make([]string, MaxNetPatterns+1)
			for i := range d.NetExclude {
				d.NetExclude[i] = "lo"
			}
		}},
		{"long pattern", func(d *heronv1.AgentDiagnostics) { d.NetExclude = []string{strings.Repeat("x", MaxNetPatternBytes+1)} }},
		{"invalid glob", func(d *heronv1.AgentDiagnostics) { d.NetExclude = []string{"["} }},
		{"control", func(d *heronv1.AgentDiagnostics) { d.NetExclude = []string{"lo\nsecret"} }},
		{"invalid utf8", func(d *heronv1.AgentDiagnostics) { d.NetExclude = []string{string([]byte{0xff})} }},
		{"total mismatch", func(d *heronv1.AgentDiagnostics) { d.NetInterfacesTotal = 2 }},
		{"long name", func(d *heronv1.AgentDiagnostics) {
			d.NetInterfaces = []string{strings.Repeat("x", MaxInterfaceNameBytes+1)}
		}},
		{"duplicate name", func(d *heronv1.AgentDiagnostics) {
			d.NetInterfaces = []string{"eth0", "eth0"}
			d.NetInterfacesTotal = 2
		}},
		{"unordered name", func(d *heronv1.AgentDiagnostics) {
			d.NetInterfaces = []string{"eth1", "eth0"}
			d.NetInterfacesTotal = 2
		}},
		{"unknown component", func(d *heronv1.AgentDiagnostics) { d.FailedCollectors = []heronv1.CollectionComponent{99} }},
		{"unspecified component", func(d *heronv1.AgentDiagnostics) { d.FailedCollectors = []heronv1.CollectionComponent{0} }},
		{"duplicate component", func(d *heronv1.AgentDiagnostics) { d.FailedCollectors = []heronv1.CollectionComponent{1, 1} }},
		{"failed network has names", func(d *heronv1.AgentDiagnostics) {
			d.FailedCollectors = []heronv1.CollectionComponent{heronv1.CollectionComponent_COLLECTION_COMPONENT_NET}
		}},
		{"short interval", func(d *heronv1.AgentDiagnostics) { d.ReportIntervalMs = 1 }},
		{"long interval", func(d *heronv1.AgentDiagnostics) { d.ReportIntervalMs = 60001 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := proto.Clone(good).(*heronv1.AgentDiagnostics)
			tc.change(d)
			if err := ValidateDiagnostics(d); err == nil {
				t.Fatalf("invalid diagnostics accepted: %v", d)
			}
		})
	}
	for _, epoch := range []string{"", strings.Repeat("a", 64)} {
		if err := ValidateCounterEpoch(epoch); err != nil {
			t.Fatal(err)
		}
	}
	for _, epoch := range []string{strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		if err := ValidateCounterEpoch(epoch); err == nil {
			t.Fatalf("invalid epoch accepted: %q", epoch)
		}
	}
}
