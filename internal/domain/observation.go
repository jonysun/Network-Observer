package domain

import "time"

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

type Device struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Type         ObserverType `json:"type"`
	OS           string       `json:"os"`
	Architecture string       `json:"architecture"`
	AgentVersion string       `json:"agent_version"`
	Online       bool         `json:"online"`
	LastSeen     time.Time    `json:"last_seen"`
	Capabilities []Capability `json:"capabilities"`
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

type Enrichment struct {
	IP           string    `json:"ip"`
	ASN          uint32    `json:"asn,omitempty"`
	Organization string    `json:"organization,omitempty"`
	Country      string    `json:"country,omitempty"`
	Region       string    `json:"region,omitempty"`
	City         string    `json:"city,omitempty"`
	Latitude     float64   `json:"latitude,omitempty"`
	Longitude    float64   `json:"longitude,omitempty"`
	Version      string    `json:"version,omitempty"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
	Status       string    `json:"status"`
	Reason       string    `json:"reason,omitempty"`
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
