// file: internal/database/library_generation.go
// version: 1.0.0
// guid: 9696c574-1445-4078-a549-20dc2b5f8095
// last-edited: 2026-08-11

package database

import "github.com/falkcorp/audiobook-organizer/internal/cache"

// LibraryGenerationProvider is implemented by a store that maintains a
// monotonic counter of book-level mutations. Response caches derived from the
// book corpus fold that counter into their keys so entries built before a
// mutation cannot be read after it.
type LibraryGenerationProvider interface {
	LibraryGeneration() *cache.Generation
}

// fallbackLibraryGeneration is handed to callers whose store does not provide a
// counter (in-memory test doubles and mocks, chiefly). It is a single shared
// instance on purpose: every caller that falls back gets the SAME pointer, so
// their cache keys still agree with one another. Nothing bumps it, so it stays
// at generation 0 and those callers behave exactly as they did before
// generation keying existed.
var fallbackLibraryGeneration = &cache.Generation{}

// LibraryGeneration returns the store's book-mutation counter.
//
// Bumped by CreateBook, UpdateBook and DeleteBook — the three writes that
// change which books exist or which of them are primary versions.
//
// Book-FILE mutations deliberately do NOT bump it. They call
// InvalidateLibraryStats on six or seven paths that run once per file during a
// scan, so bumping there would push the list cache to a near-permanent miss for
// the whole duration of every scan on a library this size. Book-file edits can
// therefore still be reflected late in a cached list; that residue is bounded
// by the cache TTL rather than by the counter, which is the trade this design
// accepts.
func (p *PebbleStore) LibraryGeneration() *cache.Generation { return &p.libGen }

// LibraryGenerationOf resolves the book-mutation counter from s, looking
// through any decorator chain that opts into StoreUnwrapper.
//
// It always returns a usable non-nil *cache.Generation. The bool reports
// whether a real store-backed counter was found; false means the shared
// never-bumped fallback was returned instead. Callers should surface that
// case (log it once at wiring time) rather than swallow it: a silent fallback
// leaves every generation-keyed cache pinned at generation 0, which reinstates
// exactly the staleness bug the counter exists to prevent, while every test
// that constructs a real store keeps passing.
//
// Resolution goes through AsCapability rather than a bare type assertion
// because the production store is wrapped (indexedStore) and a naked assertion
// against the wrapper fails.
func LibraryGenerationOf(s any) (*cache.Generation, bool) {
	if provider, ok := AsCapability[LibraryGenerationProvider](s); ok {
		if gen := provider.LibraryGeneration(); gen != nil {
			return gen, true
		}
	}
	return fallbackLibraryGeneration, false
}
