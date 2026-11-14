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
	listener, err := net.Listen("tcp", s.config.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen for agents: %w", err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS13,
	})))
	networkobserverv1.RegisterAgentServiceServer(server, s)
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		server.GracefulStop()
		return nil
	case err := <-serveResult:
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
	for {
		message, err := stream.Recv()
		if err != nil {
			return err
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
			s.broadcastDeviceStatus(deviceID, nil)
			if err := stream.Send(&networkobserverv1.ServerMessage{Payload: &networkobserverv1.ServerMessage_Registered{Registered: response}}); err != nil {
				return err
			}
		case *networkobserverv1.AgentMessage_Authenticate:
			if deviceID != "" {
				return status.Error(codes.InvalidArgument, "authenticate must be the first message")
			}
			last, err := s.store.AuthenticateDevice(stream.Context(), payload.Authenticate.DeviceId, payload.Authenticate.DeviceCredential)
			if err != nil {
				return status.Error(codes.Unauthenticated, "invalid device credential")
			}
			deviceID = payload.Authenticate.DeviceId
			if err := s.store.TouchDevice(stream.Context(), deviceID); err != nil {
				return status.Error(codes.Unauthenticated, "device credential revoked")
			}
			s.broadcastDeviceStatus(deviceID, nil)
			if err := stream.Send(&networkobserverv1.ServerMessage{Payload: &networkobserverv1.ServerMessage_Authenticated{
				Authenticated: &networkobserverv1.Authenticated{NextSequence: last + 1, HeartbeatIntervalSeconds: 15},
			}}); err != nil {
				return err
			}
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
			if err := s.store.UpdateCapabilities(stream.Context(), deviceID, capabilities); err != nil {
				return status.Error(codes.Unauthenticated, "device credential revoked")
			}
			s.broadcastDeviceStatus(deviceID, capabilities)
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
			for _, observation := range observations {
				s.hub.broadcast("connection.updated", observation)
			}
			if err := stream.Send(&networkobserverv1.ServerMessage{Payload: &networkobserverv1.ServerMessage_Ack{Ack: &networkobserverv1.BatchAck{AcknowledgedSequence: acknowledged}}}); err != nil {
				return err
			}
		default:
			return status.Error(codes.InvalidArgument, "message payload is required")
		}
	}
}

func (s *Server) registerAgent(ctx context.Context, request *networkobserverv1.RegisterRequest) (*networkobserverv1.RegisterResponse, error) {
	if request == nil || !s.validateEnrollmentToken(request.EnrollmentToken) {
		return nil, status.Error(codes.Unauthenticated, "invalid enrollment token")
	}
	observerType := domain.ObserverType(request.ObserverType)
	if strings.TrimSpace(request.DeviceName) == "" || observerType != domain.ObserverEndpoint && observerType != domain.ObserverGateway {
		return nil, status.Error(codes.InvalidArgument, "device_name and valid observer_type are required")
	}
	credential, err := randomToken()
	if err != nil {
		return nil, status.Error(codes.Internal, "could not issue device credential")
	}
	id, err := s.store.RegisterDevice(ctx, request.DeviceName, observerType, request.Os, request.Architecture, request.AgentVersion, credential)
	if err != nil {
		return nil, status.Error(codes.Internal, "could not register device")
	}
	return &networkobserverv1.RegisterResponse{DeviceId: id, DeviceCredential: credential, HeartbeatIntervalSeconds: 15}, nil
}

func (s *Server) broadcastDeviceStatus(deviceID string, capabilities []domain.Capability) {
	update := map[string]any{
		"id": deviceID, "online": true, "last_seen": time.Now().UTC(),
	}
	if capabilities != nil {
		update["capabilities"] = capabilities
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
		result = append(result, domain.Capability{Name: capability.Name, Status: capability.Status, Detail: capability.Detail, CheckedAt: checkedAt})
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
		attributionSource := domain.AttributionSource(item.AttributionSource)
		if attributionSource == "" {
			attributionSource = domain.AttributionUnknown
		}
		result = append(result, domain.FlowObservation{
			ID: item.Id, FlowID: item.FlowId, DeviceID: deviceID, ObserverID: item.ObserverId,
			ObserverType: domain.ObserverType(item.ObserverType), LocalIsSource: item.LocalIsSource,
			Sequence: item.Sequence, ObservedAt: observedAt, NetworkNamespace: item.NetworkNamespace, Interface: item.Interface,
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
		})
	}
	return result, nil
}
