package server

import (
	"context"
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

func buildLANTopology(assets []domain.LANDevice, devices []domain.Device, centerIPs []net.IP) domain.LANTopology {
	groups := make(map[string]*domain.LANTopologyGroup)
	agentsByNetwork := make(map[string]map[string]domain.LANTopologyAgent)
	assignedAgents := make(map[string]struct{})
	center := &domain.LANTopologyCenter{ID: "center", Name: "Center"}
	centerAssigned := false

	for _, asset := range assets {
		if asset.NetworkID == "" {
			continue
		}
		group := groups[asset.NetworkID]
		if group == nil {
			group = &domain.LANTopologyGroup{
				NetworkID: asset.NetworkID, NetworkPrefix: asset.NetworkID,
				Assets: make([]domain.LANDevice, 0), Agents: make([]domain.LANTopologyAgent, 0), Edges: make([]domain.LANTopologyEdge, 0),
			}
			groups[asset.NetworkID] = group
		}
		group.Assets = append(group.Assets, asset)
		for _, observer := range asset.Observers {
			if agentsByNetwork[asset.NetworkID] == nil {
				agentsByNetwork[asset.NetworkID] = make(map[string]domain.LANTopologyAgent)
			}
			current, ok := agentsByNetwork[asset.NetworkID][observer.ID]
			if !ok || observer.LastSeen.After(current.LastSeen) {
				agentsByNetwork[asset.NetworkID][observer.ID] = domain.LANTopologyAgent{
					ID: observer.ID, Name: observer.Name, LastSeen: observer.LastSeen,
				}
			}
		}
	}

	devicesByID := make(map[string]domain.Device, len(devices))
	for _, device := range devices {
		devicesByID[device.ID] = device
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
		Unassigned: domain.LANTopologyUnassigned{Agents: make([]domain.LANTopologyAgent, 0), Assets: make([]domain.LANDevice, 0)},
	}
	for _, networkID := range networkIDs {
		result.Groups = append(result.Groups, *groups[networkID])
	}
	for _, device := range devices {
		if _, ok := assignedAgents[device.ID]; ok {
			continue
		}
		result.Unassigned.Agents = append(result.Unassigned.Agents, domain.LANTopologyAgent{
			ID: device.ID, Name: device.Name, Type: device.Type, DeviceCategory: device.DeviceCategory, LastSeen: device.LastSeen,
		})
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
		if asset.NetworkID == "" {
			result.Unassigned.Assets = append(result.Unassigned.Assets, asset)
		}
	}
	return result
}
