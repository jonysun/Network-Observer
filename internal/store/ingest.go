package store

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/network-observer/network-observer/internal/domain"
)

const trafficDedupWindow = 30 * time.Second

func (s *Store) IngestObservations(ctx context.Context, deviceID string, observations []domain.FlowObservation) (uint64, error) {
	tx, err := s.Postgres.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var last int64
	if err := tx.QueryRow(ctx, `SELECT last_sequence FROM ingestion_checkpoints WHERE device_id = $1 FOR UPDATE`, deviceID).Scan(&last); err != nil {
		return 0, err
	}
	if last < 0 {
		return 0, fmt.Errorf("invalid ingestion checkpoint")
	}
	newObservations := make([]domain.FlowObservation, 0, len(observations))
	for _, observation := range observations {
		if observation.DeviceID != deviceID {
			return uint64(last), fmt.Errorf("observation device does not match authenticated device")
		}
		if observation.Sequence <= uint64(last) {
			continue
		}
		if observation.ID == "" || observation.FlowID == "" || observation.ObservedAt.IsZero() {
			return uint64(last), fmt.Errorf("observation requires id, flow_id, and observed_at")
		}
		if observation.Protocol != domain.ProtocolTCP && observation.Protocol != domain.ProtocolUDP {
			return uint64(last), fmt.Errorf("unsupported protocol %q", observation.Protocol)
		}
		if observation.SamplingRate > 1 && observation.Confidence != "estimated" {
			return uint64(last), fmt.Errorf("sampled gateway observations must use estimated confidence")
		}
		newObservations = append(newObservations, observation)
	}
	if len(newObservations) == 0 {
		return uint64(last), tx.Commit(ctx)
	}
	for i, observation := range newObservations {
		if observation.Sequence != uint64(last)+uint64(i)+1 {
			return uint64(last), fmt.Errorf("sequence gap: expected %d, got %d", uint64(last)+uint64(i)+1, observation.Sequence)
		}
	}
	if err := s.associateTrafficPerspectives(ctx, tx, newObservations); err != nil {
		return uint64(last), err
	}

	if err := s.insertObservations(ctx, newObservations); err != nil {
		return uint64(last), err
	}
	newLast := newObservations[len(newObservations)-1].Sequence
	if _, err := tx.Exec(ctx, `UPDATE ingestion_checkpoints SET last_sequence = $2, updated_at = now() WHERE device_id = $1`, deviceID, int64(newLast)); err != nil {
		return uint64(last), err
	}
	result, err := tx.Exec(ctx, `UPDATE devices SET last_seen = now() WHERE id = $1 AND revoked_at IS NULL`, deviceID)
	if err != nil {
		return uint64(last), err
	}
	if result.RowsAffected() == 0 {
		return uint64(last), fmt.Errorf("device credential revoked")
	}
	if err := tx.Commit(ctx); err != nil {
		return uint64(last), err
	}
	return newLast, nil
}

func (s *Store) insertObservations(ctx context.Context, observations []domain.FlowObservation) error {
	live, err := s.ClickHouseNative.PrepareBatch(ctx, `INSERT INTO observer.live_connections (
		device_id, flow_id, observer_type, observer_id, local_is_source, observed_at, interface, network_namespace,
		protocol, state, source_ip, source_port, destination_ip, destination_port, nat_source_ip, nat_source_port,
		nat_destination_ip, nat_destination_port, bytes_source_to_dest, bytes_dest_to_source,
		rate_bytes_source_to_dest, rate_bytes_dest_to_source, pid, process_name, container_id, container_name,
		service_name, attribution_source, attribution_reason, sampling_rate, counter_source, confidence, active, version
	)`)
	if err != nil {
		return err
	}
	traffic, err := s.ClickHouseNative.PrepareBatch(ctx, `INSERT INTO observer.traffic_minute (
		event_key, bucket, dimension_type, dimension_value, device_id, interface, protocol, direction, bytes, packets, estimated, version
	)`)
	if err != nil {
		return err
	}

	for _, observation := range observations {
		observedAt := observation.ObservedAt.UTC()
		version := observedAt
		if err := live.Append(
			observation.DeviceID, observation.FlowID, string(observation.ObserverType), observation.ObserverID,
			observation.LocalIsSource, observedAt, observation.Interface, observation.NetworkNamespace,
			string(observation.Protocol), observation.State, observation.SourceIP, observation.SourcePort,
			observation.DestinationIP, observation.DestinationPort, observation.NATSourceIP, observation.NATSourcePort,
			observation.NATDestinationIP, observation.NATDestinationPort, observation.TotalBytesSourceToDest,
			observation.TotalBytesDestToSource, observation.RateBytesSourceToDest, observation.RateBytesDestToSource,
			observation.PID, observation.ProcessName, observation.ContainerID, observation.ContainerName,
			observation.ServiceName, string(observation.AttributionSource), observation.AttributionReason,
			observation.SamplingRate, observation.CounterSource, observation.Confidence, observation.Active, version,
		); err != nil {
			return err
		}
		if err := appendTraffic(traffic, observation); err != nil {
			return err
		}
	}
	if err := live.Send(); err != nil {
		return fmt.Errorf("insert live connections: %w", err)
	}
	if err := traffic.Send(); err != nil {
		return fmt.Errorf("insert traffic summaries: %w", err)
	}
	return nil
}

