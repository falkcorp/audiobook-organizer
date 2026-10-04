// file: internal/database/pebble_store_legacy_seed.go
// version: 1.0.1
// guid: 44f1e74c-758d-4e58-9c04-57e60cd237c6
// last-edited: 2026-10-03

package database

import (
	"errors"
	"testing"
)

// ErrLegacySeedOutsideTest is what SeedLegacyBookRowForTest returns when it is
// called from a binary that is not a test.
var ErrLegacySeedOutsideTest = errors.New("SeedLegacyBookRowForTest called outside a test binary")

// SeedLegacyBookRowForTest is ModifyBook for tests that need a book row in a
// shape this build no longer writes: Book.Series kept while SeriesID is nil,
// or naming a different series than SeriesID. Older builds left such rows in
// production, and the stale-series relink (internal/plugins/maintenance,
// relink_stale_series_fixer.go) exists to repair them. The normal write path
// now holds Series to SeriesID on every write (enforceSeriesInvariant, shared by CreateBook and the update path), so a test cannot build
// that state through ModifyBook or UpdateBook any more.
//
// It is the ordinary book update with one rule skipped: Series is written as
// fn leaves it. Everything else is the same as ModifyBook: the write stripe is
// held across the read and the write, a book_ver: snapshot is taken, the
// secondary indexes are updated, the memdb row is written through, change
// listeners are notified and the narrator junction is synced. So GetBookByID,
// GetAllBooks and the iterators see the seeded row exactly as a reader of an
// older build's row would.
//
// It returns (nil, nil) when the book does not exist, and the fn error as-is
// (ErrSkipBookWrite writes nothing and returns the row). Outside a test binary
// it writes nothing and returns ErrLegacySeedOutsideTest: no production path
// may write a row the store invariant forbids.
func (p *PebbleStore) SeedLegacyBookRowForTest(id string, fn func(*Book) error) (*Book, error) {
	if !testing.Testing() {
		return nil, ErrLegacySeedOutsideTest
	}
	updated, before, err := p.modifyBookLockedMode(id, fn, bookWriteOpts{legacySeries: true})
	if err == nil && updated != nil {
		p.syncNarratorJunctionAfterWrite(id, before, updated)
	}
	return updated, err
}
