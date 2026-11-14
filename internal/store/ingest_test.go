package store

import (
	"testing"
	"time"

	"github.com/network-observer/network-observer/internal/domain"
)

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
