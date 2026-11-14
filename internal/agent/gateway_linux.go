//go:build linux

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	networkobserverv1 "github.com/network-observer/network-observer/gen/networkobserver/v1"
	"google.golang.org/protobuf/proto"
)

const defaultConntrackPath = "/proc/net/nf_conntrack"
const externalFlowIdleTimeout = 60 * time.Second

type gatewayCollector struct {
	observerID       string
	previous         map[string]*networkobserverv1.FlowObservation
	externalState    map[string]externalCounterState
	config           Config
	receivers        []*gatewayFlowReceiver
	receiverStatuses map[string]gatewayReceiverStatus
	closeOnce        sync.Once
}

type externalCounterState struct {
	bytes            uint64
	packets          uint64
	lastExporterByte uint64
	lastExporterPkts uint64
	observedAt       time.Time
	lastSeen         time.Time
	observation      *networkobserverv1.FlowObservation
}

type conntrackTuple struct {
	source        net.IP
	destination   net.IP
	sourcePort    uint16
	destPort      uint16
	hasSourcePort bool
	hasDestPort   bool
	bytes         uint64
	packets       uint64
}

type conntrackFlow struct {
	protocol string
	state    string
	tuple    conntrackTuple
	reply    conntrackTuple
	zone     string
}

func newGatewayCollectorWithConfig(config Config) Collector {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "linux-gateway"
	}
	collector := &gatewayCollector{
		observerID: hostname + ":gateway",
		previous:   make(map[string]*networkobserverv1.FlowObservation), externalState: make(map[string]externalCounterState),
		config: config, receiverStatuses: make(map[string]gatewayReceiverStatus),
	}
	for kind, address := range map[string]string{"netflow": config.NetFlowListen, "ipfix": config.IPFIXListen, "sflow": config.SFlowListen} {
		receiver, status := startGatewayReceiver(kind, address)
		collector.receiverStatuses[kind] = status
		if receiver != nil {
			collector.receivers = append(collector.receivers, receiver)
		}
	}
	return collector
}

