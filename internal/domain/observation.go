package domain

import (
	"strings"
	"time"
)

type Protocol string

const (
	ProtocolTCP Protocol = "tcp"
	ProtocolUDP Protocol = "udp"
)

type ObserverType string

const (
	ObserverEndpoint ObserverType = "endpoint"
	ObserverGateway  ObserverType = "gateway"
)

type AttributionSource string

const (
	AttributionAuto    AttributionSource = "auto"
	AttributionManual  AttributionSource = "manual"
	AttributionUnknown AttributionSource = "unknown"
)

type FlowObservation struct {
	ID                     string            `json:"id"`
	FlowID                 string            `json:"flow_id"`
	DeviceID               string            `json:"device_id"`
	ObserverID             string            `json:"observer_id"`
	ObserverType           ObserverType      `json:"observer_type"`
	LocalIsSource          bool              `json:"local_is_source"`
	Sequence               uint64            `json:"sequence"`
	ObservedAt             time.Time         `json:"observed_at"`
	CoverageStart          time.Time         `json:"-"`
	NetworkNamespace       string            `json:"network_namespace,omitempty"`
	Interface              string            `json:"interface,omitempty"`
	Protocol               Protocol          `json:"protocol"`
	State                  string            `json:"state,omitempty"`
	SourceIP               string            `json:"source_ip"`
	SourcePort             uint16            `json:"source_port"`
	DestinationIP          string            `json:"destination_ip"`
	DestinationPort        uint16            `json:"destination_port"`
	NATSourceIP            string            `json:"nat_source_ip,omitempty"`
	NATSourcePort          uint16            `json:"nat_source_port,omitempty"`
	NATDestinationIP       string            `json:"nat_destination_ip,omitempty"`
	NATDestinationPort     uint16            `json:"nat_destination_port,omitempty"`
	BytesSourceToDest      uint64            `json:"bytes_source_to_destination"`
	BytesDestToSource      uint64            `json:"bytes_destination_to_source"`
	TotalBytesSourceToDest uint64            `json:"total_bytes_source_to_destination"`
	TotalBytesDestToSource uint64            `json:"total_bytes_destination_to_source"`
	RateBytesSourceToDest  uint64            `json:"rate_bytes_source_to_destination"`
	RateBytesDestToSource  uint64            `json:"rate_bytes_destination_to_source"`
	PacketsSourceToDest    uint64            `json:"packets_source_to_destination,omitempty"`
	PacketsDestToSource    uint64            `json:"packets_destination_to_source,omitempty"`
	PID                    uint32            `json:"pid,omitempty"`
	ProcessName            string            `json:"process_name,omitempty"`
	ContainerID            string            `json:"container_id,omitempty"`
	ContainerName          string            `json:"container_name,omitempty"`
	ServiceName            string            `json:"service_name,omitempty"`
	AttributionSource      AttributionSource `json:"attribution_source"`
	AttributionReason      string            `json:"attribution_reason,omitempty"`
	SamplingRate           uint32            `json:"sampling_rate,omitempty"`
	CounterSource          string            `json:"counter_source"`
	Confidence             string            `json:"confidence"`
	Domain                 string            `json:"domain,omitempty"`
	DomainSource           string            `json:"domain_source,omitempty"`
	ASN                    uint32            `json:"asn,omitempty"`
	Organization           string            `json:"organization,omitempty"`
	Country                string            `json:"country,omitempty"`
	Region                 string            `json:"region,omitempty"`
	City                   string            `json:"city,omitempty"`
	Latitude               float64           `json:"latitude,omitempty"`
	Longitude              float64           `json:"longitude,omitempty"`
	EnrichmentVersion      string            `json:"enrichment_version,omitempty"`
	GapReason              string            `json:"gap_reason,omitempty"`
	DNSMetadataOnly        bool              `json:"-"`
	DNSIP                  string            `json:"-"`
	DNSTTL                 uint32            `json:"-"`
	Active                 bool              `json:"active"`
	// TrafficSuppressed marks a duplicate gateway perspective whose traffic is
	// already counted by an endpoint observation. The live connection remains
	// visible; only minute traffic aggregation skips it.
	TrafficSuppressed bool `json:"-"`
}

