// file: internal/server/search_coverage.go
// version: 1.0.0
// guid: ee9cc3d9-3925-4f72-af8a-e9f25a943fb9
// last-edited: 2026-08-13
//
// Boot-time repair for a PARTIALLY built Bleve search index.
//
// WHY THIS EXISTS
//
// buildSearchIndexIfEmpty (server_search.go) is the only bulk index build,
// and it gates on DocCount() == 0. That encodes "non-empty means complete",
// which is false: the build loop honours s.bgCtx, so a shutdown part-way
// through leaves a populated-but-incomplete index. On the next boot
// DocCount() > 0, the build returns early, and the gap is PERMANENT.
//
// It walks books in ULID order (GetAllBooksFullFrom from ""), and ULIDs are
// time-ordered, so the books lost to a cancellation are always the NEWEST
// ones. That is the signature measured on prod 2026-08-13: books created
// 2026-04 were 97% searchable (38 found / 1 missing) while books created
// 2026-08 were 2% (1 found / 50 missing) — a step function, not a uniform
// sampling loss. The owner-visible symptom was a Library search for "All
// Jobs and Classes" returning five unrelated books: the two rows that
// actually matched were in the missing cohort, and the five survivors
// matched only on the description field (boost 0.5). The same search worked
// in the AudiobookShelf app because that path does not filter to primary
// versions.
//
// The #2268 dirty-set reconciler does NOT cover this. markIndexDirty is
// called from exactly one place — enqueueIndex's queue-full branch — so it
// repairs DROPPED events only. A book the backfill never reached was never
// enqueued, is therefore never dirty, and is never reconciled.
//
// WHY IT SEEDS THE DIRTY SET RATHER THAN RE-RUNNING THE BACKFILL
//
// Flipping the gate to "DocCount != bookCount" would re-run the same
// non-resumable, bgCtx-cancellable loop — the exact property that created a
// permanent gap in the first place, just with a wider trigger. Marking the
// books dirty instead writes durable Pebble keys, so a cancellation
// mid-sweep costs nothing: the marks survive the restart and the shipped
// reconciler drains them on its normal ticker. This reuses tested machinery
// instead of adding a second backfill path with its own failure modes.

package server

import (
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// bookIDLister is the narrow capability this repair needs. Declared here
// rather than widening database.Store, matching the SearchIndexDirtyStore
// convention in pebble_store_search_dirty.go.
type bookIDLister interface {
	ListBookIDs() ([]string, error)
}

// reconcileSearchIndexCoverage compares the number of indexed documents
// against the number of books and, when the index is short, marks every book
// dirty so runSearchReconciler re-indexes them.
//
// It is a no-op on a healthy index, so it is safe to run on every boot.
//
// Deliberately compares counts rather than probing each book: a per-book
// existence check against Bleve would be ~40K point lookups on every start,
// and the count comparison is enough to decide whether a sweep is warranted.
// The cost of being wrong is a redundant re-index, not data loss.
func (s *Server) reconcileSearchIndexCoverage() {
	if s.searchIndex == nil {
		return
	}
	store := s.Store()
	if store == nil {
		return
	}
	lister, ok := store.(bookIDLister)
	if !ok {
		slog.Warn("search coverage: store cannot list book IDs; skipping coverage check")
		return
	}
	if database.AsSearchIndexDirtyStore(store) == nil {
		// Without the durable dirty set there is nothing to seed, and the
		// reconciler would have nothing to drain. Say so rather than
		// silently doing nothing — a silent no-op here is what let the
		// original gap persist unnoticed.
		slog.Warn("search coverage: no durable dirty set; cannot repair index coverage")
		return
	}

	docs, err := s.searchIndex.DocCount()
	if err != nil {
		slog.Warn("search coverage: DocCount failed", "err", err)
		return
	}
	ids, err := lister.ListBookIDs()
	if err != nil {
		slog.Warn("search coverage: ListBookIDs failed", "err", err)
		return
	}

	if uint64(len(ids)) <= docs {
		slog.Info("search index coverage OK", "indexed", docs, "books", len(ids))
		return
	}

	missing := uint64(len(ids)) - docs
	slog.Warn("search index is short of the library; marking books for reconciliation",
		"indexed", docs, "books", len(ids), "shortfall", missing)

	start := time.Now()
	marked := 0
	for _, id := range ids {
		select {
		case <-s.bgCtx.Done():
			// Cancellation is safe here in a way it is not in the backfill:
			// the marks already written are durable, so the next boot's
			// coverage check resumes from a strictly better position.
			slog.Info("search coverage: marking canceled (bgCtx)", "marked", marked, "of", len(ids))
			return
		default:
		}
		s.markIndexDirty(id)
		marked++
	}
	slog.Info("search coverage: books marked for reconciliation",
		"marked", marked, "shortfall", missing, "took", time.Since(start))
}
