//go:build linux

package agent

import (
	"context"
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"

	networkobserverv1 "github.com/network-observer/network-observer/gen/networkobserver/v1"
)

func TestParseNetFlowV5(t *testing.T) {
	packet := make([]byte, 24+48)
	binary.BigEndian.PutUint16(packet[0:2], 5)
	binary.BigEndian.PutUint16(packet[2:4], 1)
	copy(packet[24:28], net.IPv4(192, 0, 2, 10).To4())
	copy(packet[28:32], net.IPv4(198, 51, 100, 20).To4())
	binary.BigEndian.PutUint32(packet[40:44], 12)
	binary.BigEndian.PutUint32(packet[44:48], 1200)
	binary.BigEndian.PutUint16(packet[56:58], 4242)
	binary.BigEndian.PutUint16(packet[58:60], 443)
	packet[61] = 6
	binary.BigEndian.PutUint16(packet[22:24], 10)
	flows, err := parseNetFlowV5(packet, net.IPv4(192, 0, 2, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(flows) != 1 || flows[0].protocol != "tcp" || flows[0].bytes != 1200 || flows[0].packets != 12 || flows[0].samplingRate != 10 || flows[0].confidence != "estimated" {
		t.Fatalf("unexpected NetFlow result: %+v", flows)
	}
}

func TestParseIPFIXTemplateAndData(t *testing.T) {
	templateBody := make([]byte, 4+7*4)
	binary.BigEndian.PutUint16(templateBody[0:2], 256)
	binary.BigEndian.PutUint16(templateBody[2:4], 7)
	fields := []struct {
		id uint16
		ln uint16
	}{
		{4, 1}, {7, 2}, {11, 2}, {1, 8}, {2, 8}, {8, 4}, {12, 4},
	}
	for index, field := range fields {
		binary.BigEndian.PutUint16(templateBody[4+index*4:6+index*4], field.id)
		binary.BigEndian.PutUint16(templateBody[6+index*4:8+index*4], field.ln)
	}
	templateSet := make([]byte, 4+len(templateBody))
	binary.BigEndian.PutUint16(templateSet[0:2], 2)
	binary.BigEndian.PutUint16(templateSet[2:4], uint16(len(templateSet)))
	copy(templateSet[4:], templateBody)

	dataBody := make([]byte, 29)
	dataBody[0] = 6
	binary.BigEndian.PutUint16(dataBody[1:3], 4242)
	binary.BigEndian.PutUint16(dataBody[3:5], 443)
	binary.BigEndian.PutUint64(dataBody[5:13], 1200)
	binary.BigEndian.PutUint64(dataBody[13:21], 12)
	copy(dataBody[21:25], net.IPv4(192, 0, 2, 10).To4())
	copy(dataBody[25:29], net.IPv4(198, 51, 100, 20).To4())
	dataSet := make([]byte, 4+len(dataBody))
	binary.BigEndian.PutUint16(dataSet[0:2], 256)
	binary.BigEndian.PutUint16(dataSet[2:4], uint16(len(dataSet)))
	copy(dataSet[4:], dataBody)

	packet := make([]byte, 16+len(templateSet)+len(dataSet))
	binary.BigEndian.PutUint16(packet[0:2], 10)
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	binary.BigEndian.PutUint32(packet[12:16], 7)
	copy(packet[16:], templateSet)
	copy(packet[16+len(templateSet):], dataSet)
	templates := make(map[ipfixTemplateKey]ipfixTemplate)
	flows, err := parseIPFIX(packet, net.IPv4(192, 0, 2, 1), templates)
	if err != nil {
		t.Fatal(err)
	}
	if len(flows) != 1 || flows[0].sourcePort != 4242 || flows[0].destinationPort != 443 || flows[0].bytes != 1200 || flows[0].sourceIP.String() != "192.0.2.10" {
		t.Fatalf("unexpected IPFIX result: %+v", flows)
	}
}

func TestParseSFlowRawIPv4TCP(t *testing.T) {
	ip := make([]byte, 20)
	ip[0] = 0x45
	ip[9] = 6
	copy(ip[12:16], net.IPv4(192, 0, 2, 10).To4())
	copy(ip[16:20], net.IPv4(198, 51, 100, 20).To4())
	tcp := []byte{0x10, 0x92, 0x01, 0xbb}
	ethernet := append([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 0x08, 0x00}, append(ip, tcp...)...)
	record := make([]byte, 16+len(ethernet))
	binary.BigEndian.PutUint32(record[0:4], 1)
	binary.BigEndian.PutUint32(record[4:8], uint32(len(ethernet)))
	binary.BigEndian.PutUint32(record[12:16], uint32(len(ethernet)))
	copy(record[16:], ethernet)
	sample := make([]byte, 32+8+len(record))
	binary.BigEndian.PutUint32(sample[0:4], 1)
	binary.BigEndian.PutUint32(sample[8:12], 100)
	binary.BigEndian.PutUint32(sample[28:32], 1)
	binary.BigEndian.PutUint32(sample[32:36], 1)
	binary.BigEndian.PutUint32(sample[36:40], uint32(len(record)))
	copy(sample[40:], record)

	packet := make([]byte, 28+8+len(sample))
	binary.BigEndian.PutUint32(packet[0:4], 5)
	binary.BigEndian.PutUint32(packet[4:8], 1)
	copy(packet[8:12], net.IPv4(192, 0, 2, 1).To4())
	binary.BigEndian.PutUint32(packet[24:28], 1)
	binary.BigEndian.PutUint32(packet[28:32], 1)
	binary.BigEndian.PutUint32(packet[32:36], uint32(len(sample)))
	copy(packet[36:], sample)
	flows, err := parseSFlowV5(packet, net.IPv4(192, 0, 2, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(flows) != 1 || flows[0].protocol != "tcp" || flows[0].sourcePort != 4242 || flows[0].samplingRate != 100 || flows[0].confidence != "estimated" {
		t.Fatalf("unexpected sFlow result: %+v", flows)
	}
}

func TestParseIPFIXDataWithTemplate(t *testing.T) {
	packet := make([]byte, 16+4+4+1+2+2+8+4+4)
	binary.BigEndian.PutUint16(packet[0:2], 10)
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	binary.BigEndian.PutUint32(packet[12:16], 3)
	binary.BigEndian.PutUint16(packet[16:18], 256)
	binary.BigEndian.PutUint16(packet[18:20], uint16(len(packet)-16))
	packet[20] = 6
	binary.BigEndian.PutUint16(packet[21:23], 4242)
	binary.BigEndian.PutUint16(packet[23:25], 443)
	binary.BigEndian.PutUint64(packet[25:33], 100)
	binary.BigEndian.PutUint32(packet[33:37], 2)
	copy(packet[37:41], net.IPv4(192, 0, 2, 10).To4())
	copy(packet[41:45], net.IPv4(198, 51, 100, 20).To4())
	templates := map[ipfixTemplateKey]ipfixTemplate{{exporter: "192.0.2.1", domain: 3, id: 256}: {fields: []ipfixField{
		{id: 4, length: 1}, {id: 7, length: 2}, {id: 11, length: 2}, {id: 1, length: 8}, {id: 2, length: 4}, {id: 8, length: 4}, {id: 12, length: 4},
	}}}
	flows, err := parseIPFIX(packet, net.IPv4(192, 0, 2, 1), templates)
	if err != nil {
		t.Fatal(err)
	}
	if len(flows) != 1 || flows[0].sourcePort != 4242 || flows[0].sourceIP.String() != "192.0.2.10" {
		t.Fatalf("unexpected parsed flow: %+v", flows)
	}
}

func TestGatewayReceiverAndFlowsRemainAvailableWithoutConntrack(t *testing.T) {
	receiver, status := startGatewayReceiver("netflow", "127.0.0.1:0")
	if receiver == nil || status.status != "supported" {
		t.Fatalf("start receiver: receiver=%v status=%+v", receiver, status)
	}
	defer receiver.close()
	collector := &gatewayCollector{
		config:           Config{ConntrackPath: t.TempDir() + "/missing"},
		receivers:        []*gatewayFlowReceiver{receiver},
		receiverStatuses: map[string]gatewayReceiverStatus{"netflow": status, "ipfix": {status: "disabled"}, "sflow": {status: "disabled"}},
		externalState:    make(map[string]externalCounterState),
		previous:         make(map[string]*networkobserverv1.FlowObservation),
	}
	conn, err := net.DialUDP("udp", nil, receiver.conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write(makeNetFlowV5Fixture()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		observations, capabilities, err := collector.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, capability := range capabilities {
			if len(observations) != 0 && capability.Name == "netflow" && capability.Status == "supported" && !strings.Contains(capability.Detail, "received 1 datagram") {
				t.Fatalf("expected received datagram status, got %+v", capability)
			}
		}
		if len(observations) != 0 {
			if len(observations) != 1 || observations[0].CounterSource != "netflow_v5" || observations[0].SourceIp != "192.0.2.10" {
				t.Fatalf("unexpected observations with conntrack unavailable: %+v", observations)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("external NetFlow observation was not emitted while conntrack was unavailable")
}

func TestGatewayExternalFlowExpiresAsClosedObservation(t *testing.T) {
	now := time.Now().UTC()
	last := &networkobserverv1.FlowObservation{
		Id: "flow:1", FlowId: "flow", State: "ACTIVE", Active: true,
		TotalBytesSourceToDestination: 321, BytesSourceToDestination: 12,
	}
	collector := &gatewayCollector{externalState: map[string]externalCounterState{
		"flow": {bytes: 321, lastSeen: now.Add(-externalFlowIdleTimeout - time.Second), observation: last},
	}}
	observations := collector.externalObservations(now)
	if len(observations) != 1 {
		t.Fatalf("expected one expiry observation, got %d", len(observations))
	}
	closed := observations[0]
	if closed.Active || closed.State != "CLOSED" || closed.TotalBytesSourceToDestination != 321 || closed.BytesSourceToDestination != 0 {
		t.Fatalf("unexpected closed observation: %+v", closed)
	}
	if _, exists := collector.externalState["flow"]; exists {
		t.Fatal("expired flow remains in external state")
	}
}

func TestGatewayReceiverCloseIsIdempotentAndReportsErrors(t *testing.T) {
	receiver, _ := startGatewayReceiver("netflow", "127.0.0.1:0")
	if receiver == nil {
		t.Fatal("expected receiver")
	}
	receiver.parseErrs.Add(2)
	if status := receiver.status(); status.status != "degraded" || !strings.Contains(status.detail, "parse errors 2") {
		t.Fatalf("unexpected receiver status: %+v", status)
	}
	receiver.close()
	receiver.close()
}

func makeNetFlowV5Fixture() []byte {
	packet := make([]byte, 24+48)
	binary.BigEndian.PutUint16(packet[0:2], 5)
	binary.BigEndian.PutUint16(packet[2:4], 1)
	copy(packet[24:28], net.IPv4(192, 0, 2, 10).To4())
	copy(packet[28:32], net.IPv4(198, 51, 100, 20).To4())
	binary.BigEndian.PutUint32(packet[40:44], 12)
	binary.BigEndian.PutUint32(packet[44:48], 1200)
	binary.BigEndian.PutUint16(packet[56:58], 4242)
	binary.BigEndian.PutUint16(packet[58:60], 443)
	packet[61] = 6
	return packet
}
