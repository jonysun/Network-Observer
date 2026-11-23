package store

import (
	"strings"
	"testing"
	"time"
)

func TestAnalyticsRankingsSQLDoesMetricTopNInsideClickHouse(t *testing.T) {
	query := analyticsRankingsSQL("WITH source AS (SELECT 1)", []string{"remote_ip", "process", "device"})
	for _, fragment := range []string{
		"dimension_tuple.1 AS dimension_name",
		"dimension_tuple.2 AS value",
		"GROUP BY dimension_name, value",
		"uniqExactIf(tuple(device_id, flow_id), flow_id != '') AS connections",
		"tuple('connections', connections)",
		"tuple('upload', upload_sum)",
		"tuple('download', download_sum)",
		"tuple('total', total_bytes)",
		"sum(upload_bytes) AS upload_sum",
		"sum(download_bytes) AS download_sum",
		"upload_sum AS upload_bytes",
		"download_sum AS download_bytes",
		"LIMIT ? BY dimension_name, metric",
		"ORDER BY dimension_name, metric, ranking.2 DESC, value ASC",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("analytics ranking SQL is missing %q:\n%s", fragment, query)
		}
	}
	if strings.Contains(query, "analyticsRowLimit") || strings.Contains(query, "200000") {
		t.Fatalf("aggregate query must not use the Center-side result cap:\n%s", query)
	}
}

func TestAnalyticsRankingsSQLOnlyInterpolatesAllowlistedDimensions(t *testing.T) {
	query := analyticsRankingsSQL("WITH source AS (SELECT 1)", []string{"remote_ip", "remote_ip; DROP TABLE observer.traffic_minute"})
	if !strings.Contains(query, "tuple('remote_ip', if(remote_ip = '', '未知', remote_ip))") {
		t.Fatalf("expected allowlisted remote IP expression in SQL:\n%s", query)
	}
	if strings.Contains(query, "DROP TABLE") {
		t.Fatalf("untrusted dimension was interpolated into SQL:\n%s", query)
	}
}

func TestAnalyticsRelationshipsSQLBoundsEveryRelationshipGroup(t *testing.T) {
	query := analyticsRelationshipsSQL("WITH source AS (SELECT 1)")
	for _, relation := range []string{"process_to_remote_ip", "process_to_domain", "remote_ip_to_remote_port", "device_to_remote_ip", "protocol_to_remote_ip"} {
		if !strings.Contains(query, "'"+relation+"'") {
			t.Errorf("relationship %q is missing from SQL", relation)
		}
	}
	if !strings.Contains(query, "LIMIT ? BY relationship SETTINGS max_execution_time=10") {
		t.Fatalf("relationship groups must be bounded in ClickHouse:\n%s", query)
	}
	for _, fragment := range []string{"sum(upload_bytes) AS upload_sum", "sum(download_bytes) AS download_sum", "upload_sum AS upload_bytes", "download_sum AS download_bytes"} {
		if !strings.Contains(query, fragment) {
			t.Errorf("relationship SQL is missing %q:\n%s", fragment, query)
		}
	}
	if strings.Contains(query, "sum(upload_bytes) AS upload_bytes") || strings.Contains(query, "sum(download_bytes) AS download_bytes") {
		t.Fatalf("aggregate aliases must not shadow source columns:\n%s", query)
	}
	if !strings.Contains(query, "uniqExactIf(tuple(device_id, flow_id), flow_id != '')") {
		t.Fatal("relationship connection counts must use distinct observed flows")
	}
}

func TestAnalyticsSourceUsesNarrowDeviceTrafficAndPresenceInputs(t *testing.T) {
	filter := AnalyticsFilter{
		From:  time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		To:    time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
		Limit: 10,
		Trust: "all",
	}
	query, args := analyticsSource(filter, nil, nil)
	for _, fragment := range []string{
		"FROM observer.traffic_minute FINAL",
		"dimension_type = 'device'",
		"if(direction = 'upload', bytes, toUInt64(0)) AS upload_bytes",
		"FROM observer.connection_presence_minute FINAL",
		"ranking_dimensions_available AS dimensions_available",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("source CTE is missing %q:\n%s", fragment, query)
		}
	}
	if strings.Contains(query, "GROUP BY bucket, device_id, remote_ip") {
		t.Fatalf("source CTE must not group across unrelated dimensions:\n%s", query)
	}
	if len(args) != 4 {
		t.Fatalf("expected two time bounds per source branch, got %d args: %#v", len(args), args)
	}
}
