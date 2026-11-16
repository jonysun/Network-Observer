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

func TestConfigPreservesTLSServerName(t *testing.T) {
	config := validAgentConfig(t)
	config.ServerAddress = "192.168.31.176:8444"
	config.TLSServerName = "observer.example.lan"

	path := filepath.Join(t.TempDir(), "agent.json")
	if err := SaveConfig(path, config); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TLSServerName != config.TLSServerName {
		t.Fatalf("TLS server name = %q, want %q", loaded.TLSServerName, config.TLSServerName)
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

func TestConfigValidateEbpfModeDefaultsAndRejectsUnknown(t *testing.T) {
	config := validAgentConfig(t)
	if config.EbpfMode != "" {
		t.Fatal("fixture should exercise the default")
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	config.EbpfMode = "sometimes"
	if err := config.Validate(); err == nil {
		t.Fatal("expected invalid eBPF mode")
	}
}
