package agent

import (
	"fmt"
	"testing"

	pb "github.com/network-observer/network-observer/gen/networkobserver/v1"
)

func TestRuntimeRealtimeSnapshotAndCloseDelivery(t *testing.T) {
	state := &runtimeState{}
	first := &pb.FlowObservation{FlowId: "flow-1", Active: true}
	state.setRealtime([]*pb.FlowObservation{first})
	first.FlowId = "mutated-after-set"

	batch := state.takeRealtime()
	if len(batch) != 1 || batch[0].FlowId != "flow-1" {
		t.Fatalf("snapshot was not cloned: %#v", batch)
	}
	if next := state.takeRealtime(); next != nil {
		t.Fatalf("same realtime version was delivered twice: %#v", next)
	}

	state.setRealtime([]*pb.FlowObservation{
		{FlowId: "flow-2", Active: true},
		{FlowId: "flow-3", Active: true},
	})
	replaced := state.takeRealtime()
	if len(replaced) != 3 {
		t.Fatalf("new snapshot should include two updates and one close: %#v", replaced)
	}
	seen := map[string]bool{}
	for _, item := range replaced {
		seen[item.FlowId] = item.Active
	}
	if seen["flow-1"] || !seen["flow-2"] || !seen["flow-3"] {
		t.Fatalf("unexpected realtime changes: %#v", replaced)
	}
}

func TestRuntimeRealtimeSnapshotFiltersDNSAndEmptyFlows(t *testing.T) {
	state := &runtimeState{}
	state.setRealtime([]*pb.FlowObservation{
		{FlowId: "", Active: true},
		{FlowId: "dns", DnsMetadataOnly: true},
		{FlowId: "flow", Active: true},
	})
	items := state.takeRealtime()
	if len(items) != 1 || items[0].FlowId != "flow" {
		t.Fatalf("non-live records entered realtime snapshot: %#v", items)
	}
}

func TestRuntimeRealtimeDeltaDoesNotReplaceStableFlows(t *testing.T) {
	state := &runtimeState{}
	full := []*pb.FlowObservation{
		{ObserverId: "obs", FlowId: "a", Active: true},
		{ObserverId: "obs", FlowId: "b", Active: true},
	}
	state.updateRealtimeForObserver("obs", full, full)
	_ = state.takeRealtime()
	state.updateRealtimeForObserver("obs", full, []*pb.FlowObservation{{ObserverId: "obs", FlowId: "a", Active: true, RateBytesSourceToDestination: 42}})
	changes := state.takeRealtime()
	if len(changes) != 1 || changes[0].FlowId != "a" || changes[0].RateBytesSourceToDestination != 42 {
		t.Fatalf("unexpected reduced delta: %#v", changes)
	}
	snapshot := state.realtimeSnapshot()
	if len(snapshot) != 2 {
		t.Fatalf("stable flow disappeared from latest state: %#v", snapshot)
	}
}

func TestRuntimeRealtimeEmptySnapshotClosesKnownObserver(t *testing.T) {
	state := &runtimeState{}
	state.updateRealtimeForObserver("obs", []*pb.FlowObservation{{ObserverId: "obs", FlowId: "a", Active: true}}, nil)
	_ = state.takeRealtime()
	state.updateRealtimeForObserver("obs", nil, nil)
	changes := state.takeRealtime()
	if len(changes) != 1 || changes[0].FlowId != "a" || changes[0].Active {
		t.Fatalf("empty snapshot did not close flow: %#v", changes)
	}
}

func TestRuntimeRealtimePendingIsBoundedByFlowCount(t *testing.T) {
	state := &runtimeState{}
	full := make([]*pb.FlowObservation, 0, 1000)
	for i := 0; i < 1000; i++ {
		full = append(full, &pb.FlowObservation{ObserverId: "obs", FlowId: fmt.Sprintf("flow-%04d", i), Active: true})
	}
	state.updateRealtimeForObserver("obs", full, full)
	_ = state.takeRealtime()
	for i := 0; i < 20; i++ {
		state.updateRealtimeForObserver("obs", full, full[:100])
	}
	state.mu.Lock()
	pending := len(state.realtimePending)
	state.mu.Unlock()
	if pending != 100 {
		t.Fatalf("pending delta map grew beyond changed flow count: %d", pending)
	}
}
