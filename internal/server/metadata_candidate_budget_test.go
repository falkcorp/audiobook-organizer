// file: internal/server/metadata_candidate_budget_test.go
// version: 1.0.0
// guid: 9d4b2e61-7a3c-4f18-b5e0-6c2a8f1d3e94
// last-edited: 2026-10-01

package server

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"golang.org/x/time/rate"
)

// The candidate fetch's gate is the enabled sources' summed budget, not a
// fixed 10/s, and its pool is sized from that budget within [16, 32] unless
// configured.
func TestCandidateFetchBudget(t *testing.T) {
	prev := config.AppConfig.MetadataSources
	t.Cleanup(func() { config.AppConfig.MetadataSources = prev })
	config.AppConfig.MetadataSources = []config.MetadataSource{
		{ID: "audible", Enabled: true, RateLimit: config.MetadataSourceRateLimit{RPS: 8, Burst: 4}},
		{ID: "openlibrary", Enabled: true, RateLimit: config.MetadataSourceRateLimit{RPS: 5, Burst: 2}},
		{ID: "google-books", Enabled: false, RateLimit: config.MetadataSourceRateLimit{RPS: 50, Burst: 50}},
	}
	rps, burst := metafetch.EnabledSourcesBudget()
	if rps != 13 || burst != 6 {
		t.Fatalf("budget = %.1f/%d, want 13/6 (disabled sources excluded)", rps, burst)
	}
	if l := candidateFetchLimiter(rps, burst); l.Limit() != rate.Limit(13) || l.Burst() != 6 {
		t.Errorf("limiter = %v/%d, want 13/6", l.Limit(), l.Burst())
	}
	if l := candidateFetchLimiter(0, 0); l.Limit() != rate.Limit(candidateFetchFallbackRPS) || l.Burst() != 1 {
		t.Errorf("no budget: limiter = %v/%d, want the fallback", l.Limit(), l.Burst())
	}
	for _, tc := range []struct {
		rps        float64
		configured int
		want       int
	}{
		{13, 0, 16},  // 13 x 0.15 x 4 = 7.8 -> floor 16
		{40, 0, 24},  // 40 x 0.6 = 24
		{100, 0, 32}, // ceiling
		{13, 12, 12}, // configured wins
		{13, 500, 64},
	} {
		if got := candidateFetchWorkers(tc.rps, tc.configured); got != tc.want {
			t.Errorf("candidateFetchWorkers(%v, %d) = %d, want %d", tc.rps, tc.configured, got, tc.want)
		}
	}
}
