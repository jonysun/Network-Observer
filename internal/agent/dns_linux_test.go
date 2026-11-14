//go:build linux

package agent

import (
	"encoding/binary"
	"net"
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
	ip[9] = 17
	copy(ip[12:16], net.IPv4(8, 8, 8, 8).To4())
	copy(ip[16:20], net.IPv4(192, 168, 1, 5).To4())
	udp := packet[34:42]
	binary.BigEndian.PutUint16(udp[0:2], 53)
	binary.BigEndian.PutUint16(udp[2:4], 53000)
	copy(packet[42:], dns)
	observation, ok := parseDNSPacket(packet, time.Unix(1, 0))
	if !ok {
		t.Fatal("expected a visible UDP DNS response")
	}
	if observation.Domain != "www.example.com" || observation.DnsIp != "1.2.3.4" || observation.DnsTtl != 60 || !observation.DnsMetadataOnly {
		t.Fatalf("unexpected DNS metadata: %+v", observation)
	}
}
