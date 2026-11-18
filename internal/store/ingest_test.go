package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/network-observer/network-observer/internal/domain"
)

func TestPresenceSplitsCoverageWithoutRepeatingTraffic(t *testing.T) {
	start := time.Date(2026, 11, 18, 12, 0, 55, 800000000, time.UTC)
	batch := &appendRecorder{}
	item := domain.FlowObservation{ID: "obs", DeviceID: "agent", FlowID: "flow", Protocol: domain.ProtocolTCP,
		CoverageStart: start, ObservedAt: start.Add(10 * time.Second), Active: true,
		LocalIsSource: false, SourceIP: "203.0.113.1", SourcePort: 443, DestinationPort: 40000,
		BytesSourceToDest: 20, BytesDestToSource: 30}
	if err := appendPresence(batch, item); err != nil {
		t.Fatal(err)
	}
	if len(batch.rows) != 2 {
		t.Fatalf("rows=%d, want two minute buckets", len(batch.rows))
	}
	var seconds uint32
	var upload, download uint64
	for i, row := range batch.rows {
		seconds += row[13].(uint32)
		upload += row[14].(uint64)
		download += row[15].(uint64)
		if row[24] != uint16(40000) || row[25] != "203.0.113.1" {
			t.Fatal("presence lost local/remote perspective")
		}
		if i == 0 && (row[14] != uint64(0) || row[15] != uint64(0)) {
			t.Fatal("traffic was distributed to the earlier minute")
		}
	}
	if seconds != 10 || upload != 30 || download != 20 {
		t.Fatalf("seconds=%d upload/download=%d/%d", seconds, upload, download)
	}
	if batch.rows[0][13] != uint32(5) || batch.rows[1][13] != uint32(5) || batch.rows[0][0] == batch.rows[1][0] {
		t.Fatal("coverage or event keys did not split at the minute boundary")
	}
	replayed := &appendRecorder{}
	_ = appendPresence(replayed, item)
	for i := range batch.rows {
		if batch.rows[i][0] != replayed.rows[i][0] {
			t.Fatal("replay changed the event key")
		}
	}
}

func TestPresenceJitterAndClosedTailPreserveActiveDuration(t *testing.T) {
	start := time.Date(2026, 11, 18, 12, 0, 0, 100000000, time.UTC)
	previous := start
	batch := &appendRecorder{}
	for second := 1; second <= 60; second++ {
		// Alternating sub-second jitter must not halve the recorded active time.
		at := start.Add(time.Duration(second)*time.Second + time.Duration(second%2)*200*time.Millisecond)
		if err := appendPresence(batch, domain.FlowObservation{ID: fmt.Sprint(second), FlowID: "flow",
			CoverageStart: previous, ObservedAt: at, Active: second != 60}); err != nil {
			t.Fatal(err)
		}
		previous = at
	}
	var seconds uint32
	for _, row := range batch.rows {
		seconds += row[13].(uint32)
	}
	if seconds != 60 {
		t.Fatalf("active seconds=%d, want 60 including the closed tail", seconds)
	}
}

func TestPresenceLegacyFirstObservationAndInvalidCoverage(t *testing.T) {
	at := time.Now().UTC()
	for _, active := range []bool{false, true} {
		legacy := &appendRecorder{}
		item := domain.FlowObservation{ID: "obs", FlowID: "f", ObservedAt: at, Active: active}
		if err := appendPresence(legacy, item); err != nil {
			t.Fatal(err)
		}
		want := uint32(0)
		if active {
			want = 2
		}
		if len(legacy.rows) != 1 || legacy.rows[0][13] != want {
			t.Fatal("legacy presence behavior changed")
		}
		first := &appendRecorder{}
		item.CoverageStart = at
		if err := appendPresence(first, item); err != nil || len(first.rows) != 1 || first.rows[0][13] != uint32(0) {
			t.Fatal("first sparse observation must preserve flow presence with zero inferred duration")
		}
	}
	for _, start := range []time.Time{at.Add(time.Second), at.Add(-3 * time.Minute)} {
		if err := appendPresence(&appendRecorder{}, domain.FlowObservation{FlowID: "f", ObservedAt: at, CoverageStart: start}); err == nil {
			t.Fatal("invalid coverage was accepted")
		}
	}
}

