// file: internal/maintenance/jobs/cleanup_series.go
// version: 2.6.0
// guid: a1000002-0000-0000-0000-000000000002
// last-edited: 2026-08-23

package jobs

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

func init() { maintenance.Register(&cleanupSeriesJob{}) }

type cleanupSeriesJob struct{}

func (j *cleanupSeriesJob) ID() string       { return "cleanup-series" }
func (j *cleanupSeriesJob) Name() string     { return "Cleanup Series" }
func (j *cleanupSeriesJob) Category() string { return "library" }
func (j *cleanupSeriesJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: true}
}
func (j *cleanupSeriesJob) Description() string {
	return "Remove 1-book series and merge duplicate series"
}
func (j *cleanupSeriesJob) CanResume() bool { return false }

func (j *cleanupSeriesJob) Run(ctx context.Context, store maintenance.JobStore, reporter maintenance.ProgressReporter, dryRun bool) error {
	allSeries, err := store.GetAllSeries()
	if err != nil {
		return fmt.Errorf("failed to list series: %w", err)
	}

	bookCounts, err := store.GetAllSeriesBookCounts()
	if err != nil {
		return fmt.Errorf("failed to get series book counts: %w", err)
	}

	// bookCounts above is the DISPLAY count: GetAllSeriesBookCounts skips
	// trashed and non-primary rows, which is right for a badge and wrong as an
	// existence test. Both phases below delete series rows, so they also need
	// the UNFILTERED count -- "is anything still pointing at this?" is a
	// different question from "how many books should I show".
	//
	// Using the filtered count as an existence test is what produced the 6,893
	// phantom series IDs held by 13,322 live books recorded in
	// database/series_bookref.go. This job runs unattended, so the damage
	// accumulates silently.
	//
	// Fails CLOSED: a store that cannot answer aborts the job rather than
	// falling back to bookCounts.
	refCounts, err := database.SeriesRefCounts(store)
	if err != nil {
		return fmt.Errorf("cleanup-series refusing to run without unfiltered reference counts: %w", err)
	}

	reporter.SetTotal(len(allSeries))

	// Phase 1: single-book series
	var singleApplied, singleFound int
	deletedIDs := make(map[int]bool)

	for _, ser := range allSeries {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		count := bookCounts[ser.ID]
		if count != 1 {
			reporter.Increment()
			continue
		}

		books, bErr := store.GetBooksBySeriesIDCore(ser.ID)
		if bErr != nil || len(books) == 0 {
			reporter.Increment()
			continue
		}
		book := books[0]
		if book.SeriesSequence != nil && *book.SeriesSequence > 1 {
			reporter.Increment()
			continue
		}

		// The filtered count says one book, so this looks like a 1-book series
		// worth collapsing. Only act if the UNFILTERED count agrees: a series
		// with one primary book and three non-primary versions also reads as
		// count==1 here, and unlinking the one visible book then deleting the
		// row strands the other three.
		if refCounts[ser.ID] > 1 {
			slog.Info("Skipping 1-book series: more books reference it than the filtered count shows",
				"seriesID", ser.ID, "seriesName", ser.Name,
				"filtered_count", count, "unfiltered_count", refCounts[ser.ID])
			reporter.Increment()
			continue
		}

		singleFound++
		if !dryRun {
			if applyErr := csUnlinkAndDeleteSeries(store, &book, ser.ID); applyErr != nil {
				slog.Error("Failed to remove 1-book series", "seriesID", ser.ID, "seriesName", ser.Name, "err", applyErr)
			} else {
				deletedIDs[ser.ID] = true
				singleApplied++
			}
		}
		reporter.Increment()
	}

	// Phase 2: duplicate series by normalized name
	normGroups := make(map[string][]database.Series)
	for _, ser := range allSeries {
		if deletedIDs[ser.ID] {
			continue
		}
		key := csNormalizeSeriesName(ser.Name)
		normGroups[key] = append(normGroups[key], ser)
	}

	var dupApplied, dupFound int
	for normName, group := range normGroups {
		if len(group) < 2 {
			continue
		}
		dupFound++

		keepIdx := 0
		for i, ser := range group {
			if bookCounts[ser.ID] > bookCounts[group[keepIdx].ID] {
				keepIdx = i
			}
		}
		keeper := group[keepIdx]

		var mergeIDs []int
		for i, ser := range group {
			if i != keepIdx {
				mergeIDs = append(mergeIDs, ser.ID)
			}
		}

		if !dryRun {
			if mergeErr := csMergeSeriesGroup(store, keeper.ID, mergeIDs, refCounts); mergeErr != nil {
				slog.Error("Failed to merge series group", "normName", normName, "err", mergeErr)
			} else {
				dupApplied++
			}
		}
	}

	slog.Info("Done single_found single_applied dup_groups_found dup_applied dryRun", "singleFound", singleFound, "singleApplied", singleApplied, "dupFound", dupFound, "dupApplied", dupApplied, "dryRun", dryRun)
	return nil
}

