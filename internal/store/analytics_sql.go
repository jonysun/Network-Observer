package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/network-observer/network-observer/internal/domain"
)

// analyticsSource builds a narrow logical input for dashboard analytics. It
// collapses directional traffic rows to one per bucket/device/attribution and
// unions presence rows so exact distinct-flow counts remain available without
// returning the high-cardinality groups to Center.
func analyticsSource(filter AnalyticsFilter, trustedIPs, networks []string) (string, []any) {
	trafficWhere, trafficArgs := analyticsWhere(filter, trustedIPs, networks, false)
	presenceWhere, presenceArgs := analyticsWhere(filter, trustedIPs, networks, true)
	remotePort := analyticsPresenceRemotePortExpression()
	trafficSelect := []string{
		"bucket", "device_id", "'' AS flow_id", "remote_ip", "process_name", "container_name", "domain", "interface", "protocol",
		"organization", "country", "region", "city", "toUInt32(local_port) AS local_port", "toUInt32(remote_port) AS remote_port", "toUInt32(asn) AS asn",
		"if(direction = 'upload', bytes, toUInt64(0)) AS upload_bytes", "if(direction = 'download', bytes, toUInt64(0)) AS download_bytes",
		"estimated", "gap_reason", "toUInt8(1) AS dimensions_available",
	}
	presenceSelect := []string{
		"bucket", "device_id", "flow_id", "remote_ip", "process_name", "container_name", "domain", "interface", "protocol",
		"organization", "country", "region", "city", "toUInt32(local_port) AS local_port", "toUInt32(" + remotePort + ") AS remote_port", "toUInt32(asn) AS asn",
		"toUInt64(0) AS upload_bytes", "toUInt64(0) AS download_bytes", "estimated", "gap_reason", "ranking_dimensions_available AS dimensions_available",
	}
	query := fmt.Sprintf(`WITH source AS (
		SELECT %s FROM observer.traffic_minute FINAL
		WHERE %s AND dimension_type = 'device'
		UNION ALL
		SELECT %s FROM observer.connection_presence_minute FINAL
		WHERE %s AND flow_id != ''
	)`, strings.Join(trafficSelect, ", "), trafficWhere, strings.Join(presenceSelect, ", "), presenceWhere)
	args := append(append([]any{}, trafficArgs...), presenceArgs...)
	return query, args
}

func analyticsDimensionTuple(dimension string) (string, bool) {
	_, ok := analyticsColumns[dimension]
	if !ok {
		return "", false
	}
	expression := analyticsDimensionExpression(dimension, false)
	if dimension == "remote_port" {
		expression = "if(remote_port = 0, '未知端口', toString(remote_port))"
	}
	if dimension == "device" {
		expression = "toString(device_id)"
	}
	return "tuple('" + dimension + "', " + expression + ")", true
}

func analyticsDimensionTuples(dimensions []string) string {
	tuples := make([]string, 0, len(dimensions))
	for _, dimension := range dimensions {
		if tuple, ok := analyticsDimensionTuple(dimension); ok {
			tuples = append(tuples, tuple)
		}
	}
	return "[" + strings.Join(tuples, ", ") + "]"
}

func analyticsRankingsSQL(source string, dimensions []string) string {
	return source + fmt.Sprintf(`,
	grouped AS (
		SELECT dimension_tuple.1 AS dimension_name, dimension_tuple.2 AS value,
			uniqExactIf(tuple(device_id, flow_id), flow_id != '') AS connections,
			sum(upload_bytes) AS upload_sum, sum(download_bytes) AS download_sum,
			sum(upload_bytes) + sum(download_bytes) AS total_bytes,
			max(estimated) AS estimated, minIf(gap_reason, gap_reason != '') AS gap_reason
		FROM source ARRAY JOIN %s AS dimension_tuple
		GROUP BY dimension_name, value
	), ranked AS (
		SELECT *, arrayJoin([
			tuple('connections', connections), tuple('upload', upload_sum),
			tuple('download', download_sum), tuple('total', total_bytes)
		]) AS ranking
		FROM grouped
	)
	SELECT dimension_name, value, ranking.1 AS metric, connections, upload_sum AS upload_bytes, download_sum AS download_bytes,
		total_bytes, estimated, gap_reason
	FROM ranked
	ORDER BY dimension_name, metric, ranking.2 DESC, value ASC
	LIMIT ? BY dimension_name, metric SETTINGS max_execution_time=10`, analyticsDimensionTuples(dimensions))
}

