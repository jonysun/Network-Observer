package server

import (
	"context"
	"encoding/json"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/network-observer/network-observer/internal/domain"
)

func centerAddresses(ctx context.Context, host string) []net.IP {
	addresses := make([]net.IP, 0, 8)
	host = strings.TrimSpace(host)
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	if parsed := net.ParseIP(strings.Trim(host, "[]")); parsed != nil {
		addresses = append(addresses, parsed)
	} else if host != "" {
		lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		resolved, err := net.DefaultResolver.LookupIP(lookupCtx, "ip", host)
		cancel()
		if err == nil {
			addresses = append(addresses, resolved...)
		}
	}
	if interfaces, err := net.Interfaces(); err == nil {
		for _, iface := range interfaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			ifaceAddresses, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, addr := range ifaceAddresses {
				ip, _, err := net.ParseCIDR(addr.String())
				if err == nil && !ip.IsLoopback() && !ip.IsUnspecified() && !ip.IsMulticast() {
					addresses = append(addresses, ip)
				}
			}
		}
	}
	unique := make([]net.IP, 0, len(addresses))
	seen := make(map[string]struct{}, len(addresses))
	for _, address := range addresses {
		key := address.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, address)
	}
	return unique
}

func topologyNetworkID(asset domain.LANDevice) string {
	for _, value := range []string{asset.NetworkPrefix, asset.NetworkID} {
		_, prefix, err := net.ParseCIDR(value)
		if err != nil {
			continue
		}
		bits, maxBits := prefix.Mask.Size()
		if bits < maxBits {
			return prefix.String()
		}
	}
	return asset.NetworkID
}

// resolveTopologyNetworkIDs folds host-route observations into the most-specific
// observed LAN prefix containing their address. This avoids drawing a /32 or /128
// as a separate LAN when another observation provides the actual subnet.
func resolveTopologyNetworkIDs(assets []domain.LANDevice) map[string]string {
	prefixes := make([]*net.IPNet, 0, len(assets))
	seen := make(map[string]struct{}, len(assets))
	for _, asset := range assets {
		id := topologyNetworkID(asset)
		_, prefix, err := net.ParseCIDR(id)
		if err != nil {
			continue
		}
		bits, maxBits := prefix.Mask.Size()
		if bits == maxBits || bits < 0 {
			continue
		}
		if _, ok := seen[prefix.String()]; ok {
			continue
		}
		seen[prefix.String()] = struct{}{}
		prefixes = append(prefixes, prefix)
	}

	resolved := make(map[string]string, len(assets))
	for _, asset := range assets {
		id := topologyNetworkID(asset)
		_, hostRoute, err := net.ParseCIDR(id)
		if err != nil {
			resolved[asset.Key] = id
			continue
		}
		bits, maxBits := hostRoute.Mask.Size()
		if bits != maxBits {
			resolved[asset.Key] = id
			continue
		}

		bestPrefix := ""
		ambiguous := false
		for _, address := range asset.Addresses {
			ip := net.ParseIP(address.IP)
			if ip == nil {
				continue
			}
			addressBits := -1
			addressPrefix := ""
			for _, prefix := range prefixes {
				prefixBits, _ := prefix.Mask.Size()
				if prefixBits <= addressBits || !prefix.Contains(ip) {
					continue
				}
				addressBits, addressPrefix = prefixBits, prefix.String()
			}
			if addressPrefix == "" {
				continue
			}
			if bestPrefix != "" && bestPrefix != addressPrefix {
				ambiguous = true
				break
			}
			bestPrefix = addressPrefix
		}
		if bestPrefix != "" && !ambiguous {
			id = bestPrefix
		}
		resolved[asset.Key] = id
	}
	return resolved
}

