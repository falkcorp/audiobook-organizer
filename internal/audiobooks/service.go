// file: internal/audiobooks/service.go
// version: 1.31.0
// guid: 5e6f7a8b-9c0d-1e2f-3a4b-5c6d7e8f9a0b
// last-edited: 2026-06-17

package audiobooks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// audiobookStore is the narrow slice of database.Store that
// AudiobookService actually needs — both for its own method calls
// and for the helpers it forwards the store to (asExternalIDStore,
// NewMetadataStateService). Declared as a named composite so the
// service's dependency surface is inspectable in one place.
type audiobookStore interface {
	database.BookStore
	database.AuthorStore
	database.SeriesStore
	database.NarratorStore
	database.BookFileStore
	database.HashBlocklistStore
	database.TagStore
	// Transitively required — audiobook_service forwards svc.store to
	// NewMetadataStateService for change history tracking and to
	// asExternalIDStore for tombstone cleanup.
	database.MetadataStore
	database.UserPreferenceStore
	// Per-user filter pass on the listing endpoint reads UserBookState
	// to evaluate read_status / progress_pct / last_played.
	database.UserPositionStore
	// Needed by isProtectedPath to compare absolute paths against
	// configured import roots (SERVER-GLOBAL-STORE-AUDIT phase 6).
	// Single method inline rather than database.ImportPathStore so
	// the narrower audiobookUpdateStore adapter doesn't have to
	// implement the full ImportPath CRUD surface.
	GetAllImportPaths() ([]database.ImportPath, error)
}

// ITunesEnqueuer is the narrow surface AudiobookService uses to push
// iTunes mutations from delete/purge paths. Satisfied by
// *itunesservice.WriteBackBatcher. Optional — nil-safe.
type ITunesEnqueuer interface {
	EnqueueRemove(pid string)
}

// AudiobookService handles all audiobook business logic
type AudiobookService struct {
	store           audiobookStore
	bookCache       *cache.Cache[*database.Book]
	listCache       *cache.Cache[[]database.Book]
	activityService *activity.Service
	// searchIndex is the Bleve index for full-text search. When nil
	// the service falls back to the legacy Store.SearchBooks path.
	// Wired in by the Server after Bleve opens in Start(), which is
	// after NewAudiobookService runs in NewServer.
	searchIndex *search.BleveIndex
	// itunesEnqueuer is wired by the Server after the WriteBackBatcher
	// is constructed. Nil-safe — when nil the delete/purge paths skip
	// the iTunes side-effect (e.g. tests, iTunes disabled in config).
	itunesEnqueuer ITunesEnqueuer
}

// SetActivityService wires the activity service for snapshot fallback in GetAudiobookTags.
func (svc *AudiobookService) SetActivityService(as *activity.Service) {
	svc.activityService = as
}

// SetSearchIndex wires the Bleve index for Bleve-backed search.
// Calling with nil reverts to the Store.SearchBooks fallback.
func (svc *AudiobookService) SetSearchIndex(idx *search.BleveIndex) {
	svc.searchIndex = idx
}

// SetITunesEnqueuer wires (or re-wires) the iTunes write-back batcher.
// Nil disables iTunes side-effects in delete/purge paths.
func (svc *AudiobookService) SetITunesEnqueuer(e ITunesEnqueuer) {
	svc.itunesEnqueuer = e
}

// NewAudiobookService creates a new AudiobookService instance
func NewAudiobookService(store audiobookStore) *AudiobookService {
	return &AudiobookService{
		store:     store,
		// MAYDEPLOY-I4: cap entry count via LRU so 24h TTL doesn't allow
		// unbounded growth of full Book payloads.
		bookCache: cache.NewWithLimit[*database.Book]("book", 24*time.Hour, 5000),
		listCache: cache.NewWithLimit[[]database.Book]("audiobook_list", 24*time.Hour, 500),
	}
}

// InvalidateBookCaches clears all book-related caches. Called after any
// mutation (create, update, delete) to keep reads consistent.
//
// Order matters: invalidate bookCache first, then listCache. If we did it the
// other way around, a concurrent reader could miss the list cache (just
// cleared), re-fetch a fresh list from the DB, but still hit stale individual
// book entries that haven't been invalidated yet. By clearing individual books
// first, any concurrent reader that re-fetches the list will also get fresh
// individual books on subsequent lookups.
//
// When config.CacheInvalidateOnBookUpdate is false (the default), only the
// per-book cache is cleared; the list/facets caches are left warm so metadata
// fetches and write-back operations do not reset library page performance.
func (svc *AudiobookService) InvalidateBookCaches() {
	svc.bookCache.InvalidateAll()
	if config.AppConfig.CacheInvalidateOnBookUpdate {
		svc.listCache.InvalidateAll()
	}
}

// AudiobooksListResponse represents the response for listing audiobooks
type AudiobooksListResponse struct {
	Items  []AudiobookDetail `json:"items"`
	Count  int               `json:"count"`
	Limit  int               `json:"limit"`
	Offset int               `json:"offset"`
}

// AudiobookDetail extends database.Book with author and series names for response
type AudiobookDetail struct {
	*database.Book
	AuthorName *string `json:"author_name,omitempty"`
	SeriesName *string `json:"series_name,omitempty"`
}

// DuplicatesResult represents the result of duplicate detection
type DuplicatesResult struct {
	Groups         [][]database.Book `json:"groups"`
	GroupCount     int               `json:"group_count"`
	DuplicateCount int               `json:"duplicate_count"`
}

// SoftDeletedBooksResponse represents the response for listing soft-deleted books
type SoftDeletedBooksResponse struct {
	Items  []database.Book `json:"items"`
	Count  int             `json:"count"`
	Total  int             `json:"total"`
	Limit  int             `json:"limit"`
	Offset int             `json:"offset"`
}

// PurgeResult represents the result of purging soft-deleted books
type PurgeResult struct {
	Attempted    int      `json:"attempted"`
	Purged       int      `json:"purged"`
	FilesDeleted int      `json:"files_deleted"`
	Errors       []string `json:"errors"`
}

// AudiobookUpdate represents a partial update to an audiobook
type AudiobookUpdate struct {
	*database.Book
	AuthorName      *string                    `json:"author_name,omitempty"`
	SeriesName      *string                    `json:"series_name,omitempty"`
	Overrides       map[string]OverridePayload `json:"overrides,omitempty"`
	UnlockOverrides []string                   `json:"unlock_overrides,omitempty"`
}

// OverridePayload represents metadata override information
type OverridePayload struct {
	Value        json.RawMessage `json:"value"`
	Locked       *bool           `json:"locked,omitempty"`
	FetchedValue json.RawMessage `json:"fetched_value,omitempty"`
	Clear        bool            `json:"clear,omitempty"`
}

// FieldFilter represents a field-specific search filter from advanced search.
type FieldFilter struct {
	Field   string `json:"field"`
	Value   string `json:"value"`
	Negated bool   `json:"negated"`
}

// ListFilters holds optional filters for listing audiobooks.
type ListFilters struct {
	IsPrimaryVersion *bool
	LibraryState     string
	Tag              string
	Tags             []string
	SortBy           string        // column sort key
	SortOrder        string        // "asc" or "desc"
	FieldFilters     []FieldFilter // advanced field-specific filters (book-global)
	PerUserFilters   []FieldFilter // per-user filters (read_status, progress_pct, last_played)
	UserID           string        // caller's user ID; required for PerUserFilters
	// Fingerprinting filters
	FingerprintStatus  string // "none", "partial", "complete", or "" for any
	CoveragePercentMin *int   // minimum coverage percentage (inclusive)
	CoveragePercentMax *int   // maximum coverage percentage (inclusive)
}

// PerUserFieldNames is the set of search fields whose values come from
// per-user state (database.UserBookState) rather than book columns.
// Handlers use this to split incoming FieldFilters between the global
// pass (matchesFieldFilters) and the per-user pass.
var PerUserFieldNames = map[string]struct{}{
	"read_status":  {},
	"progress_pct": {},
	"last_played":  {},
}

// IsPerUserField reports whether f targets per-user state.
func IsPerUserField(field string) bool {
	_, ok := PerUserFieldNames[field]
	return ok
}

// derefStr safely dereferences a *string, returning "" for nil.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// derefInt safely dereferences a *int, returning 0 for nil.
func derefInt(i *int) int {
	if i == nil {
		return 0
	}
	return *i
}

// derefInt64 safely dereferences a *int64, returning 0 for nil.
func derefInt64(i *int64) int64 {
	if i == nil {
		return 0
	}
	return *i
}

