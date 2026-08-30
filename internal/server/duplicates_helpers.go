// file: internal/server/duplicates_helpers.go
// version: 1.10.0
// guid: 550a807d-8c00-4e34-9a8c-52a80710a0b9
// last-edited: 2026-08-30
//
// Shared, non-HTTP helpers that were extracted from duplicates_handlers.go when
// the 17 duplicates HTTP handlers moved to internal/server/handlers/duplicates.
// These helpers STAY in package server because they are referenced by files that
// did not move:
//
//   - filterReviewedAuthorGroups      → duplicates_ops.go (author-scan op) and
//                                        wire_handlers.go (system handler injection)
//   - executeSeriesPrune              → duplicates_ops.go, server_maintenance_deps.go
//   - executeSeriesNormalizeCore      → duplicates_ops.go, server_maintenance_deps.go
//   - computeSeriesNormalizeActions   → executeSeriesNormalizeCore + duplicates_handlers_test.go
//   - mergeSeriesGroupHelper          → executeSeriesNormalizeCore
//   - seriesNormalizeAction /
//     seriesNormalizePreviewResult    → duplicates_handlers_test.go + the
//                                        normalize preview payload builder
//
// Signatures and the *Server-method-vs-package-func form are preserved EXACTLY
// so existing callers (and tests) compile unchanged. The duplicates sub-package
// reaches the handler-facing helpers via injected func closures wired in
// wire_handlers.go (it cannot call s.<method>).

package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	ulid "github.com/oklog/ulid/v2"
)

