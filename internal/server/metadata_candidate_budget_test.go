// file: internal/server/metadata_candidate_budget_test.go
// version: 1.2.0
// guid: 9d4b2e61-7a3c-4f18-b5e0-6c2a8f1d3e94
// last-edited: 2026-10-01

package server

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"golang.org/x/time/rate"
)

// The candidate fetch's gate is the enabled sources' summed budget, not a
// fixed 10/s; its pool is sized from that budget within [16, 32] unless
// configured, and never past what the slowest source drains within its
// timeout.
func TestCandidateFetchBudget(t *testing.T) {
	prev := config.AppConfig.MetadataSources
	t.Cleanup(func() { config.AppConfig.MetadataSources = prev })
	config.AppConfig.MetadataSources = []config.MetadataSource{
		{ID: "audible", Enabled: true, RateLimit: config.MetadataSourceRateLimit{RPS: 8, Burst: 4}},
		{ID: "openlibrary", Enabled: true, RateLimit: config.MetadataSourceRateLimit{RPS: 5, Burst: 2, TimeoutSeconds: 10}},
		{ID: "google-books", Enabled: false, RateLimit: config.MetadataSourceRateLimit{RPS: 0.1, Burst: 50}},
	}
	b := metafetch.EnabledSourcesBudget()
	// Audible and Open Library are fan-out sources: 4 calls per book each,
	// so Open Library (5/s / 4) binds at 1.25 books/s.
	if b.CallsPerBook != metafetch.MaxSearchCallsPerBook("audible") || b.BindingID != "openlibrary" || b.BooksPerSec != 5/float64(b.CallsPerBook) {
		t.Fatalf("binding = %s %.2f books/s at %d calls/book", b.BindingID, b.BooksPerSec, b.CallsPerBook)
	}
	if b.RPS != 13 || b.Burst != 6 {
		t.Fatalf("budget = %.1f/%d, want 13/6 (disabled sources excluded)", b.RPS, b.Burst)
	}
	// openlibrary 5/s x 10 s = 50 < audible 8/s x 30 s = 240.
	if b.SlowestID != "openlibrary" || b.SlowestRPS != 5 || b.SlowestTimeout != 10*time.Second {
		t.Fatalf("slowest = %s %.1f %v, want openlibrary 5 10s", b.SlowestID, b.SlowestRPS, b.SlowestTimeout)
	}
	if l := candidateFetchLimiter(b.RPS, b.Burst); l.Limit() != rate.Limit(13) || l.Burst() != 6 {
		t.Errorf("limiter = %v/%d, want 13/6", l.Limit(), l.Burst())
	}
	if l := candidateFetchLimiter(0, 0); l.Limit() != rate.Limit(candidateFetchFallbackRPS) || l.Burst() != 1 {
		t.Errorf("no budget: limiter = %v/%d, want the fallback", l.Limit(), l.Burst())
	}
	roomy := metafetch.SourcesBudget{SlowestID: "x", SlowestRPS: 10, SlowestTimeout: 30 * time.Second} // cap 150
	slow := metafetch.SourcesBudget{SlowestID: "x", SlowestRPS: 1, SlowestTimeout: 20 * time.Second}   // cap 10
	with := func(base metafetch.SourcesBudget, rps float64) metafetch.SourcesBudget { base.RPS = rps; return base }
	for _, tc := range []struct {
		name       string
		b          metafetch.SourcesBudget
		configured int
		want       int
	}{
		{"floor", with(roomy, 13), 0, 16}, // 13 x 0.15 x 4 = 7.8
		{"sized", with(roomy, 40), 0, 24}, // 40 x 0.6 = 24
		{"ceiling", with(roomy, 100), 0, 32},
		{"configured wins", with(roomy, 13), 12, 12},
		{"configured clamp", with(roomy, 13), 500, 64},
		{"slow source caps the floor", with(slow, 13), 0, 10},
		{"slow source caps configured", with(slow, 13), 40, 10},
		{"tiny source still one worker", metafetch.SourcesBudget{RPS: 0.1, SlowestID: "x", SlowestRPS: 0.1, SlowestTimeout: 5 * time.Second}, 0, 1},
		{"no sources", metafetch.SourcesBudget{}, 0, 16},
		{"prod 2026-10-01", with(metafetch.SourcesBudget{SlowestID: "audnexus", SlowestRPS: 2, SlowestTimeout: 30 * time.Second}, 16), 0, 16},
		{"calls per book from the budget", metafetch.SourcesBudget{RPS: 40, CallsPerBook: 1, SlowestID: "x", SlowestRPS: 10, SlowestTimeout: 30 * time.Second}, 0, 16}, // 40 x 0.15 x 1 = 6
	} {
		if got := candidateFetchWorkers(tc.b, tc.configured); got != tc.want {
			t.Errorf("%s: candidateFetchWorkers = %d, want %d", tc.name, got, tc.want)
		}
	}
}
