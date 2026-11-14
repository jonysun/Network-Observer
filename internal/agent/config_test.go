package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func validAgentConfig(t *testing.T) Config {
	t.Helper()
	ca := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(ca, []byte("test certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Config{
		ServerAddress: "observer.example.lan:8444", CACertFile: ca,
		SecurityMode: true, EnrollmentToken: "persistent-token", DeviceID: "device-1", DeviceName: "lab-1",
		ObserverType: "endpoint", WALPath: filepath.Join(t.TempDir(), "agent.wal"), WALMaxBytes: 1,
	}
}

func TestConfigValidateTLSModeRequiresCAAndToken(t *testing.T) {
	config := validAgentConfig(t)
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	config.EnrollmentToken = ""
	if err := config.Validate(); err == nil {
		t.Fatal("expected all modes to require the persistent token")
	}
}

func TestConfigValidateInsecureModeDoesNotRequireCA(t *testing.T) {
	config := validAgentConfig(t)
	config.SecurityMode = false
	config.CACertFile = ""
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	config.EnrollmentToken = ""
	if err := config.Validate(); err == nil {
		t.Fatal("expected insecure mode to require the persistent token")
	}
}