// seriesPruneStore and seriesMergeStore are what the series maintenance helpers
// actually call, measured by emptying each and reading the compiler's
// enumeration. Both were inline anonymous interfaces of database.* embeds —
// 111 and 60 methods — in parameter position.
type seriesPruneStore interface {
	GetAllSeries() ([]database.Series, error)
	GetBooksBySeriesIDCore(seriesID int) ([]database.BookCore, error)
	// See maintenanceSeriesStore: display may filter, writes may not.
	GetBooksBySeriesIDAllVersions(seriesID int) ([]database.BookCore, error)
	DeleteSeries(id int) error
	GetBookByID(id string) (*database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
	CreateOperationChange(change *database.OperationChange) error
}

type seriesMergeStore interface {
	GetAllSeries() ([]database.Series, error)
	GetBooksBySeriesIDCore(seriesID int) ([]database.BookCore, error)
}

// filterReviewedAuthorGroups removes author dedup groups where all author IDs
// have already been reviewed via AI scans (applied results with skip/split/merge).
func (s *Server) filterReviewedAuthorGroups(groups []dedup.AuthorDedupGroup) []dedup.AuthorDedupGroup {
	if s.aiScanStore == nil {
		return groups
	}
	applied, err := s.aiScanStore.GetAllAppliedResults()
	if err != nil || len(applied) == 0 {
		return groups
	}

	// Build set of reviewed author ID sets (key = sorted comma-joined IDs)
	reviewedSets := make(map[string]bool)
	for _, r := range applied {
		if len(r.Suggestion.AuthorIDs) < 2 {
			continue
		}
		ids := make([]int, len(r.Suggestion.AuthorIDs))
		copy(ids, r.Suggestion.AuthorIDs)
		sort.Ints(ids)
		parts := make([]string, len(ids))
		for i, id := range ids {
			parts[i] = strconv.Itoa(id)
		}
		reviewedSets[strings.Join(parts, ",")] = true
	}

	if len(reviewedSets) == 0 {
		return groups
	}

	// Filter: exclude groups whose author IDs match a reviewed set
	filtered := make([]dedup.AuthorDedupGroup, 0, len(groups))
	for _, g := range groups {
		ids := make([]int, 0, 1+len(g.Variants))
		ids = append(ids, g.Canonical.ID)
		for _, v := range g.Variants {
			ids = append(ids, v.ID)
		}
		sort.Ints(ids)
		parts := make([]string, len(ids))
		for i, id := range ids {
			parts[i] = strconv.Itoa(id)
		}
		key := strings.Join(parts, ",")
		if !reviewedSets[key] {
			filtered = append(filtered, g)
		}
	}
	return filtered
}

// executeSeriesPrune performs the actual series prune logic (used by both HTTP handler and scheduler).
//
// Counters and the cache invalidation are hoisted to the top and deferred on
// purpose. This function has seven exits — two ctx cancellations, two store
// failures, both fail-closed reference-count guards (phase 1's and phase 2's),
// and the normal end — and every one of them can be reached AFTER phase 1 has
// already repointed books. Invalidating
// only at the normal exit left the other five serving pre-merge membership under
// the cached list's 24-hour TTL, which is the 2026-08-14 production symptom
// quoted below reached from a third direction. A defer is the only form that
// cannot be bypassed by the next early return somebody adds.
func (s *Server) executeSeriesPrune(ctx context.Context, store seriesPruneStore, progress operations.ProgressReporter, operationID string) (err error) {
	_ = progress.Log("info", "Starting series auto-prune...", nil)

	// Declared before the first exit so the deferred invalidation can see them.
	totalMerged := 0
	orphansDeleted := 0
	// Books actually repointed, counted separately from series deleted.
	//
	// These used to be the same thing: a merge either repointed everything and
	// deleted the series, or errored. Now a merge can repoint some books and then
	// REFUSE the delete, which changes book→series assignments while leaving
	// totalMerged at zero.
	booksRepointed := 0
	var mergeErrors []string

	// Drop the cached series list, but only when this run actually changed
	// something.
	//
	// The cache holds a 24-hour TTL and is warmed at startup, and until 2026-08-14
	// it was invalidated only by the interactive entities API — so a prune that
	// merged and deleted correctly left /api/v1/series serving the pre-prune list,
	// which is indistinguishable from a prune that did nothing. Measured on
	// production: "17 duplicates merged, 326 orphans deleted, 0 errors" followed by
	// a series list still reporting all 14,629 rows.
	//
	// A run that changed nothing must NOT invalidate: dropping a warm cache costs a
	// full recount for no reason. Same rule the author-conjunction repair follows
	// (658d91a2). "Changed nothing" is why booksRepointed is in the predicate and
	// not just rows removed — see its declaration above.
	defer func() {
		if totalMerged+orphansDeleted > 0 || booksRepointed > 0 {
			s.InvalidateSeriesCache()
			_ = progress.Log("info", fmt.Sprintf(
				"Invalidated the cached series list (%d rows removed, %d books repointed)",
				totalMerged+orphansDeleted, booksRepointed), nil)
		}

		// Attaching the recorded errors here, to the NAMED return, is what makes
		// them survive every exit rather than only the last one.
		//
		// Both cancellation exits used to `return ctx.Err()` bare. Phase 1 can
		// refuse a delete -- appending "REFUSING to delete it ... Re-run after
		// resolving the errors above" to mergeErrors -- and then the operator
		// cancels, or the maintenance window's context ends. A bare return
		// destroyed that message and the counts with it, leaving "context
		// canceled" and no record that a series is now half-merged.
		//
		// That is the same defect this change fixes one file over with
		// errors.Join(opErr, ctx.Err()); doing it in the defer means the NEXT
		// early return somebody adds inherits the behaviour instead of
		// reintroducing the bug.
		if len(mergeErrors) == 0 {
			return
		}
		detail := strings.Join(mergeErrors[:min(len(mergeErrors), 10)], "; ")
		if len(mergeErrors) > 10 {
			detail += fmt.Sprintf(" (and %d more)", len(mergeErrors)-10)
		}
		recorded := fmt.Errorf("series prune recorded %d error(s) (%d series merged, %d orphans deleted, %d books repointed): %s",
			len(mergeErrors), totalMerged, orphansDeleted, booksRepointed, detail)
		if err != nil {
			err = errors.Join(err, recorded)
			return
		}
		err = recorded
	}()

	allSeries, err := store.GetAllSeries()
	if err != nil {
		return fmt.Errorf("failed to get series: %w", err)
	}

	// Schedule: scan phase (N=len(allSeries)) + 1 orphan phase + 1 done.
	totalSteps := len(allSeries) + 2
	_ = progress.UpdateProgress(0, totalSteps, fmt.Sprintf("Scanning %d series... (0/%d 0.00%%)", len(allSeries), totalSteps))

	// Group by LOWER(TRIM(name)) + author_id
	type groupKey struct {
		name     string
		authorID int
	}
	groups := make(map[groupKey][]database.Series)
	for _, s := range allSeries {
		aid := 0
		if s.AuthorID != nil {
			aid = *s.AuthorID
		}
		key := groupKey{name: util.NormalizeString(s.Name), authorID: aid}
		groups[key] = append(groups[key], s)
	}

	// UNFILTERED reference counts for PHASE 1, read once before the merge loop.
	//
	// Phase 1's existing repointFailed gate answers "did every row I was HANDED
	// get repointed?". That is a different question from "is anything still
	// pointing at this series?", and only the second one is safe to delete on.
	// GetBooksBySeriesIDAllVersions below returns non-primary versions but still
	// SKIPS TRASHED rows, so a series whose books are all in the trash
	// enumerates EMPTY, repoints nothing, passes repointFailed == 0 and used to
	// be deleted — stranding every trashed row on a series ID that no longer
	// resolves. That is the surviving half of the hazard that left 6,893 phantom
	// series IDs held by 13,322 live books (+702 trashed) on production
	// 2026-08-14; see internal/database/series_bookref.go.
	//
	// Read ONCE for the whole library rather than per series: it turns one scan
	// per duplicate group into one scan total, and the counts cannot go stale in
	// a direction that makes this guard wrong. Phase 1 only ever moves books AWAY
	// from a merged-from series, and each series ID belongs to exactly one group
	// and is visited once, so refCounts[ser.ID] is an upper bound on what still
	// references it — a stale-high count refuses, never permits.
	//
	// Fails CLOSED, and before anything has been deleted. A store that cannot
	// answer the unfiltered question aborts the prune rather than falling back to
	// the filtered count; that fallback IS the bug, and it is silent.
	phase1RefCounts, refCountErr := database.SeriesRefCounts(store)
	if refCountErr != nil {
		return fmt.Errorf("series prune refusing to merge without unfiltered reference counts: %w", refCountErr)
	}

	// Phase 1: Merge duplicates. Counters are declared at the top of the function
	// so the deferred cache invalidation can observe them on every exit path.
	dupGroupCount := 0

	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		dupGroupCount++

		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Pick canonical: most books, then lowest ID
		canonicalIdx := 0
		canonicalBookCount := 0
		voteFailed := false
		for i, s := range group {
			// Core is right here: this is candidate SELECTION, picking which of
			// several duplicate series survives. Counting alternate rips would let
			// a series win the vote on copies rather than on distinct books. The
			// repoint loop below reads the complete set — the two getters play
			// different roles and that is deliberate.
			books, err := store.GetBooksBySeriesIDCore(s.ID)
			if err != nil {
				// A failed count DISQUALIFIES the whole group.
				//
				// Recording the error and carrying on was not enough. A series whose
				// count fails to load counts as zero books, so it loses the vote to any
				// sibling — and the loser is then DELETED. One transient read error
				// could make a 400-book series lose to a 2-book typo of itself: the
				// books survive (they get repointed) but the canonical row, its ID and
				// every external reference to it are gone, and reversing it means
				// hand-reading the OperationChange ledger.
				//
				// Skipping the group costs one deferred merge. Getting the vote wrong
				// costs a row that cannot be recovered from the summary.
				mergeErrors = append(mergeErrors, fmt.Sprintf(
					"failed to count books for series %d (%q) while picking which of %d duplicate "+
						"series to keep; REFUSING to merge this group, because an unreadable count "+
						"would silently lose the vote and get the series deleted: %v",
					s.ID, s.Name, len(group), err))
				voteFailed = true
				break
			}
			bc := len(books)
			if bc > canonicalBookCount || (bc == canonicalBookCount && s.ID < group[canonicalIdx].ID) {
				canonicalIdx = i
				canonicalBookCount = bc
			}
		}
		if voteFailed {
			continue
		}
		keepID := group[canonicalIdx].ID

		for i, ser := range group {
			if i == canonicalIdx {
				continue
			}
			// Hydrate the full row via GetBookByID and mutate/write THAT. The
			// tempting shortcut — bookCore.ToBook() then UpdateBook — would drop
			// the denormalized Author/Series (db:"-"): they are NOT covered by
			// UpdateBook's STOR-1 preserve-on-empty guard (which only restores
			// Description/VersionNotes/BookSig*). Hydrating keeps the write
			// correct and self-contained.
			// See docs/specs/2026-07-05-store-getter-fidelity-unification.md.
			//
			// AllVersions, not the Core listing getter: this loop repoints every
			// row it is handed and then deletes ser.ID below. A non-primary
			// version the listing getter hides is one this loop never repoints,
			// and it is left holding a series that no longer exists.
			books, err := store.GetBooksBySeriesIDAllVersions(ser.ID)
			if err != nil {
				mergeErrors = append(mergeErrors, fmt.Sprintf("failed to get books for series %d: %v", ser.ID, err))
				continue
			}
			// Every book that could NOT be repointed. The delete below is gated on
			// this being zero: a book still holding ser.ID when the row is deleted
			// is stranded exactly as if the getter had hidden it, and the operator
			// is told the prune succeeded. Recording the failure is not enough --
			// that is what this loop already did.
			repointFailed := 0
			// Rows this iteration actually MOVED off ser.ID, counted on the
			// write succeeding. It is the subtrahend of the reference guard
			// below and must never be len(books): that is what was ATTEMPTED,
			// and it would make the guard pass on exactly the failures it
			// exists to catch. booksRepointed is the run-wide total and cannot
			// serve here.
			moved := 0
			for _, bookCore := range books {
				oldSeriesID := ser.ID
				full, herr := store.GetBookByID(bookCore.ID)
				if herr != nil {
					mergeErrors = append(mergeErrors, fmt.Sprintf("failed to hydrate book %s: %v", bookCore.ID, herr))
					repointFailed++
					continue
				}
				if full == nil {
					// Reachable: the Pebble store returns (nil, nil) on ErrNotFound,
					// and the membership getter can list a row from the memdb that a
					// later point-get cannot hydrate. This branch used to be entirely
					// silent -- no error, no counter -- and then fell through to the
					// delete.
					mergeErrors = append(mergeErrors, fmt.Sprintf(
						"book %s is listed under series %d but does not resolve", bookCore.ID, ser.ID))
					repointFailed++
					continue
				}
				full.SeriesID = &keepID
				if _, err := store.UpdateBook(full.ID, full); err != nil {
					mergeErrors = append(mergeErrors, fmt.Sprintf("failed to reassign book %s: %v", bookCore.ID, err))
					repointFailed++
				} else {
					// Counted on the WRITE succeeding, not inside the operationID
					// branch below: a repoint with no operation to attribute it to
					// still changed the book's series and still invalidates the
					// cached series list.
					booksRepointed++
					moved++
					if operationID != "" {
						_ = store.CreateOperationChange(&database.OperationChange{
							ID:          ulid.Make().String(),
							OperationID: operationID,
							BookID:      bookCore.ID,
							ChangeType:  "series_merge",
							FieldName:   "series_id",
							OldValue:    fmt.Sprintf("%d (%s)", oldSeriesID, ser.Name),
							NewValue:    fmt.Sprintf("%d (%s)", keepID, group[canonicalIdx].Name),
						})
					}
				}
			}
			if repointFailed > 0 {
				mergeErrors = append(mergeErrors, fmt.Sprintf(
					"series %d (%q): %d of %d books could not be repointed to %d; REFUSING to "+
						"delete it, which would leave them holding a series row that no longer "+
						"exists. Re-run after resolving the errors above.",
					ser.ID, ser.Name, repointFailed, len(books), keepID))
				continue
			}
			// The reference guard, kept SEPARATE from the repointFailed gate
			// above on purpose: they answer different questions and fire on
			// different populations. repointFailed covers rows this loop was
			// handed and could not write; this covers rows it was never handed
			// at all. Reaching here means the unfiltered counter sees more
			// references than GetBooksBySeriesIDAllVersions returned — a
			// TRASHED row (both series getters skip soft-deleted books, and a
			// trashed row cannot be repointed) or a row the memdb counts and
			// Pebble can no longer hydrate.
			//
			// Only the row removal is refused. The books that WERE repointed
			// stay repointed: that is strictly an improvement and rolling it
			// back would re-strand them. A surviving series row is visible and
			// re-cleanable; a deleted one is not.
			if stranded := phase1RefCounts[ser.ID] - moved; stranded > 0 {
				mergeErrors = append(mergeErrors, fmt.Sprintf(
					"series %d (%q): %d book(s) still reference it after reassigning %d to %d "+
						"(trashed rows, which cannot be repointed, or rows the reference count sees "+
						"and this run could not); REFUSING to delete it, which would leave them "+
						"holding a series row that no longer exists",
					ser.ID, ser.Name, stranded, moved, keepID))
				continue
			}
			if err := store.DeleteSeries(ser.ID); err != nil {
				mergeErrors = append(mergeErrors, fmt.Sprintf("failed to delete series %d: %v", ser.ID, err))
			} else {
				totalMerged++
				if operationID != "" {
					_ = store.CreateOperationChange(&database.OperationChange{
						ID:          ulid.Make().String(),
						OperationID: operationID,
						ChangeType:  "series_delete",
						FieldName:   "series",
						OldValue:    fmt.Sprintf("%d: %s", ser.ID, ser.Name),
						NewValue:    fmt.Sprintf("merged into %d: %s", keepID, group[canonicalIdx].Name),
					})
				}
			}
		}
	}

	_ = progress.Log("info", fmt.Sprintf("Phase 1 complete: merged %d duplicate series from %d groups", totalMerged, dupGroupCount), nil)
	orphanStep := len(allSeries) + 1
	_ = progress.UpdateProgress(orphanStep, totalSteps, fmt.Sprintf("Scanning for orphan series... (%d/%d %.2f%%)", orphanStep, totalSteps, float64(orphanStep)/float64(totalSteps)*100))

	// Phase 2: Delete orphan series — series NOTHING references.
	//
	// This used to ask GetBooksBySeriesIDCore, which skips trashed and
	// non-primary books. Those books still hold the series_id, so deleting on a
	// zero from that counter left them pointing at a row that no longer exists.
	// On production 2026-08-14 that had already produced 6,893 phantom series
	// IDs held by 13,322 live books (+702 trashed), every one of them rendering
	// with no series. See internal/database/series_bookref.go.
	//
	// The reference counts are computed ONCE for the whole library rather than
	// per series: it turns 14,626 scans into one, and more importantly it is the
	// only form in which "referenced by nothing" is actually answerable.
	//
	// This is a SECOND, FRESH scan — do NOT "optimize" it into reusing
	// phase1RefCounts. Phase 1 repoints books ONTO the canonical series it keeps,
	// so a canonical series that had zero references when phase1RefCounts was
	// read can hold several by the time this loop runs. Phase 1's guard is
	// RELATIVE (count minus what it moved) and is monotone-safe against its own
	// writes; this one is ABSOLUTE (== 0) and is not. Reusing the pre-Phase-1 map
	// here would delete the merge TARGET out from under every book Phase 1 just
	// moved into it — a worse bug than the one the guard exists to prevent.
	refCounter := database.AsSeriesBookRefStore(store)
	if refCounter == nil {
		// Deliberately fatal. Falling back to the filtered counter is precisely
		// the bug being removed, and it would delete rows while reporting
		// success — the failure family this repo keeps rediscovering.
		return fmt.Errorf("series prune: store cannot count unfiltered series references (got %T); "+
			"refusing to delete orphans from a filtered count, which silently drops "+
			"series whose books are trashed or non-primary", store)
	}
	refCounts, refErr := refCounter.GetAllSeriesBookRefCounts()
	if refErr != nil {
		return fmt.Errorf("series prune: failed to count series references: %w", refErr)
	}
	_ = progress.Log("info", fmt.Sprintf("Reference scan: %d series are referenced by at least one book (any state)", len(refCounts)), nil)

	// Re-fetch series to account for merges
	refreshedSeries, err := store.GetAllSeries()
	if err != nil {
		// Recorded as an error, not just logged. This skips the ENTIRE orphan
		// sweep, and without an entry in mergeErrors the run still reported
		// "0 errors" — a false clean bill of health on the one phase whose job is
		// finding stale rows. The two failures eight lines above (refCounter nil,
		// GetAllSeriesBookRefCounts failing) are hard returns; this one was the
		// odd branch out.
		mergeErrors = append(mergeErrors, fmt.Sprintf(
			"failed to refresh the series list; the orphan sweep was SKIPPED entirely "+
				"and no orphan count in this run's summary is meaningful: %v", err))
		_ = progress.Log("warn", fmt.Sprintf("Failed to refresh series list, skipping orphan sweep: %v", err), nil)
	} else {
		for _, ser := range refreshedSeries {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if refCounts[ser.ID] == 0 {
				if err := store.DeleteSeries(ser.ID); err != nil {
					mergeErrors = append(mergeErrors, fmt.Sprintf("failed to delete orphan series %d: %v", ser.ID, err))
				} else {
					orphansDeleted++
					if operationID != "" {
						_ = store.CreateOperationChange(&database.OperationChange{
							ID:          ulid.Make().String(),
							OperationID: operationID,
							ChangeType:  "series_delete",
							FieldName:   "orphan_series",
							OldValue:    fmt.Sprintf("%d: %s", ser.ID, ser.Name),
							NewValue:    "deleted (0 book references, any state)",
						})
					}
				}
			}
		}
	}

	totalCleaned := totalMerged + orphansDeleted
	resultMsg := fmt.Sprintf("Series prune complete: %d duplicates merged, %d orphans deleted (%d total cleaned, %d errors)",
		totalMerged, orphansDeleted, totalCleaned, len(mergeErrors))
	_ = progress.Log("info", resultMsg, nil)

	// Record summary change
	if operationID != "" {
		_ = store.CreateOperationChange(&database.OperationChange{
			ID:          ulid.Make().String(),
			OperationID: operationID,
			ChangeType:  "series_prune_summary",
			FieldName:   "summary",
			OldValue:    fmt.Sprintf("%d total series scanned", len(allSeries)),
			NewValue:    resultMsg,
		})
	}
	// Capped at ten in the message so a run with thousands of failures does not
	// produce an unreadable error string; the count above it is the complete
	// figure, and every entry is in the operation log.
	errDetail := ""
	if len(mergeErrors) > 0 {
		errDetail = strings.Join(mergeErrors[:min(len(mergeErrors), 10)], "; ")
		if len(mergeErrors) > 10 {
			errDetail += fmt.Sprintf(" (and %d more)", len(mergeErrors)-10)
		}
		_ = progress.Log("warn", fmt.Sprintf("Errors: %s", errDetail), nil)
	}
	_ = progress.UpdateProgress(totalSteps, totalSteps, fmt.Sprintf("%s (%d/%d 100.00%%)", resultMsg, totalSteps, totalSteps))

	if s.dedupCache != nil {
		s.dedupCache.InvalidateAll()
	}
	// The cached series list is dropped by the deferred invalidation at the top of
	// this function, so that every exit path gets it and not just this one.

	// The errors are turned into this function's return value by the deferred
	// block at the top -- NOT here.
	//
	// Until 2026-08-24 this was an unconditional `return nil`, so mergeErrors --
	// every hydrate failure, every refused delete, every skipped orphan sweep --
	// reached the operator only as a warn line truncated to ten entries. The caller
	// (duplicates_ops.go) read the nil, set status "success" and emitted "Series
	// prune completed"; the nightly maintenance job did the same.
	//
	// That made the fail-closed refusal added in #2828 self-defeating. Its whole
	// purpose is to stop a merge deleting a series whose books could not all be
	// moved, and its message ends "Re-run after resolving the errors above" -- an
	// instruction delivered to a run that reported itself green, so nobody re-ran
	// it. The books stay split across both series rows indefinitely.
	//
	// This is the same predicate mistake the cache invalidation had: repoint-then-
	// refuse is an outcome class that did not exist when these conditions were
	// written, and it has to be taught to every condition that consumes the same
	// facts, not just the first one noticed.
	return nil
}

