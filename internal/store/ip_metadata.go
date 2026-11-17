package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/network-observer/network-observer/internal/domain"
	"github.com/network-observer/network-observer/internal/enrichment"
)

type ipMetadataValidationError struct{ message string }

func (e ipMetadataValidationError) Error() string { return e.message }

func IsIPMetadataValidationError(err error) bool {
	var target ipMetadataValidationError
	return errors.As(err, &target)
}

func invalidIPMetadata(err error) error {
	return ipMetadataValidationError{message: err.Error()}
}

func (s *Store) IPMetadata(ctx context.Context, ip string) (domain.IPMetadataOverride, error) {
	normalized, err := domain.NormalizeIP(ip)
	if err != nil {
		return domain.IPMetadataOverride{}, err
	}
	item, err := scanIPMetadata(s.Postgres.QueryRow(ctx, `
		SELECT host(ip_address), domain, asn, organization, country, region, city,
		       latitude, longitude, trusted, updated_by, updated_at
		FROM ip_metadata_overrides WHERE ip_address = $1::inet`, normalized))
	if err == pgx.ErrNoRows {
		return domain.IPMetadataOverride{IP: normalized}, nil
	}
	return item, err
}

func (s *Store) UpdateIPMetadata(ctx context.Context, patch domain.IPMetadataPatch, actor, requestID string) (domain.IPMetadataOverride, error) {
	ip, err := domain.NormalizeIP(patch.IP)
	if err != nil {
		return domain.IPMetadataOverride{}, invalidIPMetadata(err)
	}
	if !patch.HasChanges() {
		return domain.IPMetadataOverride{}, invalidIPMetadata(fmt.Errorf("at least one metadata field must be provided"))
	}
	if err := validateIPMetadataPatch(patch); err != nil {
		return domain.IPMetadataOverride{}, invalidIPMetadata(err)
	}
	tx, err := s.Postgres.Begin(ctx)
	if err != nil {
		return domain.IPMetadataOverride{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO ip_metadata_overrides (ip_address, trusted, updated_by)
		VALUES ($1::inet, FALSE, $2)
		ON CONFLICT (ip_address) DO NOTHING`, ip, actor); err != nil {
		return domain.IPMetadataOverride{}, err
	}

	current, err := scanIPMetadata(tx.QueryRow(ctx, `
		SELECT host(ip_address), domain, asn, organization, country, region, city,
		       latitude, longitude, trusted, updated_by, updated_at
		FROM ip_metadata_overrides WHERE ip_address = $1::inet FOR UPDATE`, ip))
	if err != nil {
		return domain.IPMetadataOverride{}, err
	}
	applyIPMetadataPatch(&current, patch)
	if err := domain.ValidateAgentCoordinates(current.Latitude, current.Longitude); err != nil {
		return domain.IPMetadataOverride{}, invalidIPMetadata(err)
	}
	current.UpdatedBy = actor
	if _, err := tx.Exec(ctx, `
		INSERT INTO ip_metadata_overrides (
			ip_address, domain, asn, organization, country, region, city, latitude, longitude, trusted, updated_by, updated_at
		) VALUES ($1::inet, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, now())
		ON CONFLICT (ip_address) DO UPDATE SET
			domain = EXCLUDED.domain, asn = EXCLUDED.asn, organization = EXCLUDED.organization,
			country = EXCLUDED.country, region = EXCLUDED.region, city = EXCLUDED.city,
			latitude = EXCLUDED.latitude, longitude = EXCLUDED.longitude,
			trusted = EXCLUDED.trusted, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		ip, optionalString(current.Domain), optionalUint32(current.ASN), optionalString(current.Organization),
		optionalString(current.Country), optionalString(current.Region), optionalString(current.City),
		optionalFloat64(current.Latitude), optionalFloat64(current.Longitude), current.Trusted, actor,
	); err != nil {
		return domain.IPMetadataOverride{}, err
	}
	changed := patch.ChangedFields()
	changedJSON, err := json.Marshal(changed)
	if err != nil {
		return domain.IPMetadataOverride{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_log (actor, action, object_type, object_id, request_id, detail)
		VALUES ($1, 'update', 'ip_metadata', $2, $3, jsonb_build_object('fields', $4::jsonb))`,
		actor, ip, requestID, changedJSON); err != nil {
		return domain.IPMetadataOverride{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.IPMetadataOverride{}, err
	}
	return s.IPMetadata(ctx, ip)
}

func validateIPMetadataPatch(patch domain.IPMetadataPatch) error {
	for _, field := range []struct {
		name  string
		value *string
		limit int
		set   bool
	}{
		{"domain", patch.Domain, 253, patch.SetDomain},
		{"organization", patch.Organization, 256, patch.SetOrganization},
		{"country", patch.Country, 64, patch.SetCountry},
		{"region", patch.Region, 128, patch.SetRegion},
		{"city", patch.City, 128, patch.SetCity},
	} {
		if field.set && field.value != nil {
			value := strings.TrimSpace(*field.value)
			if value == "" || len(value) > field.limit {
				return fmt.Errorf("%s must contain 1 to %d characters or be null", field.name, field.limit)
			}
			*field.value = value
		}
	}
	if patch.SetASN && patch.ASN != nil && *patch.ASN == 0 {
		return fmt.Errorf("asn must be greater than zero or null")
	}
	if patch.SetLatitude && patch.Latitude != nil && (*patch.Latitude < -90 || *patch.Latitude > 90) {
		return fmt.Errorf("latitude must be between -90 and 90")
	}
	if patch.SetLongitude && patch.Longitude != nil && (*patch.Longitude < -180 || *patch.Longitude > 180) {
		return fmt.Errorf("longitude must be between -180 and 180")
	}
	if patch.SetLatitude != patch.SetLongitude {
		return fmt.Errorf("latitude and longitude must be changed together")
	}
	return nil
}

func applyIPMetadataPatch(current *domain.IPMetadataOverride, patch domain.IPMetadataPatch) {
	if patch.SetDomain {
		current.Domain = patch.Domain
	}
	if patch.SetASN {
		current.ASN = patch.ASN
	}
	if patch.SetOrganization {
		current.Organization = patch.Organization
	}
	if patch.SetCountry {
		current.Country = patch.Country
	}
	if patch.SetRegion {
		current.Region = patch.Region
	}
	if patch.SetCity {
		current.City = patch.City
	}
	if patch.SetLatitude {
		current.Latitude = patch.Latitude
		current.Longitude = patch.Longitude
	}
	if patch.Trusted != nil {
		current.Trusted = *patch.Trusted
	}
}

func scanIPMetadata(row pgx.Row) (domain.IPMetadataOverride, error) {
	var item domain.IPMetadataOverride
	var domainName, organization, country, region, city pgtype.Text
	var asn pgtype.Int8
	var latitude, longitude pgtype.Float8
	err := row.Scan(&item.IP, &domainName, &asn, &organization, &country, &region, &city,
		&latitude, &longitude, &item.Trusted, &item.UpdatedBy, &item.UpdatedAt)
	if err != nil {
		return domain.IPMetadataOverride{}, err
	}
	item.Exists = true
	item.Domain = textPointer(domainName)
	if asn.Valid {
		value := uint32(asn.Int64)
		item.ASN = &value
	}
	item.Organization = textPointer(organization)
	item.Country = textPointer(country)
	item.Region = textPointer(region)
	item.City = textPointer(city)
	item.Latitude = floatPointer(latitude)
	item.Longitude = floatPointer(longitude)
	return item, nil
}

func (s *Store) EnrichedIP(ctx context.Context, ip string) (domain.Enrichment, error) {
	result := s.EnrichIP(ip)
	normalized, err := domain.NormalizeIP(ip)
	if err != nil {
		return result, nil
	}
	manual, err := s.IPMetadata(ctx, normalized)
	if err != nil {
		return domain.Enrichment{}, err
	}
	result.IP = normalized
	result.Automatic = domain.EnrichmentFields{
		ASN: result.ASN, Organization: result.Organization, Country: result.Country,
		Region: result.Region, City: result.City, Latitude: result.Latitude, Longitude: result.Longitude,
		CoordinatesAvailable: result.CoordinatesAvailable,
	}
	result.Sources = map[string]string{
		"asn":          enrichmentSource(result.ASN != 0, manual.ASN != nil),
		"organization": enrichmentSource(result.Organization != "", manual.Organization != nil),
		"country":      enrichmentSource(result.Country != "", manual.Country != nil),
		"region":       enrichmentSource(result.Region != "", manual.Region != nil),
		"city":         enrichmentSource(result.City != "", manual.City != nil),
		"coordinates":  enrichmentSource(result.CoordinatesAvailable, manual.Latitude != nil),
		"domain":       "unknown",
	}
	candidates, dnsSource, dnsUpdatedAt, dnsErr := s.dnsDomainCandidates(ctx, normalized)
	if dnsErr == nil {
		result.DomainCandidates = candidates
		if len(candidates) == 1 {
			result.Domain, result.DomainSource, result.UpdatedAt = candidates[0], dnsSource, dnsUpdatedAt
			result.Sources["domain"] = dnsSource
		} else if len(candidates) > 1 {
			result.DomainSource = "ambiguous"
			result.Sources["domain"] = "ambiguous"
		}
	}
	ptr, ptrErr := s.readPTRCache(ctx, normalized)
	if ptrErr == nil && ptr != nil {
		result.PTRStatus, result.PTRReason, result.PTRUpdatedAt = ptr.Status, ptr.Reason, ptr.UpdatedAt
		if ptr.Hostname != "" && len(candidates) == 0 {
			result.Domain, result.DomainSource = ptr.Hostname, "ptr"
			result.Sources["domain"] = "ptr"
		}
	} else if len(candidates) == 0 && enrichment.IsPublicIP(normalized) {
		result.PTRStatus = "pending"
		s.enqueuePTR(normalized)
	} else if !enrichment.IsPublicIP(normalized) {
		result.PTRStatus, result.PTRReason = "skipped", "private_or_reserved"
	}
	if manual.ASN != nil {
		result.ASN = *manual.ASN
	}
	if manual.Organization != nil {
		result.Organization = *manual.Organization
	}
	if manual.Country != nil {
		result.Country = *manual.Country
	}
	if manual.Region != nil {
		result.Region = *manual.Region
	}
	if manual.City != nil {
		result.City = *manual.City
	}
	if manual.Latitude != nil && manual.Longitude != nil {
		result.Latitude, result.Longitude = *manual.Latitude, *manual.Longitude
		result.CoordinatesAvailable = true
	}
	if manual.Domain != nil {
		result.Domain, result.DomainSource = *manual.Domain, "manual"
		result.Sources["domain"] = "manual"
	}
	result.Trusted = manual.Trusted
	if manual.Exists {
		result.Manual = &manual
	}
	return result, nil
}

type ptrCacheRecord struct {
	Hostname  string
	Status    string
	Reason    string
	ExpiresAt time.Time
	UpdatedAt time.Time
}

func (s *Store) dnsDomainCandidates(ctx context.Context, ip string) ([]string, string, time.Time, error) {
	rows, err := s.ClickHouse.QueryContext(ctx, `
		SELECT domain, source, max(observed_at) AS updated_at
		FROM observer.dns_observations
		WHERE ip = ? AND expires_at > now64(3)
		GROUP BY domain, source
		ORDER BY updated_at DESC LIMIT 32`, ip)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	defer rows.Close()
	latest := make(map[string]struct {
		source    string
		updatedAt time.Time
	})
	for rows.Next() {
		var name, source string
		var updatedAt time.Time
		if err := rows.Scan(&name, &source, &updatedAt); err != nil {
			return nil, "", time.Time{}, err
		}
		name = normalizeDomain(name)
		if name != "" {
			if old, ok := latest[name]; !ok || updatedAt.After(old.updatedAt) {
				latest[name] = struct {
					source    string
					updatedAt time.Time
				}{source: source, updatedAt: updatedAt}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", time.Time{}, err
	}
	names := make([]string, 0, len(latest))
	var source string
	var updatedAt time.Time
	for name, item := range latest {
		names = append(names, name)
		if item.updatedAt.After(updatedAt) {
			updatedAt = item.updatedAt
			source = item.source
		}
	}
	sort.Strings(names)
	if len(names) > 1 {
		source = "ambiguous"
	}
	return names, source, updatedAt, nil
}

func (s *Store) readPTRCache(ctx context.Context, ip string) (*ptrCacheRecord, error) {
	var item ptrCacheRecord
	err := s.Postgres.QueryRow(ctx, `
		SELECT hostname, status, reason, expires_at, updated_at
		FROM ip_ptr_cache WHERE ip_address = $1::inet AND expires_at > now()`, ip).
		Scan(&item.Hostname, &item.Status, &item.Reason, &item.ExpiresAt, &item.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *Store) enqueuePTR(ip string) {
	if !enrichment.IsPublicIP(ip) || s.ptrQueue == nil {
		return
	}
	s.ptrMu.Lock()
	if s.ptrPending[ip] {
		s.ptrMu.Unlock()
		return
	}
	s.ptrPending[ip] = true
	s.ptrMu.Unlock()
	select {
	case s.ptrQueue <- ip:
	default:
		s.ptrMu.Lock()
		delete(s.ptrPending, ip)
		s.ptrMu.Unlock()
	}
}

func (s *Store) runPTRWorker() {
	defer s.ptrWorkers.Done()
	for {
		select {
		case <-s.ptrContext.Done():
			return
		case ip := <-s.ptrQueue:
			ctx, cancel := context.WithTimeout(s.ptrContext, 3*time.Second)
			hostnames, err := net.DefaultResolver.LookupAddr(ctx, ip)
			cancel()
			status, reason, hostname, ttl := "no_record", "no_ptr_record", "", 24*time.Hour
			if err == nil {
				for _, candidate := range hostnames {
					candidate = normalizeDomain(candidate)
					if candidate != "" {
						hostname = candidate
						break
					}
				}
				if hostname != "" {
					status, reason, ttl = "matched", "", 7*24*time.Hour
				}
			} else {
				status, reason, ttl = "error", "lookup_failed", 15*time.Minute
			}
			ctx, cancel = context.WithTimeout(s.ptrContext, 3*time.Second)
			_, _ = s.Postgres.Exec(ctx, `
				INSERT INTO ip_ptr_cache (ip_address, hostname, status, reason, expires_at, updated_at)
				VALUES ($1::inet, $2, $3, $4, now() + $5 * interval '1 second', now())
				ON CONFLICT (ip_address) DO UPDATE SET hostname=EXCLUDED.hostname, status=EXCLUDED.status,
					reason=EXCLUDED.reason, expires_at=EXCLUDED.expires_at, updated_at=now()`,
				ip, hostname, status, reason, ttl.Seconds())
			cancel()
			s.ptrMu.Lock()
			delete(s.ptrPending, ip)
			s.ptrMu.Unlock()
		}
	}
}

func enrichmentSource(automatic, manual bool) string {
	if manual {
		return "manual"
	}
	if automatic {
		return "geolite"
	}
	return "unknown"
}

func textPointer(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	copy := value.String
	return &copy
}

func floatPointer(value pgtype.Float8) *float64 {
	if !value.Valid {
		return nil
	}
	copy := value.Float64
	return &copy
}

func optionalString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func optionalUint32(value *uint32) any {
	if value == nil {
		return nil
	}
	return *value
}

func optionalFloat64(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}
