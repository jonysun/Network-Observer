package server

import (
	"testing"
	"time"
)

func TestDefaultOverviewRankingWindowUsesCompletedMinute(t *testing.T) {
	input := time.Date(2026, time.November, 19, 12, 34, 56, 789, time.FixedZone("test", 8*60*60))
	from, to := defaultOverviewRankingWindow(input)
	wantTo := input.UTC().Truncate(time.Minute)
	if !to.Equal(wantTo) {
		t.Fatalf("window end = %s, want completed minute %s", to, wantTo)
	}
	if got := to.Sub(from); got != 24*time.Hour {
		t.Fatalf("window duration = %s, want 24h", got)
	}
	if from.Second() != 0 || from.Nanosecond() != 0 {
		t.Fatalf("window start is not minute-aligned: %s", from)
	}
}

func TestOverviewRankingCacheKeyCanonicalizesDimensions(t *testing.T) {
	from := time.Date(2026, time.November, 18, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	first := overviewRankingCacheKey([]string{"device", "domain", "device"}, 10, "all", "traffic", from, to)
	second := overviewRankingCacheKey([]string{"domain", "device"}, 10, "all", "traffic", from, to)
	if first != second {
		t.Fatalf("equivalent dimension sets produced different cache keys: %q != %q", first, second)
	}
	if first == overviewRankingCacheKey([]string{"device", "domain"}, 20, "all", "traffic", from, to) {
		t.Fatal("different limits must not share a cache key")
	}
}

func TestOverviewRankingCacheExpiresAtDeadline(t *testing.T) {
	server := &Server{rankingCache: make(map[string]overviewRankingCacheEntry)}
	key := "test"
	expires := time.Now().UTC().Add(time.Hour)
	server.putOverviewRankingCache(key, overviewRankingCacheEntry{expiresAt: expires})
	if _, ok := server.getOverviewRankingCache(key, expires.Add(-time.Nanosecond)); !ok {
		t.Fatal("cache entry expired before its deadline")
	}
	if _, ok := server.getOverviewRankingCache(key, expires); ok {
		t.Fatal("cache entry remained valid at its expiration deadline")
	}
}
