package server

import (
	"strings"
	"testing"
	"time"

	networkobserverv1 "github.com/network-observer/network-observer/gen/networkobserver/v1"
	"github.com/network-observer/network-observer/internal/domain"
)

func TestConvertObservationCoverageAndLegacy(t *testing.T) {
	at := time.Now().UTC()
	for _, seconds := range []int64{0, 10, 120, -1, 121} {
		item := &networkobserverv1.FlowObservation{Id: "obs", FlowId: "flow", ObservedAtUnixNanos: at.UnixNano()}
		if seconds != 0 {
			item.CoverageStartUnixNanos = at.Add(-time.Duration(seconds) * time.Second).UnixNano()
		}
		got, err := convertObservations("device", []*networkobserverv1.FlowObservation{item})
		if seconds < 0 || seconds > 120 {
			if err == nil {
				t.Fatalf("accepted invalid coverage: %ds", seconds)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if seconds == 0 && !got[0].CoverageStart.IsZero() {
			t.Fatal("legacy observation acquired inferred coverage")
		}
		if seconds != 0 && !got[0].CoverageStart.Equal(at.Add(-time.Duration(seconds)*time.Second)) {
			t.Fatal("coverage did not survive conversion")
		}
	}
}

func TestConvertCapabilitiesSeparatesHistoricalGapFromCurrentPressure(t *testing.T) {
	got := convertCapabilities([]*networkobserverv1.Capability{
		{Name: "data_gap", Status: "degraded", Detail: "8741877 observations skipped"},
		{Name: "local_wal", Status: "degraded", Detail: "WAL capacity pressure"},
	})
	if len(got) != 2 {
		t.Fatalf("got %d capabilities, want 2", len(got))
	}
	if got[0].Status != "historical" || !strings.HasPrefix(got[0].Detail, "historical gap:") {
		t.Fatalf("legacy data gap was not normalized: %+v", got[0])
	}
	if got[1].Status != "degraded" || got[1].Detail != "WAL capacity pressure" {
		t.Fatalf("current WAL pressure was changed: %+v", got[1])
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

func TestConvertNeighborObservationsPreservesDualStackScope(t *testing.T) {
	at := time.Now().UTC()
	items, err := convertNeighborObservations("device-1", []*networkobserverv1.NeighborObservation{{
		NetworkId: "192.168.31.0/24", NetworkPrefix: "2001:db8:31::123/64", Ip: "2001:db8:31::123",
		Mac: "AA:BB:CC:DD:EE:FF", Interface: "eth0", State: "reachable", IsLocal: true,
	}}, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ObserverDeviceID != "device-1" || items[0].NetworkID != "192.168.31.0/24" || items[0].NetworkPrefix != "2001:db8:31::/64" || items[0].MAC != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("unexpected normalized neighbor: %+v", items)
	}
	if !items[0].LastSeen.Equal(at) {
		t.Fatalf("last_seen = %s, want %s", items[0].LastSeen, at)
	}
}

func TestConvertNeighborObservationsRejectsInvalidIdentity(t *testing.T) {
	tests := []*networkobserverv1.NeighborObservation{
		{Ip: "192.168.31.2", Mac: "aa:bb:cc:dd:ee:ff"},
		{NetworkId: "192.168.31.0/24", Ip: "not-an-ip"},
		{NetworkId: "192.168.31.0/24", NetworkPrefix: "bad", Ip: "192.168.31.2"},
		{NetworkId: "192.168.31.0/24", Ip: "192.168.31.2", Mac: "bad-mac"},
	}
	for _, item := range tests {
		if _, err := convertNeighborObservations("device-1", []*networkobserverv1.NeighborObservation{item}, time.Now()); err == nil {
			t.Fatalf("expected invalid observation to be rejected: %+v", item)
		}
	}
}
