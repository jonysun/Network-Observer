package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/network-observer/network-observer/internal/domain"
)

type analyticsTrafficRow struct {
	bucket                                                                                               time.Time
	deviceID, remoteIP, process, container, domain, iface, protocol, organization, country, region, city string
	localPort, remotePort, asn                                                                           uint32
	upload, download                                                                                     uint64
	estimated                                                                                            uint8
	gap                                                                                                  string
}

type analyticsPresenceRow struct {
	bucket                                                                                                       time.Time
	deviceID, flowID, remoteIP, process, container, domain, iface, protocol, organization, country, region, city string
	localPort, remotePort, asn                                                                                   uint32
	estimated, dimensionsAvailable                                                                               uint8
	gap                                                                                                          string
}

type analyticsAggregate struct {
	domain.AnalyticsMetric
	flows map[string]struct{}
}

type analyticsRelationshipAggregate struct {
	domain.AnalyticsRelationship
	flows map[string]struct{}
}

type AnalyticsFilter struct {
	DeviceID, Trust, DetailDimension, DetailValue string
	From, To                                      time.Time
	Dimensions                                    []string
	Limit                                         uint32
	HideInternal                                  bool
}

var analyticsColumns = map[string]string{
	"device": "toString(device_id)", "remote_ip": "remote_ip", "process": "process_name", "domain": "domain",
	"remote_port": "remote_port", "local_port": "local_port", "protocol": "protocol", "interface": "interface",
	"container": "container_name", "asn": "asn", "organization": "organization", "country": "country", "region": "region", "city": "city",
}

var ErrAnalyticsResultLimit = errors.New("analytics input exceeded its result budget")

const analyticsRowLimit = 200000

func ValidateAnalyticsFilter(filter AnalyticsFilter) error {
	if filter.Limit < 1 || filter.Limit > 50 {
		return fmt.Errorf("analytics limit must be between 1 and 50")
	}
	if filter.Trust != "all" && filter.Trust != "trusted" && filter.Trust != "untrusted" {
		return fmt.Errorf("trusted must be all, trusted or untrusted")
	}
	if filter.DeviceID != "" {
		if _, err := uuid.Parse(filter.DeviceID); err != nil {
			return fmt.Errorf("device_id must be a UUID")
		}
	}
	if len(filter.Dimensions) > len(analyticsColumns) {
		return fmt.Errorf("too many analytics dimensions")
	}
	for _, item := range filter.Dimensions {
		if _, ok := analyticsColumns[strings.TrimSpace(item)]; !ok {
			return fmt.Errorf("unsupported analytics dimension %q", item)
		}
	}
	if filter.DetailDimension != "" || filter.DetailValue != "" {
		if _, ok := analyticsColumns[filter.DetailDimension]; !ok || filter.DetailValue == "" || len(filter.DetailValue) > 4096 {
			return fmt.Errorf("invalid analytics detail filter")
		}
	}
	return nil
}

func analyticsDimensionExpression(dimension string, presence bool) string {
	column := analyticsColumns[dimension]
	if dimension == "remote_port" && presence {
		column = analyticsPresenceRemotePortExpression()
	}
	switch dimension {
	case "remote_port", "local_port":
		return "if(" + column + " = 0, '未知端口', toString(" + column + "))"
	case "asn":
		return "if(asn = 0, '未知 ASN', toString(asn))"
	case "process":
		return "if(process_name = '', '未知应用', process_name)"
	default:
		return "if(" + column + " = '', '未知', " + column + ")"
	}
}

func analyticsWhere(filter AnalyticsFilter, trustedIPs, networks []string, presence bool) (string, []any) {
	where := "bucket >= ? AND bucket < ?"
	args := []any{filter.From.UTC(), filter.To.UTC()}
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
			condition, values := analyticsTrustedIPCondition("remote_ip", trustedIPs, filter.Trust == "untrusted")
			where += " AND " + condition
			args = append(args, values...)
		} else if filter.Trust == "trusted" {
			where += " AND 0"
		}
	}
	if filter.DetailDimension != "" {
		where += " AND " + analyticsDimensionExpression(filter.DetailDimension, presence) + " = ?"
		args = append(args, filter.DetailValue)
	}
	return where, args
}

