package server

import (
	"net"
	"testing"
	"time"

	"github.com/network-observer/network-observer/internal/domain"
)

func TestBuildLANTopologyMergesObservedSameSubnetAcrossNetworkIDs(t *testing.T) {
	assets := []domain.LANDevice{
		{Key: "agent-a|nas", NetworkID: "192.168.31.100/32", NetworkPrefix: "192.168.31.0/24", Name: "NAS", Observers: []domain.LANObserver{{ID: "agent-a"}}},
		{Key: "agent-b|pc", NetworkID: "192.168.31.0/24", NetworkPrefix: "192.168.31.0/24", Name: "PC", Observers: []domain.LANObserver{{ID: "agent-b"}}},
	}
	devices := []domain.Device{{ID: "agent-a", Name: "Agent A"}, {ID: "agent-b", Name: "Agent B"}}
	result := buildLANTopology(assets, devices, []net.IP{net.ParseIP("192.168.31.176")})
	if len(result.Groups) != 1 {
		t.Fatalf("observations with the same confirmed subnet should form one group, got %d: %+v", len(result.Groups), result.Groups)
	}
	group := result.Groups[0]
	if group.NetworkID != "192.168.31.0/24" || len(group.Assets) != 2 || len(group.Agents) != 2 || group.Center == nil || !group.Center.Known {
		t.Fatalf("same-subnet assets, agents and center should be unified: %+v", group)
	}
}

func TestBuildLANTopologyFoldsHostRouteIntoObservedSubnet(t *testing.T) {
	assets := []domain.LANDevice{
		{Key: "host-route|nas", NetworkID: "192.168.31.100/32", Name: "NAS", Addresses: []domain.LANAddress{{IP: "192.168.31.100"}}},
		{Key: "subnet|pc", NetworkID: "192.168.31.0/24", Name: "PC", Addresses: []domain.LANAddress{{IP: "192.168.31.20"}}},
	}
	result := buildLANTopology(assets, nil, nil)
	if len(result.Groups) != 1 {
		t.Fatalf("host-route address within a known subnet should not form another LAN group: %+v", result.Groups)
	}
	if result.Groups[0].NetworkID != "192.168.31.0/24" || len(result.Groups[0].Assets) != 2 {
		t.Fatalf("host-route asset should join the observed subnet: %+v", result.Groups[0])
	}
}

func TestBuildLANTopologyKeepsMultiSubnetHostRouteUnresolved(t *testing.T) {
	assets := []domain.LANDevice{
		{Key: "multi-subnet-host", NetworkID: "192.168.31.100/32", Name: "Multi-NIC", Addresses: []domain.LANAddress{{IP: "192.168.31.100"}, {IP: "10.0.0.100"}}},
		{Key: "subnet-a", NetworkID: "192.168.31.0/24", Name: "Subnet A", Addresses: []domain.LANAddress{{IP: "192.168.31.20"}}},
		{Key: "subnet-b", NetworkID: "10.0.0.0/24", Name: "Subnet B", Addresses: []domain.LANAddress{{IP: "10.0.0.20"}}},
	}
	result := buildLANTopology(assets, nil, nil)
	if len(result.Groups) != 3 {
		t.Fatalf("a multi-subnet host route must not be assigned to one guessed LAN: %+v", result.Groups)
	}
}

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

func TestBuildLANTopologyNestsLocalInterfacesUnderAgent(t *testing.T) {
	seen := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	assets := []domain.LANDevice{
		{Key: "lan-a|aa", NetworkID: "192.168.1.0/24", Name: "Agent eth0", LocalAgentIDs: []string{"agent-a"}, Observers: []domain.LANObserver{{ID: "agent-a", LastSeen: seen}}},
		{Key: "lan-a|bb", NetworkID: "192.168.1.0/24", Name: "NAS"},
	}
	devices := []domain.Device{{ID: "agent-a", Name: "Host", Type: domain.ObserverEndpoint}}
	result := buildLANTopology(assets, devices, nil)
	if len(result.Agents) != 1 || len(result.Agents[0].Interfaces) != 1 || result.Agents[0].Interfaces[0].Key != "lan-a|aa" {
		t.Fatalf("local interfaces should be nested under their Agent: %+v", result.Agents)
	}
	if len(result.Groups) != 1 || len(result.Groups[0].Assets) != 1 || result.Groups[0].Assets[0].Name != "NAS" {
		t.Fatalf("local Agent interface should not appear as a separate LAN asset: %+v", result.Groups)
	}
}