// cmpTime compares two *time.Time values, treating nil as zero time.
func cmpTime(a, b *time.Time) int {
	ta := time.Time{}
	tb := time.Time{}
	if a != nil {
		ta = *a
	}
	if b != nil {
		tb = *b
	}
	if ta.Before(tb) {
		return -1
	}
	if ta.After(tb) {
		return 1
	}
	return 0
}

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

// strippedMemdbFields enumerates Book fields that stripBookForMemdb()
// clears from memdb-resident copies. Predicate filters on these fields
// silently miss against memdb Books (which is the default code path in
// production) — callers must fetch the full Book from Pebble via
// GetBookByID and re-test those filters. See internal/database/memdb_strip.go.
var strippedMemdbFields = map[string]bool{
	"description":   true,
	"version_notes": true,
	"book_sig_v1":   true,
}

// splitFieldFilters partitions a FieldFilter list into ones that can be
// evaluated against a memdb-stripped *Book (cheap) and ones that require
// the full Pebble-resident Book (stripped). Order within each partition
// is preserved.
func splitFieldFilters(filters []FieldFilter) (cheap, stripped []FieldFilter) {
	for _, f := range filters {
		if strippedMemdbFields[f.Field] {
			stripped = append(stripped, f)
		} else {
			cheap = append(cheap, f)
		}
	}
	return cheap, stripped
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
func (svc *AudiobookService) summariesPushdown(limit, offset int, isPrimary *bool, sortBy string, sortAscending bool) ([]database.BookSummary, error) {
	type filteredSummaryStore interface {
		GetAllBookSummariesFiltered(limit, offset int, f database.BookSummaryFilter) ([]database.BookSummary, error)
	}
	filter := database.BookSummaryFilter{
		IsPrimaryVersion: isPrimary,
		SortBy:           sortBy,
		SortAscending:    sortAscending,
	}
	if fs, ok := svc.store.(filteredSummaryStore); ok {
		return fs.GetAllBookSummariesFiltered(limit, offset, filter)
	}
	if uw, ok := svc.store.(interface{ Unwrap() database.Store }); ok {
		if fs, ok2 := uw.Unwrap().(filteredSummaryStore); ok2 {
			return fs.GetAllBookSummariesFiltered(limit, offset, filter)
		}
	}
	return svc.store.GetAllBookSummaries(limit, offset)
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
	if f.SortBy != "" && f.SortBy != "title" {
		return database.BookSummaryFilter{}, false, nil
	}
	if f.FingerprintStatus != "" || f.CoveragePercentMin != nil || f.CoveragePercentMax != nil {
		return database.BookSummaryFilter{}, false, nil
	}

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
	if len(remainingFF) > 0 || hasPerUser {
		store := svc.store
		userID := f.UserID
		perUser := f.PerUserFilters
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
			return true
		}
		pebbleLookupsPtr = pebbleLookups
	}

	bsf := database.BookSummaryFilter{
		IsPrimaryVersion: f.IsPrimaryVersion,
		LibraryState:     libraryState,
		ReviewStatus:     reviewStatus,
		RestrictToIDs:    restrictIDs,
		Predicate:        predicate,
	}
	if f.SortBy == "title" {
		bsf.SortBy = "title"
		bsf.SortAscending = sortAsc
	}
	return bsf, true, pebbleLookupsPtr
}

// strippedFieldNames returns the field names from a FieldFilter slice,
// for log diagnostics only.
func strippedFieldNames(ff []FieldFilter) []string {
	out := make([]string, 0, len(ff))
	for _, f := range ff {
		out = append(out, f.Field)
	}
	return out
}

// GetAudiobooks retrieves audiobooks with optional filtering.
// Supports search, author_id, series_id, is_primary_version, and library_state filters.
func (svc *AudiobookService) GetAudiobooks(ctx context.Context, limit int, offset int, search string, authorID *int, seriesID *int, filters ...ListFilters) ([]database.Book, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	// Normalize limit and offset
	if limit <= 0 || limit > 100000 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var f ListFilters
	if len(filters) > 0 {
		f = filters[0]
	}
	hasSorting := f.SortBy != ""
	// "title" sort can be pushed down to memdb (sorted radix index) — every
	// other sort key still needs the heavy in-memory path. This is the
	// dominant case (library page default), so the pushdown matters.
	titleSortPushdownable := f.SortBy == "title"
	heavySorting := hasSorting && !titleSortPushdownable
	hasPerUser := len(f.PerUserFilters) > 0 && f.UserID != ""
	hasFingerprintingFilters := f.FingerprintStatus != "" || f.CoveragePercentMin != nil || f.CoveragePercentMax != nil
	// "Heavy" post-filters require fetching all rows to apply in Go.
	// IsPrimaryVersion is intentionally NOT in this set because the store
	// (memdb-backed) can push it down via an indexed iteration — fetching
	// all 68K rows to satisfy ?is_primary_version=true was the prod
	// "library spins forever" bug.
	hasHeavyPostFilters := f.LibraryState != "" || f.Tag != "" || len(f.Tags) > 0 || len(f.FieldFilters) > 0 || hasPerUser || heavySorting || hasFingerprintingFilters
	hasPostFilters := hasHeavyPostFilters || f.IsPrimaryVersion != nil || titleSortPushdownable

	// When heavy post-filters are active, fetch all and filter in memory.
	storeLimit := limit
	storeOffset := offset
	if hasHeavyPostFilters {
		storeLimit = 0
		storeOffset = 0
	}

	// Initialize as empty slice to ensure we return [] instead of null
	books := []database.Book{}
	var err error

	// Apply filters in order of precedence
	if search != "" {
		if svc.searchIndex != nil {
			books, err = svc.searchWithBleve(search, limit, offset)
		} else {
			books, err = svc.store.SearchBooks(search, limit, offset)
		}
	} else if authorID != nil {
		books, err = svc.store.GetBooksByAuthorID(*authorID)
	} else if seriesID != nil {
		books, err = svc.store.GetBooksBySeriesID(*seriesID)
	}

	// Fall back to generic list only when no filter was applied
	if search == "" && authorID == nil && seriesID == nil {
		// Pushdown path: only "light" filters (is_primary_version, title
		// sort) — let the store paginate using its index, no in-memory
		// full-scan.
		sortAsc := !strings.EqualFold(f.SortOrder, "desc")
		if !hasHeavyPostFilters {
			cacheKey := fmt.Sprintf("all:%d:%d:p=%v:sb=%s:asc=%v",
				limit, offset, f.IsPrimaryVersion, f.SortBy, sortAsc)
			if cached, ok := svc.listCache.Get(cacheKey); ok {
				return cached, nil
			}
			summaries, sErr := svc.summariesPushdown(storeLimit, storeOffset, f.IsPrimaryVersion, f.SortBy, sortAsc)
			if sErr == nil && summaries != nil {
				books = bookSummariesToBooks(summaries)
				svc.listCache.Set(cacheKey, books)
			}
		} else {
			// Heavy-filter pushdown: build a BookSummaryFilter that
			// captures every predicate the walker can apply in-loop, then
			// call summariesPushdownFiltered with REAL limit/offset. The
			// walker stops at limit+offset matches — no full-corpus
			// materialization, no 1GB working set per query.
			//
			// Falls back to the legacy fetch-all-then-filter path when the
			// filter set contains something we can't push down (non-title
			// sort, fingerprint filters). Pre-fix this was 100% of heavy
			// queries; post-fix it's the rare ones.
			if bsf, pushdownOK, pebbleLookups := svc.buildBookSummaryFilterWithLookupCount(f, sortAsc); pushdownOK {
				summaries, didPushdown, sErr := svc.summariesPushdownFiltered(limit, offset, bsf)
				if sErr == nil && summaries != nil {
					books = bookSummariesToBooks(summaries)
				}
				if pebbleLookups != nil && *pebbleLookups > 0 {
					slog.Debug("GetAudiobooks: stripped-field predicate Pebble fallback",
						"lookups", *pebbleLookups, "books_returned", len(books))
				}
				// When the store actually pushed down (production memdb
				// path), the walker has already applied filters AND
				// pagination — re-running the in-memory post-filter
				// would re-apply pagination against an already-paginated
				// slice (offset bug) and waste cycles. Skip it.
				//
				// When the store fell back (mock / no filteredSummaryStore
				// impl), summaries are unfiltered — let the post-filter
				// pass below do its thing as the safety net.
				if didPushdown {
					hasPostFilters = false
				}
			} else {
				summaries, _, sErr := svc.summariesPushdownFiltered(storeLimit, storeOffset, database.BookSummaryFilter{})
				if sErr == nil && summaries != nil {
					books = bookSummariesToBooks(summaries)
				}
			}
		}
	}

	if err != nil {
		return nil, err
	}

	// Apply post-filters
	if hasPostFilters {
		// If tag filter is set, build a set of matching book IDs (intersection of all tags)
		var tagBookIDs map[string]struct{}
		tagsToMatch := f.Tags
		if len(tagsToMatch) == 0 && f.Tag != "" {
			tagsToMatch = []string{f.Tag}
		}
		if len(tagsToMatch) > 0 {
			for _, tag := range tagsToMatch {
				if tag == "" {
					continue
				}
				ids, tagErr := svc.store.GetBooksByTag(tag)
				if tagErr != nil {
					return nil, tagErr
				}
				curSet := make(map[string]struct{}, len(ids))
				for _, id := range ids {
					curSet[id] = struct{}{}
				}
				if tagBookIDs == nil {
					tagBookIDs = curSet
				} else {
					for id := range tagBookIDs {
						if _, ok := curSet[id]; !ok {
							delete(tagBookIDs, id)
						}
					}
				}
				if len(tagBookIDs) == 0 {
					break
				}
			}
		}

		filtered := make([]database.Book, 0, len(books))
		for _, b := range books {
			if len(tagsToMatch) > 0 {
				if tagBookIDs == nil {
					continue
				}
				if _, ok := tagBookIDs[b.ID]; !ok {
					continue
				}
			}
			if f.IsPrimaryVersion != nil {
				bPrimary := b.IsPrimaryVersion != nil && *b.IsPrimaryVersion
				if *f.IsPrimaryVersion != bPrimary {
					continue
				}
			}
			if f.LibraryState != "" {
				bState := ""
				if b.LibraryState != nil {
					bState = *b.LibraryState
				}
				if bState != f.LibraryState {
					continue
				}
			}
			filtered = append(filtered, b)
		}

		// Apply field-specific filters (advanced search). Books here came
		// from BookSummary projections, which never carry stripped fields
		// (description / version_notes / book_sig_v1). Route those filters
		// through the Pebble fallback so they don't silently miss.
		if len(f.FieldFilters) > 0 {
			cheapFF, strippedFF := splitFieldFilters(f.FieldFilters)
			var pebbleLookups int64
			var warnOnce sync.Once
			warnFn := func(id string, err error) {
				warnOnce.Do(func() {
					slog.Warn("post-filter stripped-field Pebble fallback: GetBookByID failed; dropping row",
						"book_id", id, "err", err)
				})
			}
			fetchFull := func(id string) (*database.Book, error) {
				return svc.store.GetBookByID(id)
			}
			fieldFiltered := make([]database.Book, 0, len(filtered))
			for i := range filtered {
				b := filtered[i]
				if matchesFieldFiltersWithStrippedFallback(&b, cheapFF, strippedFF, fetchFull, &pebbleLookups, warnFn) {
					fieldFiltered = append(fieldFiltered, b)
				}
			}
			if pebbleLookups > 0 {
				slog.Debug("GetAudiobooks post-filter: stripped-field Pebble fallback",
					"lookups", pebbleLookups, "matched", len(fieldFiltered))
			}
			filtered = fieldFiltered
		}

		// Apply fingerprinting filters
		if hasFingerprintingFilters {
			fpFiltered := make([]database.Book, 0, len(filtered))
			for _, b := range filtered {
				if f.FingerprintStatus != "" {
					// Filter by fingerprint status
					if b.FingerprintStatus != f.FingerprintStatus {
						continue
					}
				}
				if f.CoveragePercentMin != nil {
					// Filter by minimum coverage percentage
					if b.CoveragePercent < *f.CoveragePercentMin {
						continue
					}
				}
				if f.CoveragePercentMax != nil {
					// Filter by maximum coverage percentage
					if b.CoveragePercent > *f.CoveragePercentMax {
						continue
					}
				}
				fpFiltered = append(fpFiltered, b)
			}
			filtered = fpFiltered
		}

		// Apply per-user filters (read_status / progress_pct / last_played).
		// Requires a caller user ID; without one we skip rather than
		// silently dropping every book.
		if hasPerUser {
			perUserFiltered := make([]database.Book, 0, len(filtered))
			for _, b := range filtered {
				state, _ := svc.store.GetUserBookState(f.UserID, b.ID)
				if matchesAllPerUserFilters(state, f.PerUserFilters) {
					perUserFiltered = append(perUserFiltered, b)
				}
			}
			filtered = perUserFiltered
		}

		// Apply pagination after filtering
		if offset > 0 && offset < len(filtered) {
			filtered = filtered[offset:]
		} else if offset >= len(filtered) {
			filtered = nil
		}
		if limit > 0 && limit < len(filtered) {
			filtered = filtered[:limit]
		}
		books = filtered
	}

	// Apply sorting after all filtering but before returning
	applySorting(books, f)

	// Ensure we never return null - always return empty array
	if books == nil {
		books = []database.Book{}
	}

	return books, nil
}

