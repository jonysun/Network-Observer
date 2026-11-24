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

type endpointCollector struct {
	observerID string
	previous   map[string]*networkobserverv1.FlowObservation
	config     Config
	dnsCapture
	closeOnce  sync.Once
	ebpfStatus string
	ebpfDetail string
	ebpf       *ebpfCollector
}

type processMetadata struct {
	pid         uint32
	name        string
	containerID string
}

func newPlatformCollector() Collector {
	return newPlatformCollectorWithConfig(Config{})
}

func newPlatformCollectorWithConfig(config Config) Collector {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "linux-endpoint"
	}
	mode := config.EbpfMode
	if mode == "" {
		mode = "auto"
	}
	status, detail := "disabled", "disabled by agent configuration"
	if mode != "off" {
		status, detail = probeEBPF()
	}
	collector := &endpointCollector{observerID: hostname + ":endpoint", previous: make(map[string]*networkobserverv1.FlowObservation), config: config, ebpfStatus: status, ebpfDetail: detail}
	if mode != "off" && status == "unavailable" {
		if attached, err := newEBPFCollector(config); err == nil {
			collector.ebpf, collector.ebpfStatus, collector.ebpfDetail = attached, "supported", "CO-RE cgroup skb flow counter attached"
		} else {
			collector.ebpfDetail = err.Error()
		}
	}
	_ = collector.SetDNSMetadataEnabled(config.DNSMetadataEnabled)
	return collector
}

