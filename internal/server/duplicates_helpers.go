// file: internal/server/duplicates_helpers.go
// version: 1.2.0
// guid: 550a807d-8c00-4e34-9a8c-52a80710a0b9
// last-edited: 2026-07-07
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
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	ulid "github.com/oklog/ulid/v2"
)

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
func (s *Server) executeSeriesPrune(ctx context.Context, store interface {
	database.BookStore
	database.AuthorStore
	database.SeriesStore
	database.OperationStore
}, progress operations.ProgressReporter, operationID string) error {
	_ = progress.Log("info", "Starting series auto-prune...", nil)

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

	// Phase 1: Merge duplicates
	totalMerged := 0
	var mergeErrors []string
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
		for i, s := range group {
			books, err := store.GetBooksBySeriesIDCore(s.ID)
			if err != nil {
				continue
			}
			bc := len(books)
			if bc > canonicalBookCount || (bc == canonicalBookCount && s.ID < group[canonicalIdx].ID) {
				canonicalIdx = i
				canonicalBookCount = bc
			}
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
			books, err := store.GetBooksBySeriesIDCore(ser.ID)
			if err != nil {
				mergeErrors = append(mergeErrors, fmt.Sprintf("failed to get books for series %d: %v", ser.ID, err))
				continue
			}
			for _, bookCore := range books {
				oldSeriesID := ser.ID
				full, herr := store.GetBookByID(bookCore.ID)
				if herr != nil {
					mergeErrors = append(mergeErrors, fmt.Sprintf("failed to hydrate book %s: %v", bookCore.ID, herr))
					continue
				}
				if full == nil {
					continue
				}
				full.SeriesID = &keepID
				if _, err := store.UpdateBook(full.ID, full); err != nil {
					mergeErrors = append(mergeErrors, fmt.Sprintf("failed to reassign book %s: %v", bookCore.ID, err))
				} else if operationID != "" {
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
	orphansDeleted := 0
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
		_ = progress.Log("warn", fmt.Sprintf("Failed to refresh series list: %v", err), nil)
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
	if len(mergeErrors) > 0 {
		errDetail := strings.Join(mergeErrors[:min(len(mergeErrors), 10)], "; ")
		_ = progress.Log("warn", fmt.Sprintf("Errors: %s", errDetail), nil)
	}
	_ = progress.UpdateProgress(totalSteps, totalSteps, fmt.Sprintf("%s (%d/%d 100.00%%)", resultMsg, totalSteps, totalSteps))

	if s.dedupCache != nil {
		s.dedupCache.InvalidateAll()
	}

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
func computeSeriesNormalizeActions(store interface {
	database.SeriesStore
	database.BookStore
}) []seriesNormalizeAction {
	allSeries, err := store.GetAllSeries()
	if err != nil {
		return nil
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
	return actions
}

// buildSeriesNormalizePreview computes the dry-run normalize actions over store
// and assembles the preview response payload. Extracted from the former
// seriesNormalizePreview HTTP handler so the duplicates sub-package can obtain
// the identical payload through an injected closure (it cannot reference the
// unexported seriesNormalizeAction / seriesNormalizePreviewResult types).
func buildSeriesNormalizePreview(store interface {
	database.SeriesStore
	database.BookStore
}) seriesNormalizePreviewResult {
	actions := computeSeriesNormalizeActions(store)

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
		books, err := store.GetBooksBySeriesIDCore(fromID)
		if err != nil {
			return fmt.Errorf("GetBooksBySeriesIDCore(%d): %w", fromID, err)
		}

		for _, book := range books {
			current, err := store.GetBookByID(book.ID)
			if err != nil {
				return fmt.Errorf("GetBookByID(%s): %w", book.ID, err)
			}
			if current == nil {
				continue
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
	actions := computeSeriesNormalizeActions(store)

	// Collect affected book IDs BEFORE renaming/merging.
	seen := make(map[string]bool)
	for _, a := range actions {
		if a.Action == "flag" {
			continue
		}
		books, bErr := store.GetBooksBySeriesIDCore(a.SeriesID)
		if bErr != nil {
			continue
		}
		for _, b := range books {
			if !seen[b.ID] {
				seen[b.ID] = true
				affectedBookIDs = append(affectedBookIDs, b.ID)
			}
		}
	}

	var errs []string

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
