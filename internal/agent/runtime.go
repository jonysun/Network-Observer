package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	pb "github.com/network-observer/network-observer/gen/networkobserver/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// These are safety bounds for data that has not yet reached the durable WAL.
// They are deliberately large enough for the supported home-lab scale and do
// not change the collector interval or the fields emitted by a collector. They
// only apply after the WAL is full or otherwise unable to accept more data.
const (
	maxPendingHistoryAggregates = 100_000
	maxPendingHistoryBytes      = 64 << 20
	maxPendingDNSRecords        = 50_000
	maxPendingDNSBytes          = 16 << 20
)

type runtimeState struct {
	mu                 sync.Mutex
	capabilities       []*pb.Capability
	neighborSnapshot   *pb.NeighborSnapshot
	neighborCapability *pb.Capability
	coverageSupported  bool
	// realtimeLatest is the complete current active-flow state. It is used for
	// the first message after a reconnect so the Center can rebuild its cache.
	realtimeLatest map[string]*pb.FlowObservation
	// realtimePending contains only the newest change for each flow. Keeping a
	// keyed pending set bounds memory by the number of flows instead of the
	// number of collector ticks.
	realtimePending   map[string]*pb.FlowObservation
	realtimeObservers map[string]struct{}
}

func (s *runtimeState) setCoverageSupported(supported bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.coverageSupported = supported
}

func (s *runtimeState) supportsCoverage() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.coverageSupported
}

func deviceCredential(config Config) string {
	if config.DeviceCredential != "" {
		return config.DeviceCredential
	}
	// Compatibility with pre-v1.0 configurations that used the persistent
	// token directly. New registrations always persist DeviceCredential.
	return config.EnrollmentToken
}

func (s *runtimeState) set(items []*pb.Capability) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.capabilities = items
}

func realtimeFlowKey(item *pb.FlowObservation) string {
	return item.ObserverId + "\x00" + item.FlowId
}

func validRealtimeObservation(item *pb.FlowObservation) bool {
	return item != nil && !item.DnsMetadataOnly && item.FlowId != ""
}

// updateRealtime applies a complete collector snapshot and a reduced change
// set. The collector still provides every socket every tick; only the change
// set is sent over the live stream. Missing flows are converted to close events
// so the Center removes them without requiring a high-frequency full snapshot.
func (s *runtimeState) updateRealtime(full, changes []*pb.FlowObservation) {
	s.updateRealtimeForObserver("", full, changes)
}

func (s *runtimeState) updateRealtimeForObserver(observerID string, full, changes []*pb.FlowObservation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.realtimeLatest == nil {
		s.realtimeLatest = make(map[string]*pb.FlowObservation)
	}
	if s.realtimePending == nil {
		s.realtimePending = make(map[string]*pb.FlowObservation)
	}
	if s.realtimeObservers == nil {
		s.realtimeObservers = make(map[string]struct{})
	}
	if observerID != "" {
		s.realtimeObservers[observerID] = struct{}{}
	}

	active := make(map[string]struct{}, len(full))
	observers := make(map[string]struct{})
	if observerID != "" {
		observers[observerID] = struct{}{}
	}
	for _, item := range full {
		if !validRealtimeObservation(item) {
			continue
		}
		observers[item.ObserverId] = struct{}{}
		if !item.Active {
			continue
		}
		key := realtimeFlowKey(item)
		active[key] = struct{}{}
		s.realtimeLatest[key] = proto.Clone(item).(*pb.FlowObservation)
	}

	// A successful collector snapshot is authoritative for the observer. Emit a
	// synthetic close for a flow that disappeared without an explicit close
	// record, preserving the live view under collectors that only return current
	// sockets.
	for key, previous := range s.realtimeLatest {
		_, scoped := observers[previous.ObserverId]
		if !scoped {
			continue
		}
		if _, exists := active[key]; exists {
			continue
		}
		closed := proto.Clone(previous).(*pb.FlowObservation)
		closed.Active = false
		closed.State = "CLOSED"
		closed.ObservedAtUnixNanos = time.Now().UnixNano()
		closed.BytesSourceToDestination = 0
		closed.BytesDestinationToSource = 0
		closed.PacketsSourceToDestination = 0
		closed.PacketsDestinationToSource = 0
		closed.RateBytesSourceToDestination = 0
		closed.RateBytesDestinationToSource = 0
		delete(s.realtimeLatest, key)
		s.realtimePending[key] = closed
	}

	for _, item := range changes {
		if !validRealtimeObservation(item) {
			continue
		}
		key := realtimeFlowKey(item)
		copyOf := proto.Clone(item).(*pb.FlowObservation)
		s.realtimePending[key] = copyOf
		if copyOf.Active {
			s.realtimeLatest[key] = proto.Clone(copyOf).(*pb.FlowObservation)
		} else {
			delete(s.realtimeLatest, key)
		}
	}
}

