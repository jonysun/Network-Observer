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

const lanActiveObservationWindow = 2 * time.Minute

type lanIPConflictTracker struct {
	observerMACs map[string]map[string]map[string]struct{}
}

func (tracker *lanIPConflictTracker) observe(observerID, interfaceName, mac string) {
	if observerID == "" || interfaceName == "" || mac == "" {
		return
	}
	if tracker.observerMACs == nil {
		tracker.observerMACs = make(map[string]map[string]map[string]struct{})
	}
	if tracker.observerMACs[observerID] == nil {
		tracker.observerMACs[observerID] = make(map[string]map[string]struct{})
	}
	if tracker.observerMACs[observerID][interfaceName] == nil {
		tracker.observerMACs[observerID][interfaceName] = make(map[string]struct{})
	}
	tracker.observerMACs[observerID][interfaceName][mac] = struct{}{}
}

func (tracker lanIPConflictTracker) confirmed() bool {
	for _, interfaces := range tracker.observerMACs {
		for _, macs := range interfaces {
			if len(macs) > 1 {
				return true
			}
		}
	}
	return false
}

func canonicalCIDR(value string) string {
	_, network, err := net.ParseCIDR(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return network.String()
}

func dockerSubnetOwners(networks []domain.DockerNetwork) map[string]map[string]struct{} {
	result := make(map[string]map[string]struct{})
	for _, network := range networks {
		for _, subnet := range network.SubnetCIDRs {
			if cidr := canonicalCIDR(subnet); cidr != "" {
				if result[cidr] == nil {
					result[cidr] = make(map[string]struct{})
				}
				result[cidr][network.OwnerAgentID] = struct{}{}
			}
		}
	}
	return result
}

func isDockerSubnetOwner(networkID, observerID string, dockerOwners map[string]map[string]struct{}) bool {
	_, ok := dockerOwners[canonicalCIDR(networkID)][observerID]
	return ok
}

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
	dockerNetworks, err := s.ListDockerNetworks(ctx)
	if err != nil {
		return nil, err
	}
	dockerOwners := dockerSubnetOwners(dockerNetworks)

	rows, err := s.Postgres.Query(ctx, `
		SELECT n.network_id, n.ip_address, n.mac_address, n.interface_name, n.state, n.is_local,
		       n.observer_device_id::text, COALESCE(d.name, ''), COALESCE(d.device_category, 'unknown'), n.last_seen,
		       COALESCE(a.alias, ''), COALESCE(a.device_category, 'unknown')
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
		device        domain.LANDevice
		addresses     map[string]int
		observers     map[string]int
		alias         string
		category      string
		agentName     string
		agentCategory string
		detectedCategory string
		localSeen     time.Time
	}
	groups := make(map[string]*aggregate)
	macsByObserverIP := make(map[string]*lanIPConflictTracker)
	for rows.Next() {
		var networkID, ip, mac, iface, state, observerID, observerName, observerCategory, alias, aliasCategory string
		var isLocal bool
		var lastSeen time.Time
		if err := rows.Scan(&networkID, &ip, &mac, &iface, &state, &isLocal, &observerID, &observerName, &observerCategory, &lastSeen, &alias, &aliasCategory); err != nil {
			return nil, err
		}
		key := networkID + "|" + mac
		if mac == "" {
			key += "|" + ip
			if state != "gateway" {
				// A non-gateway observation without a MAC is not a stable
				// device identity, so keep it scoped to its observing Agent.
				key += "|" + observerID
			}
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
		if state == "gateway" {
			item.detectedCategory = "gateway"
		}
		if !isLocal && !isDockerSubnetOwner(networkID, observerID, dockerOwners) && mac != "" && time.Since(lastSeen) <= lanActiveObservationWindow {
			identity := networkID + "|" + ip
			tracker := macsByObserverIP[identity]
			if tracker == nil {
				tracker = &lanIPConflictTracker{}
				macsByObserverIP[identity] = tracker
			}
			tracker.observe(observerID, iface, mac)
		}
		if isLocal && !contains(item.device.LocalAgentIDs, observerID) {
			item.device.LocalAgentIDs = append(item.device.LocalAgentIDs, observerID)
			sort.Strings(item.device.LocalAgentIDs)
		}
		if alias != "" {
			item.alias = alias
		}
		if aliasCategory != "unknown" {
			item.category = aliasCategory
		}
		if isLocal && observerName != "" && (lastSeen.After(item.localSeen) || item.agentName == "") {
			item.agentName, item.agentCategory, item.localSeen = observerName, observerCategory, lastSeen
		}
		if index, ok := item.addresses[ip]; ok {
			address := &item.device.Addresses[index]
			if lastSeen.After(address.LastSeen) {
				address.LastSeen = lastSeen
				if state == "gateway" || address.State == "" {
					address.State = state
				}
			} else if state == "gateway" {
				// A default-route observation is stronger than a later ARP/NDP
				// state for the same address. Keep it so topology classification
				// remains stable even when neighbor refreshes race the route scan.
				address.State = state
			}
			if iface != "" && !contains(address.Interfaces, iface) {
				address.Interfaces = append(address.Interfaces, iface)
				sort.Strings(address.Interfaces)
			}
		} else {
			item.addresses[ip] = len(item.device.Addresses)
			interfaces := make([]string, 0, 1)
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
	for _, item := range groups {
		item.device.Name, item.device.NameSource = "未知设备", "unknown"
		item.device.DeviceCategory, item.device.CategorySource = "unknown", "unknown"
		if item.agentName != "" {
			item.device.Name, item.device.NameSource = item.agentName, "agent"
		}
		if item.agentCategory != "" && item.agentCategory != "unknown" {
			item.device.DeviceCategory, item.device.CategorySource = item.agentCategory, "agent"
		}
		if item.detectedCategory != "" {
			item.device.DeviceCategory, item.device.CategorySource = item.detectedCategory, "detected"
		}
		if item.alias != "" {
			item.device.Name, item.device.NameSource = item.alias, "manual"
		}
		if item.category != "" && item.category != "unknown" {
			item.device.DeviceCategory, item.device.CategorySource = item.category, "manual"
		}
		item.device.Stale = time.Since(item.device.LastSeen) > 2*time.Minute
		sort.Slice(item.device.Addresses, func(i, j int) bool { return item.device.Addresses[i].IP < item.device.Addresses[j].IP })
		sort.Slice(item.device.Observers, func(i, j int) bool { return item.device.Observers[i].Name < item.device.Observers[j].Name })
		devices = append(devices, item.device)
	}
	for i := range devices {
		for _, address := range devices[i].Addresses {
			if tracker := macsByObserverIP[devices[i].NetworkID+"|"+address.IP]; tracker != nil && tracker.confirmed() {
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
	networks := make([]string, 0)
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
	return s.SaveLANProfile(ctx, networkID, mac, alias, "", actor, requestID)
}

func (s *Store) SaveLANProfile(ctx context.Context, networkID, mac, alias, category, actor, requestID string) error {
	alias = strings.TrimSpace(alias)
	category = strings.TrimSpace(strings.ToLower(category))
	if len(alias) > 128 {
		return fmt.Errorf("LAN device name must be at most 128 characters")
	}
	if category != "" && !domain.ValidDeviceCategory(category) {
		return fmt.Errorf("invalid device category")
	}
	if alias == "" && category == "unknown" {
		if _, err := s.Postgres.Exec(ctx, `DELETE FROM lan_device_aliases WHERE network_id = $1 AND mac_address = $2`, networkID, mac); err != nil {
			return err
		}
		return s.audit(ctx, actor, "delete", "lan_device_alias", networkID+"|"+mac, requestID, nil)
	}
	_, err := s.Postgres.Exec(ctx, `
		INSERT INTO lan_device_aliases (network_id, mac_address, alias, device_category, updated_by, updated_at)
		VALUES ($1, $2, $3, COALESCE(NULLIF($4, ''), 'unknown'), $5, now())
		ON CONFLICT (network_id, mac_address)
		DO UPDATE SET alias = EXCLUDED.alias, device_category = CASE WHEN $4 = '' THEN lan_device_aliases.device_category ELSE EXCLUDED.device_category END, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		networkID, mac, alias, category, actor)
	if err != nil {
		return err
	}
	detailCategory := category
	if detailCategory == "" {
		detailCategory = "unchanged"
	}
	return s.audit(ctx, actor, "upsert", "lan_device_alias", networkID+"|"+mac, requestID, map[string]any{"alias": alias, "device_category": detailCategory})
}

func contains(items []string, value string) bool {
	for _, item := range items {
		if strings.EqualFold(item, value) {
			return true
		}
	}
	return false
}