func (c *gatewayCollector) Snapshot(ctx context.Context) ([]*networkobserverv1.FlowObservation, []*networkobserverv1.Capability, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	path := c.config.ConntrackPath
	if path == "" {
		path = defaultConntrackPath
	}
	if c.config.DisableConntrack {
		observations := c.closeConntrack(now)
		observations = append(observations, c.externalObservations(now)...)
		capabilities := gatewayCapabilities("disabled", "disabled by agent configuration", 0, c.currentReceiverStatuses())
		capabilities[len(capabilities)-1] = capability("gateway_nat_tuple", "unavailable", "conntrack is disabled")
		return observations, capabilities, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		status := "degraded"
		if os.IsNotExist(err) {
			status = "unavailable"
		}
		observations := c.externalObservations(now)
		capabilities := gatewayCapabilities(status, fmt.Sprintf("read %s: %v", path, err), len(c.previous), c.currentReceiverStatuses())
		capabilities[len(capabilities)-1] = capability("gateway_nat_tuple", "unavailable", "conntrack tuples could not be read")
		return observations, capabilities, nil
	}

	current := make(map[string]*networkobserverv1.FlowObservation)
	observations := make([]*networkobserverv1.FlowObservation, 0)
	malformed := 0
	natTuples := 0
	natFlows := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		flow, err := parseConntrackLine(line)
		if err != nil {
			malformed++
			continue
		}
		if tupleHasCompletePorts(flow.reply) {
			natTuples++
			if hasNATTranslation(flow) {
				natFlows++
			}
		}
		flowID := conntrackFlowID(flow)
		observation := &networkobserverv1.FlowObservation{
			Id: fmt.Sprintf("%s:%d", flowID, now.UnixNano()), FlowId: flowID, ObserverId: c.observerID,
			ObserverType: "gateway", LocalIsSource: false, ObservedAtUnixNanos: now.UnixNano(),
			NetworkNamespace: gatewayNetworkNamespace(), Protocol: flow.protocol, State: flow.state,
			SourceIp: flow.tuple.source.String(), SourcePort: uint32(flow.tuple.sourcePort),
			DestinationIp: flow.tuple.destination.String(), DestinationPort: uint32(flow.tuple.destPort), Active: true,
			NatSourceIp: natSourceIP(flow), NatSourcePort: natSourcePort(flow),
			NatDestinationIp: natDestinationIP(flow), NatDestinationPort: natDestinationPort(flow),
			TotalBytesSourceToDestination: flow.tuple.bytes, TotalBytesDestinationToSource: flow.reply.bytes,
			PacketsSourceToDestination: flow.tuple.packets, PacketsDestinationToSource: flow.reply.packets,
			CounterSource: "conntrack", Confidence: "exact", SamplingRate: 1,
			AttributionSource: "unknown", AttributionReason: "gateway conntrack has no process ownership",
		}
		if previous := c.previous[flowID]; previous != nil {
			observation.BytesSourceToDestination = counterDelta(flow.tuple.bytes, previous.TotalBytesSourceToDestination)
			observation.BytesDestinationToSource = counterDelta(flow.reply.bytes, previous.TotalBytesDestinationToSource)
			observation.PacketsSourceToDestination = counterDelta(flow.tuple.packets, previous.PacketsSourceToDestination)
			observation.PacketsDestinationToSource = counterDelta(flow.reply.packets, previous.PacketsDestinationToSource)
			elapsed := time.Duration(observation.ObservedAtUnixNanos - previous.ObservedAtUnixNanos)
			observation.RateBytesSourceToDestination = ratePerSecond(observation.BytesSourceToDestination, elapsed)
			observation.RateBytesDestinationToSource = ratePerSecond(observation.BytesDestinationToSource, elapsed)
		}
		current[flowID] = observation
		observations = append(observations, observation)
	}
	for flowID, previous := range c.previous {
		if _, exists := current[flowID]; exists {
			continue
		}
		closed := proto.Clone(previous).(*networkobserverv1.FlowObservation)
		closed.Id = fmt.Sprintf("%s:%d", flowID, now.UnixNano())
		closed.ObservedAtUnixNanos = now.UnixNano()
		closed.State = "CLOSED"
		closed.Active = false
		closed.BytesSourceToDestination = 0
		closed.BytesDestinationToSource = 0
		closed.PacketsSourceToDestination = 0
		closed.PacketsDestinationToSource = 0
		closed.RateBytesSourceToDestination = 0
		closed.RateBytesDestinationToSource = 0
		observations = append(observations, closed)
	}
	c.previous = current
	observations = append(observations, c.externalObservations(now)...)
	status, detail := "supported", fmt.Sprintf("read %d conntrack flows from %s", len(current), filepath.Clean(path))
	if malformed > 0 {
		status = "degraded"
		detail = fmt.Sprintf("read %d conntrack flows from %s; skipped %d malformed line(s)", len(current), filepath.Clean(path), malformed)
	}
	capabilities := gatewayCapabilities(status, detail, len(current), c.currentReceiverStatuses())
	if natTuples > 0 {
		capabilities[len(capabilities)-1] = capability("gateway_nat_tuple", "supported", fmt.Sprintf("parsed original/reply tuples; %d active flow(s) expose NAT translation", natFlows))
	} else {
		capabilities[len(capabilities)-1] = capability("gateway_nat_tuple", "unavailable", "no complete original/reply tuple was exposed by conntrack")
	}
	for _, item := range capabilities {
		item.CheckedAtUnixNanos = now.UnixNano()
	}
	return observations, capabilities, nil
}

func (c *gatewayCollector) currentReceiverStatuses() map[string]gatewayReceiverStatus {
	statuses := make(map[string]gatewayReceiverStatus, len(c.receiverStatuses))
	for kind, status := range c.receiverStatuses {
		statuses[kind] = status
	}
	for _, receiver := range c.receivers {
		statuses[receiver.kind] = receiver.status()
	}
	return statuses
}

func (c *gatewayCollector) Close() error {
	c.closeOnce.Do(func() {
		for _, receiver := range c.receivers {
			receiver.close()
		}
	})
	return nil
}

