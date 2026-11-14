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
		DeviceID: "device-1", DeviceCredential: "credential-1", DeviceName: "lab-1",
		ObserverType: "endpoint", WALPath: filepath.Join(t.TempDir(), "agent.wal"), WALMaxBytes: 1,
	}
}

func TestConfigValidateTLSModeRequiresCAAndCredential(t *testing.T) {
	config := validAgentConfig(t)
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	config.DeviceCredential = ""
	if err := config.Validate(); err == nil {
		t.Fatal("expected TLS mode to require a device credential")
	}
}

func TestConfigValidateTokenModeStillRequiresCA(t *testing.T) {
	config := validAgentConfig(t)
	config.TokenMode = true
	config.DeviceCredential = ""
	config.EnrollmentToken = "persistent-token"
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	config.CACertFile = ""
	if err := config.Validate(); err == nil {
		t.Fatal("expected token mode to require the CA for TLS")
	}
	config.CACertFile = validAgentConfig(t).CACertFile
	config.EnrollmentToken = ""
	if err := config.Validate(); err == nil {
		t.Fatal("expected token mode to require the persistent token")
	}
}