type Capability struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Detail    string    `json:"detail,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

// NormalizeCapability keeps capability semantics stable when a newer Center
// reads data written by an older Agent. A data_gap is a durable historical
// fact; current WAL pressure is represented separately by local_wal.
func NormalizeCapability(capability Capability) Capability {
	if capability.Name == "data_gap" && capability.Status == "degraded" {
		capability.Status = "historical"
		if capability.Detail != "" && !strings.HasPrefix(capability.Detail, "historical gap:") {
			capability.Detail = "historical gap: " + capability.Detail
		}
	}
	return capability
}

type Device struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	RegisteredName string       `json:"registered_name"`
	DeviceCategory string       `json:"device_category"`
	Latitude       *float64     `json:"latitude,omitempty"`
	Longitude      *float64     `json:"longitude,omitempty"`
	Type           ObserverType `json:"type"`
	OS             string       `json:"os"`
	Architecture   string       `json:"architecture"`
	AgentVersion   string       `json:"agent_version"`
	Online         bool         `json:"online"`
	LastSeen       time.Time    `json:"last_seen"`
	Capabilities   []Capability `json:"capabilities"`
}

func ValidDeviceCategory(category string) bool {
	switch category {
	case "unknown", "computer", "router", "gateway", "switch", "laptop", "tablet", "phone", "tv", "server", "nas", "other":
		return true
	default:
		return false
	}
}

type TrafficPoint struct {
	Bucket        time.Time `json:"bucket"`
	Dimension     string    `json:"dimension"`
	Value         string    `json:"value"`
	Protocol      Protocol  `json:"protocol"`
	Direction     string    `json:"direction"`
	Bytes         uint64    `json:"bytes"`
	Packets       uint64    `json:"packets"`
	Estimated     bool      `json:"estimated"`
	UploadBytes   uint64    `json:"upload_bytes,omitempty"`
	DownloadBytes uint64    `json:"download_bytes,omitempty"`
	GapReason     string    `json:"gap_reason,omitempty"`
}

type PresencePoint struct {
	Key             string    `json:"key"`
	DeviceID        string    `json:"device_id"`
	Protocol        Protocol  `json:"protocol"`
	SourceIP        string    `json:"source_ip"`
	SourcePort      uint16    `json:"source_port"`
	DestinationIP   string    `json:"destination_ip"`
	DestinationPort uint16    `json:"destination_port"`
	Domain          string    `json:"domain,omitempty"`
	FirstSeen       time.Time `json:"first_seen"`
	LastSeen        time.Time `json:"last_seen"`
	ObservedMinutes uint32    `json:"observed_minutes"`
	ActiveSeconds   uint64    `json:"active_seconds"`
	ContinuityRatio float64   `json:"continuity_ratio"`
	UploadBytes     uint64    `json:"upload_bytes"`
	DownloadBytes   uint64    `json:"download_bytes"`
	ASN             uint32    `json:"asn,omitempty"`
	Organization    string    `json:"organization,omitempty"`
	Country         string    `json:"country,omitempty"`
	Region          string    `json:"region,omitempty"`
	City            string    `json:"city,omitempty"`
	Estimated       bool      `json:"estimated"`
	GapReason       string    `json:"gap_reason,omitempty"`
}

type OverviewRankingRow struct {
	Dimension                 string `json:"dimension"`
	Value                     string `json:"value"`
	DeviceID                  string `json:"device_id,omitempty"`
	RemoteIP                  string `json:"remote_ip,omitempty"`
	UploadBytes               uint64 `json:"upload_bytes"`
	DownloadBytes             uint64 `json:"download_bytes"`
	Connections               uint64 `json:"connections"`
	Estimated                 bool   `json:"estimated"`
	GapReason                 string `json:"gap_reason,omitempty"`
	HistoryDimensionAvailable bool   `json:"history_dimension_available"`
}

type Enrichment struct {
	IP                   string              `json:"ip"`
	Domain               string              `json:"domain,omitempty"`
	DomainSource         string              `json:"domain_source,omitempty"`
	DomainCandidates     []string            `json:"domain_candidates,omitempty"`
	PTRStatus            string              `json:"ptr_status,omitempty"`
	PTRReason            string              `json:"ptr_reason,omitempty"`
	PTRUpdatedAt         time.Time           `json:"ptr_updated_at,omitempty"`
	ASN                  uint32              `json:"asn,omitempty"`
	Organization         string              `json:"organization,omitempty"`
	Country              string              `json:"country,omitempty"`
	Region               string              `json:"region,omitempty"`
	City                 string              `json:"city,omitempty"`
	Latitude             float64             `json:"latitude"`
	Longitude            float64             `json:"longitude"`
	CoordinatesAvailable bool                `json:"coordinates_available"`
	Version              string              `json:"version,omitempty"`
	UpdatedAt            time.Time           `json:"updated_at,omitempty"`
	Status               string              `json:"status"`
	Reason               string              `json:"reason,omitempty"`
	Trusted              bool                `json:"trusted"`
	Automatic            EnrichmentFields    `json:"automatic"`
	Sources              map[string]string   `json:"sources,omitempty"`
	Manual               *IPMetadataOverride `json:"manual,omitempty"`
}

type EnrichmentFields struct {
	ASN                  uint32  `json:"asn,omitempty"`
	Organization         string  `json:"organization,omitempty"`
	Country              string  `json:"country,omitempty"`
	Region               string  `json:"region,omitempty"`
	City                 string  `json:"city,omitempty"`
	Latitude             float64 `json:"latitude"`
	Longitude            float64 `json:"longitude"`
	CoordinatesAvailable bool    `json:"coordinates_available"`
}

type DNSRelationship struct {
	Domain     string    `json:"domain"`
	IP         string    `json:"ip"`
	DeviceID   string    `json:"device_id"`
	ObservedAt time.Time `json:"observed_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	TTL        uint32    `json:"ttl"`
	Source     string    `json:"source"`
	Confidence string    `json:"confidence"`
}

