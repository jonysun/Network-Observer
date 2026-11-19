package server

import (
	"testing"
	"time"

	"github.com/network-observer/network-observer/internal/domain"
)

func TestMergeConnectionObservationsRealtimeWinsPerDeviceAndFlow(t *testing.T) {
	stored := []domain.FlowObservation{
		{ID: "old-a", DeviceID: "device-a", FlowID: "flow-1", RateBytesSourceToDest: 10},
		{ID: "old-b", DeviceID: "device-b", FlowID: "flow-1", RateBytesSourceToDest: 20},
		{ID: "old-c", DeviceID: "device-a", FlowID: "flow-2", RateBytesSourceToDest: 30},
	}
	realtime := []domain.FlowObservation{
		{ID: "new-a", DeviceID: "device-a", FlowID: "flow-1", RateBytesSourceToDest: 100},
	}

	got := mergeConnectionObservations(stored, realtime)
	if len(got) != 3 {
		t.Fatalf("merged %d items, want 3", len(got))
	}
	byKey := make(map[string]domain.FlowObservation, len(got))
	for _, item := range got {
		byKey[item.DeviceID+"\x00"+item.FlowID] = item
	}
	if got := byKey["device-a\x00flow-1"].ID; got != "new-a" {
		t.Fatalf("realtime row did not win: %q", got)
	}
	if got := byKey["device-b\x00flow-1"].ID; got != "old-b" {
		t.Fatalf("same flow on another device was overwritten: %q", got)
	}
	if got := byKey["device-a\x00flow-2"].ID; got != "old-c" {
		t.Fatalf("unrelated flow was lost: %q", got)
	}
	if got := mergeConnectionObservations(stored, realtime)[0].ID; got != "new-a" {
		t.Fatalf("merged rows are not sorted by rate: %q", got)
	}
}

func TestPaginateConnectionsAfterMerge(t *testing.T) {
	items := []domain.FlowObservation{
		{DeviceID: "device", FlowID: "a"},
		{DeviceID: "device", FlowID: "b"},
		{DeviceID: "device", FlowID: "c"},
	}
	page := paginateConnections(items, 1, 1)
	if len(page) != 1 || page[0].FlowID != "b" {
		t.Fatalf("unexpected page: %#v", page)
	}
	if page := paginateConnections(items, 10, 10); len(page) != 0 {
		t.Fatalf("out-of-range page returned %d rows", len(page))
	}
}

func TestHistoricalAndFreshDurableRowsHaveDifferentRealtimeEligibility(t *testing.T) {
	at := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	if realtimeEligibleForDurableBatch(domain.FlowObservation{ID: "history-abc", ObservedAt: at}, at) {
		t.Fatal("history aggregate entered realtime cache")
	}
	if !realtimeEligibleForDurableBatch(domain.FlowObservation{ID: "observation-abc", ObservedAt: at.Add(-time.Second)}, at) {
		t.Fatal("fresh durable observation was excluded from realtime cache")
	}
	if realtimeEligibleForDurableBatch(domain.FlowObservation{ID: "observation-old", ObservedAt: at.Add(-realtimeExpiry - time.Second)}, at) {
		t.Fatal("stale durable observation entered realtime cache")
	}
}
