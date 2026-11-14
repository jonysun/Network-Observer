package server

import (
	"context"
	"testing"
	"time"

	networkobserverv1 "github.com/network-observer/network-observer/gen/networkobserver/v1"
	"github.com/network-observer/network-observer/internal/config"
	"github.com/network-observer/network-observer/internal/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestValidateAgentAuthModeRejectsMismatchBeforeRegistration(t *testing.T) {
	tests := []struct {
		name      string
		center    bool
		metadata  metadata.MD
		wantError bool
	}{
		{name: "matching secure mode", center: true, metadata: metadata.Pairs("x-network-observer-security-mode", "true")},
		{name: "matching insecure mode", center: false, metadata: metadata.Pairs("x-network-observer-security-mode", "false")},
		{name: "mode mismatch", center: true, metadata: metadata.Pairs("x-network-observer-security-mode", "false"), wantError: true},
		{name: "missing mode rejected", center: false, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{config: config.Config{AgentSecurityMode: test.center}}
			ctx := metadata.NewIncomingContext(context.Background(), test.metadata)
			err := server.validateAgentSecurityMode(ctx)
			if (err != nil) != test.wantError {
				t.Fatalf("validateAgentSecurityMode() error = %v, wantError %v", err, test.wantError)
			}
			if err != nil && status.Code(err) != codes.Unauthenticated {
				t.Fatalf("status code = %v, want Unauthenticated", status.Code(err))
			}
		})
	}
}

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