// seriesNormalizeAction describes a single action the normalize pass would take.
type seriesNormalizeAction struct {
	SeriesID      int    `json:"series_id"`
	OldName       string `json:"old_name"`
	NewName       string `json:"new_name"`
	NewPosition   string `json:"new_position,omitempty"`
	Action        string `json:"action"` // "rename", "merge_into", "flag"
	MergeTargetID *int   `json:"merge_target_id,omitempty"`
	BookCount     int    `json:"book_count"`
}

// seriesNormalizePreviewResult is the response body for the dry-run preview endpoint.
type seriesNormalizePreviewResult struct {
	Actions             []seriesNormalizeAction `json:"actions"`
	TotalSeriesAffected int                     `json:"total_series_affected"`
	TotalBooksAffected  int                     `json:"total_books_affected"`
	FlaggedForReview    []seriesNormalizeAction `json:"flagged_for_review"`
}

// computeSeriesNormalizeActions iterates all series, strips contamination from
// each name, and returns the list of rename / merge_into / flag actions that
// would be taken by a full normalize run. No writes are performed.
//
// Returns an error rather than swallowing one. This used to be `return nil` on a
// GetAllSeries failure, with no error return to put it in and no log: an empty
// action list is indistinguishable from "the library is already clean", so a
// store failure made the operation report "Series normalization complete,
// affected_books=0" with status success. The same swallow zeroed the dry-run
// PREVIEW, so the operator's pre-approval check also showed a clean, empty list —
// nothing had been examined in either case.
func computeSeriesNormalizeActions(store seriesMergeStore) ([]seriesNormalizeAction, error) {
	allSeries, err := store.GetAllSeries()
	if err != nil {
		return nil, fmt.Errorf("failed to list series: %w", err)
	}

	type groupKey struct {
		name     string
		authorID int
	}
	canonical := make(map[groupKey]int)
	var actions []seriesNormalizeAction

	for _, s := range allSeries {
		cleaned, pos, flagged := metadata.StripSeriesContamination(s.Name, "")

		if flagged {
			books, _ := store.GetBooksBySeriesIDCore(s.ID)
			actions = append(actions, seriesNormalizeAction{
				SeriesID:  s.ID,
				OldName:   s.Name,
				NewName:   s.Name,
				Action:    "flag",
				BookCount: len(books),
			})
			continue
		}

		if cleaned == s.Name && pos == "" {
			continue
		}

		aid := 0
		if s.AuthorID != nil {
			aid = *s.AuthorID
		}
		key := groupKey{name: strings.ToLower(cleaned), authorID: aid}
		books, _ := store.GetBooksBySeriesIDCore(s.ID)

		if existingID, ok := canonical[key]; ok {
			actions = append(actions, seriesNormalizeAction{
				SeriesID:      s.ID,
				OldName:       s.Name,
				NewName:       cleaned,
				NewPosition:   pos,
				Action:        "merge_into",
				MergeTargetID: &existingID,
				BookCount:     len(books),
			})
		} else {
			canonical[key] = s.ID
			actions = append(actions, seriesNormalizeAction{
				SeriesID:    s.ID,
				OldName:     s.Name,
				NewName:     cleaned,
				NewPosition: pos,
				Action:      "rename",
				BookCount:   len(books),
			})
		}
	}
	return actions, nil
}

