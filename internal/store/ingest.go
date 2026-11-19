package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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
		if !strings.HasPrefix(observation.ID, "history-") {
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

// EnrichObservation applies cached DNS and GeoIP metadata without persisting the observation.
func (s *Store) EnrichObservation(observation *domain.FlowObservation) {
	if observation == nil {
		return
	}
	s.applyEnrichment(observation)
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
		if err := appendPresence(batch, observation); err != nil {
			return err
		}
	}
	return batch.Send()
}

func appendPresence(batch interface{ Append(...any) error }, observation domain.FlowObservation) error {
	if observation.TrafficSuppressed || observation.FlowID == "" || observation.ObservedAt.IsZero() {
		return nil
	}
	end := observation.ObservedAt.UTC()
	start := observation.CoverageStart.UTC()
	legacy := observation.CoverageStart.IsZero()
	if legacy {
		start = end
	} else if start.After(end) || end.Sub(start) > 2*time.Minute {
		return fmt.Errorf("invalid observation coverage interval")
	}
	estimated := uint8(0)
	if observation.Confidence == "estimated" || observation.SamplingRate > 1 {
		estimated = 1
	}
	uploadBytes, downloadBytes := observation.BytesSourceToDest, observation.BytesDestToSource
	localPort, remoteIP := observation.SourcePort, observation.DestinationIP
	if !observation.LocalIsSource {
		uploadBytes, downloadBytes = downloadBytes, uploadBytes
		localPort, remoteIP = observation.DestinationPort, observation.SourceIP
	}
	currentBucket := end.Truncate(time.Minute)
	for bucket := start.Truncate(time.Minute); !bucket.After(currentBucket); bucket = bucket.Add(time.Minute) {
		first, last := start, end
		if first.Before(bucket) {
			first = bucket
		}
		if boundary := bucket.Add(time.Minute); last.After(boundary) {
			last = boundary
		}
		// Subtract second boundaries rather than flooring each interval duration:
		// fractional sampling jitter must not accumulate into missing seconds.
		activeSeconds := uint32(last.Unix() - first.Unix())
		if legacy && observation.Active {
			activeSeconds = 2
		}
		upload, download := uint64(0), uint64(0)
		if bucket.Equal(currentBucket) {
			// Deltas belong to this observation once, including at a minute boundary.
			upload, download = uploadBytes, downloadBytes
		}
		eventKey := observation.ID + ":presence"
		if !legacy {
			eventKey += ":" + strconv.FormatInt(bucket.Unix(), 10)
		}
		if err := batch.Append(eventKey, bucket, observation.DeviceID, observation.FlowID, string(observation.Protocol),
			observation.SourceIP, observation.SourcePort, observation.DestinationIP, observation.DestinationPort, observation.Domain,
			first, last, uint32(1), activeSeconds, upload, download, observation.ASN, observation.Organization,
			observation.Country, observation.Region, observation.City, estimated, observation.GapReason, observation.Interface,
			localPort, remoteIP, observation.ProcessName, observation.ContainerName, uint8(1), end); err != nil {
			return err
		}
	}
	return nil
}

