package domain

import "time"

type AnalyticsMetric struct {
	Value             string  `json:"value"`
	DeviceID          string  `json:"device_id,omitempty"`
	Connections       uint64  `json:"connections"`
	ConfirmedTCPOpens *uint64 `json:"confirmed_tcp_opens"`
	UploadBytes       uint64  `json:"upload_bytes"`
	DownloadBytes     uint64  `json:"download_bytes"`
	TotalBytes        uint64  `json:"total_bytes"`
	Estimated         bool    `json:"estimated"`
	GapReason         string  `json:"gap_reason,omitempty"`
	Availability      string  `json:"availability"`
}

type AnalyticsTimeseriesPoint struct {
	Bucket              time.Time `json:"bucket"`
	UploadBytes         *uint64   `json:"upload_bytes"`
	DownloadBytes       *uint64   `json:"download_bytes"`
	ObservedConnections *uint64   `json:"observed_connections"`
	ConfirmedTCPOpens   *uint64   `json:"confirmed_tcp_opens"`
}

type AnalyticsRelationship struct {
	From              string  `json:"from"`
	To                string  `json:"to"`
	Connections       uint64  `json:"connections"`
	ConfirmedTCPOpens *uint64 `json:"confirmed_tcp_opens"`
	UploadBytes       uint64  `json:"upload_bytes"`
	DownloadBytes     uint64  `json:"download_bytes"`
	TotalBytes        uint64  `json:"total_bytes"`
	Estimated         bool    `json:"estimated"`
	GapReason         string  `json:"gap_reason,omitempty"`
	Availability      string  `json:"availability"`
}

type AnalyticsCapabilities struct {
	ConfirmedTCPConnections Capability `json:"confirmed_tcp_connections"`
}

type AnalyticsCoverage struct {
	From                 time.Time `json:"from"`
	To                   time.Time `json:"to"`
	BucketSeconds        uint32    `json:"bucket_seconds"`
	LegacyDimensions     bool      `json:"legacy_dimensions"`
	HasUnobservedBuckets bool      `json:"has_unobserved_buckets"`
}

type AnalyticsDashboard struct {
	ActiveConnections        *uint64                                 `json:"active_connections"`
	DevicesWithTraffic       uint64                                  `json:"devices_with_traffic"`
	DeviceConnectionRankings []AnalyticsMetric                       `json:"device_connection_rankings"`
	Summary                  AnalyticsMetric                         `json:"summary"`
	Timeseries               []AnalyticsTimeseriesPoint              `json:"timeseries"`
	DeviceRankings           []AnalyticsMetric                       `json:"device_rankings"`
	DimensionRankings        map[string]map[string][]AnalyticsMetric `json:"dimension_rankings"`
	RelationshipRankings     map[string][]AnalyticsRelationship      `json:"relationship_rankings"`
	Capabilities             AnalyticsCapabilities                   `json:"capabilities"`
	Coverage                 AnalyticsCoverage                       `json:"coverage"`
}