// setRealtime is retained for focused tests and compatibility with older
// callers. New collection code should call updateRealtime.
func (s *runtimeState) setRealtime(items []*pb.FlowObservation) {
	s.updateRealtime(items, items)
}

// realtimeSnapshot returns the complete current active-flow state in a stable
// order. It does not consume pending changes.
func (s *runtimeState) realtimeSnapshot() []*pb.FlowObservation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.realtimeSnapshotLocked()
}

// realtimeSnapshotAndResetPending is used immediately after authentication.
// It consumes changes already represented by the full snapshot, while changes
// produced after this call remain pending and are sent as deltas.
func (s *runtimeState) realtimeSnapshotAndResetPending() []*pb.FlowObservation {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := s.realtimeSnapshotLocked()
	s.realtimePending = make(map[string]*pb.FlowObservation)
	return result
}

func (s *runtimeState) realtimeSnapshotLocked() []*pb.FlowObservation {
	result := make([]*pb.FlowObservation, 0, len(s.realtimeLatest))
	for _, item := range s.realtimeLatest {
		result = append(result, proto.Clone(item).(*pb.FlowObservation))
	}
	sort.Slice(result, func(i, j int) bool {
		return realtimeFlowKey(result[i]) < realtimeFlowKey(result[j])
	})
	return result
}

// takeRealtime returns the newest change per flow and removes only the copy
// handed to the sender. Changes arriving while a batch is in flight remain in
// realtimePending for the next iteration.
func (s *runtimeState) takeRealtime() []*pb.FlowObservation {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.realtimePending) == 0 {
		return nil
	}
	keys := make([]string, 0, len(s.realtimePending))
	for key := range s.realtimePending {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]*pb.FlowObservation, 0, len(keys))
	for _, key := range keys {
		result = append(result, proto.Clone(s.realtimePending[key]).(*pb.FlowObservation))
		delete(s.realtimePending, key)
	}
	return result
}

func (s *runtimeState) report(wal *WAL) []*pb.Capability {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]*pb.Capability, 0, len(s.capabilities)+2)
	for _, item := range s.capabilities {
		if item != nil {
			items = append(items, proto.Clone(item).(*pb.Capability))
		}
	}
	// The Center uses this metadata-only capability to refresh the version of
	// an already registered device after an in-place Agent upgrade. It is
	// removed before capabilities are persisted, so it never appears as a
	// health status in the Web console.
	items = append(items, &pb.Capability{Name: "agent_version", Status: "metadata", Detail: agentVersion})
	items = append(items, agentUpgradeCapability())
	if s.neighborCapability != nil {
		items = append(items, proto.Clone(s.neighborCapability).(*pb.Capability))
	}
	pending, used, oldest, _, _ := wal.Stats()
	gap := wal.GapStatus()
	state := "supported"
	reason := "buffering and replay available"
	if used >= wal.maxBytes*9/10 {
		state = "degraded"
		reason = "WAL capacity pressure"
	}
	if oldest != nil && time.Since(time.Unix(0, oldest.ObservedAtUnixNanos)) > 24*time.Hour {
		state = "degraded"
		reason = "oldest unacknowledged observation exceeded 24h"
	}
	items = append(items, &pb.Capability{Name: "local_wal", Status: state,
		Detail:             fmt.Sprintf("%s; %d pending; %d/%d bytes; 24h age limit; old unacknowledged records are preserved", reason, pending, used, wal.maxBytes),
		CheckedAtUnixNanos: time.Now().UnixNano()})
	if !gap.First.IsZero() {
		items = append(items, &pb.Capability{Name: "data_gap", Status: "historical",
			Detail:             fmt.Sprintf("historical gap: %d observations skipped; first=%s; last=%s; reason=%s", gap.Dropped, gap.First.Format(time.RFC3339), gap.Last.Format(time.RFC3339), gap.Reason),
			CheckedAtUnixNanos: time.Now().UnixNano()})
	}
	return items
}