// CountAudiobooksFiltered returns the count of audiobooks matching the
// given filters. Uses the memdb count-only pushdown (no projection
// allocations, no full-corpus materialization) for the common filter set.
// Falls back to materialize-and-count when the filter set contains
// something we can't push down (non-title sort doesn't matter for counts;
// fingerprint filters depend on BookFile data).
func (svc *AudiobookService) CountAudiobooksFiltered(ctx context.Context, filters ListFilters) (int, error) {
	if svc.store == nil {
		return 0, fmt.Errorf("database not initialized")
	}
	// SortBy doesn't affect counts; clear it so buildBookSummaryFilter
	// doesn't reject the filter for non-title sort. Counts don't depend
	// on iteration order.
	filtersForCount := filters
	filtersForCount.SortBy = ""
	bsf, pushdownOK := svc.buildBookSummaryFilter(filtersForCount, true)
	if pushdownOK {
		return svc.countSummariesPushdownFiltered(bsf)
	}

	// Fallback: legacy fetch-all-then-filter path. Hit only when the
	// caller passed a fingerprint filter, which depends on BookFile data
	// not stored on Book. Slow but correct.
	books, err := svc.store.GetAllBooks(0, 0)
	if err != nil {
		return 0, err
	}
	tagsToMatch := filters.Tags
	if len(tagsToMatch) == 0 && filters.Tag != "" {
		tagsToMatch = []string{filters.Tag}
	}
	var tagBookIDs map[string]struct{}
	for _, tag := range tagsToMatch {
		if tag == "" {
			continue
		}
		ids, tagErr := svc.store.GetBooksByTag(tag)
		if tagErr != nil {
			return 0, tagErr
		}
		curSet := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			curSet[id] = struct{}{}
		}
		if tagBookIDs == nil {
			tagBookIDs = curSet
		} else {
			for id := range tagBookIDs {
				if _, ok := curSet[id]; !ok {
					delete(tagBookIDs, id)
				}
			}
		}
		if len(tagBookIDs) == 0 {
			break
		}
	}

	count := 0
	for _, b := range books {
		if filters.IsPrimaryVersion != nil {
			bPrimary := b.IsPrimaryVersion != nil && *b.IsPrimaryVersion
			if *filters.IsPrimaryVersion != bPrimary {
				continue
			}
		}
		if filters.LibraryState != "" {
			bState := ""
			if b.LibraryState != nil {
				bState = *b.LibraryState
			}
			if bState != filters.LibraryState {
				continue
			}
		}
		if len(tagsToMatch) > 0 {
			if tagBookIDs == nil {
				continue
			}
			if _, ok := tagBookIDs[b.ID]; !ok {
				continue
			}
		}
		if filters.FingerprintStatus != "" && b.FingerprintStatus != filters.FingerprintStatus {
			continue
		}
		if filters.CoveragePercentMin != nil && b.CoveragePercent < *filters.CoveragePercentMin {
			continue
		}
		if filters.CoveragePercentMax != nil && b.CoveragePercent > *filters.CoveragePercentMax {
			continue
		}
		count++
	}
	return count, nil
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

// aggregateFileMetadata calculates total duration and file size from files
// for each book. Uses GetBookFilesForIDs to fetch ONLY the files for the
// passed-in books — not GetAllBookFiles, which materialized all 308K+
// book_file rows (~46GB heap) on every library list query and trampolined
// the process to 67GB the moment the filter-pushdown warmer started.
//
// This is the fetch-then-aggregate wrapper. Callers that already have a
// files map (e.g. the list handler which uses it twice) should call
// FetchBookFilesForBooks once and then aggregateFileMetadataWithFiles +
// EnrichAudiobooksWithNamesAndFiles to avoid a duplicate fetch.
func (svc *AudiobookService) aggregateFileMetadata(books []database.Book) {
	if svc.store == nil || len(books) == 0 {
		return
	}
	filesByBookID := svc.FetchBookFilesForBooks(books)
	svc.aggregateFileMetadataWithFiles(books, filesByBookID)
}

