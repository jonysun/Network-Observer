package server

import (
	"testing"
	"time"

	networkobserverv1 "github.com/network-observer/network-observer/gen/networkobserver/v1"
	"github.com/network-observer/network-observer/internal/domain"
)

func TestConvertObservationsPreservesIPv6AndFlowMetadata(t *testing.T) {
	at := time.Now().UTC()
	input := []*networkobserverv1.FlowObservation{{
		Id: "obs-1", FlowId: "flow-1", DeviceId: "device-1", ObserverType: "gateway",
		ObservedAtUnixNanos: at.UnixNano(), Protocol: "udp", SourceIp: "2001:db8::1", SourcePort: 53,
		DestinationIp: "2001:db8::2", DestinationPort: 5353, NatDestinationIp: "2001:db8::3",
		NatDestinationPort: 5353, SamplingRate: 100, Confidence: "estimated", Active: true,
	}}

	observations, err := convertObservations("device-1", input)
	if err != nil {
		t.Fatal(err)
	}
	got := observations[0]
	if got.SourceIP != "2001:db8::1" || got.NATDestinationIP != "2001:db8::3" || got.Protocol != domain.ProtocolUDP {
		t.Fatalf("network tuple was not preserved: %+v", got)
	}
	if got.SamplingRate != 100 || got.Confidence != "estimated" || !got.Active {
		t.Fatalf("sampling and lifecycle metadata were not preserved: %+v", got)
	}
}

func TestConvertObservationsRejectsInvalidPortsAndUnmarkedSampling(t *testing.T) {
	tests := []struct {
		name string
		item *networkobserverv1.FlowObservation
	}{
		{name: "port overflow", item: &networkobserverv1.FlowObservation{ObservedAtUnixNanos: 1, SourcePort: 65536}},
		{name: "sampling not marked estimated", item: &networkobserverv1.FlowObservation{ObservedAtUnixNanos: 1, SamplingRate: 2, Confidence: "exact"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := convertObservations("device-1", []*networkobserverv1.FlowObservation{test.item}); err == nil {
				t.Fatal("expected invalid observation to be rejected")
			}
		})
	}
}
