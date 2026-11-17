package store

import (
	"context"
	"testing"
	"time"

	"github.com/network-observer/network-observer/internal/domain"
)

func TestOverviewRankingsRejectsUnsupportedDimensionsBeforeQuerying(t *testing.T) {
	store := &Store{}
	_, _, _, err := store.OverviewRankings(context.Background(), []string{"raw_sql"}, 10, "all", "traffic", time.Now().Add(-time.Hour), time.Now())
	if err == nil {
		t.Fatal("unsupported dimension accepted")
	}
}

func TestOverviewRankingsRejectsInvalidLimitsAndTrustFilter(t *testing.T) {
	store := &Store{}
	from, to := time.Now().Add(-time.Hour), time.Now()
	for _, test := range []struct {
		name      string
		limit     uint32
		trust     string
		dimension []string
	}{
		{name: "zero limit", limit: 0, trust: "all", dimension: []string{"device"}},
		{name: "too many dimensions", limit: 10, trust: "all", dimension: []string{"device", "domain", "process", "local_port", "remote_ip", "asn"}},
		{name: "invalid trust filter", limit: 10, trust: "maybe", dimension: []string{"device"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, _, err := store.OverviewRankings(context.Background(), test.dimension, test.limit, test.trust, "traffic", from, to)
			if err == nil {
				t.Fatal("invalid ranking request accepted")
			}
		})
	}
}

func TestOverviewRankingsRejectsInvalidSortBeforeQuerying(t *testing.T) {
	store := &Store{}
	_, _, _, err := store.OverviewRankings(context.Background(), []string{"device"}, 10, "all", "arbitrary", time.Now().Add(-time.Hour), time.Now())
	if err == nil {
		t.Fatal("unsupported ranking sort accepted")
	}
}

func TestCurrentRankingValueAppliesIPOverridesToHistoricalDimensions(t *testing.T) {
	metadata := rankingMetadata{
		trusted: true, domain: "manual.example", organization: "Manual ISP", country: "JP",
		region: "Tokyo", city: "Chiyoda", asn: 64500,
	}
	tests := []struct {
		dimension string
		stored    string
		want      string
	}{
		{"domain", "未知域名 · 203.0.113.8", "manual.example"},
		{"asn", "0", "64500"},
		{"organization", "", "Manual ISP"},
		{"country", "", "JP"},
		{"region", "", "Tokyo"},
		{"city", "", "Chiyoda"},
	}
	for _, test := range tests {
		t.Run(test.dimension, func(t *testing.T) {
			if got := currentRankingValue(test.dimension, test.stored, "203.0.113.8", metadata); got != test.want {
				t.Fatalf("currentRankingValue(%q) = %q, want %q", test.dimension, got, test.want)
			}
		})
	}
}

func TestCurrentRankingValueUsesPTROnlyForUnknownDomain(t *testing.T) {
	metadata := rankingMetadata{ptrDomain: "ptr.example"}
	if got := currentRankingValue("domain", "未知域名 · 203.0.113.8", "203.0.113.8", metadata); got != "ptr.example（PTR）" {
		t.Fatalf("PTR did not fill unknown domain: %q", got)
	}
	if got := currentRankingValue("domain", "dns.example", "203.0.113.8", metadata); got != "dns.example" {
		t.Fatalf("PTR replaced an observed DNS name: %q", got)
	}
}

func TestIPMetadataPatchPreservesUnsetFieldsAndClearsExplicitNull(t *testing.T) {
	domainName, organization, city := "new.example", "Example ISP", "Tokyo"
	current := domain.IPMetadataOverride{
		IP: "192.0.2.5", Domain: &domainName, Organization: &organization, City: &city, Trusted: true,
	}
	patch := domain.IPMetadataPatch{SetDomain: true}
	applyIPMetadataPatch(&current, patch)
	if current.Domain != nil || current.Organization == nil || *current.Organization != organization || !current.Trusted {
		t.Fatalf("patch changed omitted fields: %#v", current)
	}
}

func TestValidateIPMetadataPatch(t *testing.T) {
	name := "  edge.example  "
	patch := domain.IPMetadataPatch{SetDomain: true, Domain: &name}
	if err := validateIPMetadataPatch(patch); err != nil {
		t.Fatalf("valid domain override rejected: %v", err)
	}
	if name != "edge.example" {
		t.Fatalf("domain was not trimmed: %q", name)
	}

	zero := uint32(0)
	if err := validateIPMetadataPatch(domain.IPMetadataPatch{SetASN: true, ASN: &zero}); err == nil {
		t.Fatal("zero ASN accepted as a manual override")
	}
	latitude := 35.0
	if err := validateIPMetadataPatch(domain.IPMetadataPatch{SetLatitude: true, Latitude: &latitude}); err == nil {
		t.Fatal("partial coordinate update accepted")
	}
}
