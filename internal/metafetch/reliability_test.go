// file: internal/metafetch/reliability_test.go
// version: 1.0.2
// guid: 4b8c1d2e-3f5a-4c6b-8d7e-9a0b1c2d3e4f
// last-edited: 2026-09-28
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
	"math"
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
	for range 5 {
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
// each issuing one SearchByTitle, the search must draw N tokens, and a limiter
// holding only N-1 must stop the Nth outbound call. Under the old per-book
// behavior the whole search consumed a single token.
//
// The test counts tokens instead of timing the search. It used to assert that
// the unlimited run finished within one 40ms interval, and a loaded CI runner
// took 88ms for work that involves no waiting at all. Here the limiter refills
// once per hour, so its token count moves only when a call draws a token, and
// a Wait that would need a refill fails at once against the context deadline
// instead of sleeping. Nothing below depends on how fast the machine is.
func TestSearchMetadataForBook_LimiterGatesPerRequest(t *testing.T) {
	const nSources = 4
	// Refill slowly enough that no token can come back during the test.
	const refill = time.Hour

	var calls int64
	sources := make([]metadata.MetadataSource, 0, nSources)
	for range nSources {
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

	// A deadline far below the refill interval: limiter.Wait returns an error
	// at once when the next token would arrive after it, instead of blocking.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Baseline: nil limiter, no throttling, and N live calls.
	atomic.StoreInt64(&calls, 0)
	if _, err := svc.searchMetadataForBook(ctx, nil, "b1", "", "", "", "", SearchOptions{}); err != nil {
		t.Fatalf("unlimited search error: %v", err)
	}
	if got := atomic.LoadInt64(&calls); got != nSources {
		t.Fatalf("expected %d live source calls, got %d", nSources, got)
	}

	// Enough tokens for every call: the search draws exactly one per live call.
	const burst = 10 * nSources
	atomic.StoreInt64(&calls, 0)
	plenty := rate.NewLimiter(rate.Every(refill), burst)
	if _, err := svc.searchMetadataForBook(ctx, plenty, "b1", "", "", "", "", SearchOptions{}); err != nil {
		t.Fatalf("limited search error: %v", err)
	}
	if got := atomic.LoadInt64(&calls); got != nSources {
		t.Fatalf("expected %d live source calls under limiter, got %d", nSources, got)
	}
	// Tokens() includes the refill since creation, millionths of a token at one
	// per hour; rounding drops that without being able to hide a whole token.
	if drawn := int(math.Round(burst - plenty.Tokens())); drawn != nSources {
		t.Fatalf("limiter did not gate per request: %d live calls drew %d token(s), want %d",
			nSources, drawn, nSources)
	}

	// One token short: the limiter must refuse exactly one live call, so it
	// stands in front of each request rather than being charged once per book.
	atomic.StoreInt64(&calls, 0)
	short := rate.NewLimiter(rate.Every(refill), nSources-1)
	// The refused source is recorded as a failed source; whether the search as
	// a whole reports an error is not what this test is about.
	_, _ = svc.searchMetadataForBook(ctx, short, "b1", "", "", "", "", SearchOptions{})
	if got := atomic.LoadInt64(&calls); got != nSources-1 {
		t.Fatalf("with %d tokens the limiter let %d live calls through, want %d",
			nSources-1, got, nSources-1)
	}
}
