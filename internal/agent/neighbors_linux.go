//go:build linux

package agent

import (
	"context"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"

	networkobserverv1 "github.com/network-observer/network-observer/gen/networkobserver/v1"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func (c *endpointCollector) NeighborSnapshot(ctx context.Context) ([]*networkobserverv1.NeighborObservation, *networkobserverv1.Capability) {
	return collectLinuxNeighbors(ctx)
}

func (c *gatewayCollector) NeighborSnapshot(ctx context.Context) ([]*networkobserverv1.NeighborObservation, *networkobserverv1.Capability) {
	return collectLinuxNeighbors(ctx)
}

func collectLinuxNeighbors(ctx context.Context) ([]*networkobserverv1.NeighborObservation, *networkobserverv1.Capability) {
	if err := ctx.Err(); err != nil {
		return nil, capability("lan_neighbors", "degraded", err.Error())
	}
	links, err := netlink.LinkList()
	if err != nil {
		return nil, capability("lan_neighbors", "unavailable", fmt.Sprintf("list Linux interfaces: %v", err))
	}
	items := make(map[string]*networkobserverv1.NeighborObservation)
	var details []string
	networkIDs := make(map[string]string)
	networkPrefixes := make(map[string][]string)
	neighborCount := 0
	needProcFallback := false
	for _, link := range links {
		if err := ctx.Err(); err != nil {
			return nil, capability("lan_neighbors", "degraded", err.Error())
		}
		attrs := link.Attrs()
		if attrs == nil || attrs.Name == "" || attrs.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, addrErr := netlink.AddrList(link, netlink.FAMILY_ALL)
		if addrErr != nil {
			details = append(details, fmt.Sprintf("%s addresses: %v", attrs.Name, addrErr))
			needProcFallback = true
		}
		prefixes := make([]string, 0, len(addrs))
		type localAddress struct{ ip, prefix string }
		localAddresses := make([]localAddress, 0, len(addrs))
		for _, address := range addrs {
			if address.IPNet == nil || address.IP == nil || address.IP.IsLoopback() || !address.IP.IsGlobalUnicast() {
				continue
			}
			_, prefix, parseErr := net.ParseCIDR(address.IPNet.String())
			if parseErr != nil {
				continue
			}
			prefixID := prefix.String()
			prefixes = append(prefixes, prefixID)
			localAddresses = append(localAddresses, localAddress{ip: address.IP.String(), prefix: prefixID})
		}
		if len(localAddresses) == 0 {
			if iface, fallbackErr := net.InterfaceByName(attrs.Name); fallbackErr == nil {
				rawAddresses, addrsFallbackErr := iface.Addrs()
				if addrsFallbackErr != nil {
					details = append(details, fmt.Sprintf("%s addresses fallback: %v", attrs.Name, addrsFallbackErr))
					needProcFallback = true
				} else {
					for _, rawAddress := range rawAddresses {
						ip, prefix, parseErr := net.ParseCIDR(rawAddress.String())
						if parseErr != nil || ip.IsLoopback() || !ip.IsGlobalUnicast() {
							continue
						}
						prefixID := prefix.String()
						prefixes = append(prefixes, prefixID)
						localAddresses = append(localAddresses, localAddress{ip: ip.String(), prefix: prefixID})
					}
				}
			} else {
				details = append(details, fmt.Sprintf("%s addresses fallback: %v", attrs.Name, fallbackErr))
			}
		}
		networkID := interfaceNetworkID(prefixes, attrs.Name)
		networkIDs[attrs.Name] = networkID
		networkPrefixes[attrs.Name] = prefixes
		for _, address := range localAddresses {
			addNeighbor(items, &networkobserverv1.NeighborObservation{
				NetworkId: networkID, NetworkPrefix: address.prefix, Ip: address.ip, Mac: normalizedMAC(attrs.HardwareAddr),
				Interface: attrs.Name, State: "local", IsLocal: true,
			})
		}
		neighbors, neighErr := netlink.NeighList(attrs.Index, netlink.FAMILY_ALL)
		if neighErr != nil {
			details = append(details, fmt.Sprintf("%s neighbors: %v", attrs.Name, neighErr))
			needProcFallback = true
			continue
		}
		for _, neighbor := range neighbors {
			if neighbor.IP == nil || !neighbor.IP.IsGlobalUnicast() && !neighbor.IP.IsLinkLocalUnicast() {
				continue
			}
			networkPrefix := neighborNetworkPrefix(neighbor.IP, prefixes)
			addNeighbor(items, &networkobserverv1.NeighborObservation{
				NetworkId: networkID, NetworkPrefix: networkPrefix, Ip: neighbor.IP.String(), Mac: normalizedMAC(neighbor.HardwareAddr),
				Interface: attrs.Name, State: linuxNeighborState(neighbor.State),
			})
			neighborCount++
		}
	}
	// Some containerized or restricted kernels allow interface enumeration but
	// reject RTM_GETNEIGH. The procfs ARP table is a read-only fallback for IPv4.
	if neighborCount == 0 || needProcFallback {
		fallback, fallbackErr := collectProcARP(networkIDs, networkPrefixes)
		if fallbackErr != nil {
			details = append(details, fmt.Sprintf("procfs ARP: %v", fallbackErr))
		} else {
			for _, item := range fallback {
				addNeighbor(items, item)
			}
		}
	}
	result := make([]*networkobserverv1.NeighborObservation, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].NetworkId != result[j].NetworkId {
			return result[i].NetworkId < result[j].NetworkId
		}
		if result[i].Ip != result[j].Ip {
			return result[i].Ip < result[j].Ip
		}
		return result[i].Mac < result[j].Mac
	})
	if len(details) != 0 || len(result) == 0 {
		if len(details) == 0 {
			details = append(details, "no local addresses or ARP/NDP neighbors were returned")
		}
		return result, capability("lan_neighbors", "degraded", strings.Join(details, "; "))
	}
	return result, capability("lan_neighbors", "supported", fmt.Sprintf("read-only Linux ARP/NDP neighbor snapshot; %d entries", len(result)))
}

