package agent

import (
	"fmt"
	"testing"
	"time"

	pb "github.com/network-observer/network-observer/gen/networkobserver/v1"
)

func historyTestObservation(at time.Time, flow string, bytes, packets uint64, active bool) *pb.FlowObservation {
	return &pb.FlowObservation{
		Id:                            flow + "-" + at.Format(time.RFC3339Nano),
		FlowId:                        flow,
		ObserverId:                    "device:endpoint",
		ObserverType:                  "endpoint",
		ObservedAtUnixNanos:           at.UnixNano(),
		SourceIp:                      "192.168.1.10",
		SourcePort:                    40000,
		DestinationIp:                 "203.0.113.10",
		DestinationPort:               443,
		Protocol:                      "tcp",
		Active:                        active,
		BytesSourceToDestination:      bytes,
		PacketsSourceToDestination:    packets,
		TotalBytesSourceToDestination: bytes,
	}
}

func TestHistoryAggregatorAccumulatesExactDeltas(t *testing.T) {
	start := time.Date(2026, 11, 19, 12, 0, 1, 0, time.UTC)
	a := newHistoryAggregator()
	for i := 0; i < 5; i++ {
		a.add([]*pb.FlowObservation{historyTestObservation(start.Add(time.Duration(i)*time.Second), "flow-1", uint64(100+i), 2, true)}, time.Second)
	}
	items := a.flushDue(start.Add(5*time.Second), true)
	if len(items) != 1 {
		t.Fatalf("flush returned %d aggregates, want 1", len(items))
	}
	if got := items[0].BytesSourceToDestination; got != 510 {
		t.Fatalf("bytes=%d, want 510", got)
	}
	if got := items[0].PacketsSourceToDestination; got != 10 {
		t.Fatalf("packets=%d, want 10", got)
	}
	if got := items[0].CoverageStartUnixNanos; got != start.UnixNano() {
		t.Fatalf("coverage start=%d, want %d", got, start.UnixNano())
	}

	a.markFlushed(items)
	if retry := a.flushDue(start.Add(6*time.Second), true); len(retry) != 0 {
		t.Fatalf("clean aggregate was emitted again: %d", len(retry))
	}
}

func TestHistoryAggregatorStartsNewSegmentAcrossMinuteAndGap(t *testing.T) {
	start := time.Date(2026, 11, 19, 12, 0, 59, 0, time.UTC)
	a := newHistoryAggregator()
	a.add([]*pb.FlowObservation{historyTestObservation(start, "flow-1", 10, 1, true)}, time.Second)
	a.add([]*pb.FlowObservation{historyTestObservation(start.Add(time.Second), "flow-1", 20, 2, true)}, time.Second)
	a.add([]*pb.FlowObservation{historyTestObservation(start.Add(4*time.Second), "flow-1", 40, 4, true)}, time.Second)
	items := a.flushDue(start.Add(10*time.Second), true)
	if len(items) != 3 {
		t.Fatalf("segments=%d, want 3", len(items))
	}
	if items[0].BytesSourceToDestination != 10 || items[1].BytesSourceToDestination != 20 || items[2].BytesSourceToDestination != 40 {
		t.Fatalf("unexpected segment deltas: %d, %d, %d", items[0].BytesSourceToDestination, items[1].BytesSourceToDestination, items[2].BytesSourceToDestination)
	}
	if items[0].Id == items[1].Id || items[1].Id == items[2].Id {
		t.Fatal("segment IDs must remain distinct across minute and collection gaps")
	}
}

func TestHistoryAggregatorKeepsDirtyAfterUnacknowledgedFlush(t *testing.T) {
	start := time.Date(2026, 11, 19, 12, 0, 1, 0, time.UTC)
	a := newHistoryAggregator()
	a.add([]*pb.FlowObservation{historyTestObservation(start, "flow-1", 7, 1, true)}, time.Second)
	first := a.flushDue(start.Add(2*time.Second), true)
	if len(first) != 1 {
		t.Fatalf("first flush=%d, want 1", len(first))
	}
	second := a.flushDue(start.Add(3*time.Second), true)
	if len(second) != 1 || second[0].BytesSourceToDestination != 7 {
		t.Fatalf("unacknowledged aggregate was not retained: %#v", second)
	}
}

func TestHistoryAggregatorRestartSegmentUsesNewID(t *testing.T) {
	start := time.Date(2026, 11, 19, 12, 0, 1, 0, time.UTC)
	first := newHistoryAggregator()
	first.add([]*pb.FlowObservation{historyTestObservation(start, "flow-1", 7, 1, true)}, time.Second)
	firstItems := first.flushDue(start.Add(2*time.Second), true)
	if len(firstItems) != 1 {
		t.Fatalf("first flush=%d, want 1", len(firstItems))
	}

	second := newHistoryAggregator()
	second.add([]*pb.FlowObservation{historyTestObservation(start.Add(time.Second), "flow-1", 9, 1, true)}, time.Second)
	secondItems := second.flushDue(start.Add(3*time.Second), true)
	if len(secondItems) != 1 {
		t.Fatalf("second flush=%d, want 1", len(secondItems))
	}
	if firstItems[0].Id == secondItems[0].Id {
		t.Fatal("a restarted aggregator must not overwrite the previous segment")
	}
}

