package store

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/network-observer/network-observer/internal/domain"
)

var allowedTrafficDimensions = map[string]bool{
	"device": true, "interface": true, "protocol": true, "source_ip": true,
	"destination_ip": true, "source_port": true, "destination_port": true,
	"process": true, "container": true, "service": true, "domain": true, "asn": true,
	"organization": true, "country": true, "region": true, "city": true, "local_port": true, "remote_port": true,
}

type TrafficFilter struct {
	Dimension, Value, DeviceID, Interface, Protocol              string
	LocalPort, RemotePort                                        uint16
	SourceIP, DestinationIP, Domain, Organization                string
	ASN                                                          uint32
	Country, Region, City, ProcessName, ContainerName, Direction string
	From, To                                                     time.Time
	Limit                                                        uint32
	HideInternal                                                 bool
	LANNetworks                                                  []string
}

var allowedRankingDimensions = map[string]string{
	"device": "toString(device_id)", "domain": "if(domain = '', if(remote_ip = '', '未知域名（历史维度不可用）', concat('未知域名 · ', remote_ip)), domain)", "remote_ip": "remote_ip",
	"local_port": "toString(local_port)", "process": "if(process_name = '', '未知进程', process_name)", "interface": "if(interface = '', '未知网卡', interface)",
	"asn": "toString(asn)", "organization": "organization", "country": "country", "region": "region", "city": "city", "container": "container_name",
}

type rankingMetadata struct {
	trusted                                                bool
	domain, ptrDomain, organization, country, region, city string
	asn                                                    uint32
}