func buildLANTopology(assets []domain.LANDevice, devices []domain.Device, centerIPs []net.IP) domain.LANTopology {
	groups := make(map[string]*domain.LANTopologyGroup)
	agentsByNetwork := make(map[string]map[string]domain.LANTopologyAgent)
	assignedAgents := make(map[string]struct{})
	interfacesByAgent := make(map[string][]domain.LANDevice)
	sharedLocalEvidence := make(map[string]bool)
	networkIDs := resolveTopologyNetworkIDs(assets)
	center := &domain.LANTopologyCenter{ID: "center", Name: "Center"}
	centerAssigned := false

	for _, asset := range assets {
		for _, agentID := range asset.LocalAgentIDs {
			interfacesByAgent[agentID] = append(interfacesByAgent[agentID], asset)
		}
		networkID := networkIDs[asset.Key]
		if networkID == "" {
			continue
		}
		group := groups[networkID]
		if group == nil {
			group = &domain.LANTopologyGroup{
				ID: networkID, NetworkID: networkID, NetworkPrefix: networkID,
				Assets: make([]domain.LANDevice, 0), Agents: make([]domain.LANTopologyAgent, 0), Edges: make([]domain.LANTopologyEdge, 0),
			}
			groups[networkID] = group
		}
		if len(asset.LocalAgentIDs) == 0 {
			group.Assets = append(group.Assets, asset)
		}
		for _, observer := range asset.Observers {
			if len(asset.LocalAgentIDs) != 0 && !asset.Stale {
				ownedByObserver := false
				for _, owner := range asset.LocalAgentIDs {
					ownedByObserver = ownedByObserver || owner == observer.ID
				}
				if !ownedByObserver {
					sharedLocalEvidence[networkID] = true
				}
			}
			if agentsByNetwork[networkID] == nil {
				agentsByNetwork[networkID] = make(map[string]domain.LANTopologyAgent)
			}
			current, ok := agentsByNetwork[networkID][observer.ID]
			if !ok || observer.LastSeen.After(current.LastSeen) {
				agentsByNetwork[networkID][observer.ID] = domain.LANTopologyAgent{
					ID: observer.ID, Name: observer.Name, LastSeen: observer.LastSeen,
				}
			}
		}
	}

	devicesByID := make(map[string]domain.Device, len(devices))
	for _, device := range devices {
		devicesByID[device.ID] = device
	}

	// A subnet observed only through an Agent's own interface is an
	// implementation detail of that Agent, not an independently discovered
	// LAN. Keep it in Agent.Interfaces and avoid drawing a duplicate global
	// network node. Peer sightings of a local interface also establish a shared LAN.
	for networkID, group := range groups {
		if len(group.Assets) == 0 && !sharedLocalEvidence[networkID] {
			delete(groups, networkID)
			delete(agentsByNetwork, networkID)
		}
	}

	networkIDs := make([]string, 0, len(groups))
	for networkID := range groups {
		networkIDs = append(networkIDs, networkID)
	}
	sort.Strings(networkIDs)

	for _, networkID := range networkIDs {
		group := groups[networkID]
		if _, network, err := net.ParseCIDR(networkID); err == nil {
			for _, address := range centerIPs {
				if network.Contains(address) {
					copy := *center
					copy.Known = true
					group.Center = &copy
					centerAssigned = true
					group.Edges = append(group.Edges, domain.LANTopologyEdge{From: "center", To: "network:" + networkID, Kind: "hosts"})
					break
				}
			}
		}

		for id, observedAgent := range agentsByNetwork[networkID] {
			device, ok := devicesByID[id]
			if !ok {
				continue
			}
			observedAgent.Name = device.Name
			observedAgent.Type = device.Type
			observedAgent.DeviceCategory = device.DeviceCategory
			group.Agents = append(group.Agents, observedAgent)
			assignedAgents[id] = struct{}{}
		}
		sort.Slice(group.Agents, func(i, j int) bool {
			if group.Agents[i].Name != group.Agents[j].Name {
				return group.Agents[i].Name < group.Agents[j].Name
			}
			return group.Agents[i].ID < group.Agents[j].ID
		})
		for _, agent := range group.Agents {
			group.Edges = append(group.Edges, domain.LANTopologyEdge{From: "agent:" + agent.ID, To: "network:" + networkID, Kind: "observes"})
		}
		sort.Slice(group.Assets, func(i, j int) bool {
			if group.Assets[i].Name != group.Assets[j].Name {
				return group.Assets[i].Name < group.Assets[j].Name
			}
			return group.Assets[i].Key < group.Assets[j].Key
		})
		for _, asset := range group.Assets {
			group.Edges = append(group.Edges, domain.LANTopologyEdge{From: "asset:" + asset.Key, To: "network:" + networkID, Kind: "neighbor"})
		}
	}

	result := domain.LANTopology{
		Groups:     make([]domain.LANTopologyGroup, 0, len(groups)),
		Agents:     make([]domain.LANTopologyAgent, 0, len(devices)),
		Unassigned: domain.LANTopologyUnassigned{Agents: make([]domain.LANTopologyAgent, 0), Assets: make([]domain.LANDevice, 0)},
		Layout:     domain.LANTopologyLayout{Positions: make(map[string]domain.LANTopologyPoint), Infrastructure: []domain.LANTopologyInfrastructure{}, ConfirmedLinks: []domain.LANTopologyLink{}},
	}
	for _, networkID := range networkIDs {
		result.Groups = append(result.Groups, *groups[networkID])
	}
	result.Groups = splitTopologyGroupsByGateway(result.Groups)
	for index := range result.Groups {
		selectTopologyRoot(&result.Groups[index])
		result.Groups[index].Edges = buildTopologyGroupEdges(result.Groups[index])
	}
	for _, device := range devices {
		agent := domain.LANTopologyAgent{ID: device.ID, Name: device.Name, Type: device.Type, DeviceCategory: device.DeviceCategory, LastSeen: device.LastSeen, Interfaces: interfacesByAgent[device.ID], DockerNetworks: []domain.DockerNetwork{}}
		if agent.Interfaces == nil {
			agent.Interfaces = []domain.LANDevice{}
		}
		result.Agents = append(result.Agents, agent)
		if _, ok := assignedAgents[device.ID]; ok {
			continue
		}
		result.Unassigned.Agents = append(result.Unassigned.Agents, agent)
	}
	sort.Slice(result.Unassigned.Agents, func(i, j int) bool {
		if result.Unassigned.Agents[i].Name != result.Unassigned.Agents[j].Name {
			return result.Unassigned.Agents[i].Name < result.Unassigned.Agents[j].Name
		}
		return result.Unassigned.Agents[i].ID < result.Unassigned.Agents[j].ID
	})
	if !centerAssigned {
		center.Known = false
		result.Unassigned.Center = center
	}
	for _, asset := range assets {
		if asset.NetworkID == "" && len(asset.LocalAgentIDs) == 0 {
			result.Unassigned.Assets = append(result.Unassigned.Assets, asset)
		}
	}
	return result
}