// FetchBookFilesForBooks performs the targeted batch fetch via memdb's
// book_id index, with the same unwrap + fallback semantics as the original
// aggregateFileMetadata. Returns a non-nil map (possibly empty) so callers
// can safely range over it without nil checks.
func (svc *AudiobookService) FetchBookFilesForBooks(books []database.Book) map[string][]database.BookFile {
	if svc.store == nil || len(books) == 0 {
		return map[string][]database.BookFile{}
	}
	bookIDs := make([]string, 0, len(books))
	for _, b := range books {
		bookIDs = append(bookIDs, b.ID)
	}

	type batchFilesStore interface {
		GetBookFilesForIDs(ids []string) (map[string][]database.BookFile, error)
	}
	var filesByBookID map[string][]database.BookFile
	if fs, ok := svc.store.(batchFilesStore); ok {
		if m, err := fs.GetBookFilesForIDs(bookIDs); err == nil {
			filesByBookID = m
		}
	} else if uw, ok := svc.store.(interface{ Unwrap() database.Store }); ok {
		if fs, ok2 := uw.Unwrap().(batchFilesStore); ok2 {
			if m, err := fs.GetBookFilesForIDs(bookIDs); err == nil {
				filesByBookID = m
			}
		}
	}
	if filesByBookID == nil {
		// Store doesn't expose the batched method — fall back to per-book
		// fetch. Slow (N+1) but bounded by len(books) (typically 20).
		filesByBookID = make(map[string][]database.BookFile, len(books))
		for _, id := range bookIDs {
			files, err := svc.store.GetBookFiles(id)
			if err != nil {
				slog.Warn("FetchBookFilesForBooks: GetBookFiles failed", "book_id", id, "err", err)
				continue
			}
			filesByBookID[id] = files
		}
	}
	return filesByBookID
}