type DNSObservation struct {
	DeviceID   string    `json:"device_id"`
	ObservedAt time.Time `json:"observed_at"`
	Interface  string    `json:"interface,omitempty"`
	Domain     string    `json:"domain"`
	IP         string    `json:"ip"`
	TTL        uint32    `json:"ttl"`
	Source     string    `json:"source"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type LANAddress struct {
	IP         string    `json:"ip"`
	Interfaces []string  `json:"interfaces"`
	State      string    `json:"state"`
	LastSeen   time.Time `json:"last_seen"`
}

type LANObserver struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	LastSeen time.Time `json:"last_seen"`
}

type LANDevice struct {
	Key            string        `json:"key"`
	NetworkID      string        `json:"network_id"`
	MAC            string        `json:"mac"`
	Name           string        `json:"name"`
	NameSource     string        `json:"name_source"`
	DeviceCategory string        `json:"device_category"`
	CategorySource string        `json:"category_source"`
	Addresses      []LANAddress  `json:"addresses"`
	Observers      []LANObserver `json:"observers"`
	LastSeen       time.Time     `json:"last_seen"`
	Stale          bool          `json:"stale"`
	IPConflict     bool          `json:"ip_conflict"`
	LocalAgentIDs  []string      `json:"local_agent_ids,omitempty"`
}

type LANTopologyAgent struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Type           ObserverType    `json:"type"`
	DeviceCategory string          `json:"device_category"`
	LastSeen       time.Time       `json:"last_seen"`
	Interfaces     []LANDevice     `json:"interfaces,omitempty"`
	DockerNetworks []DockerNetwork `json:"docker_networks,omitempty"`
}

type DockerNetwork struct {
	OwnerAgentID   string            `json:"owner_agent_id"`
	OwnerAgentName string            `json:"owner_agent_name,omitempty"`
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Driver         string            `json:"driver"`
	Scope          string            `json:"scope"`
	SubnetCIDRs    []string          `json:"subnet_cidrs"`
	Containers     []DockerContainer `json:"containers"`
	LastSeen       time.Time         `json:"last_seen"`
}

type DockerContainer struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	IPv4Address string `json:"ipv4_address,omitempty"`
	IPv6Address string `json:"ipv6_address,omitempty"`
	MACAddress  string `json:"mac_address,omitempty"`
}

type LANTopologyCenter struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Known bool   `json:"known"`
}

type LANTopologyEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

type LANTopologyGroup struct {
	ID            string             `json:"id"`
	NetworkID     string             `json:"network_id"`
	NetworkPrefix string             `json:"network_prefix"`
	RootDeviceKey string             `json:"root_device_key,omitempty"`
	RootAgentID   string             `json:"root_agent_id,omitempty"`
	Center        *LANTopologyCenter `json:"center,omitempty"`
	Agents        []LANTopologyAgent `json:"agents"`
	Assets        []LANDevice        `json:"assets"`
	Edges         []LANTopologyEdge  `json:"edges"`
}

type LANTopologyUnassigned struct {
	Center *LANTopologyCenter `json:"center,omitempty"`
	Agents []LANTopologyAgent `json:"agents"`
	Assets []LANDevice        `json:"assets"`
}

type LANTopology struct {
	Groups     []LANTopologyGroup    `json:"groups"`
	Agents     []LANTopologyAgent    `json:"agents,omitempty"`
	Unassigned LANTopologyUnassigned `json:"unassigned"`
	Layout     LANTopologyLayout     `json:"layout"`
	Settings   LANTopologySettings   `json:"settings"`
	Snapshot   *LANTopologySnapshot  `json:"snapshot,omitempty"`
}

type LANTopologyPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type LANTopologyInfrastructure struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Category string  `json:"category"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
}

type LANTopologyLink struct {
	ID   string `json:"id"`
	From string `json:"from"`
	To   string `json:"to"`
}

type LANTopologyLayout struct {
	Version        int                          `json:"version"`
	Mode           string                       `json:"mode,omitempty"`
	Positions      map[string]LANTopologyPoint `json:"positions"`
	Infrastructure []LANTopologyInfrastructure `json:"infrastructure"`
	ConfirmedLinks []LANTopologyLink           `json:"confirmed_links"`
}

type LANTopologySettings struct {
	SnapshotIntervalMinutes int `json:"snapshot_interval_minutes"`
	RetentionDays           int `json:"retention_days"`
}

type LANTopologySnapshot struct {
	ID         int64     `json:"id"`
	CapturedAt time.Time `json:"captured_at"`
}

type NeighborObservation struct {
	ObserverDeviceID string
	NetworkID        string
	NetworkPrefix    string
	IP               string
	MAC              string
	Interface        string
	State            string
	IsLocal          bool
	LastSeen         time.Time
}
