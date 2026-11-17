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
	dnsObservations := make([]domain.DNSObservation, 0)
	expectedSequence := uint64(last) + 1
	for _, observation := range observations {
		if observation.DeviceID != deviceID {
			return uint64(last), fmt.Errorf("observation device does not match authenticated device")
		}
		if observation.Sequence <= uint64(last) {
			continue
		}
		if observation.Sequence != expectedSequence {
			return uint64(last), fmt.Errorf("sequence gap: expected %d, got %d", expectedSequence, observation.Sequence)
		}
		expectedSequence++
		if observation.ID == "" || observation.FlowID == "" || observation.ObservedAt.IsZero() {
			return uint64(last), fmt.Errorf("observation requires id, flow_id, and observed_at")
		}
		if observation.Protocol != domain.ProtocolTCP && observation.Protocol != domain.ProtocolUDP {
			return uint64(last), fmt.Errorf("unsupported protocol %q", observation.Protocol)
		}
		if observation.SamplingRate > 1 && observation.Confidence != "estimated" {
			return uint64(last), fmt.Errorf("sampled gateway observations must use estimated confidence")
		}
		if observation.DNSMetadataOnly {
			if observation.Domain == "" || net.ParseIP(observation.DNSIP) == nil || observation.DNSTTL == 0 {
				return uint64(last), fmt.Errorf("invalid DNS metadata observation")
			}
			dnsObservations = append(dnsObservations, domain.DNSObservation{DeviceID: deviceID, ObservedAt: observation.ObservedAt, Interface: observation.Interface, Domain: observation.Domain, IP: observation.DNSIP, TTL: observation.DNSTTL, Source: observation.CounterSource, ExpiresAt: observation.ObservedAt.Add(time.Duration(observation.DNSTTL) * time.Second)})
			continue
		}
		newObservations = append(newObservations, observation)
	}
	if len(newObservations) == 0 {
		if err := s.insertDNSObservations(ctx, dnsObservations); err != nil {
			return uint64(last), err
		}
		s.cacheDNSObservations(dnsObservations)
		if len(dnsObservations) == 0 {
			return uint64(last), tx.Commit(ctx)
		}
		maxSequence := uint64(last)
		for _, observation := range observations {
			if observation.Sequence > maxSequence {
				maxSequence = observation.Sequence
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE ingestion_checkpoints SET last_sequence = $2, updated_at = now() WHERE device_id = $1`, deviceID, int64(maxSequence)); err != nil {
			return uint64(last), err
		}
		if _, err := tx.Exec(ctx, `UPDATE devices SET last_seen = now() WHERE id = $1 AND revoked_at IS NULL`, deviceID); err != nil {
			return uint64(last), err
		}
		if err := tx.Commit(ctx); err != nil {
			return uint64(last), err
		}
		return maxSequence, nil
	}
	if err := s.associateTrafficPerspectives(ctx, tx, newObservations); err != nil {
		return uint64(last), err
	}
	if err := s.insertDNSObservations(ctx, dnsObservations); err != nil {
		return uint64(last), err
	}
	s.cacheDNSObservations(dnsObservations)

	if err := s.insertObservations(ctx, newObservations); err != nil {
		return uint64(last), err
	}
	newLast := observations[len(observations)-1].Sequence
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

func (s *Store) insertDNSObservations(ctx context.Context, observations []domain.DNSObservation) error {
	if len(observations) == 0 {
		return nil
	}
	batch, err := s.ClickHouseNative.PrepareBatch(ctx, `INSERT INTO observer.dns_observations (device_id, observed_at, interface, domain, ip, ttl, source, expires_at, version)`)
	if err != nil {
		return err
	}
	for _, item := range observations {
		if err := batch.Append(item.DeviceID, item.ObservedAt.UTC(), item.Interface, item.Domain, item.IP, item.TTL, item.Source, item.ExpiresAt.UTC(), item.ObservedAt.UTC()); err != nil {
			return err
		}
	}
	return batch.Send()
}

func (s *Store) IngestDNSObservations(ctx context.Context, observations []domain.DNSObservation) error {
	if len(observations) == 0 {
		return nil
	}
	batch, err := s.ClickHouseNative.PrepareBatch(ctx, `INSERT INTO observer.dns_observations (device_id, observed_at, interface, domain, ip, ttl, source, expires_at, version)`)
	if err != nil {
		return err
	}
	for _, item := range observations {
		if err := batch.Append(item.DeviceID, item.ObservedAt.UTC(), item.Interface, item.Domain, item.IP, item.TTL, item.Source, item.ExpiresAt.UTC(), item.ObservedAt.UTC()); err != nil {
			return err
		}
	}
	return batch.Send()
}

func (s *Store) insertObservations(ctx context.Context, observations []domain.FlowObservation) error {
	for i := range observations {
		s.applyEnrichment(&observations[i])
	}
	live, err := s.ClickHouseNative.PrepareBatch(ctx, `INSERT INTO observer.live_connections (
		device_id, flow_id, observer_type, observer_id, local_is_source, observed_at, interface, network_namespace,
		protocol, state, source_ip, source_port, destination_ip, destination_port, nat_source_ip, nat_source_port,
		nat_destination_ip, nat_destination_port, bytes_source_to_dest, bytes_dest_to_source,
		rate_bytes_source_to_dest, rate_bytes_dest_to_source, pid, process_name, container_id, container_name,
		service_name, attribution_source, attribution_reason, sampling_rate, counter_source, confidence,
		domain, domain_source, asn, organization, country, region, city, latitude, longitude, enrichment_version, gap_reason, active, version
	)`)
	if err != nil {
		return err
	}
	traffic, err := s.ClickHouseNative.PrepareBatch(ctx, `INSERT INTO observer.traffic_minute (
		event_key, bucket, dimension_type, dimension_value, device_id, interface, protocol, direction, bytes, packets, estimated,
		source_ip, destination_ip, local_port, remote_port, domain, asn, organization, country, region, city, process_name, container_name, confidence, gap_reason, remote_ip, version
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
			observation.SamplingRate, observation.CounterSource, observation.Confidence, observation.Domain, observation.DomainSource,
			observation.ASN, observation.Organization, observation.Country, observation.Region, observation.City, observation.Latitude,
			observation.Longitude, observation.EnrichmentVersion, observation.GapReason, observation.Active, version,
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
	if err := s.insertPresence(ctx, observations); err != nil {
		return fmt.Errorf("insert connection presence: %w", err)
	}
	return nil
}

func (s *Store) applyEnrichment(observation *domain.FlowObservation) {
	s.applyDNSAttribution(observation)
	if s.Enricher == nil {
		return
	}
	// The remote endpoint is the primary geography target. For gateway views,
	// destination remains the stable public-side key when local_is_source is false.
	remote := observation.DestinationIP
	if !observation.LocalIsSource {
		remote = observation.SourceIP
	}
	record, version, reason := s.Enricher.Lookup(remote)
	observation.ASN, observation.Organization = record.ASN, record.Organization
	observation.Country, observation.Region, observation.City = record.Country, record.Region, record.City
	observation.Latitude, observation.Longitude = record.Latitude, record.Longitude
	observation.EnrichmentVersion = version
	if reason != "matched" && observation.GapReason == "" {
		observation.GapReason = "enrichment:" + reason
	}
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
		"domain":           observation.Domain,
		"asn":              strconv.FormatUint(uint64(observation.ASN), 10),
		"organization":     observation.Organization,
		"country":          observation.Country,
		"region":           observation.Region,
		"city":             observation.City,
	}
	localPort, remotePort := observation.SourcePort, observation.DestinationPort
	remoteIP := observation.DestinationIP
	if !observation.LocalIsSource {
		localPort, remotePort = observation.DestinationPort, observation.SourcePort
		remoteIP = observation.SourceIP
	}
	dimensions["local_port"] = strconv.FormatUint(uint64(localPort), 10)
	dimensions["remote_port"] = strconv.FormatUint(uint64(remotePort), 10)
	if dimensions["process"] == "" {
		dimensions["process"] = "未知进程"
	}
	if dimensions["domain"] == "" {
		dimensions["domain"] = "未知域名 · " + remoteIP
	}
	if dimensions["interface"] == "" {
		dimensions["interface"] = "未知网卡"
	}
	if observation.ASN == 0 {
		delete(dimensions, "asn")
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
		forwardDirection, reverseDirection := "upload", "download"
		if !observation.LocalIsSource {
			forwardDirection, reverseDirection = reverseDirection, forwardDirection
		}
		for _, side := range []struct {
			name    string
			bytes   uint64
			packets uint64
		}{
			{name: forwardDirection, bytes: observation.BytesSourceToDest, packets: observation.PacketsSourceToDest},
			{name: reverseDirection, bytes: observation.BytesDestToSource, packets: observation.PacketsDestToSource},
		} {
			if side.bytes == 0 && side.packets == 0 {
				continue
			}
			bytes := side.bytes * uint64(observation.SamplingRate)
			packets := side.packets * uint64(observation.SamplingRate)
			eventKey := observation.ID + ":" + dimensionType + ":" + dimensionValue + ":" + side.name
			if err := batch.Append(eventKey, bucket, dimensionType, dimensionValue, observation.DeviceID,
				observation.Interface, string(observation.Protocol), side.name, bytes, packets, estimated,
				observation.SourceIP, observation.DestinationIP, localPort, remotePort, observation.Domain, observation.ASN,
				observation.Organization, observation.Country, observation.Region, observation.City, observation.ProcessName,
				observation.ContainerName, observation.Confidence, observation.GapReason, remoteIP, observation.ObservedAt.UTC()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) insertPresence(ctx context.Context, observations []domain.FlowObservation) error {
	batch, err := s.ClickHouseNative.PrepareBatch(ctx, `INSERT INTO observer.connection_presence_minute (
		event_key, bucket, device_id, flow_id, protocol, source_ip, source_port, destination_ip, destination_port, domain,
		first_seen, last_seen, observed_minutes, active_seconds, upload_bytes, download_bytes, asn, organization, country, region, city, estimated, gap_reason,
		interface, local_port, remote_ip, process_name, container_name, ranking_dimensions_available, version
	)`)
	if err != nil {
		return err
	}
	for _, observation := range observations {
		if observation.TrafficSuppressed {
			continue
		}
		if observation.FlowID == "" || observation.ObservedAt.IsZero() {
			continue
		}
		bucket := observation.ObservedAt.UTC().Truncate(time.Minute)
		estimated := uint8(0)
		if observation.Confidence == "estimated" || observation.SamplingRate > 1 {
			estimated = 1
		}
		activeSeconds := uint32(0)
		if observation.Active {
			activeSeconds = 2
		}
		uploadBytes, downloadBytes := observation.BytesSourceToDest, observation.BytesDestToSource
		localPort, remoteIP := observation.SourcePort, observation.DestinationIP
		if !observation.LocalIsSource {
			uploadBytes, downloadBytes = downloadBytes, uploadBytes
			localPort, remoteIP = observation.DestinationPort, observation.SourceIP
		}
		if err := batch.Append(observation.ID+":presence", bucket, observation.DeviceID, observation.FlowID, string(observation.Protocol),
			observation.SourceIP, observation.SourcePort, observation.DestinationIP, observation.DestinationPort, observation.Domain,
			observation.ObservedAt.UTC(), observation.ObservedAt.UTC(), uint32(1), activeSeconds, uploadBytes,
			downloadBytes, observation.ASN, observation.Organization, observation.Country, observation.Region,
			observation.City, estimated, observation.GapReason, observation.Interface, localPort, remoteIP,
			observation.ProcessName, observation.ContainerName, uint8(1), observation.ObservedAt.UTC()); err != nil {
			return err
		}
	}
	return batch.Send()
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
