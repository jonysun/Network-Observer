package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"time"

	networkobserverv1 "github.com/network-observer/network-observer/gen/networkobserver/v1"
	"github.com/network-observer/network-observer/internal/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

func (s *Server) ServeGRPC(ctx context.Context) error {
	certificate, err := tls.LoadX509KeyPair(s.config.TLSCertFile, s.config.TLSKeyFile)
	if err != nil {
		return fmt.Errorf("load agent TLS certificate: %w", err)
	}
	secureListener, err := net.Listen("tcp", s.config.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen for secure agents: %w", err)
	}
	secureServer := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS13,
	})))
	networkobserverv1.RegisterAgentServiceServer(secureServer, s)

	servers := []*grpc.Server{secureServer}
	listeners := []net.Listener{secureListener}
	if strings.TrimSpace(s.config.GRPCInsecureAddr) != "" {
		insecureListener, listenErr := net.Listen("tcp", s.config.GRPCInsecureAddr)
		if listenErr != nil {
			secureListener.Close()
			return fmt.Errorf("listen for insecure agents: %w", listenErr)
		}
		insecureServer := grpc.NewServer()
		networkobserverv1.RegisterAgentServiceServer(insecureServer, s)
		servers = append(servers, insecureServer)
		listeners = append(listeners, insecureListener)
	}

	serveResult := make(chan error, len(servers))
	for i := range servers {
		server, listener := servers[i], listeners[i]
		go func() { serveResult <- server.Serve(listener) }()
	}
	select {
	case <-ctx.Done():
		for _, server := range servers {
			server.GracefulStop()
		}
		for _, listener := range listeners {
			_ = listener.Close()
		}
		return nil
	case err := <-serveResult:
		for _, server := range servers {
			server.GracefulStop()
		}
		for _, listener := range listeners {
			_ = listener.Close()
		}
		if err == grpc.ErrServerStopped {
			return nil
		}
		return err
	}
}