func (s *Store) AnalyticsDashboard(ctx context.Context, filter AnalyticsFilter) (domain.AnalyticsDashboard, error) {
	if err := ValidateAnalyticsFilter(filter); err != nil {
		return domain.AnalyticsDashboard{}, err
	}
	from, to := filter.From, filter.To
	selected := make([]string, 0, len(filter.Dimensions))
	seen := map[string]bool{}
	for _, item := range filter.Dimensions {
		item = strings.TrimSpace(item)
		if !seen[item] {
			selected = append(selected, item)
			seen[item] = true
		}
	}
	if len(selected) == 0 {
		selected = []string{"remote_ip", "process", "domain", "remote_port", "protocol", "interface", "container"}
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
		defer rows.Close()
		for rows.Next() {
			var ip string
			if err := rows.Scan(&ip); err != nil {
				return domain.AnalyticsDashboard{}, err
			}
			trustedIPs = append(trustedIPs, ip)
		}
		if err := rows.Err(); err != nil {
			return domain.AnalyticsDashboard{}, err
		}
	}

	seriesBucket := analyticsSeriesBucketSize(from, to)
	// Group in the database before transferring historical rows. The interval is derived only from the validated time range.
	bucketExpression := fmt.Sprintf("toDateTime(toStartOfInterval(bucket, INTERVAL %d HOUR, 'UTC'), 'UTC')", seriesBucket/time.Hour)
	trafficWhere, trafficArgs := analyticsWhere(filter, trustedIPs, networks, false)
	trafficRows, err := s.ClickHouse.QueryContext(ctx, `
        SELECT `+bucketExpression+` AS series_bucket, toString(device_id), remote_ip, process_name, container_name, domain, interface, protocol,
               organization, country, region, city, toUInt32(local_port), toUInt32(remote_port), toUInt32(asn),
               sumIf(bytes, direction='upload'), sumIf(bytes, direction='download'), max(estimated), minIf(gap_reason, gap_reason != '')
        FROM observer.traffic_minute FINAL
        WHERE `+trafficWhere+` AND dimension_type = 'device'
        GROUP BY series_bucket, device_id, remote_ip, process_name, container_name, domain, interface, protocol,
                 organization, country, region, city, local_port, remote_port, asn
        LIMIT ? SETTINGS max_execution_time=12`, append(trafficArgs, analyticsRowLimit+1)...)
	if err != nil {
		return domain.AnalyticsDashboard{}, err
	}
	defer trafficRows.Close()

	traffic := make([]analyticsTrafficRow, 0)
	for trafficRows.Next() {
		if len(traffic) == analyticsRowLimit {
			return domain.AnalyticsDashboard{}, ErrAnalyticsResultLimit
		}
		var row analyticsTrafficRow
		if err := trafficRows.Scan(&row.bucket, &row.deviceID, &row.remoteIP, &row.process, &row.container, &row.domain, &row.iface, &row.protocol,
			&row.organization, &row.country, &row.region, &row.city, &row.localPort, &row.remotePort, &row.asn,
			&row.upload, &row.download, &row.estimated, &row.gap); err != nil {
			return domain.AnalyticsDashboard{}, err
		}
		traffic = append(traffic, row)
	}
	if err := trafficRows.Err(); err != nil {
		return domain.AnalyticsDashboard{}, err
	}

	presenceWhere, presenceArgs := analyticsWhere(filter, trustedIPs, networks, true)
	presenceRemotePort := analyticsPresenceRemotePortExpression()
	presenceRows, err := s.ClickHouse.QueryContext(ctx, `
        SELECT `+bucketExpression+` AS series_bucket, toString(device_id), flow_id, remote_ip, process_name, container_name, domain, interface, protocol,
               organization, country, region, city, toUInt32(asn), toUInt32(local_port), toUInt32(`+presenceRemotePort+`),
               max(estimated), minIf(gap_reason, gap_reason != ''), min(ranking_dimensions_available)
        FROM observer.connection_presence_minute FINAL
        WHERE `+presenceWhere+` AND flow_id != ''
        GROUP BY series_bucket, device_id, flow_id, remote_ip, process_name, container_name, domain, interface, protocol,
                 organization, country, region, city, asn, local_port, `+presenceRemotePort+`
        LIMIT ? SETTINGS max_execution_time=12`, append(presenceArgs, analyticsRowLimit+1)...)
	if err != nil {
		return domain.AnalyticsDashboard{}, err
	}
	defer presenceRows.Close()
	presence := make([]analyticsPresenceRow, 0)
	for presenceRows.Next() {
		if len(presence) == analyticsRowLimit {
			return domain.AnalyticsDashboard{}, ErrAnalyticsResultLimit
		}
		var row analyticsPresenceRow
		if err := presenceRows.Scan(&row.bucket, &row.deviceID, &row.flowID, &row.remoteIP, &row.process, &row.container, &row.domain, &row.iface, &row.protocol, &row.organization, &row.country, &row.region, &row.city, &row.asn,
			&row.localPort, &row.remotePort, &row.estimated, &row.gap, &row.dimensionsAvailable); err != nil {
			return domain.AnalyticsDashboard{}, err
		}
		presence = append(presence, row)
	}
	if err := presenceRows.Err(); err != nil {
		return domain.AnalyticsDashboard{}, err
	}

	var active *uint64
	if filter.DetailDimension == "" {
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
		var count uint64
		if err := s.ClickHouse.QueryRowContext(ctx, `SELECT uniqExact(device_id, flow_id) FROM observer.live_connections FINAL WHERE `+where+` AND flow_id != '' SETTINGS max_execution_time=4`, args...).Scan(&count); err != nil {
			return domain.AnalyticsDashboard{}, err
		}
		active = &count
	}
	return buildAnalyticsDashboard(filter, selected, traffic, presence, active), nil
}

