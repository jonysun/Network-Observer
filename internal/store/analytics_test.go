package store

import (
	"strings"
	"testing"
	"time"
)

func TestAddDistinctAnalyticsFlow(t *testing.T) {
	flows := make(map[string]struct{})
	if !addDistinctAnalyticsFlow(flows, "device-a", "flow-1") {
		t.Fatal("first observation should add the flow")
	}
	if addDistinctAnalyticsFlow(flows, "device-a", "flow-1") {
		t.Fatal("repeated minute observation should not add the same flow twice")
	}
	if !addDistinctAnalyticsFlow(flows, "device-b", "flow-1") {
		t.Fatal("flow IDs from different devices must remain distinct")
	}
	if addDistinctAnalyticsFlow(flows, "device-a", "") {
		t.Fatal("empty flow IDs must not be counted")
	}
}

func TestAnalyticsPresenceRemotePortUsesStoredColumns(t *testing.T) {
	expression := analyticsPresenceRemotePortExpression()
	for _, column := range []string{"remote_ip", "destination_ip", "local_port", "source_port", "destination_port"} {
		if !strings.Contains(expression, column) {
			t.Errorf("remote-port expression does not reference %q: %s", column, expression)
		}
	}
	if strings.Contains(expression, "remote_port") {
		t.Fatalf("presence table has no remote_port column: %s", expression)
	}
}

func TestAnalyticsSeriesBucketSize(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		span time.Duration
		want time.Duration
	}{
		{name: "24h", span: 24 * time.Hour, want: time.Hour},
		{name: "7d", span: 7 * 24 * time.Hour, want: 6 * time.Hour},
		{name: "30d", span: 30 * 24 * time.Hour, want: 24 * time.Hour},
		{name: "90d", span: 90 * 24 * time.Hour, want: 24 * time.Hour},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := analyticsSeriesBucketSize(start, start.Add(test.span)); got != test.want {
				t.Fatalf("analyticsSeriesBucketSize() = %s, want %s", got, test.want)
			}
		})
	}
}

func TestAnalyticsTrustedIPConditionUsesScalarParameters(t *testing.T) {
	condition, args := analyticsTrustedIPCondition("remote_ip", []string{"192.0.2.1", "2001:db8::1"}, false)
	if condition != "remote_ip IN (?, ?)" {
		t.Fatalf("unexpected trusted condition: %s", condition)
	}
	if len(args) != 2 || args[0] != "192.0.2.1" || args[1] != "2001:db8::1" {
		t.Fatalf("unexpected trusted args: %#v", args)
	}
	condition, args = analyticsTrustedIPCondition("remote_ip", []string{"192.0.2.1"}, true)
	if condition != "remote_ip NOT IN (?)" || len(args) != 1 || args[0] != "192.0.2.1" {
		t.Fatalf("unexpected untrusted condition: %s %#v", condition, args)
	}
}

func TestBuildAnalyticsDashboardSortsEachMetricIndependently(t *testing.T) {
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	traffic := []analyticsTrafficRow{
		{bucket: from, deviceID: "device-a", remoteIP: "198.51.100.10", process: "chatty", upload: 1, download: 1000},
		{bucket: from, deviceID: "device-b", remoteIP: "198.51.100.20", process: "uploader", upload: 500, download: 1},
	}
	dashboard := buildAnalyticsDashboard(AnalyticsFilter{From: from, To: from.Add(24 * time.Hour), Limit: 2}, []string{"remote_ip", "process"}, traffic, nil, nil)
	remote := dashboard.DimensionRankings["remote_ip"]
	if len(remote["upload"]) != 2 || remote["upload"][0].Value != "198.51.100.20" {
		t.Fatalf("upload ranking was not independent: %#v", remote["upload"])
	}
	if len(remote["download"]) != 2 || remote["download"][0].Value != "198.51.100.10" {
		t.Fatalf("download ranking was not independent: %#v", remote["download"])
	}
	if len(remote["total"]) != 2 || remote["total"][0].Value != "198.51.100.10" {
		t.Fatalf("total ranking was not independent: %#v", remote["total"])
	}
}

func TestBuildAnalyticsDashboardKeepsUnsupportedTCPAsNull(t *testing.T) {
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	dashboard := buildAnalyticsDashboard(AnalyticsFilter{From: from, To: from.Add(24 * time.Hour), Limit: 1}, []string{"remote_ip"}, nil, nil, nil)
	if dashboard.Summary.ConfirmedTCPOpens != nil {
		t.Fatal("unsupported TCP-open capability must remain null")
	}
	if dashboard.Capabilities.ConfirmedTCPConnections.Status != "unavailable" {
		t.Fatalf("unexpected TCP-open capability: %#v", dashboard.Capabilities.ConfirmedTCPConnections)
	}
}