// csUnlinkAndDeleteSeries only reads book.ID from the passed-in row (BookCore
// is sufficient — see caller) and hydrates the real writeback target via
// GetBookByID below, so no heavy-field fidelity is lost.
func csUnlinkAndDeleteSeries(store seriesUnlinker, book *database.BookCore, seriesID int) error {
	current, err := store.GetBookByID(book.ID)
	if err != nil {
		return fmt.Errorf("GetBookByID: %w", err)
	}
	if current == nil {
		return fmt.Errorf("book %s not found", book.ID)
	}
	current.SeriesID = nil
	current.SeriesSequence = nil
	if _, err = store.UpdateBook(book.ID, current); err != nil {
		return fmt.Errorf("UpdateBook: %w", err)
	}
	if err = store.DeleteSeries(seriesID); err != nil {
		return fmt.Errorf("DeleteSeries: %w", err)
	}
	return nil
}

// csMergeSeriesGroup folds mergeIDs into keepID. refCounts is the UNFILTERED
// seriesID -> book-count map; it is required, not optional, because the
// reassignment loop below reads membership from GetBooksBySeriesIDCore, which
// hides trashed and non-primary rows. Deleting a series whose unfiltered count
// exceeds what was reassigned strands those rows on a series ID that no longer
// resolves.
func csMergeSeriesGroup(store seriesMerger, keepID int, mergeIDs []int, refCounts map[int]int) error {
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
		if stranded := refCounts[fromID] - len(books); stranded > 0 {
			// Books were still reassigned above -- that is strictly an
			// improvement and is not rolled back. Only the row removal is
			// refused, leaving a resolvable series ID for the rows we could
			// not see. A later run, or the reconciler, can finish the job once
			// those rows become visible.
			slog.Warn("Keeping merged-from series row: books reference it that the filtered getter cannot see",
				"seriesID", fromID, "keepID", keepID,
				"reassigned", len(books), "stranded_if_deleted", stranded)
			continue
		}
		if err = store.DeleteSeries(fromID); err != nil {
			return fmt.Errorf("DeleteSeries(%d): %w", fromID, err)
		}
	}
	return nil
}

var csNonAlphanumRE = regexp.MustCompile(`[^\p{L}\p{N}\s]+`)

func csNormalizeSeriesName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.TrimPrefix(s, "the ")
	for _, suffix := range []string{" series", " saga", " trilogy", " duology", " quartet"} {
		if strings.HasSuffix(s, suffix) {
			s = s[:len(s)-len(suffix)]
			break
		}
	}
	s = csNonAlphanumRE.ReplaceAllString(s, " ")
	fields := strings.FieldsFunc(s, unicode.IsSpace)
	return strings.Join(fields, " ")
}

// Policy declares the bridge's existing behaviour verbatim: see DefaultPolicy.
func (j *cleanupSeriesJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}