// buildSeriesNormalizePreview computes the dry-run normalize actions over store
// and assembles the preview response payload. Extracted from the former
// seriesNormalizePreview HTTP handler so the duplicates sub-package can obtain
// the identical payload through an injected closure (it cannot reference the
// unexported seriesNormalizeAction / seriesNormalizePreviewResult types).
func buildSeriesNormalizePreview(store seriesMergeStore) seriesNormalizePreviewResult {
	// A failed listing yields an empty preview, which reads as "nothing to do" to
	// an operator deciding whether to approve the run. The error is logged rather
	// than returned because this builds a payload for a dry-run view with no error
	// channel; SERIES-NORMALIZE-PREVIEW-SWALLOWS-ERROR in todo.d tracks giving it
	// one, which needs a handler signature change.
	actions, err := computeSeriesNormalizeActions(store)
	if err != nil {
		logging.Error(context.Background(),
			"series normalize preview could not list series; returning an EMPTY preview that must not be read as a clean library",
			"err", err)
	}

	flagged := make([]seriesNormalizeAction, 0)
	normal := make([]seriesNormalizeAction, 0)
	totalBooks := 0
	for _, a := range actions {
		if a.Action == "flag" {
			flagged = append(flagged, a)
		} else {
			normal = append(normal, a)
			totalBooks += a.BookCount
		}
	}

	return seriesNormalizePreviewResult{
		Actions:             normal,
		TotalSeriesAffected: len(normal),
		TotalBooksAffected:  totalBooks,
		FlaggedForReview:    flagged,
	}
}

