package domain

import (
	"fmt"
	"net"
	"strings"
	"time"
)

type IPMetadataOverride struct {
	Exists       bool      `json:"-"`
	IP           string    `json:"ip"`
	Domain       *string   `json:"domain,omitempty"`
	ASN          *uint32   `json:"asn,omitempty"`
	Organization *string   `json:"organization,omitempty"`
	Country      *string   `json:"country,omitempty"`
	Region       *string   `json:"region,omitempty"`
	City         *string   `json:"city,omitempty"`
	Latitude     *float64  `json:"latitude,omitempty"`
	Longitude    *float64  `json:"longitude,omitempty"`
	Trusted      bool      `json:"trusted"`
	UpdatedBy    string    `json:"updated_by,omitempty"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
}

type IPMetadataPatch struct {
	IP string

	Domain          *string
	SetDomain       bool
	ASN             *uint32
	SetASN          bool
	Organization    *string
	SetOrganization bool
	Country         *string
	SetCountry      bool
	Region          *string
	SetRegion       bool
	City            *string
	SetCity         bool
	Latitude        *float64
	SetLatitude     bool
	Longitude       *float64
	SetLongitude    bool
	Trusted         *bool
}

func (p IPMetadataPatch) HasChanges() bool {
	return p.SetDomain || p.SetASN || p.SetOrganization || p.SetCountry || p.SetRegion || p.SetCity ||
		p.SetLatitude || p.SetLongitude || p.Trusted != nil
}

func (p IPMetadataPatch) ChangedFields() []string {
	fields := make([]string, 0, 10)
	for _, field := range []struct {
		name string
		set  bool
	}{
		{"domain", p.SetDomain}, {"asn", p.SetASN}, {"organization", p.SetOrganization},
		{"country", p.SetCountry}, {"region", p.SetRegion}, {"city", p.SetCity},
		{"latitude", p.SetLatitude}, {"longitude", p.SetLongitude}, {"trusted", p.Trusted != nil},
	} {
		if field.set {
			fields = append(fields, field.name)
		}
	}
	return fields
}

func NormalizeIP(value string) (string, error) {
	parsed := net.ParseIP(strings.TrimSpace(value))
	if parsed == nil {
		return "", fmt.Errorf("invalid IP address")
	}
	if ipv4 := parsed.To4(); ipv4 != nil {
		return ipv4.String(), nil
	}
	return parsed.String(), nil
}

func ValidateAgentCoordinates(latitude, longitude *float64) error {
	if latitude == nil && longitude == nil {
		return nil
	}
	if latitude == nil || longitude == nil {
		return fmt.Errorf("latitude and longitude must be set or cleared together")
	}
	if *latitude < -90 || *latitude > 90 {
		return fmt.Errorf("latitude must be between -90 and 90")
	}
	if *longitude < -180 || *longitude > 180 {
		return fmt.Errorf("longitude must be between -180 and 180")
	}
	return nil
}
