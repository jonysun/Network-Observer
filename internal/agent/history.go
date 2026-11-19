package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	pb "github.com/network-observer/network-observer/gen/networkobserver/v1"
	"google.golang.org/protobuf/proto"
)

// historyAggregator preserves every collector sample in the current process
// long enough to accumulate exact byte and packet deltas. It emits cumulative
// minute rows with stable IDs, so a newer row replaces the previous row in
// ClickHouse instead of adding the same traffic a second time.
type historyAggregator struct {
	mu          sync.Mutex
	records     map[string]*historyAggregate
	byID        map[string]*historyAggregate
	lastSeen    map[string]historyCursor
	lastEmit    time.Time
	memoryBytes int64
}

type historyCursor struct {
	lastSeen       time.Time
	aggregateKey   string
	segmentStarted time.Time
}

type historyAggregate struct {
	observation *pb.FlowObservation
	bucket      time.Time
	dirty       bool
	sizeBytes   int64
}

func newHistoryAggregator() *historyAggregator {
	return &historyAggregator{
		records:  make(map[string]*historyAggregate),
		byID:     make(map[string]*historyAggregate),
		lastSeen: make(map[string]historyCursor),
	}
}

// add consumes raw observations. A new minute or a collection gap starts a
// separate segment, which prevents a later cumulative update from extending
// presence across an interval that was never observed. Byte and packet fields
// are deltas and are therefore summed exactly; total counters remain the latest
// observed totals for diagnostics.
func (a *historyAggregator) add(items []*pb.FlowObservation, interval time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if interval <= 0 {
		interval = time.Second
	}
	for _, item := range items {
		if item == nil || item.DnsMetadataOnly || item.FlowId == "" || item.ObservedAtUnixNanos == 0 {
			continue
		}
		observed := time.Unix(0, item.ObservedAtUnixNanos).UTC()
		flowKey := historyFlowKey(item)
		bucket := observed.Truncate(time.Minute)
		cursor, hasCursor := a.lastSeen[flowKey]
		continuous := hasCursor && observed.After(cursor.lastSeen) && observed.Sub(cursor.lastSeen) <= 2*interval && cursor.lastSeen.Truncate(time.Minute).Equal(bucket)

		key := cursor.aggregateKey
		aggregate := a.records[key]
		if !continuous || aggregate == nil {
			segmentStarted := observed
			key = historyAggregateKey(flowKey, bucket, segmentStarted)
			copyOf := proto.Clone(item).(*pb.FlowObservation)
			copyOf.Id = historyRecordID(flowKey, bucket, segmentStarted)
			copyOf.Sequence = 0
			copyOf.CoverageStartUnixNanos = observed.UnixNano()
			copyOf.BytesSourceToDestination = 0
			copyOf.BytesDestinationToSource = 0
			copyOf.PacketsSourceToDestination = 0
			copyOf.PacketsDestinationToSource = 0
			copyOf.RateBytesSourceToDestination = 0
			copyOf.RateBytesDestinationToSource = 0
			aggregate = &historyAggregate{observation: copyOf, bucket: bucket, dirty: true, sizeBytes: int64(proto.Size(copyOf))}
			a.records[key] = aggregate
			a.byID[copyOf.Id] = aggregate
			a.memoryBytes += aggregate.sizeBytes
		}

		observation := aggregate.observation
		oldSize := aggregate.sizeBytes
		observation.ObservedAtUnixNanos = observed.UnixNano()
		observation.Active = item.Active
		observation.State = item.State
		observation.BytesSourceToDestination += item.BytesSourceToDestination
		observation.BytesDestinationToSource += item.BytesDestinationToSource
		observation.PacketsSourceToDestination += item.PacketsSourceToDestination
		observation.PacketsDestinationToSource += item.PacketsDestinationToSource
		observation.TotalBytesSourceToDestination = item.TotalBytesSourceToDestination
		observation.TotalBytesDestinationToSource = item.TotalBytesDestinationToSource
		observation.RateBytesSourceToDestination = item.RateBytesSourceToDestination
		observation.RateBytesDestinationToSource = item.RateBytesDestinationToSource
		observation.GapReason = item.GapReason
		aggregate.sizeBytes = int64(proto.Size(observation))
		a.memoryBytes += aggregate.sizeBytes - oldSize
		aggregate.dirty = true

		if item.Active {
			a.lastSeen[flowKey] = historyCursor{lastSeen: observed, aggregateKey: key, segmentStarted: time.Unix(0, observation.CoverageStartUnixNanos).UTC()}
		} else {
			delete(a.lastSeen, flowKey)
		}
	}
}