func (s *Server) Connect(stream grpc.BidiStreamingServer[networkobserverv1.AgentMessage, networkobserverv1.ServerMessage]) error {
	s.metrics.connected.Inc()
	defer s.metrics.connected.Dec()
	var deviceID string
	var updates <-chan *networkobserverv1.ConfigUpdate
	var upgradeUpdates <-chan *networkobserverv1.AgentUpgradeRequest
	var sentRevision int64
	defer func() {
		if deviceID != "" {
			s.markDeviceDisconnected(deviceID)
		}
	}()
	recv := make(chan *networkobserverv1.AgentMessage)
	recvError := make(chan error, 1)
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	go func() {
		for {
			message, err := stream.Recv()
			if err != nil {
				recvError <- err
				return
			}
			select {
			case recv <- message:
			case <-ctx.Done():
				return
			}
		}
	}()
	for {
		var message *networkobserverv1.AgentMessage
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-recvError:
			return err
		case upgrade := <-upgradeUpdates:
			if upgrade != nil {
				if err := stream.Send(&networkobserverv1.ServerMessage{Payload: &networkobserverv1.ServerMessage_UpgradeRequest{UpgradeRequest: upgrade}}); err != nil {
					return err
				}
			}
			continue
		case config := <-updates:
			if config.ConfigRevision > sentRevision {
				if err := stream.Send(&networkobserverv1.ServerMessage{Payload: &networkobserverv1.ServerMessage_Config{Config: config}}); err != nil {
					return err
				}
				sentRevision = config.ConfigRevision
			}
			continue
		case message = <-recv:
		}
		switch payload := message.Payload.(type) {
		case *networkobserverv1.AgentMessage_Register:
			if deviceID != "" {
				return status.Error(codes.InvalidArgument, "register must be the first message")
			}
			response, err := s.registerAgent(stream.Context(), payload.Register)
			if err != nil {
				return err
			}
			deviceID = response.DeviceId
			s.clearRealtimeDevice(deviceID)
			s.markDeviceConnected(deviceID)
			s.broadcastDeviceStatus(deviceID, nil)
			if err := stream.Send(&networkobserverv1.ServerMessage{Payload: &networkobserverv1.ServerMessage_Registered{Registered: response}}); err != nil {
				return err
			}
		case *networkobserverv1.AgentMessage_Authenticate:
			if deviceID != "" {
				return status.Error(codes.InvalidArgument, "authenticate must be the first message")
			}
			authConfig, err := s.store.AuthenticateDevice(stream.Context(), payload.Authenticate.DeviceId, payload.Authenticate.DeviceCredential)
			if err != nil && s.validateEnrollmentToken(payload.Authenticate.DeviceCredential) {
				authConfig, err = s.store.ActiveDeviceConfig(stream.Context(), payload.Authenticate.DeviceId)
			}
			if err != nil {
				return status.Error(codes.Unauthenticated, "invalid agent authentication")
			}
			deviceID = payload.Authenticate.DeviceId
			s.clearRealtimeDevice(deviceID)
			s.markDeviceConnected(deviceID)
			if err := s.store.TouchDevice(stream.Context(), deviceID); err != nil {
				return status.Error(codes.Unauthenticated, "device credential revoked")
			}
			if payload.Authenticate.AcceptsConfigUpdates {
				session := s.registerAgentSession(deviceID)
				defer s.unregisterAgentSession(deviceID, session)
				updates = session.updates
				upgradeUpdates = session.upgrades
				// Re-read after subscribing so an update concurrent with authentication
				// is observed either in the snapshot or in the queue.
				authConfig, err = s.store.ActiveDeviceConfig(stream.Context(), deviceID)
				if err != nil {
					return status.Error(codes.Unavailable, "could not read agent configuration")
				}
			}
			sentRevision = authConfig.ConfigRevision
			s.broadcastDeviceStatus(deviceID, nil)
			if err := stream.Send(&networkobserverv1.ServerMessage{Payload: &networkobserverv1.ServerMessage_Authenticated{
				Authenticated: &networkobserverv1.Authenticated{NextSequence: authConfig.LastSequence + 1, HeartbeatIntervalSeconds: 15, SupportsObservationCoverage: true, DnsMetadataEnabled: authConfig.DNSMetadataEnabled, ConfigRevision: authConfig.ConfigRevision},
			}}); err != nil {
				return err
			}
		case *networkobserverv1.AgentMessage_UpgradeStatus:
			if deviceID == "" {
				return status.Error(codes.Unauthenticated, "register or authenticate first")
			}
			s.setAgentUpgradeStatus(deviceID, payload.UpgradeStatus)
		case *networkobserverv1.AgentMessage_Heartbeat:
			if deviceID == "" {
				return status.Error(codes.Unauthenticated, "register or authenticate first")
			}
			if err := s.store.TouchDevice(stream.Context(), deviceID); err != nil {
				return status.Error(codes.Unauthenticated, "device credential revoked")
			}
			s.broadcastDeviceStatus(deviceID, nil)
		case *networkobserverv1.AgentMessage_Capabilities:
			if deviceID == "" {
				return status.Error(codes.Unauthenticated, "register or authenticate first")
			}
			capabilities := convertCapabilities(payload.Capabilities.Capabilities)
			filtered := make([]domain.Capability, 0, len(capabilities))
			var reportedVersion string
			for _, capability := range capabilities {
				if capability.Name == "agent_version" {
					reportedVersion = strings.TrimSpace(capability.Detail)
					if err := s.store.UpdateAgentVersion(stream.Context(), deviceID, capability.Detail); err != nil {
						return status.Error(codes.Unauthenticated, "device credential revoked")
					}
					continue
				}
				filtered = append(filtered, domain.NormalizeCapability(capability))
			}
			capabilities = filtered
			if err := s.store.UpdateCapabilities(stream.Context(), deviceID, capabilities); err != nil {
				return status.Error(codes.Unauthenticated, "device credential revoked")
			}
			s.confirmAgentUpgrade(deviceID, reportedVersion)
			s.broadcastDeviceStatus(deviceID, capabilities, reportedVersion)
		case *networkobserverv1.AgentMessage_Observations:
			if deviceID == "" {
				return status.Error(codes.Unauthenticated, "register or authenticate first")
			}
			observations, err := convertObservations(deviceID, payload.Observations.Observations)
			if err != nil {
				s.metrics.errors.Inc()
				return status.Error(codes.InvalidArgument, err.Error())
			}
			for i := range observations {
				s.manager.Apply(&observations[i])
			}
			acknowledged, err := s.store.IngestObservations(stream.Context(), deviceID, observations)
			if err != nil {
				s.metrics.errors.Inc()
				if err.Error() == "device credential revoked" {
					return status.Error(codes.Unauthenticated, err.Error())
				}
				return status.Error(codes.Unavailable, err.Error())
			}
			s.metrics.ingested.Add(float64(len(observations)))
			for i := range observations {
				s.store.EnrichObservation(&observations[i])
			}
			// Durable batches contain minute aggregates and replayed observations.
			// Only fresh non-history rows refresh the short-lived live cache; every
			// row above is still persisted with full attribution and counters.
			fresh := make([]domain.FlowObservation, 0, len(observations))
			at := time.Now().UTC()
			for _, observation := range observations {
				if realtimeEligibleForDurableBatch(observation, at) {
					fresh = append(fresh, observation)
				}
			}
			s.updateRealtime(deviceID, fresh)
			for _, observation := range fresh {
				s.hub.broadcast("connection.updated", observation)
			}
			if err := stream.Send(&networkobserverv1.ServerMessage{Payload: &networkobserverv1.ServerMessage_Ack{Ack: &networkobserverv1.BatchAck{AcknowledgedSequence: acknowledged}}}); err != nil {
				return err
			}
		case *networkobserverv1.AgentMessage_Realtime:
			if deviceID == "" {
				return status.Error(codes.Unauthenticated, "register or authenticate first")
			}
			if payload.Realtime == nil {
				return status.Error(codes.InvalidArgument, "realtime batch is required")
			}
			observations, err := convertObservations(deviceID, payload.Realtime.Observations)
			if err != nil {
				s.metrics.errors.Inc()
				return status.Error(codes.InvalidArgument, err.Error())
			}
			for i := range observations {
				s.manager.Apply(&observations[i])
				s.store.EnrichObservation(&observations[i])
			}
			s.updateRealtime(deviceID, observations)
			for _, observation := range observations {
				s.hub.broadcast("connection.updated", observation)
			}
		case *networkobserverv1.AgentMessage_DnsMetadata:
			if deviceID == "" {
				return status.Error(codes.Unauthenticated, "register or authenticate first")
			}
			items, err := convertDNSObservations(deviceID, payload.DnsMetadata.GetObservations())
			if err != nil {
				return status.Error(codes.InvalidArgument, err.Error())
			}
			if err := s.store.IngestDNSObservations(stream.Context(), items); err != nil {
				return status.Error(codes.Unavailable, err.Error())
			}
		case *networkobserverv1.AgentMessage_NeighborSnapshot:
			if deviceID == "" {
				return status.Error(codes.Unauthenticated, "register or authenticate first")
			}
			observedAt := time.Now().UTC()
			if payload.NeighborSnapshot.GetObservedAtUnixNanos() != 0 {
				candidate := time.Unix(0, payload.NeighborSnapshot.GetObservedAtUnixNanos()).UTC()
				if candidate.After(time.Now().Add(-2*time.Minute)) && candidate.Before(time.Now().Add(time.Minute)) {
					observedAt = candidate
				}
			}
			items, err := convertNeighborObservations(deviceID, payload.NeighborSnapshot.GetObservations(), observedAt)
			if err != nil {
				return status.Error(codes.InvalidArgument, err.Error())
			}
			if err := s.store.IngestNeighborSnapshot(stream.Context(), items); err != nil {
				return status.Error(codes.Unavailable, err.Error())
			}
			dockerNetworks, err := convertDockerNetworks(deviceID, payload.NeighborSnapshot.GetDockerNetworks(), observedAt)
			if err != nil {
				return status.Error(codes.InvalidArgument, err.Error())
			}
			if err := s.store.IngestDockerNetworkSnapshot(stream.Context(), deviceID, dockerNetworks, payload.NeighborSnapshot.GetDockerTopologyComplete(), observedAt); err != nil {
				return status.Error(codes.Unavailable, err.Error())
			}
			if err := s.captureLANTopologySnapshot(stream.Context()); err != nil {
				s.logger.Warn("capture LAN topology snapshot", "observer_device_id", deviceID, "error", err)
			}
			s.hub.broadcast("lan.updated", map[string]any{"observer_device_id": deviceID, "observed_at": observedAt})
		default:
			return status.Error(codes.InvalidArgument, "message payload is required")
		}
	}
}