// aggregateFileMetadataWithFiles applies duration/file-size aggregation
// using a pre-fetched files map. Skips the GetBookFilesForIDs call,
// allowing callers that already have the map to reuse it.
func (svc *AudiobookService) aggregateFileMetadataWithFiles(books []database.Book, filesByBookID map[string][]database.BookFile) {
	if len(books) == 0 {
		return
	}
	if filesByBookID == nil {
		filesByBookID = map[string][]database.BookFile{}
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

// EnrichAudiobooksWithNames adds author and series names to audiobook details.
// Also aggregates duration and file size from individual files.
// Batch-fetches authors and series by unique IDs to avoid N+1 DB lookups.
//
// This is the fetch-then-enrich wrapper. Callers that have already fetched
// the book_files map should use EnrichAudiobooksWithNamesAndFiles to avoid
// a duplicate GetBookFilesForIDs call.
func (svc *AudiobookService) EnrichAudiobooksWithNames(books []database.Book) []AudiobookDetail {
	filesByBookID := svc.FetchBookFilesForBooks(books)
	return svc.EnrichAudiobooksWithNamesAndFiles(books, filesByBookID)
}

// EnrichAudiobooksWithNamesAndFiles is the variant that accepts a pre-fetched
// files map and threads it into aggregation, avoiding a duplicate
// GetBookFilesForIDs call when the caller already has the map (e.g. the
// list handler uses it again for fingerprint compute).
func (svc *AudiobookService) EnrichAudiobooksWithNamesAndFiles(books []database.Book, filesByBookID map[string][]database.BookFile) []AudiobookDetail {
	// Aggregate file metadata for all books at once (avoids N+1)
	svc.aggregateFileMetadataWithFiles(books, filesByBookID)

	// Collect unique IDs that need DB lookups (skip pre-loaded objects).
	authorIDs := make([]int, 0)
	seriesIDs := make([]int, 0)
	for i := range books {
		b := &books[i]
		if b.Author == nil && b.AuthorID != nil {
			authorIDs = append(authorIDs, *b.AuthorID)
		}
		if b.Series == nil && b.SeriesID != nil {
			seriesIDs = append(seriesIDs, *b.SeriesID)
		}
	}

	var authorsMap map[int]*database.Author
	var seriesMap map[int]*database.Series
	if len(authorIDs) > 0 && svc.store != nil {
		authorsMap, _ = svc.store.GetAuthorsByIDs(authorIDs)
	}
	if len(seriesIDs) > 0 && svc.store != nil {
		seriesMap, _ = svc.store.GetSeriesByIDs(seriesIDs)
	}

	enrichedBooks := make([]AudiobookDetail, 0, len(books))
	for i := range books {
		b := &books[i]
		detail := AudiobookDetail{Book: b}

		authorName := ""
		if b.Author != nil {
			authorName = b.Author.Name
		} else if b.AuthorID != nil && authorsMap != nil {
			if a, ok := authorsMap[*b.AuthorID]; ok {
				authorName = a.Name
			}
		}

		seriesName := ""
		if b.Series != nil {
			seriesName = b.Series.Name
		} else if b.SeriesID != nil && seriesMap != nil {
			if s, ok := seriesMap[*b.SeriesID]; ok {
				seriesName = s.Name
			}
		}

		if authorName != "" {
			detail.AuthorName = &authorName
		}
		if seriesName != "" {
			detail.SeriesName = &seriesName
		}
		enrichedBooks = append(enrichedBooks, detail)
	}
	return enrichedBooks
}

// GetAudiobook retrieves a single audiobook by ID with full metadata provenance
func (svc *AudiobookService) GetAudiobook(ctx context.Context, id string) (*database.Book, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	if cached, ok := svc.bookCache.Get(id); ok {
		return cached, nil
	}

	book, err := svc.store.GetBookByID(id)
	if err != nil {
		return nil, err
	}
	if book == nil {
		return nil, fmt.Errorf("audiobook not found")
	}

	// Load metadata state and extract file metadata
	state, err := svc.loadMetadataState(book.ID)
	if err != nil {
		slog.Info("[ERROR] GetAudiobook failed to load metadata state for", "book", book.ID, "err", err)
		// Don't fail the entire request, just use empty state
		state = map[string]metadataFieldState{}
	}

	authorName, seriesName := resolveAuthorAndSeriesNames(svc.store, book)

	meta := svc.extractBookFileMetadata(book, authorName)

	// Backfill duration (and other media info) from file if DB fields are missing
	if book.FilePath != "" && book.Duration == nil {
		if mi, miErr := mediainfo.Extract(book.FilePath); miErr == nil && mi.Duration > 0 {
			book.Duration = &mi.Duration
			if _, updErr := svc.store.UpdateBook(book.ID, book); updErr != nil {
				slog.Warn("GetAudiobook failed to backfill duration for", "book", book.ID, "updErr", updErr)
			}
		}
	}

	// Build metadata provenance
	book.MetadataProvenance = buildMetadataProvenance(book, state, meta, authorName, seriesName, nil)
	nowUTC := time.Now().UTC()
	book.MetadataProvenanceAt = &nowUTC

	svc.bookCache.Set(id, book)
	return book, nil
}

// GetAudiobookTags retrieves metadata tags and media info for an audiobook
func (svc *AudiobookService) GetAudiobookTags(ctx context.Context, id string, compareID string, snapshotTS string) (map[string]any, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	book, err := svc.store.GetBookByID(id)
	if err != nil {
		return nil, err
	}
	if book == nil {
		return nil, fmt.Errorf("audiobook not found")
	}

	state, err := svc.loadMetadataState(book.ID)
	if err != nil {
		slog.Info("[ERROR] GetAudiobookTags failed to load metadata state for", "book", book.ID, "err", err)
		state = map[string]metadataFieldState{}
	}

	authorName, seriesName := resolveAuthorAndSeriesNames(svc.store, book)

	response := map[string]any{
		"media_info": map[string]any{
			"codec":       stringVal(book.Codec),
			"bitrate":     intVal(book.Bitrate),
			"sample_rate": intVal(book.SampleRate),
			"channels":    intVal(book.Channels),
			"bit_depth":   intVal(book.BitDepth),
			"quality":     stringVal(book.Quality),
			"duration":    intVal(book.Duration),
		},
		"tags": map[string]database.MetadataProvenanceEntry{},
	}

	meta := svc.extractBookFileMetadata(book, authorName)

	// Backfill empty media_info from file if DB fields are missing
	if book.FilePath != "" && (book.Codec == nil || book.Bitrate == nil || book.SampleRate == nil) {
		if mi, err := mediainfo.Extract(book.FilePath); err == nil {
			needsUpdate := false
			if book.Codec == nil && mi.Codec != "" {
				book.Codec = &mi.Codec
				needsUpdate = true
			}
			if book.Bitrate == nil && mi.Bitrate > 0 {
				book.Bitrate = &mi.Bitrate
				needsUpdate = true
			}
			if book.SampleRate == nil && mi.SampleRate > 0 {
				book.SampleRate = &mi.SampleRate
				needsUpdate = true
			}
			if book.Channels == nil && mi.Channels > 0 {
				book.Channels = &mi.Channels
				needsUpdate = true
			}
			if book.Duration == nil && mi.Duration > 0 {
				book.Duration = &mi.Duration
				needsUpdate = true
			}
			if needsUpdate {
				if _, err := svc.store.UpdateBook(book.ID, book); err != nil {
					slog.Warn("GetAudiobookTags failed to backfill media info for", "book", book.ID, "err", err)
				}
				response["media_info"] = map[string]any{
					"codec":       stringVal(book.Codec),
					"bitrate":     intVal(book.Bitrate),
					"sample_rate": intVal(book.SampleRate),
					"channels":    intVal(book.Channels),
					"bit_depth":   intVal(book.BitDepth),
					"quality":     stringVal(book.Quality),
					"duration":    intVal(book.Duration),
				}
			}
		}
	}

	// Load comparison metadata if compare_id is provided
	var comparisonValues map[string]any
	if snapshotTS != "" {
		ts, err := time.Parse(time.RFC3339Nano, snapshotTS)
		if err != nil {
			return nil, fmt.Errorf("invalid snapshot timestamp: %w", err)
		}
		snapshotBook, verErr := svc.store.GetBookAtVersion(id, ts)
		if verErr == nil && snapshotBook != nil {
			snapshotAuthorName, snapshotSeriesName := resolveAuthorAndSeriesNames(svc.store, snapshotBook)
			comparisonValues = buildComparisonValuesFromBook(snapshotBook, snapshotAuthorName, snapshotSeriesName)
		} else {
			// Fallback: reconstruct "before" state from activity log old_values
			slog.Debug("GetAudiobookTags GetBookAtVersion failed (), falling back to activity log for snapshot at", "verErr", verErr, "snapshotTS", snapshotTS)
			if svc.activityService != nil {
				comparisonValues = buildComparisonValuesFromActivityLog(svc.activityService, id, ts)
			}
		}
	} else if compareID != "" {
		compBook, err := svc.store.GetBookByID(compareID)
		if err != nil {
			slog.Warn("GetAudiobookTags failed to load comparison book", "compareID", compareID, "err", err)
		} else if compBook != nil && compBook.FilePath != "" {
			if cm, err := metadata.ExtractMetadata(compBook.FilePath, nil); err == nil {
				comparisonValues = buildComparisonValuesFromMetadata(&cm)
			} else {
				slog.Warn("GetAudiobookTags failed to extract comparison metadata for", "compBook", compBook.FilePath, "err", err)
			}
		}
	}

	tags := buildMetadataProvenance(book, state, meta, authorName, seriesName, comparisonValues)
	response["tags"] = tags

	return response, nil
}

func (svc *AudiobookService) extractBookFileMetadata(book *database.Book, authorName string) metadata.Metadata {
	var meta metadata.Metadata
	if book == nil || book.FilePath == "" {
		return meta
	}

	m, err := metadata.ExtractMetadata(book.FilePath, nil)
	if err != nil {
		slog.Warn("audiobook_service failed to extract metadata for", "book", book.FilePath, "err", err)
		return meta
	}

	if m.OrganizerTagVersion == "" &&
		strings.TrimSpace(authorName) != "" &&
		strings.TrimSpace(m.Narrator) != "" &&
		strings.EqualFold(strings.TrimSpace(m.Artist), strings.TrimSpace(m.Narrator)) {
		m.Artist = authorName
		m.AuthorSource = "database author fallback"
	}

	return m
}

// GetDuplicateBooks retrieves all duplicate book groups
func (svc *AudiobookService) GetDuplicateBooks(ctx context.Context) (*DuplicatesResult, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	// Get hash-based duplicates
	duplicateGroups, err := svc.store.GetDuplicateBooks()
	if err != nil {
		return nil, err
	}
	if duplicateGroups == nil {
		duplicateGroups = [][]database.Book{}
	}

	// Get folder-based duplicates (same title in same folder, e.g. M4B + MP3)
	folderGroups, err := svc.store.GetFolderDuplicates()
	if err != nil {
		slog.Warn("folder duplicate detection failed", "err", err)
	} else {
		// Merge folder groups, avoiding duplicate groups already found by hash
		seenBookIDs := map[string]bool{}
		for _, group := range duplicateGroups {
			for _, b := range group {
				seenBookIDs[b.ID] = true
			}
		}
		for _, group := range folderGroups {
			// Skip if all books in this group are already in hash-based groups
			allSeen := true
			for _, b := range group {
				if !seenBookIDs[b.ID] {
					allSeen = false
					break
				}
			}
			if !allSeen {
				duplicateGroups = append(duplicateGroups, group)
				for _, b := range group {
					seenBookIDs[b.ID] = true
				}
			}
		}
	}

	// Calculate total duplicates count
	totalDuplicates := 0
	for _, group := range duplicateGroups {
		totalDuplicates += len(group) - 1
	}

	return &DuplicatesResult{
		Groups:         duplicateGroups,
		GroupCount:     len(duplicateGroups),
		DuplicateCount: totalDuplicates,
	}, nil
}

// GetSoftDeletedBooks retrieves soft-deleted audiobooks with optional age filter
func (svc *AudiobookService) GetSoftDeletedBooks(ctx context.Context, limit int, offset int, olderThanDays *int) ([]database.Book, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	// Normalize limit and offset
	if limit <= 0 || limit > 10000 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var cutoff *time.Time
	if olderThanDays != nil && *olderThanDays > 0 {
		ts := time.Now().AddDate(0, 0, -*olderThanDays)
		cutoff = &ts
	}

	books, err := svc.store.ListSoftDeletedBooks(limit, offset, cutoff)
	if err != nil {
		return nil, err
	}

	if books == nil {
		books = []database.Book{}
	}

	return books, nil
}

// PurgeSoftDeletedBooks permanently deletes soft-deleted audiobooks
func (svc *AudiobookService) PurgeSoftDeletedBooks(ctx context.Context, deleteFiles bool, olderThanDays *int) (*PurgeResult, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	var cutoff *time.Time
	if olderThanDays != nil && *olderThanDays > 0 {
		ts := time.Now().AddDate(0, 0, -*olderThanDays)
		cutoff = &ts
	}

	books, err := svc.store.ListSoftDeletedBooks(1_000_000, 0, cutoff)
	if err != nil {
		return nil, err
	}

	result := &PurgeResult{
		Attempted: len(books),
		Errors:    []string{},
	}

	for _, book := range books {
		// Tombstone external IDs so reimport is blocked
		if eidStore := asExternalIDStore(svc.store); eidStore != nil {
			extIDs, _ := eidStore.GetExternalIDsForBook(book.ID)
			for _, ext := range extIDs {
				_ = eidStore.TombstoneExternalID(ext.Source, ext.ExternalID)
			}
		}

		// Defense-in-depth: enqueue iTunes removes for any PIDs still
		// on this book. Soft-delete already enqueues these but if the
		// book was soft-deleted before that hook existed, this is the
		// last chance to clean iTunes before the row vanishes.
		bookCopy := book
		svc.enqueueITunesRemovesForBook(book.ID, &bookCopy)

		// Step 1: Create tombstone (snapshot of book for rollback)
		if err := svc.store.CreateBookTombstone(&book); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: failed to create tombstone: %v", book.ID, err))
			continue
		}

		// Step 2: Delete from database (book record gone, tombstone preserved)
		if err := svc.store.DeleteBook(book.ID); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: failed to delete DB record: %v", book.ID, err))
			// Tombstone exists but book still exists — sweeper will clean up tombstone
			continue
		}

		// Step 3: Delete file if requested (only from organizer root, never from protected/import paths)
		if deleteFiles && book.FilePath != "" {
			if isProtectedPath(svc.store, book.FilePath) {
				slog.Debug("purge skipping file deletion for — protected path", "book", book.ID, "book", book.FilePath)
			} else {
				info, statErr := os.Stat(book.FilePath)
				if statErr == nil && info.IsDir() {
					// Directory-based book: remove all book files then the directory
					if bookFiles, bfErr := svc.store.GetBookFiles(book.ID); bfErr == nil {
						for _, bf := range bookFiles {
							if bf.FilePath != "" && !isProtectedPath(svc.store, bf.FilePath) {
								if rmErr := os.Remove(bf.FilePath); rmErr != nil && !os.IsNotExist(rmErr) {
									result.Errors = append(result.Errors, fmt.Sprintf("%s: failed to delete book file %s: %v", book.ID, bf.FilePath, rmErr))
								}
							}
						}
					}
					// Remove the directory if it is now empty
					if entries, rdErr := os.ReadDir(book.FilePath); rdErr == nil && len(entries) == 0 {
						if rmErr := os.Remove(book.FilePath); rmErr != nil && !os.IsNotExist(rmErr) {
							result.Errors = append(result.Errors, fmt.Sprintf("%s: failed to remove empty dir %s: %v", book.ID, book.FilePath, rmErr))
						} else if rmErr == nil {
							result.FilesDeleted++
							// Also clean up empty parent dirs up to RootDir
							if config.AppConfig.RootDir != "" {
								parentDir := filepath.Dir(book.FilePath)
								for parentDir != config.AppConfig.RootDir &&
									strings.HasPrefix(parentDir, config.AppConfig.RootDir) &&
									parentDir != "/" {
									pe, peErr := os.ReadDir(parentDir)
									if peErr != nil || len(pe) > 0 {
										break
									}
									if os.Remove(parentDir) != nil {
										break
									}
									parentDir = filepath.Dir(parentDir)
								}
							}
						}
					}
				} else if statErr == nil {
					// Single-file book
					if err := os.Remove(book.FilePath); err != nil && !os.IsNotExist(err) {
						result.Errors = append(result.Errors, fmt.Sprintf("%s: failed to delete file (tombstone preserved): %v", book.ID, err))
						// DB record gone, file still exists, tombstone preserved for sweeper
					} else if err == nil {
						result.FilesDeleted++
						// Clean up empty parent dirs up to RootDir
						if config.AppConfig.RootDir != "" {
							parentDir := filepath.Dir(book.FilePath)
							for parentDir != config.AppConfig.RootDir &&
								strings.HasPrefix(parentDir, config.AppConfig.RootDir) &&
								parentDir != "/" {
								pe, peErr := os.ReadDir(parentDir)
								if peErr != nil || len(pe) > 0 {
									break
								}
								if os.Remove(parentDir) != nil {
									break
								}
								parentDir = filepath.Dir(parentDir)
							}
						}
					}
				}
				// If statErr is os.IsNotExist, file is already gone — that's fine
			}
		}

		// Step 4: Clean up tombstone (best-effort — sweeper handles failures)
		_ = svc.store.DeleteBookTombstone(book.ID)

		result.Purged++
	}

	if result.Purged > 0 {
		svc.InvalidateBookCaches()
	}

	return result, nil
}

