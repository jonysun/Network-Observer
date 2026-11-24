package server

import (
	"testing"
	"time"

	pb "github.com/network-observer/network-observer/gen/networkobserver/v1"
	"github.com/network-observer/network-observer/internal/domain"
)

func TestAgentUpgradeStatusRequiresMatchingTask(t *testing.T) {
	server := &Server{upgrades: map[string]domain.AgentUpgrade{
		"device-1": {Supported: true, TargetVersion: "v1.2.3", State: "queued", TaskID: "task-1"},
	}}
	server.setAgentUpgradeStatus("device-1", &pb.AgentUpgradeStatus{TaskId: "stale-task", State: "failed", Message: "stale"})
	if got := server.upgrades["device-1"]; got.State != "queued" || got.Message != "" {
		t.Fatalf("stale task changed current upgrade: %+v", got)
	}
	server.setAgentUpgradeStatus("device-1", &pb.AgentUpgradeStatus{TaskId: "task-1", State: "verifying", Message: "checksum"})
	if got := server.upgrades["device-1"]; got.State != "verifying" || got.Message != "checksum" {
		t.Fatalf("matching task status not recorded: %+v", got)
	}
}

func TestAgentUpgradeConfirmationRequiresTargetVersionAndIsTerminal(t *testing.T) {
	oldNow := now
	defer func() { now = oldNow }()
	currentTime := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { currentTime = currentTime.Add(time.Second); return currentTime }
	server := &Server{upgrades: map[string]domain.AgentUpgrade{
		"device-1": {Supported: true, TargetVersion: "v1.2.3", State: "restarting", TaskID: "task-1"},
	}}
	server.confirmAgentUpgrade("device-1", "v1.2.2")
	if got := server.upgrades["device-1"]; got.State != "restarting" {
		t.Fatalf("wrong version confirmed upgrade: %+v", got)
	}
	server.confirmAgentUpgrade("device-1", "v1.2.3")
	firstUpdated := server.upgrades["device-1"].UpdatedAt
	if got := server.upgrades["device-1"]; got.State != "succeeded" {
		t.Fatalf("target version not confirmed: %+v", got)
	}
	server.confirmAgentUpgrade("device-1", "v1.2.3")
	if !server.upgrades["device-1"].UpdatedAt.Equal(firstUpdated) {
		t.Fatal("terminal success timestamp was rewritten by later reports")
	}
}
