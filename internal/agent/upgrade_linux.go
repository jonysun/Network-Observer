//go:build linux

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	pb "github.com/network-observer/network-observer/gen/networkobserver/v1"
	"google.golang.org/grpc"
)

var upgradeVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
var upgradeDigestPattern = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

func agentUpgradeCapability() *pb.Capability {
	status, detail := "unsupported", "remote upgrade requires a native Linux systemd install"
	_, dockerMarker := os.Stat("/.dockerenv")
	_, containerMarker := os.Stat("/run/.containerenv")
	if os.Geteuid() == 0 && errors.Is(dockerMarker, os.ErrNotExist) && errors.Is(containerMarker, os.ErrNotExist) {
		if _, err := os.Stat("/run/systemd/system"); err == nil {
			if _, err := exec.LookPath("systemctl"); err == nil {
				status, detail = "available", "native Linux systemd Agent"
			}
		}
	}
	return &pb.Capability{Name: "agent_upgrade", Status: status, Detail: detail}
}

func performAgentUpgrade(ctx context.Context, config Config, request *pb.AgentUpgradeRequest, progress func(string, string) error) error {
	if request == nil || request.TaskId == "" || !upgradeVersionPattern.MatchString(request.Version) {
		return errors.New("invalid upgrade request")
	}
	if os.Geteuid() != 0 {
		return errors.New("Agent upgrade requires root privileges")
	}
	base, err := url.Parse(strings.TrimRight(request.ReleaseBaseUrl, "/"))
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return errors.New("release source must be a valid HTTPS URL")
	}
	arch := runtime.GOARCH
	if arch != "amd64" && arch != "arm64" {
		return fmt.Errorf("unsupported Linux architecture %s", arch)
	}
	if err := progress("downloading", "正在下载 Agent 发布文件"); err != nil {
		return err
	}
	client := &http.Client{Timeout: 3 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || req.URL.Scheme != "https" {
			return errors.New("unsafe release redirect")
		}
		return nil
	}}
	releaseURL := strings.TrimRight(request.ReleaseBaseUrl, "/") + "/" + request.Version + "/"
	manifest, err := fetchUpgradeAsset(ctx, client, releaseURL+"SHA256SUMS", 1<<20)
	if err != nil {
		return fmt.Errorf("download release checksums: %w", err)
	}
	binaryAsset := "network-observer-agent-linux-" + arch
	binary, err := fetchUpgradeAsset(ctx, client, releaseURL+binaryAsset, 128<<20)
	if err != nil {
		return fmt.Errorf("download Agent binary: %w", err)
	}

	var ebpfPath string
	var ebpf []byte
	if config.ObserverType == "endpoint" && config.EbpfMode != "off" {
		ebpfAsset := "network-observer-flow-counter-linux-" + arch + ".o"
		ebpf, err = fetchUpgradeAsset(ctx, client, releaseURL+ebpfAsset, 16<<20)
		if err != nil {
			return fmt.Errorf("download eBPF object: %w", err)
		}
		ebpfPath = config.EbpfObjectPath
		if ebpfPath == "" {
			ebpfPath = "/usr/local/lib/network-observer/flow_counter.o"
		}
		if !filepath.IsAbs(ebpfPath) {
			return errors.New("configured eBPF object path must be absolute for remote upgrade")
		}
	}
	if err := progress("verifying", "正在验证 SHA-256 校验和"); err != nil {
		return err
	}
	if err := verifyUpgradeAsset(manifest, binaryAsset, binary); err != nil {
		return fmt.Errorf("verify Agent binary: %w", err)
	}
	if ebpfPath != "" {
		ebpfAsset := "network-observer-flow-counter-linux-" + arch + ".o"
		if err := verifyUpgradeAsset(manifest, ebpfAsset, ebpf); err != nil {
			return fmt.Errorf("verify eBPF object: %w", err)
		}
	}
	binaryPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve Agent executable: %w", err)
	}
	binaryPath, err = filepath.EvalSymlinks(binaryPath)
	if err != nil {
		return fmt.Errorf("resolve Agent executable path: %w", err)
	}
	if !filepath.IsAbs(binaryPath) {
		return errors.New("Agent executable path is not absolute")
	}
	if err := progress("installing", "校验完成，正在原子替换 Agent 文件"); err != nil {
		return err
	}
	if err := progress("restarting", "正在准备重新启动 Agent"); err != nil {
		return err
	}
	return installAndExec(binaryPath, binary, ebpfPath, ebpf)
}

