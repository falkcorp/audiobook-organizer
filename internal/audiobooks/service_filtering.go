// file: internal/audiobooks/service_filtering.go
// version: 1.14.0
// guid: b4e8c3d2-e5f6-7a80-9b0c-1d2e3f4a5b6c
// last-edited: 2026-08-25

package audiobooks

import (
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/util"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// applySorting sorts a slice of books in-place based on the filter's SortBy
// and SortOrder.
//
// The ordering rule itself lives in database.SortBooks, which is also what the
// memdb sorted indexes are built from. It used to live here as a private
// sortFieldMap kept in step with the indexes by a comment; it was not in step
// (unknown values sorted first here and last there). Delegating means the
// pushdown and materialise paths cannot disagree, because there is only one
// rule to disagree with.
func applySorting(books []database.Book, f ListFilters) {
	if f.SortBy == "" {
		return
	}
	database.SortBooks(books, f.SortBy, f.SortOrder != "desc")
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

// FirstEmptyFilterValue returns the field name of the first filter carrying an
// empty value, and whether one was found.
//
// WHY this exists: the comparison at the bottom of fieldMatchesValue is
//
//	strings.Contains(strings.ToLower(bookValue), strings.ToLower(value))
//
// and strings.Contains(anything, "") is ALWAYS TRUE in Go. So a filter with an
// empty value does not narrow anything — it silently matches every book.
//
// Measured on production 2026-08-13, and the filter is otherwise working:
//
//	title=""       -> 63,870 books (the entire library)
//	title="zzqqxx" ->      0 books
//	title="Skills" ->     25 books
//
// That is not merely a confusing read. FieldFilters also reach
// Server.resolveFilterToBookIDs, which resolves a FilterSpec into concrete book
// IDs for BACKGROUND OPERATIONS at a limit of 100,000 — so an empty value there
// silently targets the whole library. That is the same failure shape as the
// base64 op-params defect (#2309), where organize defaulted to every book.
//
// Callers use this to reject the input loudly. matchesFieldFilters below also
// fails closed as a last line of defence, so a filter that somehow evades
// validation matches nothing rather than everything.
func FirstEmptyFilterValue(filters []FieldFilter) (string, bool) {
	for _, f := range filters {
		if f.Value == "" {
			return f.Field, true
		}
	}
	return "", false
}

// matchesFieldFilters returns true if a book matches all the given field filters.
// All filters are ANDed: every filter must match for the book to be included.
func matchesFieldFilters(book database.Book, filters []FieldFilter) bool {
	for _, f := range filters {
		// Fail CLOSED on an empty value. Falling through would reach
		// strings.Contains(x, "") == true and match every book — see
		// FirstEmptyFilterValue. Matching nothing is visibly wrong and harmless;
		// matching everything is invisibly wrong and, on the background-op path,
		// destructive. Negated is deliberately not consulted: neither `f == ""`
		// nor `f != ""` is a constraint anyone can have meant.
		//
		// No in-repo code builds a filter with an empty value (the list warmer's
		// constructions all carry real values), so this only ever fires on input
		// that the boundary validation should already have rejected.
		if f.Value == "" {
			return false
		}
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
// authorNames/seriesNames are optional authorID→name / seriesID→name maps
// (see buildAuthorSeriesNameMaps). memBook.Author / memBook.Series are
// "Related objects (populated via joins, not stored in DB)" — see
// database.Book's doc comment — so neither the memdb-resident copy NOR the
// raw-JSON Pebble fetchFull result ever carries them; only AuthorID/SeriesID
// survive. Without these maps, an "author"/"series" FieldFilter always
// compared against "" and rejected every row (TODO 16b). Both maps are nil
// when the filter set doesn't reference author/series, so callers pay
// nothing extra for the common case.
//
// pebbleLookups is incremented once per actual GetBookByID call so the
// caller can log a per-query count at DEBUG.
func matchesFieldFiltersWithStrippedFallback(
	memBook *database.Book,
	cheap, stripped []FieldFilter,
	fetchFull func(id string) (*database.Book, error),
	pebbleLookups *int64,
	warnOnce func(id string, err error),
	authorNames, seriesNames map[int]string,
) bool {
	if len(cheap) > 0 {
		cheapBook := memBook
		if authorNames != nil || seriesNames != nil {
			hydrated := hydrateAuthorSeriesNames(*memBook, authorNames, seriesNames)
			cheapBook = &hydrated
		}
		if !matchesFieldFilters(*cheapBook, cheap) {
			return false
		}
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

// hydrateAuthorSeriesNames returns a copy of book with Author/Series
// populated from the given id→name maps when the book carries an
// AuthorID/SeriesID but no already-resolved Author/Series struct. Safe to
// call with nil maps (no-op) or when the book's Author/Series is already
// set (left untouched).
func hydrateAuthorSeriesNames(book database.Book, authorNames, seriesNames map[int]string) database.Book {
	if book.Author == nil && book.AuthorID != nil && authorNames != nil {
		if name, ok := authorNames[*book.AuthorID]; ok {
			book.Author = &database.Author{ID: *book.AuthorID, Name: name}
		}
	}
	if book.Series == nil && book.SeriesID != nil && seriesNames != nil {
		if name, ok := seriesNames[*book.SeriesID]; ok {
			book.Series = &database.Series{ID: *book.SeriesID, Name: name}
		}
	}
	return book
}

// buildAuthorSeriesNameMaps returns authorID→name / seriesID→name maps when
// filters contains an "author" / "series" FieldFilter, or nil maps
// otherwise. Authors/Series are small, fully in-memory collections (unlike
// Books), so fetching all of them once per query — the same GetAllAuthors /
// GetAllSeries accessor author_series.go's ListSeriesWithCounts already uses
// for its author-name join — is cheap and avoids a per-book store call
// inside the filter loop (see CLAUDE.md's concurrency/per-item-DB-call
// guidance).
func (svc *AudiobookService) buildAuthorSeriesNameMaps(filters []FieldFilter) (authorNames, seriesNames map[int]string) {
	needAuthor, needSeries := false, false
	for _, f := range filters {
		switch f.Field {
		case "author":
			needAuthor = true
		case "series":
			needSeries = true
		}
	}
	if needAuthor {
		if authors, err := svc.store.GetAllAuthors(); err == nil {
			authorNames = make(map[int]string, len(authors))
			for _, a := range authors {
				authorNames[a.ID] = a.Name
			}
		}
	}
	if needSeries {
		if series, err := svc.store.GetAllSeries(); err == nil {
			seriesNames = make(map[int]string, len(series))
			for _, s := range series {
				seriesNames[s.ID] = s.Name
			}
		}
	}
	return authorNames, seriesNames
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

	bookValue, known := bookFieldValue(book, field)
	if !known {
		return false
	}
	return strings.Contains(strings.ToLower(bookValue), strings.ToLower(value))
}

// bookFieldValue renders a book field as the string the substring matcher
// compares against, and reports whether the field name is known at all.
//
// Split out of fieldMatchesValue so that "which fields exist" has exactly ONE
// definition. FieldIsKnown answers that question by asking this function, which
// is the only way a validator and a matcher can be guaranteed to agree — a
// second hand-maintained list of valid names would drift from this switch the
// moment either changed, and drift between two copies of one rule is the defect
// this whole area keeps producing.
//
// An unset numeric renders as "0" rather than "", which is how the pre-existing
// duration_seconds / bitrate_kbps / file_size_bytes / sample_rate_hz cases have
// always behaved; the consequence is that e.g. channels:0 matches every book
// with no channel count recorded. Kept uniform across old and new numeric fields
// deliberately — an alias that behaved differently from the name it aliases
// would be worse than the footgun.
func bookFieldValue(book database.Book, field string) (string, bool) {
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

	// Media technicals. Each accepts BOTH spellings: the unit-suffixed name
	// this switch has always used, and the bare name the search bar sends.
	//
	// The four suffixed cases were added under a comment reading "Aliases for
	// frontend field names", which had it exactly backwards — those are the
	// backend spellings, and web/src/utils/searchParser.ts offers the user
	// `duration:`, `bitrate:`, `file_size:` and `sample_rate:`. Nothing checked
	// the two lists against each other, so all four typed queries fell through
	// to the unknown-field default and answered "no books found". Measured on
	// production 2026-08-14: duration:1 returned 0 while duration_seconds:1
	// returned 25,090 — the same rows, under a name the UI never sends.
	case "duration", "duration_seconds":
		bookValue = fmt.Sprintf("%d", derefInt(book.Duration))
	case "bitrate", "bitrate_kbps":
		bookValue = fmt.Sprintf("%d", derefInt(book.Bitrate))
	case "file_size", "file_size_bytes":
		bookValue = fmt.Sprintf("%d", derefInt64(book.FileSize))
	case "sample_rate", "sample_rate_hz":
		bookValue = fmt.Sprintf("%d", derefInt(book.SampleRate))
	case "channels":
		bookValue = fmt.Sprintf("%d", derefInt(book.Channels))
	case "bit_depth":
		bookValue = fmt.Sprintf("%d", derefInt(book.BitDepth))

	// Identity and bibliographic fields the search bar has always offered and
	// this switch never implemented.
	case "series_number":
		bookValue = fmt.Sprintf("%d", derefInt(book.SeriesSequence))
	case "isbn10":
		bookValue = derefStr(book.ISBN10)
	case "isbn13":
		bookValue = derefStr(book.ISBN13)
	case "version_group_id":
		bookValue = derefStr(book.VersionGroupID)
	case "work_id":
		bookValue = derefStr(book.WorkID)

	// "year" is deliberately not one column. A book carries a print year and an
	// audiobook release year, they routinely differ, and someone typing
	// year:2019 means "released in 2019" by either reckoning rather than
	// nominating one of the two. Both are rendered and substring-matched; the
	// space keeps 1997 and 2007 from matching a typed "9720".
	case "year":
		bookValue = strings.TrimSpace(
			fmt.Sprintf("%s %s", intPtrToSearchString(book.PrintYear), intPtrToSearchString(book.AudiobookReleaseYear)))

	// Timestamps render RFC3339 so a date prefix works as a substring:
	// created_at:2026-08 matches everything created that month. Unset renders
	// empty rather than the zero time, so a filter on an unset timestamp
	// matches nothing instead of matching "0001-01-01".
	case "created_at":
		bookValue = timePtrToSearchString(book.CreatedAt)
	case "updated_at":
		bookValue = timePtrToSearchString(book.UpdatedAt)

	// The trash bit, as "true"/"false". Note this predicate alone is not enough
	// to make marked_for_deletion:true return anything — the rows have to reach
	// this filter first, which is what the pushdown in
	// buildBookSummaryFilterWithLookupCount arranges. See that function.
	case "marked_for_deletion":
		bookValue = strconv.FormatBool(book.IsSoftDeleted())

	default:
		return "", false // unknown field
	}
	return bookValue, true
}

// intPtrToSearchString renders an optional int for substring matching, empty
// when unset so an absent value matches nothing rather than matching "0".
func intPtrToSearchString(v *int) string {
	if v == nil {
		return ""
	}
	return strconv.Itoa(*v)
}

// timePtrToSearchString renders an optional timestamp as RFC3339, empty when
// unset. See the created_at case for why empty rather than the zero time.
func timePtrToSearchString(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

// FieldIsKnown reports whether field names something the library list can
// actually filter on: a book column, a per-user state field, or a rating.
//
// It answers by consulting bookFieldValue rather than by carrying its own list
// of names, so the validator and the matcher cannot disagree. The zero Book is
// safe to probe because bookFieldValue's "known" result depends only on the
// field name, never on the row's contents.
func FieldIsKnown(field string) bool {
	switch field {
	case "user_rating_overall", "user_rating_story", "user_rating_performance":
		return true
	}
	if IsPerUserField(field) {
		return true
	}
	_, known := bookFieldValue(database.Book{}, field)
	return known
}

// FirstUnknownFilterField returns the first filter naming a field the list
// cannot filter on.
//
// Callers reject the request rather than running it. An unknown field is not
// ignored by the matcher — it falls to bookFieldValue's default and matches
// NOTHING, so the endpoint answers count:0. That is the most misleading answer
// available: "0" is indistinguishable from a truthful "no books match", so a
// typo, a renamed field, or a UI sending a name the backend never implemented
// all read as a fact about the library. Measured on production 2026-08-14:
// filtering on the nonsense field zzz_not_a_real_field returned exactly the
// same count:0 as filtering on marked_for_deletion, for which 3,953 books
// qualified.
//
// Sorted for determinism, on the same reasoning as firstBareFilterFieldParam:
// an error that names a different field on each retry is its own small cruelty.
func FirstUnknownFilterField(filters []FieldFilter) (string, bool) {
	unknown := make([]string, 0, 2)
	for _, f := range filters {
		if !FieldIsKnown(f.Field) {
			unknown = append(unknown, f.Field)
		}
	}
	if len(unknown) == 0 {
		return "", false
	}
	sort.Strings(unknown)
	return unknown[0], true
}

// KnownFilterFields returns every field name the list accepts, sorted. Used to
// name the alternatives in the rejection message, and by the conformance test
// that holds this set against the search bar's own list.
func KnownFilterFields() []string {
	out := make([]string, 0, len(allFilterFieldNames))
	out = append(out, allFilterFieldNames...)
	sort.Strings(out)
	return out
}

// allFilterFieldNames is the enumeration behind KnownFilterFields. It cannot be
// derived from bookFieldValue's switch — Go offers no way to enumerate case
// labels — so it is the one place a second list is unavoidable. That is exactly
// why TestFilterFieldNames_MatchTheMatcher exists: it asserts every name here is
// accepted by FieldIsKnown, so a name that is listed but not implemented fails
// the build rather than reaching a user as an empty result set.
var allFilterFieldNames = []string{
	"title", "author", "narrator", "series", "series_number", "genre",
	"language", "publisher", "edition", "description", "format", "codec",
	"quality", "library_state", "metadata_review_status", "review",
	"has_cover", "has_written", "needs_writeback", "has_organized",
	"itunes_sync_status", "duration", "duration_seconds", "bitrate",
	"bitrate_kbps", "file_size", "file_size_bytes", "sample_rate",
	"sample_rate_hz", "channels", "bit_depth", "isbn10", "isbn13", "work_id",
	"version_group_id",
	"year", "created_at", "updated_at", "marked_for_deletion",
	"read_status", "progress_pct", "last_played",
	"user_rating_overall", "user_rating_story", "user_rating_performance",
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

// normalizeEffectivePrimaryVersion rewrites every book's nullable
// IsPrimaryVersion to the concrete boolean that this request's filters just
// used, so the value a client reads out of the JSON body agrees with the value
// the server filtered on.
//
// Without it the listing response is self-contradicting. database.Book tags the
// field `json:"is_primary_version,omitempty"`, so a nil *bool omits the key
// ENTIRELY — not `null`. A client sees no field (`undefined`, falsy in the
// web UI) on a row that ?is_primary_version=true had just selected, because
// every filter in the pipeline resolves nil to primary while the serializer
// passed the raw pointer straight through. Measured on a three-row Pebble
// fixture before this change: the nil-flagged book came back from
// is_primary_version=true with no is_primary_version key at all, alongside an
// explicit-true row that carried `"is_primary_version":true`. In production
// that is 5,731 ungrouped books whose serialized field disagrees with the
// filter that returned them.
//
// Scope is deliberately the listing response only. The store still returns the
// raw tri-state, and GET /audiobooks/:id is untouched: normalizing at the
// storage layer would light BookDetailHeader's "Primary Version" chip on those
// same 5,731 ungrouped books, a UI change nothing asked for. Making the flag
// explicit ON DISK is a separate, deliberate operation — the
// maintenance.normalize-primary-flags job from PR #2449.
//
// Mutates in place. It assigns a fresh *bool per element rather than writing
// through the existing pointer, which may be shared with a store-internal
// object.
func normalizeEffectivePrimaryVersion(books []database.Book) {
	for i := range books {
		eff := database.EffectiveIsPrimaryVersion(books[i].IsPrimaryVersion)
		books[i].IsPrimaryVersion = &eff
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
	filter := database.BookSummaryFilter{
		IsPrimaryVersion:   isPrimary,
		SortBy:             sortBy,
		SortAscending:      sortAscending,
		ExcludeQuarantined: excludeQuarantined,
	}
	if fs, ok := database.AsCapability[filteredSummaryStore](svc.store); ok {
		s, e := fs.GetAllBookSummariesFiltered(limit, offset, filter)
		return s, true, e
	}
	s, e := svc.store.GetAllBookSummaries(limit, offset)
	return s, false, e
}

// filteredSummaryStore is the fast-path contract for pushing a
// BookSummaryFilter down into the store.
//
// The marker method is the point. This interface used to be declared inline
// as GetAllBookSummariesFiltered alone, and the callers below read a
// successful type assertion as proof the filter had been applied — then
// skipped their own post-filter pass on the strength of it. But a type
// assertion answers "does this type have the method?", never "did the method
// honor the filter?". PebbleStore satisfied it while its memdb-unavailable
// fallback applied only 2 of 8 predicates, so a filtered library query issued
// during the ~2 minute startup warmup returned very nearly the whole library
// and the post-filter pass that would have caught it was skipped by design.
//
// Requiring a second, purpose-built method makes the default fail-safe: a
// store that has not deliberately declared full conformance simply does not
// satisfy this interface, so didPushdown is false and the caller post-filters
// in memory — slower, but correct. Partial conformance now requires an
// explicit false claim rather than arriving by omission.
//
// See PebbleStore.HonorsEveryBookSummaryFilter for what implementing it
// commits to.
type filteredSummaryStore interface {
	GetAllBookSummariesFiltered(limit, offset int, f database.BookSummaryFilter) ([]database.BookSummary, error)
	HonorsEveryBookSummaryFilter()
}

// countingFilteredStore is the count-side half of filteredSummaryStore, and
// carries the same conformance requirement for the same reason.
//
// The count path is the less forgiving of the two: countSummariesPushdownFiltered
// returns this store's number directly with no post-filter correction
// available, so a partial implementer here produces a wrong pagination total
// with nothing downstream to catch it. That is how a filtered query came back
// reporting a total of 63,870.
type countingFilteredStore interface {
	CountBookSummariesFiltered(f database.BookSummaryFilter) (int, error)
	HonorsEveryBookSummaryFilter()
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
	if fs, ok := database.AsCapability[filteredSummaryStore](svc.store); ok {
		s, e := fs.GetAllBookSummariesFiltered(limit, offset, filter)
		return s, true, e
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
	if cs, ok := database.AsCapability[countingFilteredStore](svc.store); ok {
		return cs.CountBookSummariesFiltered(filter)
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
			eff := database.EffectiveIsPrimaryVersion(s.IsPrimaryVersion)
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
	//
	// Seeded from f.RestrictToIDs so a caller-supplied ID set (the
	// has_file_errors / quick-query fast paths) is ANDed with the tags rather
	// than replacing them or being replaced by them. The loop below mutates
	// this map with delete(), so copy rather than aliasing the caller's set.
	// A non-nil empty seed stays non-nil empty, which is the "no book is
	// eligible" signal the walkers short-circuit on.
	var restrictIDs map[string]struct{}
	if f.RestrictToIDs != nil {
		restrictIDs = make(map[string]struct{}, len(f.RestrictToIDs))
		for id := range f.RestrictToIDs {
			restrictIDs[id] = struct{}{}
		}
	}
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
	var markedForDeletion *bool
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
		// marked_for_deletion MUST be plucked rather than left to the
		// predicate. The store excludes soft-deleted rows before any predicate
		// runs, so a post-filter asking for them is asking an already-live-only
		// set and can only ever answer zero — which is how the endpoint came to
		// report count:0 for a library holding 3,953 trashed books. Setting the
		// store filter's tri-state is what puts those rows in front of the
		// filter in the first place.
		if !ff.Negated && ff.Field == "marked_for_deletion" && markedForDeletion == nil {
			if want, err := strconv.ParseBool(ff.Value); err == nil {
				markedForDeletion = &want
				continue
			}
			// Unparseable value: leave it in remainingFF so the ordinary
			// matcher rejects the row, rather than silently widening the scan
			// to include the trash on the strength of a malformed filter.
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
		authorNames, seriesNames := svc.buildAuthorSeriesNameMaps(remainingFF)
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
				if !matchesFieldFiltersWithStrippedFallback(b, cheapFF, strippedFF, fetchFull, pebbleLookups, warnFn, authorNames, seriesNames) {
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
		MarkedForDeletion:  markedForDeletion,
		Predicate:          predicate,
	}
	// Hand the store the sort whenever it can honor it, not just for title.
	// The store chooses the page from the ORDERED match set; we cannot, because
	// what comes back is []BookSummary and BookSummary drops most sortable
	// fields (author, series, year, genre, language, publisher, codec, quality,
	// edition, bitrate, sample_rate). Sorting those here compares "" to "" on
	// every row and silently returns the input order — measured at 13 of the 23
	// keys in bookSortComparators before this changed. See
	// docs/audits/2026-08-25-author-series-sort-degenerate.md.
	//
	// Gating on "title" alone meant the store was never even told which sort
	// was requested, so its own ordered path could not run.
	if database.CanSortBooksBy(f.SortBy) {
		bsf.SortBy = f.SortBy
		bsf.SortAscending = sortAsc
	}
	return bsf, true, pebbleLookupsPtr
}

// splitMultipleNames delegates to util.SplitCreditNames, the single source of
// truth for author/narrator credit splitting. This used to be a local
// strings.Split(name, " & "), duplicated verbatim in
// internal/server/handlers/operations/handler.go; both copies are gone.
func splitMultipleNames(name string) []string {
	return util.SplitCreditNames(name)
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
	if fs, ok := database.AsCapability[batchFilesStore](svc.store); ok {
		if m, err := fs.GetBookFilesForIDsCore(bookIDs); err == nil {
			filesByBookID = m
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
			// 🔴 NEVER DIVIDE UNCONDITIONALLY. This was `f.Duration / 1000` on the
			// assumption that BookFile.Duration is milliseconds. It is SECONDS by
			// convention (see database/duration_sanity.go) — only ~2% of rows are
			// milliseconds, from the iTunes importer.
			//
			// So this divided correct values by 1000, and because it truncated per
			// row BEFORE summing, every file shorter than 1000 s contributed
			// exactly 0. Hyperion listed 20 s against a stored 174,658 s, and
			// 25,938 of 44,886 books showed an implausibly small duration — every
			// one of them a book that HAS files, while the "plausible" ones were
			// books with none, which skip this loop entirely.
			//
			// NormalizeDurationSec divides only when the file's implied bitrate
			// proves the value is milliseconds, so a correct row passes through
			// untouched and a genuine ms row is still repaired.
			agg.totalDuration += database.NormalizeDurationSec(f.FileSize, f.Duration)
			agg.totalSize += f.FileSize
		}
		if idx, ok := bookIDMap[bookID]; ok {
			books[idx].Duration = &agg.totalDuration
			books[idx].FileSize = &agg.totalSize
		}
	}
}