func (s *runtimeState) setNeighbors(observations []*pb.NeighborObservation, dockerNetworks []*pb.DockerNetwork, dockerTopologyComplete bool, observedAt int64, capability *pb.Capability) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]*pb.NeighborObservation, 0, len(observations))
	for _, item := range observations {
		if item != nil {
			items = append(items, proto.Clone(item).(*pb.NeighborObservation))
		}
	}
	networks := make([]*pb.DockerNetwork, 0, len(dockerNetworks))
	for _, network := range dockerNetworks {
		if network != nil {
			networks = append(networks, proto.Clone(network).(*pb.DockerNetwork))
		}
	}
	s.neighborSnapshot = &pb.NeighborSnapshot{
		ObservedAtUnixNanos: observedAt, Observations: items, DockerNetworks: networks,
		DockerTopologyComplete: dockerTopologyComplete,
	}
	if capability != nil {
		s.neighborCapability = proto.Clone(capability).(*pb.Capability)
	}
}

func (s *runtimeState) neighbors() *pb.NeighborSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.neighborSnapshot == nil {
		return &pb.NeighborSnapshot{}
	}
	return proto.Clone(s.neighborSnapshot).(*pb.NeighborSnapshot)
}

func Run(ctx context.Context, configPath string, logger *slog.Logger) error {
	config, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	if logger == nil {
		logger = slog.Default()
	}
	wal, err := OpenWAL(config.WALPath, config.WALMaxBytes)
	if err != nil {
		return err
	}
	defer wal.Close()
	collector := newConfiguredCollector(config)
	if closer, ok := collector.(interface{ Close() error }); ok {
		defer func() {
			if err := closer.Close(); err != nil {
				logger.Warn("close collector resources", "error", err)
			}
		}()
	}
	if config.ObserverType == "endpoint" && config.EbpfMode == "on" && !config.DisableSocketDiag {
		if validator, ok := collector.(interface{ ValidateRequired() error }); ok {
			if err := validator.ValidateRequired(); err != nil {
				return err
			}
		}
	}
	return run(ctx, config, configPath, wal, collector, logger)
}

func run(ctx context.Context, config Config, configPath string, wal *WAL, collector Collector, logger *slog.Logger) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	state := &runtimeState{}
	changed := make(chan struct{}, 1)
	collectionResult := make(chan error, 1)
	collectionDone := make(chan struct{})
	go func() {
		defer close(collectionDone)
		collectionResult <- collectLoop(runCtx, config, wal, collector, state, changed, logger)
		cancel()
	}()
	defer func() {
		cancel()
		<-collectionDone
	}()
	desired := config // Only the stream loop owns the mutable DNS configuration.
	applyConfig := func(enabled bool, revision int64) error {
		return applyDNSConfig(&desired, configPath, collector, enabled, revision, logger)
	}
	backoff := time.Second
	for runCtx.Err() == nil {
		started := time.Now()
		err := runSession(runCtx, config, wal, state, changed, logger, applyConfig)
		if status.Code(err) == codes.Unauthenticated {
			return fmt.Errorf("device credential rejected; agent stopped: %w", err)
		}
		if runCtx.Err() != nil {
			break
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		logger.Warn("agent stream disconnected", "error", err, "retry_in", backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-runCtx.Done():
			timer.Stop()
		case <-timer.C:
		}
		backoff = min(30*time.Second, backoff*2)
	}
	select {
	case err := <-collectionResult:
		return err
	default:
		return nil
	}
}

func applyDNSConfig(config *Config, path string, collector Collector, enabled bool, revision int64, logger *slog.Logger) error {
	if revision <= config.ConfigRevision || revision <= 0 {
		return nil
	}
	next := *config
	next.DNSMetadataEnabled, next.ConfigRevision = enabled, revision
	if err := SaveConfig(path, next); err != nil {
		return fmt.Errorf("persist DNS configuration revision %d: %w", revision, err)
	}
	*config = next
	if target, ok := collector.(dnsConfigurableCollector); ok {
		if err := target.SetDNSMetadataEnabled(enabled); err != nil {
			logger.Warn("update DNS collector", "error", err)
		}
	}
	return nil
}

