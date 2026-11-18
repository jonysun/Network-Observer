//go:build linux

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConntrackPacketDeltasRemainZeroWhenIdle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nf_conntrack")
	collector := newGatewayCollectorWithConfig(Config{ConntrackPath: path}).(*gatewayCollector)
	t.Cleanup(func() { _ = collector.Close() })
	for round, packets := range []uint64{12, 12, 12, 15, 15, 15} {
		line := fmt.Sprintf("ipv4 2 tcp 6 100 ESTABLISHED src=10.0.0.10 dst=203.0.113.20 sport=4242 dport=443 packets=%d bytes=1200 src=203.0.113.20 dst=10.0.0.10 sport=443 dport=4242 packets=8 bytes=800", packets)
		if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
		items, _, err := collector.Snapshot(context.Background())
		if err != nil || len(items) != 1 {
			t.Fatalf("snapshot count=%d err=%v", len(items), err)
		}
		want := uint64(0)
		if round == 3 {
			want = 3
		}
		if got := items[0]; got.PacketsSourceToDestination != want || got.PacketsDestinationToSource != 0 {
			t.Fatalf("round %d packets=%d/%d, want %d/0", round, got.PacketsSourceToDestination, got.PacketsDestinationToSource, want)
		}
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	items, _, err := collector.Snapshot(context.Background())
	if err != nil || len(items) != 1 || items[0].Active || items[0].PacketsSourceToDestination != 0 || len(collector.previousPackets) != 0 {
		t.Fatal("closed flow retained packet deltas or state")
	}
}

func TestParseConntrackLine(t *testing.T) {
	line := "ipv4 2 tcp 6 431999 ESTABLISHED src=192.0.2.10 dst=198.51.100.20 sport=4242 dport=443 packets=12 bytes=1200 src=198.51.100.20 dst=192.0.2.10 sport=443 dport=4242 packets=8 bytes=800 zone=3"
	flow, err := parseConntrackLine(line)
	if err != nil {
		t.Fatal(err)
	}
	if flow.protocol != "tcp" || flow.state != "ESTABLISHED" || flow.tuple.source.String() != "192.0.2.10" || flow.tuple.destination.String() != "198.51.100.20" {
		t.Fatalf("unexpected flow: %+v", flow)
	}
	if flow.tuple.sourcePort != 4242 || flow.tuple.destPort != 443 || flow.tuple.bytes != 1200 || flow.reply.bytes != 800 || flow.zone != "3" {
		t.Fatalf("unexpected counters: %+v", flow)
	}
}

func TestParseConntrackRejectsUnsupportedLine(t *testing.T) {
	if _, err := parseConntrackLine("ipv4 2 icmp 1 30 src=192.0.2.1 dst=198.51.100.1"); err == nil {
		t.Fatal("expected unsupported protocol error")
	}
	if _, err := parseConntrackLine(strings.TrimSpace("ipv4 2 tcp")); err == nil {
		t.Fatal("expected malformed line error")
	}
}

func TestConntrackReplyTupleExposesNATTranslation(t *testing.T) {
	flow, err := parseConntrackLine("ipv4 2 tcp 6 431999 ESTABLISHED src=10.0.0.10 dst=203.0.113.20 sport=4242 dport=443 packets=12 bytes=1200 src=192.0.2.5 dst=198.51.100.20 sport=443 dport=50000 packets=8 bytes=800")
	if err != nil {
		t.Fatal(err)
	}
	if natSourceIP(flow) != "198.51.100.20" || natSourcePort(flow) != 50000 || natDestinationIP(flow) != "192.0.2.5" || natDestinationPort(flow) != 0 {
		t.Fatalf("unexpected NAT mapping: source=%s:%d destination=%s:%d", natSourceIP(flow), natSourcePort(flow), natDestinationIP(flow), natDestinationPort(flow))
	}
}

func TestGatewaySnapshotReportsConntrackNATFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nf_conntrack")
	line := "ipv4 2 tcp 6 431999 ESTABLISHED src=10.0.0.10 dst=203.0.113.20 sport=4242 dport=443 packets=12 bytes=1200 src=192.0.2.5 dst=198.51.100.20 sport=443 dport=50000 packets=8 bytes=800"
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	collector := newGatewayCollectorWithConfig(Config{ConntrackPath: path}).(*gatewayCollector)
	t.Cleanup(func() { _ = collector.Close() })
	observations, capabilities, err := collector.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 1 {
		t.Fatalf("expected one flow observation, got %d", len(observations))
	}
	flow := observations[0]
	if flow.NatSourceIp != "198.51.100.20" || flow.NatSourcePort != 50000 || flow.NatDestinationIp != "192.0.2.5" {
		t.Fatalf("unexpected NAT fields: %+v", flow)
	}
	found := false
	for _, item := range capabilities {
		if item.Name == "gateway_nat_tuple" {
			found = item.Status == "supported" && strings.Contains(item.Detail, "1 active flow")
		}
	}
	if !found {
		t.Fatalf("expected supported NAT capability, got %+v", capabilities)
	}
}
