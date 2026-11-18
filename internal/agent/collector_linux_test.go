//go:build linux

package agent

import (
	"context"
	"testing"

	pb "github.com/network-observer/network-observer/gen/networkobserver/v1"
)

func TestEndpointCloseDoesNotRepeatPreviousTrafficDelta(t *testing.T) {
	previous := testObservation("test-removed-socket")
	previous.BytesSourceToDestination, previous.BytesDestinationToSource = 100, 200
	previous.PacketsSourceToDestination, previous.PacketsDestinationToSource = 3, 4
	previous.TotalBytesSourceToDestination, previous.TotalBytesDestinationToSource = 1000, 2000
	collector := &endpointCollector{previous: map[string]*pb.FlowObservation{previous.FlowId: previous},
		config: Config{EbpfMode: "off", DisableSocketDiag: true, DisableProcessAttribution: true}}
	items, _, err := collector.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.FlowId != previous.FlowId {
			continue
		}
		if item.Active || item.BytesSourceToDestination != 0 || item.BytesDestinationToSource != 0 || item.PacketsSourceToDestination != 0 || item.PacketsDestinationToSource != 0 {
			t.Fatal("close repeated the previous traffic delta")
		}
		if item.TotalBytesSourceToDestination != 1000 || item.TotalBytesDestinationToSource != 2000 {
			t.Fatal("close lost cumulative counters")
		}
		return
	}
	t.Fatal("missing close observation")
}
