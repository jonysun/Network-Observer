package agent

import (
	"crypto/sha256"
	"time"

	pb "github.com/network-observer/network-observer/gen/networkobserver/v1"
	"google.golang.org/protobuf/proto"
)

const idleObservationInterval = 10 * time.Second

type observationState struct {
	fingerprint [32]byte
	emittedAt   int64
	seenAt      int64
}

// observationReducer only suppresses unchanged, idle socket/conntrack snapshots.
// Exporter events and DNS metadata are independent events and always pass through.
type observationReducer struct {
	flows   map[string]observationState
	enabled bool
}

func (r *observationReducer) reset() { r.flows = nil }

func (r *observationReducer) reduce(items []*pb.FlowObservation, enabled bool, interval time.Duration) []*pb.FlowObservation {
	if enabled != r.enabled {
		r.reset()
		r.enabled = enabled
	}
	if !enabled {
		return items
	}
	if r.flows == nil {
		r.flows = make(map[string]observationState)
	}
	result := make([]*pb.FlowObservation, 0, len(items))
	for _, item := range items {
		if item.DnsMetadataOnly || (item.ObserverType != "endpoint" && item.CounterSource != "conntrack") {
			result = append(result, item)
			continue
		}
		key := item.ObserverId + ":" + item.FlowId
		previous, exists := r.flows[key]
		// Gaps, clock regressions and long collector stalls cannot establish
		// continuous presence. Start new coverage at the current observation.
		continuous := exists && item.ObservedAtUnixNanos > previous.seenAt &&
			item.ObservedAtUnixNanos-previous.seenAt <= int64(2*interval)
		fingerprint := observationFingerprint(item)
		hasTraffic := item.BytesSourceToDestination != 0 || item.BytesDestinationToSource != 0 ||
			item.PacketsSourceToDestination != 0 || item.PacketsDestinationToSource != 0
		if continuous && item.Active && !hasTraffic && fingerprint == previous.fingerprint &&
			item.ObservedAtUnixNanos-previous.emittedAt < int64(idleObservationInterval) {
			previous.seenAt = item.ObservedAtUnixNanos
			r.flows[key] = previous
			continue
		}
		item.CoverageStartUnixNanos = item.ObservedAtUnixNanos
		if continuous {
			item.CoverageStartUnixNanos = previous.emittedAt
		}
		result = append(result, item)
		if item.Active {
			r.flows[key] = observationState{fingerprint: fingerprint, emittedAt: item.ObservedAtUnixNanos, seenAt: item.ObservedAtUnixNanos}
		} else {
			delete(r.flows, key)
		}
	}
	return result
}

func observationFingerprint(item *pb.FlowObservation) [32]byte {
	stable := proto.Clone(item).(*pb.FlowObservation)
	stable.Id, stable.Sequence, stable.ObservedAtUnixNanos, stable.CoverageStartUnixNanos = "", 0, 0, 0
	stable.BytesSourceToDestination, stable.BytesDestinationToSource = 0, 0
	stable.PacketsSourceToDestination, stable.PacketsDestinationToSource = 0, 0
	// FlowObservation has no maps; deterministic encoding also preserves future
	// fields when deciding whether metadata changed.
	encoded, _ := proto.MarshalOptions{Deterministic: true}.Marshal(stable)
	return sha256.Sum256(encoded)
}