func convertDockerNetworks(ownerID string, input []*networkobserverv1.DockerNetwork, observedAt time.Time) ([]domain.DockerNetwork, error) {
	if len(input) > 1000 {
		return nil, fmt.Errorf("Docker network snapshot exceeds 1000 networks")
	}
	result := make([]domain.DockerNetwork, 0, len(input))
	seen := make(map[string]struct{}, len(input))
	for _, item := range input {
		if item == nil || strings.TrimSpace(item.GetId()) == "" || len(item.GetId()) > 256 {
			return nil, fmt.Errorf("Docker network requires an ID of at most 256 characters")
		}
		if _, exists := seen[item.GetId()]; exists {
			return nil, fmt.Errorf("Docker network snapshot contains duplicate ID %q", item.GetId())
		}
		seen[item.GetId()] = struct{}{}
		network := domain.DockerNetwork{
			OwnerAgentID: ownerID, ID: item.GetId(), Name: item.GetName(), Driver: item.GetDriver(),
			Scope: item.GetScope(), LastSeen: observedAt, SubnetCIDRs: make([]string, 0, len(item.GetSubnetCidrs())),
			Containers: make([]domain.DockerContainer, 0, len(item.GetContainers())),
		}
		if len(network.Name) > 255 || len(network.Driver) > 64 || len(network.Scope) > 64 || len(item.GetContainers()) > 5000 {
			return nil, fmt.Errorf("Docker network %q exceeds supported metadata limits", item.GetId())
		}
		for _, subnet := range item.GetSubnetCidrs() {
			if _, _, err := net.ParseCIDR(subnet); err != nil {
				return nil, fmt.Errorf("Docker network %q has invalid subnet %q", item.GetId(), subnet)
			}
			network.SubnetCIDRs = append(network.SubnetCIDRs, subnet)
		}
		for _, container := range item.GetContainers() {
			if container == nil || len(container.GetId()) > 128 || len(container.GetName()) > 255 {
				return nil, fmt.Errorf("Docker network %q has invalid container metadata", item.GetId())
			}
			network.Containers = append(network.Containers, domain.DockerContainer{
				ID: container.GetId(), Name: container.GetName(), IPv4Address: container.GetIpv4Address(),
				IPv6Address: container.GetIpv6Address(), MACAddress: strings.ToLower(container.GetMacAddress()),
			})
		}
		result = append(result, network)
	}
	return result, nil
}

