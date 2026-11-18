package agent

import (
	"fmt"
	"testing"
	"time"

	pb "github.com/network-observer/network-observer/gen/networkobserver/v1"
	"google.golang.org/protobuf/proto"
)

func idleObservation(at time.Time, flow string) *pb.FlowObservation {
	return &pb.FlowObservation{Id: fmt.Sprintf("%s:%d", flow, at.UnixNano()), FlowId: flow,
		ObserverId: "agent:endpoint", ObserverType: "endpoint", ObservedAtUnixNanos: at.UnixNano(),
		Protocol: "tcp", State: "ESTABLISHED", Active: true}
}

func TestIdleObservationsReduceTenThousandFlowsWithoutLosingCoverage(t *testing.T) {
	const flows = 10000
	start := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	r := &observationReducer{}
	count, covered := 0, int64(0)
	for second := 0; second <= 60; second++ {
		items := make([]*pb.FlowObservation, flows)
		for flow := range items {
			items[flow] = idleObservation(start.Add(time.Duration(second)*time.Second), fmt.Sprint(flow))
		}
		for _, item := range r.reduce(items, true, time.Second) {
			count++
			covered += item.ObservedAtUnixNanos - item.CoverageStartUnixNanos
		}
		if second == 59 && count != 60000 {
			t.Fatalf("60 snapshots emitted %d observations, want 60000 instead of 600000", count)
		}
	}
	if count != 70000 || covered != int64(flows)*int64(time.Minute) {
		t.Fatalf("emitted=%d coverage=%s, want 70000 / %s", count, time.Duration(covered), flows*time.Minute)
	}
	t.Log("10000 idle flows: first 60 snapshots reduced from 600000 to 60000 observations; 60s coverage preserved at the next keepalive")
}

func TestObservationChangesAndCloseAreNeverSuppressed(t *testing.T) {
	r := &observationReducer{}
	start := time.Now().UTC()
	item := idleObservation(start, "flow")
	if len(r.reduce([]*pb.FlowObservation{item}, true, time.Second)) != 1 {
		t.Fatal("first observation was suppressed")
	}
	for second := 1; second <= 7; second++ {
		item = proto.Clone(item).(*pb.FlowObservation)
		item.Id = fmt.Sprint(second)
		item.ObservedAtUnixNanos = start.Add(time.Duration(second) * time.Second).UnixNano()
		switch second {
		case 2:
			item.BytesSourceToDestination, item.RateBytesSourceToDestination = 100, 100
		case 3:
			item.BytesSourceToDestination, item.RateBytesSourceToDestination = 0, 0
		case 4:
			item.ProcessName, item.ContainerId, item.Interface = "new-process", "container", "eth1"
		case 5:
			item.PacketsDestinationToSource = 1
		case 6:
			item.PacketsDestinationToSource = 0
		case 7:
			item.Active, item.State = false, "CLOSED"
		}
		got := r.reduce([]*pb.FlowObservation{item}, true, time.Second)
		want := second != 1 && second != 6
		if (len(got) == 1) != want {
			t.Fatalf("second %d emitted=%d want=%v", second, len(got), want)
		}
		if second == 7 && got[0].CoverageStartUnixNanos != start.Add(5*time.Second).UnixNano() {
			t.Fatal("close did not account for the idle tail")
		}
	}
	if len(r.flows) != 0 {
		t.Fatal("closed flow retained reducer state")
	}
}

func TestObservationReducerDoesNotBridgeCollectionGaps(t *testing.T) {
	for _, gap := range []string{"reset", "stall", "clock regression"} {
		t.Run(gap, func(t *testing.T) {
			r := &observationReducer{}
			start := time.Now().UTC()
			r.reduce([]*pb.FlowObservation{idleObservation(start, "f")}, true, time.Second)
			at := start.Add(time.Second)
			switch gap {
			case "reset":
				r.reset()
			case "stall":
				at = start.Add(4 * time.Second)
			case "clock regression":
				at = start.Add(-time.Second)
			}
			got := r.reduce([]*pb.FlowObservation{idleObservation(at, "f")}, true, time.Second)
			if len(got) != 1 || got[0].CoverageStartUnixNanos != at.UnixNano() {
				t.Fatal("gap was suppressed or claimed continuous coverage")
			}
		})
	}
}

func TestReducerPreservesLegacyCenterAndIndependentEvents(t *testing.T) {
	r := &observationReducer{}
	start := time.Now().UTC()
	for second := 0; second < 3; second++ {
		got := r.reduce([]*pb.FlowObservation{idleObservation(start.Add(time.Duration(second)*time.Second), "f")}, false, time.Second)
		if len(got) != 1 || got[0].CoverageStartUnixNanos != 0 {
			t.Fatal("legacy Center must receive complete snapshots without coverage")
		}
	}
	for second := 0; second < 3; second++ {
		at := start.Add(time.Duration(second) * time.Second)
		dns, exporter := idleObservation(at, "dns"), idleObservation(at, "export")
		dns.DnsMetadataOnly = true
		exporter.ObserverType, exporter.CounterSource = "gateway", "sflow"
		if got := r.reduce([]*pb.FlowObservation{dns, exporter}, true, time.Second); len(got) != 2 {
			t.Fatal("DNS or exporter events were suppressed")
		}
	}
	first := idleObservation(start, "socket")
	r.reduce([]*pb.FlowObservation{first}, true, time.Second)
	got := r.reduce([]*pb.FlowObservation{idleObservation(start.Add(time.Second), "socket")}, false, time.Second)
	if len(got) != 1 || got[0].CoverageStartUnixNanos != 0 {
		t.Fatal("downgraded handshake did not disable sparse reporting")
	}
}
