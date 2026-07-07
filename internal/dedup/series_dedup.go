// file: internal/dedup/series_dedup.go
// version: 1.2.0
// guid: d4e5f6a7-b8c9-0123-defa-234567890123
// last-edited: 2026-07-07

// Package dedup: series_dedup.go contains the extracted execution logic for the
// "dedup.series-scan", "dedup.series-dedup", and "dedup.series-merge" async
// operations.  The *Server wrappers in internal/server/duplicates_ops.go are
// now thin callers that write results into the server-owned dedupCache.
package dedup

import (
	"context"
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	ulid "github.com/oklog/ulid/v2"
)

// ── series scan ───────────────────────────────────────────────────────────────

// SeriesBookSummary is a lightweight book reference used inside SeriesDupGroup.
type SeriesBookSummary struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	CoverURL string `json:"cover_url,omitempty"`
}

// SeriesWithBooks enriches a database.Series with its first few books and the
// author name derived from the authorID.
type SeriesWithBooks struct {
	database.Series
	Books      []SeriesBookSummary `json:"books"`
	AuthorName string              `json:"author_name,omitempty"`
}

// SeriesDupGroup is a group of series that are likely duplicates of each other.
type SeriesDupGroup struct {
	Name          string            `json:"name"`
	Count         int               `json:"count"`
	Series        []SeriesWithBooks `json:"series"`
	SuggestedName string            `json:"suggested_name,omitempty"`
	MatchType     string            `json:"match_type"` // "exact" | "subseries"
}

// SeriesScanResult is the return value of ScanSeriesDuplicates.
type SeriesScanResult struct {
	Groups      []SeriesDupGroup
	TotalSeries int
}

