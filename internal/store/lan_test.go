package store

import (
	"testing"

	"github.com/network-observer/network-observer/internal/domain"
)

func TestLANIPConflictRequiresOneObserverToSeeMultipleMACs(t *testing.T) {
	tracker := lanIPConflictTracker{}
	tracker.observe("linux-agent", "docker0", "02:00:00:00:00:01")
	tracker.observe("nas-agent", "docker0", "02:00:00:00:00:02")
	if tracker.confirmed() {
		t.Fatal("same IP observed on different agents must not alone prove an IP conflict")
	}
	tracker.observe("linux-agent", "eth0", "02:00:00:00:00:02")
	if tracker.confirmed() {
		t.Fatal("same IP on different interfaces must not alone prove an IP conflict")
	}
	tracker.observe("linux-agent", "docker0", "02:00:00:00:00:02")
	if !tracker.confirmed() {
		t.Fatal("one observer seeing multiple MACs for an IP should be classified as a conflict")
	}
}

func TestDockerSubnetOwnersCanonicalizeAndScopeByAgent(t *testing.T) {
	owners := dockerSubnetOwners([]domain.DockerNetwork{
		{OwnerAgentID: "linux", SubnetCIDRs: []string{"172.17.9.3/16", "bad-cidr"}},
		{OwnerAgentID: "nas", SubnetCIDRs: []string{"172.17.0.0/16"}},
	})
	if !isDockerSubnetOwner("172.17.0.0/16", "linux", owners) || !isDockerSubnetOwner("172.17.0.0/16", "nas", owners) {
		t.Fatalf("Docker subnet owners were not canonicalized: %+v", owners)
	}
	if isDockerSubnetOwner("172.17.0.0/16", "other", owners) || isDockerSubnetOwner("192.168.1.0/24", "linux", owners) || len(owners) != 1 {
		t.Fatalf("Docker subnet exclusion must remain scoped to its owners: %+v", owners)
	}
}

func TestUsableLANPrefixRequiresMatchingNonHostNetwork(t *testing.T) {
	cases := []struct {
		name    string
		prefix  string
		address string
		want    bool
	}{
		{name: "ipv4 subnet", prefix: "192.168.31.0/24", address: "192.168.31.100", want: true},
		{name: "ipv6 subnet", prefix: "2001:db8:31::/64", address: "2001:db8:31::100", want: true},
		{name: "host route", prefix: "192.168.31.100/32", address: "192.168.31.100"},
		{name: "prefix does not contain address", prefix: "192.168.30.0/24", address: "192.168.31.100"},
		{name: "invalid prefix", prefix: "not-a-cidr", address: "192.168.31.100"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := usableLANPrefix(test.prefix, test.address); got != test.want {
				t.Fatalf("usableLANPrefix(%q, %q) = %v, want %v", test.prefix, test.address, got, test.want)
			}
		})
	}
}