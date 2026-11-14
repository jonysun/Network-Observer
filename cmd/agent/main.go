package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
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
	registerCommand := flag.NewFlagSet("register", flag.ContinueOnError)
	registerServer := registerCommand.String("server", "", "central gRPC address, for example observer.example.lan:8444")
	registerCA := registerCommand.String("ca", "", "PEM CA certificate used to verify the center")
	registerToken := registerCommand.String("token", "", "one-time enrollment token")
	registerName := registerCommand.String("name", "", "device display name")
	registerType := registerCommand.String("type", "endpoint", "observer type: endpoint or gateway")
	registerConfig := registerCommand.String("config", "/etc/network-observer/agent.json", "agent identity/configuration path")
	registerWAL := registerCommand.String("wal", "/var/lib/network-observer/agent.wal", "local write-ahead log path")
	registerWALBytes := registerCommand.Int64("wal-max-bytes", agent.DefaultWALMaxBytes, "maximum local WAL size")

	if len(os.Args) > 1 && os.Args[1] == "register" {
		if err := registerCommand.Parse(os.Args[2:]); err != nil {
			return err
		}
		return agent.Register(context.Background(), agent.Registration{
			ServerAddress: *registerServer, CACertFile: *registerCA, Token: *registerToken,
			DeviceName: *registerName, ObserverType: *registerType, ConfigPath: *registerConfig,
			WALPath: *registerWAL, WALMaxBytes: *registerWALBytes,
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
