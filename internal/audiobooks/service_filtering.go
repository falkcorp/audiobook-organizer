// file: internal/audiobooks/service_filtering.go
// version: 1.4.0
// guid: b4e8c3d2-e5f6-7a80-9b0c-1d2e3f4a5b6c
// last-edited: 2026-07-18

package audiobooks

import (
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// sortFieldMap maps sort keys to comparison functions.
// Each function returns <0 if a<b, 0 if equal, >0 if a>b.
var sortFieldMap = map[string]func(a, b *database.Book) int{
	"title": func(a, b *database.Book) int {
		return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
	},
	"author": func(a, b *database.Book) int {
		an := ""
		bn := ""
		if a.Author != nil {
			an = a.Author.Name
		}
		if b.Author != nil {
			bn = b.Author.Name
		}
		return strings.Compare(strings.ToLower(an), strings.ToLower(bn))
	},
	"narrator": func(a, b *database.Book) int {
		return strings.Compare(strings.ToLower(derefStr(a.Narrator)), strings.ToLower(derefStr(b.Narrator)))
	},
	"series": func(a, b *database.Book) int {
		an := ""
		bn := ""
		if a.Series != nil {
			an = a.Series.Name
		}
		if b.Series != nil {
			bn = b.Series.Name
		}
		return strings.Compare(strings.ToLower(an), strings.ToLower(bn))
	},
	"genre": func(a, b *database.Book) int {
		return strings.Compare(strings.ToLower(derefStr(a.Genre)), strings.ToLower(derefStr(b.Genre)))
	},
	"year": func(a, b *database.Book) int {
		ay := derefInt(a.AudiobookReleaseYear)
		by := derefInt(b.AudiobookReleaseYear)
		if ay == 0 {
			ay = derefInt(a.PrintYear)
		}
		if by == 0 {
			by = derefInt(b.PrintYear)
		}
		return ay - by
	},
	"language": func(a, b *database.Book) int {
		return strings.Compare(strings.ToLower(derefStr(a.Language)), strings.ToLower(derefStr(b.Language)))
	},
	"publisher": func(a, b *database.Book) int {
		return strings.Compare(strings.ToLower(derefStr(a.Publisher)), strings.ToLower(derefStr(b.Publisher)))
	},
	"format": func(a, b *database.Book) int {
		return strings.Compare(strings.ToLower(a.Format), strings.ToLower(b.Format))
	},
	"duration": func(a, b *database.Book) int {
		return derefInt(a.Duration) - derefInt(b.Duration)
	},
	"bitrate": func(a, b *database.Book) int {
		return derefInt(a.Bitrate) - derefInt(b.Bitrate)
	},
	"file_size": func(a, b *database.Book) int {
		diff := derefInt64(a.FileSize) - derefInt64(b.FileSize)
		if diff < 0 {
			return -1
		}
		if diff > 0 {
			return 1
		}
		return 0
	},
	"codec": func(a, b *database.Book) int {
		return strings.Compare(strings.ToLower(derefStr(a.Codec)), strings.ToLower(derefStr(b.Codec)))
	},
	"created_at": func(a, b *database.Book) int {
		return cmpTime(a.CreatedAt, b.CreatedAt)
	},
	"updated_at": func(a, b *database.Book) int {
		return cmpTime(a.UpdatedAt, b.UpdatedAt)
	},
	"library_state": func(a, b *database.Book) int {
		return strings.Compare(strings.ToLower(derefStr(a.LibraryState)), strings.ToLower(derefStr(b.LibraryState)))
	},
	"quality": func(a, b *database.Book) int {
		return strings.Compare(strings.ToLower(derefStr(a.Quality)), strings.ToLower(derefStr(b.Quality)))
	},
	"edition": func(a, b *database.Book) int {
		return strings.Compare(strings.ToLower(derefStr(a.Edition)), strings.ToLower(derefStr(b.Edition)))
	},
	// Aliases for frontend field names (e.g. SortField enum uses suffixed variants)
	"duration_seconds": func(a, b *database.Book) int {
		return derefInt(a.Duration) - derefInt(b.Duration)
	},
	"bitrate_kbps": func(a, b *database.Book) int {
		return derefInt(a.Bitrate) - derefInt(b.Bitrate)
	},
	"file_size_bytes": func(a, b *database.Book) int {
		diff := derefInt64(a.FileSize) - derefInt64(b.FileSize)
		if diff < 0 {
			return -1
		}
		if diff > 0 {
			return 1
		}
		return 0
	},
	"sample_rate_hz": func(a, b *database.Book) int {
		return derefInt(a.SampleRate) - derefInt(b.SampleRate)
	},
}

// applySorting sorts a slice of books in-place based on the filter's SortBy and SortOrder.
func applySorting(books []database.Book, f ListFilters) {
	if f.SortBy == "" {
		return
	}
	cmpFn, ok := sortFieldMap[f.SortBy]
	if !ok {
		return
	}
	sort.SliceStable(books, func(i, j int) bool {
		result := cmpFn(&books[i], &books[j])
		if result == 0 {
			// Tiebreaker: sort by ID for stable ordering
			result = strings.Compare(books[i].ID, books[j].ID)
		}
		if f.SortOrder == "desc" {
			return result > 0
		}
		return result < 0
	})
}

// paginateFilteredBooks slices books to the given offset/limit window.
// offset<=0 is treated as "from the start"; offset>=len(books) returns nil
// (page past the end); limit<=0 means "no limit". Shared by the post-filter
// pagination pass and the didPushdown+heavySorting pushdown path in
// GetAudiobooks so both paginate identically.
func paginateFilteredBooks(books []database.Book, limit, offset int) []database.Book {
	if offset > 0 && offset < len(books) {
		books = books[offset:]
	} else if offset >= len(books) {
		return nil
	}
	if limit > 0 && limit < len(books) {
		books = books[:limit]
	}
	return books
}

// matchesPerUserFilter evaluates one per-user FieldFilter against a
// UserBookState. A nil state is treated as zero-value (Status="",
// ProgressPct=0, no last activity) so that e.g. `-read_status:finished`
// correctly matches books the user has never opened. Mirrors the
// semantics of playlist_evaluator.perUserFilterMatches so smart
// playlists and the library list agree on what "finished" means.
func matchesPerUserFilter(state *database.UserBookState, f FieldFilter) bool {
	if state == nil {
		state = &database.UserBookState{}
	}
	switch f.Field {
	case "read_status":
		return strings.EqualFold(state.Status, f.Value)
	case "progress_pct":
		// Listing-side FieldFilters carry no operator (parser strips
		// them at this layer), so equality is the only sensible match.
		want, err := strconv.Atoi(f.Value)
		if err != nil {
			return false
		}
		return state.ProgressPct == want
	case "last_played":
		// Without an operator we can only test presence — value is
		// ignored. Useful as `-last_played:` (never played).
		return !state.LastActivityAt.IsZero()
	default:
		return false
	}
}

// matchesAllPerUserFilters returns true iff every per-user filter
// matches (with negation applied), i.e. the book belongs in the
// filtered set for this user.
func matchesAllPerUserFilters(state *database.UserBookState, filters []FieldFilter) bool {
	for _, f := range filters {
		ok := matchesPerUserFilter(state, f)
		if f.Negated {
			ok = !ok
		}
		if !ok {
			return false
		}
	}
	return true
}

// matchesFieldFilters returns true if a book matches all the given field filters.
// All filters are ANDed: every filter must match for the book to be included.
func matchesFieldFilters(book database.Book, filters []FieldFilter) bool {
	for _, f := range filters {
		matches := fieldMatchesValue(book, f.Field, f.Value)
		if f.Negated && matches {
			return false // NOT filter: exclude if matches
		}
		if !f.Negated && !matches {
			return false // positive filter: exclude if doesn't match
		}
	}
	return true
}

// matchesFieldFiltersWithStrippedFallback evaluates field filters against a
// memdb-resident *Book, fetching the full Book from Pebble (via fetchFull)
// only when filters reference stripped fields AND all cheap filters have
// already passed. This keeps the common path (no stripped filters, or row
// already filtered out by a cheap predicate) at memdb cost.
//
// If fetchFull returns nil/err the row is dropped regardless of negation:
// we can't verify either way, and silently flipping a Negated filter to
// "matches" would be the wrong default. This is consistent with the
// pre-fix behavior where memdb books silently failed all
// stripped-field filters.
//
// pebbleLookups is incremented once per actual GetBookByID call so the
// caller can log a per-query count at DEBUG.
func matchesFieldFiltersWithStrippedFallback(
	memBook *database.Book,
	cheap, stripped []FieldFilter,
	fetchFull func(id string) (*database.Book, error),
	pebbleLookups *int64,
	warnOnce func(id string, err error),
) bool {
	if len(cheap) > 0 && !matchesFieldFilters(*memBook, cheap) {
		return false
	}
	if len(stripped) == 0 {
		return true
	}
	full, err := fetchFull(memBook.ID)
	if pebbleLookups != nil {
		*pebbleLookups++
	}
	if err != nil || full == nil {
		if warnOnce != nil {
			warnOnce(memBook.ID, err)
		}
		return false
	}
	return matchesFieldFilters(*full, stripped)
}

// fieldMatchesValue checks whether a book's field value matches the search
// value. For user_rating_* fields the value may be a numeric comparison
// expression such as ">4", "<=3.5", ">=4", "<3", "==5", "!=2"; any other
// value is treated as an equality check.  All other fields use
// case-insensitive substring matching.  Unknown fields return false.
func fieldMatchesValue(book database.Book, field, value string) bool {
	// Numeric rating fields — delegate to numericCompare.
	switch field {
	case "user_rating_overall":
		return numericCompare(book.UserRatingOverall, value)
	case "user_rating_story":
		return numericCompare(book.UserRatingStory, value)
	case "user_rating_performance":
		return numericCompare(book.UserRatingPerformance, value)
	}

	var bookValue string
	switch field {
	case "title":
		bookValue = book.Title
	case "author":
		if book.Author != nil {
			bookValue = book.Author.Name
		}
	case "narrator":
		bookValue = derefStr(book.Narrator)
	case "series":
		if book.Series != nil {
			bookValue = book.Series.Name
		}
	case "genre":
		bookValue = derefStr(book.Genre)
	case "language":
		bookValue = derefStr(book.Language)
	case "publisher":
		bookValue = derefStr(book.Publisher)
	case "edition":
		bookValue = derefStr(book.Edition)
	case "format":
		bookValue = book.Format
	case "codec":
		bookValue = derefStr(book.Codec)
	case "quality":
		bookValue = derefStr(book.Quality)
	case "library_state":
		bookValue = derefStr(book.LibraryState)
	case "description":
		bookValue = derefStr(book.Description)
	case "metadata_review_status", "review":
		bookValue = derefStr(book.MetadataReviewStatus)
	case "has_cover":
		if book.CoverURL != nil && *book.CoverURL != "" {
			bookValue = "yes"
		} else {
			bookValue = "no"
		}
	case "has_written":
		if book.LastWrittenAt != nil {
			bookValue = "yes"
		} else {
			bookValue = "no"
		}
	case "needs_writeback":
		// True when metadata was updated after the last file write (or never written).
		needsWrite := book.MetadataUpdatedAt != nil &&
			(book.LastWrittenAt == nil || book.LastWrittenAt.Before(*book.MetadataUpdatedAt))
		if needsWrite {
			bookValue = "yes"
		} else {
			bookValue = "no"
		}
	case "has_organized":
		if book.LastOrganizedAt != nil {
			bookValue = "yes"
		} else {
			bookValue = "no"
		}
	case "itunes_sync_status":
		bookValue = derefStr(book.ITunesSyncStatus)
	// Aliases for frontend field names
	case "duration_seconds":
		bookValue = fmt.Sprintf("%d", derefInt(book.Duration))
	case "bitrate_kbps":
		bookValue = fmt.Sprintf("%d", derefInt(book.Bitrate))
	case "file_size_bytes":
		bookValue = fmt.Sprintf("%d", derefInt64(book.FileSize))
	case "sample_rate_hz":
		bookValue = fmt.Sprintf("%d", derefInt(book.SampleRate))
	default:
		return false // unknown field
	}
	return strings.Contains(strings.ToLower(bookValue), strings.ToLower(value))
}

// numericCompare evaluates a filter value expression against a nullable
// float64 book field.  The expression may start with one of the operators
// >=, <=, !=, ==, >, <.  A bare number (no operator prefix) is treated as
// == equality.  If the book field is nil (unset) the function always returns
// false.
func numericCompare(fieldVal *float64, expr string) bool {
	if fieldVal == nil {
		return false
	}
	bookNum := *fieldVal

	var op string
	var numStr string
	switch {
	case strings.HasPrefix(expr, ">="):
		op, numStr = ">=", expr[2:]
	case strings.HasPrefix(expr, "<="):
		op, numStr = "<=", expr[2:]
	case strings.HasPrefix(expr, "!="):
		op, numStr = "!=", expr[2:]
	case strings.HasPrefix(expr, "=="):
		op, numStr = "==", expr[2:]
	case strings.HasPrefix(expr, ">"):
		op, numStr = ">", expr[1:]
	case strings.HasPrefix(expr, "<"):
		op, numStr = "<", expr[1:]
	default:
		op, numStr = "==", expr
	}

	threshold, err := strconv.ParseFloat(strings.TrimSpace(numStr), 64)
	if err != nil {
		return false // unparseable — treat as no match
	}

	switch op {
	case ">":
		return bookNum > threshold
	case "<":
		return bookNum < threshold
	case ">=":
		return bookNum >= threshold
	case "<=":
		return bookNum <= threshold
	case "!=":
		return bookNum != threshold
	default: // "=="
		return bookNum == threshold
	}
}

// bookSummaryToBook converts a BookSummary to a Book struct for compatibility.
// Only the fields present in BookSummary are populated; other fields remain zero-valued.
func bookSummaryToBook(summary database.BookSummary) database.Book {
	return database.Book{
		ID:                   summary.ID,
		Title:                summary.Title,
		AuthorID:             summary.AuthorID,
		SeriesID:             summary.SeriesID,
		SeriesSequence:       summary.SeriesSequence,
		FilePath:             summary.FilePath,
		Format:               summary.Format,
		Duration:             summary.Duration,
		Narrator:             summary.Narrator,
		TranscribedTitle:     summary.TranscribedTitle,
		OriginalFilename:     summary.OriginalFilename,
		FileHash:             summary.FileHash,
		FileSize:             summary.FileSize,
		OriginalFileHash:     summary.OriginalFileHash,
		OrganizedFileHash:    summary.OrganizedFileHash,
		LibraryState:         summary.LibraryState,
		QuarantinedAt:        summary.QuarantinedAt,
		QuarantineReason:     summary.QuarantineReason,
		CoverURL:             summary.CoverURL,
		CreatedAt:            summary.CreatedAt,
		UpdatedAt:            summary.UpdatedAt,
		MetadataUpdatedAt:    summary.MetadataUpdatedAt,
		IsPrimaryVersion:     summary.IsPrimaryVersion,
		VersionGroupID:       summary.VersionGroupID,
		MetadataReviewStatus: summary.MetadataReviewStatus,
	}
}

// bookSummariesToBooks converts a slice of BookSummary to Book structs.
func bookSummariesToBooks(summaries []database.BookSummary) []database.Book {
	books := make([]database.Book, len(summaries))
	for i, s := range summaries {
		books[i] = bookSummaryToBook(s)
	}
	return books
}

// summariesPushdown — light-filter path (IsPrimary + title sort).
// Calls the store with the real (limit, offset) so the page is paginated
// by the store. Mock-friendly: when the store lacks
// GetAllBookSummariesFiltered, falls back to GetAllBookSummaries(limit,
// offset) — preserves the pre-pushdown test contract.
//
// Returns didPushdown=true when the store applied the IsPrimary/title filter
// itself and already paginated the result (production memdb path). false means
// the store fell back to the unfiltered GetAllBookSummaries path — the caller
// must keep the in-memory post-filter pass so IsPrimary is still applied. This
// boolean is what lets the caller safely SKIP the post-filter re-pagination:
// re-slicing an already-paginated page by the original offset is what made
// "page 2" (any offset>0) return zero rows.
func (svc *AudiobookService) summariesPushdown(limit, offset int, isPrimary *bool, sortBy string, sortAscending, excludeQuarantined bool) (summaries []database.BookSummary, didPushdown bool, err error) {
	type filteredSummaryStore interface {
		GetAllBookSummariesFiltered(limit, offset int, f database.BookSummaryFilter) ([]database.BookSummary, error)
	}
	filter := database.BookSummaryFilter{
		IsPrimaryVersion:   isPrimary,
		SortBy:             sortBy,
		SortAscending:      sortAscending,
		ExcludeQuarantined: excludeQuarantined,
	}
	if fs, ok := svc.store.(filteredSummaryStore); ok {
		s, e := fs.GetAllBookSummariesFiltered(limit, offset, filter)
		return s, true, e
	}
	if uw, ok := svc.store.(interface{ Unwrap() database.Store }); ok {
		if fs, ok2 := uw.Unwrap().(filteredSummaryStore); ok2 {
			s, e := fs.GetAllBookSummariesFiltered(limit, offset, filter)
			return s, true, e
		}
	}
	s, e := svc.store.GetAllBookSummaries(limit, offset)
	return s, false, e
}

// summariesPushdownFiltered runs the full pushdown — every filter the
// memdb walker can apply in-loop is on the BookSummaryFilter. Returns at
// most `limit` summaries (after `offset` matches are skipped). Walker
// stops on its own; no full-corpus materialization.
//
// Returns didPushdown=true when the store actually applied the filter
// (production path). false means the store fell back to fetching all
// summaries unfiltered — caller must re-apply filters in-memory (mock /
// non-memdb test path). This boolean is the contract that lets the
// caller skip the post-filter pass safely in production.
func (svc *AudiobookService) summariesPushdownFiltered(limit, offset int, filter database.BookSummaryFilter) (summaries []database.BookSummary, didPushdown bool, err error) {
	type filteredSummaryStore interface {
		GetAllBookSummariesFiltered(limit, offset int, f database.BookSummaryFilter) ([]database.BookSummary, error)
	}
	if fs, ok := svc.store.(filteredSummaryStore); ok {
		s, e := fs.GetAllBookSummariesFiltered(limit, offset, filter)
		return s, true, e
	}
	if uw, ok := svc.store.(interface{ Unwrap() database.Store }); ok {
		if fs, ok2 := uw.Unwrap().(filteredSummaryStore); ok2 {
			s, e := fs.GetAllBookSummariesFiltered(limit, offset, filter)
			return s, true, e
		}
	}
	// Fallback: no filtered store. Fetch everything; caller post-filters
	// in-memory. Pass (0, 0) so the caller sees the full set and applies
	// pagination after filtering.
	s, e := svc.store.GetAllBookSummaries(0, 0)
	return s, false, e
}

// countSummariesPushdownFiltered counts matches without allocating
// BookSummary projections. Used by CountAudiobooksFiltered for the
// pagination "total" field. Falls back to materialize-and-count when the
// store lacks the count fast path — and when summariesPushdownFiltered
// itself fell back to unfiltered summaries (mock/non-memdb path), we
// re-apply the filter here so the count is still correct.
func (svc *AudiobookService) countSummariesPushdownFiltered(filter database.BookSummaryFilter) (int, error) {
	type countingFilteredStore interface {
		CountBookSummariesFiltered(f database.BookSummaryFilter) (int, error)
	}
	if cs, ok := svc.store.(countingFilteredStore); ok {
		return cs.CountBookSummariesFiltered(filter)
	}
	if uw, ok := svc.store.(interface{ Unwrap() database.Store }); ok {
		if cs, ok2 := uw.Unwrap().(countingFilteredStore); ok2 {
			return cs.CountBookSummariesFiltered(filter)
		}
	}
	summaries, didPushdown, err := svc.summariesPushdownFiltered(0, 0, filter)
	if err != nil {
		return 0, err
	}
	if didPushdown {
		return len(summaries), nil
	}
	// Store fell back to unfiltered set; apply the filter manually so
	// the count is still correct. Only the fields the BookSummary
	// projects from Book are inspectable here.
	n := 0
	for _, s := range summaries {
		if filter.IsPrimaryVersion != nil {
			eff := s.IsPrimaryVersion == nil || *s.IsPrimaryVersion
			if eff != *filter.IsPrimaryVersion {
				continue
			}
		}
		if filter.LibraryState != "" {
			ls := ""
			if s.LibraryState != nil {
				ls = *s.LibraryState
			}
			if ls != filter.LibraryState {
				continue
			}
		}
		if filter.ReviewStatus != "" {
			rs := ""
			if s.MetadataReviewStatus != nil {
				rs = *s.MetadataReviewStatus
			}
			if !strings.EqualFold(rs, filter.ReviewStatus) {
				continue
			}
		}
		if filter.RestrictToIDs != nil {
			if _, ok := filter.RestrictToIDs[s.ID]; !ok {
				continue
			}
		}
		// Predicate inspects *Book, but the fallback only has summaries.
		// In the rare fallback case with a complex predicate, accept that
		// the count may be approximate — production path uses memdb and
		// doesn't hit this branch.
		n++
	}
	return n, nil
}

// buildBookSummaryFilter translates service-level ListFilters into a
// database.BookSummaryFilter the memdb walker can apply in-loop.
// Returns ok=false when any component CAN'T be pushed down (non-title
// sort, fingerprint filters), and the caller falls back to the old
// fetch-all-then-filter path.
//
// pebbleLookups is non-nil when the predicate may invoke a Pebble
// fallback for memdb-stripped fields (description / version_notes /
// book_sig_v1). The caller can DEBUG-log *pebbleLookups after the
// walker returns to surface the cost of D3 fallback queries.
func (svc *AudiobookService) buildBookSummaryFilter(f ListFilters, sortAsc bool) (database.BookSummaryFilter, bool) {
	bsf, ok, _ := svc.buildBookSummaryFilterWithLookupCount(f, sortAsc)
	return bsf, ok
}

func (svc *AudiobookService) buildBookSummaryFilterWithLookupCount(f ListFilters, sortAsc bool) (database.BookSummaryFilter, bool, *int64) {
	// Non-title sorts: the memdb walker still applies all other filter predicates
	// (IsPrimary, LibraryState, RestrictToIDs, Predicate) and returns the
	// filtered subset; the caller applySorting sorts that smaller slice in
	// memory. Previously this fell back to an unfiltered full-corpus fetch.
	// Fingerprint/coverage: FingerprintStatus and CoveragePercent are
	// denormalized on the Book record, so they can be pushed as in-loop
	// predicates without extra I/O.

	// Tag intersection → ID set. Empty set ⇒ no matches (walker short-circuits).
	var restrictIDs map[string]struct{}
	tagsToMatch := f.Tags
	if len(tagsToMatch) == 0 && f.Tag != "" {
		tagsToMatch = []string{f.Tag}
	}
	for _, tag := range tagsToMatch {
		if tag == "" {
			continue
		}
		ids, err := svc.store.GetBooksByTag(tag)
		if err != nil {
			return database.BookSummaryFilter{}, false, nil
		}
		cur := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			cur[id] = struct{}{}
		}
		if restrictIDs == nil {
			restrictIDs = cur
		} else {
			for id := range restrictIDs {
				if _, ok := cur[id]; !ok {
					delete(restrictIDs, id)
				}
			}
		}
		if len(restrictIDs) == 0 {
			break
		}
	}

	// Pluck exact-match positive FieldFilters for indexed columns; the
	// rest run via the predicate closure on surviving rows only.
	libraryState := f.LibraryState
	reviewStatus := ""
	remainingFF := make([]FieldFilter, 0, len(f.FieldFilters))
	for _, ff := range f.FieldFilters {
		if !ff.Negated && (ff.Field == "review" || ff.Field == "metadata_review_status") && reviewStatus == "" {
			reviewStatus = ff.Value
			continue
		}
		if !ff.Negated && ff.Field == "library_state" && libraryState == "" {
			libraryState = ff.Value
			continue
		}
		remainingFF = append(remainingFF, ff)
	}

	var predicate func(*database.Book) bool
	var pebbleLookupsPtr *int64
	hasPerUser := len(f.PerUserFilters) > 0 && f.UserID != ""
	hasFPFilters := f.FingerprintStatus != "" || f.CoveragePercentMin != nil || f.CoveragePercentMax != nil
	if len(remainingFF) > 0 || hasPerUser || hasFPFilters {
		store := svc.store
		userID := f.UserID
		perUser := f.PerUserFilters
		fpStatus := f.FingerprintStatus
		fpCovMin := f.CoveragePercentMin
		fpCovMax := f.CoveragePercentMax
		cheapFF, strippedFF := splitFieldFilters(remainingFF)
		if len(strippedFF) > 0 {
			slog.Debug("predicate uses stripped-field Pebble fallback",
				"stripped_fields", strippedFieldNames(strippedFF),
				"cheap_filter_count", len(cheapFF))
		}
		// Per-query Pebble-lookup counter + once-per-query warn for
		// nil/err. The walker invokes the predicate row-by-row on the
		// caller's goroutine, so a plain int64 + sync.Once captured in the
		// closure is safe. Pointer is returned so the caller can DEBUG-log
		// the total after the walker returns.
		pebbleLookups := new(int64)
		var warnOnce sync.Once
		warnFn := func(id string, err error) {
			warnOnce.Do(func() {
				slog.Warn("predicate stripped-field Pebble fallback: GetBookByID failed; dropping row",
					"book_id", id, "err", err)
			})
		}
		fetchFull := func(id string) (*database.Book, error) {
			return store.GetBookByID(id)
		}
		predicate = func(b *database.Book) bool {
			if len(remainingFF) > 0 {
				if !matchesFieldFiltersWithStrippedFallback(b, cheapFF, strippedFF, fetchFull, pebbleLookups, warnFn) {
					return false
				}
			}
			if hasPerUser {
				state, _ := store.GetUserBookState(userID, b.ID)
				if !matchesAllPerUserFilters(state, perUser) {
					return false
				}
			}
			// FingerprintStatus and CoveragePercent are denormalized on the
			// Book record so they're available without a BookFile lookup.
			if fpStatus != "" && b.FingerprintStatus != fpStatus {
				return false
			}
			if fpCovMin != nil && b.CoveragePercent < *fpCovMin {
				return false
			}
			if fpCovMax != nil && b.CoveragePercent > *fpCovMax {
				return false
			}
			return true
		}
		pebbleLookupsPtr = pebbleLookups
	}

	bsf := database.BookSummaryFilter{
		IsPrimaryVersion:   f.IsPrimaryVersion,
		ExcludeQuarantined: f.ExcludeQuarantined,
		LibraryState:       libraryState,
		ReviewStatus:       reviewStatus,
		RestrictToIDs:      restrictIDs,
		Predicate:          predicate,
	}
	if f.SortBy == "title" {
		bsf.SortBy = "title"
		bsf.SortAscending = sortAsc
	}
	return bsf, true, pebbleLookupsPtr
}

