package agent

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	pb "github.com/network-observer/network-observer/gen/networkobserver/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const agentVersion = "0.2.0"

type Registration struct {
	ServerAddress string
	CACertFile    string
	Token         string
	DeviceName    string
	ObserverType  string
	ConfigPath    string
	WALPath       string
	WALMaxBytes   int64
}

func Register(ctx context.Context, request Registration) error {
	if request.Token == "" || request.ConfigPath == "" {
		return errors.New("enrollment token and config path are required")
	}
	if _, err := os.Stat(request.ConfigPath); err == nil {
		return errors.New("agent config already exists; use the existing identity or move it before registering another device")
	} else if !os.IsNotExist(err) {
		return err
	}
	caPath, err := filepath.Abs(request.CACertFile)
	if err != nil {
		return err
	}
	walPath, err := filepath.Abs(request.WALPath)
	if err != nil {
		return err
	}
	config := Config{ServerAddress: request.ServerAddress, CACertFile: caPath,
		DeviceID: "pending", DeviceCredential: "pending", DeviceName: request.DeviceName,
		ObserverType: request.ObserverType, WALPath: walPath, WALMaxBytes: request.WALMaxBytes}
	if config.ObserverType == "" {
		config.ObserverType = "endpoint"
	}
	if config.WALMaxBytes == 0 {
		config.WALMaxBytes = DefaultWALMaxBytes
	}
	if err := config.Validate(); err != nil {
		return err
	}
	connection, err := dial(ctx, config.ServerAddress, config.CACertFile)
	if err != nil {
		return err
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
	if registered == nil || registered.DeviceId == "" || registered.DeviceCredential == "" {
		return errors.New("invalid registration response")
	}
	config.DeviceID, config.DeviceCredential = registered.DeviceId, registered.DeviceCredential
	return SaveConfig(request.ConfigPath, config)
}

func dial(ctx context.Context, address, caFile string) (*grpc.ClientConn, error) {
	certificates, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("load agent CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificates) {
		return nil, errors.New("CA file contains no PEM certificates")
	}
	dialContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return grpc.DialContext(dialContext, address,
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13})),
		grpc.WithBlock(), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(4<<20)),
	)
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