// evictOldest removes only unpersisted aggregate rows when the in-memory
// backlog exceeds its caller supplied safety limits. Each row represents a
// complete minute/flow aggregate, so this does not affect the collector's
// per-second sampling or realtime state. The caller records the returned
// count as an explicit data gap; the next sample for an evicted flow starts a
// new segment instead of pretending that the missing interval was observed.
func (a *historyAggregator) evictOldest(maxRecords int, maxBytes int64) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if maxRecords <= 0 {
		maxRecords = 1
	}
	if maxBytes <= 0 {
		maxBytes = 1
	}
	if len(a.records) <= maxRecords && a.memoryBytes <= maxBytes {
		return 0
	}
	type candidate struct {
		key       string
		bucket    time.Time
		observed  int64
		sizeBytes int64
	}
	candidates := make([]candidate, 0, len(a.records))
	for key, item := range a.records {
		if item == nil || !item.dirty || item.observation == nil {
			continue
		}
		candidates = append(candidates, candidate{key: key, bucket: item.bucket, observed: item.observation.ObservedAtUnixNanos, sizeBytes: item.sizeBytes})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].bucket.Equal(candidates[j].bucket) {
			return candidates[i].bucket.Before(candidates[j].bucket)
		}
		if candidates[i].observed != candidates[j].observed {
			return candidates[i].observed < candidates[j].observed
		}
		return candidates[i].key < candidates[j].key
	})
	dropped := 0
	for _, item := range candidates {
		if len(a.records) <= maxRecords && a.memoryBytes <= maxBytes {
			break
		}
		aggregate := a.records[item.key]
		if aggregate == nil {
			continue
		}
		delete(a.records, item.key)
		a.memoryBytes -= aggregate.sizeBytes
		if a.memoryBytes < 0 {
			a.memoryBytes = 0
		}
		for flowKey, cursor := range a.lastSeen {
			if cursor.aggregateKey == item.key {
				delete(a.lastSeen, flowKey)
			}
		}
		if aggregate.observation != nil {
			delete(a.byID, aggregate.observation.Id)
		}
		dropped++
	}
	return dropped
}

func (a *historyAggregator) memoryUsage() (records int, bytes int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.records), a.memoryBytes
}

// flushDue emits completed minutes during normal operation. The current
// minute remains in memory and is flushed only when force is true (normally
// graceful shutdown), so a busy socket is written once per minute instead of
// once per collector tick. The aggregate stays dirty until markFlushed confirms
// that WAL append succeeded, so a full WAL causes a retry instead of silent loss.
func (a *historyAggregator) flushDue(now time.Time, force bool) []*pb.FlowObservation {
	a.mu.Lock()
	defer a.mu.Unlock()
	now = now.UTC()
	if !force && !a.lastEmit.IsZero() && now.Sub(a.lastEmit) < 10*time.Second {
		return nil
	}
	currentBucket := now.Truncate(time.Minute)
	keys := make([]string, 0, len(a.records))
	for key, item := range a.records {
		if item != nil && item.dirty && (force || item.bucket.Before(currentBucket)) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	result := make([]*pb.FlowObservation, 0, len(keys))
	for _, key := range keys {
		item := a.records[key]
		if item == nil || !item.dirty || item.observation == nil {
			continue
		}
		result = append(result, proto.Clone(item.observation).(*pb.FlowObservation))
	}
	if len(result) > 0 {
		a.lastEmit = now
	}
	for key, item := range a.records {
		if item != nil && item.bucket.Before(currentBucket) && !item.dirty {
			a.memoryBytes -= item.sizeBytes
			if a.memoryBytes < 0 {
				a.memoryBytes = 0
			}
			delete(a.records, key)
		}
	}
	return result
}

func (a *historyAggregator) markFlushed(items []*pb.FlowObservation) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, item := range items {
		if item == nil || item.Id == "" {
			continue
		}
		aggregate := a.byID[item.Id]
		if aggregate == nil || aggregate.observation == nil {
			continue
		}
		// A collector sample may arrive while the WAL write is in progress.
		// Only clear dirty when the persisted cumulative values still match.
		if aggregate.observation.ObservedAtUnixNanos == item.ObservedAtUnixNanos &&
			aggregate.observation.BytesSourceToDestination == item.BytesSourceToDestination &&
			aggregate.observation.BytesDestinationToSource == item.BytesDestinationToSource &&
			aggregate.observation.PacketsSourceToDestination == item.PacketsSourceToDestination &&
			aggregate.observation.PacketsDestinationToSource == item.PacketsDestinationToSource {
			aggregate.dirty = false
		}
	}
}

func historyFlowKey(item *pb.FlowObservation) string {
	stable := proto.Clone(item).(*pb.FlowObservation)
	stable.Id = ""
	stable.Sequence = 0
	stable.ObservedAtUnixNanos = 0
	stable.CoverageStartUnixNanos = 0
	stable.BytesSourceToDestination = 0
	stable.BytesDestinationToSource = 0
	stable.TotalBytesSourceToDestination = 0
	stable.TotalBytesDestinationToSource = 0
	stable.RateBytesSourceToDestination = 0
	stable.RateBytesDestinationToSource = 0
	stable.PacketsSourceToDestination = 0
	stable.PacketsDestinationToSource = 0
	stable.Active = false
	stable.State = ""
	stable.GapReason = ""
	encoded, _ := proto.MarshalOptions{Deterministic: true}.Marshal(stable)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func historyAggregateKey(flowKey string, bucket, segmentStarted time.Time) string {
	return fmt.Sprintf("%s:%d:%d", flowKey, bucket.Unix(), segmentStarted.UnixNano())
}

func historyRecordID(flowKey string, bucket, segmentStarted time.Time) string {
	digest := sha256.Sum256([]byte(historyAggregateKey(flowKey, bucket, segmentStarted)))
	return "history-" + hex.EncodeToString(digest[:16])
}