// splitMultipleNames splits a name string on " & " to support multiple authors/narrators.
func splitMultipleNames(name string) []string {
	parts := strings.Split(name, " & ")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	if len(result) == 0 {
		return []string{name}
	}
	return result
}

// FetchBookFilesForBooks performs the targeted batch fetch via memdb's
// book_id index, with the same unwrap + fallback semantics as the original
// aggregateFileMetadata. Returns a non-nil map (possibly empty) so callers
// can safely range over it without nil checks.
//
// Core-typed (STOREFID): returns BookFileCore, not BookFile. Every current
// consumer of this map (duration/file-size aggregation, fingerprint_status
// badge via fingerprint.FileWithFingerprint) only reads light/retained
// fields, so this is not a behavior change — see
// docs/specs/2026-07-05-store-getter-fidelity-unification.md. A future
// caller that needs a heavy fingerprint-diagnostic field must fetch it via
// svc.store.GetBookFiles(bookID) instead of adding it here.
func (svc *AudiobookService) FetchBookFilesForBooks(books []database.Book) map[string][]database.BookFileCore {
	if svc.store == nil || len(books) == 0 {
		return map[string][]database.BookFileCore{}
	}
	bookIDs := make([]string, 0, len(books))
	for _, b := range books {
		bookIDs = append(bookIDs, b.ID)
	}

	type batchFilesStore interface {
		GetBookFilesForIDsCore(ids []string) (map[string][]database.BookFileCore, error)
	}
	var filesByBookID map[string][]database.BookFileCore
	if fs, ok := svc.store.(batchFilesStore); ok {
		if m, err := fs.GetBookFilesForIDsCore(bookIDs); err == nil {
			filesByBookID = m
		}
	} else if uw, ok := svc.store.(interface{ Unwrap() database.Store }); ok {
		if fs, ok2 := uw.Unwrap().(batchFilesStore); ok2 {
			if m, err := fs.GetBookFilesForIDsCore(bookIDs); err == nil {
				filesByBookID = m
			}
		}
	}
	if filesByBookID == nil {
		// Store doesn't expose the batched method — fall back to per-book
		// fetch. Slow (N+1) but bounded by len(books) (typically 20).
		filesByBookID = make(map[string][]database.BookFileCore, len(books))
		for _, id := range bookIDs {
			files, err := svc.store.GetBookFiles(id)
			if err != nil {
				slog.Warn("FetchBookFilesForBooks: GetBookFiles failed", "book_id", id, "err", err)
				continue
			}
			cores := make([]database.BookFileCore, len(files))
			for i, f := range files {
				cores[i] = f.Core()
			}
			filesByBookID[id] = cores
		}
	}
	return filesByBookID
}

