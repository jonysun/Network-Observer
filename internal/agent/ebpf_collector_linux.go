//go:build linux

package agent

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"sync"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	networkobserverv1 "github.com/network-observer/network-observer/gen/networkobserver/v1"
)

type bpfFlowKey struct {
	Family          uint8
	Protocol        uint8
	Direction       uint8
	Pad             uint8
	Source          [16]byte
	Destination     [16]byte
	SourcePort      uint16
	DestinationPort uint16
}

type bpfFlowValue struct{ Bytes, Packets, LastSeenNS uint64 }

type bpfCounterDelta struct {
	Bytes, Packets           uint64
	TotalBytes, TotalPackets uint64
}

type ebpfCollector struct {
	mu         sync.Mutex
	collection *ebpf.Collection
	counts     *ebpf.Map
	links      []link.Link
	previous   map[bpfFlowKey]bpfFlowValue
	last       map[bpfFlowKey]bpfCounterDelta
}

func newEBPFCollector(config Config) (*ebpfCollector, error) {
	objectPath := config.EbpfObjectPath
	if objectPath == "" {
		objectPath = "/usr/local/lib/network-observer/flow_counter.o"
	}
	cgroupPath := config.EbpfCgroupPath
	if cgroupPath == "" {
		cgroupPath = "/sys/fs/cgroup"
	}
	if _, err := os.Stat(objectPath); err != nil {
		return nil, fmt.Errorf("eBPF object: %w", err)
	}
	spec, err := ebpf.LoadCollectionSpec(objectPath)
	if err != nil {
		return nil, fmt.Errorf("load eBPF object: %w", err)
	}
	collection, err := ebpf.NewCollection(spec)
	if err != nil {
		return nil, fmt.Errorf("load eBPF programs: %w", err)
	}
	collector := &ebpfCollector{collection: collection, counts: collection.Maps["flow_counts"], previous: make(map[bpfFlowKey]bpfFlowValue), last: make(map[bpfFlowKey]bpfCounterDelta)}
	if collector.counts == nil {
		collector.Close()
		return nil, fmt.Errorf("eBPF object does not contain flow_counts map")
	}
	for _, item := range []struct {
		name   string
		attach ebpf.AttachType
	}{{"count_egress", ebpf.AttachCGroupInetEgress}, {"count_ingress", ebpf.AttachCGroupInetIngress}} {
		program := collection.Programs[item.name]
		if program == nil {
			collector.Close()
			return nil, fmt.Errorf("eBPF object is missing %s", item.name)
		}
		attached, err := link.AttachCgroup(link.CgroupOptions{Path: cgroupPath, Attach: item.attach, Program: program})
		if err != nil {
			collector.Close()
			return nil, fmt.Errorf("attach %s to %s: %w", item.name, cgroupPath, err)
		}
		collector.links = append(collector.links, attached)
	}
	return collector, nil
}

func (c *ebpfCollector) Snapshot() map[bpfFlowKey]bpfCounterDelta {
	c.mu.Lock()
	defer c.mu.Unlock()
	current := make(map[bpfFlowKey]bpfCounterDelta)
	var key bpfFlowKey
	var value bpfFlowValue
	iterator := c.counts.Iterate()
	for iterator.Next(&key, &value) {
		previous := c.previous[key]
		current[key] = bpfCounterDelta{Bytes: value.Bytes - previous.Bytes, Packets: value.Packets - previous.Packets, TotalBytes: value.Bytes, TotalPackets: value.Packets}
		c.previous[key] = value
	}
	// Keep only entries observed during this cycle; stale map entries are not
	// reported as active traffic and will naturally be replaced by the LRU map.
	c.last = current
	return current
}

func (c *ebpfCollector) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var first error
	for _, item := range c.links {
		if err := item.Close(); err != nil && first == nil {
			first = err
		}
	}
	if c.collection != nil {
		c.collection.Close()
	}
	return first
}

func (c *ebpfCollector) DeltasFor(observation *networkobserverv1.FlowObservation) (bpfCounterDelta, bpfCounterDelta) {
	return c.last[bpfKey(observation.SourceIp, observation.DestinationIp, uint16(observation.SourcePort), uint16(observation.DestinationPort), observation.Protocol, 1)], c.last[bpfKey(observation.DestinationIp, observation.SourceIp, uint16(observation.DestinationPort), uint16(observation.SourcePort), observation.Protocol, 0)]
}

func bpfKey(source, destination string, sourcePort, destinationPort uint16, protocol string, direction uint8) bpfFlowKey {
	key := bpfFlowKey{Direction: direction, SourcePort: sourcePort, DestinationPort: destinationPort}
	if protocol == "tcp" {
		key.Protocol = 6
	} else {
		key.Protocol = 17
	}
	parsedSource, parsedDestination := net.ParseIP(source), net.ParseIP(destination)
	if parsedSource != nil && parsedSource.To4() != nil {
		key.Family = 4
		copy(key.Source[12:], parsedSource.To4())
		copy(key.Destination[12:], parsedDestination.To4())
	} else {
		key.Family = 6
		copy(key.Source[:], parsedSource.To16())
		copy(key.Destination[:], parsedDestination.To16())
	}
	return key
}

func encodeBPFKey(key bpfFlowKey) []byte {
	result := make([]byte, 40)
	result[0], result[1], result[2], result[3] = key.Family, key.Protocol, key.Direction, key.Pad
	copy(result[4:20], key.Source[:])
	copy(result[20:36], key.Destination[:])
	binary.LittleEndian.PutUint16(result[36:38], key.SourcePort)
	binary.LittleEndian.PutUint16(result[38:40], key.DestinationPort)
	return result
}
