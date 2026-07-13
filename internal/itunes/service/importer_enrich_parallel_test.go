// file: internal/itunes/service/importer_enrich_parallel_test.go
// version: 1.1.0
// guid: 8d3a5b1e-6f2c-4a97-9e1d-3c7b8f4a2d6e
// last-edited: 2026-07-07

package itunesservice

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// fakeMetadataFetcher is a metadataFetcher test double. It tracks every
// FetchMetadataForBook call (count + max in-flight concurrency observed) and
// decides success/failure per book ID via a caller-supplied predicate —
// letting tests exercise both the happy path and the CONC-11 circuit
// breaker without wiring a real metafetch.Service + metadata.MetadataSource
// + database.Store chain.
type fakeMetadataFetcher struct {
	fails func(id string) bool

	mu          sync.Mutex
	calls       int
	inFlight    int
	maxInFlight int
	seen        map[string]int
}

func newFakeMetadataFetcher(fails func(id string) bool) *fakeMetadataFetcher {
	return &fakeMetadataFetcher{fails: fails, seen: map[string]int{}}
}

func (f *fakeMetadataFetcher) FetchMetadataForBook(_ context.Context, id string) (*metafetch.FetchMetadataResponse, error) {
	f.mu.Lock()
	f.calls++
	f.inFlight++
	if f.inFlight > f.maxInFlight {
		f.maxInFlight = f.inFlight
	}
	f.seen[id]++
	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()

	if f.fails != nil && f.fails(id) {
		return nil, fmt.Errorf("no metadata found for %q from any source", id)
	}

	authorID := 42
	return &metafetch.FetchMetadataResponse{
		Message: "metadata fetched and applied",
		Book:    &database.Book{ID: id, AuthorID: &authorID},
		Source:  "fake",
	}, nil
}

