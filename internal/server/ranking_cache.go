package server

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const overviewRankingCacheTTL = 35 * time.Second

func defaultOverviewRankingWindow(at time.Time) (time.Time, time.Time) {
	to := at.UTC().Truncate(time.Minute)
	return to.Add(-24 * time.Hour), to
}

func overviewRankingCacheKey(dimensions []string, limit uint32, trustFilter, sortBy string, from, to time.Time) string {
	canonical := append([]string(nil), dimensions...)
	sort.Strings(canonical)
	unique := canonical[:0]
	for _, dimension := range canonical {
		if len(unique) == 0 || unique[len(unique)-1] != dimension {
			unique = append(unique, dimension)
		}
	}
	return fmt.Sprintf("%s|%d|%s|%s|%d|%d", strings.Join(unique, ","), limit, trustFilter, sortBy, from.UTC().UnixNano(), to.UTC().UnixNano())
}

func (s *Server) getOverviewRankingCache(key string, at time.Time) (overviewRankingCacheEntry, bool) {
	s.rankingCacheMu.Lock()
	defer s.rankingCacheMu.Unlock()
	entry, ok := s.rankingCache[key]
	if !ok || !at.Before(entry.expiresAt) {
		delete(s.rankingCache, key)
		return overviewRankingCacheEntry{}, false
	}
	return entry, true
}

func (s *Server) putOverviewRankingCache(key string, entry overviewRankingCacheEntry) {
	s.rankingCacheMu.Lock()
	defer s.rankingCacheMu.Unlock()
	currentTime := now().UTC()
	for oldKey, oldEntry := range s.rankingCache {
		if !currentTime.Before(oldEntry.expiresAt) {
			delete(s.rankingCache, oldKey)
		}
	}
	if len(s.rankingCache) >= 128 {
		for oldKey := range s.rankingCache {
			delete(s.rankingCache, oldKey)
			break
		}
	}
	s.rankingCache[key] = entry
}