func (s *Store) OverviewRankings(ctx context.Context, dimensions []string, limit uint32, trustFilter, sortBy string, from, to time.Time) (map[string][]domain.OverviewRankingRow, bool, []string, error) {
	if limit < 1 || limit > 50 || len(dimensions) == 0 || len(dimensions) > 5 {
		return nil, false, nil, fmt.Errorf("invalid ranking limit or dimension count")
	}
	if trustFilter != "all" && trustFilter != "trusted" && trustFilter != "untrusted" {
		return nil, false, nil, fmt.Errorf("invalid trust filter")
	}
	if sortBy != "traffic" && sortBy != "connections" {
		return nil, false, nil, fmt.Errorf("invalid ranking sort")
	}
	for _, dimension := range dimensions {
		if _, ok := allowedRankingDimensions[dimension]; !ok {
			return nil, false, nil, fmt.Errorf("unsupported ranking dimension %q", dimension)
		}
	}
	metadata := make(map[string]rankingMetadata)
	rows, err := s.Postgres.Query(ctx, `SELECT host(ip_address), trusted, COALESCE(domain,''), COALESCE(organization,''), COALESCE(country,''), COALESCE(region,''), COALESCE(city,''), COALESCE(asn,0) FROM ip_metadata_overrides`)
	if err != nil {
		return nil, false, nil, err
	}
	for rows.Next() {
		var ip string
		var item rankingMetadata
		if err := rows.Scan(&ip, &item.trusted, &item.domain, &item.organization, &item.country, &item.region, &item.city, &item.asn); err != nil {
			rows.Close()
			return nil, false, nil, err
		}
		metadata[ip] = item
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, false, nil, err
	}
	rows.Close()
	ptrRows, err := s.Postgres.Query(ctx, `SELECT host(ip_address), hostname FROM ip_ptr_cache WHERE status = 'matched' AND hostname <> '' AND expires_at > now()`)
	if err != nil {
		return nil, false, nil, err
	}
	for ptrRows.Next() {
		var ip, hostname string
		if err := ptrRows.Scan(&ip, &hostname); err != nil {
			ptrRows.Close()
			return nil, false, nil, err
		}
		item := metadata[ip]
		item.ptrDomain = hostname
		metadata[ip] = item
	}
	if err := ptrRows.Err(); err != nil {
		ptrRows.Close()
		return nil, false, nil, err
	}
	ptrRows.Close()

	type aggregate struct {
		row   domain.OverviewRankingRow
		total uint64
		flows map[string]struct{}
	}
	result := make(map[string][]domain.OverviewRankingRow, len(dimensions))
	partial := false
	legacyDimensionRows := false
	for _, dimension := range dimensions {
		if dimension == "local_port" || dimension == "process" || dimension == "interface" || dimension == "container" {
			var count uint64
			if err := s.ClickHouse.QueryRowContext(ctx, `SELECT count() FROM (SELECT 1 FROM observer.connection_presence_minute FINAL WHERE bucket >= ? AND bucket < ? AND ranking_dimensions_available = 0 LIMIT 1) SETTINGS max_execution_time=2`, from.UTC(), to.UTC()).Scan(&count); err != nil {
				return nil, false, nil, err
			}
			legacyDimensionRows = count > 0
			break
		}
	}
	var unavailableDimensions []string
	for _, dimension := range dimensions {
		expression := allowedRankingDimensions[dimension]
		groups := make(map[string]*aggregate)
		// Read canonical device rows and derive the requested bucket in memory.
		// This lets current IP overrides reclassify retained bytes even when
		// the original ASN, organization, country, or domain was unknown.
		trafficWhere := "bucket >= ? AND bucket < ? AND dimension_type = 'device'"
		trafficArgs := []any{from.UTC(), to.UTC()}
		if trustFilter == "trusted" {
			trafficWhere += " AND remote_ip != ''"
		}
		trafficRows, err := s.ClickHouse.QueryContext(ctx, `
			SELECT `+expression+`, toString(device_id), remote_ip,
			       sumIf(bytes, direction='upload'), sumIf(bytes, direction='download'), max(estimated), any(gap_reason)
			FROM observer.traffic_minute FINAL
			WHERE `+trafficWhere+`
			GROUP BY `+expression+`, device_id, remote_ip
			ORDER BY sum(bytes) DESC LIMIT 50001
			SETTINGS max_execution_time=8`, trafficArgs...)
		if err != nil {
			return nil, false, nil, err
		}
		trafficCount := 0
		for trafficRows.Next() {
			trafficCount++
			var value, deviceID, remoteIP, gap string
			var upload, download uint64
			var estimated uint8
			if err := trafficRows.Scan(&value, &deviceID, &remoteIP, &upload, &download, &estimated, &gap); err != nil {
				trafficRows.Close()
				return nil, false, nil, err
			}
			meta := metadata[remoteIP]
			if trustFilter == "trusted" && !meta.trusted || trustFilter == "untrusted" && meta.trusted {
				continue
			}
			value = currentRankingValue(dimension, value, remoteIP, meta)
			if value == "" {
				continue
			}
			groupValue, groupDevice := value, ""
			if dimension == "device" || dimension == "local_port" || dimension == "process" || dimension == "interface" || dimension == "container" {
				groupDevice = deviceID
			}
			key := groupDevice + "\x00" + groupValue
			item := groups[key]
			if item == nil {
				item = &aggregate{row: domain.OverviewRankingRow{Dimension: dimension, Value: groupValue, DeviceID: groupDevice}, flows: make(map[string]struct{})}
				groups[key] = item
			}
			if dimension == "remote_ip" {
				item.row.RemoteIP = groupValue
			}
			item.row.UploadBytes += upload
			item.row.DownloadBytes += download
			item.row.Estimated = item.row.Estimated || estimated != 0
			if gap != "" {
				item.row.GapReason = gap
			}
			item.total += upload + download
		}
		if err := trafficRows.Err(); err != nil {
			trafficRows.Close()
			return nil, false, nil, err
		}
		trafficRows.Close()
		if trafficCount > 50000 {
			partial = true
		}

		presenceWhere := "bucket >= ? AND bucket < ?"
		presenceArgs := []any{from.UTC(), to.UTC()}
		if dimension == "local_port" || dimension == "process" || dimension == "interface" || dimension == "container" {
			presenceWhere += " AND ranking_dimensions_available = 1"
			if legacyDimensionRows {
				unavailableDimensions = append(unavailableDimensions, dimension)
			}
		}
		if trustFilter == "trusted" {
			presenceWhere += " AND remote_ip != ''"
		}
		if dimension == "local_port" {
			presenceWhere += " AND local_port != 0"
		}
		if dimension == "remote_ip" {
			presenceWhere += " AND remote_ip != ''"
		}
		if dimension == "asn" {
			presenceWhere += " AND asn != 0"
		}
		presenceRows, err := s.ClickHouse.QueryContext(ctx, `
			SELECT `+expression+`, toString(device_id), remote_ip, flow_id, max(estimated), any(gap_reason)
			FROM observer.connection_presence_minute FINAL
			WHERE `+presenceWhere+` AND `+expression+` != ''
			GROUP BY `+expression+`, device_id, remote_ip, flow_id
			LIMIT 200001
			SETTINGS max_execution_time=8`, presenceArgs...)
		if err != nil {
			return nil, false, nil, err
		}
		presenceCount := 0
		for presenceRows.Next() {
			presenceCount++
			var value, deviceID, remoteIP, flowID, gap string
			var estimated uint8
			if err := presenceRows.Scan(&value, &deviceID, &remoteIP, &flowID, &estimated, &gap); err != nil {
				presenceRows.Close()
				return nil, false, nil, err
			}
			meta := metadata[remoteIP]
			if trustFilter == "trusted" && !meta.trusted || trustFilter == "untrusted" && meta.trusted {
				continue
			}
			value = currentRankingValue(dimension, value, remoteIP, meta)
			if value == "" {
				continue
			}
			groupValue, groupDevice := value, ""
			if dimension == "device" || dimension == "local_port" || dimension == "process" || dimension == "interface" || dimension == "container" {
				groupDevice = deviceID
			}
			key := groupDevice + "\x00" + groupValue
			item := groups[key]
			if item == nil {
				item = &aggregate{row: domain.OverviewRankingRow{Dimension: dimension, Value: groupValue, DeviceID: groupDevice}, flows: make(map[string]struct{})}
				groups[key] = item
			}
			if dimension == "remote_ip" {
				item.row.RemoteIP = groupValue
			}
			item.flows[deviceID+"\x00"+flowID] = struct{}{}
			item.row.Estimated = item.row.Estimated || estimated != 0
			if gap != "" {
				item.row.GapReason = gap
			}
		}
		if err := presenceRows.Err(); err != nil {
			presenceRows.Close()
			return nil, false, nil, err
		}
		presenceRows.Close()
		if presenceCount > 200000 {
			partial = true
		}

		ordered := make([]*aggregate, 0, len(groups))
		for _, item := range groups {
			ordered = append(ordered, item)
		}
		sort.Slice(ordered, func(i, j int) bool {
			if sortBy == "connections" && len(ordered[i].flows) != len(ordered[j].flows) {
				return len(ordered[i].flows) > len(ordered[j].flows)
			}
			if sortBy == "traffic" && ordered[i].total != ordered[j].total {
				return ordered[i].total > ordered[j].total
			}
			return ordered[i].row.Value < ordered[j].row.Value
		})
		if len(ordered) > int(limit) {
			ordered = ordered[:limit]
		}
		items := make([]domain.OverviewRankingRow, 0, len(ordered))
		for _, item := range ordered {
			item.row.Connections = uint64(len(item.flows))
			item.row.HistoryDimensionAvailable = !legacyDimensionRows || (dimension != "local_port" && dimension != "process" && dimension != "interface" && dimension != "container")
			items = append(items, item.row)
		}
		result[dimension] = items
	}
	return result, partial, unavailableDimensions, nil
}