// RestoreAudiobook restores a soft-deleted audiobook
func (svc *AudiobookService) RestoreAudiobook(ctx context.Context, id string) (*database.Book, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	book, err := svc.store.GetBookByID(id)
	if err != nil || book == nil {
		return nil, fmt.Errorf("audiobook not found")
	}

	// Restore to imported state so the UI can re-process if needed
	book.MarkedForDeletion = boolPtr(false)
	book.MarkedForDeletionAt = nil
	book.LibraryState = stringPtr("imported")

	updated, err := svc.store.UpdateBook(id, book)
	if err != nil {
		return nil, err
	}

	svc.InvalidateBookCaches()
	return updated, nil
}

// CountAudiobooks returns the total count of audiobooks
func (svc *AudiobookService) CountAudiobooks(ctx context.Context) (int, error) {
	if svc.store == nil {
		return 0, fmt.Errorf("database not initialized")
	}

	count, err := svc.store.CountBooks()
	if err != nil {
		return 0, err
	}
	return count, nil
}

// UpdateAudiobookRequest represents parameters for updating an audiobook
type UpdateAudiobookRequest struct {
	Updates             *AudiobookUpdate
	RawPayload          map[string]json.RawMessage
	ResolvingAuthorName string
	ResolvingSeriesName string
}