func collectProcARP(networkIDs map[string]string, networkPrefixes map[string][]string) ([]*networkobserverv1.NeighborObservation, error) {
	data, err := os.ReadFile("/proc/net/arp")
	if err != nil {
		return nil, err
	}
	return parseProcARP(string(data), networkIDs, networkPrefixes), nil
}

func parseProcARP(data string, networkIDs map[string]string, networkPrefixes map[string][]string) []*networkobserverv1.NeighborObservation {
	result := make([]*networkobserverv1.NeighborObservation, 0)
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[0] == "IP" {
			continue
		}
		ip := net.ParseIP(fields[0])
		if ip == nil || !ip.IsGlobalUnicast() {
			continue
		}
		interfaceName := fields[5]
		networkID := networkIDs[interfaceName]
		if networkID == "" {
			networkID = "link:" + interfaceName
		}
		prefixes := networkPrefixes[interfaceName]
		mac := ""
		if parsed, err := net.ParseMAC(fields[3]); err == nil {
			mac = normalizedMAC(parsed)
		}
		result = append(result, &networkobserverv1.NeighborObservation{
			NetworkId: networkID, NetworkPrefix: neighborNetworkPrefix(ip, prefixes), Ip: ip.String(),
			Mac: mac, Interface: interfaceName, State: "arp",
		})
	}
	return result
}

func interfaceNetworkID(prefixes []string, interfaceName string) string {
	var privateV4, anyV4, anyV6 string
	for _, value := range prefixes {
		_, prefix, err := net.ParseCIDR(value)
		if err != nil {
			continue
		}
		if prefix.IP.To4() != nil {
			if anyV4 == "" || prefixSize(value) > prefixSize(anyV4) {
				anyV4 = value
			}
			if prefix.IP.IsPrivate() && (privateV4 == "" || prefixSize(value) > prefixSize(privateV4)) {
				privateV4 = value
			}
		} else if !prefix.IP.IsLinkLocalUnicast() && (anyV6 == "" || prefixSize(value) > prefixSize(anyV6)) {
			anyV6 = value
		}
	}
	if privateV4 != "" {
		return privateV4
	}
	if anyV4 != "" {
		return anyV4
	}
	if anyV6 != "" {
		return anyV6
	}
	return "link:" + interfaceName
}

func neighborNetworkPrefix(ip net.IP, prefixes []string) string {
	var fallback string
	for _, value := range prefixes {
		_, prefix, err := net.ParseCIDR(value)
		if err != nil || (ip.To4() != nil) != (prefix.IP.To4() != nil) {
			continue
		}
		if fallback == "" || prefixSize(value) > prefixSize(fallback) {
			fallback = value
		}
		if prefix.Contains(ip) {
			return value
		}
	}
	return fallback
}

func prefixSize(value string) int {
	_, prefix, err := net.ParseCIDR(value)
	if err != nil {
		return -1
	}
	bits, _ := prefix.Mask.Size()
	return bits
}

func normalizedMAC(address net.HardwareAddr) string {
	if len(address) == 0 {
		return ""
	}
	return strings.ToLower(address.String())
}

func addNeighbor(items map[string]*networkobserverv1.NeighborObservation, item *networkobserverv1.NeighborObservation) {
	key := strings.Join([]string{item.NetworkId, item.Ip, item.Mac, item.Interface}, "\x00")
	if previous := items[key]; previous != nil {
		previous.IsLocal = previous.IsLocal || item.IsLocal
		if item.State != "" {
			previous.State = item.State
		}
		return
	}
	items[key] = item
}

func linuxNeighborState(state int) string {
	states := []struct {
		flag int
		name string
	}{
		{unix.NUD_REACHABLE, "reachable"}, {unix.NUD_STALE, "stale"}, {unix.NUD_DELAY, "delay"},
		{unix.NUD_PROBE, "probe"}, {unix.NUD_FAILED, "failed"}, {unix.NUD_NOARP, "noarp"}, {unix.NUD_PERMANENT, "permanent"},
	}
	var names []string
	for _, item := range states {
		if state&item.flag != 0 {
			names = append(names, item.name)
		}
	}
	if len(names) == 0 {
		return "unknown"
	}
	return strings.Join(names, "|")
}
