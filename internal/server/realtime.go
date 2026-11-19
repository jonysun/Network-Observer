package server

import (
	"context"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/network-observer/network-observer/internal/domain"
)

const realtimeExpiry = 15 * time.Second

var realtimeLANNetworks = []string{
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16",
	"fc00::/7", "fe80::/10", "::1/128",
}

func realtimeKey(deviceID, flowID string) string {
	return deviceID + "\x00" + flowID
}

func (s *Server) updateRealtime(deviceID string, observations []domain.FlowObservation) {
	nowAt := now().UTC()
	s.realtimeMu.Lock()
	defer s.realtimeMu.Unlock()
	if s.realtime == nil {
		s.realtime = make(map[string]realtimeObservation)
	}
	for key, item := range s.realtime {
		if item.observation.DeviceID == deviceID && nowAt.Sub(item.updatedAt) > realtimeExpiry {
			delete(s.realtime, key)
		}
	}
	for _, item := range observations {
		item.DeviceID = deviceID
		key := realtimeKey(deviceID, item.FlowID)
		if !item.Active {
			delete(s.realtime, key)
			continue
		}
		s.realtime[key] = realtimeObservation{observation: item, updatedAt: nowAt}
	}
	if s.metrics.active != nil {
		s.metrics.active.Set(float64(len(s.realtime)))
	}
}

func (s *Server) clearRealtimeDevice(deviceID string) {
	s.realtimeMu.Lock()
	for key, item := range s.realtime {
		if item.observation.DeviceID == deviceID {
			delete(s.realtime, key)
		}
	}
	total := len(s.realtime)
	s.realtimeMu.Unlock()
	if s.metrics.active != nil {
		s.metrics.active.Set(float64(total))
	}
}

func (s *Server) realtimeAvailable(deviceID string) bool {
	nowAt := now().UTC()
	s.realtimeMu.Lock()
	defer s.realtimeMu.Unlock()
	available := false
	for key, saved := range s.realtime {
		if nowAt.Sub(saved.updatedAt) > realtimeExpiry {
			delete(s.realtime, key)
			continue
		}
		if deviceID == "" || saved.observation.DeviceID == deviceID {
			available = true
		}
	}
	return available
}

func (s *Server) listRealtimeFiltered(deviceID, protocol, query string, hideInternal bool, networks []string, limit, offset uint32) ([]domain.FlowObservation, uint64) {
	nowAt := now().UTC()
	needle := strings.ToLower(strings.TrimSpace(query))
	normalizedNetworks := append(append([]string(nil), realtimeLANNetworks...), networks...)
	prefixes := make([]*net.IPNet, 0, len(normalizedNetworks))
	for _, value := range normalizedNetworks {
		_, prefix, err := net.ParseCIDR(strings.TrimSpace(value))
		if err == nil {
			prefixes = append(prefixes, prefix)
		}
	}
	items := make([]domain.FlowObservation, 0)
	s.realtimeMu.Lock()
	for key, saved := range s.realtime {
		if nowAt.Sub(saved.updatedAt) > realtimeExpiry {
			delete(s.realtime, key)
			continue
		}
		item := saved.observation
		if deviceID != "" && item.DeviceID != deviceID {
			continue
		}
		if protocol != "" && strings.ToLower(string(item.Protocol)) != strings.ToLower(protocol) {
			continue
		}
		if hideInternal && realtimeBothInternal(item.SourceIP, item.DestinationIP, prefixes) {
			continue
		}
		if needle != "" && !realtimeMatches(item, needle) {
			continue
		}
		items = append(items, item)
	}
	totalActive := len(s.realtime)
	s.realtimeMu.Unlock()
	sort.SliceStable(items, func(i, j int) bool {
		left := items[i].RateBytesSourceToDest + items[i].RateBytesDestToSource
		right := items[j].RateBytesSourceToDest + items[j].RateBytesDestToSource
		if left != right {
			return left > right
		}
		return items[i].ID < items[j].ID
	})
	total := uint64(len(items))
	if limit == 0 {
		limit = 100
	}
	start := int(offset)
	if start >= len(items) {
		if s.metrics.active != nil {
			s.metrics.active.Set(float64(totalActive))
		}
		return []domain.FlowObservation{}, total
	}
	end := start + int(limit)
	if end > len(items) {
		end = len(items)
	}
	if s.metrics.active != nil {
		s.metrics.active.Set(float64(totalActive))
	}
	return items[start:end], total
}