func currentRankingValue(dimension, value, remoteIP string, metadata rankingMetadata) string {
	switch dimension {
	case "domain":
		if metadata.domain != "" {
			return metadata.domain
		}
		if (value == "" || strings.HasPrefix(value, "未知域名 · ")) && metadata.ptrDomain != "" {
			return metadata.ptrDomain + "（PTR）"
		}
	case "asn":
		if metadata.asn != 0 {
			return strconv.FormatUint(uint64(metadata.asn), 10)
		}
	case "organization":
		if metadata.organization != "" {
			return metadata.organization
		}
	case "country":
		if metadata.country != "" {
			return metadata.country
		}
	case "region":
		if metadata.region != "" {
			return metadata.region
		}
	case "city":
		if metadata.city != "" {
			return metadata.city
		}
	}
	if value == "" && dimension == "domain" {
		if remoteIP == "" {
			return "未知域名"
		}
		return "未知域名 · " + remoteIP
	}
	if value == "" && dimension == "process" {
		return "未知进程"
	}
	if value == "0" && (dimension == "local_port" || dimension == "asn") {
		return ""
	}
	return value
}

func (s *Store) ListConnections(ctx context.Context, deviceID, protocol, query string, limit, offset uint32) ([]domain.FlowObservation, uint64, error) {
	return s.ListConnectionsFiltered(ctx, deviceID, protocol, query, false, nil, limit, offset)
}