func (c *gatewayCollector) externalObservations(now time.Time) []*networkobserverv1.FlowObservation {
	latest := make(map[string]externalFlow)
	for _, receiver := range c.receivers {
		for _, flow := range receiver.drain() {
			key := externalFlowID(flow)
			latest[key] = flow
		}
	}
	observations := make([]*networkobserverv1.FlowObservation, 0, len(latest)+len(c.externalState))
	for flowID, flow := range latest {
		state := c.externalState[flowID]
		bytesDelta := flow.bytes
		packetsDelta := flow.packets
		if flow.cumulative {
			bytesDelta = counterDelta(flow.bytes, state.lastExporterByte)
			packetsDelta = counterDelta(flow.packets, state.lastExporterPkts)
			state.lastExporterByte = flow.bytes
			state.lastExporterPkts = flow.packets
			state.bytes += bytesDelta
			state.packets += packetsDelta
		} else {
			state.bytes += flow.bytes
			state.packets += flow.packets
		}
		previousAt := state.observedAt
		state.observedAt = now
		state.lastSeen = now
		observation := &networkobserverv1.FlowObservation{
			Id: fmt.Sprintf("%s:%d", flowID, now.UnixNano()), FlowId: flowID, ObserverId: c.observerID,
			ObserverType: "gateway", LocalIsSource: false, ObservedAtUnixNanos: now.UnixNano(),
			NetworkNamespace: gatewayNetworkNamespace(), Protocol: flow.protocol, State: "ACTIVE",
			Interface: flow.interfaceName,
			SourceIp:  flow.sourceIP.String(), SourcePort: uint32(flow.sourcePort), DestinationIp: flow.destinationIP.String(), DestinationPort: uint32(flow.destinationPort), Active: true,
			TotalBytesSourceToDestination: state.bytes, TotalBytesDestinationToSource: 0, PacketsSourceToDestination: state.packets,
			SamplingRate: normalizedSamplingRate(flow.samplingRate), CounterSource: flow.counterSource, Confidence: normalizedConfidence(flow.confidence, flow.samplingRate),
			AttributionSource: "unknown", AttributionReason: "gateway flow export has no process ownership",
		}
		observation.BytesSourceToDestination = bytesDelta
		if !previousAt.IsZero() {
			elapsed := time.Duration(observation.ObservedAtUnixNanos - previousAt.UnixNano())
			observation.RateBytesSourceToDestination = ratePerSecond(observation.BytesSourceToDestination, elapsed)
		}
		state.observation = observation
		c.externalState[flowID] = state
		observations = append(observations, observation)
	}
	for flowID, state := range c.externalState {
		if _, updated := latest[flowID]; updated || state.lastSeen.IsZero() || now.Sub(state.lastSeen) < externalFlowIdleTimeout {
			continue
		}
		if state.observation != nil {
			closed := proto.Clone(state.observation).(*networkobserverv1.FlowObservation)
			closed.Id = fmt.Sprintf("%s:%d", flowID, now.UnixNano())
			closed.ObservedAtUnixNanos = now.UnixNano()
			closed.State = "CLOSED"
			closed.Active = false
			closed.BytesSourceToDestination = 0
			closed.BytesDestinationToSource = 0
			closed.PacketsSourceToDestination = 0
			closed.PacketsDestinationToSource = 0
			closed.RateBytesSourceToDestination = 0
			closed.RateBytesDestinationToSource = 0
			observations = append(observations, closed)
		}
		delete(c.externalState, flowID)
	}
	return observations
}

func normalizedSamplingRate(rate uint32) uint32 {
	if rate == 0 {
		return 1
	}
	return rate
}

func normalizedConfidence(confidence string, rate uint32) string {
	if confidence != "" {
		return confidence
	}
	return confidenceForSampling(normalizedSamplingRate(rate))
}

func externalFlowID(flow externalFlow) string {
	return fmt.Sprintf("%s|%s|%s|%d|%s|%d|%s", flow.source, flow.protocol, flow.sourceIP, flow.sourcePort, flow.destinationIP, flow.destinationPort, flow.interfaceName)
}

func (c *gatewayCollector) closeConntrack(now time.Time) []*networkobserverv1.FlowObservation {
	observations := make([]*networkobserverv1.FlowObservation, 0, len(c.previous))
	for flowID, previous := range c.previous {
		closed := proto.Clone(previous).(*networkobserverv1.FlowObservation)
		closed.Id = fmt.Sprintf("%s:%d", flowID, now.UnixNano())
		closed.ObservedAtUnixNanos = now.UnixNano()
		closed.State = "CLOSED"
		closed.Active = false
		closed.BytesSourceToDestination = 0
		closed.BytesDestinationToSource = 0
		closed.PacketsSourceToDestination = 0
		closed.PacketsDestinationToSource = 0
		closed.RateBytesSourceToDestination = 0
		closed.RateBytesDestinationToSource = 0
		observations = append(observations, closed)
	}
	c.previous = make(map[string]*networkobserverv1.FlowObservation)
	return observations
}

func gatewayCapabilities(status, detail string, flowCount int, receivers map[string]gatewayReceiverStatus) []*networkobserverv1.Capability {
	now := time.Now().UTC().UnixNano()
	items := []*networkobserverv1.Capability{
		capability("gateway_conntrack", status, detail),
		capability("gateway_ebpf", "unavailable", "Gateway eBPF flow counters are not attached by this build"),
		capability("netflow", receivers["netflow"].status, receivers["netflow"].detail),
		capability("ipfix", receivers["ipfix"].status, receivers["ipfix"].detail),
		capability("sflow", receivers["sflow"].status, receivers["sflow"].detail),
		capability("gateway_nat_tuple", "unavailable", "conntrack NAT tuple export is not available for this kernel output"),
	}
	if status == "supported" {
		items[0].Detail = fmt.Sprintf("%s; %d active flow(s)", detail, flowCount)
	}
	for _, item := range items {
		item.CheckedAtUnixNanos = now
	}
	return items
}