// UpdateAudiobook updates an audiobook with new metadata and handles overrides
func (svc *AudiobookService) UpdateAudiobook(ctx context.Context, id string, req *UpdateAudiobookRequest) (*database.Book, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	currentBook, err := svc.store.GetBookByID(id)
	if err != nil {
		return nil, err
	}
	if currentBook == nil {
		return nil, fmt.Errorf("audiobook not found")
	}

	now := time.Now()

	// Apply updates from req.Updates to currentBook first
	// This ensures extractors can read the updated fields
	if req.Updates.Title != "" {
		currentBook.Title = req.Updates.Title
	}
	if req.Updates.Format != "" {
		currentBook.Format = req.Updates.Format
	}
	if req.Updates.FilePath != "" && req.Updates.FilePath != currentBook.FilePath {
		// Validate the new path is inside an allowed directory before accepting
		// the change (go/path-injection), then confirm it exists.
		absNewPath, err := filepath.Abs(req.Updates.FilePath)
		if err != nil {
			return nil, fmt.Errorf("invalid new path: %w", err)
		}
		importPaths, err := svc.store.GetAllImportPaths()
		if err != nil {
			return nil, fmt.Errorf("failed to check allowed paths: %w", err)
		}
		if !fileops.IsAllowedPath(absNewPath, importPaths) {
			return nil, fmt.Errorf("new path is not in an allowed directory")
		}
		// Validate new file exists before accepting path change
		if _, err := os.Stat(absNewPath); err != nil {
			return nil, fmt.Errorf("file does not exist at new path: %s", absNewPath)
		}
		slog.Info("audiobook_service FilePath changed for →", "id", id, "currentBook", currentBook.FilePath, "value2", absNewPath)
		currentBook.FilePath = absNewPath
	}
	if req.Updates.Narrator != nil {
		currentBook.Narrator = req.Updates.Narrator
	}
	if req.Updates.Publisher != nil {
		currentBook.Publisher = req.Updates.Publisher
	}
	if req.Updates.Language != nil {
		currentBook.Language = req.Updates.Language
	}
	if req.Updates.AudiobookReleaseYear != nil {
		currentBook.AudiobookReleaseYear = req.Updates.AudiobookReleaseYear
	}
	if req.Updates.ISBN10 != nil {
		currentBook.ISBN10 = req.Updates.ISBN10
	}
	if req.Updates.ISBN13 != nil {
		currentBook.ISBN13 = req.Updates.ISBN13
	}
	if req.Updates.AuthorID != nil {
		currentBook.AuthorID = req.Updates.AuthorID
	}
	if req.Updates.SeriesID != nil {
		currentBook.SeriesID = req.Updates.SeriesID
	}

	payload := &AudiobookUpdate{
		Book: currentBook,
	}

	// Load and process metadata state
	state, err := svc.loadMetadataState(id)
	if err != nil {
		slog.Info("[ERROR] UpdateAudiobook failed to load metadata state", "err", err)
		return nil, fmt.Errorf("failed to load metadata state")
	}
	if state == nil {
		state = map[string]metadataFieldState{}
	}

	// Create a MetadataStateService for recording change history.
	mss := newMetadataStateSvc(svc.store)

	// Process overrides
	for field, override := range req.Updates.Overrides {
		entry := state[field]
		oldOverrideValue := entry.OverrideValue
		if override.Clear {
			entry.OverrideValue = nil
			entry.OverrideLocked = false
			entry.UpdatedAt = now
			// Record history for clearing an override.
			if fmt.Sprintf("%v", oldOverrideValue) != fmt.Sprintf("%v", nil) {
				mss.recordChange(id, field, "override", "user_edit", oldOverrideValue, nil)
			}
		} else {
			if len(override.Value) > 0 {
				val := decodeRawValue(override.Value)
				entry.OverrideValue = val
				entry.OverrideLocked = override.Locked == nil || *override.Locked
				entry.UpdatedAt = now
				ApplyOverrideToPayload(payload, field, val)
				// Record history for setting an override.
				if fmt.Sprintf("%v", oldOverrideValue) != fmt.Sprintf("%v", val) {
					mss.recordChange(id, field, "override", "user_edit", oldOverrideValue, val)
				}
			} else if override.Locked != nil {
				entry.OverrideLocked = *override.Locked
				entry.UpdatedAt = now
			}
			if len(override.FetchedValue) > 0 {
				entry.FetchedValue = decodeRawValue(override.FetchedValue)
				if entry.UpdatedAt.IsZero() {
					entry.UpdatedAt = now
				}
			}
		}
		state[field] = entry
	}

	// Resolve author by name or ID — auto-split on " & " for multiple authors
	var resolvedAuthorName string
	if req.Updates.AuthorName != nil {
		name := strings.TrimSpace(*req.Updates.AuthorName)
		if name != "" {
			// Split on " & " to support multiple authors
			authorNames := splitMultipleNames(name)
			var bookAuthors []database.BookAuthor
			var primaryAuthorID int
			for i, aName := range authorNames {
				aName = strings.TrimSpace(aName)
				if aName == "" {
					continue
				}
				normalizedName := dedup.NormalizeAuthorName(aName)
				author, err := svc.store.GetAuthorByName(normalizedName)
				if err != nil {
					return nil, fmt.Errorf("failed to resolve author")
				}
				if author == nil {
					author, err = svc.store.CreateAuthor(normalizedName)
					if err != nil {
						return nil, fmt.Errorf("failed to create author")
					}
				}
				role := "author"
				if i > 0 {
					role = "co-author"
				}
				bookAuthors = append(bookAuthors, database.BookAuthor{
					BookID: id, AuthorID: author.ID, Role: role, Position: i,
				})
				if i == 0 {
					primaryAuthorID = author.ID
				}
			}
			// Set primary author on the book for backward compat
			payload.AuthorID = &primaryAuthorID
			resolvedAuthorName = name // Keep the combined name for display
			// Save multiple authors to join table
			if len(bookAuthors) > 0 {
				if err := svc.store.SetBookAuthors(id, bookAuthors); err != nil {
					slog.Warn("failed to set book authors", "err", err)
				}
			}
		} else {
			payload.AuthorID = nil
		}
	} else if payload.AuthorID != nil {
		if author, err := svc.store.GetAuthorByID(*payload.AuthorID); err == nil && author != nil {
			resolvedAuthorName = author.Name
		}
	}

	// Resolve narrator — auto-split on " & " for multiple narrators
	if req.Updates.Narrator != nil {
		narStr := strings.TrimSpace(*req.Updates.Narrator)
		if narStr != "" {
			narratorNames := splitMultipleNames(narStr)
			var bookNarrators []database.BookNarrator
			for i, nName := range narratorNames {
				nName = strings.TrimSpace(nName)
				if nName == "" {
					continue
				}
				narrator, err := svc.store.GetNarratorByName(nName)
				if err != nil || narrator == nil {
					narrator, err = svc.store.CreateNarrator(nName)
					if err != nil {
						slog.Warn("failed to create narrator", "nName", nName, "err", err)
						continue
					}
				}
				role := "narrator"
				if i > 0 {
					role = "co-narrator"
				}
				bookNarrators = append(bookNarrators, database.BookNarrator{
					BookID: id, NarratorID: narrator.ID, Role: role, Position: i,
				})
			}
			if len(bookNarrators) > 0 {
				if err := svc.store.SetBookNarrators(id, bookNarrators); err != nil {
					slog.Warn("failed to set book narrators", "err", err)
				}
			}
		}
	}

	// Resolve series by name or ID
	var resolvedSeriesName string
	if req.Updates.SeriesName != nil {
		name := strings.TrimSpace(*req.Updates.SeriesName)
		if name != "" {
			series, err := svc.store.GetSeriesByName(name, payload.AuthorID)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve series")
			}
			if series == nil {
				series, err = svc.store.CreateSeries(name, payload.AuthorID)
				if err != nil {
					return nil, fmt.Errorf("failed to create series")
				}
			}
			payload.SeriesID = &series.ID
			resolvedSeriesName = series.Name
		} else {
			payload.SeriesID = nil
		}
	} else if payload.SeriesID != nil {
		if series, err := svc.store.GetSeriesByID(*payload.SeriesID); err == nil && series != nil {
			resolvedSeriesName = series.Name
		}
	}

	// Process direct field updates (non-override)
	fieldExtractors := map[string]func() (any, bool){
		"title": func() (any, bool) {
			return payload.Title, true
		},
		"author_name": func() (any, bool) {
			if resolvedAuthorName == "" {
				return nil, false
			}
			return resolvedAuthorName, true
		},
		"series_name": func() (any, bool) {
			if resolvedSeriesName == "" {
				return nil, false
			}
			return resolvedSeriesName, true
		},
		"narrator": func() (any, bool) {
			if payload.Narrator == nil {
				return nil, false
			}
			return *payload.Narrator, true
		},
		"publisher": func() (any, bool) {
			if payload.Publisher == nil {
				return nil, false
			}
			return *payload.Publisher, true
		},
		"language": func() (any, bool) {
			if payload.Language == nil {
				return nil, false
			}
			return *payload.Language, true
		},
		"audiobook_release_year": func() (any, bool) {
			if payload.AudiobookReleaseYear == nil {
				return nil, false
			}
			return *payload.AudiobookReleaseYear, true
		},
		"isbn10": func() (any, bool) {
			if payload.ISBN10 == nil {
				return nil, false
			}
			return *payload.ISBN10, true
		},
		"isbn13": func() (any, bool) {
			if payload.ISBN13 == nil {
				return nil, false
			}
			return *payload.ISBN13, true
		},
	}

	for field, extractor := range fieldExtractors {
		if _, ok := req.RawPayload[field]; !ok {
			slog.Debug("UpdateAudiobook field not in RawPayload", "field", field)
			continue
		}
		if _, hasOverride := req.Updates.Overrides[field]; hasOverride {
			slog.Debug("UpdateAudiobook field has explicit override", "field", field)
			continue
		}
		if value, ok := extractor(); ok {
			slog.Debug("UpdateAudiobook creating state for field with value", "field", field, "value", value)
			entry := state[field]
			oldValue := entry.OverrideValue

			entry.OverrideValue = value
			entry.OverrideLocked = true
			entry.UpdatedAt = now
			state[field] = entry

			// Record history only when the value actually changed.
			if fmt.Sprintf("%v", oldValue) != fmt.Sprintf("%v", value) {
				mss.recordChange(id, field, "override", "user_edit", oldValue, value)
			}
		} else {
			slog.Debug("UpdateAudiobook extractor for field returned false/nil", "field", field)
		}
	}

	// Process unlock overrides
	for _, field := range req.Updates.UnlockOverrides {
		entry := state[field]
		entry.OverrideLocked = false
		entry.UpdatedAt = now
		state[field] = entry
	}

	// Save to database
	updatedBook, err := svc.store.UpdateBook(id, payload.Book)
	if err != nil {
		return nil, err
	}

	// Save metadata state
	if err := svc.saveMetadataState(id, state); err != nil {
		slog.Info("[ERROR] UpdateAudiobook failed to save metadata state", "err", err)
		return nil, fmt.Errorf("failed to persist metadata state")
	}

	svc.InvalidateBookCaches()

	// Enrich response with resolved names
	if resolvedAuthorName != "" && updatedBook.AuthorID != nil {
		updatedBook.Author = &database.Author{ID: *updatedBook.AuthorID, Name: resolvedAuthorName}
	}
	if resolvedSeriesName != "" && updatedBook.SeriesID != nil {
		updatedBook.Series = &database.Series{ID: *updatedBook.SeriesID, Name: resolvedSeriesName, AuthorID: payload.AuthorID}
	}

	return updatedBook, nil
}

// DeleteAudiobookOptions contains options for deleting an audiobook
type DeleteAudiobookOptions struct {
	SoftDelete bool
	BlockHash  bool
}