func installAndExec(binaryPath string, binary []byte, ebpfPath string, ebpf []byte) error {
	binaryTemp, err := writeUpgradeTemp(filepath.Dir(binaryPath), filepath.Base(binaryPath)+".upgrade-*", binary, 0o755)
	if err != nil {
		return fmt.Errorf("stage Agent binary: %w", err)
	}
	defer os.Remove(binaryTemp)
	binaryBackup, err := backupUpgradeFile(binaryPath)
	if err != nil {
		return fmt.Errorf("backup current Agent binary: %w", err)
	}
	if err := os.Rename(binaryTemp, binaryPath); err != nil {
		_ = os.Remove(binaryBackup)
		return fmt.Errorf("atomically install Agent binary: %w", err)
	}

	var ebpfBackup string
	if ebpfPath != "" {
		ebpfTemp, err := writeUpgradeTemp(filepath.Dir(ebpfPath), filepath.Base(ebpfPath)+".upgrade-*", ebpf, 0o644)
		if err != nil {
			_ = restoreUpgradeFile(binaryPath, binaryBackup)
			return fmt.Errorf("stage eBPF object: %w", err)
		}
		ebpfBackup, err = backupUpgradeFile(ebpfPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(ebpfTemp)
			_ = restoreUpgradeFile(binaryPath, binaryBackup)
			return fmt.Errorf("backup current eBPF object: %w", err)
		}
		if err := os.Rename(ebpfTemp, ebpfPath); err != nil {
			_ = os.Remove(ebpfTemp)
			if ebpfBackup != "" {
				_ = restoreUpgradeFile(ebpfPath, ebpfBackup)
			}
			_ = restoreUpgradeFile(binaryPath, binaryBackup)
			return fmt.Errorf("atomically install eBPF object: %w", err)
		}
	}
	if err := syscall.Exec(binaryPath, os.Args, os.Environ()); err != nil {
		if ebpfBackup != "" {
			_ = restoreUpgradeFile(ebpfPath, ebpfBackup)
		}
		_ = restoreUpgradeFile(binaryPath, binaryBackup)
		return fmt.Errorf("restart Agent with upgraded binary: %w", err)
	}
	return nil
}

func backupUpgradeFile(path string) (string, error) {
	backup, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".rollback-*")
	if err != nil {
		return "", err
	}
	backupPath := backup.Name()
	if err := backup.Close(); err != nil {
		_ = os.Remove(backupPath)
		return "", err
	}
	if err := os.Remove(backupPath); err != nil {
		return "", err
	}
	if err := os.Link(path, backupPath); err != nil {
		return "", err
	}
	return backupPath, nil
}

func restoreUpgradeFile(path, backup string) error {
	if backup == "" {
		return os.Remove(path)
	}
	return os.Rename(backup, path)
}

func writeUpgradeTemp(directory, pattern string, content []byte, mode os.FileMode) (string, error) {
	file, err := os.CreateTemp(directory, pattern)
	if err != nil {
		return "", err
	}
	name := file.Name()
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return "", err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return "", err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

func fetchUpgradeAsset(ctx context.Context, client *http.Client, assetURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP status %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("release asset exceeds size limit")
	}
	return data, nil
}

func verifyUpgradeAsset(manifest []byte, asset string, data []byte) error {
	var expected string
	for _, line := range strings.Split(string(manifest), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 || !upgradeDigestPattern.MatchString(fields[0]) {
			return errors.New("invalid SHA256SUMS entry")
		}
		name := strings.TrimPrefix(fields[1], "*")
		if strings.Contains(name, "/") || strings.Contains(name, "\\") {
			return errors.New("invalid asset name in SHA256SUMS")
		}
		if name == asset {
			if expected != "" {
				return errors.New("duplicate asset entry in SHA256SUMS")
			}
			expected = strings.ToLower(fields[0])
		}
	}
	if expected == "" {
		return fmt.Errorf("SHA256SUMS has no entry for %s", asset)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != expected {
		return errors.New("SHA-256 mismatch")
	}
	return nil
}

func sendUpgradeStatus(ctx context.Context, cancel context.CancelFunc, stream grpc.BidiStreamingClient[pb.AgentMessage, pb.ServerMessage], taskID, state, message, version string) error {
	return sendWithin(ctx, cancel, stream, &pb.AgentMessage{Payload: &pb.AgentMessage_UpgradeStatus{UpgradeStatus: &pb.AgentUpgradeStatus{
		TaskId: taskID, State: state, Message: message, InstalledVersion: version, UpdatedAtUnixNanos: time.Now().UnixNano(),
	}}})
}
