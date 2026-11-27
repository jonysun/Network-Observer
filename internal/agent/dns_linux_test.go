//go:build linux

package agent

import (
	"encoding/binary"
	"github.com/google/gopacket/tcpassembly"
	pb "github.com/network-observer/network-observer/gen/networkobserver/v1"
	"net"
	"strings"
	"testing"
	"time"
)

func TestDNSNameRejectsMalformedPointers(t *testing.T) {
	if _, _, ok := dnsName([]byte{0xc0}, 0); ok {
		t.Fatal("expected truncated DNS pointer to be rejected")
	}
	if _, _, ok := dnsName([]byte{64, 'a'}, 0); ok {
		t.Fatal("expected oversized DNS label to be rejected")
	}
}

func TestDNSNameParsesLabelsAndReturnsRecordOffset(t *testing.T) {
	name, offset, ok := dnsName([]byte{3, 'w', 'w', 'w', 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 0, 1, 2}, 0)
	if !ok || name != "www.example" || offset != 13 {
		t.Fatalf("got name=%q offset=%d ok=%v", name, offset, ok)
	}
}

func TestParseDNSPacketEmitsOnlyAnswerMetadata(t *testing.T) {
	name := []byte{3, 'w', 'w', 'w', 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0}
	dns := make([]byte, 12)
	binary.BigEndian.PutUint16(dns[2:4], 0x8180)
	binary.BigEndian.PutUint16(dns[4:6], 1)
	binary.BigEndian.PutUint16(dns[6:8], 1)
	dns = append(dns, name...)
	dns = append(dns, 0, 1, 0, 1, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 1, 2, 3, 4)
	packet := make([]byte, 14+20+8+len(dns))
	binary.BigEndian.PutUint16(packet[12:14], 0x0800)
	ip := packet[14:34]
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:4], uint16(20+8+len(dns)))
	ip[9] = 17
	copy(ip[12:16], net.IPv4(8, 8, 8, 8).To4())
	copy(ip[16:20], net.IPv4(192, 168, 1, 5).To4())
	udp := packet[34:42]
	binary.BigEndian.PutUint16(udp[0:2], 53)
	binary.BigEndian.PutUint16(udp[2:4], 53000)
	binary.BigEndian.PutUint16(udp[4:6], uint16(8+len(dns)))
	copy(packet[42:], dns)
	parsed := parseDNSPacket(packet, time.Unix(1, 0))
	if !parsed.valid || len(parsed.observations) != 1 {
		t.Fatal("expected a visible UDP DNS response")
	}
	observation := parsed.observations[0]
	if observation.Domain != "www.example.com" || observation.DnsIp != "1.2.3.4" || observation.DnsTtl != 60 || !observation.DnsMetadataOnly {
		t.Fatalf("unexpected DNS metadata: %+v", observation)
	}
	if observation.Id == "" || observation.ObservedAtUnixNanos != time.Unix(1, 0).UnixNano() || observation.LocalIsSource {
		t.Fatalf("DNS capture identity/time/direction not preserved: %+v", observation)
	}
}

func TestDNSCapabilityShowsCaptureEvidence(t *testing.T) {
	c := &dnsCollector{fd: -1, available: true}
	status, detail := c.Status()
	if status != "supported" || !strings.Contains(detail, "frames=0") || !strings.Contains(detail, "udp53_packets=0") || !strings.Contains(detail, "last_dns_response=none") || !strings.Contains(detail, "tcp53_packets=0") {
		t.Fatalf("empty capture must be distinguishable from observed DNS: status=%q detail=%q", status, detail)
	}
	c.frames, c.dnsPackets, c.responses, c.records = 20, 2, 1, 1
	c.lastResponse = time.Unix(10, 0).UTC()
	status, detail = c.Status()
	if status != "supported" || !strings.Contains(detail, "udp53_packets=2") || !strings.Contains(detail, "metadata_records=1") || !strings.Contains(detail, "1970-01-01T00:00:10Z") {
		t.Fatalf("capture evidence missing from capability: status=%q detail=%q", status, detail)
	}
}

func TestDNSPacketClassifierRecognizesDNSPortsAndVLAN(t *testing.T) {
	packet := make([]byte, 14+4+20+8)
	binary.BigEndian.PutUint16(packet[12:14], 0x8100)
	binary.BigEndian.PutUint16(packet[16:18], 0x0800)
	packet[18] = 0x45
	packet[27] = 17
	binary.BigEndian.PutUint16(packet[14+4+20:14+4+22], 53)
	if !isDNSUDP53Packet(packet) {
		t.Fatal("expected VLAN-tagged UDP/53 frame to be identified")
	}
	binary.BigEndian.PutUint16(packet[14+4+20:14+4+22], 53000)
	binary.BigEndian.PutUint16(packet[14+4+22:14+4+24], 443)
	if isDNSUDP53Packet(packet) {
		t.Fatal("non-DNS UDP frame classified as DNS")
	}
}

