package store

import (
	"strings"
	"testing"
)

func TestPresenceQueryOrdersByAggregatedActiveSeconds(t *testing.T) {
	query := presenceQuery("bucket >= ? AND bucket < ?")
	if !strings.Contains(query, "ORDER BY sum(active_seconds) DESC") {
		t.Fatalf("presence query must order by the aggregated active_seconds: %s", query)
	}
	if strings.Contains(query, "ORDER BY active_seconds DESC") {
		t.Fatal("presence query orders by the inner active_seconds column")
	}
}

func TestInternalFlowExclusionRequiresBothEndpointsInsideLAN(t *testing.T) {
	condition, args := internalFlowExclusion("source_ip", "destination_ip", []string{"192.168.31.0/24"})
	if !strings.Contains(condition, "NOT ((isIPAddressInRange(source_ip, ?)") || !strings.Contains(condition, "AND (isIPAddressInRange(destination_ip, ?)") {
		t.Fatalf("unexpected internal flow condition: %s", condition)
	}
	if len(args)%2 != 0 || args[len(args)/2-1] != "192.168.31.0/24" || args[len(args)-1] != "192.168.31.0/24" {
		t.Fatalf("source and destination network parameters should end with the custom LAN range: %#v", args)
	}
}

func TestNormalizeLANNetworksDropsInvalidAndDuplicates(t *testing.T) {
	networks := normalizeLANNetworks([]string{"192.168.31.7/24", "bad", "192.168.31.0/24"})
	count := 0
	for _, network := range networks {
		if network == "192.168.31.0/24" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected canonical network once, got %#v", networks)
	}
}