func TestHistoryAggregatorDefersCurrentMinuteUntilClosed(t *testing.T) {
	start := time.Date(2026, 11, 19, 12, 0, 1, 0, time.UTC)
	a := newHistoryAggregator()
	a.add([]*pb.FlowObservation{historyTestObservation(start, "flow-1", 100, 1, true)}, time.Second)
	if got := a.flushDue(start.Add(10*time.Second), false); len(got) != 0 {
		t.Fatalf("current minute was written before it closed: %d records", len(got))
	}
	got := a.flushDue(start.Add(61*time.Second), false)
	if len(got) != 1 || got[0].BytesSourceToDestination != 100 {
		t.Fatalf("closed minute was not emitted exactly once: %#v", got)
	}
}

func TestHistoryAggregatorScalesWithFlowCountWithoutPerTickDuplicates(t *testing.T) {
	start := time.Date(2026, 11, 19, 12, 0, 1, 0, time.UTC)
	a := newHistoryAggregator()
	items := make([]*pb.FlowObservation, 0, 10000)
	for i := 0; i < 10000; i++ {
		item := historyTestObservation(start, fmt.Sprintf("flow-%05d", i), uint64(i+1), 1, true)
		items = append(items, item)
	}
	a.add(items, time.Second)
	if got := a.flushDue(start.Add(10*time.Second), false); len(got) != 0 {
		t.Fatalf("current minute emitted %d duplicate rows", len(got))
	}
	got := a.flushDue(start.Add(61*time.Second), false)
	if len(got) != 10000 {
		t.Fatalf("completed minute emitted %d rows, want 10000", len(got))
	}
	var total uint64
	for _, item := range got {
		total += item.BytesSourceToDestination
	}
	if total != 10000*10001/2 {
		t.Fatalf("exact byte aggregation changed: got %d", total)
	}
}

func TestHistoryAggregatorEvictionRequiresWALPressure(t *testing.T) {
	start := time.Date(2026, 11, 19, 12, 0, 1, 0, time.UTC)
	a := newHistoryAggregator()
	a.add([]*pb.FlowObservation{
		historyTestObservation(start, "flow-1", 10, 1, true),
		historyTestObservation(start, "flow-2", 20, 1, true),
		historyTestObservation(start, "flow-3", 30, 1, true),
	}, time.Second)
	if dropped := evictHistoryIfPressured(a, false, 1, 1); dropped != 0 {
		t.Fatalf("healthy WAL dropped %d aggregates", dropped)
	}
	if records, _ := a.memoryUsage(); records != 3 {
		t.Fatalf("healthy WAL changed aggregate count to %d, want 3", records)
	}
	if dropped := evictHistoryIfPressured(a, true, 1, 1<<30); dropped != 2 {
		t.Fatalf("pressured WAL dropped %d aggregates, want 2", dropped)
	}
}

func TestHistoryAggregatorEvictsOldestRowsOnlyAfterBoundIsExceeded(t *testing.T) {
	start := time.Date(2026, 11, 19, 12, 0, 1, 0, time.UTC)
	a := newHistoryAggregator()
	items := []*pb.FlowObservation{
		historyTestObservation(start, "flow-1", 10, 1, true),
		historyTestObservation(start, "flow-2", 20, 1, true),
		historyTestObservation(start, "flow-3", 30, 1, true),
	}
	a.add(items, time.Second)
	if records, bytes := a.memoryUsage(); records != 3 || bytes <= 0 {
		t.Fatalf("memory usage before eviction = records=%d bytes=%d, want 3 and positive bytes", records, bytes)
	}
	if dropped := a.evictOldest(2, 1<<30); dropped != 1 {
		t.Fatalf("dropped=%d, want 1", dropped)
	}
	if records, bytes := a.memoryUsage(); records != 2 || bytes <= 0 {
		t.Fatalf("memory usage after eviction = records=%d bytes=%d, want 2 and positive bytes", records, bytes)
	}
	// The evicted flow must start a new segment when it is seen again. This
	// proves that an in-memory bound never bridges the missing interval.
	a.add([]*pb.FlowObservation{historyTestObservation(start.Add(3*time.Second), "flow-1", 40, 1, true)}, time.Second)
	got := a.flushDue(start.Add(61*time.Second), true)
	if len(got) != 3 {
		t.Fatalf("rows after reappearing evicted flow=%d, want 3", len(got))
	}
}