func evictHistoryIfPressured(history *historyAggregator, walPressure bool, maxRecords int, maxBytes int64) int {
	if !walPressure || history == nil {
		return 0
	}
	return history.evictOldest(maxRecords, maxBytes)
}

func collectLoop(ctx context.Context, config Config, wal *WAL, collector Collector, state *runtimeState, changed chan<- struct{}, logger *slog.Logger) error {
	interval := time.Duration(config.SnapshotIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastNeighborSnapshot := time.Time{}
	history := newHistoryAggregator()
	reducer := &observationReducer{}
	var pendingDNS []*pb.FlowObservation
	var pendingDNSBytes int64
	walPressure := false

	recordGap := func(count uint64, reason string) error {
		if count == 0 {
			return nil
		}
		if err := wal.RecordGap(count, reason); err != nil {
			return fmt.Errorf("record data gap (%s): %w", reason, err)
		}
		logger.Warn("agent durable data dropped after bounded backlog was exhausted", "count", count, "reason", reason)
		return nil
	}

	queueDNS := func(observation *pb.FlowObservation) error {
		if observation == nil {
			return nil
		}
		pendingDNS = append(pendingDNS, observation)
		pendingDNSBytes += int64(proto.Size(observation))
		dropped := uint64(0)
		for len(pendingDNS) > maxPendingDNSRecords || pendingDNSBytes > maxPendingDNSBytes {
			oldest := pendingDNS[0]
			pendingDNS = pendingDNS[1:]
			pendingDNSBytes -= int64(proto.Size(oldest))
			if pendingDNSBytes < 0 {
				pendingDNSBytes = 0
			}
			dropped++
		}
		return recordGap(dropped, "pending DNS metadata memory limit")
	}

	appendDurable := func(historyItems []*pb.FlowObservation) error {
		if len(historyItems) == 0 && len(pendingDNS) == 0 {
			return nil
		}
		items := make([]*pb.FlowObservation, 0, len(historyItems)+len(pendingDNS))
		items = append(items, historyItems...)
		items = append(items, pendingDNS...)
		appended, err := wal.AppendBatch(items)
		if appended > 0 {
			// A successful append means durable capacity accepted data again.
			walPressure = false
			historyAccepted := appended
			if historyAccepted > len(historyItems) {
				historyAccepted = len(historyItems)
			}
			if historyAccepted > 0 {
				history.markFlushed(historyItems[:historyAccepted])
			}
			dnsAccepted := appended - historyAccepted
			if dnsAccepted > len(pendingDNS) {
				dnsAccepted = len(pendingDNS)
			}
			if dnsAccepted > 0 {
				for _, item := range pendingDNS[:dnsAccepted] {
					pendingDNSBytes -= int64(proto.Size(item))
				}
				if pendingDNSBytes < 0 {
					pendingDNSBytes = 0
				}
				pendingDNS = pendingDNS[dnsAccepted:]
			}
		}
		if errors.Is(err, ErrWALFull) || errors.Is(err, ErrWALTooOld) {
			walPressure = true
			// Keep unacknowledged aggregates and DNS metadata in memory and retry
			// after the next flush interval. Neither path is declared dropped merely
			// because the WAL is temporarily full.
			logger.Warn("agent durable data waiting for WAL capacity", "error", err, "history_pending", len(historyItems), "dns_pending", len(pendingDNS))
			return nil
		}
		return err
	}

	flushHistory := func(force bool) error {
		items := history.flushDue(time.Now().UTC(), force)
		return appendDurable(items)
	}

	for {
		if ctx.Err() != nil {
			_ = flushHistory(true)
			return nil
		}
		observations, capabilities, err := collector.Snapshot(ctx)
		if err != nil {
			reducer.reset()
			if ctx.Err() != nil {
				_ = flushHistory(true)
				return nil
			}
			logger.Warn("endpoint snapshot failed", "error", err)
			capabilities = append(capabilities, &pb.Capability{Name: "endpoint_collection", Status: "degraded", Detail: err.Error(), CheckedAtUnixNanos: time.Now().UnixNano()})
			if err := wal.RecordGap(0, "endpoint snapshot failed"); err != nil {
				return err
			}
		} else {
			for _, observation := range observations {
				if observation == nil {
					return errors.New("collector returned a nil observation")
				}
				observation.DeviceId = config.DeviceID
				observation.ObserverId = config.DeviceID + ":" + config.ObserverType
				observation.ObserverType = config.ObserverType
			}
			// The collector still samples every configured interval. Raw socket
			// observations go to the exact minute accumulator; only the live
			// transport suppresses unchanged idle snapshots.
			flowObservations := make([]*pb.FlowObservation, 0, len(observations))
			for _, observation := range observations {
				if observation.DnsMetadataOnly {
					// DNS metadata remains durable through the sequence/WAL path; it
					// is not a live connection and must not enter history aggregation.
					if err := queueDNS(observation); err != nil {
						return err
					}
					continue
				}
				flowObservations = append(flowObservations, observation)
			}
			history.add(flowObservations, interval)
			state.updateRealtimeForObserver(config.DeviceID+":"+config.ObserverType, flowObservations, reducer.reduce(flowObservations, true, interval))
			if err := flushHistory(false); err != nil {
				return err
			}
			// Never evict a history aggregate while the WAL is healthy. This
			// gate is set only after AppendBatch reports that durable capacity
			// is exhausted, so normal operation retains every minute/flow row.
			if walPressure && !wal.UnderPressure() {
				walPressure = false
			}
			if dropped := evictHistoryIfPressured(history, walPressure, maxPendingHistoryAggregates, maxPendingHistoryBytes); dropped != 0 {
				if err := recordGap(uint64(dropped), "pending history aggregate memory limit while WAL is pressured"); err != nil {
					return err
				}
			}
		}
		state.set(capabilities)
		if source, ok := collector.(NeighborCollector); ok && (lastNeighborSnapshot.IsZero() || time.Since(lastNeighborSnapshot) >= 30*time.Second) {
			neighbors, capability := source.NeighborSnapshot(ctx)
			var dockerNetworks []*pb.DockerNetwork
			dockerTopologyComplete := false
			if dockerSource, supported := collector.(DockerNetworkCollector); supported {
				var dockerCapability *pb.Capability
				dockerNetworks, dockerCapability = dockerSource.DockerNetworkSnapshot(ctx)
				if dockerCapability != nil {
					state.set(append(capabilities, dockerCapability))
					dockerTopologyComplete = dockerCapability.Status == "supported"
				}
			}
			state.setNeighbors(neighbors, dockerNetworks, dockerTopologyComplete, time.Now().UTC().UnixNano(), capability)
			lastNeighborSnapshot = time.Now()
		}
		select {
		case changed <- struct{}{}:
		default:
		}
		select {
		case <-ctx.Done():
			_ = flushHistory(true)
			return nil
		case <-ticker.C:
		}
	}
}

func runSession(ctx context.Context, config Config, wal *WAL, state *runtimeState, changed <-chan struct{}, logger *slog.Logger, applyConfig func(bool, int64) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	connection, err := dial(ctx, config.ServerAddress, config.TLSServerName, config.CACertFile, config.SecurityMode)
	if err != nil {
		return err
	}
	defer connection.Close()
	stream, err := pb.NewAgentServiceClient(connection).Connect(ctx)
	if err != nil {
		return fmt.Errorf("open agent stream: %w", err)
	}
	_, _, _, lastAcked, _ := wal.Stats()
	if err := sendWithin(ctx, cancel, stream, &pb.AgentMessage{Payload: &pb.AgentMessage_Authenticate{Authenticate: &pb.AuthenticateRequest{
		DeviceId: config.DeviceID, DeviceCredential: deviceCredential(config), LastAcknowledgedSequence: lastAcked,
		AcceptsConfigUpdates: applyConfig != nil,
	}}}); err != nil {
		return err
	}
	response, err := receiveWithin(ctx, cancel, stream)
	if err != nil {
		return err
	}
	authenticated := response.GetAuthenticated()
	if authenticated == nil {
		return errors.New("server did not authenticate agent stream")
	}
	if applyConfig != nil {
		if err := applyConfig(authenticated.DnsMetadataEnabled, authenticated.ConfigRevision); err != nil {
			return err
		}
	}
	incoming := receiveMessages(ctx, stream)
	handleUpgrade := func(request *pb.AgentUpgradeRequest) error {
		if request == nil {
			return errors.New("server sent an empty Agent upgrade request")
		}
		progress := func(state, message string) error {
			version := ""
			if state == "restarting" {
				version = request.Version
			}
			return sendUpgradeStatus(ctx, cancel, stream, request.TaskId, state, message, version)
		}
		if err := performAgentUpgrade(ctx, config, request, progress); err != nil {
			if reportErr := sendUpgradeStatus(ctx, cancel, stream, request.TaskId, "failed", err.Error(), ""); reportErr != nil {
				return reportErr
			}
			return nil
		}
		return nil
	}
	handleResponse := func(result streamResponse) error { return result.handle(applyConfig, handleUpgrade) }
	state.setCoverageSupported(authenticated.SupportsObservationCoverage)
	volatileGap, err := wal.ReconcileWithMode(authenticated.NextSequence, config.VolatileWAL)
	if err != nil {
		return err
	}
	if volatileGap {
		if err := wal.RecordGap(0, "volatile WAL was empty after restart; observations after the last acknowledged sequence may be missing"); err != nil {
			return err
		}
	}
	pending, walBytes, _, _, _ := wal.Stats()
	logger.Info("agent stream authenticated", "device_id", config.DeviceID, "pending_observations", pending, "wal_bytes", walBytes)
	heartbeatInterval := time.Duration(authenticated.HeartbeatIntervalSeconds) * time.Second
	if heartbeatInterval < time.Second || heartbeatInterval > time.Minute {
		heartbeatInterval = 15 * time.Second
	}
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	poll := time.NewTicker(200 * time.Millisecond)
	defer poll.Stop()
	lastDigest := [32]byte{}
	lastCapabilitySent := time.Time{}
	lastNeighborSent := time.Time{}
	lastProgressLog := time.Time{}
	snapshotSent := false
	for {
		select {
		case result := <-incoming:
			if err := handleResponse(result); err != nil {
				return err
			}
		default:
		}
		capabilities := state.report(wal)
		if digest := capabilityDigest(capabilities); digest != lastDigest && (lastCapabilitySent.IsZero() || time.Since(lastCapabilitySent) >= time.Second) {
			if err := sendWithin(ctx, cancel, stream, &pb.AgentMessage{Payload: &pb.AgentMessage_Capabilities{Capabilities: &pb.CapabilityReport{Capabilities: capabilities}}}); err != nil {
				return err
			}
			lastDigest = digest
			lastCapabilitySent = time.Now()
		}
		if !snapshotSent {
			if snapshot := state.realtimeSnapshotAndResetPending(); len(snapshot) != 0 {
				for offset := 0; offset < len(snapshot); offset += 500 {
					end := offset + 500
					if end > len(snapshot) {
						end = len(snapshot)
					}
					if err := sendWithin(ctx, cancel, stream, &pb.AgentMessage{Payload: &pb.AgentMessage_Realtime{Realtime: &pb.RealtimeBatch{Observations: snapshot[offset:end]}}}); err != nil {
						return err
					}
				}
			}
			snapshotSent = true
		}
		if realtime := state.takeRealtime(); len(realtime) != 0 {
			for offset := 0; offset < len(realtime); offset += 500 {
				end := offset + 500
				if end > len(realtime) {
					end = len(realtime)
				}
				if err := sendWithin(ctx, cancel, stream, &pb.AgentMessage{Payload: &pb.AgentMessage_Realtime{Realtime: &pb.RealtimeBatch{Observations: realtime[offset:end]}}}); err != nil {
					return err
				}
			}
		}
		if lastNeighborSent.IsZero() || time.Since(lastNeighborSent) >= 30*time.Second {
			if err := sendWithin(ctx, cancel, stream, &pb.AgentMessage{Payload: &pb.AgentMessage_NeighborSnapshot{NeighborSnapshot: state.neighbors()}}); err != nil {
				return err
			}
			lastNeighborSent = time.Now()
		}
		// One bounded batch per iteration keeps heartbeat/status updates responsive during replay.
		batch, err := wal.Pending(500)
		if err != nil {
			return fmt.Errorf("read pending WAL batch: %w", err)
		}
		if len(batch) != 0 {
			batchStarted := time.Now()
			first, last := batch[0].Sequence, batch[len(batch)-1].Sequence
			if err := sendWithin(ctx, cancel, stream, &pb.AgentMessage{Payload: &pb.AgentMessage_Observations{Observations: &pb.ObservationBatch{
				Observations: batch, FirstSequence: first, LastSequence: last,
			}}}); err != nil {
				return err
			}
			response, err := receiveUntilAck(ctx, incoming, handleResponse)
			if err != nil {
				return err
			}
			ack := response.GetAck()
			if ack == nil || ack.AcknowledgedSequence != last {
				return fmt.Errorf("invalid acknowledgement for batch %d-%d", first, last)
			}
			if err := wal.AckThrough(last); err != nil {
				return err
			}
			if lastProgressLog.IsZero() || time.Since(lastProgressLog) >= 15*time.Second {
				pending, walBytes, oldest, _, _ := wal.Stats()
				oldestAgeSeconds := int64(0)
				if oldest != nil {
					oldestAgeSeconds = max(int64(0), int64(time.Since(time.Unix(0, oldest.ObservedAtUnixNanos)).Seconds()))
				}
				logger.Info("agent WAL batch acknowledged", "device_id", config.DeviceID,
					"acknowledged_sequence", last, "batch_observations", len(batch),
					"pending_observations", pending, "wal_bytes", walBytes,
					"ack_latency_ms", time.Since(batchStarted).Milliseconds(), "oldest_age_seconds", oldestAgeSeconds)
				lastProgressLog = time.Now()
			}
		}
		// Check cancellation and heartbeats between every bounded batch. While
		// replaying, proceed immediately instead of imposing a 200ms batch delay.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-heartbeat.C:
			_, _, _, _, sequence := wal.Stats()
			if err := sendWithin(ctx, cancel, stream, &pb.AgentMessage{Payload: &pb.AgentMessage_Heartbeat{Heartbeat: &pb.Heartbeat{
				Sequence: sequence, SentAtUnixNanos: time.Now().UnixNano(),
			}}}); err != nil {
				return err
			}
		default:
		}
		if len(batch) != 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result := <-incoming:
			if err := handleResponse(result); err != nil {
				return err
			}
		case <-changed:
		case <-poll.C:
		}
	}
}

