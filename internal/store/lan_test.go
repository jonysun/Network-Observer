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
