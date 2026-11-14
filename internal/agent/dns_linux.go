//go:build linux

package agent

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	pb "github.com/network-observer/network-observer/gen/networkobserver/v1"
	"golang.org/x/sys/unix"
)

type dnsCollector struct {
	fd        int
	stop      chan struct{}
	done      chan struct{}
	mu        sync.Mutex
	items     []*pb.FlowObservation
	available bool
	dropped   uint64
}

func newDNSCollector() *dnsCollector {
	c := &dnsCollector{fd: -1, stop: make(chan struct{}), done: make(chan struct{})}
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_NONBLOCK, int(htons(unix.ETH_P_ALL)))
	if err != nil {
		close(c.done)
		return c
	}
	c.fd = fd
	c.available = true
	go c.loop()
	return c
}

func (c *dnsCollector) loop() {
	defer close(c.done)
	buf := make([]byte, 64*1024)
	for {
		select {
		case <-c.stop:
			return
		default:
		}
		n, _, err := unix.Recvfrom(c.fd, buf, 0)
		if err != nil {
			if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
				time.Sleep(20 * time.Millisecond)
				continue
			}
			return
		}
		if observation, ok := parseDNSPacket(buf[:n], time.Now().UTC()); ok {
			c.mu.Lock()
			if len(c.items) < 1000 {
				c.items = append(c.items, observation)
			} else {
				c.dropped++
			}
			c.mu.Unlock()
		}
	}
}

func (c *dnsCollector) Drain(observerID string, now time.Time) []*pb.FlowObservation {
	c.mu.Lock()
	defer c.mu.Unlock()
	items := c.items
	c.items = nil
	for _, item := range items {
		item.ObserverId = observerID
		item.Id = fmt.Sprintf("%s:%d", item.FlowId, item.ObservedAtUnixNanos)
		item.ObservedAtUnixNanos = now.UnixNano()
		item.Id = fmt.Sprintf("%s:%d", item.FlowId, item.ObservedAtUnixNanos)
	}
	return items
}

func (c *dnsCollector) Close() error {
	select {
	case <-c.done:
	default:
		close(c.stop)
		<-c.done
	}
	if c.fd >= 0 {
		return unix.Close(c.fd)
	}
	return nil
}

func (c *dnsCollector) Status() (string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.available && c.dropped == 0 {
		return "supported", "visible UDP/53 DNS metadata parser attached"
	}
	if c.available {
		return "degraded", fmt.Sprintf("DNS metadata queue dropped %d records", c.dropped)
	}
	return "unavailable", "AF_PACKET raw socket could not be opened; grant NET_RAW or disable DNS metadata"
}

func parseDNSPacket(packet []byte, observed time.Time) (*pb.FlowObservation, bool) {
	if len(packet) < 14 {
		return nil, false
	}
	offset := 14
	ethType := binary.BigEndian.Uint16(packet[12:14])
	if ethType == 0x8100 && len(packet) >= 18 {
		ethType = binary.BigEndian.Uint16(packet[16:18])
		offset = 18
	}
	var src, dst net.IP
	var udp []byte
	if ethType == 0x0800 {
		if len(packet) < offset+20 {
			return nil, false
		}
		ihl := int(packet[offset]&0x0f) * 4
		if len(packet) < offset+ihl+8 || packet[offset+9] != 17 {
			return nil, false
		}
		src, dst = net.IP(packet[offset+12:offset+16]), net.IP(packet[offset+16:offset+20])
		udp = packet[offset+ihl:]
	} else if ethType == 0x86dd {
		if len(packet) < offset+40+8 || packet[offset+6] != 17 {
			return nil, false
		}
		src, dst = net.IP(packet[offset+8:offset+24]), net.IP(packet[offset+24:offset+40])
		udp = packet[offset+40:]
	} else {
		return nil, false
	}
	if len(udp) < 8 {
		return nil, false
	}
	sourcePort, destPort := binary.BigEndian.Uint16(udp[:2]), binary.BigEndian.Uint16(udp[2:4])
	if sourcePort != 53 && destPort != 53 {
		return nil, false
	}
	dns := udp[8:]
	if len(dns) < 12 {
		return nil, false
	}
	flags := binary.BigEndian.Uint16(dns[2:4])
	questions := binary.BigEndian.Uint16(dns[4:6])
	answers := binary.BigEndian.Uint16(dns[6:8])
	if questions == 0 {
		return nil, false
	}
	name, pos, ok := dnsName(dns, 12)
	if !ok {
		return nil, false
	}
	for i := uint16(0); i < questions; i++ {
		if pos+4 > len(dns) {
			return nil, false
		}
		pos += 4
	}
	if flags&0x8000 == 0 {
		return nil, false
	}
	for i := uint16(0); i < answers; i++ {
		_, next, ok := dnsName(dns, pos)
		if !ok || next+10 > len(dns) {
			return nil, false
		}
		pos = next
		typ := binary.BigEndian.Uint16(dns[pos : pos+2])
		class := binary.BigEndian.Uint16(dns[pos+2 : pos+4])
		ttl := binary.BigEndian.Uint32(dns[pos+4 : pos+8])
		length := int(binary.BigEndian.Uint16(dns[pos+8 : pos+10]))
		pos += 10
		if pos+length > len(dns) {
			return nil, false
		}
		if class == 1 && (typ == 1 && length == 4 || typ == 28 && length == 16) {
			ip := net.IP(dns[pos : pos+length])
			flow := fmt.Sprintf("dns:%s:%s", name, ip.String())
			return &pb.FlowObservation{FlowId: flow, ObserverId: "dns", ObserverType: "endpoint", LocalIsSource: true, ObservedAtUnixNanos: observed.UnixNano(), Interface: "", Protocol: "udp", SourceIp: src.String(), SourcePort: uint32(sourcePort), DestinationIp: dst.String(), DestinationPort: uint32(destPort), CounterSource: "dns_udp53", Confidence: "exact", SamplingRate: 1, Domain: strings.TrimSuffix(strings.ToLower(name), "."), DnsMetadataOnly: true, DnsIp: ip.String(), DnsTtl: ttl, Active: false}, true
		}
		pos += length
	}
	return nil, false
}

func dnsName(packet []byte, pos int) (string, int, bool) {
	var labels []string
	start := pos
	jumped := false
	jumps := 0
	for {
		if pos >= len(packet) || jumps > 16 {
			return "", 0, false
		}
		length := int(packet[pos])
		pos++
		if length == 0 {
			if !jumped {
				start = pos
			}
			return strings.Join(labels, "."), start, true
		}
		if length&0xc0 == 0xc0 {
			if pos >= len(packet) {
				return "", 0, false
			}
			pointer := ((length & 0x3f) << 8) | int(packet[pos])
			pos++
			if !jumped {
				start = pos
			}
			pos = pointer
			jumped = true
			jumps++
			continue
		}
		if length > 63 || pos+length > len(packet) {
			return "", 0, false
		}
		labels = append(labels, string(packet[pos:pos+length]))
		pos += length
	}
}

func htons(value uint16) uint16 { return value<<8 | value>>8 }
