package server

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	pb "github.com/network-observer/network-observer/gen/networkobserver/v1"
	"github.com/network-observer/network-observer/internal/domain"
)

func (s *Server) agentUpgradeInfo(device domain.Device) *domain.AgentUpgrade {
	info := &domain.AgentUpgrade{TargetVersion: s.config.AgentReleaseVersion, State: "unavailable"}
	supportedOS := strings.EqualFold(device.OS, "linux")
	supportedArch := device.Architecture == "amd64" || device.Architecture == "arm64"
	capable := false
	for _, capability := range device.Capabilities {
		if capability.Name == "agent_upgrade" && capability.Status == "available" {
			capable = true
		}
	}
	switch {
	case !supportedOS:
		info.Reason = "当前平台暂不支持 Center 自动升级"
	case !supportedArch:
		info.Reason = "仅支持 Linux amd64/arm64 自动升级"
	case !capable:
		info.Reason = "此 Agent 尚未声明原生 systemd 升级能力"
	case !device.Online:
		info.Reason = "Agent 离线；上线后可发起升级"
	default:
		info.Supported = true
		if device.AgentVersion == s.config.AgentReleaseVersion {
			info.State = "up_to_date"
		} else {
			info.State = "available"
		}
	}
	s.upgradeMu.RLock()
	if task, ok := s.upgrades[device.ID]; ok {
		*info = task
		info.Supported = supportedOS && supportedArch && capable
		if !info.Supported && info.Reason == "" {
			info.Reason = "Agent 不支持自动升级"
		}
	}
	s.upgradeMu.RUnlock()
	return info
}

func (s *Server) handleAgentUpgrade(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListDevices(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "query_failed", "could not load devices")
		return
	}
	var device *domain.Device
	for i := range items {
		if items[i].ID == r.PathValue("id") {
			device = &items[i]
			break
		}
	}
	if device == nil {
		writeError(w, r, http.StatusNotFound, "not_found", "device not found")
		return
	}
	if s.deviceConnected(device.ID) {
		device.Online = true
	}
	info := s.agentUpgradeInfo(*device)
	if !info.Supported {
		writeError(w, r, http.StatusConflict, "upgrade_unavailable", info.Reason)
		return
	}
	if info.State == "up_to_date" {
		writeError(w, r, http.StatusConflict, "already_current", "Agent 已是目标版本")
		return
	}
	base, err := url.Parse(s.config.AgentReleaseBaseURL)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		writeError(w, r, http.StatusServiceUnavailable, "release_source_invalid", "Agent 发布源必须是有效的 HTTPS 地址")
		return
	}
	taskID, err := randomToken()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "task_error", "could not create upgrade task")
		return
	}
	task := &pb.AgentUpgradeRequest{TaskId: taskID, Version: s.config.AgentReleaseVersion, ReleaseBaseUrl: strings.TrimRight(s.config.AgentReleaseBaseURL, "/")}
	state := domain.AgentUpgrade{Supported: true, TargetVersion: task.Version, State: "queued", TaskID: taskID, Message: "等待 Agent 接收升级任务", UpdatedAt: now().UTC()}
	s.upgradeMu.Lock()
	if previous, ok := s.upgrades[device.ID]; ok && (previous.State == "queued" || previous.State == "downloading" || previous.State == "verifying" || previous.State == "installing" || previous.State == "restarting") {
		s.upgradeMu.Unlock()
		writeError(w, r, http.StatusConflict, "upgrade_in_progress", "该设备已有升级任务正在执行")
		return
	}
	s.upgrades[device.ID] = state
	s.upgradeMu.Unlock()
	if !s.queueAgentUpgrade(device.ID, task) {
		s.upgradeMu.Lock()
		delete(s.upgrades, device.ID)
		s.upgradeMu.Unlock()
		writeError(w, r, http.StatusConflict, "agent_disconnected", "Agent 已断开，请刷新设备状态后重试")
		return
	}
	writeJSON(w, http.StatusAccepted, state)
}

func (s *Server) setAgentUpgradeStatus(deviceID string, report *pb.AgentUpgradeStatus) {
	if report == nil || report.TaskId == "" {
		return
	}
	allowed := map[string]bool{"downloading": true, "verifying": true, "installing": true, "failed": true, "restarting": true}
	if !allowed[report.State] {
		return
	}
	s.upgradeMu.Lock()
	defer s.upgradeMu.Unlock()
	current, ok := s.upgrades[deviceID]
	if !ok || current.TaskID != report.TaskId {
		return
	}
	current.State = report.State
	current.Message = report.Message
	if report.InstalledVersion != "" && report.State == "restarting" {
		current.Message = "新版本已安装，等待 Agent 重新连接确认"
	}
	if report.UpdatedAtUnixNanos > 0 {
		current.UpdatedAt = time.Unix(0, report.UpdatedAtUnixNanos).UTC()
	} else {
		current.UpdatedAt = now().UTC()
	}
	s.upgrades[deviceID] = current
}

func (s *Server) confirmAgentUpgrade(deviceID, version string) {
	s.upgradeMu.Lock()
	defer s.upgradeMu.Unlock()
	current, ok := s.upgrades[deviceID]
	if !ok || current.State == "failed" || current.State == "succeeded" || version != current.TargetVersion {
		return
	}
	current.State, current.Message, current.UpdatedAt = "succeeded", "Agent 已重新连接并报告目标版本", now().UTC()
	s.upgrades[deviceID] = current
}
