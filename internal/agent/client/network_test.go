package client

import (
	"context"
	"errors"
	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"google.golang.org/protobuf/proto"
	"testing"
	"time"
)

type networkSnapshot struct{ value *heronv1.NetworkInfo }

func (n *networkSnapshot) Snapshot() *heronv1.NetworkInfo {
	return proto.Clone(n.value).(*heronv1.NetworkInfo)
}

func TestNetworkChangesReconcileWithoutRestart(t *testing.T) {
	hub := &fakeHub{interval: 5000, reconcile: true}
	r, _ := newRunner(t, hub)
	n := &networkSnapshot{value: &heronv1.NetworkInfo{Ipv4: &heronv1.AddressDetection{State: 1, Address: "8.8.4.4", CheckedAt: 100}}}
	r.Network = n
	round := 0
	r.Sleep = func(context.Context, time.Duration) error {
		round++
		if round == 1 {
			n.value.Ipv4 = &heronv1.AddressDetection{State: 3, CheckedAt: 200}
		}
		if round == 3 {
			return context.Canceled
		}
		return nil
	}
	if err := r.Run(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	reports := hub.received()
	if reports[0].Facts.GetNetwork().GetIpv4().GetAddress() != "8.8.4.4" {
		t.Fatalf("initial=%v", reports[0])
	}
	if reports[1].FactsHash == reports[0].FactsHash || reports[1].Facts != nil {
		t.Fatal("network change did not use hash reconciliation")
	}
	if got := reports[2].Facts.GetNetwork().GetIpv4(); got.GetState() != 3 || got.GetAddress() != "" || got.GetCheckedAt() != 200 {
		t.Fatalf("requested network=%v", got)
	}
}