func splitTopologyGroupsByGateway(groups []domain.LANTopologyGroup) []domain.LANTopologyGroup {
	result := make([]domain.LANTopologyGroup, 0, len(groups))
	for _, group := range groups {
		roots := make([]domain.LANDevice, 0)
		for _, asset := range group.Assets {
			if topologyRootPriority(asset) > 0 {
				roots = append(roots, asset)
			}
		}
		if len(roots) < 2 || !gatewayRootsAreSeparated(roots) || hasSharedGatewayObservers(group.Assets, roots) {
			// Multiple router/gateway labels on one observed CIDR are not
			// enough evidence of multiple LANs. Keep one shared group so a
			// manually labelled switch/router does not make the canvas invent
			// a second LAN. Only split when each root is observed by a disjoint
			// set of Agents.
			group.ID = group.NetworkID
			result = append(result, group)
			continue
		}

		rootObservers := make([]map[string]struct{}, len(roots))
		rootGroups := make([]domain.LANTopologyGroup, len(roots))
		for index, root := range roots {
			observers := make(map[string]struct{}, len(root.Observers))
			for _, observer := range root.Observers {
				observers[observer.ID] = struct{}{}
			}
			rootObservers[index] = observers
			rootGroups[index] = domain.LANTopologyGroup{
				ID: group.NetworkID + "|root|" + root.Key, NetworkID: group.NetworkID,
				NetworkPrefix: group.NetworkPrefix, RootDeviceKey: root.Key,
				Agents: make([]domain.LANTopologyAgent, 0), Assets: []domain.LANDevice{root}, Edges: make([]domain.LANTopologyEdge, 0),
			}
		}
		unresolved := group
		unresolved.ID = group.NetworkID + "|unresolved"
		unresolved.Center = nil
		unresolved.Agents = make([]domain.LANTopologyAgent, 0)
		unresolved.Assets = make([]domain.LANDevice, 0)
		unresolved.Edges = make([]domain.LANTopologyEdge, 0)
		for _, agent := range group.Agents {
			matches := matchingGatewayObservers(agent.ID, rootObservers)
			if len(matches) == 1 {
				rootGroups[matches[0]].Agents = append(rootGroups[matches[0]].Agents, agent)
			} else {
				unresolved.Agents = append(unresolved.Agents, agent)
			}
		}
		for _, asset := range group.Assets {
			if isGatewayRoot(asset, roots) {
				continue
			}
			matches := matchingGatewayObserversForAsset(asset, rootObservers)
			if len(matches) == 1 {
				rootGroups[matches[0]].Assets = append(rootGroups[matches[0]].Assets, asset)
			} else {
				unresolved.Assets = append(unresolved.Assets, asset)
			}
		}
		if group.Center != nil {
			unresolved.Center = group.Center
		}
		for _, rootGroup := range rootGroups {
			result = append(result, rootGroup)
		}
		if len(unresolved.Agents) != 0 || len(unresolved.Assets) != 0 || unresolved.Center != nil {
			result = append(result, unresolved)
		}
	}
	return result
}

