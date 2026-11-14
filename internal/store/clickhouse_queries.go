package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/network-observer/network-observer/internal/domain"
)

var allowedTrafficDimensions = map[string]bool{
	"device": true, "interface": true, "protocol": true, "source_ip": true,
	"destination_ip": true, "source_port": true, "destination_port": true,
	"process": true, "container": true, "service": true,
}

func (s *Store) ListConnections(ctx context.Context, deviceID, protocol, query string, limit, offset uint32) ([]domain.FlowObservation, uint64, error) {
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
		conditions = append(conditions, "positionCaseInsensitive(concat(source_ip, ':', toString(source_port), ' ', destination_ip, ':', toString(destination_port), ' ', process_name, ' ', container_name, ' ', service_name), ?) > 0")
		args = append(args, query)
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
		       attribution_reason, sampling_rate, counter_source, confidence, active
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
			&item.SamplingRate, &item.CounterSource, &item.Confidence, &active,
		); err != nil {
			return nil, 0, err
		}
		item.ID = item.DeviceID + ":" + item.FlowID
		item.Active = active != 0
		items = append(items, item)
	}
	return items, total, rows.Err()
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
