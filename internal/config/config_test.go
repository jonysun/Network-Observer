package config

import (
	"os"
	"testing"
)

func TestLoadRequiresAgentTLSFiles(t *testing.T) {
	values := map[string]string{
		"POSTGRES_DSN":           "postgres://observer:pass@localhost/observer",
		"CLICKHOUSE_DSN":         "clickhouse://localhost/observer",
		"ADMIN_PASSWORD":         "admin-pass",
		"ENROLLMENT_TOKEN":       "persistent-token",
		"TLS_CERT_FILE":          "/tmp/server.crt",
		"TLS_KEY_FILE":           "/tmp/server.key",
		"GRPC_INSECURE_ADDR":     "",
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
	if config.GRPCInsecureAddr != "" {
		t.Fatal("expected insecure listener to be disabled by default")
	}
}

func TestLoadReadsOptionalInsecureListener(t *testing.T) {
	values := map[string]string{
		"POSTGRES_DSN": "postgres://observer:pass@localhost/observer", "CLICKHOUSE_DSN": "clickhouse://localhost/observer",
		"ADMIN_PASSWORD": "admin-pass", "ENROLLMENT_TOKEN": "persistent-token", "SUMMARY_RETENTION_DAYS": "90",
		"TLS_CERT_FILE": "/tmp/server.crt", "TLS_KEY_FILE": "/tmp/server.key",
		"GRPC_INSECURE_ADDR": "0.0.0.0:8445",
	}
	previous := make(map[string]string, len(values))
	for key, value := range values {
		previous[key] = os.Getenv(key)
		_ = os.Setenv(key, value)
	}
	t.Cleanup(func() {
		for key, value := range previous {
			_ = os.Setenv(key, value)
		}
	})
	if config, err := Load(); err != nil || config.GRPCInsecureAddr != "0.0.0.0:8445" {
		t.Fatalf("expected optional insecure listener address, got config=%+v err=%v", config, err)
	}
}
