package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/network-observer/network-observer/internal/agent"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("network observer agent stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	if len(os.Args) > 1 && os.Args[1] == "verify-cert" {
		verifyCommand := flag.NewFlagSet("verify-cert", flag.ContinueOnError)
		certificatePath := verifyCommand.String("file", "", "PEM certificate file")
		expectedFingerprint := verifyCommand.String("fingerprint", "", "expected SHA-256 fingerprint")
		if err := verifyCommand.Parse(os.Args[2:]); err != nil {
			return err
		}
		if *certificatePath == "" || *expectedFingerprint == "" {
			return fmt.Errorf("verify-cert requires --file and --fingerprint")
		}
		data, err := os.ReadFile(*certificatePath)
		if err != nil {
			return err
		}
		block, _ := pem.Decode(data)
		if block == nil {
			return fmt.Errorf("certificate is not PEM encoded")
		}
		certs, err := x509.ParseCertificates(block.Bytes)
		if err != nil || len(certs) == 0 {
			return fmt.Errorf("parse certificate: %w", err)
		}
		digest := sha256.Sum256(certs[0].Raw)
		actual := hex.EncodeToString(digest[:])
		expected := ""
		for _, ch := range *expectedFingerprint {
			if ch != ':' && ch != ' ' {
				expected += string(ch)
			}
		}
		if !strings.EqualFold(actual, expected) {
			return fmt.Errorf("certificate fingerprint mismatch: got %s", actual)
		}
		fmt.Println(actual)
		return nil
	}
	registerCommand := flag.NewFlagSet("register", flag.ContinueOnError)
	registerServer := registerCommand.String("server", "", "central gRPC address, for example observer.example.lan:8444")
	registerTLSServerName := registerCommand.String("tls-server-name", "", "TLS certificate name when it differs from the server address")
	registerCA := registerCommand.String("ca", "", "PEM CA certificate used to verify the center")
	registerToken := registerCommand.String("token", "", "enrollment token")
	registerSecurityMode := registerCommand.Bool("security-mode", true, "use TLS encryption; point --server at the matching center listener")
	registerName := registerCommand.String("name", "", "device display name")
	registerType := registerCommand.String("type", "endpoint", "observer type: endpoint or gateway")
	registerConfig := registerCommand.String("config", "/etc/network-observer/agent.json", "agent identity/configuration path")
	registerWAL := registerCommand.String("wal", "/var/lib/network-observer/agent.wal", "local write-ahead log path")
	registerWALBytes := registerCommand.Int64("wal-max-bytes", agent.DefaultWALMaxBytes, "maximum local WAL size")
	registerVolatileWAL := registerCommand.Bool("volatile-wal", false, "allow an empty WAL after restart to resume at the Center checkpoint")
	registerSnapshotInterval := registerCommand.Int("snapshot-interval", 1, "collector snapshot interval in seconds (1-60)")

	if len(os.Args) > 1 && os.Args[1] == "register" {
		if err := registerCommand.Parse(os.Args[2:]); err != nil {
			return err
		}
		return agent.Register(context.Background(), agent.Registration{
			ServerAddress: *registerServer, TLSServerName: *registerTLSServerName, CACertFile: *registerCA, SecurityMode: *registerSecurityMode, Token: *registerToken,
			DeviceName: *registerName, ObserverType: *registerType, ConfigPath: *registerConfig,
			WALPath: *registerWAL, WALMaxBytes: *registerWALBytes, VolatileWAL: *registerVolatileWAL,
			SnapshotIntervalSeconds: *registerSnapshotInterval,
		})
	}
	if len(os.Args) > 1 && os.Args[1] == "snapshot" {
		snapshotCommand := flag.NewFlagSet("snapshot", flag.ContinueOnError)
		snapshotType := snapshotCommand.String("type", "endpoint", "observer type: endpoint or gateway")
		snapshotConntrack := snapshotCommand.String("conntrack", "", "gateway conntrack file, defaults to /proc/net/nf_conntrack")
		if err := snapshotCommand.Parse(os.Args[2:]); err != nil {
			return err
		}
		if *snapshotType != "endpoint" && *snapshotType != "gateway" {
			return fmt.Errorf("unsupported snapshot type %q; expected endpoint or gateway", *snapshotType)
		}
		collector := agent.NewCollector(agent.Config{ObserverType: *snapshotType, ConntrackPath: *snapshotConntrack})
		observations, capabilities, err := collector.Snapshot(context.Background())
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"observations": observations, "capabilities": capabilities})
	}
	runCommand := flag.NewFlagSet("run", flag.ContinueOnError)
	configPath := runCommand.String("config", "/etc/network-observer/agent.json", "agent identity/configuration path")
	if len(os.Args) > 1 && os.Args[1] != "run" {
		return fmt.Errorf("unknown command %q; use run or register", os.Args[1])
	}
	if len(os.Args) > 1 {
		if err := runCommand.Parse(os.Args[2:]); err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return agent.Run(ctx, *configPath, logger)
}
