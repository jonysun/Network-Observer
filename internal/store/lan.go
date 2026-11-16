package store

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/network-observer/network-observer/internal/domain"
)

func (s *Store) IngestNeighborSnapshot(ctx context.Context, observations []domain.NeighborObservation) error {
	if len(observations) == 0 {
		return nil
	}
	tx, err := s.Postgres.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, item := range observations {
		_, err := tx.Exec(ctx, `
			INSERT INTO lan_neighbor_sightings (
				observer_device_id, network_id, network_prefix, ip_address, mac_address, interface_name, state, is_local, last_seen
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (observer_device_id, network_id, ip_address, mac_address, interface_name)
			DO UPDATE SET network_prefix = EXCLUDED.network_prefix, state = EXCLUDED.state, is_local = EXCLUDED.is_local, last_seen = EXCLUDED.last_seen`,
			item.ObserverDeviceID, item.NetworkID, item.NetworkPrefix, item.IP, item.MAC, item.Interface, item.State, item.IsLocal, item.LastSeen)
		if err != nil {
			return fmt.Errorf("upsert LAN neighbor %s: %w", item.IP, err)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM lan_neighbor_sightings WHERE last_seen < now() - interval '30 days'`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ListLANDevices(ctx context.Context) ([]domain.LANDevice, error) {
	rows, err := s.Postgres.Query(ctx, `
		SELECT n.network_id, n.ip_address, n.mac_address, n.interface_name, n.state, n.is_local,
		       n.observer_device_id::text, COALESCE(d.name, ''), n.last_seen, COALESCE(a.alias, '')
		FROM lan_neighbor_sightings AS n
		LEFT JOIN devices AS d ON d.id = n.observer_device_id AND d.revoked_at IS NULL
		LEFT JOIN lan_device_aliases AS a ON a.network_id = n.network_id AND a.mac_address = n.mac_address
		WHERE n.last_seen >= now() - interval '30 days'
		ORDER BY n.network_id, n.mac_address, n.ip_address, n.last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type aggregate struct {
		device    domain.LANDevice
		addresses map[string]int
		observers map[string]int
		alias     string
		agentName string
	}
	groups := make(map[string]*aggregate)
	for rows.Next() {
		var networkID, ip, mac, iface, state, observerID, observerName, alias string
		var isLocal bool
		var lastSeen time.Time
		if err := rows.Scan(&networkID, &ip, &mac, &iface, &state, &isLocal, &observerID, &observerName, &lastSeen, &alias); err != nil {
			return nil, err
		}
		key := networkID + "|" + mac
		if mac == "" {
			key += "|" + ip + "|" + observerID
		}
		item := groups[key]
		if item == nil {
			item = &aggregate{
				device:    domain.LANDevice{Key: key, NetworkID: networkID, MAC: mac, Addresses: make([]domain.LANAddress, 0), Observers: make([]domain.LANObserver, 0)},
				addresses: make(map[string]int), observers: make(map[string]int),
			}
			groups[key] = item
		}
		if lastSeen.After(item.device.LastSeen) {
			item.device.LastSeen = lastSeen
		}
		if alias != "" {
			item.alias = alias
		}
		if isLocal && observerName != "" {
			item.agentName = observerName
		}
		if index, ok := item.addresses[ip]; ok {
			address := &item.device.Addresses[index]
			if lastSeen.After(address.LastSeen) {
				address.LastSeen, address.State = lastSeen, state
			}
			if iface != "" && !contains(address.Interfaces, iface) {
				address.Interfaces = append(address.Interfaces, iface)
				sort.Strings(address.Interfaces)
			}
		} else {
			item.addresses[ip] = len(item.device.Addresses)
			interfaces := []string(nil)
			if iface != "" {
				interfaces = []string{iface}
			}
			item.device.Addresses = append(item.device.Addresses, domain.LANAddress{IP: ip, Interfaces: interfaces, State: state, LastSeen: lastSeen})
		}
		if _, ok := item.observers[observerID]; !ok {
			item.observers[observerID] = len(item.device.Observers)
			item.device.Observers = append(item.device.Observers, domain.LANObserver{ID: observerID, Name: observerName, LastSeen: lastSeen})
		} else {
			observer := &item.device.Observers[item.observers[observerID]]
			if lastSeen.After(observer.LastSeen) {
				observer.LastSeen = lastSeen
			}
			if observer.Name == "" {
				observer.Name = observerName
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	devices := make([]domain.LANDevice, 0, len(groups))
	macsByIP := make(map[string]map[string]struct{})
	for _, item := range groups {
		item.device.Name, item.device.NameSource = "未知设备", "unknown"
		if item.agentName != "" {
			item.device.Name, item.device.NameSource = item.agentName, "agent"
		}
		if item.alias != "" {
			item.device.Name, item.device.NameSource = item.alias, "manual"
		}
		item.device.Stale = time.Since(item.device.LastSeen) > 2*time.Minute
		sort.Slice(item.device.Addresses, func(i, j int) bool { return item.device.Addresses[i].IP < item.device.Addresses[j].IP })
		sort.Slice(item.device.Observers, func(i, j int) bool { return item.device.Observers[i].Name < item.device.Observers[j].Name })
		for _, address := range item.device.Addresses {
			if item.device.MAC == "" {
				continue
			}
			identity := item.device.NetworkID + "|" + address.IP
			if macsByIP[identity] == nil {
				macsByIP[identity] = make(map[string]struct{})
			}
			macsByIP[identity][item.device.MAC] = struct{}{}
		}
		devices = append(devices, item.device)
	}
	for i := range devices {
		for _, address := range devices[i].Addresses {
			if len(macsByIP[devices[i].NetworkID+"|"+address.IP]) > 1 {
				devices[i].IPConflict = true
				break
			}
		}
	}
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].NetworkID != devices[j].NetworkID {
			return devices[i].NetworkID < devices[j].NetworkID
		}
		return devices[i].Name < devices[j].Name
	})
	return devices, nil
}

func (s *Store) ListLANNetworks(ctx context.Context) ([]string, error) {
	rows, err := s.Postgres.Query(ctx, `
		SELECT DISTINCT network_prefix FROM lan_neighbor_sightings
		WHERE is_local AND network_prefix <> '' AND last_seen >= now() - interval '2 minutes' ORDER BY network_prefix`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var networks []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		if _, _, err := net.ParseCIDR(value); err == nil {
			networks = append(networks, value)
		}
	}
	return networks, rows.Err()
}

func (s *Store) SaveLANAlias(ctx context.Context, networkID, mac, alias, actor, requestID string) error {
	if alias == "" {
		if _, err := s.Postgres.Exec(ctx, `DELETE FROM lan_device_aliases WHERE network_id = $1 AND mac_address = $2`, networkID, mac); err != nil {
			return err
		}
		return s.audit(ctx, actor, "delete", "lan_device_alias", networkID+"|"+mac, requestID, nil)
	}
	_, err := s.Postgres.Exec(ctx, `
		INSERT INTO lan_device_aliases (network_id, mac_address, alias, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (network_id, mac_address)
		DO UPDATE SET alias = EXCLUDED.alias, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		networkID, mac, alias, actor)
	if err != nil {
		return err
	}
	return s.audit(ctx, actor, "upsert", "lan_device_alias", networkID+"|"+mac, requestID, map[string]any{"alias": alias})
}

func contains(items []string, value string) bool {
	for _, item := range items {
		if strings.EqualFold(item, value) {
			return true
		}
	}
	return false
}
