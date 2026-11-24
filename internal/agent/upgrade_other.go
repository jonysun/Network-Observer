//go:build !linux

package agent

import (
	"context"
	"errors"

	pb "github.com/network-observer/network-observer/gen/networkobserver/v1"
)

func agentUpgradeCapability() *pb.Capability {
	return &pb.Capability{Name: "agent_upgrade", Status: "unsupported", Detail: "remote Agent upgrade is supported only on native Linux systemd installs"}
}

func performAgentUpgrade(_ context.Context, _ Config, _ *pb.AgentUpgradeRequest, _ func(string, string) error) error {
	return errors.New("remote Agent upgrade is unsupported on this platform")
}
