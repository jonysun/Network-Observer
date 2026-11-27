//go:build linux

package agent

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/tcpassembly"
	pb "github.com/network-observer/network-observer/gen/networkobserver/v1"
	"golang.org/x/sys/unix"
)

type dnsPacketResult struct {
	candidate    bool
	response     bool
	valid        bool
	observations []*pb.FlowObservation
}

type dnsCollector struct {
	fd           int
	stop         chan struct{}
	done         chan struct{}
	mu           sync.Mutex
	items        []*pb.FlowObservation
	available    bool
	frames       uint64
	dnsPackets   uint64
	tcpPackets   uint64
	tcpResponses uint64
	tcpRecords   uint64
	tcpDropped   uint64
	responses    uint64
	records      uint64
	lastResponse time.Time
	dropped      uint64
	tcpAssembler *tcpassembly.Assembler
	tcpFactory   *dnsTCPFactory
	tcpFlows     map[string]struct{}
}

// dnsCapture serializes drain, enable/disable and shutdown without locking
// unrelated socket or conntrack collection.
type dnsCapture struct {
	mu        sync.Mutex
	collector *dnsCollector
	closed    bool
}

func (c *dnsCapture) SetDNSMetadataEnabled(enabled bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	if enabled && c.collector == nil {
		c.collector = newDNSCollector()
	} else if !enabled && c.collector != nil {
		collector := c.collector
		c.collector = nil
		return collector.Close()
	}
	return nil
}

func (c *dnsCapture) snapshot(observerID string, now time.Time) ([]*pb.FlowObservation, *pb.Capability) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.collector == nil {
		return nil, capability("dns_metadata", "disabled", "DNS metadata collection is disabled by configuration")
	}
	items := c.collector.Drain(observerID, now)
	status, detail := c.collector.Status()
	return items, capability("dns_metadata", status, detail)
}

func (c *dnsCapture) close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.collector == nil {
		return nil
	}
	collector := c.collector
	c.collector = nil
	return collector.Close()
}

func newDNSCollector() *dnsCollector {
	c := &dnsCollector{fd: -1, stop: make(chan struct{}), done: make(chan struct{})}
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, int(htons(unix.ETH_P_ALL)))
	if err != nil {
		close(c.done)
		return c
	}
	c.fd = fd
	c.available = true
	c.tcpFlows = make(map[string]struct{})
	c.tcpFactory = &dnsTCPFactory{collector: c}
	c.tcpAssembler = tcpassembly.NewAssembler(tcpassembly.NewStreamPool(c.tcpFactory))
	c.tcpAssembler.MaxBufferedPagesTotal = 256
	c.tcpAssembler.MaxBufferedPagesPerConnection = 16
	go c.loop()
	return c
}

func (c *dnsCollector) loop() {
	defer close(c.done)
	buf := make([]byte, 64*1024)
	flush := time.NewTicker(5 * time.Second)
	defer flush.Stop()
	for {
		select {
		case <-c.stop:
			c.tcpAssembler.FlushAll()
			return
		case now := <-flush.C:
			c.tcpAssembler.FlushOlderThan(now.Add(-30 * time.Second))
		default:
		}
		n, from, err := unix.Recvfrom(c.fd, buf, 0)
		if err != nil {
			if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
				time.Sleep(20 * time.Millisecond)
				continue
			}
			if err == unix.EINTR {
				continue
			}
			c.mu.Lock()
			c.available = false
			c.mu.Unlock()
			return
		}
		observed := time.Now().UTC()
		parsed := parseDNSPacket(buf[:n], observed)
		if tp, ok := parseDNSTCPPacket(buf[:n]); ok {
			c.mu.Lock()
			c.tcpPackets++
			_, exists := c.tcpFlows[tp.flowKey]
			if !exists && len(c.tcpFlows) < 256 && (tp.tcp.SYN || len(tp.tcp.Payload) > 0) {
				c.tcpFlows[tp.flowKey] = struct{}{}
				exists = true
			} else if !exists && (tp.tcp.SYN || len(tp.tcp.Payload) > 0) {
				c.tcpDropped++
			}
			c.mu.Unlock()
			if exists {
				c.tcpAssembler.AssembleWithTimestamp(tp.networkFlow, tp.tcp, observed)
			}
		}
		c.mu.Lock()
		c.frames++
		if parsed.candidate {
			c.dnsPackets++
		}
		if parsed.response {
			c.responses++
			c.lastResponse = observed
		}
		if link, ok := from.(*unix.SockaddrLinklayer); ok && link.Ifindex > 0 {
			if networkInterface, lookupErr := net.InterfaceByIndex(link.Ifindex); lookupErr == nil {
				for _, observation := range parsed.observations {
					observation.Interface = networkInterface.Name
				}
			}
		}
		if parsed.valid {
			c.records += uint64(len(parsed.observations))
			for _, observation := range parsed.observations {
				if len(c.items) < 1000 {
					c.items = append(c.items, observation)
				} else {
					c.dropped++
				}
			}
		}
		c.mu.Unlock()
	}
}