func TestBuildLANTopologyNestsLocalInterfacesWithoutNetwork(t *testing.T) {
	asset := domain.LANDevice{Key: "local|aa", Name: "Agent interface", LocalAgentIDs: []string{"agent-a"}}
	devices := []domain.Device{{ID: "agent-a", Name: "Host", Type: domain.ObserverEndpoint}}
	result := buildLANTopology([]domain.LANDevice{asset}, devices, nil)
	if len(result.Agents) != 1 || len(result.Agents[0].Interfaces) != 1 || result.Agents[0].Interfaces[0].Key != asset.Key {
		t.Fatalf("local interface without a subnet should remain under its Agent: %+v", result.Agents)
	}
	if len(result.Unassigned.Assets) != 0 {
		t.Fatalf("local interface should not be repeated as an unassigned asset: %+v", result.Unassigned.Assets)
	}
}

func TestBuildLANTopologyHidesAgentOnlySubnetFromGlobalCanvas(t *testing.T) {
	assets := []domain.LANDevice{{Key: "local|eth0", NetworkID: "192.168.1.0/24", Name: "Agent eth0", LocalAgentIDs: []string{"agent-a"}, Observers: []domain.LANObserver{{ID: "agent-a"}}}}
	result := buildLANTopology(assets, []domain.Device{{ID: "agent-a", Name: "Linux Agent"}}, nil)
	if len(result.Groups) != 0 {
		t.Fatalf("Agent-only subnet must not be duplicated as a global LAN group: %+v", result.Groups)
	}
	if len(result.Agents) != 1 || len(result.Agents[0].Interfaces) != 1 {
		t.Fatalf("Agent interface should remain nested under the Agent: %+v", result.Agents)
	}
}

func TestBuildLANTopologyUsesManualGatewayAsRoot(t *testing.T) {
	assets := []domain.LANDevice{
		{Key: "lan|router", NetworkID: "192.168.1.0/24", Name: "家庭路由器", DeviceCategory: "router", CategorySource: "manual", Observers: []domain.LANObserver{{ID: "agent-a"}}},
		{Key: "lan|nas", NetworkID: "192.168.1.0/24", Name: "NAS", Observers: []domain.LANObserver{{ID: "agent-a"}}},
	}
	result := buildLANTopology(assets, []domain.Device{{ID: "agent-a", Name: "Linux Agent"}}, nil)
	if len(result.Groups) != 1 || result.Groups[0].RootDeviceKey != "lan|router" {
		t.Fatalf("manual router should become the LAN root: %+v", result.Groups)
	}
	if result.Groups[0].ID != result.Groups[0].NetworkID {
		t.Fatalf("unsplit group should retain its identity when its root changes: %+v", result.Groups[0])
	}
}

func TestBuildLANTopologyDoesNotSplitOverlappingGatewayLabels(t *testing.T) {
	assets := []domain.LANDevice{
		{Key: "lan|router-a", NetworkID: "192.168.1.0/24", Name: "Router A", DeviceCategory: "router", CategorySource: "manual", Observers: []domain.LANObserver{{ID: "agent-a"}}},
		{Key: "lan|router-b", NetworkID: "192.168.1.0/24", Name: "Router B", DeviceCategory: "gateway", CategorySource: "manual", Observers: []domain.LANObserver{{ID: "agent-a"}}},
	}
	result := buildLANTopology(assets, []domain.Device{{ID: "agent-a", Name: "Agent"}}, nil)
	if len(result.Groups) != 1 || len(result.Groups[0].Assets) != 2 {
		t.Fatalf("overlapping gateway observations should remain one shared LAN: %+v", result.Groups)
	}
}

