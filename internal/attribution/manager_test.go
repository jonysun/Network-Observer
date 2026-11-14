package attribution

import (
	"testing"
	"time"

	"github.com/network-observer/network-observer/internal/domain"
)

func TestApplyUsesOverrideBeforeRule(t *testing.T) {
	manager := New([]Rule{{
		ID: "rule-1", Priority: 1, Enabled: true,
		Matcher:     Matcher{Protocol: "tcp", DestinationPort: 443},
		Attribution: Value{ServiceName: "rule-service"}, EffectiveFrom: time.Time{},
	}}, map[string]Value{
		ConnectionKey("device-1", "flow-1"): {ServiceName: "manual-service"},
	})
	observation := domain.FlowObservation{
		DeviceID: "device-1", FlowID: "flow-1", Protocol: domain.ProtocolTCP,
		DestinationPort: 443, ObservedAt: time.Now().UTC(), AttributionSource: domain.AttributionUnknown,
	}

	manager.Apply(&observation)

	if observation.ServiceName != "manual-service" || observation.AttributionSource != domain.AttributionManual {
		t.Fatalf("override must win over rules: %+v", observation)
	}
}

func TestApplyRulesOnlyAfterEffectiveTimeAndKeepAutomaticSource(t *testing.T) {
	effectiveFrom := time.Now().UTC()
	manager := New([]Rule{{
		ID: "rule-1", Priority: 1, Enabled: true,
		Matcher:     Matcher{Protocol: "udp", DestinationPort: 53},
		Attribution: Value{ServiceName: "dns"}, EffectiveFrom: effectiveFrom,
	}}, nil)

	before := domain.FlowObservation{
		DeviceID: "device-1", FlowID: "before", Protocol: domain.ProtocolUDP,
		DestinationPort: 53, ObservedAt: effectiveFrom.Add(-time.Second), AttributionSource: domain.AttributionUnknown,
	}
	manager.Apply(&before)
	if before.ServiceName != "" || before.AttributionSource != domain.AttributionUnknown {
		t.Fatalf("rule must not affect observations before activation: %+v", before)
	}

	after := domain.FlowObservation{
		DeviceID: "device-1", FlowID: "after", Protocol: domain.ProtocolUDP,
		DestinationPort: 53, ObservedAt: effectiveFrom, AttributionSource: domain.AttributionUnknown,
	}
	manager.Apply(&after)
	if after.ServiceName != "dns" || after.AttributionSource != domain.AttributionAuto {
		t.Fatalf("rule should provide automatic attribution after activation: %+v", after)
	}
}

func TestApplyPreservesKnownCollectorAttribution(t *testing.T) {
	manager := New([]Rule{{
		ID: "rule-1", Priority: 1, Enabled: true,
		Matcher:     Matcher{Protocol: "tcp"},
		Attribution: Value{ServiceName: "rule-service"},
	}}, nil)
	observation := domain.FlowObservation{
		Protocol: domain.ProtocolTCP, ObservedAt: time.Now().UTC(),
		ServiceName: "collector-service", AttributionSource: domain.AttributionAuto,
	}

	manager.Apply(&observation)

	if observation.ServiceName != "collector-service" {
		t.Fatalf("manager must preserve an existing resolved attribution, got %q", observation.ServiceName)
	}
}

func TestConnectionKeyAllowsColonInFlowID(t *testing.T) {
	deviceID, flowID, ok := ParseConnectionKey("device-1:flow:with:colons")
	if !ok || deviceID != "device-1" || flowID != "flow:with:colons" {
		t.Fatalf("unexpected connection key parsing: %q %q %t", deviceID, flowID, ok)
	}
}
