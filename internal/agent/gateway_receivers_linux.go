//go:build linux

package agent

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type externalFlow struct {
	source          string
	protocol        string
	sourceIP        net.IP
	destinationIP   net.IP
	sourcePort      uint16
	destinationPort uint16
	interfaceName   string
	bytes           uint64
	packets         uint64
	cumulative      bool
	samplingRate    uint32
	counterSource   string
	confidence      string
}

type gatewayReceiverStatus struct {
	status string
	detail string
}

type gatewayFlowReceiver struct {
	kind       string
	conn       *net.UDPConn
	queue      chan externalFlow
	stop       chan struct{}
	done       chan struct{}
	dropped    atomic.Uint64
	parseErrs  atomic.Uint64
	datagrams  atomic.Uint64
	parsed     atomic.Uint64
	templates  map[ipfixTemplateKey]ipfixTemplate
	templateMu sync.Mutex
	closeOnce  sync.Once
}

func startGatewayReceiver(kind, address string) (*gatewayFlowReceiver, gatewayReceiverStatus) {
	if address == "" {
		return nil, gatewayReceiverStatus{status: "disabled", detail: "listen address is not configured"}
	}
	udpAddress, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return nil, gatewayReceiverStatus{status: "degraded", detail: fmt.Sprintf("resolve %s listener %q: %v", kind, address, err)}
	}
	conn, err := net.ListenUDP("udp", udpAddress)
	if err != nil {
		return nil, gatewayReceiverStatus{status: "degraded", detail: fmt.Sprintf("listen for %s on %s: %v", kind, address, err)}
	}
	receiver := &gatewayFlowReceiver{
		kind: kind, conn: conn, queue: make(chan externalFlow, 4096), stop: make(chan struct{}), done: make(chan struct{}),
		templates: make(map[ipfixTemplateKey]ipfixTemplate),
	}
	go receiver.run()
	return receiver, gatewayReceiverStatus{status: "supported", detail: fmt.Sprintf("listening for %s on %s", kind, address)}
}

func (r *gatewayFlowReceiver) run() {
	defer close(r.done)
	defer r.conn.Close()
	packet := make([]byte, 64<<10)
	for {
		_ = r.conn.SetReadDeadline(time.Now().Add(time.Second))
		n, remote, err := r.conn.ReadFromUDP(packet)
		if err != nil {
			select {
			case <-r.stop:
				return
			default:
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			continue
		}
		flows, err := r.parse(packet[:n], remote.IP)
		r.datagrams.Add(1)
		if err != nil {
			r.parseErrs.Add(1)
			continue
		}
		r.parsed.Add(uint64(len(flows)))
		for _, flow := range flows {
			select {
			case r.queue <- flow:
			default:
				r.dropped.Add(1)
			}
		}
	}
}

func (r *gatewayFlowReceiver) close() {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		close(r.stop)
		_ = r.conn.Close()
		<-r.done
	})
}

func (r *gatewayFlowReceiver) status() gatewayReceiverStatus {
	if r == nil {
		return gatewayReceiverStatus{status: "disabled", detail: "listener is not configured"}
	}
	dropped := r.dropped.Load()
	parseErrors := r.parseErrs.Load()
	state := "supported"
	if dropped != 0 || parseErrors != 0 {
		state = "degraded"
	}
	return gatewayReceiverStatus{
		status: state,
		detail: fmt.Sprintf("listening on %s; received %d datagram(s), parsed %d flow record(s), dropped %d, parse errors %d",
			r.conn.LocalAddr(), r.datagrams.Load(), r.parsed.Load(), dropped, parseErrors),
	}
}

func (r *gatewayFlowReceiver) drain() []externalFlow {
	if r == nil {
		return nil
	}
	flows := make([]externalFlow, 0)
	for {
		select {
		case flow := <-r.queue:
			flows = append(flows, flow)
		default:
			return flows
		}
	}
}

func (r *gatewayFlowReceiver) parse(packet []byte, exporter net.IP) ([]externalFlow, error) {
	switch r.kind {
	case "netflow":
		return parseNetFlowV5(packet, exporter)
	case "ipfix":
		r.templateMu.Lock()
		defer r.templateMu.Unlock()
		return parseIPFIX(packet, exporter, r.templates)
	case "sflow":
		return parseSFlowV5(packet, exporter)
	default:
		return nil, fmt.Errorf("unsupported gateway receiver %q", r.kind)
	}
}

