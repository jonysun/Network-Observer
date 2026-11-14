package config

import (
	"errors"
	"os"
	"strconv"
)

type Config struct {
	HTTPAddr             string
	GRPCAddr             string
	GRPCInsecureAddr     string
	PostgresDSN          string
	ClickHouseDSN        string
	TLSCertFile          string
	TLSKeyFile           string
	AdminUsername        string
	AdminPassword        string
	EnrollmentToken      string
	SummaryRetentionDays uint32
	IPEnrichmentASNMMDB  string
	IPEnrichmentCityMMDB string
}

func Load() (Config, error) {
	retention, err := strconv.ParseUint(env("SUMMARY_RETENTION_DAYS", "90"), 10, 32)
	if err != nil || retention == 0 {
		return Config{}, errors.New("SUMMARY_RETENTION_DAYS must be a positive integer")
	}
	cfg := Config{
		HTTPAddr:             env("HTTP_ADDR", "0.0.0.0:8080"),
		GRPCAddr:             env("GRPC_ADDR", "0.0.0.0:8443"),
		GRPCInsecureAddr:     os.Getenv("GRPC_INSECURE_ADDR"),
		PostgresDSN:          os.Getenv("POSTGRES_DSN"),
		ClickHouseDSN:        os.Getenv("CLICKHOUSE_DSN"),
		TLSCertFile:          os.Getenv("TLS_CERT_FILE"),
		TLSKeyFile:           os.Getenv("TLS_KEY_FILE"),
		AdminUsername:        env("ADMIN_USERNAME", "admin"),
		AdminPassword:        os.Getenv("ADMIN_PASSWORD"),
		EnrollmentToken:      os.Getenv("ENROLLMENT_TOKEN"),
		SummaryRetentionDays: uint32(retention),
		IPEnrichmentASNMMDB:  os.Getenv("IP_ENRICHMENT_ASN_MMDB"),
		IPEnrichmentCityMMDB: os.Getenv("IP_ENRICHMENT_CITY_MMDB"),
	}
	if cfg.PostgresDSN == "" || cfg.ClickHouseDSN == "" {
		return Config{}, errors.New("POSTGRES_DSN and CLICKHOUSE_DSN are required")
	}
	if cfg.AdminPassword == "" {
		return Config{}, errors.New("ADMIN_PASSWORD is required")
	}
	if cfg.TLSCertFile == "" || cfg.TLSKeyFile == "" {
		return Config{}, errors.New("TLS_CERT_FILE and TLS_KEY_FILE are required")
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