// aggregateFileMetadataWithFiles applies duration/file-size aggregation
// using a pre-fetched files map. Skips the GetBookFilesForIDsCore call,
// allowing callers that already have the map to reuse it.
func (svc *AudiobookService) aggregateFileMetadataWithFiles(books []database.Book, filesByBookID map[string][]database.BookFileCore) {
	if len(books) == 0 {
		return
	}
	if filesByBookID == nil {
		filesByBookID = map[string][]database.BookFileCore{}
	}

	bookIDMap := make(map[string]int, len(books))
	for i, b := range books {
		bookIDMap[b.ID] = i
	}

	aggregates := make(map[string]*struct {
		totalDuration int
		totalSize     int64
	}, len(books))
	for _, b := range books {
		aggregates[b.ID] = &struct {
			totalDuration int
			totalSize     int64
		}{}
	}

	for bookID, files := range filesByBookID {
		agg, ok := aggregates[bookID]
		if !ok {
			continue
		}
		for _, f := range files {
			if f.Missing {
				continue
			}
			agg.totalDuration += f.Duration / 1000
			agg.totalSize += f.FileSize
		}
		if idx, ok := bookIDMap[bookID]; ok {
			books[idx].Duration = &agg.totalDuration
			books[idx].FileSize = &agg.totalSize
		}
	}
}