type appendRecorder struct {
	rows [][]any
}

func (r *appendRecorder) Append(values ...any) error {
	r.rows = append(r.rows, append([]any(nil), values...))
	return nil
}

func TestAppendTrafficMarksSampledAndEstimatedObservations(t *testing.T) {
	tests := []struct {
		name         string
		sampling     uint32
		confidence   string
		wantEstimate uint8
	}{
		{name: "exact counters", sampling: 1, confidence: "exact", wantEstimate: 0},
		{name: "sampled counters", sampling: 100, confidence: "estimated", wantEstimate: 1},
		{name: "estimated source", sampling: 1, confidence: "estimated", wantEstimate: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			batch := &appendRecorder{}
			err := appendTraffic(batch, domain.FlowObservation{
				ID: "observation-1", DeviceID: "device-1", ObservedAt: time.Now().UTC(),
				Protocol: domain.ProtocolTCP, SourceIP: "192.0.2.1", DestinationIP: "198.51.100.2",
				SourcePort: 12345, DestinationPort: 443, BytesSourceToDest: 10,
				SamplingRate: test.sampling, Confidence: test.confidence,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(batch.rows) == 0 {
				t.Fatal("expected traffic rows")
			}
			for _, row := range batch.rows {
				if got := row[10].(uint8); got != test.wantEstimate {
					t.Fatalf("estimated marker = %d, want %d", got, test.wantEstimate)
				}
			}
		})
	}
}

func TestAppendTrafficSkipsOnlySuppressedPerspective(t *testing.T) {
	batch := &appendRecorder{}
	observation := domain.FlowObservation{
		ID: "gateway-observation", DeviceID: "gateway", ObservedAt: time.Now().UTC(),
		Protocol: domain.ProtocolTCP, SourceIP: "192.0.2.1", DestinationIP: "198.51.100.2",
		SourcePort: 12345, DestinationPort: 443, BytesSourceToDest: 10,
		SamplingRate: 1, Confidence: "exact", TrafficSuppressed: true,
	}
	if err := appendTraffic(batch, observation); err != nil {
		t.Fatal(err)
	}
	if len(batch.rows) != 0 {
		t.Fatalf("suppressed traffic produced %d rows", len(batch.rows))
	}
}

func TestTrafficDedupKeyCanonicalizesDirectionAndIPv6(t *testing.T) {
	first := domain.FlowObservation{Protocol: domain.ProtocolTCP, SourceIP: "2001:0db8::1", SourcePort: 1234, DestinationIP: "2001:db8::2", DestinationPort: 443}
	second := domain.FlowObservation{Protocol: domain.ProtocolTCP, SourceIP: "2001:db8::2", SourcePort: 443, DestinationIP: "2001:db8::1", DestinationPort: 1234}
	if got, want := trafficDedupKey(first), trafficDedupKey(second); got != want {
		t.Fatalf("directional keys differ: %q != %q", got, want)
	}
	if got := trafficDedupKey(domain.FlowObservation{Protocol: domain.ProtocolTCP, SourceIP: "192.0.2.1", SourcePort: 0, DestinationIP: "198.51.100.2", DestinationPort: 443}); got != "" {
		t.Fatalf("incomplete tuple got key %q", got)
	}
}

func TestTrafficDedupWindowAndExactCounterPolicy(t *testing.T) {
	now := time.Now().UTC()
	if !withinTrafficDedupWindow(now.Add(29*time.Second), now) || withinTrafficDedupWindow(now.Add(31*time.Second), now) {
		t.Fatal("unexpected dedup window result")
	}
	base := domain.FlowObservation{Confidence: "exact", CounterSource: "socket_diag_tcp_info", TotalBytesSourceToDest: 1}
	if !endpointCountersAreExact(base) {
		t.Fatal("exact endpoint counters were rejected")
	}
	base.CounterSource = "unavailable"
	if endpointCountersAreExact(base) {
		t.Fatal("unavailable endpoint counters were accepted")
	}
}