func (c *endpointCollector) Snapshot(ctx context.Context) ([]*networkobserverv1.FlowObservation, []*networkobserverv1.Capability, error) {
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	default:
	}
	now := time.Now().UTC()
	if c.ebpf != nil {
		c.ebpf.Snapshot()
	}
	dnsObservations, dnsCapability := c.dnsCapture.snapshot(c.observerID, now)
	all := make([]procSocket, 0, 512)
	files := []struct {
		name     string
		family   string
		protocol string
	}{
		{name: "tcp", family: "ipv4", protocol: "tcp"},
		{name: "tcp6", family: "ipv6", protocol: "tcp"},
		{name: "udp", family: "ipv4", protocol: "udp"},
		{name: "udp6", family: "ipv6", protocol: "udp"},
	}
	var tableErrors []string
	for _, source := range files {
		path := filepath.Join("/proc/net", source.name)
		file, err := os.Open(path)
		if err != nil {
			tableErrors = append(tableErrors, source.name+": "+err.Error())
			continue
		}
		sockets, parseErr := parseProcSocketTable(file, source.family, source.protocol)
		closeErr := file.Close()
		if parseErr != nil {
			return nil, nil, fmt.Errorf("read %s: %w", path, parseErr)
		}
		if closeErr != nil {
			return nil, nil, closeErr
		}
		all = append(all, sockets...)
	}
	if len(all) == 0 && len(tableErrors) == len(files) {
		return nil, nil, fmt.Errorf("no proc network socket tables are readable: %s", strings.Join(tableErrors, "; "))
	}

	inodes := make(map[uint64]struct{}, len(all))
	for _, socket := range all {
		if socket.Inode != 0 {
			inodes[socket.Inode] = struct{}{}
		}
	}
	processes := make(map[uint64]processMetadata)
	inaccessibleProcesses := 0
	if !c.config.DisableProcessAttribution {
		processes, inaccessibleProcesses = indexProcesses("/proc", inodes)
	}
	dockerMetadata := enrichDockerMetadata(ctx, c.config.DockerSocket, processes)
	interfaces := localInterfaces()
	namespace, _ := os.Readlink("/proc/self/ns/net")
	diag := make(map[uint64]diagStats)
	diagErr := error(nil)
	if !c.config.DisableSocketDiag {
		diag, diagErr = collectTCPSocketDiag(ctx)
	}
	diagCounters := 0
	for _, stats := range diag {
		if stats.HasCounters {
			diagCounters++
		}
	}
	current := make(map[string]*networkobserverv1.FlowObservation, len(all))
	observations := make([]*networkobserverv1.FlowObservation, 0, len(all))
	observations = append(observations, dnsObservations...)
	for _, socket := range all {
		metadata := processes[socket.Inode]
		diagSocket, hasDiag := diag[socket.Inode]
		flowID := socketFlowID(socket)
		localIP, remoteIP := socket.LocalIP.String(), socket.RemoteIP.String()
		interfaceNameForSocket := interfaces[localIP]
		if hasDiag {
			if name := interfaceName(diagSocket.InterfaceIndex); name != "" {
				interfaceNameForSocket = name
			}
		}
		observation := &networkobserverv1.FlowObservation{
			Id: fmt.Sprintf("%s:%d", flowID, now.UnixNano()), FlowId: flowID, ObserverId: c.observerID,
			ObserverType: "endpoint", LocalIsSource: true, ObservedAtUnixNanos: now.UnixNano(),
			NetworkNamespace: namespace, Interface: interfaceNameForSocket, Protocol: socket.Protocol,
			State: socket.State, SourceIp: localIP, SourcePort: uint32(socket.LocalPort),
			DestinationIp: remoteIP, DestinationPort: uint32(socket.RemotePort), Active: true,
			CounterSource: "unavailable", Confidence: "unavailable", SamplingRate: 1,
		}
		if socket.Protocol == "tcp" && hasDiag && diagSocket.HasCounters {
			observation.TotalBytesSourceToDestination = diagSocket.BytesSent
			observation.TotalBytesDestinationToSource = diagSocket.BytesReceived
			observation.CounterSource = "socket_diag_tcp_info"
			observation.Confidence = "exact"
			if previous := c.previous[flowID]; previous != nil {
				elapsed := time.Duration(observation.ObservedAtUnixNanos - previous.ObservedAtUnixNanos)
				observation.BytesSourceToDestination = tcpCounterDelta(observation.TotalBytesSourceToDestination, previous.TotalBytesSourceToDestination)
				observation.BytesDestinationToSource = tcpCounterDelta(observation.TotalBytesDestinationToSource, previous.TotalBytesDestinationToSource)
				observation.RateBytesSourceToDestination = ratePerSecond(observation.BytesSourceToDestination, elapsed)
				observation.RateBytesDestinationToSource = ratePerSecond(observation.BytesDestinationToSource, elapsed)
			}
		}
		if c.ebpf != nil {
			forward, reverse := c.ebpf.DeltasFor(observation)
			if forward.TotalBytes != 0 || reverse.TotalBytes != 0 {
				observation.BytesSourceToDestination, observation.BytesDestinationToSource = forward.Bytes, reverse.Bytes
				observation.TotalBytesSourceToDestination, observation.TotalBytesDestinationToSource = forward.TotalBytes, reverse.TotalBytes
				observation.PacketsSourceToDestination, observation.PacketsDestinationToSource = forward.Packets, reverse.Packets
				observation.CounterSource, observation.Confidence = "ebpf_cgroup_skb", "exact"
				if previous := c.previous[flowID]; previous != nil {
					elapsed := time.Duration(observation.ObservedAtUnixNanos - previous.ObservedAtUnixNanos)
					observation.RateBytesSourceToDestination = ratePerSecond(observation.BytesSourceToDestination, elapsed)
					observation.RateBytesDestinationToSource = ratePerSecond(observation.BytesDestinationToSource, elapsed)
				}
			}
		}
		if hasDiag && diagSocket.State != "" {
			observation.State = diagSocket.State
		}
		if metadata.pid != 0 {
			observation.Pid = metadata.pid
			observation.ProcessName = metadata.name
			observation.ContainerId = metadata.containerID
			if container := dockerMetadata.containers[metadata.containerID]; container.name != "" || container.service != "" {
				observation.ContainerName = container.name
				observation.ServiceName = container.service
			}
			observation.AttributionSource = "auto"
			observation.AttributionReason = "procfs socket inode ownership"
		} else {
			observation.AttributionSource = "unknown"
			observation.AttributionReason = "socket owner is not visible in /proc"
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

	capabilities := c.endpointCapabilities(inaccessibleProcesses, len(tableErrors) > 0, diagErr, len(diag), diagCounters, dockerMetadata)
	ebpfStatus, ebpfDetail := c.ebpfStatus, c.ebpfDetail
	mode := c.config.EbpfMode
	if mode == "" {
		mode = "auto"
	}
	if mode == "on" {
		return nil, capabilities, fmt.Errorf("eBPF mode is on but the endpoint CO-RE collector is unavailable: %s", ebpfDetail)
	}
	capabilities = replaceCapability(capabilities, capability("ebpf_byte_counters", ebpfStatus, ebpfDetail))
	capabilities = append(capabilities, dnsCapability)
	return observations, capabilities, nil
}

func replaceCapability(items []*networkobserverv1.Capability, replacement *networkobserverv1.Capability) []*networkobserverv1.Capability {
	for i, item := range items {
		if item != nil && item.Name == replacement.Name {
			items[i] = replacement
			return items
		}
	}
	return append(items, replacement)
}

func (c *endpointCollector) endpointCapabilities(inaccessibleProcesses int, partialTables bool, diagErr error, diagSockets, diagCounters int, dockerMetadata dockerMetadataResult) []*networkobserverv1.Capability {
	now := time.Now().UTC().UnixNano()
	dockerStatus := dockerMetadata.status
	if dockerStatus == "" {
		dockerStatus = "unavailable"
	}
	result := []*networkobserverv1.Capability{
		capability("procfs_socket_inventory", "supported", "IPv4/IPv6 TCP/UDP sockets read from /proc/net"),
		capability("ebpf_byte_counters", "unavailable", "No per-flow byte counters are collected; reported byte totals are zero"),
		capability("docker_metadata", dockerStatus, dockerMetadata.detail),
	}
	if c.config.DisableSocketDiag {
		result = append(result, capability("socket_diag", "disabled", "disabled by agent configuration"))
	} else if diagErr != nil {
		result = append(result, capability("socket_diag", "degraded", diagErr.Error()))
	} else {
		result = append(result, capability("socket_diag", "supported", fmt.Sprintf("TCP Socket Diag inventory available for %d sockets", diagSockets)))
	}
	if diagErr == nil && diagCounters > 0 {
		result = append(result, capability("tcp_byte_counters", "supported", fmt.Sprintf("TCP_INFO counters available for %d sockets", diagCounters)))
	} else {
		result = append(result, capability("tcp_byte_counters", "unavailable", "TCP Socket Diag counters are unavailable; TCP byte totals remain zero"))
	}
	if c.config.DisableProcessAttribution {
		result = append(result, capability("process_attribution", "disabled", "disabled by agent configuration"))
	}
	if _, err := os.Stat("/sys/kernel/btf/vmlinux"); err == nil {
		result = append(result, capability("kernel_btf", "available", "BTF metadata is present; eBPF attachment is attempted when ebpf_mode is auto or on"))
	} else {
		result = append(result, capability("kernel_btf", "unsupported", "/sys/kernel/btf/vmlinux is not available"))
	}
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err == nil {
		result = append(result, capability("cgroup_v2", "available", "cgroup v2 hierarchy is present"))
	} else {
		result = append(result, capability("cgroup_v2", "unavailable", "cgroup v2 hierarchy is not mounted"))
	}
	if !c.config.DisableProcessAttribution {
		if inaccessibleProcesses == 0 && os.Geteuid() == 0 {
			result = append(result, capability("process_attribution_status", "supported", "socket inode ownership is visible to the agent"))
		} else {
			result = append(result, capability("process_attribution_status", "degraded", fmt.Sprintf("%d process descriptor tables could not be inspected; run with permission to read host /proc", inaccessibleProcesses)))
		}
	}
	if partialTables {
		result = append(result, capability("procfs_ipv6_or_udp_tables", "degraded", "one or more /proc/net socket tables were unavailable"))
	}
	for _, item := range result {
		item.CheckedAtUnixNanos = now
	}
	return result
}

func (c *endpointCollector) Close() error {
	var first error
	c.closeOnce.Do(func() {
		if c.ebpf != nil {
			first = c.ebpf.Close()
		}
		if err := c.dnsCapture.close(); first == nil {
			first = err
		}
	})
	return first
}

func (c *endpointCollector) ValidateRequired() error {
	if c.config.EbpfMode != "on" {
		return nil
	}
	if c.ebpfStatus != "supported" {
		return fmt.Errorf("eBPF mode on: %s", c.ebpfDetail)
	}
	return nil
}

func capability(name, status, detail string) *networkobserverv1.Capability {
	return &networkobserverv1.Capability{Name: name, Status: status, Detail: detail}
}

func indexProcesses(procRoot string, wanted map[uint64]struct{}) (map[uint64]processMetadata, int) {
	result := make(map[uint64]processMetadata, len(wanted))
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return result, 1
	}
	inaccessible := 0
	for _, process := range entries {
		if _, err := strconv.ParseUint(process.Name(), 10, 32); err != nil {
			continue
		}
		fds, err := os.ReadDir(filepath.Join(procRoot, process.Name(), "fd"))
		if err != nil {
			if !os.IsNotExist(err) && !os.IsPermission(err) {
				inaccessible++
			} else if os.IsPermission(err) {
				inaccessible++
			}
			continue
		}
		var owned []uint64
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(procRoot, process.Name(), "fd", fd.Name()))
			if err != nil || !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
				continue
			}
			inode, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]"), 10, 64)
			if err != nil {
				continue
			}
			if _, ok := wanted[inode]; ok {
				owned = append(owned, inode)
			}
		}
		if len(owned) == 0 {
			continue
		}
		pid, _ := strconv.ParseUint(process.Name(), 10, 32)
		nameBytes, _ := os.ReadFile(filepath.Join(procRoot, process.Name(), "comm"))
		name := strings.TrimSpace(string(nameBytes))
		if name == "" {
			name = "unknown"
		}
		containerID := readContainerID(filepath.Join(procRoot, process.Name(), "cgroup"))
		metadata := processMetadata{pid: uint32(pid), name: name, containerID: containerID}
		for _, inode := range owned {
			if _, exists := result[inode]; !exists {
				result[inode] = metadata
			}
		}
	}
	return result, inaccessible
}

func readContainerID(path string) string {
	contents, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(contents), "\n") {
		for _, component := range strings.Split(strings.TrimSpace(line), "/") {
			component = strings.TrimSuffix(component, ".scope")
			component = strings.TrimPrefix(component, "docker-")
			if len(component) == 64 {
				if _, err := hex.DecodeString(component); err == nil {
					return component
				}
			}
		}
	}
	return ""
}

func localInterfaces() map[string]string {
	result := make(map[string]string)
	interfaces, err := net.Interfaces()
	if err != nil {
		return result
	}
	for _, iface := range interfaces {
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err == nil {
				result[ip.String()] = iface.Name
			}
		}
	}
	return result
}

func socketFlowID(socket procSocket) string {
	key := fmt.Sprintf("%s|%s|%d|%s|%d|%d", socket.Protocol, socket.LocalIP, socket.LocalPort,
		socket.RemoteIP, socket.RemotePort, socket.Inode)
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:16])
}
