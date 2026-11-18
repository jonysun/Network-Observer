package agent

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	pb "github.com/network-observer/network-observer/gen/networkobserver/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

var agentVersion = "dev"

type Registration struct {
	ServerAddress           string
	TLSServerName           string
	CACertFile              string
	SecurityMode            bool
	Token                   string
	DeviceName              string
	ObserverType            string
	ConfigPath              string
	WALPath                 string
	WALMaxBytes             int64
	VolatileWAL             bool
	SnapshotIntervalSeconds int
}

func Register(ctx context.Context, request Registration) error {
	if request.Token == "" || request.ConfigPath == "" {
		return errors.New("enrollment token and config path are required")
	}
	if openwrtLiteBuild && request.ObserverType != "" && request.ObserverType != "gateway" {
		return errors.New("OpenWrt Gateway Lite only supports gateway registration")
	}
	if _, err := os.Stat(request.ConfigPath); err == nil {
		return errors.New("agent config already exists; use the existing identity or move it before registering another device")
	} else if !os.IsNotExist(err) {
		return err
	}
	caPath := ""
	if request.CACertFile != "" {
		pathValue, err := filepath.Abs(request.CACertFile)
		if err != nil {
			return err
		}
		caPath = pathValue
	}
	if request.SecurityMode && caPath == "" {
		return errors.New("CA certificate is required when security mode is enabled")
	}
	walPath, err := filepath.Abs(request.WALPath)
	if err != nil {
		return err
	}
	tlsServerName := effectiveTLSServerName(request.ServerAddress, request.TLSServerName)
	config := Config{ServerAddress: request.ServerAddress, TLSServerName: tlsServerName, CACertFile: caPath, SecurityMode: request.SecurityMode,
		EnrollmentToken: request.Token, DeviceID: "pending", DeviceName: request.DeviceName,
		ObserverType: request.ObserverType, WALPath: walPath, WALMaxBytes: request.WALMaxBytes,
		VolatileWAL: request.VolatileWAL, SnapshotIntervalSeconds: request.SnapshotIntervalSeconds}
	if config.ObserverType == "" {
		config.ObserverType = "endpoint"
	}
	if config.WALMaxBytes == 0 {
		config.WALMaxBytes = DefaultWALMaxBytes
	}
	if config.SnapshotIntervalSeconds == 0 {
		config.SnapshotIntervalSeconds = 1
	}
	if err := config.Validate(); err != nil {
		return err
	}
	connection, err := dial(ctx, config.ServerAddress, config.TLSServerName, config.CACertFile, config.SecurityMode)
	if err != nil {
		if !config.SecurityMode {
			return fmt.Errorf("connect to Center %s (security_mode=false, plaintext gRPC; use the Center plaintext listener, default port 8445): %w", config.ServerAddress, err)
		}
		return fmt.Errorf("connect to Center %s (security_mode=true, TLS server name %q): %w", config.ServerAddress, config.TLSServerName, err)
	}
	defer connection.Close()
	stream, err := pb.NewAgentServiceClient(connection).Connect(ctx)
	if err != nil {
		return fmt.Errorf("open registration stream: %w", err)
	}
	defer stream.CloseSend()
	if err := stream.Send(&pb.AgentMessage{Payload: &pb.AgentMessage_Register{Register: &pb.RegisterRequest{
		DeviceName: config.DeviceName, ObserverType: config.ObserverType, Os: runtime.GOOS,
		Architecture: runtime.GOARCH, AgentVersion: agentVersion, EnrollmentToken: request.Token,
	}}}); err != nil {
		return fmt.Errorf("send registration: %w", err)
	}
	response, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("receive registration: %w", err)
	}
	registered := response.GetRegistered()
	if registered == nil || registered.DeviceId == "" {
		return errors.New("invalid registration response")
	}
	config.DeviceID = registered.DeviceId
	if registered.DeviceCredential == "" {
		return fmt.Errorf("device %s registered but center did not issue a device credential; revoke it before retrying", registered.DeviceId)
	}
	config.EnrollmentToken = ""
	config.DeviceCredential = registered.DeviceCredential
	if err := config.Validate(); err != nil {
		return fmt.Errorf("device %s registered but issued config is invalid; revoke it before retrying: %w", registered.DeviceId, err)
	}
	if err := SaveConfig(request.ConfigPath, config); err != nil {
		return fmt.Errorf("device %s registered but local config could not be saved; revoke it before retrying: %w", registered.DeviceId, err)
	}
	return nil
}

func dial(ctx context.Context, address, tlsServerName, caFile string, securityMode bool) (*grpc.ClientConn, error) {
	if !securityMode {
		dialContext, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return grpc.DialContext(dialContext, address,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithBlock(), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(4<<20)),
		)
	}
	tlsServerName = effectiveTLSServerName(address, tlsServerName)
	certificates, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("load agent CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificates) {
		return nil, errors.New("CA file contains no PEM certificates")
	}
	transport := grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: roots, ServerName: tlsServerName, MinVersion: tls.VersionTLS13}))
	dialContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return grpc.DialContext(dialContext, address,
		transport,
		grpc.WithBlock(), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(4<<20)),
	)
}

// effectiveTLSServerName keeps older/generated install commands compatible:
// when --tls-server-name was omitted, use the host portion of host:port. This
// preserves certificate verification for both DNS names and IP SANs and also
// records the effective value in the registered agent config.
func effectiveTLSServerName(address, explicit string) string {
	if name := strings.TrimSpace(explicit); name != "" {
		return name
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return ""
	}
	return host
}

func capabilityDigest(capabilities []*pb.Capability) [32]byte {
	type stableCapability struct{ Name, Status, Detail string }
	stable := make([]stableCapability, 0, len(capabilities))
	for _, item := range capabilities {
		if item != nil {
			stable = append(stable, stableCapability{item.Name, item.Status, item.Detail})
		}
	}
	encoded, _ := json.Marshal(stable)
	return sha256.Sum256(encoded)
}
