// file: internal/metafetch/reliability_test.go
// version: 1.0.0
// guid: 4b8c1d2e-3f5a-4c6b-8d7e-9a0b1c2d3e4f
// last-edited: 2026-07-13
//
// Regression tests for the metadata-reliability fixes:
//   - Bug 3: BuildSourceChain memoizes the chain so the per-source circuit
//     breaker (and Hardcover's rate limiter) persist ACROSS per-book fetches
//     instead of being recreated fresh each book.
//   - Bug 4: the candidate-op search path throttles ACTUAL outbound requests
//     (one limiter token per live source call), not once per book.

package metafetch

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// withMetadataSourceConfig temporarily swaps the global metadata-source config
// and restores it on cleanup. Not parallel-safe (mutates a process global), so
// callers must NOT t.Parallel().
func withMetadataSourceConfig(t *testing.T, sources []config.MetadataSource) {
	t.Helper()
	savedSources := config.AppConfig.MetadataSources
	savedHC := config.AppConfig.HardcoverAPIToken
	savedG := config.AppConfig.GoogleBooksAPIKey
	t.Cleanup(func() {
		config.AppConfig.MetadataSources = savedSources
		config.AppConfig.HardcoverAPIToken = savedHC
		config.AppConfig.GoogleBooksAPIKey = savedG
	})
	config.AppConfig.MetadataSources = sources
}

// TestBuildSourceChain_MemoizedAndBreakerPersists proves Bug 3's fix: repeated
// BuildSourceChain calls (one per book in a batch) return the SAME
// *ProtectedSource instances, so a circuit breaker tripped while processing one
// book is still open for the next — the breaker can actually trip for a down
// source, and Hardcover's limiter accumulates rather than resetting per book.
func TestBuildSourceChain_MemoizedAndBreakerPersists(t *testing.T) {
	withMetadataSourceConfig(t, []config.MetadataSource{
		{ID: "openlibrary", Enabled: true, Priority: 1},
		{ID: "audnexus", Enabled: true, Priority: 2},
	})

	mfs := NewService(&database.MockStore{})

	c1 := mfs.BuildSourceChain()
	c2 := mfs.BuildSourceChain()
	if len(c1) != 2 || len(c2) != 2 {
		t.Fatalf("expected 2 sources per chain, got %d and %d", len(c1), len(c2))
	}
	// Interface equality on *ProtectedSource compares pointers: memoization
	// returns the identical instances across calls.
	for i := range c1 {
		if c1[i] != c2[i] {
			t.Fatalf("chain element %d not memoized: %p vs %p", i, c1[i], c2[i])
		}
	}

	// Trip the first source's breaker (5 consecutive failures = threshold).
	ps, ok := c1[0].(*metadata.ProtectedSource)
	if !ok {
		t.Fatalf("expected *metadata.ProtectedSource, got %T", c1[0])
	}
	for i := 0; i < 5; i++ {
		ps.Breaker().RecordFailure()
	}
	if got := ps.Breaker().StateName(); got != "open" {
		t.Fatalf("breaker should be open after 5 failures, got %q", got)
	}

	// The NEXT book's chain sees the SAME breaker, still open — this is the
	// property that was broken before (fresh breaker per book never tripped).
	c3 := mfs.BuildSourceChain()
	ps3, ok := c3[0].(*metadata.ProtectedSource)
	if !ok {
		t.Fatalf("expected *metadata.ProtectedSource, got %T", c3[0])
	}
	if got := ps3.Breaker().StateName(); got != "open" {
		t.Fatalf("breaker state did not persist across BuildSourceChain calls, got %q", got)
	}

	// A config change (settings edit) must rebuild the chain with fresh instances
	// so runtime config changes are honored.
	config.AppConfig.MetadataSources = []config.MetadataSource{
		{ID: "openlibrary", Enabled: true, Priority: 1},
	}
	c4 := mfs.BuildSourceChain()
	if len(c4) != 1 {
		t.Fatalf("expected 1 source after config change, got %d", len(c4))
	}
	if c4[0] == c1[0] {
		t.Fatal("chain should rebuild (new instances) after a config change")
	}
}

// countingSource records how many live source calls it received. Every method
// returns empty results so the search short-circuits into "no candidates".
type countingSource struct {
	name  string
	calls *int64
}

func (c *countingSource) Name() string { return c.name }
func (c *countingSource) SearchByTitle(_ context.Context, _ string) ([]metadata.BookMetadata, error) {
	atomic.AddInt64(c.calls, 1)
	return nil, nil
}
func (c *countingSource) SearchByTitleAndAuthor(_ context.Context, _, _ string) ([]metadata.BookMetadata, error) {
	atomic.AddInt64(c.calls, 1)
	return nil, nil
}

// TestSearchMetadataForBook_LimiterGatesPerRequest proves Bug 4's fix: the
// limiter is consumed per LIVE source call, not once per book. With N sources
// each issuing one SearchByTitle, a limiter permitting one call per interval
// forces total elapsed ≈ (N-1)*interval. Under the old per-book behavior the
// whole search consumed a single token and returned effectively instantly
// regardless of how many outbound requests it made.
func TestSearchMetadataForBook_LimiterGatesPerRequest(t *testing.T) {
	const nSources = 4
	const interval = 40 * time.Millisecond

	var calls int64
	sources := make([]metadata.MetadataSource, 0, nSources)
	for i := 0; i < nSources; i++ {
		sources = append(sources, &countingSource{name: "src", calls: &calls})
	}

	// book.Title has no chapter markers and no author/narrator, so each source
	// issues exactly ONE SearchByTitle call (searchTitle == book.Title).
	book := &database.Book{ID: "b1", Title: "A Plain Title"}
	mock := &database.MockStore{
		GetBookByIDFunc: func(string) (*database.Book, error) { return book, nil },
	}
	svc := NewService(mock)
	svc.SetOverrideSources(sources)

	// Baseline: nil limiter → no throttling → fast, and N live calls.
	atomic.StoreInt64(&calls, 0)
	startNoLimit := time.Now()
	if _, err := svc.searchMetadataForBook(context.Background(), nil, "b1", "", "", "", "", SearchOptions{}); err != nil {
		t.Fatalf("unlimited search error: %v", err)
	}
	elapsedNoLimit := time.Since(startNoLimit)
	if got := atomic.LoadInt64(&calls); got != nSources {
		t.Fatalf("expected %d live source calls, got %d", nSources, got)
	}
	if elapsedNoLimit > interval {
		t.Fatalf("unlimited search should be fast, took %v", elapsedNoLimit)
	}

	// Rate-limited: one call per interval, burst 1. N calls ⇒ ≈ (N-1)*interval.
	atomic.StoreInt64(&calls, 0)
	limiter := rate.NewLimiter(rate.Every(interval), 1)
	startLimited := time.Now()
	if _, err := svc.searchMetadataForBook(context.Background(), limiter, "b1", "", "", "", "", SearchOptions{}); err != nil {
		t.Fatalf("limited search error: %v", err)
	}
	elapsedLimited := time.Since(startLimited)
	if got := atomic.LoadInt64(&calls); got != nSources {
		t.Fatalf("expected %d live source calls under limiter, got %d", nSources, got)
	}
	// (N-1) waits of `interval` each; allow slack for scheduler jitter but require
	// clearly more than a single per-book token would have cost (~0).
	minExpected := time.Duration(nSources-2) * interval
	if elapsedLimited < minExpected {
		t.Fatalf("limiter did not gate per request: %d calls took only %v (want ≥ %v)",
			nSources, elapsedLimited, minExpected)
	}
}