func (f *fakeMetadataFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

var _ metadataFetcher = (*fakeMetadataFetcher)(nil)

// buildEnrichFixture returns n "imported" books plus a mock store wired to
// answer the GetAllBooks/GetBookAuthors/SetBookAuthors calls
// enrichImportedBooks makes. Every book starts with zero existing authors,
// so a successful fetch always exercises the SetBookAuthors call.
func buildEnrichFixture(t *testing.T, n int) ([]database.Book, *dbmocks.MockStore) {
	t.Helper()

	imported := "imported"
	src := "/mnt/itunes/Library.xml"

	books := make([]database.Book, n)
	for i := 0; i < n; i++ {
		books[i] = database.Book{
			ID:                 fmt.Sprintf("book-%d", i),
			Title:              fmt.Sprintf("Title-%d", i),
			LibraryState:       &imported,
			ITunesImportSource: &src,
		}
	}

	cores := make([]database.BookCore, len(books))
	for i := range books {
		cores[i] = books[i].Core()
	}

	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetAllBooksCore(10000, 0).Return(cores, nil)
	for i := range books {
		id := books[i].ID
		m.EXPECT().GetBookAuthors(id).Return(nil, nil).Maybe()
		m.EXPECT().SetBookAuthors(id, mock.Anything).Return(nil).Maybe()
	}
	return books, m
}

// TestEnrichImportedBooks_ParallelMatchesSerial runs enrichImportedBooks
// twice against equivalent fixtures — once forced sequential
// (enrichConcurrencyOverride=1) and once forced parallel
// (enrichConcurrencyOverride=8, comfortably more than the real
// enrichConcurrency=4 default so the worker pool actually overlaps workers)
// — and asserts both runs fetch metadata for every imported book exactly
// once and enrich the same result set (order-independent). Every book
// succeeds in this fixture, so the breaker is never exercised here — see
// TestEnrichImportedBooks_BreakerCancelsRemainingWork for that.
//
// Run with `go test -race`: the fakeMetadataFetcher's shared counters plus
// enrichImportedBooks' own `enriched`/breakerFails state are exactly the
// kind of concurrent access a missing lock/atomic would corrupt.
func TestEnrichImportedBooks_ParallelMatchesSerial(t *testing.T) {
	const totalBooks = 12

	// --- Sequential run -------------------------------------------------
	seqBooks, seqStore := buildEnrichFixture(t, totalBooks)
	seqFetcher := newFakeMetadataFetcher(nil) // never fails
	seqImp := &Importer{
		store:                     seqStore,
		mfs:                       seqFetcher,
		enrichConcurrencyOverride: 1,
	}
	seqStatus := &itunesImportStatus{}
	seqLog := logger.New("test-enrich-seq")

	seqImp.enrichImportedBooks(context.Background(), seqStatus, seqLog)

	require.Equal(t, totalBooks, seqFetcher.callCount(), "sequential run must fetch metadata for every imported book")
	require.LessOrEqual(t, seqFetcher.maxInFlight, 1, "sequential run must never have concurrent FetchMetadataForBook calls")
	for _, b := range seqBooks {
		require.Equal(t, 1, seqFetcher.seen[b.ID], "each book fetched exactly once")
	}

	// --- Parallel run -----------------------------------------------------
	parBooks, parStore := buildEnrichFixture(t, totalBooks)
	parFetcher := newFakeMetadataFetcher(nil) // never fails
	parImp := &Importer{
		store:                     parStore,
		mfs:                       parFetcher,
		enrichConcurrencyOverride: 8,
	}
	parStatus := &itunesImportStatus{}
	parLog := logger.New("test-enrich-par")

	parImp.enrichImportedBooks(context.Background(), parStatus, parLog)

	require.Equal(t, totalBooks, parFetcher.callCount(), "parallel run must fetch metadata for every imported book — same result set as serial")
	for _, b := range parBooks {
		require.Equal(t, 1, parFetcher.seen[b.ID], "each book fetched exactly once, no duplicate/dropped work under concurrency")
	}
}

// TestEnrichImportedBooks_EmptyList exercises the zero-books path (no
// imported books) — RunItems must be a no-op and must not panic.
func TestEnrichImportedBooks_EmptyList(t *testing.T) {
	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetAllBooksCore(10000, 0).Return(nil, nil)

	fetcher := newFakeMetadataFetcher(nil)
	imp := &Importer{store: m, mfs: fetcher, enrichConcurrencyOverride: 4}
	status := &itunesImportStatus{}
	log := logger.New("test-enrich-empty")

	imp.enrichImportedBooks(context.Background(), status, log)

	require.Equal(t, 0, fetcher.callCount())
}

// TestEnrichImportedBooks_NoMetafetchService exercises the imp.mfs == nil
// guard clause — must return immediately without touching the store.
func TestEnrichImportedBooks_NoMetafetchService(t *testing.T) {
	m := dbmocks.NewMockStore(t) // no .EXPECT() calls set up — any call fails the test
	imp := &Importer{store: m, mfs: nil}
	status := &itunesImportStatus{}
	log := logger.New("test-enrich-no-mfs")

	imp.enrichImportedBooks(context.Background(), status, log)
}

// TestEnrichImportedBooks_BreakerCancelsRemainingWork is the correctness-
// critical CONC-11 test: with every FetchMetadataForBook call failing, the
// shared-aggregate breaker (breakerFails >= enrichBreakerThreshold) must
// cancel the RunItems context so registry.RunItems stops LAUNCHING new
// items — the total number of attempted fetches must stay well below the
// full book count instead of hammering every single one.
//
// Uses a large book count (50) relative to enrichConcurrency (4) and
// enrichBreakerThreshold (5) so the "stopped early" signal is unambiguous
// even accounting for the handful of workers already in flight when the
// breaker trips.
func TestEnrichImportedBooks_BreakerCancelsRemainingWork(t *testing.T) {
	const totalBooks = 50

	books, store := buildEnrichFixture(t, totalBooks)
	_ = books
	fetcher := newFakeMetadataFetcher(func(string) bool { return true }) // every call fails
	imp := &Importer{store: store, mfs: fetcher, enrichConcurrencyOverride: 4}
	status := &itunesImportStatus{}
	log := logger.New("test-enrich-breaker")

	imp.enrichImportedBooks(context.Background(), status, log)

	attempted := fetcher.callCount()
	require.Less(t, attempted, totalBooks/2,
		"breaker must cancel remaining work — expected far fewer than %d attempts, got %d", totalBooks, attempted)
	// Enough workers must have run to have actually crossed the threshold
	// (otherwise the test would trivially pass on a broken/no-op breaker
	// that just happens to run fewer than 50 items for some other reason).
	require.GreaterOrEqual(t, attempted, enrichBreakerThreshold,
		"expected at least enough attempts to trip the breaker, got %d", attempted)
}

// TestEnrichImportedBooks_BreakerResetsOnSuccess asserts the shared
// aggregate failure counter resets on success rather than being a
// cumulative lifetime tally — an alternating fail/succeed/fail/succeed
// pattern (which never reaches enrichBreakerThreshold consecutively-ish)
// must run to completion, same as the original consecutiveErrors design.
func TestEnrichImportedBooks_BreakerResetsOnSuccess(t *testing.T) {
	const totalBooks = 20

	_, store := buildEnrichFixture(t, totalBooks)

	var callN int32
	fetcher := newFakeMetadataFetcher(func(string) bool {
		// Fail every other call: 1 failure, 1 success, repeat. Never
		// accumulates enrichBreakerThreshold(5) failures without an
		// intervening reset, whether run sequentially or with the real
		// worker pool (which serializes atomic increments/resets even
		// though call ORDER across books is not guaranteed).
		n := atomic.AddInt32(&callN, 1)
		return n%2 == 1
	})
	// Force sequential so the fail/succeed/fail/succeed pattern is
	// deterministic by call order (concurrent scheduling would make the
	// exact interleaving non-deterministic, which isn't needed to prove
	// the reset-on-success behavior).
	imp := &Importer{store: store, mfs: fetcher, enrichConcurrencyOverride: 1}
	status := &itunesImportStatus{}
	log := logger.New("test-enrich-breaker-reset")

	imp.enrichImportedBooks(context.Background(), status, log)

	require.Equal(t, totalBooks, fetcher.callCount(),
		"alternating fail/succeed must never trip the breaker (aggregate counter resets on success), all books attempted")
}