func parseNetFlowV5(packet []byte, exporter net.IP) ([]externalFlow, error) {
	if len(packet) < 24 || binary.BigEndian.Uint16(packet[0:2]) != 5 {
		return nil, fmt.Errorf("invalid NetFlow v5 header")
	}
	count := int(binary.BigEndian.Uint16(packet[2:4]))
	if count < 0 || len(packet) < 24+count*48 {
		return nil, fmt.Errorf("NetFlow v5 packet is truncated")
	}
	sampling := binary.BigEndian.Uint16(packet[22:24]) & 0x3fff
	if sampling == 0 {
		sampling = 1
	}
	flows := make([]externalFlow, 0, count)
	for index := 0; index < count; index++ {
		offset := 24 + index*48
		record := packet[offset : offset+48]
		protocol := record[37]
		if protocol != 6 && protocol != 17 {
			continue
		}
		flows = append(flows, externalFlow{
			source:          "netflow:" + exporter.String(),
			protocol:        protocolName(protocol),
			sourceIP:        net.IPv4(record[0], record[1], record[2], record[3]),
			destinationIP:   net.IPv4(record[4], record[5], record[6], record[7]),
			sourcePort:      binary.BigEndian.Uint16(record[32:34]),
			destinationPort: binary.BigEndian.Uint16(record[34:36]),
			interfaceName:   interfaceNameOrIndex(uint32(binary.BigEndian.Uint16(record[12:14]))),
			bytes:           uint64(binary.BigEndian.Uint32(record[20:24])),
			packets:         uint64(binary.BigEndian.Uint32(record[16:20])),
			cumulative:      true,
			samplingRate:    uint32(sampling),
			counterSource:   "netflow_v5",
			confidence:      confidenceForSampling(uint32(sampling)),
		})
	}
	return flows, nil
}

type ipfixTemplateKey struct {
	exporter string
	domain   uint32
	id       uint16
}

type ipfixTemplate struct {
	fields []ipfixField
}

type ipfixField struct {
	id         uint16
	length     uint16
	enterprise uint32
}

func parseIPFIX(packet []byte, exporter net.IP, templates map[ipfixTemplateKey]ipfixTemplate) ([]externalFlow, error) {
	if len(packet) < 16 || binary.BigEndian.Uint16(packet[0:2]) != 10 {
		return nil, fmt.Errorf("invalid IPFIX header")
	}
	length := int(binary.BigEndian.Uint16(packet[2:4]))
	if length < 16 || length > len(packet) {
		return nil, fmt.Errorf("invalid IPFIX length")
	}
	domain := binary.BigEndian.Uint32(packet[12:16])
	flows := make([]externalFlow, 0)
	for offset := 16; offset+4 <= length; {
		setID := binary.BigEndian.Uint16(packet[offset : offset+2])
		setLength := int(binary.BigEndian.Uint16(packet[offset+2 : offset+4]))
		if setLength < 4 || offset+setLength > length {
			return nil, fmt.Errorf("invalid IPFIX set length")
		}
		body := packet[offset+4 : offset+setLength]
		if setID == 2 {
			if err := parseIPFIXTemplates(body, ipfixTemplateKey{exporter: exporter.String(), domain: domain}, templates); err != nil {
				return nil, err
			}
		} else if setID >= 256 {
			template, ok := templates[ipfixTemplateKey{exporter: exporter.String(), domain: domain, id: setID}]
			if ok {
				parsed, err := parseIPFIXData(body, template)
				if err != nil {
					return nil, err
				}
				for index := range parsed {
					parsed[index].source = "ipfix:" + exporter.String()
					parsed[index].counterSource = "ipfix"
					parsed[index].samplingRate = 1
					parsed[index].confidence = "exact"
					parsed[index].cumulative = false
				}
				flows = append(flows, parsed...)
			}
		}
		offset += setLength
	}
	return flows, nil
}

func parseIPFIXTemplates(body []byte, base ipfixTemplateKey, templates map[ipfixTemplateKey]ipfixTemplate) error {
	for offset := 0; offset+4 <= len(body); {
		id := binary.BigEndian.Uint16(body[offset : offset+2])
		fieldCount := int(binary.BigEndian.Uint16(body[offset+2 : offset+4]))
		offset += 4
		fields := make([]ipfixField, 0, fieldCount)
		for index := 0; index < fieldCount; index++ {
			if offset+4 > len(body) {
				return fmt.Errorf("truncated IPFIX template")
			}
			fieldID := binary.BigEndian.Uint16(body[offset : offset+2])
			fieldLength := binary.BigEndian.Uint16(body[offset+2 : offset+4])
			offset += 4
			field := ipfixField{id: fieldID & 0x7fff, length: fieldLength}
			if fieldID&0x8000 != 0 {
				if offset+4 > len(body) {
					return fmt.Errorf("truncated IPFIX enterprise field")
				}
				field.enterprise = binary.BigEndian.Uint32(body[offset : offset+4])
				offset += 4
			}
			fields = append(fields, field)
		}
		templates[ipfixTemplateKey{exporter: base.exporter, domain: base.domain, id: id}] = ipfixTemplate{fields: fields}
	}
	return nil
}