func (s *Store) ListConnectionsFiltered(ctx context.Context, deviceID, protocol, query string, hideInternal bool, networks []string, limit, offset uint32) ([]domain.FlowObservation, uint64, error) {
	conditions := []string{"active = 1", "observed_at > now64(3) - INTERVAL 15 MINUTE"}
	args := make([]any, 0, 6)
	if deviceID != "" {
		conditions = append(conditions, "device_id = ?")
		args = append(args, deviceID)
	}
	if protocol != "" {
		conditions = append(conditions, "protocol = ?")
		args = append(args, protocol)
	}
	if query != "" {
		conditions = append(conditions, "positionCaseInsensitive(concat(source_ip, ':', toString(source_port), ' ', destination_ip, ':', toString(destination_port), ' ', process_name, ' ', container_name, ' ', service_name, ' ', domain, ' ', organization, ' ', country), ?) > 0")
		args = append(args, query)
	}
	if hideInternal {
		condition, values := internalFlowExclusion("source_ip", "destination_ip", networks)
		conditions = append(conditions, condition)
		args = append(args, values...)
	}
	where := strings.Join(conditions, " AND ")
	var total uint64
	if err := s.ClickHouse.QueryRowContext(ctx, `SELECT count() FROM observer.live_connections FINAL WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := s.ClickHouse.QueryContext(ctx, `
		SELECT device_id, flow_id, observer_id, observer_type, local_is_source, observed_at, interface,
		       network_namespace, protocol, state, source_ip, source_port, destination_ip, destination_port,
		       nat_source_ip, nat_source_port, nat_destination_ip, nat_destination_port,
		       bytes_source_to_dest, bytes_dest_to_source, rate_bytes_source_to_dest, rate_bytes_dest_to_source,
		       pid, process_name, container_id, container_name, service_name, attribution_source,
		       attribution_reason, sampling_rate, counter_source, confidence, domain, domain_source, asn,
		       organization, country, region, city, latitude, longitude, enrichment_version, gap_reason, active
		FROM observer.live_connections FINAL WHERE `+where+`
		ORDER BY rate_bytes_source_to_dest + rate_bytes_dest_to_source DESC
		LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := make([]domain.FlowObservation, 0)
	for rows.Next() {
		var item domain.FlowObservation
		var active uint8
		if err := rows.Scan(
			&item.DeviceID, &item.FlowID, &item.ObserverID, &item.ObserverType, &item.LocalIsSource, &item.ObservedAt,
			&item.Interface, &item.NetworkNamespace, &item.Protocol, &item.State, &item.SourceIP, &item.SourcePort,
			&item.DestinationIP, &item.DestinationPort, &item.NATSourceIP, &item.NATSourcePort,
			&item.NATDestinationIP, &item.NATDestinationPort, &item.TotalBytesSourceToDest, &item.TotalBytesDestToSource,
			&item.RateBytesSourceToDest, &item.RateBytesDestToSource, &item.PID, &item.ProcessName, &item.ContainerID,
			&item.ContainerName, &item.ServiceName, &item.AttributionSource, &item.AttributionReason,
			&item.SamplingRate, &item.CounterSource, &item.Confidence, &item.Domain, &item.DomainSource, &item.ASN,
			&item.Organization, &item.Country, &item.Region, &item.City, &item.Latitude, &item.Longitude,
			&item.EnrichmentVersion, &item.GapReason, &active,
		); err != nil {
			return nil, 0, err
		}
		item.ID = item.DeviceID + ":" + item.FlowID
		item.Active = active != 0
		items = append(items, item)
	}
	return items, total, rows.Err()
}

func (s *Store) TrafficTimeseries(ctx context.Context, filter TrafficFilter, bucket time.Duration) ([]domain.TrafficPoint, error) {
	if !allowedTrafficDimensions[filter.Dimension] {
		return nil, fmt.Errorf("unsupported dimension %q", filter.Dimension)
	}
	if bucket != time.Minute && bucket != 5*time.Minute && bucket != time.Hour {
		return nil, fmt.Errorf("unsupported bucket")
	}
	seconds := int64(bucket / time.Second)
	conditions, args := trafficConditions(filter)
	args = append([]any{seconds, seconds}, args...)
	args = append(args, filter.Limit)
	rows, err := s.ClickHouse.QueryContext(ctx, `
		SELECT toDateTime(intDiv(toUnixTimestamp(bucket), ?) * ?) AS bucket,
			dimension_type, dimension_value, protocol,
			sumIf(bytes, direction = 'upload') AS upload,
			sumIf(bytes, direction = 'download') AS download,
			sum(packets) AS packets, max(estimated) AS estimated,
			any(gap_reason) AS gap_reason
		FROM observer.traffic_minute FINAL WHERE `+strings.Join(conditions, " AND ")+`
		GROUP BY bucket, dimension_type, dimension_value, protocol ORDER BY bucket LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.TrafficPoint, 0)
	for rows.Next() {
		var p domain.TrafficPoint
		var estimated uint8
		if err := rows.Scan(&p.Bucket, &p.Dimension, &p.Value, &p.Protocol, &p.UploadBytes, &p.DownloadBytes, &p.Packets, &estimated, &p.GapReason); err != nil {
			return nil, err
		}
		p.Bytes = p.UploadBytes + p.DownloadBytes
		p.Estimated = estimated != 0
		result = append(result, p)
	}
	return result, rows.Err()
}

func (s *Store) Presence(ctx context.Context, deviceID, domainFilter string, from, to time.Time, minimumRatio float64, limit uint32) ([]domain.PresencePoint, error) {
	return s.PresenceFiltered(ctx, deviceID, domainFilter, from, to, minimumRatio, limit, false, nil)
}

func (s *Store) PresenceFiltered(ctx context.Context, deviceID, domainFilter string, from, to time.Time, minimumRatio float64, limit uint32, hideInternal bool, networks []string) ([]domain.PresencePoint, error) {
	where := `bucket >= ? AND bucket < ?`
	args := []any{from.UTC(), to.UTC()}
	if deviceID != "" {
		where += ` AND device_id = ?`
		args = append(args, deviceID)
	}
	if domainFilter != "" {
		where += ` AND domain = ?`
		args = append(args, domainFilter)
	}
	if hideInternal {
		condition, values := internalFlowExclusion("source_ip", "destination_ip", networks)
		where += ` AND ` + condition
		args = append(args, values...)
	}
	minimumActiveSeconds := minimumRatio * to.Sub(from).Seconds()
	rows, err := s.ClickHouse.QueryContext(ctx, presenceQuery(where), append(args, minimumActiveSeconds, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.PresencePoint, 0)
	denominator := to.Sub(from).Seconds()
	if denominator <= 0 {
		denominator = 1
	}
	for rows.Next() {
		var p domain.PresencePoint
		var deviceIDString, flowID string
		var estimated uint8
		if err := rows.Scan(&deviceIDString, &flowID, &p.Protocol, &p.SourceIP, &p.SourcePort, &p.DestinationIP, &p.DestinationPort,
			&p.Domain, &p.FirstSeen, &p.LastSeen, &p.ObservedMinutes, &p.ActiveSeconds, &p.UploadBytes, &p.DownloadBytes,
			&p.ASN, &p.Organization, &p.Country, &p.Region, &p.City, &estimated, &p.GapReason); err != nil {
			return nil, err
		}
		p.DeviceID = deviceIDString
		p.Key = flowID
		p.ContinuityRatio = float64(p.ActiveSeconds) / denominator
		if p.ContinuityRatio > 1 {
			p.ContinuityRatio = 1
		}
		p.Estimated = estimated != 0
		if p.ContinuityRatio >= minimumRatio {
			result = append(result, p)
		}
	}
	return result, rows.Err()
}

func presenceQuery(where string) string {
	return `
		SELECT device_id, flow_id, protocol, source_ip, source_port, destination_ip, destination_port,
			any(domain), min(first_seen), max(last_seen), sum(observed_minutes), sum(active_seconds),
			sum(upload_bytes), sum(download_bytes), any(asn), any(organization), any(country), any(region), any(city), max(estimated), any(gap_reason)
		FROM (
			SELECT bucket, device_id, flow_id, protocol, source_ip, source_port, destination_ip, destination_port,
				any(domain) AS domain, min(first_seen) AS first_seen, max(last_seen) AS last_seen,
				toUInt32(1) AS observed_minutes, least(toUInt32(60), sum(active_seconds)) AS active_seconds,
				sum(upload_bytes) AS upload_bytes, sum(download_bytes) AS download_bytes,
				any(asn) AS asn, any(organization) AS organization, any(country) AS country, any(region) AS region,
				any(city) AS city, max(estimated) AS estimated, any(gap_reason) AS gap_reason
			FROM observer.connection_presence_minute FINAL WHERE ` + where + `
			GROUP BY bucket, device_id, flow_id, protocol, source_ip, source_port, destination_ip, destination_port
		)
		GROUP BY device_id, flow_id, protocol, source_ip, source_port, destination_ip, destination_port
		HAVING sum(active_seconds) >= ?
		ORDER BY sum(active_seconds) DESC LIMIT ?`
}

func (s *Store) EnrichIP(ip string) domain.Enrichment {
	result := domain.Enrichment{IP: ip, Status: "unknown"}
	if s.Enricher == nil {
		result.Reason = "database_unconfigured"
		return result
	}
	record, version, reason := s.Enricher.Lookup(ip)
	result.ASN, result.Organization, result.Country, result.Region, result.City = record.ASN, record.Organization, record.Country, record.Region, record.City
	result.Latitude, result.Longitude, result.CoordinatesAvailable = record.Latitude, record.Longitude, record.CoordinatesAvailable
	result.Version, result.UpdatedAt = version, s.Enricher.UpdatedAt()
	result.Reason = reason
	if reason == "matched" {
		result.Status = "matched"
	}
	return result
}

func (s *Store) DNSRelationships(ctx context.Context, deviceID, ip, domainFilter string, from, to time.Time, limit uint32) ([]domain.DNSRelationship, error) {
	where := `observed_at >= ? AND observed_at < ?`
	args := []any{from.UTC(), to.UTC()}
	if deviceID != "" {
		where += ` AND device_id = ?`
		args = append(args, deviceID)
	}
	if ip != "" {
		where += ` AND ip = ?`
		args = append(args, ip)
	}
	if domainFilter != "" {
		where += ` AND domain = ?`
		args = append(args, domainFilter)
	}
	args = append(args, limit)
	rows, err := s.ClickHouse.QueryContext(ctx, `SELECT domain, ip, device_id, observed_at, expires_at, ttl, source,
		if(countIf(other.domain != current.domain AND greatest(current.observed_at, other.observed_at) < least(current.expires_at, other.expires_at)) > 0, 'ambiguous', 'exact')
		FROM observer.dns_observations AS current FINAL
		LEFT JOIN observer.dns_observations AS other FINAL ON current.device_id = other.device_id AND current.ip = other.ip
		WHERE `+strings.ReplaceAll(where, "observed_at", "current.observed_at")+`
		GROUP BY current.domain, current.ip, current.device_id, current.observed_at, current.expires_at, current.ttl, current.source
		ORDER BY current.observed_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.DNSRelationship, 0)
	for rows.Next() {
		var item domain.DNSRelationship
		if err := rows.Scan(&item.Domain, &item.IP, &item.DeviceID, &item.ObservedAt, &item.ExpiresAt, &item.TTL, &item.Source, &item.Confidence); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) TrafficSummary(ctx context.Context, dimension, deviceID string, from, to time.Time, limit uint32) ([]domain.TrafficPoint, error) {
	if !allowedTrafficDimensions[dimension] {
		return nil, fmt.Errorf("unsupported dimension %q", dimension)
	}
	where := `dimension_type = ? AND bucket >= ? AND bucket < ?`
	args := []any{dimension, from.UTC(), to.UTC()}
	if deviceID != "" {
		where += ` AND device_id = ?`
		args = append(args, deviceID)
	}
	args = append(args, limit)
	rows, err := s.ClickHouse.QueryContext(ctx, `
		SELECT bucket, dimension_type, dimension_value, protocol, direction,
		       sum(bytes) AS bytes, sum(packets) AS packets, max(estimated) AS estimated
		FROM observer.traffic_minute FINAL
		WHERE `+where+`
			GROUP BY bucket, dimension_type, dimension_value, protocol, direction
		ORDER BY bucket, bytes DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	points := make([]domain.TrafficPoint, 0)
	for rows.Next() {
		var point domain.TrafficPoint
		var estimated uint8
		if err := rows.Scan(&point.Bucket, &point.Dimension, &point.Value, &point.Protocol, &point.Direction, &point.Bytes, &point.Packets, &estimated); err != nil {
			return nil, err
		}
		point.Estimated = estimated != 0
		points = append(points, point)
	}
	return points, rows.Err()
}

func (s *Store) TrafficSummaryFiltered(ctx context.Context, filter TrafficFilter) ([]domain.TrafficPoint, error) {
	if !allowedTrafficDimensions[filter.Dimension] {
		return nil, fmt.Errorf("unsupported dimension %q", filter.Dimension)
	}
	conditions, args := trafficConditions(filter)
	args = append(args, filter.Limit)
	rows, err := s.ClickHouse.QueryContext(ctx, `SELECT bucket, dimension_type, dimension_value, protocol, direction, sum(bytes), sum(packets), max(estimated)
		FROM observer.traffic_minute FINAL WHERE `+strings.Join(conditions, " AND ")+` GROUP BY bucket, dimension_type, dimension_value, protocol, direction ORDER BY bucket, sum(bytes) DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.TrafficPoint, 0)
	for rows.Next() {
		var item domain.TrafficPoint
		var estimated uint8
		if err := rows.Scan(&item.Bucket, &item.Dimension, &item.Value, &item.Protocol, &item.Direction, &item.Bytes, &item.Packets, &estimated); err != nil {
			return nil, err
		}
		item.Estimated = estimated != 0
		if item.Direction == "upload" {
			item.UploadBytes = item.Bytes
		} else if item.Direction == "download" {
			item.DownloadBytes = item.Bytes
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func trafficConditions(filter TrafficFilter) ([]string, []any) {
	conditions := []string{"dimension_type = ?", "bucket >= ?", "bucket < ?"}
	args := []any{filter.Dimension, filter.From.UTC(), filter.To.UTC()}
	add := func(column, value string) {
		if value != "" {
			conditions = append(conditions, column+" = ?")
			args = append(args, value)
		}
	}
	add("dimension_value", filter.Value)
	add("device_id", filter.DeviceID)
	add("interface", filter.Interface)
	add("protocol", filter.Protocol)
	add("source_ip", filter.SourceIP)
	add("destination_ip", filter.DestinationIP)
	add("domain", filter.Domain)
	add("organization", filter.Organization)
	add("country", filter.Country)
	add("region", filter.Region)
	add("city", filter.City)
	add("process_name", filter.ProcessName)
	add("container_name", filter.ContainerName)
	add("direction", filter.Direction)
	if filter.LocalPort != 0 {
		conditions = append(conditions, "local_port = ?")
		args = append(args, filter.LocalPort)
	}
	if filter.RemotePort != 0 {
		conditions = append(conditions, "remote_port = ?")
		args = append(args, filter.RemotePort)
	}
	if filter.ASN != 0 {
		conditions = append(conditions, "asn = ?")
		args = append(args, filter.ASN)
	}
	if filter.HideInternal {
		condition, values := internalFlowExclusion("source_ip", "destination_ip", filter.LANNetworks)
		conditions = append(conditions, condition)
		args = append(args, values...)
	}
	return conditions, args
}

var defaultLANNetworks = []string{
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16",
	"fc00::/7", "fe80::/10", "::1/128",
}

func internalFlowExclusion(sourceColumn, destinationColumn string, networks []string) (string, []any) {
	normalized := normalizeLANNetworks(networks)
	source, sourceArgs := ipInNetworks(sourceColumn, normalized)
	destination, destinationArgs := ipInNetworks(destinationColumn, normalized)
	return "NOT (" + source + " AND " + destination + ")", append(sourceArgs, destinationArgs...)
}

func ipInNetworks(column string, networks []string) (string, []any) {
	conditions := make([]string, 0, len(networks))
	args := make([]any, 0, len(networks))
	for _, network := range networks {
		conditions = append(conditions, "isIPAddressInRange("+column+", ?)")
		args = append(args, network)
	}
	return "(" + strings.Join(conditions, " OR ") + ")", args
}

func normalizeLANNetworks(networks []string) []string {
	seen := make(map[string]struct{}, len(defaultLANNetworks)+len(networks))
	result := make([]string, 0, len(defaultLANNetworks)+len(networks))
	for _, candidate := range append(append([]string(nil), defaultLANNetworks...), networks...) {
		_, prefix, err := net.ParseCIDR(strings.TrimSpace(candidate))
		if err != nil {
			continue
		}
		value := prefix.String()
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
