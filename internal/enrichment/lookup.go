package enrichment

import (
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/oschwald/geoip2-golang"
)

// Record is an offline ASN/city lookup result. Lookups use local
// MaxMind-compatible MMDB files; no observed address leaves the center.
type Record struct {
	ASN                                 uint32
	Organization, Country, Region, City string
	Latitude, Longitude                 float64
	Version                             string
}

type Lookup struct {
	mu         sync.RWMutex
	asn, city  *geoip2.Reader
	version    string
	updatedAt  time.Time
	configured bool
}

func LoadMMDB(asnPath, cityPath string) (*Lookup, error) {
	l := &Lookup{updatedAt: time.Now().UTC()}
	for _, item := range []struct {
		path   string
		target **geoip2.Reader
		name   string
	}{
		{strings.TrimSpace(asnPath), &l.asn, "ASN"},
		{strings.TrimSpace(cityPath), &l.city, "city"},
	} {
		if item.path == "" {
			continue
		}
		reader, err := geoip2.Open(item.path)
		if err != nil {
			l.Close()
			return nil, fmt.Errorf("open %s MMDB: %w", item.name, err)
		}
		*item.target = reader
		if info, err := os.Stat(item.path); err == nil && info.ModTime().After(l.updatedAt.Add(-time.Second)) {
			l.updatedAt = info.ModTime().UTC()
		}
		l.configured = true
		if l.version == "" {
			metadata := reader.Metadata()
			l.version = fmt.Sprintf("%s:%d", metadata.DatabaseType, metadata.BuildEpoch)
			if metadata.DatabaseType == "" {
				l.version = strings.TrimSuffix(filepathBase(item.path), ".mmdb")
			}
		}
	}
	return l, nil
}

func filepathBase(path string) string {
	path = strings.TrimRight(path, `/\\`)
	if index := strings.LastIndexAny(path, `/\\`); index >= 0 {
		return path[index+1:]
	}
	return path
}

func (l *Lookup) Lookup(ip string) (Record, string, string) {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return Record{}, l.Version(), "invalid_ip"
	}
	if !isPublic(parsed) {
		return Record{}, l.Version(), "private_or_reserved"
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	var record Record
	matched := false
	if l.asn != nil {
		if result, err := l.asn.ASN(parsed); err == nil && result.AutonomousSystemNumber != 0 {
			record.ASN = uint32(result.AutonomousSystemNumber)
			record.Organization = result.AutonomousSystemOrganization
			matched = true
		}
	}
	if l.city != nil {
		if result, err := l.city.City(parsed); err == nil {
			if result.Country.IsoCode != "" {
				record.Country = result.Country.IsoCode
				matched = true
			}
			for _, subdivision := range result.Subdivisions {
				if record.Region == "" {
					record.Region = subdivision.Names["en"]
				}
			}
			record.City = result.City.Names["en"]
			record.Latitude, record.Longitude = result.Location.Latitude, result.Location.Longitude
		}
	}
	record.Version = l.version
	if matched {
		return record, l.version, "matched"
	}
	if !l.configured {
		return Record{}, l.version, "database_unconfigured"
	}
	return Record{}, l.version, "no_match"
}

func isPublic(ip net.IP) bool {
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	// Filter special-purpose ranges that net.IP's helpers intentionally leave public-looking.
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "2001:db8::/32"} {
		_, network, _ := net.ParseCIDR(cidr)
		if network.Contains(ip) {
			return false
		}
	}
	return true
}

func (l *Lookup) Version() string      { l.mu.RLock(); defer l.mu.RUnlock(); return l.version }
func (l *Lookup) UpdatedAt() time.Time { l.mu.RLock(); defer l.mu.RUnlock(); return l.updatedAt }

// ReplaceFromPaths loads a complete replacement before swapping it in. The
// write lock waits for active readers before closing their mapped databases.
func (l *Lookup) ReplaceFromPaths(asnPath, cityPath string) error {
	replacement, err := LoadMMDB(asnPath, cityPath)
	if err != nil {
		return err
	}
	l.mu.Lock()
	oldASN, oldCity := l.asn, l.city
	l.asn, l.city = replacement.asn, replacement.city
	l.version, l.updatedAt, l.configured = replacement.version, replacement.updatedAt, replacement.configured
	replacement.asn, replacement.city = nil, nil
	l.mu.Unlock()
	if oldASN != nil {
		_ = oldASN.Close()
	}
	if oldCity != nil {
		_ = oldCity.Close()
	}
	return nil
}

func (l *Lookup) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var first error
	if l.asn != nil {
		first = l.asn.Close()
		l.asn = nil
	}
	if l.city != nil {
		if err := l.city.Close(); first == nil {
			first = err
		}
		l.city = nil
	}
	return first
}