// listConnectionsMerged combines the short-lived realtime cache with the
// durable active-connection table. Realtime observations win for the same
// device/flow key, while older agents that have no current realtime stream are
// still visible. Pagination is applied only after the merge.
func (s *Server) listConnectionsMerged(ctx context.Context, deviceID, protocol, query string, hideInternal bool, networks []string, limit, offset uint32) ([]domain.FlowObservation, uint64, error) {
	const fetchLimit = uint32(10000)
	realtimeItems, _ := s.listRealtimeFiltered(deviceID, protocol, query, hideInternal, networks, fetchLimit, 0)
	storedItems, _, err := s.store.ListConnectionsFiltered(ctx, deviceID, protocol, query, hideInternal, networks, fetchLimit, 0)
	if err != nil {
		return nil, 0, err
	}
	items := mergeConnectionObservations(storedItems, realtimeItems)
	return paginateConnections(items, limit, offset), uint64(len(items)), nil
}

// mergeConnectionObservations merges durable and live views by device and flow.
// Realtime observations win so a delayed WAL replay can never overwrite a
// current socket state with an older minute aggregate.
func mergeConnectionObservations(stored, realtime []domain.FlowObservation) []domain.FlowObservation {
	merged := make(map[string]domain.FlowObservation, len(stored)+len(realtime))
	for _, item := range stored {
		merged[item.DeviceID+"\x00"+item.FlowID] = item
	}
	for _, item := range realtime {
		merged[item.DeviceID+"\x00"+item.FlowID] = item
	}
	items := make([]domain.FlowObservation, 0, len(merged))
	for _, item := range merged {
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		left := items[i].RateBytesSourceToDest + items[i].RateBytesDestToSource
		right := items[j].RateBytesSourceToDest + items[j].RateBytesDestToSource
		if left != right {
			return left > right
		}
		return items[i].DeviceID+"\x00"+items[i].FlowID < items[j].DeviceID+"\x00"+items[j].FlowID
	})
	return items
}

func paginateConnections(items []domain.FlowObservation, limit, offset uint32) []domain.FlowObservation {
	if limit == 0 {
		limit = 100
	}
	start := int(offset)
	if start >= len(items) {
		return []domain.FlowObservation{}
	}
	end := start + int(limit)
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}

// realtimeEligibleForDurableBatch filters delayed WAL replay from the live
// cache. Historical minute aggregates use a stable history- ID prefix and
// must remain queryable as history without appearing as current sockets.
func realtimeEligibleForDurableBatch(item domain.FlowObservation, at time.Time) bool {
	if strings.HasPrefix(item.ID, "history-") {
		return false
	}
	if item.ObservedAt.IsZero() {
		return false
	}
	age := at.Sub(item.ObservedAt.UTC())
	return age >= -5*time.Second && age <= realtimeExpiry
}

func realtimeMatches(item domain.FlowObservation, needle string) bool {
	values := []string{
		item.SourceIP, strconv.FormatUint(uint64(item.SourcePort), 10), item.DestinationIP,
		strconv.FormatUint(uint64(item.DestinationPort), 10), item.ProcessName, item.ContainerName,
		item.ServiceName, item.Domain, item.Organization, item.Country, item.Region, item.City,
		item.Interface, item.FlowID,
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), needle) {
			return true
		}
	}
	return false
}

func realtimeBothInternal(sourceIP, destinationIP string, prefixes []*net.IPNet) bool {
	source, destination := net.ParseIP(sourceIP), net.ParseIP(destinationIP)
	if source == nil || destination == nil {
		return false
	}
	sourceInternal, destinationInternal := false, false
	for _, prefix := range prefixes {
		sourceInternal = sourceInternal || prefix.Contains(source)
		destinationInternal = destinationInternal || prefix.Contains(destination)
	}
	return sourceInternal && destinationInternal
}