func tupleHasCompletePorts(tuple conntrackTuple) bool {
	return tuple.source != nil && tuple.destination != nil && tuple.hasSourcePort && tuple.hasDestPort
}

func hasNATTranslation(flow conntrackFlow) bool {
	return natSourceIP(flow) != "" || natSourcePort(flow) != 0 || natDestinationIP(flow) != "" || natDestinationPort(flow) != 0
}

func natSourceIP(flow conntrackFlow) string {
	if tupleHasCompletePorts(flow.reply) && !flow.reply.destination.Equal(flow.tuple.source) {
		return flow.reply.destination.String()
	}
	return ""
}

func natSourcePort(flow conntrackFlow) uint32 {
	if tupleHasCompletePorts(flow.reply) && flow.reply.destPort != flow.tuple.sourcePort {
		return uint32(flow.reply.destPort)
	}
	return 0
}

func natDestinationIP(flow conntrackFlow) string {
	if tupleHasCompletePorts(flow.reply) && !flow.reply.source.Equal(flow.tuple.destination) {
		return flow.reply.source.String()
	}
	return ""
}

func natDestinationPort(flow conntrackFlow) uint32 {
	if tupleHasCompletePorts(flow.reply) && flow.reply.sourcePort != flow.tuple.destPort {
		return uint32(flow.reply.sourcePort)
	}
	return 0
}

func parseConntrackLine(line string) (conntrackFlow, error) {
	fields := strings.Fields(line)
	if len(fields) < 6 {
		return conntrackFlow{}, fmt.Errorf("conntrack line has too few fields")
	}
	protocol := strings.ToLower(fields[2])
	if protocol != "tcp" && protocol != "udp" {
		return conntrackFlow{}, fmt.Errorf("unsupported protocol %q", protocol)
	}
	flow := conntrackFlow{protocol: protocol, state: "ACTIVE"}
	if len(fields) > 5 && !strings.Contains(fields[5], "=") {
		flow.state = fields[5]
	}
	var current *conntrackTuple
	var tuples []conntrackTuple
	for _, field := range fields[5:] {
		parts := strings.SplitN(field, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, value := parts[0], parts[1]
		switch key {
		case "src":
			if current != nil {
				tuples = append(tuples, *current)
			}
			ip := net.ParseIP(value)
			if ip == nil {
				return conntrackFlow{}, fmt.Errorf("invalid source IP %q", value)
			}
			current = &conntrackTuple{source: ip}
		case "dst":
			if current == nil {
				return conntrackFlow{}, fmt.Errorf("destination before source")
			}
			current.destination = net.ParseIP(value)
			if current.destination == nil {
				return conntrackFlow{}, fmt.Errorf("invalid destination IP %q", value)
			}
		case "sport", "dport":
			if current == nil {
				return conntrackFlow{}, fmt.Errorf("port before source")
			}
			port, err := strconv.ParseUint(value, 10, 16)
			if err != nil {
				return conntrackFlow{}, fmt.Errorf("invalid %s %q", key, value)
			}
			if key == "sport" {
				current.sourcePort = uint16(port)
				current.hasSourcePort = true
			} else {
				current.destPort = uint16(port)
				current.hasDestPort = true
			}
		case "packets", "bytes":
			if current == nil {
				return conntrackFlow{}, fmt.Errorf("counter before source")
			}
			counter, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return conntrackFlow{}, fmt.Errorf("invalid %s counter %q", key, value)
			}
			if key == "packets" {
				current.packets = counter
			} else {
				current.bytes = counter
			}
		case "zone":
			flow.zone = value
		}
	}
	if current != nil {
		tuples = append(tuples, *current)
	}
	if len(tuples) == 0 || tuples[0].destination == nil || !tuples[0].hasSourcePort || !tuples[0].hasDestPort {
		return conntrackFlow{}, fmt.Errorf("conntrack line has no complete original tuple")
	}
	flow.tuple = tuples[0]
	if len(tuples) > 1 {
		flow.reply = tuples[1]
	}
	return flow, nil
}

func conntrackFlowID(flow conntrackFlow) string {
	key := fmt.Sprintf("%s|%s|%d|%s|%d|%s", flow.protocol, flow.tuple.source, flow.tuple.sourcePort, flow.tuple.destination, flow.tuple.destPort, flow.zone)
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:16])
}

func counterDelta(current, previous uint64) uint64 {
	if current < previous {
		return 0
	}
	return current - previous
}

func gatewayNetworkNamespace() string {
	namespace, _ := os.Readlink("/proc/self/ns/net")
	return namespace
}