// DeleteAudiobook deletes an audiobook (soft or hard delete)
func (svc *AudiobookService) DeleteAudiobook(ctx context.Context, id string, opts *DeleteAudiobookOptions) (map[string]any, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	if opts == nil {
		opts = &DeleteAudiobookOptions{}
	}

	// Get the book first to access its hash.
	// PebbleStore returns nil, nil for a not-found book; check both.
	book, err := svc.store.GetBookByID(id)
	if err != nil || book == nil {
		return nil, fmt.Errorf("audiobook not found")
	}

	// If soft delete requested, mark for deletion instead of hard delete
	if opts.SoftDelete {
		if (book.MarkedForDeletion != nil && *book.MarkedForDeletion) ||
			(book.LibraryState != nil && strings.EqualFold(*book.LibraryState, "deleted")) {
			return nil, fmt.Errorf("audiobook already soft deleted")
		}

		now := time.Now()
		book.MarkedForDeletion = boolPtr(true)
		book.MarkedForDeletionAt = &now
		book.LibraryState = stringPtr("deleted")

		if _, err := svc.store.UpdateBook(id, book); err != nil {
			return nil, err
		}

		// Optionally block the hash
		blocked := false
		if opts.BlockHash && book.FileHash != nil && *book.FileHash != "" {
			if err := svc.store.AddBlockedHash(*book.FileHash, "User deleted - soft delete"); err != nil {
				slog.Warn("failed to block hash during soft delete", "err", err)
			} else {
				blocked = true
			}
		}

		// Remove the book's tracks from iTunes. Soft-delete is treated
		// as "user no longer wants this in their library" and the
		// iTunes side should reflect that immediately.
		svc.enqueueITunesRemovesForBook(id, book)

		svc.InvalidateBookCaches()
		return map[string]any{
			"message":     "audiobook soft deleted",
			"blocked":     blocked,
			"soft_delete": true,
		}, nil
	}

	// Hard delete path
	// Optionally block the hash before deleting
	blocked := false
	if opts.BlockHash && book.FileHash != nil && *book.FileHash != "" {
		if err := svc.store.AddBlockedHash(*book.FileHash, "User deleted - prevent reimport"); err != nil {
			slog.Warn("failed to block hash before delete", "err", err)
			// Continue with delete even if blocking fails
		} else {
			blocked = true
		}
	}

	// Capture iTunes PIDs BEFORE the DB row vanishes so we can
	// enqueue iTunes removes after the hard delete succeeds.
	var itunesPIDs []string
	if svc.itunesEnqueuer != nil {
		itunesPIDs = svc.collectITunesPIDsForBook(id, book)
	}

	if err := svc.store.DeleteBook(id); err != nil {
		if err.Error() == "book not found" {
			return nil, fmt.Errorf("audiobook not found")
		}
		return nil, err
	}

	if svc.itunesEnqueuer != nil {
		for _, pid := range itunesPIDs {
			svc.itunesEnqueuer.EnqueueRemove(pid)
		}
	}

	svc.InvalidateBookCaches()
	return map[string]any{
		"message": "audiobook deleted",
		"blocked": blocked,
	}, nil
}

// collectITunesPIDsForBook returns every PID stored on the book's
// book_files plus the legacy Book.ITunesPersistentID field. Used by
// hard-delete to pre-capture PIDs before the row is gone, and by the
// orphan-cleanup endpoint.
func (svc *AudiobookService) collectITunesPIDsForBook(bookID string, book *database.Book) []string {
	pids := []string{}
	files, _ := svc.store.GetBookFiles(bookID)
	for _, f := range files {
		if f.ITunesPersistentID != "" {
			pids = append(pids, f.ITunesPersistentID)
		}
	}
	if book != nil && book.ITunesPersistentID != nil && *book.ITunesPersistentID != "" {
		pids = append(pids, *book.ITunesPersistentID)
	}
	return pids
}

// enqueueITunesRemovesForBook is a soft-delete helper: it pulls the
// PIDs and enqueues each via the wired batcher. No-op if the batcher
// isn't wired.
func (svc *AudiobookService) enqueueITunesRemovesForBook(bookID string, book *database.Book) {
	if svc.itunesEnqueuer == nil {
		return
	}
	for _, pid := range svc.collectITunesPIDsForBook(bookID, book) {
		svc.itunesEnqueuer.EnqueueRemove(pid)
	}
}

// ApplyOverrideToPayload applies an override value to the update payload
func ApplyOverrideToPayload(payload *AudiobookUpdate, field string, value any) {
	switch field {
	case "title":
		if v, ok := value.(string); ok {
			payload.Title = v
		}
	case "author_name":
		if v, ok := value.(string); ok {
			payload.AuthorName = &v
		}
	case "series_name":
		if v, ok := value.(string); ok {
			payload.SeriesName = &v
		}
	case "narrator":
		if v, ok := value.(string); ok {
			payload.Narrator = stringPtr(v)
		}
	case "publisher":
		if v, ok := value.(string); ok {
			payload.Publisher = stringPtr(v)
		}
	case "language":
		if v, ok := value.(string); ok {
			payload.Language = stringPtr(v)
		}
	case "audiobook_release_year":
		switch v := value.(type) {
		case float64:
			year := int(v)
			payload.AudiobookReleaseYear = &year
		case int:
			year := v
			payload.AudiobookReleaseYear = &year
		}
	case "isbn10":
		if v, ok := value.(string); ok {
			payload.ISBN10 = stringPtr(v)
		}
	case "isbn13":
		if v, ok := value.(string); ok {
			payload.ISBN13 = stringPtr(v)
		}
	case "asin":
		if v, ok := value.(string); ok {
			payload.ASIN = stringPtr(v)
		}
	}
}

// --- User tag service methods ---

// ListAllUserTags returns all unique user tags with usage counts.
func (svc *AudiobookService) ListAllUserTags() ([]database.TagWithCount, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	return svc.store.ListAllTags()
}

// GetBookUserTags returns all user tags for a specific book.
func (svc *AudiobookService) GetBookUserTags(bookID string) ([]string, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	return svc.store.GetBookTags(bookID)
}

// SetBookUserTags replaces all user tags on a book and returns the new set.
func (svc *AudiobookService) SetBookUserTags(bookID string, tags []string) ([]string, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	if err := svc.store.SetBookTags(bookID, tags); err != nil {
		return nil, err
	}
	svc.InvalidateBookCaches()
	return svc.store.GetBookTags(bookID)
}

// AddBookUserTag adds a single user tag to a book and returns all tags.
func (svc *AudiobookService) AddBookUserTag(bookID, tag string) ([]string, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	if err := svc.store.AddBookTag(bookID, tag); err != nil {
		return nil, err
	}
	svc.InvalidateBookCaches()
	return svc.store.GetBookTags(bookID)
}

// RemoveBookUserTag removes a single user tag from a book and returns remaining tags.
func (svc *AudiobookService) RemoveBookUserTag(bookID, tag string) ([]string, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	if err := svc.store.RemoveBookTag(bookID, tag); err != nil {
		return nil, err
	}
	svc.InvalidateBookCaches()
	return svc.store.GetBookTags(bookID)
}

// BatchUpdateUserTags applies tag additions and removals to multiple books.
// Returns the number of books successfully updated.
func (svc *AudiobookService) BatchUpdateUserTags(bookIDs []string, addTags []string, removeTags []string) (int, error) {
	if svc.store == nil {
		return 0, fmt.Errorf("database not initialized")
	}
	updated := 0
	for _, bookID := range bookIDs {
		for _, tag := range addTags {
			if err := svc.store.AddBookTag(bookID, tag); err != nil {
				slog.Warn("BatchUpdateUserTags failed to add tag to book", "tag", tag, "bookID", bookID, "err", err)
				continue
			}
		}
		for _, tag := range removeTags {
			if err := svc.store.RemoveBookTag(bookID, tag); err != nil {
				slog.Warn("BatchUpdateUserTags failed to remove tag from book", "tag", tag, "bookID", bookID, "err", err)
				continue
			}
		}
		updated++
	}
	if updated > 0 {
		svc.InvalidateBookCaches()
	}
	return updated, nil
}

// searchWithBleve parses the query via the DSL, translates to a
// Bleve native query, and returns the matching books. Per-user
// filters produced by the translator (read_status / progress_pct /
// last_played) are currently dropped here — the library-list route
// doesn't carry user state. Spec 3.6 will wire them back in at the
// handler layer once the user context is plumbed.
//
// Falls back to an empty slice (not nil) on zero matches so callers
// get consistent JSON shape.
func (svc *AudiobookService) searchWithBleve(query string, limit, offset int) ([]database.Book, error) {
	ast, err := search.ParseQuery(query)
	if err != nil {
		// Parser failure: fall back to the substring search path so
		// users still see results for simple queries the DSL parser
		// rejects (e.g. punctuation-heavy book titles).
		return svc.store.SearchBooks(query, limit, offset)
	}
	bleveQ, _, err := search.Translate(ast)
	if err != nil {
		return svc.store.SearchBooks(query, limit, offset)
	}
	hits, _, err := svc.searchIndex.SearchNative(bleveQ, offset, limit)
	if err != nil {
		return nil, fmt.Errorf("bleve search: %w", err)
	}
	books := make([]database.Book, 0, len(hits))
	for _, h := range hits {
		b, _ := svc.store.GetBookByID(h.BookID)
		if b != nil {
			books = append(books, *b)
		}
	}
	return books, nil
}