type streamResponse struct {
	message *pb.ServerMessage
	err     error
}

func (r streamResponse) handle(applyConfig func(bool, int64) error, handleUpgrade func(*pb.AgentUpgradeRequest) error) error {
	if r.err != nil {
		return r.err
	}
	if msg := r.message.GetError(); msg != nil {
		if msg.Retryable {
			return status.Error(codes.Unavailable, msg.Message)
		}
		return status.Error(codes.InvalidArgument, msg.Message)
	}
	if update := r.message.GetConfig(); update != nil && applyConfig != nil {
		return applyConfig(update.DnsMetadataEnabled, update.ConfigRevision)
	}
	if upgrade := r.message.GetUpgradeRequest(); upgrade != nil && handleUpgrade != nil {
		return handleUpgrade(upgrade)
	}
	return nil
}

// An idle Agent must still receive configuration, without issuing concurrent
// Recv calls when a durable batch is waiting for its acknowledgement.
func receiveMessages(ctx context.Context, stream grpc.BidiStreamingClient[pb.AgentMessage, pb.ServerMessage]) <-chan streamResponse {
	incoming := make(chan streamResponse, 1)
	go func() {
		for {
			message, err := stream.Recv()
			select {
			case incoming <- streamResponse{message, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return incoming
}

func receiveUntilAck(ctx context.Context, incoming <-chan streamResponse, handleResponse func(streamResponse) error) (*pb.ServerMessage, error) {
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, errors.New("agent acknowledgement timed out")
		case result := <-incoming:
			if err := handleResponse(result); err != nil {
				return nil, err
			}
			if result.message.GetAck() != nil {
				return result.message, nil
			}
		}
	}
}

func sendWithin(ctx context.Context, cancel context.CancelFunc, stream grpc.BidiStreamingClient[pb.AgentMessage, pb.ServerMessage], message *pb.AgentMessage) error {
	result := make(chan error, 1)
	go func() { result <- stream.Send(message) }()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		cancel()
		return errors.New("agent send timed out")
	}
}

func receiveWithin(ctx context.Context, cancel context.CancelFunc, stream grpc.BidiStreamingClient[pb.AgentMessage, pb.ServerMessage]) (*pb.ServerMessage, error) {
	type response struct {
		message *pb.ServerMessage
		err     error
	}
	result := make(chan response, 1)
	go func() { message, err := stream.Recv(); result <- response{message, err} }()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case result := <-result:
		return result.message, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		cancel()
		return nil, errors.New("agent acknowledgement timed out")
	}
}
