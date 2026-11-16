//go:build linux

package agent

import (
	"net"
	"testing"

	networkobserverv1 "github.com/network-observer/network-observer/gen/networkobserver/v1"
)

func TestInterfaceNetworkIDUnifiesDualStackAddresses(t *testing.T) {
	got := interfaceNetworkID([]string{"192.168.31.0/24", "2001:db8:31::/64", "fe80::/64"}, "eth0")
	if got != "192.168.31.0/24" {
		t.Fatalf("network id = %q, want shared IPv4 LAN segment", got)
	}
}

func TestInterfaceNetworkIDScopesIPv6OnlySegment(t *testing.T) {
	got := interfaceNetworkID([]string{"fe80::/64"}, "enp2s0")
	if got != "link:enp2s0" {
		t.Fatalf("network id = %q, want interface fallback scope", got)
	}
	got = neighborNetworkPrefix(net.ParseIP("fe80::1234"), []string{"fe80::/64", "2001:db8:31::/64"})
	if got != "fe80::/64" {
		t.Fatalf("neighbor prefix = %q, want link-local prefix", got)
	}
}

func TestAddNeighborMergesDuplicateLocalIdentity(t *testing.T) {
	items := make(map[string]*networkobserverv1.NeighborObservation)
	addNeighbor(items, &networkobserverv1.NeighborObservation{NetworkId: "192.168.31.0/24", Ip: "192.168.31.2", Mac: "aa:bb:cc:dd:ee:ff", Interface: "eth0", State: "stale"})
	addNeighbor(items, &networkobserverv1.NeighborObservation{NetworkId: "192.168.31.0/24", Ip: "192.168.31.2", Mac: "aa:bb:cc:dd:ee:ff", Interface: "eth0", State: "local", IsLocal: true})
	if len(items) != 1 {
		t.Fatalf("got %d entries, want one merged entry", len(items))
	}
	for _, item := range items {
		if !item.IsLocal || item.State != "local" {
			t.Fatalf("merged entry did not retain local identity: %+v", item)
		}
	}
}

func TestParseProcARPFallsBackToIPv4Neighbors(t *testing.T) {
	items := parseProcARP(`IP address       HW type     Flags       HW address            Mask     Device
192.168.31.42    0x1         0x2         aa:bb:cc:dd:ee:ff     *        eth0
`, map[string]string{"eth0": "192.168.31.0/24"}, map[string][]string{"eth0": []string{"192.168.31.0/24"}})
	if len(items) != 1 {
		t.Fatalf("got %d ARP entries, want one", len(items))
	}
	if items[0].Ip != "192.168.31.42" || items[0].Mac != "aa:bb:cc:dd:ee:ff" || items[0].NetworkId != "192.168.31.0/24" {
		t.Fatalf("unexpected ARP entry: %+v", items[0])
	}
}