func TestDNSObservationIDIsStableForCapturedRecord(t *testing.T) {
	packet := []byte{1, 2, 3, 4}
	observedAt := int64(1234567890123)
	first := dnsObservationID(packet, observedAt)
	if first == "" || first != dnsObservationID(packet, observedAt) {
		t.Fatalf("same captured record must keep a stable ID: %q", first)
	}
	if first != dnsObservationID(packet, observedAt+1) {
		t.Fatal("same captured bytes must remain idempotent across retries")
	}
}

func TestParseDNSPacketFollowsCNAMEAndReturnsAllAddresses(t *testing.T) {
	name := []byte{3, 'w', 'w', 'w', 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0}
	alias := []byte{5, 'a', 'l', 'i', 'a', 's', 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0}
	dns := make([]byte, 12)
	binary.BigEndian.PutUint16(dns[2:4], 0x8180)
	binary.BigEndian.PutUint16(dns[4:6], 1)
	binary.BigEndian.PutUint16(dns[6:8], 3)
	dns = append(dns, name...)
	dns = append(dns, 0, 1, 0, 1)
	dns = append(dns, 0xc0, 0x0c, 0, 5, 0, 1, 0, 0, 0, 30, 0, byte(len(alias)))
	aliasOffset := len(dns)
	dns = append(dns, alias...)
	dns = append(dns, 0xc0, byte(aliasOffset), 0, 1, 0, 1, 0, 0, 0, 90, 0, 4, 1, 2, 3, 4)
	dns = append(dns, 0xc0, byte(aliasOffset), 0, 28, 0, 1, 0, 0, 0, 90, 0, 16)
	dns = append(dns, net.ParseIP("2001:db8::1").To16()...)
	packet := make([]byte, 14+20+8+len(dns))
	binary.BigEndian.PutUint16(packet[12:14], 0x0800)
	packet[14] = 0x45
	binary.BigEndian.PutUint16(packet[16:18], uint16(20+8+len(dns)))
	packet[23] = 17
	copy(packet[26:30], net.IPv4(8, 8, 8, 8).To4())
	copy(packet[30:34], net.IPv4(192, 168, 1, 5).To4())
	binary.BigEndian.PutUint16(packet[34:36], 53)
	binary.BigEndian.PutUint16(packet[36:38], 53000)
	binary.BigEndian.PutUint16(packet[38:40], uint16(8+len(dns)))
	copy(packet[42:], dns)
	parsed := parseDNSPacket(packet, time.Unix(2, 0))
	if !parsed.valid || len(parsed.observations) != 2 {
		t.Fatalf("expected both A and AAAA through CNAME, got valid=%v observations=%+v", parsed.valid, parsed.observations)
	}
	for _, item := range parsed.observations {
		if item.Domain != "www.example.com" || item.DnsTtl != 30 {
			t.Fatalf("CNAME mapping lost original name/chain TTL: %+v", item)
		}
	}
	if parsed.observations[0].Id == parsed.observations[1].Id {
		t.Fatal("different DNS answers must have different event IDs")
	}
}

func TestDNSTCPStreamReassemblesLengthPrefixedResponseAcrossSegments(t *testing.T) {
	name := []byte{3, 'w', 'w', 'w', 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0}
	dns := make([]byte, 12)
	binary.BigEndian.PutUint16(dns[2:4], 0x8180)
	binary.BigEndian.PutUint16(dns[4:6], 1)
	binary.BigEndian.PutUint16(dns[6:8], 1)
	dns = append(dns, name...)
	dns = append(dns, 0, 1, 0, 1, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 1, 2, 3, 4)
	frame := make([]byte, len(dns)+2)
	binary.BigEndian.PutUint16(frame[:2], uint16(len(dns)))
	copy(frame[2:], dns)
	c := &dnsCollector{items: []*pb.FlowObservation{}}
	f := &dnsTCPFactory{collector: c}
	stream := &dnsTCPStream{factory: f, srcIP: "8.8.8.8", dstIP: "192.168.1.5", srcPort: 53, dstPort: 53000, response: true}
	stream.Reassembled([]tcpassembly.Reassembly{{Bytes: frame[:11]}})
	if len(c.items) != 0 {
		t.Fatal("incomplete TCP DNS message must not be parsed")
	}
	stream.Reassembled([]tcpassembly.Reassembly{{Bytes: frame[11:]}})
	if len(c.items) != 1 || c.items[0].Domain != "www.example.com" || c.items[0].DnsIp != "1.2.3.4" || c.items[0].Protocol != "tcp" || c.items[0].CounterSource != "dns_tcp53" {
		t.Fatalf("unexpected reassembled DNS observation: %+v", c.items)
	}
}

func TestDNSTCPStreamDropsMessagesAfterSequenceGap(t *testing.T) {
	c := &dnsCollector{}
	stream := &dnsTCPStream{factory: &dnsTCPFactory{collector: c}, response: true}
	stream.Reassembled([]tcpassembly.Reassembly{{Bytes: []byte{0, 20, 1, 2}}, {Skip: 3, Bytes: []byte{3, 4}}})
	if len(stream.buffer) != 0 || !stream.dropped || c.tcpDropped != 1 {
		t.Fatalf("gap should discard stream safely: buffer=%d dropped=%v count=%d", len(stream.buffer), stream.dropped, c.tcpDropped)
	}
}