func parseIPFIXData(body []byte, template ipfixTemplate) ([]externalFlow, error) {
	recordLength := 0
	for _, field := range template.fields {
		if field.length == 0xffff {
			return nil, fmt.Errorf("variable-length IPFIX fields are unsupported")
		}
		recordLength += int(field.length)
	}
	if recordLength == 0 {
		return nil, nil
	}
	flows := make([]externalFlow, 0, len(body)/recordLength)
	for offset := 0; offset+recordLength <= len(body); offset += recordLength {
		flow := externalFlow{}
		cursor := offset
		for _, field := range template.fields {
			value := body[cursor : cursor+int(field.length)]
			cursor += int(field.length)
			switch field.id {
			case 4:
				if len(value) == 1 {
					flow.protocol = protocolName(value[0])
				}
			case 7:
				if len(value) == 2 {
					flow.sourcePort = binary.BigEndian.Uint16(value)
				}
			case 11:
				if len(value) == 2 {
					flow.destinationPort = binary.BigEndian.Uint16(value)
				}
			case 1:
				flow.bytes = uint64Value(value)
			case 2:
				flow.packets = uint64Value(value)
			case 10: // ingressInterface
				if len(value) == 4 {
					flow.interfaceName = interfaceNameOrIndex(uint32Value(value))
				}
			case 14: // egressInterface
				if len(value) == 4 && flow.interfaceName == "" {
					flow.interfaceName = interfaceNameOrIndex(uint32Value(value))
				}
			case 34: // samplingInterval
				flow.samplingRate = uint32(uint64Value(value))
			case 8:
				if len(value) == 4 {
					flow.sourceIP = net.IPv4(value[0], value[1], value[2], value[3])
				}
			case 12:
				if len(value) == 4 {
					flow.destinationIP = net.IPv4(value[0], value[1], value[2], value[3])
				}
			case 27:
				if len(value) == 16 {
					flow.sourceIP = net.IP(append([]byte(nil), value...))
				}
			case 28:
				if len(value) == 16 {
					flow.destinationIP = net.IP(append([]byte(nil), value...))
				}
			}
		}
		if flow.sourceIP != nil && flow.destinationIP != nil && (flow.protocol == "tcp" || flow.protocol == "udp") && flow.sourcePort != 0 && flow.destinationPort != 0 {
			if flow.samplingRate == 0 {
				flow.samplingRate = 1
			}
			flow.confidence = confidenceForSampling(flow.samplingRate)
			flows = append(flows, flow)
		}
	}
	return flows, nil
}

func parseSFlowV5(packet []byte, exporter net.IP) ([]externalFlow, error) {
	if len(packet) < 28 || binary.BigEndian.Uint32(packet[0:4]) != 5 {
		return nil, fmt.Errorf("invalid sFlow v5 header")
	}
	offset := 4
	addressType := binary.BigEndian.Uint32(packet[offset : offset+4])
	offset += 4
	if addressType == 1 {
		if offset+4 > len(packet) {
			return nil, fmt.Errorf("truncated sFlow agent address")
		}
		offset += 4
	} else if addressType == 2 {
		if offset+16 > len(packet) {
			return nil, fmt.Errorf("truncated sFlow agent address")
		}
		offset += 16
	} else {
		return nil, fmt.Errorf("unsupported sFlow address type")
	}
	if offset+20 > len(packet) {
		return nil, fmt.Errorf("truncated sFlow header")
	}
	offset += 12 // sub-agent, datagram sequence, uptime
	sampleCount := int(binary.BigEndian.Uint32(packet[offset : offset+4]))
	offset += 4
	flows := make([]externalFlow, 0)
	for sampleIndex := 0; sampleIndex < sampleCount; sampleIndex++ {
		if offset+8 > len(packet) {
			return nil, fmt.Errorf("truncated sFlow sample")
		}
		sampleType := binary.BigEndian.Uint32(packet[offset : offset+4])
		sampleLength := int(binary.BigEndian.Uint32(packet[offset+4 : offset+8]))
		offset += 8
		if sampleLength < 0 || offset+sampleLength > len(packet) {
			return nil, fmt.Errorf("invalid sFlow sample length")
		}
		if sampleType&0x0fff == 1 {
			parsed, err := parseSFlowFlowSample(packet[offset:offset+sampleLength], exporter)
			if err != nil {
				return nil, err
			}
			flows = append(flows, parsed...)
		}
		offset += sampleLength
	}
	return flows, nil
}

