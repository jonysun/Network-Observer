package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

const DefaultWALMaxBytes int64 = 256 << 20

type Config struct {
	ServerAddress             string `json:"server_address"`
	TLSServerName             string `json:"tls_server_name,omitempty"`
	CACertFile                string `json:"ca_cert_file"`
	SecurityMode              bool   `json:"security_mode"`
	EnrollmentToken           string `json:"enrollment_token"`
	DeviceCredential          string `json:"device_credential,omitempty"`
	DeviceID                  string `json:"device_id"`
	DeviceName                string `json:"device_name"`
	ObserverType              string `json:"observer_type"`
	WALPath                   string `json:"wal_path"`
	WALMaxBytes               int64  `json:"wal_max_bytes"`
	VolatileWAL               bool   `json:"volatile_wal,omitempty"`
	SnapshotIntervalSeconds   int    `json:"snapshot_interval_seconds,omitempty"`
	DockerSocket              string `json:"docker_socket,omitempty"`
	ConntrackPath             string `json:"conntrack_path,omitempty"`
	NetFlowListen             string `json:"netflow_listen,omitempty"`
	IPFIXListen               string `json:"ipfix_listen,omitempty"`
	SFlowListen               string `json:"sflow_listen,omitempty"`
	DisableSocketDiag         bool   `json:"disable_socket_diag,omitempty"`
	DisableConntrack          bool   `json:"disable_conntrack,omitempty"`
	DisableProcessAttribution bool   `json:"disable_process_attribution,omitempty"`
	EbpfMode                  string `json:"ebpf_mode,omitempty"`
	EbpfObjectPath            string `json:"ebpf_object_path,omitempty"`
	EbpfCgroupPath            string `json:"ebpf_cgroup_path,omitempty"`
	DNSMetadataEnabled        bool   `json:"dns_metadata_enabled,omitempty"`
	ConfigRevision            int64  `json:"config_revision,omitempty"`
}

func (c *Config) Validate() error {
	if c.ServerAddress == "" || c.DeviceID == "" || c.DeviceName == "" {
		return errors.New("server address, device ID, and device name are required")
	}
	if c.EnrollmentToken == "" && c.DeviceCredential == "" {
		return errors.New("device credential is required")
	}
	if c.SecurityMode && c.CACertFile == "" {
		return errors.New("CA certificate is required when security mode is enabled")
	}
	if c.ObserverType != "endpoint" && c.ObserverType != "gateway" {
		return fmt.Errorf("unsupported observer type %q; expected endpoint or gateway", c.ObserverType)
	}
	if openwrtLiteBuild && c.ObserverType != "gateway" {
		return errors.New("OpenWrt Gateway Lite only supports observer_type=gateway")
	}
	if c.WALPath == "" {
		return errors.New("WAL path is required")
	}
	if c.WALMaxBytes <= 0 {
		return errors.New("WAL max bytes must be positive")
	}
	if c.SnapshotIntervalSeconds == 0 {
		c.SnapshotIntervalSeconds = 1
	}
	if c.SnapshotIntervalSeconds < 1 || c.SnapshotIntervalSeconds > 60 {
		return errors.New("snapshot interval seconds must be between 1 and 60")
	}
	if c.EbpfMode == "" {
		c.EbpfMode = "auto"
	}
	if c.EbpfMode != "auto" && c.EbpfMode != "on" && c.EbpfMode != "off" {
		return fmt.Errorf("ebpf_mode must be auto, on, or off")
	}
	if _, _, err := net.SplitHostPort(c.ServerAddress); err != nil {
		return fmt.Errorf("server address must be host:port: %w", err)
	}
	if c.SecurityMode {
		if _, err := os.Stat(c.CACertFile); err != nil {
			return fmt.Errorf("CA certificate is not readable: %w", err)
		}
	}
	return nil
}

func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read agent config: %w", err)
	}
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return Config{}, fmt.Errorf("decode agent config: %w", err)
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func SaveConfig(path string, config Config) error {
	return writePrivateJSON(path, config)
}

func writePrivateJSON(path string, value any) error {
	if path == "" {
		return errors.New("config path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary := path + ".tmp"
	defer os.Remove(temporary)
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create agent config: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write agent config: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replace agent config: %w", err)
	}
	// Sync the containing directory so the rename survives a power loss on Linux.
	if directory, err := os.Open(filepath.Dir(path)); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