func convertNeighborObservations(deviceID string, input []*networkobserverv1.NeighborObservation, observedAt time.Time) ([]domain.NeighborObservation, error) {
	result := make([]domain.NeighborObservation, 0, len(input))
	for _, item := range input {
		if item == nil || strings.TrimSpace(item.NetworkId) == "" || len(item.NetworkId) > 256 || net.ParseIP(item.Ip) == nil {
			return nil, fmt.Errorf("neighbor observation requires network_id and a valid IP address")
		}
		networkPrefix := strings.TrimSpace(item.NetworkPrefix)
		if networkPrefix != "" {
			_, prefix, err := net.ParseCIDR(networkPrefix)
			if err != nil {
				return nil, fmt.Errorf("neighbor observation has an invalid network prefix")
			}
			networkPrefix = prefix.String()
		}
		mac := ""
		if strings.TrimSpace(item.Mac) != "" {
			address, err := net.ParseMAC(item.Mac)
			if err != nil {
				return nil, fmt.Errorf("neighbor observation has an invalid MAC address")
			}
			mac = strings.ToLower(address.String())
		}
		result = append(result, domain.NeighborObservation{
			ObserverDeviceID: deviceID, NetworkID: strings.TrimSpace(item.NetworkId), NetworkPrefix: networkPrefix, IP: net.ParseIP(item.Ip).String(), MAC: mac,
			Interface: strings.TrimSpace(item.Interface), State: strings.TrimSpace(item.State),
			IsLocal: item.IsLocal, LastSeen: observedAt,
		})
	}
	return result, nil
}