// A manual category is authoritative. Within the same category priority,
// fresh default-route evidence can distinguish a root from another router.
func topologyRootPriority(asset domain.LANDevice) int {
	if asset.Stale {
		return 0
	}
	gateway := hasGatewayAddress(asset)
	manual := asset.CategorySource == "manual" || asset.CategorySource == "agent"
	if asset.DeviceCategory != "router" && asset.DeviceCategory != "gateway" {
		if !manual && gateway {
			return 1
		}
		return 0
	}
	priority := 1
	if manual {
		priority = 3
	}
	if gateway {
		priority++
	}
	return priority
}

func interfaceBelongsToTopologyNetwork(iface domain.LANDevice, networkID string) bool {
	if topologyNetworkID(iface) == networkID {
		return true
	}
	_, prefix, err := net.ParseCIDR(networkID)
	if err != nil {
		return false
	}
	for _, address := range iface.Addresses {
		if ip := net.ParseIP(address.IP); ip != nil && prefix.Contains(ip) {
			return true
		}
	}
	return false
}

func selectTopologyRoot(group *domain.LANTopologyGroup) {
	group.RootDeviceKey, group.RootAgentID = "", ""
	best := 0
	consider := func(priority int, deviceKey string) {
		if priority > best {
			best = priority
			group.RootDeviceKey = deviceKey
		} else if priority == best && priority > 0 {
			group.RootDeviceKey = ""
		}
	}
	for _, asset := range group.Assets {
		consider(topologyRootPriority(asset), asset.Key)
	}
	for _, agent := range group.Agents {
		if agent.DeviceCategory != "router" && agent.DeviceCategory != "gateway" {
			continue
		}
		priority := 3
		for _, iface := range agent.Interfaces {
			if interfaceBelongsToTopologyNetwork(iface, group.NetworkID) && !iface.Stale && hasGatewayAddress(iface) {
				priority = 4
				break
			}
		}
		if priority > best {
			best, group.RootDeviceKey, group.RootAgentID = priority, "", agent.ID
		} else if priority == best {
			group.RootDeviceKey, group.RootAgentID = "", ""
		}
	}
}
func hasSharedGatewayObservers(assets, roots []domain.LANDevice) bool {
	rootObservers := make([]map[string]struct{}, len(roots))
	for i, root := range roots {
		rootObservers[i] = make(map[string]struct{}, len(root.Observers))
		for _, observer := range root.Observers {
			rootObservers[i][observer.ID] = struct{}{}
		}
	}
	for _, asset := range assets {
		if !asset.Stale && len(matchingGatewayObserversForAsset(asset, rootObservers)) > 1 {
			return true
		}
	}
	return false
}