// isGarbageSeries returns true for series names that consist only of digits or
// are blank — these are noise that should be excluded from duplicate grouping.
func isGarbageSeries(name string) bool {
	trimmed := strings.TrimSpace(name)
	if len(trimmed) == 0 {
		return true
	}
	for _, r := range trimmed {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ExtractSeriesNameForDedup tries to extract a canonical series name from
// patterns like "Book Title: Series Name" or "Series Name, Book N".
// It returns the candidate name and true on success, or ("", false) when no
// pattern matches.
func ExtractSeriesNameForDedup(name string) (string, bool) {
	// Pattern: "Book Title: Series Name"
	if idx := strings.LastIndex(name, ": "); idx > 0 {
		after := strings.TrimSpace(name[idx+2:])
		before := strings.TrimSpace(name[:idx])
		if len(after) > 3 && len(after) < len(before) {
			return after, true
		}
		if len(before) > 3 && len(before) < len(after) {
			return before, true
		}
	}
	// Pattern: "Series Name, Book N" / "Series Name, Vol N" etc.
	commaPatterns := []string{", book ", ", vol ", ", volume ", ", #"}
	lower := strings.ToLower(name)
	for _, pat := range commaPatterns {
		if idx := strings.Index(lower, pat); idx > 0 {
			return strings.TrimSpace(name[:idx]), true
		}
	}
	return "", false
}

// enrichSeries loads books (up to 5) and author name for each series.
func enrichSeries(store database.Store, seriesList []database.Series, authorNameMap map[int]string) []SeriesWithBooks {
	result := make([]SeriesWithBooks, 0, len(seriesList))
	for _, s := range seriesList {
		authorName := ""
		if s.AuthorID != nil {
			authorName = authorNameMap[*s.AuthorID]
		}
		sw := SeriesWithBooks{Series: s, AuthorName: authorName}
		if books, err := store.GetBooksBySeriesIDCore(s.ID); err == nil {
			limit := 5
			if len(books) < limit {
				limit = len(books)
			}
			for _, b := range books[:limit] {
				cover := ""
				if b.CoverURL != nil {
					cover = *b.CoverURL
				}
				sw.Books = append(sw.Books, SeriesBookSummary{
					ID:       b.ID,
					Title:    b.Title,
					CoverURL: cover,
				})
			}
		}
		result = append(result, sw)
	}
	return result
}

// ScanSeriesDuplicates groups all non-garbage series by exact normalised name
// and by sub-series pattern, enriches each group with book/author info, and
// returns the consolidated result.
func ScanSeriesDuplicates(
	_ context.Context,
	store database.Store,
	progress ProgressReporter,
) (SeriesScanResult, error) {
	// Fixed-step scan: 6 stages (start, load, author-lookup, exact, subseries, done).
	const totalSteps = 6
	report := func(step int, msg string) {
		if progress != nil {
			pct := float64(step) * 100.0 / float64(totalSteps)
			_ = progress.UpdateProgress(step, totalSteps,
				fmt.Sprintf("%s (%d/%d, %.2f%%)", msg, step, totalSteps, pct))
		}
	}

	report(0, "Fetching series...")

	allSeries, err := store.GetAllSeries()
	if err != nil {
		return SeriesScanResult{}, err
	}
	report(1, fmt.Sprintf("Loaded %d series, grouping...", len(allSeries)))

	// Build exact-match groups (normalised name → series list).
	exactGroups := make(map[string][]database.Series)
	for _, s := range allSeries {
		if isGarbageSeries(s.Name) {
			continue
		}
		key := util.NormalizeString(s.Name)
		exactGroups[key] = append(exactGroups[key], s)
	}

	report(2, "Building author lookup...")

	allAuthors, _ := store.GetAllAuthors()
	authorNameMap := make(map[int]string, len(allAuthors))
	for _, a := range allAuthors {
		authorNameMap[a.ID] = a.Name
	}

	var result []SeriesDupGroup
	seen := make(map[int]bool)

	report(3, "Finding exact duplicates...")

	groupKeys := make([]string, 0, len(exactGroups))
	for k := range exactGroups {
		groupKeys = append(groupKeys, k)
	}

	processed := 0
	totalGroups := len(groupKeys)
	for _, k := range groupKeys {
		group := exactGroups[k]
		if len(group) < 2 {
			continue
		}
		for _, s := range group {
			seen[s.ID] = true
		}
		suggested, _ := ExtractSeriesNameForDedup(group[0].Name)
		result = append(result, SeriesDupGroup{
			Name:          group[0].Name,
			Count:         len(group),
			Series:        enrichSeries(store, group, authorNameMap),
			SuggestedName: suggested,
			MatchType:     "exact",
		})
		processed++
		if processed%10 == 0 && totalGroups > 0 {
			if progress != nil {
				pct := float64(processed) * 100.0 / float64(totalGroups)
				_ = progress.UpdateProgress(processed, totalGroups,
					fmt.Sprintf("Processing groups... (%d/%d, %.2f%%)", processed, totalGroups, pct))
			}
		}
	}

	report(4, "Finding sub-series patterns...")

	seriesByNormalizedName := make(map[string][]database.Series)
	for _, s := range allSeries {
		seriesByNormalizedName[util.NormalizeString(s.Name)] =
			append(seriesByNormalizedName[util.NormalizeString(s.Name)], s)
	}

	for _, s := range allSeries {
		if seen[s.ID] || isGarbageSeries(s.Name) {
			continue
		}
		suggested, ok := ExtractSeriesNameForDedup(s.Name)
		if !ok {
			continue
		}
		suggestedKey := util.NormalizeString(suggested)
		if matches, exists := seriesByNormalizedName[suggestedKey]; exists {
			group := []database.Series{s}
			seen[s.ID] = true
			for _, m := range matches {
				if !seen[m.ID] {
					group = append(group, m)
					seen[m.ID] = true
				}
			}
			if len(group) >= 2 {
				result = append(result, SeriesDupGroup{
					Name:          s.Name,
					Count:         len(group),
					Series:        enrichSeries(store, group, authorNameMap),
					SuggestedName: suggested,
					MatchType:     "subseries",
				})
			}
		}
	}

	report(6, fmt.Sprintf("Found %d duplicate groups", len(result)))

	return SeriesScanResult{
		Groups:      result,
		TotalSeries: len(allSeries),
	}, nil
}

// ── series dedup (bulk merge by normalised name) ──────────────────────────────

// SeriesDedupResult summarises the outcome of DedupSeries.
type SeriesDedupResult struct {
	TotalMerged int
	Errors      []string
}

// DedupSeries groups all series by normalised name and merges every duplicate
// group, keeping the series with the lowest ID (preferring one with an author).
// It does NOT invalidate the server cache — the caller must do that.
func DedupSeries(
	_ context.Context,
	store database.Store,
	progress ProgressReporter,
) (SeriesDedupResult, error) {
	if progress != nil {
		_ = progress.Log("info", "Starting series deduplication...", nil)
		_ = progress.UpdateProgress(0, 1, "Starting series deduplication... (0/1, 0.00%)")
	}

	allSeries, err := store.GetAllSeries()
	if err != nil {
		return SeriesDedupResult{}, fmt.Errorf("failed to get series: %w", err)
	}

	if progress != nil {
		denom := len(allSeries)
		if denom == 0 {
			denom = 1
		}
		_ = progress.UpdateProgress(0, denom,
			fmt.Sprintf("Scanning %d series for duplicates... (0/%d, 0.00%%)", len(allSeries), len(allSeries)))
	}

	// Group by normalised name.
	groups := make(map[string][]database.Series)
	for _, s := range allSeries {
		key := util.NormalizeString(s.Name)
		groups[key] = append(groups[key], s)
	}

	var dupGroups [][]database.Series
	for _, group := range groups {
		if len(group) >= 2 {
			dupGroups = append(dupGroups, group)
		}
	}

	if progress != nil {
		msg := fmt.Sprintf("Found %d duplicate groups to merge", len(dupGroups))
		_ = progress.Log("info", msg, nil)
		denom := len(dupGroups)
		if denom == 0 {
			denom = 1
		}
		_ = progress.UpdateProgress(0, denom,
			fmt.Sprintf("%s (0/%d, 0.00%%)", msg, len(dupGroups)))
	}

	var result SeriesDedupResult
	for gi, group := range dupGroups {
		if progress != nil && progress.IsCanceled() {
			_ = progress.Log("warn", "Operation cancelled by user", nil)
			return result, fmt.Errorf("cancelled")
		}

		// Pick the "best" canonical: prefer one with an authorID, then lowest ID.
		keepIdx := 0
		for i, s := range group {
			if s.AuthorID != nil && group[keepIdx].AuthorID == nil {
				keepIdx = i
			} else if (s.AuthorID != nil) == (group[keepIdx].AuthorID != nil) && s.ID < group[keepIdx].ID {
				keepIdx = i
			}
		}
		keepID := group[keepIdx].ID

		for i, s := range group {
			if i == keepIdx {
				continue
			}
			// Hydrate the full row via GetBookByID and mutate/write THAT. The
			// tempting shortcut — bookCore.ToBook() then UpdateBook — would drop
			// the denormalized Author/Series (db:"-"): they are NOT covered by
			// UpdateBook's STOR-1 preserve-on-empty guard (which only restores
			// Description/VersionNotes/BookSig*). Hydrating keeps the write
			// correct and self-contained.
			// See docs/specs/2026-07-05-store-getter-fidelity-unification.md.
			books, err := store.GetBooksBySeriesIDCore(s.ID)
			if err != nil {
				result.Errors = append(result.Errors,
					fmt.Sprintf("failed to get books for series %d: %v", s.ID, err))
				continue
			}
			for _, bookCore := range books {
				full, herr := store.GetBookByID(bookCore.ID)
				if herr != nil {
					result.Errors = append(result.Errors,
						fmt.Sprintf("failed to hydrate book %s: %v", bookCore.ID, herr))
					continue
				}
				if full == nil {
					continue
				}
				full.SeriesID = &keepID
				if _, err := store.UpdateBook(full.ID, full); err != nil {
					result.Errors = append(result.Errors,
						fmt.Sprintf("failed to reassign book %s: %v", bookCore.ID, err))
				}
			}
			if err := store.DeleteSeries(s.ID); err != nil {
				result.Errors = append(result.Errors,
					fmt.Sprintf("failed to delete series %d: %v", s.ID, err))
			} else {
				result.TotalMerged++
			}
		}

		if progress != nil {
			pct := float64(gi+1) * 100.0 / float64(len(dupGroups))
			_ = progress.UpdateProgress(gi+1, len(dupGroups),
				fmt.Sprintf("Merged %d/%d groups (%d series merged, %.2f%%)",
					gi+1, len(dupGroups), result.TotalMerged, pct))
		}
	}

	if progress != nil {
		msg := fmt.Sprintf("Series deduplication complete: merged %d duplicates, %d errors",
			result.TotalMerged, len(result.Errors))
		_ = progress.Log("info", msg, nil)
		if len(result.Errors) > 0 {
			errDetail := strings.Join(result.Errors[:minInt(len(result.Errors), 10)], "; ")
			_ = progress.Log("warn", fmt.Sprintf("Merge errors: %s", errDetail), nil)
		}
		denom := len(dupGroups)
		if denom == 0 {
			denom = 1
		}
		_ = progress.UpdateProgress(denom, denom,
			fmt.Sprintf("%s (%d/%d, 100.00%%)", msg, len(dupGroups), len(dupGroups)))
	}

	return result, nil
}

// ── series merge (explicit keep + merge IDs) ─────────────────────────────────

// SeriesMergeResult summarises the outcome of MergeSeries.
type SeriesMergeResult struct {
	MergedCount int
	Errors      []string
}

// MergeSeries reassigns all books from each series in mergeIDs to keepID,
// optionally renames the kept series to customName, and relinks authors.
// It does NOT invalidate the server cache — the caller must do that.
//
// opID is written into OperationChange records for audit.
func MergeSeries(
	_ context.Context,
	store database.Store,
	opID string,
	keepID int,
	mergeIDs []int,
	customName string,
	progress ProgressReporter,
) (SeriesMergeResult, error) {
	customName = strings.TrimSpace(customName)

	keepSeries, err := store.GetSeriesByID(keepID)
	if err != nil || keepSeries == nil {
		return SeriesMergeResult{}, fmt.Errorf("keep series %d not found", keepID)
	}

	keepName := keepSeries.Name
	if customName != "" {
		keepName = customName
	}

	// Rename the kept series if requested.
	if customName != "" {
		oldName := keepSeries.Name
		if err := store.UpdateSeriesName(keepID, customName); err != nil {
			return SeriesMergeResult{}, fmt.Errorf("failed to rename series to %q: %w", customName, err)
		}
		_ = store.CreateOperationChange(&database.OperationChange{
			ID:          ulid.Make().String(),
			OperationID: opID,
			ChangeType:  "metadata_update",
			FieldName:   "series_name",
			OldValue:    oldName,
			NewValue:    customName,
		})
		if progress != nil {
			_ = progress.Log("info",
				fmt.Sprintf("Renamed series from %q to %q", oldName, customName), nil)
		}
	}

	total := len(mergeIDs)
	if progress != nil {
		_ = progress.Log("info",
			fmt.Sprintf("Merging %d series into %q", total, keepName), nil)
		denom := total
		if denom == 0 {
			denom = 1
		}
		_ = progress.UpdateProgress(0, denom,
			fmt.Sprintf("Starting series merge... (0/%d, 0.00%%)", total))
	}

	// Collect all unique author IDs from the full set of series (keep + merge).
	allAuthorIDs := make(map[int]bool)
	allSeriesIDs := append([]int{keepID}, mergeIDs...)
	for _, sid := range allSeriesIDs {
		s, err := store.GetSeriesByID(sid)
		if err == nil && s != nil && s.AuthorID != nil {
			allAuthorIDs[*s.AuthorID] = true
		}
	}

	var result SeriesMergeResult
	for i, mergeID := range mergeIDs {
		if progress != nil && progress.IsCanceled() {
			return result, fmt.Errorf("cancelled")
		}
		if mergeID == keepID {
			continue
		}
		// Hydrate the full row via GetBookByID and mutate/write THAT. The
		// tempting shortcut — bookCore.ToBook() then UpdateBook — would drop
		// the denormalized Author/Series (db:"-"): they are NOT covered by
		// UpdateBook's STOR-1 preserve-on-empty guard (which only restores
		// Description/VersionNotes/BookSig*). Hydrating keeps the write
		// correct and self-contained.
		// See docs/specs/2026-07-05-store-getter-fidelity-unification.md.
		books, err := store.GetBooksBySeriesIDCore(mergeID)
		if err != nil {
			result.Errors = append(result.Errors,
				fmt.Sprintf("failed to get books for series %d: %v", mergeID, err))
			continue
		}

		for _, bookCore := range books {
			oldSeriesID := ""
			if bookCore.SeriesID != nil {
				oldSeriesID = fmt.Sprintf("%d", *bookCore.SeriesID)
			}
			full, herr := store.GetBookByID(bookCore.ID)
			if herr != nil {
				result.Errors = append(result.Errors,
					fmt.Sprintf("failed to hydrate book %s: %v", bookCore.ID, herr))
				continue
			}
			if full == nil {
				continue
			}
			full.SeriesID = &keepID
			if _, err := store.UpdateBook(full.ID, full); err != nil {
				result.Errors = append(result.Errors,
					fmt.Sprintf("failed to reassign book %s: %v", bookCore.ID, err))
			} else {
				_ = store.CreateOperationChange(&database.OperationChange{
					ID:          ulid.Make().String(),
					OperationID: opID,
					BookID:      bookCore.ID,
					ChangeType:  "metadata_update",
					FieldName:   "series_id",
					OldValue:    oldSeriesID,
					NewValue:    fmt.Sprintf("%d", keepID),
				})
			}
		}

		// Record the series deletion.
		mergeSeries, _ := store.GetSeriesByID(mergeID)
		mergeSeriesName := ""
		if mergeSeries != nil {
			mergeSeriesName = mergeSeries.Name
		}
		if err := store.DeleteSeries(mergeID); err != nil {
			result.Errors = append(result.Errors,
				fmt.Sprintf("failed to delete series %d: %v", mergeID, err))
		} else {
			result.MergedCount++
			_ = store.CreateOperationChange(&database.OperationChange{
				ID:          ulid.Make().String(),
				OperationID: opID,
				BookID:      "",
				ChangeType:  "series_delete",
				FieldName:   "series",
				OldValue:    fmt.Sprintf("%d:%s", mergeID, mergeSeriesName),
				NewValue:    fmt.Sprintf("merged_into:%d", keepID),
			})
		}

		if progress != nil {
			pct := float64(i+1) * 100.0 / float64(total)
			_ = progress.UpdateProgress(i+1, total,
				fmt.Sprintf("Merged %d/%d series (%.2f%%)", i+1, total, pct))
		}
	}

	// Link all books in the kept series to every unique author collected above.
	if len(allAuthorIDs) > 1 {
		if progress != nil {
			_ = progress.Log("info",
				fmt.Sprintf("Linking books to %d authors", len(allAuthorIDs)), nil)
		}
		allBooks, err := store.GetBooksBySeriesIDCore(keepID)
		if err == nil {
			for _, book := range allBooks {
				existing, _ := store.GetBookAuthors(book.ID)
				existingMap := make(map[int]bool)
				for _, ba := range existing {
					existingMap[ba.AuthorID] = true
				}
				authors := existing
				var addedAuthors []int
				for aid := range allAuthorIDs {
					if !existingMap[aid] {
						authors = append(authors, database.BookAuthor{BookID: book.ID, AuthorID: aid})
						addedAuthors = append(addedAuthors, aid)
					}
				}
				if len(authors) > len(existing) {
					if err := store.SetBookAuthors(book.ID, authors); err != nil {
						result.Errors = append(result.Errors,
							fmt.Sprintf("failed to set authors for book %s: %v", book.ID, err))
					} else {
						_ = store.CreateOperationChange(&database.OperationChange{
							ID:          ulid.Make().String(),
							OperationID: opID,
							BookID:      book.ID,
							ChangeType:  "author_link",
							FieldName:   "book_authors",
							OldValue:    fmt.Sprintf("%d authors", len(existing)),
							NewValue:    fmt.Sprintf("%d authors (added %v)", len(authors), addedAuthors),
						})
					}
				}
			}
		}
	}

	if progress != nil {
		msg := fmt.Sprintf("Series merge complete: merged %d, %d errors",
			result.MergedCount, len(result.Errors))
		_ = progress.Log("info", msg, nil)
		if len(result.Errors) > 0 {
			errDetail := strings.Join(result.Errors[:minInt(len(result.Errors), 10)], "; ")
			_ = progress.Log("warn", fmt.Sprintf("Errors: %s", errDetail), nil)
		}
		denom := total
		if denom == 0 {
			denom = 1
		}
		_ = progress.UpdateProgress(denom, denom,
			fmt.Sprintf("%s (%d/%d, 100.00%%)", msg, total, total))
	}

	return result, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