func (s *Server) registerAgentSession(deviceID string) *agentSession {
	session := &agentSession{updates: make(chan *networkobserverv1.ConfigUpdate, 1), upgrades: make(chan *networkobserverv1.AgentUpgradeRequest, 1)}
	s.agentSessionMu.Lock()
	if s.agentSessions == nil {
		s.agentSessions = make(map[string]map[*agentSession]struct{})
	}
	if s.agentSessions[deviceID] == nil {
		s.agentSessions[deviceID] = make(map[*agentSession]struct{})
	}
	s.agentSessions[deviceID][session] = struct{}{}
	s.agentSessionMu.Unlock()
	return session
}

func (s *Server) unregisterAgentSession(deviceID string, session *agentSession) {
	s.agentSessionMu.Lock()
	delete(s.agentSessions[deviceID], session)
	if len(s.agentSessions[deviceID]) == 0 {
		delete(s.agentSessions, deviceID)
	}
	s.agentSessionMu.Unlock()
}

func (s *Server) queueAgentConfig(deviceID string, config *networkobserverv1.ConfigUpdate) {
	s.agentSessionMu.Lock()
	defer s.agentSessionMu.Unlock()
	for session := range s.agentSessions[deviceID] {
		if config.ConfigRevision <= session.revision {
			continue
		}
		session.revision = config.ConfigRevision
		// Only the latest desired state matters. Never block an HTTP request
		// on a slow or disconnected Agent stream.
		select {
		case <-session.updates:
		default:
		}
		session.updates <- config
	}
}
func (s *Server) markDeviceConnected(deviceID string) {
	s.deviceConnMu.Lock()
	if s.deviceConnections == nil {
		s.deviceConnections = make(map[string]int)
	}
	s.deviceConnections[deviceID]++
	s.deviceConnMu.Unlock()
}

func (s *Server) markDeviceDisconnected(deviceID string) {
	s.deviceConnMu.Lock()
	count := s.deviceConnections[deviceID]
	if count <= 1 {
		delete(s.deviceConnections, deviceID)
	} else {
		s.deviceConnections[deviceID] = count - 1
	}
	online := count > 1
	s.deviceConnMu.Unlock()
	if !online {
		s.clearRealtimeDevice(deviceID)
		s.hub.broadcast("device.updated", map[string]any{"id": deviceID, "online": false})
	}
}

func (s *Server) deviceConnected(deviceID string) bool {
	s.deviceConnMu.RLock()
	defer s.deviceConnMu.RUnlock()
	return s.deviceConnections[deviceID] > 0
}