func buildTopologyGroupEdges(group domain.LANTopologyGroup) []domain.LANTopologyEdge {
	edges := make([]domain.LANTopologyEdge, 0, len(group.Agents)+len(group.Assets)+1)
	networkNode := "network:" + group.ID
	if group.Center != nil && group.Center.Known {
		edges = append(edges, domain.LANTopologyEdge{From: "center", To: networkNode, Kind: "hosts"})
	}
	for _, agent := range group.Agents {
		edges = append(edges, domain.LANTopologyEdge{From: "agent:" + agent.ID, To: networkNode, Kind: "observes"})
	}
	for _, asset := range group.Assets {
		edges = append(edges, domain.LANTopologyEdge{From: "asset:" + asset.Key, To: networkNode, Kind: "neighbor"})
	}
	return edges
}

func hasGatewayAddress(asset domain.LANDevice) bool {
	for _, address := range asset.Addresses {
		if address.State == "gateway" {
			return true
		}
	}
	return false
}

func isGatewayRoot(asset domain.LANDevice, roots []domain.LANDevice) bool {
	for _, root := range roots {
		if root.Key == asset.Key {
			return true
		}
	}
	return false
}

func matchingGatewayObservers(observerID string, rootObservers []map[string]struct{}) []int {
	matches := make([]int, 0, 1)
	for index, observers := range rootObservers {
		if _, ok := observers[observerID]; ok {
			matches = append(matches, index)
		}
	}
	return matches
}

func matchingGatewayObserversForAsset(asset domain.LANDevice, rootObservers []map[string]struct{}) []int {
	matched := make(map[int]struct{})
	for _, observer := range asset.Observers {
		for _, index := range matchingGatewayObservers(observer.ID, rootObservers) {
			matched[index] = struct{}{}
		}
	}
	result := make([]int, 0, len(matched))
	for index := range rootObservers {
		if _, ok := matched[index]; ok {
			result = append(result, index)
		}
	}
	return result
}

func gatewayRootsAreSeparated(roots []domain.LANDevice) bool {
	if len(roots) < 2 {
		return false
	}
	for i, root := range roots {
		if len(root.Observers) == 0 {
			return false
		}
		seen := make(map[string]struct{}, len(root.Observers))
		for _, observer := range root.Observers {
			seen[observer.ID] = struct{}{}
		}
		for _, other := range roots[i+1:] {
			for _, observer := range other.Observers {
				if _, ok := seen[observer.ID]; ok {
					return false
				}
			}
		}
	}
	return true
}