// mergeSeriesGroupHelper moves all books from each series in mergeIDs to keepID,
// then deletes the now-empty series. Named with "Helper" suffix to avoid
// collision with the duplicates handler MergeSeriesGroup.
func mergeSeriesGroupHelper(store maintenanceStore, keepID int, mergeIDs []int) error {
	for _, fromID := range mergeIDs {
		// AllVersions, not the Core listing getter: this loop repoints every row
		// it is handed and then deletes fromID below, unconditionally. A
		// non-primary version the listing getter hides is never repointed and is
		// left holding a series that no longer exists.
		books, err := store.GetBooksBySeriesIDAllVersions(fromID)
		if err != nil {
			return fmt.Errorf("GetBooksBySeriesIDAllVersions(%d): %w", fromID, err)
		}

		for _, book := range books {
			current, err := store.GetBookByID(book.ID)
			if err != nil {
				return fmt.Errorf("GetBookByID(%s): %w", book.ID, err)
			}
			if current == nil {
				// Not a skippable row. DeleteSeries(fromID) below is unconditional,
				// so continuing here deletes the series while this book still points
				// at it. The two error branches around this one already return; this
				// was the one way through, and it was silent.
				return fmt.Errorf("book %s is listed under series %d but does not resolve; "+
					"refusing to delete series %d, which would strand it", book.ID, fromID, fromID)
			}

			current.SeriesID = &keepID
			if _, err = store.UpdateBook(book.ID, current); err != nil {
				return fmt.Errorf("UpdateBook(%s): %w", book.ID, err)
			}
		}

		if err = store.DeleteSeries(fromID); err != nil {
			return fmt.Errorf("DeleteSeries(%d): %w", fromID, err)
		}
	}

	return nil
}