func buildAnalyticsDashboard(filter AnalyticsFilter, selected []string, traffic []analyticsTrafficRow, presence []analyticsPresenceRow, active *uint64) domain.AnalyticsDashboard {
	from, to, limit := filter.From, filter.To, filter.Limit
	seriesBucket := analyticsSeriesBucketSize(from, to)

	dashboard := domain.AnalyticsDashboard{
		DimensionRankings:    map[string]map[string][]domain.AnalyticsMetric{},
		RelationshipRankings: map[string][]domain.AnalyticsRelationship{},
		Capabilities:         domain.AnalyticsCapabilities{ConfirmedTCPConnections: domain.Capability{Name: "confirmed_tcp_connections", Status: "unavailable", Detail: "仅 Linux 内核 TCP 状态事件可确认；当前数据源未提供确认建立事件", CheckedAt: time.Now().UTC()}},
		Coverage:             domain.AnalyticsCoverage{From: from.UTC(), To: to.UTC(), BucketSeconds: uint32(seriesBucket / time.Second)},
		ActiveConnections:    active,
		Timeseries:           []domain.AnalyticsTimeseriesPoint{},
	}
	type bucketTotals struct{ upload, download uint64 }
	times := map[time.Time]*bucketTotals{}
	timeFlows := map[time.Time]map[string]struct{}{}
	metricGroups := map[string]map[string]*analyticsAggregate{}
	for _, dimension := range selected {
		metricGroups[dimension] = map[string]*analyticsAggregate{}
	}
	deviceGroups := map[string]*analyticsAggregate{}

	addMetric := func(groups map[string]*analyticsAggregate, value, groupDevice, flowDevice, flowID string, upload, download uint64, estimated uint8, gap string) {
		if value == "" {
			value = "未知"
		}
		key := groupDevice + "\x00" + value
		item := groups[key]
		if item == nil {
			item = &analyticsAggregate{AnalyticsMetric: domain.AnalyticsMetric{Value: value, DeviceID: groupDevice, Availability: "available"}, flows: map[string]struct{}{}}
			groups[key] = item
		}
		item.UploadBytes += upload
		item.DownloadBytes += download
		item.TotalBytes += upload + download
		if addDistinctAnalyticsFlow(item.flows, flowDevice, flowID) {
			item.Connections++
		}
		item.Estimated = item.Estimated || estimated != 0
		if gap != "" && (item.GapReason == "" || gap < item.GapReason) {
			item.GapReason = gap
		}
	}
	dimensionValue := func(row analyticsTrafficRow, dimension string) string {
		switch dimension {
		case "device":
			return row.deviceID
		case "remote_ip":
			return row.remoteIP
		case "process":
			if row.process == "" {
				return "未知应用"
			}
			return row.process
		case "domain":
			return row.domain
		case "remote_port":
			return analyticsPortLabel(row.remotePort)
		case "local_port":
			return analyticsPortLabel(row.localPort)
		case "protocol":
			return row.protocol
		case "interface":
			return row.iface
		case "container":
			return row.container
		case "asn":
			if row.asn == 0 {
				return "未知 ASN"
			}
			return fmt.Sprint(row.asn)
		case "organization":
			return row.organization
		case "country":
			return row.country
		case "region":
			return row.region
		case "city":
			return row.city
		}
		return ""
	}
	presenceValue := func(row analyticsPresenceRow, dimension string) string {
		switch dimension {
		case "device":
			return row.deviceID
		case "remote_ip":
			return row.remoteIP
		case "process":
			if row.process == "" {
				return "未知应用"
			}
			return row.process
		case "domain":
			return row.domain
		case "remote_port":
			return analyticsPortLabel(row.remotePort)
		case "local_port":
			return analyticsPortLabel(row.localPort)
		case "protocol":
			return row.protocol
		case "interface":
			return row.iface
		case "container":
			return row.container
		case "organization":
			return row.organization
		case "country":
			return row.country
		case "region":
			return row.region
		case "city":
			return row.city
		case "asn":
			if row.asn == 0 {
				return "未知 ASN"
			}
			return fmt.Sprint(row.asn)
		}
		return ""
	}
	for _, row := range traffic {
		bucket := row.bucket.UTC().Truncate(seriesBucket)
		totals := times[bucket]
		if totals == nil {
			totals = &bucketTotals{}
			times[bucket] = totals
		}
		totals.upload += row.upload
		totals.download += row.download
		addMetric(deviceGroups, row.deviceID, row.deviceID, row.deviceID, "", row.upload, row.download, row.estimated, row.gap)
		for _, dimension := range selected {
			addMetric(metricGroups[dimension], dimensionValue(row, dimension), filter.DeviceID, row.deviceID, "", row.upload, row.download, row.estimated, row.gap)
		}
	}
	for _, row := range presence {
		if row.flowID == "" {
			continue
		}
		if row.dimensionsAvailable == 0 {
			dashboard.Coverage.LegacyDimensions = true
		}
		bucket := row.bucket.UTC().Truncate(seriesBucket)
		if timeFlows[bucket] == nil {
			timeFlows[bucket] = map[string]struct{}{}
		}
		if row.flowID != "" {
			timeFlows[bucket][row.deviceID+"\x00"+row.flowID] = struct{}{}
		}
		if times[bucket] == nil {
			times[bucket] = &bucketTotals{}
		}
		addMetric(deviceGroups, row.deviceID, row.deviceID, row.deviceID, row.flowID, 0, 0, row.estimated, row.gap)
		for _, dimension := range selected {
			addMetric(metricGroups[dimension], presenceValue(row, dimension), filter.DeviceID, row.deviceID, row.flowID, 0, 0, row.estimated, row.gap)
		}
	}
	dashboard.Summary.Availability = "available"
	dashboard.Summary.DeviceID = filter.DeviceID
	for _, item := range deviceGroups {
		dashboard.Summary.Connections += item.Connections
		dashboard.Summary.UploadBytes += item.UploadBytes
		dashboard.Summary.DownloadBytes += item.DownloadBytes
		dashboard.Summary.TotalBytes += item.TotalBytes
		if item.TotalBytes > 0 {
			dashboard.DevicesWithTraffic++
		}
		dashboard.Summary.Estimated = dashboard.Summary.Estimated || item.Estimated
		if item.GapReason != "" && (dashboard.Summary.GapReason == "" || item.GapReason < dashboard.Summary.GapReason) {
			dashboard.Summary.GapReason = item.GapReason
		}
	}
	// Leave unobserved buckets null. Plotting zero there would imply coverage we do not have.
	if len(times) > 0 {
		for bucket := from.UTC().Truncate(seriesBucket); bucket.Before(to); bucket = bucket.Add(seriesBucket) {
			point := domain.AnalyticsTimeseriesPoint{Bucket: bucket}
			if totals := times[bucket]; totals != nil {
				point.UploadBytes = &totals.upload
				point.DownloadBytes = &totals.download
				observed := uint64(len(timeFlows[bucket]))
				point.ObservedConnections = &observed
			} else {
				dashboard.Coverage.HasUnobservedBuckets = true
			}
			dashboard.Timeseries = append(dashboard.Timeseries, point)
		}
	}

	dashboard.DeviceRankings = sortAnalyticsMetrics(deviceGroups, limit)
	dashboard.DeviceConnectionRankings = sortAnalyticsMetricsBy(deviceGroups, limit, func(item domain.AnalyticsMetric) uint64 { return item.Connections })
	for _, dimension := range selected {
		groups := metricGroups[dimension]
		dashboard.DimensionRankings[dimension] = map[string][]domain.AnalyticsMetric{
			"connections": sortAnalyticsMetricsBy(groups, limit, func(item domain.AnalyticsMetric) uint64 { return item.Connections }),
			"upload":      sortAnalyticsMetricsBy(groups, limit, func(item domain.AnalyticsMetric) uint64 { return item.UploadBytes }),
			"download":    sortAnalyticsMetricsBy(groups, limit, func(item domain.AnalyticsMetric) uint64 { return item.DownloadBytes }),
			"total":       sortAnalyticsMetricsBy(groups, limit, func(item domain.AnalyticsMetric) uint64 { return item.TotalBytes }),
		}
	}
	relationshipGroups := map[string]map[string]*analyticsRelationshipAggregate{
		"process_to_remote_ip":     {},
		"process_to_domain":        {},
		"remote_ip_to_remote_port": {},
		"device_to_remote_ip":      {},
		"protocol_to_remote_ip":    {},
	}
	addRelationship := func(name, from, to, device, flowID string, upload, download uint64, estimated uint8, gap string) {
		if from == "" {
			from = "未知"
			if name == "process_to_remote_ip" || name == "process_to_domain" {
				from = "未知应用"
			}
		}
		if to == "" {
			to = "未知"
		}
		key := from + "\x00" + to
		item := relationshipGroups[name][key]
		if item == nil {
			item = &analyticsRelationshipAggregate{AnalyticsRelationship: domain.AnalyticsRelationship{From: from, To: to, Availability: "available"}, flows: map[string]struct{}{}}
			relationshipGroups[name][key] = item
		}
		item.UploadBytes += upload
		item.DownloadBytes += download
		item.TotalBytes += upload + download
		item.Estimated = item.Estimated || estimated != 0
		if gap != "" && (item.GapReason == "" || gap < item.GapReason) {
			item.GapReason = gap
		}
		if addDistinctAnalyticsFlow(item.flows, device, flowID) {
			item.Connections++
		}
	}
	for _, row := range traffic {
		remotePort := analyticsPortLabel(row.remotePort)
		addRelationship("process_to_remote_ip", row.process, row.remoteIP, row.deviceID, "", row.upload, row.download, row.estimated, row.gap)
		addRelationship("process_to_domain", row.process, row.domain, row.deviceID, "", row.upload, row.download, row.estimated, row.gap)
		addRelationship("remote_ip_to_remote_port", row.remoteIP, remotePort, row.deviceID, "", row.upload, row.download, row.estimated, row.gap)
		addRelationship("device_to_remote_ip", row.deviceID, row.remoteIP, row.deviceID, "", row.upload, row.download, row.estimated, row.gap)
		addRelationship("protocol_to_remote_ip", row.protocol, row.remoteIP, row.deviceID, "", row.upload, row.download, row.estimated, row.gap)
	}
	for _, row := range presence {
		if row.flowID == "" {
			continue
		}
		remotePort := analyticsPortLabel(row.remotePort)
		addRelationship("process_to_remote_ip", row.process, row.remoteIP, row.deviceID, row.flowID, 0, 0, row.estimated, row.gap)
		addRelationship("process_to_domain", row.process, row.domain, row.deviceID, row.flowID, 0, 0, row.estimated, row.gap)
		addRelationship("remote_ip_to_remote_port", row.remoteIP, remotePort, row.deviceID, row.flowID, 0, 0, row.estimated, row.gap)
		addRelationship("device_to_remote_ip", row.deviceID, row.remoteIP, row.deviceID, row.flowID, 0, 0, row.estimated, row.gap)
		addRelationship("protocol_to_remote_ip", row.protocol, row.remoteIP, row.deviceID, row.flowID, 0, 0, row.estimated, row.gap)
	}
	for name, groups := range relationshipGroups {
		relationships := make([]domain.AnalyticsRelationship, 0, len(groups))
		for _, item := range groups {
			relationships = append(relationships, item.AnalyticsRelationship)
		}
		sort.Slice(relationships, func(i, j int) bool {
			if relationships[i].TotalBytes != relationships[j].TotalBytes {
				return relationships[i].TotalBytes > relationships[j].TotalBytes
			}
			if relationships[i].Connections != relationships[j].Connections {
				return relationships[i].Connections > relationships[j].Connections
			}
			if relationships[i].From != relationships[j].From {
				return relationships[i].From < relationships[j].From
			}
			return relationships[i].To < relationships[j].To
		})
		if len(relationships) > int(limit) {
			relationships = relationships[:limit]
		}
		dashboard.RelationshipRankings[name] = relationships
	}
	return dashboard
}

