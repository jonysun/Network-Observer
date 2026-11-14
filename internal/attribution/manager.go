package attribution

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/network-observer/network-observer/internal/domain"
)

type Value struct {
	PID           uint32 `json:"pid,omitempty"`
	ProcessName   string `json:"process_name,omitempty"`
	ContainerID   string `json:"container_id,omitempty"`
	ContainerName string `json:"container_name,omitempty"`
	ServiceName   string `json:"service_name,omitempty"`
}

type Matcher struct {
	DeviceID         string `json:"device_id,omitempty"`
	ObserverType     string `json:"observer_type,omitempty"`
	NetworkNamespace string `json:"network_namespace,omitempty"`
	Protocol         string `json:"protocol,omitempty"`
	SourceIP         string `json:"source_ip,omitempty"`
	SourcePort       uint16 `json:"source_port,omitempty"`
	DestinationIP    string `json:"destination_ip,omitempty"`
	DestinationPort  uint16 `json:"destination_port,omitempty"`
	ProcessName      string `json:"process_name,omitempty"`
	ContainerID      string `json:"container_id,omitempty"`
}

type Rule struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Priority      int       `json:"priority"`
	Enabled       bool      `json:"enabled"`
	Matcher       Matcher   `json:"matcher"`
	Attribution   Value     `json:"attribution"`
	EffectiveFrom time.Time `json:"effective_from"`
	CreatedAt     time.Time `json:"created_at"`
}

type Manager struct {
	mu        sync.RWMutex
	rules     []Rule
	overrides map[string]Value
}

func New(rules []Rule, overrides map[string]Value) *Manager {
	m := &Manager{overrides: make(map[string]Value)}
	m.ReplaceRules(rules)
	for key, value := range overrides {
		m.overrides[key] = value
	}
	return m
}

func (m *Manager) ReplaceRules(rules []Rule) {
	copyOfRules := append([]Rule(nil), rules...)
	sort.SliceStable(copyOfRules, func(i, j int) bool {
		if copyOfRules[i].Priority == copyOfRules[j].Priority {
			return copyOfRules[i].CreatedAt.Before(copyOfRules[j].CreatedAt)
		}
		return copyOfRules[i].Priority < copyOfRules[j].Priority
	})
	m.mu.Lock()
	m.rules = copyOfRules
	m.mu.Unlock()
}

func (m *Manager) SetOverride(connectionKey string, value Value) {
	m.mu.Lock()
	m.overrides[connectionKey] = value
	m.mu.Unlock()
}

func (m *Manager) DeleteOverride(connectionKey string) {
	m.mu.Lock()
	delete(m.overrides, connectionKey)
	m.mu.Unlock()
}

func (m *Manager) Apply(observation *domain.FlowObservation) {
	key := observation.DeviceID + ":" + observation.FlowID
	m.mu.RLock()
	if value, ok := m.overrides[key]; ok {
		m.mu.RUnlock()
		applyValue(observation, value, "manual", "one-time override")
		return
	}
	if observation.AttributionSource != "" && observation.AttributionSource != domain.AttributionUnknown {
		m.mu.RUnlock()
		return
	}
	for _, rule := range m.rules {
		if rule.Enabled && !observation.ObservedAt.Before(rule.EffectiveFrom) && rule.Matcher.matches(*observation) {
			m.mu.RUnlock()
			applyValue(observation, rule.Attribution, "auto", "rule:"+rule.ID)
			return
		}
	}
	m.mu.RUnlock()
	if observation.AttributionSource == "" {
		observation.AttributionSource = domain.AttributionUnknown
	}
}

func (m Matcher) matches(o domain.FlowObservation) bool {
	return (m.DeviceID == "" || m.DeviceID == o.DeviceID) &&
		(m.ObserverType == "" || m.ObserverType == string(o.ObserverType)) &&
		(m.NetworkNamespace == "" || m.NetworkNamespace == o.NetworkNamespace) &&
		(m.Protocol == "" || strings.EqualFold(m.Protocol, string(o.Protocol))) &&
		(m.SourceIP == "" || m.SourceIP == o.SourceIP) &&
		(m.SourcePort == 0 || m.SourcePort == o.SourcePort) &&
		(m.DestinationIP == "" || m.DestinationIP == o.DestinationIP) &&
		(m.DestinationPort == 0 || m.DestinationPort == o.DestinationPort) &&
		(m.ProcessName == "" || m.ProcessName == o.ProcessName) &&
		(m.ContainerID == "" || m.ContainerID == o.ContainerID)
}

func applyValue(observation *domain.FlowObservation, value Value, source, reason string) {
	observation.PID = value.PID
	observation.ProcessName = value.ProcessName
	observation.ContainerID = value.ContainerID
	observation.ContainerName = value.ContainerName
	observation.ServiceName = value.ServiceName
	observation.AttributionSource = domain.AttributionSource(source)
	observation.AttributionReason = reason
}

func DecodeRule(recordJSON, attributionJSON []byte) (Matcher, Value, error) {
	var matcher Matcher
	var value Value
	if err := json.Unmarshal(recordJSON, &matcher); err != nil {
		return matcher, value, err
	}
	if err := json.Unmarshal(attributionJSON, &value); err != nil {
		return matcher, value, err
	}
	return matcher, value, nil
}

func ConnectionKey(deviceID, flowID string) string {
	return deviceID + ":" + flowID
}

func ParseConnectionKey(value string) (string, string, bool) {
	index := strings.IndexByte(value, ':')
	if index <= 0 || index == len(value)-1 {
		return "", "", false
	}
	return value[:index], value[index+1:], true
}

func Port(value string) (uint16, error) {
	parsed, err := strconv.ParseUint(value, 10, 16)
	return uint16(parsed), err
}