func analyticsRelationshipsSQL(source string) string {
	return source + `,
	pairs AS (
		SELECT pair.1 AS relationship, pair.2 AS from_value, pair.3 AS to_value,
			device_id, flow_id, upload_bytes, download_bytes, estimated, gap_reason
		FROM source ARRAY JOIN [
			tuple('process_to_remote_ip', if(process_name = '', '未知应用', process_name), if(remote_ip = '', '未知', remote_ip)),
			tuple('process_to_domain', if(process_name = '', '未知应用', process_name), if(domain = '', '未知', domain)),
			tuple('remote_ip_to_remote_port', if(remote_ip = '', '未知', remote_ip), if(remote_port = 0, '未知端口', toString(remote_port))),
			tuple('device_to_remote_ip', toString(device_id), if(remote_ip = '', '未知', remote_ip)),
			tuple('protocol_to_remote_ip', if(protocol = '', '未知', protocol), if(remote_ip = '', '未知', remote_ip))
		] AS pair
	), grouped AS (
		SELECT relationship, from_value, to_value,
			uniqExactIf(tuple(device_id, flow_id), flow_id != '') AS connections,
			sum(upload_bytes) AS upload_sum, sum(download_bytes) AS download_sum,
			sum(upload_bytes) + sum(download_bytes) AS total_bytes,
			max(estimated) AS estimated, minIf(gap_reason, gap_reason != '') AS gap_reason
		FROM pairs GROUP BY relationship, from_value, to_value
	)
	SELECT relationship, from_value, to_value, connections, upload_sum AS upload_bytes, download_sum AS download_bytes,
		total_bytes, estimated, gap_reason
	FROM grouped
	ORDER BY relationship, total_bytes DESC, connections DESC, from_value ASC, to_value ASC
	LIMIT ? BY relationship SETTINGS max_execution_time=10`
}