func analyticsTrustedIPCondition(column string, trustedIPs []string, negate bool) (string, []any) {
	placeholders := make([]string, len(trustedIPs))
	args := make([]any, len(trustedIPs))
	for i, ip := range trustedIPs {
		placeholders[i] = "?"
		args[i] = ip
	}
	operator := " IN "
	if negate {
		operator = " NOT IN "
	}
	return column + operator + "(" + strings.Join(placeholders, ", ") + ")", args
}

func analyticsPresenceRemotePortExpression() string {
	return `multiIf(
        remote_ip = '' OR local_port = 0, 0,
        remote_ip = destination_ip AND local_port = source_port, destination_port,
        remote_ip = source_ip AND local_port = destination_port, source_port,
        0)`
}

func analyticsSeriesBucketSize(from, to time.Time) time.Duration {
	switch span := to.Sub(from); {
	case span <= 48*time.Hour:
		return time.Hour
	case span <= 8*24*time.Hour:
		return 6 * time.Hour
	default:
		return 24 * time.Hour
	}
}

func analyticsPortLabel(port uint32) string {
	if port == 0 {
		return "未知端口"
	}
	return fmt.Sprint(port)
}

func addDistinctAnalyticsFlow(flows map[string]struct{}, deviceID, flowID string) bool {
	if flowID == "" {
		return false
	}
	key := deviceID + "\x00" + flowID
	if _, exists := flows[key]; exists {
		return false
	}
	flows[key] = struct{}{}
	return true
}

func sortAnalyticsMetrics(groups map[string]*analyticsAggregate, limit uint32) []domain.AnalyticsMetric {
	return sortAnalyticsMetricsBy(groups, limit, func(item domain.AnalyticsMetric) uint64 { return item.TotalBytes })
}
func sortAnalyticsMetricsBy(groups map[string]*analyticsAggregate, limit uint32, score func(domain.AnalyticsMetric) uint64) []domain.AnalyticsMetric {
	items := make([]domain.AnalyticsMetric, 0, len(groups))
	for _, item := range groups {
		items = append(items, item.AnalyticsMetric)
	}
	sort.Slice(items, func(i, j int) bool {
		left, right := score(items[i]), score(items[j])
		if left != right {
			return left > right
		}
		return items[i].Value < items[j].Value
	})
	if len(items) > int(limit) {
		items = items[:limit]
	}
	return items
}