func convertDNSObservations(deviceID string, input []*networkobserverv1.DNSObservation) ([]domain.DNSObservation, error) {
	result := make([]domain.DNSObservation, 0, len(input))
	for _, item := range input {
		if item == nil || item.Domain == "" || net.ParseIP(item.Ip) == nil || item.ObservedAtUnixNanos == 0 {
			return nil, fmt.Errorf("DNS observation requires domain, valid IP, and observed_at")
		}
		observed := time.Unix(0, item.ObservedAtUnixNanos).UTC()
		expires := time.Unix(0, item.ExpiresAtUnixNanos).UTC()
		if item.ExpiresAtUnixNanos == 0 {
			expires = observed.Add(time.Duration(item.Ttl) * time.Second)
		}
		if !expires.After(observed) {
			return nil, fmt.Errorf("DNS observation TTL must be positive")
		}
		result = append(result, domain.DNSObservation{DeviceID: deviceID, ObservedAt: observed, Interface: item.Interface, Domain: strings.TrimSuffix(strings.ToLower(item.Domain), "."), IP: item.Ip, TTL: item.Ttl, Source: item.Source, ExpiresAt: expires})
	}
	return result, nil
}

func (s *Server) registerAgent(ctx context.Context, request *networkobserverv1.RegisterRequest) (*networkobserverv1.RegisterResponse, error) {
	if request == nil || strings.TrimSpace(request.EnrollmentToken) == "" {
		return nil, status.Error(codes.Unauthenticated, "invalid enrollment token")
	}
	observerType := domain.ObserverType(request.ObserverType)
	if strings.TrimSpace(request.DeviceName) == "" || observerType != domain.ObserverEndpoint && observerType != domain.ObserverGateway {
		return nil, status.Error(codes.InvalidArgument, "device_name and valid observer_type are required")
	}
	credential, err := randomToken()
	if err != nil {
		return nil, status.Error(codes.Internal, "could not generate device credential")
	}
	id, dnsEnabled, revision, err := s.store.ConsumeEnrollmentTokenAndRegisterDevice(ctx, request.EnrollmentToken, request.DeviceName, observerType, request.Os, request.Architecture, request.AgentVersion, credential)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}
	return &networkobserverv1.RegisterResponse{DeviceId: id, DeviceCredential: credential, HeartbeatIntervalSeconds: 15, DnsMetadataEnabled: dnsEnabled, ConfigRevision: revision}, nil
}

func (s *Server) broadcastDeviceStatus(deviceID string, capabilities []domain.Capability, agentVersion ...string) {
	update := map[string]any{
		"id": deviceID, "online": true, "last_seen": time.Now().UTC(),
	}
	if capabilities != nil {
		update["capabilities"] = capabilities
	}
	if len(agentVersion) > 0 && agentVersion[0] != "" {
		update["agent_version"] = agentVersion[0]
	}
	s.hub.broadcast("device.updated", update)
}

func convertCapabilities(input []*networkobserverv1.Capability) []domain.Capability {
	result := make([]domain.Capability, 0, len(input))
	for _, capability := range input {
		if capability == nil {
			continue
		}
		checkedAt := time.Unix(0, capability.CheckedAtUnixNanos).UTC()
		if capability.CheckedAtUnixNanos == 0 {
			checkedAt = time.Now().UTC()
		}
		result = append(result, domain.NormalizeCapability(domain.Capability{Name: capability.Name, Status: capability.Status, Detail: capability.Detail, CheckedAt: checkedAt}))
	}
	return result
}