func appendTraffic(batch interface {
	Append(...any) error
}, observation domain.FlowObservation) error {
	if observation.TrafficSuppressed {
		return nil
	}
	if observation.BytesSourceToDest == 0 && observation.BytesDestToSource == 0 {
		return nil
	}
	dimensions := map[string]string{
		"device":           observation.DeviceID,
		"interface":        observation.Interface,
		"protocol":         string(observation.Protocol),
		"source_ip":        observation.SourceIP,
		"destination_ip":   observation.DestinationIP,
		"source_port":      strconv.FormatUint(uint64(observation.SourcePort), 10),
		"destination_port": strconv.FormatUint(uint64(observation.DestinationPort), 10),
		"process":          observation.ProcessName,
		"container":        observation.ContainerName,
		"service":          observation.ServiceName,
	}
	if observation.ContainerName == "" && observation.ContainerID != "" {
		dimensions["container"] = observation.ContainerID
	}
	if observation.SamplingRate == 0 {
		observation.SamplingRate = 1
	}
	estimated := uint8(0)
	if observation.Confidence == "estimated" || observation.SamplingRate > 1 {
		estimated = 1
	}
	bucket := observation.ObservedAt.UTC().Truncate(time.Minute)
	for dimensionType, dimensionValue := range dimensions {
		if dimensionValue == "" {
			continue
		}
		for _, side := range []struct {
			name    string
			bytes   uint64
			packets uint64
		}{
			{name: "source_to_destination", bytes: observation.BytesSourceToDest, packets: observation.PacketsSourceToDest},
			{name: "destination_to_source", bytes: observation.BytesDestToSource, packets: observation.PacketsDestToSource},
		} {
			if side.bytes == 0 && side.packets == 0 {
				continue
			}
			bytes := side.bytes * uint64(observation.SamplingRate)
			packets := side.packets * uint64(observation.SamplingRate)
			eventKey := observation.ID + ":" + dimensionType + ":" + dimensionValue + ":" + side.name
			if err := batch.Append(eventKey, bucket, dimensionType, dimensionValue, observation.DeviceID,
				observation.Interface, string(observation.Protocol), side.name, bytes, packets,
				estimated, observation.ObservedAt.UTC()); err != nil {
				return err
			}
		}
	}
	return nil
}