func TestBuildLANTopologySplitsDisjointGatewayObservations(t *testing.T) {
	assets := []domain.LANDevice{
		{Key: "lan|router-a", NetworkID: "192.168.1.0/24", Name: "Router A", DeviceCategory: "router", CategorySource: "manual", Observers: []domain.LANObserver{{ID: "agent-a"}}},
		{Key: "lan|router-b", NetworkID: "192.168.1.0/24", Name: "Router B", DeviceCategory: "router", CategorySource: "manual", Observers: []domain.LANObserver{{ID: "agent-b"}}},
		{Key: "lan|nas", NetworkID: "192.168.1.0/24", Name: "NAS", Observers: []domain.LANObserver{{ID: "agent-a"}}},
	}
	result := buildLANTopology(assets, []domain.Device{{ID: "agent-a", Name: "Agent A"}, {ID: "agent-b", Name: "Agent B"}}, nil)
	if len(result.Groups) != 2 {
		t.Fatalf("disjoint gateway observers should create two logical LAN views: %+v", result.Groups)
	}
	if result.Groups[0].ID == result.Groups[1].ID || result.Groups[0].NetworkID != result.Groups[1].NetworkID {
		t.Fatalf("split groups need unique ids while retaining the source CIDR: %+v", result.Groups)
	}
}

func TestAttachDockerNetworksScopesNetworksToOwnerAgent(t *testing.T) {
	topology := domain.LANTopology{Agents: []domain.LANTopologyAgent{
		{ID: "agent-a", DockerNetworks: []domain.DockerNetwork{}},
		{ID: "agent-b", DockerNetworks: []domain.DockerNetwork{}},
	}}
	attachDockerNetworks(&topology, []domain.DockerNetwork{
		{OwnerAgentID: "agent-a", ID: "network-a", Name: "bridge", SubnetCIDRs: []string{"172.17.0.0/16"}},
		{OwnerAgentID: "agent-b", ID: "network-b", Name: "bridge", SubnetCIDRs: []string{"172.17.0.0/16"}},
	})
	if len(topology.Agents[0].DockerNetworks) != 1 || len(topology.Agents[1].DockerNetworks) != 1 || topology.Agents[0].DockerNetworks[0].ID == topology.Agents[1].DockerNetworks[0].ID {
		t.Fatalf("same Docker CIDR/name must remain scoped to its Agent: %+v", topology.Agents)
	}
}

