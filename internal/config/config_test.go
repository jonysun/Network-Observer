package config

import (
	"os"
	"testing"
)

func TestLoadRequiresAgentTLSFilesInTokenMode(t *testing.T) {
	values := map[string]string{
		"POSTGRES_DSN":           "postgres://observer:pass@localhost/observer",
		"CLICKHOUSE_DSN":         "clickhouse://localhost/observer",
		"ADMIN_PASSWORD":         "admin-pass",
		"ENROLLMENT_TOKEN":       "persistent-token",
		"AGENT_TOKEN_MODE":       "true",
		"TLS_CERT_FILE":          "/tmp/server.crt",
		"TLS_KEY_FILE":           "/tmp/server.key",
		"SUMMARY_RETENTION_DAYS": "90",
	}
	previous := make(map[string]string)
	for key, value := range values {
		previous[key] = os.Getenv(key)
		if err := os.Setenv(key, value); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for key, value := range previous {
			_ = os.Setenv(key, value)
		}
	})

	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !config.AgentTokenMode {
		t.Fatal("expected token mode to be enabled")
	}
	_ = os.Setenv("TLS_CERT_FILE", "")
	_ = os.Setenv("TLS_KEY_FILE", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected token mode to require TLS certificate and key")
	}
}

func TestLoadRequiresAgentTLSFilesInDefaultMode(t *testing.T) {
	previousMode := os.Getenv("AGENT_TOKEN_MODE")
	previousCert := os.Getenv("TLS_CERT_FILE")
	previousKey := os.Getenv("TLS_KEY_FILE")
	t.Cleanup(func() {
		_ = os.Setenv("AGENT_TOKEN_MODE", previousMode)
		_ = os.Setenv("TLS_CERT_FILE", previousCert)
		_ = os.Setenv("TLS_KEY_FILE", previousKey)
	})
	_ = os.Setenv("AGENT_TOKEN_MODE", "false")
	_ = os.Setenv("TLS_CERT_FILE", "")
	_ = os.Setenv("TLS_KEY_FILE", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected default mode to require TLS certificate files")
	}
}