func (s *Store) AnalyticsDashboard(ctx context.Context, filter AnalyticsFilter) (domain.AnalyticsDashboard, error) {
	if err := ValidateAnalyticsFilter(filter); err != nil {
		return domain.AnalyticsDashboard{}, err
	}
	selected := make([]string, 0, len(filter.Dimensions))
	seen := map[string]bool{}
	for _, dimension := range filter.Dimensions {
		dimension = strings.TrimSpace(dimension)
		if !seen[dimension] {
			selected = append(selected, dimension)
			seen[dimension] = true
		}
	}
	if len(selected) == 0 {
		selected = []string{"remote_ip", "process", "domain", "remote_port", "protocol", "interface", "container"}
		for _, dimension := range selected {
			seen[dimension] = true
		}
	}
	queryDimensions := append([]string(nil), selected...)
	if !seen["device"] {
		queryDimensions = append(queryDimensions, "device")
	}
	var networks, trustedIPs []string
	if filter.HideInternal {
		var err error
		networks, err = s.ListLANNetworks(ctx)
		if err != nil {
			return domain.AnalyticsDashboard{}, err
		}
	}
	if filter.Trust != "all" {
		rows, err := s.Postgres.Query(ctx, `SELECT host(ip_address) FROM ip_metadata_overrides WHERE trusted`)
		if err != nil {
			return domain.AnalyticsDashboard{}, err
		}
		for rows.Next() {
			var ip string
			if err := rows.Scan(&ip); err != nil {
				rows.Close()
				return domain.AnalyticsDashboard{}, err
			}
			trustedIPs = append(trustedIPs, ip)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return domain.AnalyticsDashboard{}, err
		}
		rows.Close()
	}

	now := time.Now().UTC()
	seriesBucket := analyticsSeriesBucketSize(filter.From, filter.To)
	dashboard := domain.AnalyticsDashboard{
		DeviceRankings:           []domain.AnalyticsMetric{},
		DeviceConnectionRankings: []domain.AnalyticsMetric{},
		DimensionRankings:        map[string]map[string][]domain.AnalyticsMetric{},
		RelationshipRankings:     map[string][]domain.AnalyticsRelationship{},
		Capabilities: domain.AnalyticsCapabilities{ConfirmedTCPConnections: domain.Capability{
			Name: "confirmed_tcp_connections", Status: "unavailable",
			Detail: "仅 Linux 内核 TCP 状态事件可确认；当前数据源未提供确认建立事件", CheckedAt: now,
		}},
		Coverage:   domain.AnalyticsCoverage{From: filter.From.UTC(), To: filter.To.UTC(), BucketSeconds: uint32(seriesBucket / time.Second)},
		Timeseries: []domain.AnalyticsTimeseriesPoint{},
	}
	for _, dimension := range selected {
		dashboard.DimensionRankings[dimension] = map[string][]domain.AnalyticsMetric{
			"connections": {},
			"upload":      {},
			"download":    {},
			"total":       {},
		}
	}
	for _, relationship := range []string{
		"process_to_remote_ip", "process_to_domain", "remote_ip_to_remote_port",
		"device_to_remote_ip", "protocol_to_remote_ip",
	} {
		dashboard.RelationshipRankings[relationship] = []domain.AnalyticsRelationship{}
	}

	source, sourceArgs := analyticsSource(filter, trustedIPs, networks)
	var legacyRows uint64
	presenceWhere, presenceArgs := analyticsWhere(filter, trustedIPs, networks, true)
	legacyQuery := `SELECT count() FROM (
		SELECT 1 FROM observer.connection_presence_minute FINAL WHERE ` + presenceWhere + ` AND flow_id != '' AND ranking_dimensions_available = 0 LIMIT 1
	) SETTINGS max_execution_time=5`
	if err := s.ClickHouse.QueryRowContext(ctx, legacyQuery, presenceArgs...).Scan(&legacyRows); err != nil {
		return domain.AnalyticsDashboard{}, err
	}
	dashboard.Coverage.LegacyDimensions = legacyRows > 0

	var summaryEstimated uint8
	if err := s.ClickHouse.QueryRowContext(ctx, source+` SELECT
		uniqExactIf(tuple(device_id, flow_id), flow_id != ''), sum(upload_bytes), sum(download_bytes),
		uniqExactIf(device_id, upload_bytes + download_bytes > 0), max(estimated), minIf(gap_reason, gap_reason != '')
		FROM source SETTINGS max_execution_time=10`, sourceArgs...).Scan(
		&dashboard.Summary.Connections, &dashboard.Summary.UploadBytes, &dashboard.Summary.DownloadBytes,
		&dashboard.DevicesWithTraffic, &summaryEstimated, &dashboard.Summary.GapReason); err != nil {
		return domain.AnalyticsDashboard{}, err
	}
	dashboard.Summary.DeviceID = filter.DeviceID
	dashboard.Summary.TotalBytes = dashboard.Summary.UploadBytes + dashboard.Summary.DownloadBytes
	dashboard.Summary.Estimated = summaryEstimated != 0
	dashboard.Summary.Availability = "available"

	bucketExpression := fmt.Sprintf("toDateTime(toStartOfInterval(bucket, INTERVAL %d HOUR, 'UTC'), 'UTC')", seriesBucket/time.Hour)
	timeseriesQuery := source + ` SELECT ` + bucketExpression + ` AS series_bucket,
		sum(upload_bytes), sum(download_bytes), uniqExactIf(tuple(device_id, flow_id), flow_id != '')
		FROM source GROUP BY series_bucket ORDER BY series_bucket SETTINGS max_execution_time=10`
	timeseriesRows, err := s.ClickHouse.QueryContext(ctx, timeseriesQuery, sourceArgs...)
	if err != nil {
		return domain.AnalyticsDashboard{}, err
	}
	type seriesValue struct{ upload, download, connections uint64 }
	series := make(map[time.Time]seriesValue)
	for timeseriesRows.Next() {
		var bucket time.Time
		var value seriesValue
		if err := timeseriesRows.Scan(&bucket, &value.upload, &value.download, &value.connections); err != nil {
			timeseriesRows.Close()
			return domain.AnalyticsDashboard{}, err
		}
		series[bucket.UTC()] = value
	}
	if err := timeseriesRows.Err(); err != nil {
		timeseriesRows.Close()
		return domain.AnalyticsDashboard{}, err
	}
	timeseriesRows.Close()
	if len(series) > 0 {
		for bucket := filter.From.UTC().Truncate(seriesBucket); bucket.Before(filter.To); bucket = bucket.Add(seriesBucket) {
			point := domain.AnalyticsTimeseriesPoint{Bucket: bucket}
			if value, ok := series[bucket]; ok {
				upload, download, connections := value.upload, value.download, value.connections
				point.UploadBytes, point.DownloadBytes, point.ObservedConnections = &upload, &download, &connections
			} else {
				dashboard.Coverage.HasUnobservedBuckets = true
			}
			dashboard.Timeseries = append(dashboard.Timeseries, point)
		}
	}

	rankingQuery := analyticsRankingsSQL(source, queryDimensions)
	rankingArgs := append(append([]any(nil), sourceArgs...), filter.Limit)
	rankingRows, err := s.ClickHouse.QueryContext(ctx, rankingQuery, rankingArgs...)
	if err != nil {
		return domain.AnalyticsDashboard{}, err
	}
	for rankingRows.Next() {
		var dimension, value, metricName, gap string
		var metric domain.AnalyticsMetric
		var estimated uint8
		if err := rankingRows.Scan(&dimension, &value, &metricName, &metric.Connections, &metric.UploadBytes, &metric.DownloadBytes, &metric.TotalBytes, &estimated, &gap); err != nil {
			rankingRows.Close()
			return domain.AnalyticsDashboard{}, err
		}
		metric.Value, metric.DeviceID, metric.Estimated, metric.GapReason, metric.Availability = value, filter.DeviceID, estimated != 0, gap, "available"
		if dimension == "device" {
			metric.DeviceID = value
		}
		if dimension == "device" && metricName == "total" {
			dashboard.DeviceRankings = append(dashboard.DeviceRankings, metric)
		}
		if dimension == "device" && metricName == "connections" {
			dashboard.DeviceConnectionRankings = append(dashboard.DeviceConnectionRankings, metric)
		}
		if dimension != "device" && seen[dimension] {
			if dashboard.DimensionRankings[dimension] == nil {
				dashboard.DimensionRankings[dimension] = map[string][]domain.AnalyticsMetric{}
			}
			dashboard.DimensionRankings[dimension][metricName] = append(dashboard.DimensionRankings[dimension][metricName], metric)
		}
	}
	if err := rankingRows.Err(); err != nil {
		rankingRows.Close()
		return domain.AnalyticsDashboard{}, err
	}
	rankingRows.Close()

	if !filter.SkipRelationships && filter.DetailDimension == "" {
		relationshipQuery := analyticsRelationshipsSQL(source)
		relationshipArgs := append(append([]any(nil), sourceArgs...), filter.Limit)
		rows, err := s.ClickHouse.QueryContext(ctx, relationshipQuery, relationshipArgs...)
		if err != nil {
			return domain.AnalyticsDashboard{}, err
		}
		for rows.Next() {
			var relationship domain.AnalyticsRelationship
			var name string
			var estimated uint8
			if err := rows.Scan(&name, &relationship.From, &relationship.To, &relationship.Connections,
				&relationship.UploadBytes, &relationship.DownloadBytes, &relationship.TotalBytes, &estimated, &relationship.GapReason); err != nil {
				rows.Close()
				return domain.AnalyticsDashboard{}, err
			}
			relationship.Estimated, relationship.Availability = estimated != 0, "available"
			dashboard.RelationshipRankings[name] = append(dashboard.RelationshipRankings[name], relationship)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return domain.AnalyticsDashboard{}, err
		}
		rows.Close()
	}

	if filter.DetailDimension == "" && !filter.SkipRelationships {
		where := "active = 1 AND observed_at > now64(3) - INTERVAL 15 MINUTE"
		var args []any
		if filter.DeviceID != "" {
			where += " AND device_id = ?"
			args = append(args, filter.DeviceID)
		}
		if filter.HideInternal {
			condition, values := internalFlowExclusion("source_ip", "destination_ip", networks)
			where += " AND " + condition
			args = append(args, values...)
		}
		if filter.Trust == "trusted" || filter.Trust == "untrusted" {
			if len(trustedIPs) > 0 {
				condition, values := analyticsTrustedIPCondition("if(local_is_source = 1, destination_ip, source_ip)", trustedIPs, filter.Trust == "untrusted")
				where += " AND " + condition
				args = append(args, values...)
			} else if filter.Trust == "trusted" {
				where += " AND 0"
			}
		}
		var active uint64
		if err := s.ClickHouse.QueryRowContext(ctx, `SELECT uniqExact(device_id, flow_id) FROM observer.live_connections FINAL WHERE `+where+` AND flow_id != '' SETTINGS max_execution_time=4`, args...).Scan(&active); err != nil {
			return domain.AnalyticsDashboard{}, err
		}
		dashboard.ActiveConnections = &active
	}
	return dashboard, nil
}