// executeSeriesNormalizeCore renames and merges contaminated series, enqueues
// write-back for affected books, and returns the affected book IDs for the
// caller to run organize on.
// maintenanceStore is used because mergeSeriesGroupHelper requires it.
func executeSeriesNormalizeCore(
	ctx context.Context,
	store maintenanceStore,
	enqueueWriteBack func(bookID string),
) (affectedBookIDs []string, err error) {
	// Fatal, and deliberately so. An empty action list means "nothing needs
	// normalizing"; a failed listing means "nothing was examined". Continuing past
	// this would organize zero books and report success on a library nobody looked
	// at.
	actions, err := computeSeriesNormalizeActions(store)
	if err != nil {
		return nil, fmt.Errorf("series normalize: %w", err)
	}

	var errs []string

	// Collect affected book IDs BEFORE renaming/merging.
	//
	// Core, NOT AllVersions — deliberately, and this is the one getter in this
	// file that must stay filtered.
	//
	// affectedBookIDs is not a record of what was repointed. It is the worklist
	// the caller hands to ReOrganizeInPlace (duplicates_ops.go) and to the tag
	// write-back, i.e. it decides which FILES get moved and rewritten. The
	// organizer deliberately never organizes a non-primary version while a
	// primary exists in its version group (organizer/service.go:640), and
	// duplicates_ops.go calls ReOrganizeInPlace directly, bypassing that filter.
	// Widening this list therefore does not "keep the row and its file in sync"
	// — it overrides an explicit organize policy from the outside.
	//
	// It would also collide. The default folder/file patterns
	// (config/naming_patterns.go) carry no codec/quality/edition variable, so a
	// primary and its alternate rip compute the SAME target path; whichever the
	// stable series ordering happens to emit first would claim it and the other
	// would be refused. mergeSeriesGroupHelper repointing a non-primary version
	// is correct and necessary — moving its file is a separate question with a
	// different, already-settled answer.
	//
	// Residual, recorded rather than fixed here: a repointed non-primary version
	// keeps stale series tags, because nothing adds it to any write-back list.
	// Splitting this into an organize list (Core) and a write-back list
	// (AllVersions) is the real fix and would start writing tags to files this
	// op has never touched — a production-data decision, not a bug fix. Filed as
	// SERIES-NORMALIZE-WRITEBACK-SPLIT in todo.d.
	seen := make(map[string]bool)
	for _, a := range actions {
		if a.Action == "flag" {
			continue
		}
		books, bErr := store.GetBooksBySeriesIDCore(a.SeriesID)
		if bErr != nil {
			// Not skippable: these books are about to be renamed or merged, and
			// dropping them here means their files are never reorganized and
			// their tags never refreshed, with nothing to retry from.
			errs = append(errs, fmt.Sprintf(
				"GetBooksBySeriesIDCore(%d): %v — its books will be renamed/merged but NOT "+
					"reorganized or retagged; files will be left under the old series path",
				a.SeriesID, bErr))
			continue
		}
		for _, b := range books {
			if !seen[b.ID] {
				seen[b.ID] = true
				affectedBookIDs = append(affectedBookIDs, b.ID)
			}
		}
	}

	// First pass: rename.
	for _, a := range actions {
		if a.Action != "rename" {
			continue
		}
		if ctx.Err() != nil {
			return affectedBookIDs, ctx.Err()
		}
		if rErr := store.UpdateSeriesName(a.SeriesID, a.NewName); rErr != nil {
			errs = append(errs, fmt.Sprintf("UpdateSeriesName(%d, %q): %v", a.SeriesID, a.NewName, rErr))
		}
	}

	// Second pass: merge.
	for _, a := range actions {
		if a.Action != "merge_into" || a.MergeTargetID == nil {
			continue
		}
		if ctx.Err() != nil {
			return affectedBookIDs, ctx.Err()
		}
		if mErr := mergeSeriesGroupHelper(store, *a.MergeTargetID, []int{a.SeriesID}); mErr != nil {
			errs = append(errs, fmt.Sprintf("mergeSeriesGroupHelper(keep=%d, merge=%d): %v", *a.MergeTargetID, a.SeriesID, mErr))
		}
	}

	for _, id := range affectedBookIDs {
		enqueueWriteBack(id)
	}

	if len(errs) > 0 {
		return affectedBookIDs, fmt.Errorf("series normalize errors: %s", strings.Join(errs, "; "))
	}
	return affectedBookIDs, nil
}
