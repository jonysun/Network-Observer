package store

import (
	"context"
	"net"
	"strings"
	"time"

	"github.com/network-observer/network-observer/internal/domain"
)

func dnsCacheKey(deviceID, ip string) string { return deviceID + "\x00" + ip }

func (s *Store) loadDNSCache(ctx context.Context) error {
	rows, err := s.ClickHouse.QueryContext(ctx, `SELECT device_id, observed_at, interface, domain, ip, ttl, source, expires_at FROM observer.dns_observations WHERE expires_at > now64(3) ORDER BY observed_at DESC LIMIT 100000`)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := make([]domain.DNSObservation, 0, 4096)
	for rows.Next() {
		var item domain.DNSObservation
		if err := rows.Scan(&item.DeviceID, &item.ObservedAt, &item.Interface, &item.Domain, &item.IP, &item.TTL, &item.Source, &item.ExpiresAt); err != nil {
			return err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.cacheDNSObservations(items)
	return nil
}

func (s *Store) cacheDNSObservations(items []domain.DNSObservation) {
	if len(items) == 0 {
		return
	}
	now := time.Now().UTC()
	s.dnsMu.Lock()
	defer s.dnsMu.Unlock()
	for _, item := range items {
		item.Domain = normalizeDomain(item.Domain)
		if item.ExpiresAt.IsZero() || !item.ExpiresAt.After(now) || item.Domain == "" || net.ParseIP(item.IP) == nil {
			continue
		}
		key := dnsCacheKey(item.DeviceID, item.IP)
		entries := s.dnsCache[key]
		updated := false
		for i := range entries {
			if entries[i].item.Domain == item.Domain && entries[i].item.ObservedAt.Equal(item.ObservedAt) {
				entries[i].item = item
				updated = true
				break
			}
		}
		if !updated {
			entries = append(entries, dnsCacheEntry{item: item})
		}
		fresh := entries[:0]
		for _, entry := range entries {
			if entry.item.ExpiresAt.After(now) {
				fresh = append(fresh, entry)
			}
		}
		if len(fresh) > 16 {
			fresh = fresh[len(fresh)-16:]
		}
		s.dnsCache[key] = fresh
	}
}

func (s *Store) applyDNSAttribution(observation *domain.FlowObservation) {
	if observation.Domain != "" {
		return
	}
	remoteIP := observation.DestinationIP
	if !observation.LocalIsSource {
		remoteIP = observation.SourceIP
	}
	s.dnsMu.RLock()
	entries := append([]dnsCacheEntry(nil), s.dnsCache[dnsCacheKey(observation.DeviceID, remoteIP)]...)
	s.dnsMu.RUnlock()
	names := make(map[string]struct{})
	for _, entry := range entries {
		if !entry.item.ObservedAt.After(observation.ObservedAt) && entry.item.ExpiresAt.After(observation.ObservedAt) {
			names[entry.item.Domain] = struct{}{}
		}
	}
	if len(names) == 1 {
		for name := range names {
			observation.Domain = name
		}
		observation.DomainSource = "dns_udp53"
	} else if len(names) > 1 {
		observation.DomainSource = "ambiguous"
	}
}

func normalizeDomain(value string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
}
