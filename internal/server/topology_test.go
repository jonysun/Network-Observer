package server

import (
	"net"
	"testing"
	"time"

	"github.com/network-observer/network-observer/internal/domain"
)

func TestBuildLANTopologyMergesAgentsAndAssignsCenterOnce(t *testing.T) {
	seen := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	assets := []domain.LANDevice{
		{Key: "net-a|aa", NetworkID: "192.168.31.0/24", Name: "NAS", Observers: []domain.LANObserver{{ID: "agent-a", LastSeen: seen}, {ID: "agent-b", LastSeen: seen.Add(time.Minute)}}},
		{Key: "net-b|bb", NetworkID: "10.0.0.0/24", Name: "Laptop", Observers: []domain.LANObserver{{ID: "agent-b", LastSeen: seen}}},
	}
	devices := []domain.Device{
		{ID: "agent-a", Name: "Endpoint", Type: domain.ObserverEndpoint, DeviceCategory: "computer"},
		{ID: "agent-b", Name: "Gateway", Type: domain.ObserverGateway, DeviceCategory: "router"},
		{ID: "agent-c", Name: "Unassigned", Type: domain.ObserverEndpoint},
	}
	centerIP := net.ParseIP("192.168.31.176")
	result := buildLANTopology(assets, devices, []net.IP{centerIP})

	if len(result.Groups) != 2 {
		t.Fatalf("expected two per-network groups, got %d", len(result.Groups))
	}
	if result.Groups[0].NetworkID != "10.0.0.0/24" || result.Groups[1].NetworkID != "192.168.31.0/24" {
		t.Fatalf("groups are not sorted by network: %+v", result.Groups)
	}
	shared := result.Groups[1]
	if len(shared.Agents) != 2 || len(shared.Assets) != 1 {
		t.Fatalf("same-network observations were not merged: agents=%d assets=%d", len(shared.Agents), len(shared.Assets))
	}
	if shared.Center == nil || !shared.Center.Known {
		t.Fatalf("expected center assigned to matching subnet: %+v", shared.Center)
	}
	if result.Groups[0].Center != nil {
		t.Fatalf("center should not appear in unmatched subnet: %+v", result.Groups[0].Center)
	}
	if len(result.Unassigned.Agents) != 1 || result.Unassigned.Agents[0].ID != "agent-c" {
		t.Fatalf("unexpected unassigned agents: %+v", result.Unassigned.Agents)
	}
	if result.Unassigned.Center != nil {
		t.Fatalf("center was assigned and must not also be unassigned: %+v", result.Unassigned.Center)
	}
}

func TestBuildLANTopologyMarksCenterUnknownOnlyOnce(t *testing.T) {
	assets := []domain.LANDevice{
		{Key: "a", NetworkID: "192.168.1.0/24", Name: "A"},
		{Key: "b", NetworkID: "10.0.0.0/24", Name: "B"},
	}
	result := buildLANTopology(assets, nil, []net.IP{net.ParseIP("172.16.0.2")})
	if result.Unassigned.Center == nil || result.Unassigned.Center.Known {
		t.Fatalf("expected one unassigned center: %+v", result.Unassigned.Center)
	}
	for _, group := range result.Groups {
		if group.Center != nil {
			t.Fatalf("center must not be repeated in unknown networks: %+v", group)
		}
	}
}

func TestCenterAddressesIncludeContainerInterfaces(t *testing.T) {
	addresses := centerAddresses(t.Context(), "observer.example.lan:8443")
	if len(addresses) == 0 {
		t.Fatal("expected at least one non-loopback host interface address")
	}
	for _, address := range addresses {
		if address.IsLoopback() || address.IsUnspecified() || address.IsMulticast() {
			t.Fatalf("unexpected non-unicast center address: %s", address)
		}
	}
}