func parseSFlowFlowSample(sample []byte, exporter net.IP) ([]externalFlow, error) {
	if len(sample) < 32 {
		return nil, fmt.Errorf("truncated sFlow flow sample")
	}
	samplingRate := binary.BigEndian.Uint32(sample[8:12])
	if samplingRate == 0 {
		samplingRate = 1
	}
	recordCount := int(binary.BigEndian.Uint32(sample[28:32]))
	offset := 32
	flows := make([]externalFlow, 0, recordCount)
	inputInterface := binary.BigEndian.Uint32(sample[20:24]) & 0x3fffffff
	outputInterface := binary.BigEndian.Uint32(sample[24:28]) & 0x3fffffff
	interfaceName := interfaceNameOrIndex(inputInterface)
	if interfaceName == "" {
		interfaceName = interfaceNameOrIndex(outputInterface)
	}
	for index := 0; index < recordCount; index++ {
		if offset+8 > len(sample) {
			return nil, fmt.Errorf("truncated sFlow record")
		}
		recordType := binary.BigEndian.Uint32(sample[offset : offset+4])
		recordLength := int(binary.BigEndian.Uint32(sample[offset+4 : offset+8]))
		offset += 8
		if recordLength < 0 || offset+recordLength > len(sample) {
			return nil, fmt.Errorf("invalid sFlow record length")
		}
		if recordType&0x0fff == 1 {
			if flow, ok := parseSFlowRawPacket(sample[offset : offset+recordLength]); ok {
				flow.source = "sflow:" + exporter.String()
				flow.interfaceName = interfaceName
				flow.samplingRate = samplingRate
				flow.counterSource = "sflow_v5"
				flow.confidence = confidenceForSampling(samplingRate)
				flows = append(flows, flow)
			}
		}
		offset += recordLength
	}
	return flows, nil
}

func parseSFlowRawPacket(record []byte) (externalFlow, bool) {
	if len(record) < 16 {
		return externalFlow{}, false
	}
	protocol := binary.BigEndian.Uint32(record[0:4])
	frameLength := binary.BigEndian.Uint32(record[4:8])
	headerLength := int(binary.BigEndian.Uint32(record[12:16]))
	if headerLength < 20 || 16+headerLength > len(record) {
		return externalFlow{}, false
	}
	header := record[16 : 16+headerLength]
	if protocol == 1 {
		if len(header) < 14 {
			return externalFlow{}, false
		}
		typeValue := binary.BigEndian.Uint16(header[12:14])
		header = header[14:]
		if typeValue == 0x0800 {
			protocol = 2
		} else if typeValue == 0x86dd {
			protocol = 3
		} else {
			return externalFlow{}, false
		}
	}
	if protocol == 2 {
		if len(header) < 20 {
			return externalFlow{}, false
		}
		ihl := int(header[0]&0x0f) * 4
		if ihl < 20 || len(header) < ihl+4 || (header[9] != 6 && header[9] != 17) {
			return externalFlow{}, false
		}
		return sampledTransportFlow(header[ihl:], net.IPv4(header[12], header[13], header[14], header[15]), net.IPv4(header[16], header[17], header[18], header[19]), header[9], frameLength)
	}
	if protocol == 3 {
		if len(header) < 40 || (header[6] != 6 && header[6] != 17) {
			return externalFlow{}, false
		}
		return sampledTransportFlow(header[40:], net.IP(append([]byte(nil), header[8:24]...)), net.IP(append([]byte(nil), header[24:40]...)), header[6], frameLength)
	}
	return externalFlow{}, false
}

func sampledTransportFlow(transport []byte, sourceIP, destinationIP net.IP, protocol byte, frameLength uint32) (externalFlow, bool) {
	if len(transport) < 4 {
		return externalFlow{}, false
	}
	return externalFlow{protocol: protocolName(protocol), sourceIP: sourceIP, destinationIP: destinationIP, sourcePort: binary.BigEndian.Uint16(transport[0:2]), destinationPort: binary.BigEndian.Uint16(transport[2:4]), bytes: uint64(frameLength), packets: 1}, true
}

func protocolName(protocol byte) string {
	if protocol == 6 {
		return "tcp"
	}
	if protocol == 17 {
		return "udp"
	}
	return ""
}

func uint64Value(value []byte) uint64 {
	switch len(value) {
	case 1:
		return uint64(value[0])
	case 2:
		return uint64(binary.BigEndian.Uint16(value))
	case 4:
		return uint64(binary.BigEndian.Uint32(value))
	case 8:
		return binary.BigEndian.Uint64(value)
	default:
		return 0
	}
}

func uint32Value(value []byte) uint32 {
	if len(value) != 4 {
		return 0
	}
	return binary.BigEndian.Uint32(value)
}

func confidenceForSampling(sampling uint32) string {
	if sampling > 1 {
		return "estimated"
	}
	return "exact"
}

func interfaceNameOrIndex(index uint32) string {
	if index == 0 {
		return ""
	}
	if iface, err := net.InterfaceByIndex(int(index)); err == nil {
		return iface.Name
	}
	return fmt.Sprintf("ifindex:%d", index)
}