func convertObservations(deviceID string, input []*networkobserverv1.FlowObservation) ([]domain.FlowObservation, error) {
	result := make([]domain.FlowObservation, 0, len(input))
	for _, item := range input {
		if item == nil {
			return nil, fmt.Errorf("observation must not be null")
		}
		if item.DeviceId != "" && item.DeviceId != deviceID {
			return nil, fmt.Errorf("observation device does not match authenticated device")
		}
		if item.SourcePort > 65535 || item.DestinationPort > 65535 || item.NatSourcePort > 65535 || item.NatDestinationPort > 65535 {
			return nil, fmt.Errorf("observation contains an invalid port")
		}
		if item.SamplingRate > 1 && item.Confidence != "estimated" {
			return nil, fmt.Errorf("sampled observations must use estimated confidence")
		}
		observedAt := time.Unix(0, item.ObservedAtUnixNanos).UTC()
		if item.ObservedAtUnixNanos == 0 {
			return nil, fmt.Errorf("observed_at_unix_nanos is required")
		}
		var coverageStart time.Time
		if item.CoverageStartUnixNanos != 0 {
			coverageStart = time.Unix(0, item.CoverageStartUnixNanos).UTC()
			if coverageStart.After(observedAt) || observedAt.Sub(coverageStart) > 2*time.Minute {
				return nil, fmt.Errorf("invalid observation coverage interval")
			}
		}
		attributionSource := domain.AttributionSource(item.AttributionSource)
		if attributionSource == "" {
			attributionSource = domain.AttributionUnknown
		}
		result = append(result, domain.FlowObservation{
			ID: item.Id, FlowID: item.FlowId, DeviceID: deviceID, ObserverID: item.ObserverId,
			ObserverType: domain.ObserverType(item.ObserverType), LocalIsSource: item.LocalIsSource,
			Sequence: item.Sequence, ObservedAt: observedAt, CoverageStart: coverageStart, NetworkNamespace: item.NetworkNamespace, Interface: item.Interface,
			Protocol: domain.Protocol(item.Protocol), State: item.State, SourceIP: item.SourceIp, SourcePort: uint16(item.SourcePort),
			DestinationIP: item.DestinationIp, DestinationPort: uint16(item.DestinationPort), NATSourceIP: item.NatSourceIp,
			NATSourcePort: uint16(item.NatSourcePort), NATDestinationIP: item.NatDestinationIp,
			NATDestinationPort: uint16(item.NatDestinationPort), BytesSourceToDest: item.BytesSourceToDestination,
			BytesDestToSource: item.BytesDestinationToSource, TotalBytesSourceToDest: item.TotalBytesSourceToDestination,
			TotalBytesDestToSource: item.TotalBytesDestinationToSource, RateBytesSourceToDest: item.RateBytesSourceToDestination,
			RateBytesDestToSource: item.RateBytesDestinationToSource, PacketsSourceToDest: item.PacketsSourceToDestination,
			PacketsDestToSource: item.PacketsDestinationToSource, PID: item.Pid, ProcessName: item.ProcessName,
			ContainerID: item.ContainerId, ContainerName: item.ContainerName, ServiceName: item.ServiceName,
			AttributionSource: attributionSource, AttributionReason: item.AttributionReason, SamplingRate: item.SamplingRate,
			CounterSource: item.CounterSource, Confidence: item.Confidence, Active: item.Active,
			Domain: item.Domain, DomainSource: item.DomainSource, ASN: item.Asn, Organization: item.Organization,
			Country: item.Country, Region: item.Region, City: item.City, Latitude: item.Latitude, Longitude: item.Longitude,
			EnrichmentVersion: item.EnrichmentVersion, GapReason: item.GapReason,
			DNSMetadataOnly: item.DnsMetadataOnly, DNSIP: item.DnsIp, DNSTTL: item.DnsTtl,
		})
	}
	return result, nil
}

func (s *Server) queueAgentUpgrade(deviceID string, request *networkobserverv1.AgentUpgradeRequest) bool {
	s.agentSessionMu.Lock()
	defer s.agentSessionMu.Unlock()
	queued := false
	for session := range s.agentSessions[deviceID] {
		select {
		case session.upgrades <- request:
			queued = true
		default:
			// A full queue means there is already an upgrade command in flight.
		}
		if queued {
			// A device may briefly have multiple streams during reconnect. Only one
			// process may replace the shared Agent executable.
			return true
		}
	}
	return false
}