func TestAttachDockerNetworksRemovesDockerSubnetFromSharedLANGroups(t *testing.T) {
	topology := domain.LANTopology{Groups: []domain.LANTopologyGroup{
		{
			NetworkID: "172.17.8.1/16",
			Center:    &domain.LANTopologyCenter{ID: "center", Name: "Center", Known: true},
			Agents:    []domain.LANTopologyAgent{{ID: "agent-linux"}, {ID: "agent-nas"}},
			Assets:    []domain.LANDevice{{Key: "docker-host", Observers: []domain.LANObserver{{ID: "agent-linux"}}}},
			Edges: []domain.LANTopologyEdge{
				{From: "center", To: "network:172.17.8.1/16", Kind: "hosts"},
				{From: "agent:agent-linux", To: "network:172.17.8.1/16", Kind: "observes"},
				{From: "agent:agent-nas", To: "network:172.17.8.1/16", Kind: "observes"},
				{From: "asset:docker-host", To: "network:172.17.8.1/16", Kind: "neighbor"},
			},
		},
		{
			NetworkID: "192.168.1.0/24",
			Agents:    []domain.LANTopologyAgent{{ID: "agent-linux"}, {ID: "agent-other"}},
			Assets:    []domain.LANDevice{},
			Edges: []domain.LANTopologyEdge{
				{From: "agent:agent-linux", To: "network:192.168.1.0/24", Kind: "observes"},
				{From: "agent:agent-other", To: "network:192.168.1.0/24", Kind: "observes"},
				{From: "agent:removed", To: "network:192.168.1.0/24", Kind: "observes"},
			},
		},
	}, Agents: []domain.LANTopologyAgent{{ID: "agent-linux"}, {ID: "agent-nas"}}}
	attachDockerNetworks(&topology, []domain.DockerNetwork{
		{OwnerAgentID: "agent-linux", ID: "linux-bridge", SubnetCIDRs: []string{"172.17.0.9/16"}},
		{OwnerAgentID: "agent-nas", ID: "nas-bridge", SubnetCIDRs: []string{"172.17.0.0/16"}},
		{OwnerAgentID: "agent-linux", ID: "linux-other", SubnetCIDRs: []string{"192.168.1.8/24"}},
	})
	if len(topology.Groups) != 1 || topology.Groups[0].NetworkID != "192.168.1.0/24" {
		t.Fatalf("Docker-only subnet should not remain as a shared LAN group: %+v", topology.Groups)
	}
	if len(topology.Groups[0].Edges) != 1 || topology.Groups[0].Edges[0].From != "agent:agent-other" {
		t.Fatalf("edges to removed nodes should be removed with them: %+v", topology.Groups[0].Edges)
	}
	if topology.Unassigned.Center == nil || topology.Unassigned.Center.Known {
		t.Fatalf("center matched only to the filtered Docker subnet should become unassigned: %+v", topology.Unassigned.Center)
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

func TestBuildLANTopologyUsesRouterAgentWithoutDuplicatingItsInterface(t *testing.T) {
	const network = "192.168.1.0/24"
	assets := []domain.LANDevice{
		{Key: "router-iface", NetworkID: network, LocalAgentIDs: []string{"router"}, Observers: []domain.LANObserver{{ID: "router"}, {ID: "nas"}}},
		{Key: "nas-iface", NetworkID: network, LocalAgentIDs: []string{"nas"}, Observers: []domain.LANObserver{{ID: "nas"}}},
	}
	devices := []domain.Device{{ID: "router", DeviceCategory: "router"}, {ID: "nas", DeviceCategory: "nas"}}
	result := buildLANTopology(assets, devices, nil)
	if len(result.Groups) != 1 || result.Groups[0].RootAgentID != "router" || result.Groups[0].RootDeviceKey != "" || len(result.Groups[0].Assets) != 0 || len(result.Groups[0].Agents) != 2 {
		t.Fatalf("a gateway-classified Agent should represent the real router without duplicating its interface: %+v", result.Groups)
	}
}

func TestBuildLANTopologyPrefersManualRouterAndHonorsManualSwitch(t *testing.T) {
	const network = "192.168.1.0/24"
	assets := []domain.LANDevice{
		{Key: "gateway", NetworkID: network, DeviceCategory: "gateway", CategorySource: "detected", Addresses: []domain.LANAddress{{State: "gateway"}}, Observers: []domain.LANObserver{{ID: "agent"}}},
		{Key: "manual", NetworkID: network, DeviceCategory: "router", CategorySource: "manual", Observers: []domain.LANObserver{{ID: "agent"}}},
	}
	devices := []domain.Device{{ID: "agent"}}
	result := buildLANTopology(assets, devices, nil)
	if len(result.Groups) != 1 || result.Groups[0].RootDeviceKey != "manual" {
		t.Fatalf("manual router should take precedence over the detected gateway: %+v", result.Groups)
	}
	assets[1].DeviceCategory = "switch"
	assets[1].Addresses = []domain.LANAddress{{State: "gateway"}}
	result = buildLANTopology(assets, devices, nil)
	if len(result.Groups) != 1 || result.Groups[0].RootDeviceKey != "gateway" {
		t.Fatalf("explicit switch category should override earlier gateway evidence: %+v", result.Groups)
	}
	assets[0].Stale = true
	result = buildLANTopology(assets, devices, nil)
	if len(result.Groups) != 1 || result.Groups[0].RootDeviceKey != "" {
		t.Fatalf("stale gateway should not be selected as a live root: %+v", result.Groups)
	}
}

func TestBuildLANTopologyDoesNotSplitWhenGatewaysShareAnObservedDevice(t *testing.T) {
	const network = "192.168.1.0/24"
	assets := []domain.LANDevice{
		{Key: "gateway-a", NetworkID: network, DeviceCategory: "gateway", Observers: []domain.LANObserver{{ID: "agent-a"}}},
		{Key: "gateway-b", NetworkID: network, DeviceCategory: "gateway", Observers: []domain.LANObserver{{ID: "agent-b"}}},
		{Key: "switch", NetworkID: network, DeviceCategory: "switch", Observers: []domain.LANObserver{{ID: "agent-a"}, {ID: "agent-b"}}},
	}
	result := buildLANTopology(assets, nil, nil)
	if len(result.Groups) != 1 || result.Groups[0].RootDeviceKey != "" {
		t.Fatalf("a shared device should keep the LAN together, with no arbitrary root: %+v", result.Groups)
	}
}