func (c *dnsCollector) Drain(observerID string, _ time.Time) []*pb.FlowObservation {
	c.mu.Lock()
	defer c.mu.Unlock()
	items := c.items
	c.items = nil
	for _, item := range items {
		item.ObserverId = observerID
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
	status := "supported"
	if !c.available {
		status = "unavailable"
	} else if c.dropped > 0 || c.tcpDropped > 0 {
		status = "degraded"
	}
	last := "none"
	if !c.lastResponse.IsZero() {
		last = c.lastResponse.Format(time.RFC3339)
	}
	detail := fmt.Sprintf("AF_PACKET ready; frames=%d udp53_packets=%d dns_responses=%d metadata_records=%d tcp53_packets=%d tcp53_responses=%d tcp53_records=%d tcp_reassembly_dropped=%d queue_dropped=%d last_dns_response=%s", c.frames, c.dnsPackets, c.responses, c.records, c.tcpPackets, c.tcpResponses, c.tcpRecords, c.tcpDropped, c.dropped, last)
	if !c.available {
		detail = "AF_PACKET capture stopped; verify NET_RAW and capture environment; " + detail
	}
	return status, detail
}

type dnsTCPPacket struct {
	networkFlow gopacket.Flow
	tcp         *layers.TCP
	flowKey     string
}

func parseDNSTCPPacket(frame []byte) (dnsTCPPacket, bool) {
	p := gopacket.NewPacket(frame, layers.LayerTypeEthernet, gopacket.NoCopy)
	n := p.NetworkLayer()
	if n == nil || p.TransportLayer() == nil || p.TransportLayer().LayerType() != layers.LayerTypeTCP {
		return dnsTCPPacket{}, false
	}
	tcp := p.TransportLayer().(*layers.TCP)
	if tcp.SrcPort != 53 && tcp.DstPort != 53 {
		return dnsTCPPacket{}, false
	}
	nf, tf := n.NetworkFlow(), tcp.TransportFlow()
	return dnsTCPPacket{nf, tcp, nf.String() + "/" + tf.String()}, true
}

type dnsTCPFactory struct{ collector *dnsCollector }

func (f *dnsTCPFactory) New(nf, tf gopacket.Flow) tcpassembly.Stream {
	src, dst := dnsPort(tf.Src().Raw()), dnsPort(tf.Dst().Raw())
	return &dnsTCPStream{factory: f, srcIP: nf.Src().String(), dstIP: nf.Dst().String(), srcPort: src, dstPort: dst, response: src == 53, flowKey: nf.String() + "/" + tf.String()}
}
func dnsPort(raw []byte) uint16 {
	if len(raw) < 2 {
		return 0
	}
	return binary.BigEndian.Uint16(raw)
}

type dnsTCPStream struct {
	factory          *dnsTCPFactory
	srcIP, dstIP     string
	srcPort, dstPort uint16
	response         bool
	flowKey          string
	buffer           []byte
	dropped          bool
}

func (s *dnsTCPStream) Reassembled(parts []tcpassembly.Reassembly) {
	for _, part := range parts {
		if part.Skip > 0 {
			s.buffer = nil
			s.dropped = true
			s.factory.collector.mu.Lock()
			s.factory.collector.tcpDropped++
			s.factory.collector.mu.Unlock()
		}
		if s.dropped || !s.response {
			continue
		}
		if len(s.buffer)+len(part.Bytes) > 2*(65535+2) {
			s.buffer = nil
			s.dropped = true
			s.factory.collector.mu.Lock()
			s.factory.collector.tcpDropped++
			s.factory.collector.mu.Unlock()
			continue
		}
		s.buffer = append(s.buffer, part.Bytes...)
		for len(s.buffer) >= 2 {
			size := int(binary.BigEndian.Uint16(s.buffer[:2]))
			if size < 12 {
				s.buffer = nil
				s.dropped = true
				break
			}
			if len(s.buffer) < size+2 {
				break
			}
			msg := append([]byte(nil), s.buffer[2:size+2]...)
			s.buffer = s.buffer[size+2:]
			now := time.Now().UTC()
			parsed := parseDNSMessage(msg, net.ParseIP(s.srcIP), net.ParseIP(s.dstIP), s.srcPort, s.dstPort, "tcp", now)
			c := s.factory.collector
			c.mu.Lock()
			if parsed.response {
				c.tcpResponses++
				c.lastResponse = now
			}
			if parsed.valid {
				c.tcpRecords += uint64(len(parsed.observations))
				for _, item := range parsed.observations {
					if len(c.items) < 1000 {
						c.items = append(c.items, item)
					} else {
						c.dropped++
					}
				}
			}
			c.mu.Unlock()
		}
	}
}
func (s *dnsTCPStream) ReassemblyComplete() {
	c := s.factory.collector
	c.mu.Lock()
	delete(c.tcpFlows, s.flowKey)
	c.mu.Unlock()
}

func isDNSUDP53Packet(packet []byte) bool {
	if len(packet) < 14 {
		return false
	}
	offset := 14
	typ := binary.BigEndian.Uint16(packet[12:14])
	for tags := 0; typ == 0x8100 || typ == 0x88a8 || typ == 0x9100; tags++ {
		if tags == 4 || len(packet) < offset+4 {
			return false
		}
		typ = binary.BigEndian.Uint16(packet[offset+2 : offset+4])
		offset += 4
	}
	var proto byte
	var transport []byte
	switch typ {
	case 0x0800:
		if len(packet) < offset+20 || packet[offset]>>4 != 4 {
			return false
		}
		ihl := int(packet[offset]&0xf) * 4
		if ihl < 20 || len(packet) < offset+ihl+4 {
			return false
		}
		proto = packet[offset+9]
		transport = packet[offset+ihl:]
	case 0x86dd:
		if len(packet) < offset+40+4 {
			return false
		}
		proto = packet[offset+6]
		transport = packet[offset+40:]
	default:
		return false
	}
	return proto == 17 && len(transport) >= 4 && (binary.BigEndian.Uint16(transport[:2]) == 53 || binary.BigEndian.Uint16(transport[2:4]) == 53)
}

func parseDNSPacket(packet []byte, observed time.Time) dnsPacketResult {
	var result dnsPacketResult
	if len(packet) < 14 {
		return result
	}
	offset := 14
	etherType := binary.BigEndian.Uint16(packet[12:14])
	for tags := 0; etherType == 0x8100 || etherType == 0x88a8 || etherType == 0x9100; tags++ {
		if tags == 4 || len(packet) < offset+4 {
			return result
		}
		etherType = binary.BigEndian.Uint16(packet[offset+2 : offset+4])
		offset += 4
	}
	var src, dst net.IP
	var udp []byte
	switch etherType {
	case 0x0800:
		if len(packet) < offset+20 || packet[offset]>>4 != 4 {
			return result
		}
		ihl := int(packet[offset]&0x0f) * 4
		totalLength := int(binary.BigEndian.Uint16(packet[offset+2 : offset+4]))
		if ihl < 20 || totalLength < ihl || len(packet) < offset+totalLength || packet[offset+9] != 17 || binary.BigEndian.Uint16(packet[offset+6:offset+8])&0x3fff != 0 {
			return result
		}
		src, dst = net.IP(packet[offset+12:offset+16]), net.IP(packet[offset+16:offset+20])
		udp = packet[offset+ihl : offset+totalLength]
	case 0x86dd:
		if len(packet) < offset+40 || packet[offset]>>4 != 6 || packet[offset+6] != 17 {
			return result
		}
		payloadLength := int(binary.BigEndian.Uint16(packet[offset+4 : offset+6]))
		if payloadLength == 0 || len(packet) < offset+40+payloadLength {
			return result
		}
		src, dst = net.IP(packet[offset+8:offset+24]), net.IP(packet[offset+24:offset+40])
		udp = packet[offset+40 : offset+40+payloadLength]
	default:
		return result
	}
	if len(udp) < 8 {
		return result
	}
	sourcePort, destPort := binary.BigEndian.Uint16(udp[:2]), binary.BigEndian.Uint16(udp[2:4])
	if sourcePort != 53 && destPort != 53 {
		return result
	}
	result.candidate = true
	udpLength := int(binary.BigEndian.Uint16(udp[4:6]))
	if udpLength < 8 || udpLength > len(udp) {
		return result
	}
	dns := udp[8:udpLength]
	return parseDNSMessage(dns, src, dst, sourcePort, destPort, "udp", observed)
}
func parseDNSMessage(dns []byte, src, dst net.IP, sourcePort, destPort uint16, protocol string, observed time.Time) dnsPacketResult {
	var result dnsPacketResult
	result.candidate = true
	if len(dns) < 12 {
		return result
	}
	flags := binary.BigEndian.Uint16(dns[2:4])
	questions, answers := binary.BigEndian.Uint16(dns[4:6]), binary.BigEndian.Uint16(dns[6:8])
	if flags&0x8000 == 0 {
		result.valid = true
		return result
	}
	result.response = true
	if questions == 0 {
		return result
	}
	queryName, pos, ok := dnsName(dns, 12)
	if !ok {
		return result
	}
	queryName = strings.TrimSuffix(strings.ToLower(queryName), ".")
	for i := uint16(0); i < questions; i++ {
		if i > 0 {
			_, pos, ok = dnsName(dns, pos)
			if !ok {
				return result
			}
		}
		if pos+4 > len(dns) {
			return result
		}
		pos += 4
	}
	if flags&0x000f != 0 {
		result.valid = true
		return result
	}
	type addressRecord struct {
		owner, ip string
		ttl       uint32
	}
	addresses := make([]addressRecord, 0, answers)
	aliases := make(map[string]addressRecord)
	for i := uint16(0); i < answers; i++ {
		owner, next, valid := dnsName(dns, pos)
		if !valid || next+10 > len(dns) {
			return result
		}
		pos = next
		typ, class := binary.BigEndian.Uint16(dns[pos:pos+2]), binary.BigEndian.Uint16(dns[pos+2:pos+4])
		ttl, length := binary.BigEndian.Uint32(dns[pos+4:pos+8]), int(binary.BigEndian.Uint16(dns[pos+8:pos+10]))
		pos += 10
		if pos+length > len(dns) {
			return result
		}
		owner = strings.TrimSuffix(strings.ToLower(owner), ".")
		if class == 1 && typ == 5 {
			target, consumed, valid := dnsName(dns, pos)
			if !valid || consumed > pos+length {
				return result
			}
			aliases[owner] = addressRecord{owner: strings.TrimSuffix(strings.ToLower(target), "."), ttl: ttl}
		} else if class == 1 && typ == 1 && length == net.IPv4len {
			addresses = append(addresses, addressRecord{owner: owner, ip: net.IP(dns[pos : pos+length]).String(), ttl: ttl})
		} else if class == 1 && typ == 28 && length == net.IPv6len {
			addresses = append(addresses, addressRecord{owner: owner, ip: net.IP(dns[pos : pos+length]).String(), ttl: ttl})
		}
		pos += length
	}
	result.valid = true
	owners := map[string]uint32{queryName: ^uint32(0)}
	owner := queryName
	chainTTL := ^uint32(0)
	for depth := 0; depth < 16; depth++ {
		alias, exists := aliases[owner]
		if !exists {
			break
		}
		if _, cycle := owners[alias.owner]; cycle {
			break
		}
		if alias.ttl < chainTTL {
			chainTTL = alias.ttl
		}
		owners[alias.owner] = chainTTL
		owner = alias.owner
	}
	for _, answer := range addresses {
		ttl, belongs := owners[answer.owner]
		if !belongs {
			continue
		}
		if ttl < answer.ttl {
			answer.ttl = ttl
		}
		flow := fmt.Sprintf("dns:%s:%s", queryName, answer.ip)
		identity := append(append(append(append([]byte(nil), dns...), []byte(src.String())...), []byte(dst.String())...), []byte(flow)...)
		identity = append(identity, []byte(answer.ip)...)
		result.observations = append(result.observations, &pb.FlowObservation{Id: dnsObservationID(identity, observed.UnixNano()), FlowId: flow, ObserverId: "dns", ObserverType: "endpoint", LocalIsSource: false, ObservedAtUnixNanos: observed.UnixNano(), Protocol: protocol, SourceIp: src.String(), SourcePort: uint32(sourcePort), DestinationIp: dst.String(), DestinationPort: uint32(destPort), CounterSource: "dns_" + protocol + "53", Confidence: "exact", SamplingRate: 1, Domain: queryName, DnsMetadataOnly: true, DnsIp: answer.ip, DnsTtl: answer.ttl, Active: false})
	}
	return result
}

func dnsObservationID(packet []byte, _ int64) string {
	digest := sha256.Sum256(packet)
	return "dns:" + hex.EncodeToString(digest[:16])
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
