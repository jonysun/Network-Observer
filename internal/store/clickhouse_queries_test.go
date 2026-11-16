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