// associateTrafficPerspectives records a durable, short-lived relationship
// between endpoint and gateway views. Endpoint observations win aggregation
// only when they carry exact counters; otherwise a gateway counter remains
// the useful source for the minute summary.
func (s *Store) associateTrafficPerspectives(ctx context.Context, tx pgx.Tx, observations []domain.FlowObservation) error {
	if _, err := tx.Exec(ctx, `DELETE FROM traffic_dedup_sightings WHERE updated_at < now() - INTERVAL '5 minutes'`); err != nil {
		return fmt.Errorf("prune traffic dedup state: %w", err)
	}
	for index := range observations {
		observation := &observations[index]
		key := trafficDedupKey(*observation)
		if key == "" || (observation.ObserverType != domain.ObserverEndpoint && observation.ObserverType != domain.ObserverGateway) {
			continue
		}
		var endpointSeen bool
		var endpointAt time.Time
		var endpointCounted bool
		err := tx.QueryRow(ctx, `
			SELECT endpoint_seen_at IS NOT NULL,
			       COALESCE(endpoint_seen_at, 'epoch'::timestamptz),
			       endpoint_counted
			FROM traffic_dedup_sightings WHERE flow_key = $1 FOR UPDATE`, key).Scan(&endpointSeen, &endpointAt, &endpointCounted)
		if err != nil && err != pgx.ErrNoRows {
			return fmt.Errorf("read traffic dedup state: %w", err)
		}
		if err == pgx.ErrNoRows {
			endpointSeen, endpointCounted = false, false
		}
		if observation.ObserverType == domain.ObserverGateway && endpointSeen && endpointCounted && withinTrafficDedupWindow(observation.ObservedAt, endpointAt) {
			observation.TrafficSuppressed = true
		}
		if observation.ObserverType == domain.ObserverEndpoint {
			counted := endpointCountersAreExact(*observation)
			_, err = tx.Exec(ctx, `
				INSERT INTO traffic_dedup_sightings (flow_key, endpoint_seen_at, endpoint_counted, updated_at)
				VALUES ($1, $2, $3, now())
				ON CONFLICT (flow_key) DO UPDATE SET
					endpoint_seen_at = CASE
						WHEN traffic_dedup_sightings.endpoint_seen_at IS NULL OR EXCLUDED.endpoint_seen_at > traffic_dedup_sightings.endpoint_seen_at
						THEN EXCLUDED.endpoint_seen_at ELSE traffic_dedup_sightings.endpoint_seen_at END,
					endpoint_counted = traffic_dedup_sightings.endpoint_counted OR EXCLUDED.endpoint_counted,
					updated_at = now()`, key, observation.ObservedAt, counted)
		} else {
			_, err = tx.Exec(ctx, `
				INSERT INTO traffic_dedup_sightings (flow_key, gateway_seen_at, updated_at)
				VALUES ($1, $2, now())
				ON CONFLICT (flow_key) DO UPDATE SET
					gateway_seen_at = CASE
						WHEN traffic_dedup_sightings.gateway_seen_at IS NULL OR EXCLUDED.gateway_seen_at > traffic_dedup_sightings.gateway_seen_at
						THEN EXCLUDED.gateway_seen_at ELSE traffic_dedup_sightings.gateway_seen_at END,
					updated_at = now()`, key, observation.ObservedAt)
		}
		if err != nil {
			return fmt.Errorf("write traffic dedup state: %w", err)
		}
	}
	return nil
}

func endpointCountersAreExact(observation domain.FlowObservation) bool {
	if observation.Confidence != "exact" || observation.CounterSource == "" || observation.CounterSource == "unavailable" {
		return false
	}
	return observation.BytesSourceToDest != 0 || observation.BytesDestToSource != 0 || observation.TotalBytesSourceToDest != 0 || observation.TotalBytesDestToSource != 0
}

func withinTrafficDedupWindow(observedAt, endpointAt time.Time) bool {
	delta := observedAt.Sub(endpointAt)
	if delta < 0 {
		delta = -delta
	}
	return delta <= trafficDedupWindow
}

func trafficDedupKey(observation domain.FlowObservation) string {
	if observation.SourceIP == "" || observation.DestinationIP == "" || observation.SourcePort == 0 || observation.DestinationPort == 0 || observation.Protocol == "" {
		return ""
	}
	left := flowAddress(observation.SourceIP, observation.SourcePort)
	right := flowAddress(observation.DestinationIP, observation.DestinationPort)
	if left > right {
		left, right = right, left
	}
	return strings.ToLower(string(observation.Protocol)) + "|" + left + "|" + right
}

func flowAddress(ip string, port uint16) string {
	parsed := net.ParseIP(ip)
	if parsed != nil {
		ip = parsed.String()
	}
	return net.JoinHostPort(ip, strconv.FormatUint(uint64(port), 10))
}
