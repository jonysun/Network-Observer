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
	CACertFile                string `json:"ca_cert_file"`
	SecurityMode              bool   `json:"security_mode"`
	EnrollmentToken           string `json:"enrollment_token"`
	DeviceID                  string `json:"device_id"`
	DeviceName                string `json:"device_name"`
	ObserverType              string `json:"observer_type"`
	WALPath                   string `json:"wal_path"`
	WALMaxBytes               int64  `json:"wal_max_bytes"`
	DockerSocket              string `json:"docker_socket,omitempty"`
	ConntrackPath             string `json:"conntrack_path,omitempty"`
	NetFlowListen             string `json:"netflow_listen,omitempty"`
	IPFIXListen               string `json:"ipfix_listen,omitempty"`
	SFlowListen               string `json:"sflow_listen,omitempty"`
	DisableSocketDiag         bool   `json:"disable_socket_diag,omitempty"`
	DisableConntrack          bool   `json:"disable_conntrack,omitempty"`
	DisableProcessAttribution bool   `json:"disable_process_attribution,omitempty"`
}

func (c Config) Validate() error {
	if c.ServerAddress == "" || c.DeviceID == "" || c.DeviceName == "" {
		return errors.New("server address, device ID, and device name are required")
	}
	if c.EnrollmentToken == "" {
		return errors.New("persistent enrollment token is required")
	}
	if c.SecurityMode && c.CACertFile == "" {
		return errors.New("CA certificate is required when security mode is enabled")
	}
	if c.ObserverType != "endpoint" && c.ObserverType != "gateway" {
		return fmt.Errorf("unsupported observer type %q; expected endpoint or gateway", c.ObserverType)
	}
	if c.WALPath == "" {
		return errors.New("WAL path is required")
	}
	if c.WALMaxBytes <= 0 {
		return errors.New("WAL max bytes must be positive")
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