func attachDockerNetworks(topology *domain.LANTopology, networks []domain.DockerNetwork) {
	agents := make(map[string]int, len(topology.Agents))
	dockerOwnersBySubnet := make(map[string]map[string]struct{})
	for index := range topology.Agents {
		agents[topology.Agents[index].ID] = index
	}
	for _, network := range networks {
		index, ok := agents[network.OwnerAgentID]
		if !ok {
			continue
		}
		topology.Agents[index].DockerNetworks = append(topology.Agents[index].DockerNetworks, network)
		if network.LastSeen.After(topology.Agents[index].LastSeen) {
			topology.Agents[index].LastSeen = network.LastSeen
		}
		for _, subnet := range network.SubnetCIDRs {
			_, parsed, err := net.ParseCIDR(strings.TrimSpace(subnet))
			if err != nil {
				continue
			}
			subnet = parsed.String()
			if dockerOwnersBySubnet[subnet] == nil {
				dockerOwnersBySubnet[subnet] = make(map[string]struct{})
			}
			dockerOwnersBySubnet[subnet][network.OwnerAgentID] = struct{}{}
		}
	}
	for index := range topology.Agents {
		sort.Slice(topology.Agents[index].DockerNetworks, func(i, j int) bool {
			return topology.Agents[index].DockerNetworks[i].Name < topology.Agents[index].DockerNetworks[j].Name
		})
	}

	groups := topology.Groups[:0]
	for _, group := range topology.Groups {
		groupNetwork := group.NetworkID
		if _, parsed, err := net.ParseCIDR(strings.TrimSpace(groupNetwork)); err == nil {
			groupNetwork = parsed.String()
		}
		owners := dockerOwnersBySubnet[groupNetwork]
		if len(owners) == 0 {
			groups = append(groups, group)
			continue
		}
		filteredAgents := group.Agents[:0]
		for _, agent := range group.Agents {
			if _, isDockerOwner := owners[agent.ID]; !isDockerOwner {
				filteredAgents = append(filteredAgents, agent)
			}
		}
		group.Agents = filteredAgents
		filteredAssets := group.Assets[:0]
		for _, asset := range group.Assets {
			observedOutsideDockerHosts := false
			for _, observer := range asset.Observers {
				if _, isDockerOwner := owners[observer.ID]; !isDockerOwner {
					observedOutsideDockerHosts = true
					break
				}
			}
			if observedOutsideDockerHosts || len(asset.Observers) == 0 {
				filteredAssets = append(filteredAssets, asset)
			}
		}
		group.Assets = filteredAssets
		if _, removed := owners[group.RootAgentID]; removed {
			group.RootAgentID = ""
		}
		if group.RootDeviceKey != "" && !isGatewayRoot(domain.LANDevice{Key: group.RootDeviceKey}, group.Assets) {
			group.RootDeviceKey = ""
		}
		if len(group.Agents) != 0 || len(group.Assets) != 0 {
			group.Edges = filterTopologyGroupEdges(group)
			groups = append(groups, group)
		} else if group.Center != nil {
			center := *group.Center
			center.Known = false
			topology.Unassigned.Center = &center
		}
	}
	topology.Groups = groups
}

func filterTopologyGroupEdges(group domain.LANTopologyGroup) []domain.LANTopologyEdge {
	groupID := group.ID
	if groupID == "" {
		groupID = group.NetworkID
	}
	validNodes := map[string]struct{}{"network:" + groupID: {}}
	if group.Center != nil {
		validNodes["center"] = struct{}{}
	}
	for _, agent := range group.Agents {
		validNodes["agent:"+agent.ID] = struct{}{}
	}
	for _, asset := range group.Assets {
		validNodes["asset:"+asset.Key] = struct{}{}
	}
	edges := make([]domain.LANTopologyEdge, 0, len(group.Edges))
	for _, edge := range group.Edges {
		_, fromExists := validNodes[edge.From]
		_, toExists := validNodes[edge.To]
		if fromExists && toExists {
			edges = append(edges, edge)
		}
	}
	return edges
}

func (s *Server) currentLANTopology(ctx context.Context) (domain.LANTopology, error) {
	assets, err := s.store.ListLANDevices(ctx)
	if err != nil {
		return domain.LANTopology{}, err
	}
	devices, err := s.store.ListDevices(ctx)
	if err != nil {
		return domain.LANTopology{}, err
	}
	networks, err := s.store.ListDockerNetworks(ctx)
	if err != nil {
		return domain.LANTopology{}, err
	}
	topology := buildLANTopology(assets, devices, centerAddresses(ctx, s.config.TLSCertHost))
	attachDockerNetworks(&topology, networks)
	topology.Layout, err = s.store.LANTopologyLayout(ctx)
	if err != nil {
		return domain.LANTopology{}, err
	}
	topology.Settings, err = s.store.LANTopologySettings(ctx)
	if err != nil {
		return domain.LANTopology{}, err
	}
	return topology, nil
}

func (s *Server) captureLANTopologySnapshot(ctx context.Context) error {
	topology, err := s.currentLANTopology(ctx)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(topology)
	if err != nil {
		return err
	}
	_, err = s.store.CreateLANTopologySnapshotIfDue(ctx, raw)
	return err
}