// associateTrafficPerspectives records a durable, short-lived relationship
// between endpoint and gateway views. Endpoint observations win aggregation
// only when they carry exact counters; otherwise a gateway counter remains
// the useful source for the minute summary.
func (s *Store) associateTrafficPerspectives(ctx context.Context, tx pgx.Tx, observations []domain.FlowObservation) error {
	keys := make([]string, 0, len(observations))
	for index := range observations {
		observation := &observations[index]
		if observation.ObserverType != domain.ObserverEndpoint && observation.ObserverType != domain.ObserverGateway {
			continue
		}
		if key := trafficDedupKey(*observation); key != "" {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	uniqueKeys := keys[:0]
	for _, key := range keys {
		if len(uniqueKeys) == 0 || uniqueKeys[len(uniqueKeys)-1] != key {
			uniqueKeys = append(uniqueKeys, key)
		}
	}
	keys = uniqueKeys

	if err := s.pruneTrafficDedup(ctx, tx); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		SELECT flow_key, endpoint_seen_at, endpoint_counted, gateway_seen_at
		FROM traffic_dedup_sightings
		WHERE flow_key = ANY($1::text[])
		ORDER BY flow_key
		FOR UPDATE`, keys)
	if err != nil {
		return fmt.Errorf("read traffic dedup state: %w", err)
	}
	states := make(map[string]trafficDedupState, len(keys))
	for rows.Next() {
		var key string
		var endpointSeen, gatewaySeen pgtype.Timestamptz
		var endpointCounted bool
		if err := rows.Scan(&key, &endpointSeen, &endpointCounted, &gatewaySeen); err != nil {
			rows.Close()
			return fmt.Errorf("read traffic dedup state: %w", err)
		}
		state := trafficDedupState{endpointCounted: endpointCounted}
		if endpointSeen.Valid {
			at := endpointSeen.Time
			state.endpointSeenAt = &at
		}
		if gatewaySeen.Valid {
			at := gatewaySeen.Time
			state.gatewaySeenAt = &at
		}
		states[key] = state
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read traffic dedup state: %w", err)
	}

	for index := range observations {
		observation := &observations[index]
		if observation.ObserverType != domain.ObserverEndpoint && observation.ObserverType != domain.ObserverGateway {
			continue
		}
		key := trafficDedupKey(*observation)
		if key == "" {
			continue
		}
		state := states[key]
		applyTrafficPerspective(&state, observation)
		states[key] = state
	}
	updates := make([]trafficDedupUpdate, 0, len(keys))
	for _, key := range keys {
		state := states[key]
		updates = append(updates, trafficDedupUpdate{
			FlowKey: key, EndpointSeenAt: state.endpointSeenAt,
			EndpointCounted: state.endpointCounted, GatewaySeenAt: state.gatewaySeenAt,
		})
	}
	encoded, err := json.Marshal(updates)
	if err != nil {
		return fmt.Errorf("encode traffic dedup updates: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO traffic_dedup_sightings (flow_key, endpoint_seen_at, endpoint_counted, gateway_seen_at, updated_at)
		SELECT flow_key, endpoint_seen_at, endpoint_counted, gateway_seen_at, now()
		FROM jsonb_to_recordset($1::jsonb) AS updates(
			flow_key text, endpoint_seen_at timestamptz, endpoint_counted boolean, gateway_seen_at timestamptz
		)
		ON CONFLICT (flow_key) DO UPDATE SET
			endpoint_seen_at = CASE
				WHEN EXCLUDED.endpoint_seen_at IS NULL THEN traffic_dedup_sightings.endpoint_seen_at
				WHEN traffic_dedup_sightings.endpoint_seen_at IS NULL OR EXCLUDED.endpoint_seen_at > traffic_dedup_sightings.endpoint_seen_at
				THEN EXCLUDED.endpoint_seen_at ELSE traffic_dedup_sightings.endpoint_seen_at END,
			endpoint_counted = traffic_dedup_sightings.endpoint_counted OR EXCLUDED.endpoint_counted,
			gateway_seen_at = CASE
				WHEN EXCLUDED.gateway_seen_at IS NULL THEN traffic_dedup_sightings.gateway_seen_at
				WHEN traffic_dedup_sightings.gateway_seen_at IS NULL OR EXCLUDED.gateway_seen_at > traffic_dedup_sightings.gateway_seen_at
				THEN EXCLUDED.gateway_seen_at ELSE traffic_dedup_sightings.gateway_seen_at END,
		updated_at = now()`, string(encoded))
	if err != nil {
		return fmt.Errorf("write traffic dedup state: %w", err)
	}
	return nil
}

type trafficDedupState struct {
	endpointSeenAt  *time.Time
	endpointCounted bool
	gatewaySeenAt   *time.Time
}

type trafficDedupUpdate struct {
	FlowKey         string     `json:"flow_key"`
	EndpointSeenAt  *time.Time `json:"endpoint_seen_at"`
	EndpointCounted bool       `json:"endpoint_counted"`
	GatewaySeenAt   *time.Time `json:"gateway_seen_at"`
}

func (s *Store) pruneTrafficDedup(ctx context.Context, tx pgx.Tx) error {
	s.dedupPruneMu.Lock()
	defer s.dedupPruneMu.Unlock()
	now := time.Now()
	if !s.dedupPrunedAt.IsZero() && now.Sub(s.dedupPrunedAt) < time.Minute {
		return nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM traffic_dedup_sightings WHERE updated_at < now() - INTERVAL '5 minutes'`); err != nil {
		return fmt.Errorf("prune traffic dedup state: %w", err)
	}
	s.dedupPrunedAt = now
	return nil
}

func applyTrafficPerspective(state *trafficDedupState, observation *domain.FlowObservation) {
	switch observation.ObserverType {
	case domain.ObserverEndpoint:
		if state.endpointSeenAt == nil || observation.ObservedAt.After(*state.endpointSeenAt) {
			at := observation.ObservedAt
			state.endpointSeenAt = &at
		}
		state.endpointCounted = state.endpointCounted || endpointCountersAreExact(*observation)
	case domain.ObserverGateway:
		if state.endpointSeenAt != nil && state.endpointCounted && withinTrafficDedupWindow(observation.ObservedAt, *state.endpointSeenAt) {
			observation.TrafficSuppressed = true
		}
		if state.gatewaySeenAt == nil || observation.ObservedAt.After(*state.gatewaySeenAt) {
			at := observation.ObservedAt
			state.gatewaySeenAt = &at
		}
	}
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
