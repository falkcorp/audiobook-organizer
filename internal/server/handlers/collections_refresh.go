// file: internal/server/handlers/collections_refresh.go
// version: 1.1.1
// guid: 5e2a9c74-1b83-4f06-a7d1-8c4e0b3f6a92
// last-edited: 2026-10-18

package handlers

import (
	"context"
	"errors"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/playlist"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// RelativeRefreshConcurrency is how many collections the scheduled refresh
// evaluates at once. A library has a handful of dynamic collections (tens at the
// most), so this pool will almost never fill; it exists because each evaluation
// is a search plus, for a per-user query, one state read per hit, and the
// repository rule is that a loop with per-item work of that kind is written
// bounded and concurrent from the start.
const RelativeRefreshConcurrency = 4

// RelativeRefreshResult is what one RefreshRelativeDynamic run did. The counts
// partition the collections it considered: Refreshed + Unchanged + Blocked +
// Conflicts + Failed == Considered, except when IndexUnavailable is set, where
// nothing was evaluated and every count but Considered is zero.
type RelativeRefreshResult struct {
	// Considered is the dynamic collections whose query has a relative date term.
	Considered int
	// IndexUnavailable is true when the search index was closed or rebuilding at
	// the start, so no collection was evaluated. That is a normal state around a
	// start or a reindex, not a failure: the next run retries.
	IndexUnavailable bool
	// Refreshed is how many had a changed member list stored (in a dry run, how
	// many would be).
	Refreshed int
	// Unchanged is how many evaluated to the members they already had.
	Unchanged int
	// Blocked is how many were skipped because evaluate refuses them: a per-user
	// query whose owner no longer exists (ErrCollectionOwnerMissingPerUserQuery).
	// Their stored members are left as they were.
	Blocked int
	// Conflicts is how many were skipped because another writer changed the row
	// between the list and the write (Version compare-and-swap); the next run
	// picks them up.
	Conflicts int
	// Failed is how many could not be evaluated or written for another reason.
	Failed int
}

// RefreshRelativeDynamic re-materializes every dynamic collection whose query has
// a relative date term (last_played:>-30d, date_added:>-7d), so that what the ABS
// surface serves, which never evaluates, slides with the clock instead of freezing
// at the last time somebody opened the collection on the native API. Collections
// without a relative term are left alone: their answer changes only when the
// library does, and writers that change it already re-materialize.
//
// Each collection goes through evaluate, the same path materialize and the native
// read use, so the owner rules apply exactly as they do there: a per-user query is
// evaluated as its owner, a query with no per-user field as no one, and an orphaned
// per-user query is refused (counted as Blocked, members untouched) rather than
// evaluated as the first admin.
//
// A write goes through UpdateCollection, which compares Version, so a collection
// edited between the list and the write is skipped (Conflicts) and never
// overwritten with a result computed from its old query. An unchanged member list
// writes nothing, so Version and UpdatedAt move only when the answer did. With
// dryRun nothing is written and Refreshed counts what would be.
//
// PARTITIONING: each collection is evaluated and written by exactly one worker (the
// list is split by collection, one task each), so two workers never touch the same
// row; evaluate reads shared state but writes none.
//
// If the search index is not open when the run starts it evaluates nothing and
// returns with IndexUnavailable set and no error: a closed or rebuilding index is
// routine (a start, a reindex) and the next hourly run retries, so it is neither
// a failed op nor a warning per collection. An index that closes MID-run still
// fails the collections it cut off (Failed) and returns
// playlist.ErrSearchIndexUnavailable. progress, when not nil, is called after
// each collection with (done, total).
func (h *CollectionHandler) RefreshRelativeDynamic(ctx context.Context, dryRun bool, progress func(done, total int)) (RelativeRefreshResult, error) {
	var res RelativeRefreshResult
	cols, _, err := h.store.ListCollections(database.CollectionTypeDynamic, 0, 0)
	if err != nil {
		return res, err
	}

	var due []database.Collection
	for i := range cols {
		if cols[i].Type == database.CollectionTypeDynamic && search.QueryHasRelativeDate(cols[i].Query) {
			due = append(due, cols[i])
		}
	}
	res.Considered = len(due)
	if len(due) > 0 && !h.indexAvailable() {
		res.IndexUnavailable = true
		return res, nil
	}

	var (
		mu               sync.Mutex
		done             int
		indexUnavailable bool
		g                errgroup.Group
	)
	g.SetLimit(RelativeRefreshConcurrency)
	for i := range due {
		col := due[i]
		if ctx.Err() != nil {
			break
		}
		g.Go(func() error {
			outcome, err := h.refreshOne(&col, dryRun)
			mu.Lock()
			defer mu.Unlock()
			done++
			if progress != nil {
				// Called under the lock, so the reporter sees monotonic counts.
				progress(done, len(due))
			}
			switch outcome {
			case refreshChanged:
				res.Refreshed++
			case refreshUnchanged:
				res.Unchanged++
			case refreshBlocked:
				res.Blocked++
			case refreshConflict:
				res.Conflicts++
			default:
				res.Failed++
				if errors.Is(err, playlist.ErrSearchIndexUnavailable) {
					indexUnavailable = true
				}
			}
			return nil
		})
	}
	_ = g.Wait() // the tasks return nil: every outcome is counted, none aborts the rest
	if err := ctx.Err(); err != nil {
		return res, err
	}
	if indexUnavailable {
		return res, playlist.ErrSearchIndexUnavailable
	}
	return res, nil
}

type refreshOutcome int

const (
	refreshFailed refreshOutcome = iota
	refreshChanged
	refreshUnchanged
	refreshBlocked
	refreshConflict
)

// refreshOne evaluates and, unless dryRun, stores one collection.
func (h *CollectionHandler) refreshOne(col *database.Collection, dryRun bool) (refreshOutcome, error) {
	ids, err := h.evaluate(col)
	switch {
	case errors.Is(err, ErrCollectionOwnerMissingPerUserQuery):
		return refreshBlocked, err
	case errors.Is(err, playlist.ErrSearchIndexUnavailable):
		// The run reports this once, not once per collection.
		return refreshFailed, err
	case err != nil:
		h.log.Warn("collections: scheduled refresh could not evaluate %s: %s",
			logger.SanitizeLogValue(col.ID), logger.SanitizeLogValue(err.Error()))
		return refreshFailed, err
	}
	if sameIDs(col.MaterializedBookIDs, ids) {
		return refreshUnchanged, nil
	}
	if dryRun {
		return refreshChanged, nil
	}
	col.MaterializedBookIDs = ids
	if err := h.store.UpdateCollection(col); err != nil {
		if errors.Is(err, database.ErrCollectionVersionConflict) {
			return refreshConflict, err
		}
		h.log.Warn("collections: scheduled refresh could not store %s: %s",
			logger.SanitizeLogValue(col.ID), logger.SanitizeLogValue(err.Error()))
		return refreshFailed, err
	}
	return refreshChanged, nil
}

// indexAvailable reports whether the search index is open and not rebuilding, the
// same condition playlist.EvaluateSmartPlaylist requires before it answers.
func (h *CollectionHandler) indexAvailable() bool {
	idx := h.indexFn()
	return idx != nil && !idx.Rebuilding()
}

// RelativeRefreshPending reports whether a refresh run has anything to do right
// now: at least one dynamic collection whose query has a relative date AND an
// open search index. The scheduler asks it before enqueueing the hourly op, so an
// install with no such collection (or a closed index) enqueues nothing: one
// collection list and no operation row instead of a queued, run and recorded op
// every hour that cannot change anything.
func (h *CollectionHandler) RelativeRefreshPending() (bool, error) {
	if !h.indexAvailable() {
		return false, nil
	}
	cols, _, err := h.store.ListCollections(database.CollectionTypeDynamic, 0, 0)
	if err != nil {
		return false, err
	}
	for i := range cols {
		if cols[i].Type == database.CollectionTypeDynamic && search.QueryHasRelativeDate(cols[i].Query) {
			return true, nil
		}
	}
	return false, nil
}
