// file: internal/books/store/pebble_store_book_series_check.go
// version: 1.0.0
// guid: 8d41c6f2-0a7b-4e95-b3d8-6f12e9a05c37
// last-edited: 2026-11-18

package store

import (
	"errors"
	"fmt"

	"github.com/cockroachdb/pebble/v2"
)

// requireSeriesForBookWrite refuses a book write that links the book to a series
// row that does not exist. next is the series_id the write will store, prev the
// one the stored row has (nil on create).
//
// WHY: DeleteSeriesIfUnreferenced is atomic against every book write (it holds
// every book stripe while it counts and deletes), but it can only see the rows
// that exist when it runs. A writer that read "series N exists" BEFORE the
// delete and stores series_id = N AFTER it (it was queued behind the delete's
// stripes) strands its own book on a deleted row; every reader renders that
// book with no series. The writer is the only party that can close that window,
// by looking at the row itself, inside the stripe it already holds.
//
// WHAT MAKES THE CHECK ATOMIC: the caller holds the book's write stripe from
// this read to its commit, and DeleteSeriesIfUnreferenced needs every stripe, so
// it cannot run between the two: either it ran first (the row is gone here and
// the write is refused) or it runs after the commit and counts the new link
// (the series is kept). The read is one point read of `series:<id>` in Pebble,
// the authority the series writers write first; it takes no lock, because a
// single-key read is consistent without one and holding nameIdx.series across a
// pebble.Sync commit would serialize every series-setting book write. Lock order
// is therefore unchanged (no series lock is taken under a stripe here).
//
// WHAT IT DOES NOT COVER: a plain DeleteSeries, which takes only nameIdx.series
// and no stripe, so it can still delete the row between this read and the
// commit. That delete is unconditional by contract and its callers count first
// (a count-then-delete the store cannot make atomic from outside); the guarded
// delete is DeleteSeriesIfUnreferenced.
//
// WHEN IT RUNS: only when the write MAKES a link: next non-nil and different from
// prev. A book that already names a series that is gone (production still holds
// such rows; the phantom-series repair exists for them) stays editable: an
// unchanged series_id is not checked, and clearing it never is. A series held
// for an undo exists and is a legal target (linking its books back is what the
// hold is for).
//
// The refusal wraps ErrSeriesNotFound, so a caller that restores an old link
// (an undo, a revert to a version) can tell "that series was deleted" from a
// store failure and report it instead of retrying.
func (p *PebbleStore) requireSeriesForBookWrite(bookID string, next, prev *int) error {
	if next == nil || (prev != nil && *prev == *next) {
		return nil
	}
	_, closer, err := p.db.Get([]byte(fmt.Sprintf("series:%d", *next)))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return fmt.Errorf("book %s: series %d does not exist, so the book cannot be linked to it: %w", bookID, *next, ErrSeriesNotFound)
		}
		return fmt.Errorf("book %s: read series %d: %w", bookID, *next, err)
	}
	closer.Close()
	return nil
}
