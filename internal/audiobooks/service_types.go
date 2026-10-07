// file: internal/audiobooks/service_types.go
// version: 1.9.0
// guid: a3f9b2c1-d4e5-6f70-8a9b-0c1d2e3f4a5b
// last-edited: 2026-10-06

package audiobooks

import (
	"encoding/json"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

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
	Attempted    int `json:"attempted"`
	Purged       int `json:"purged"`
	FilesDeleted int `json:"files_deleted"`
	// SkippedOwnsFiles counts books left soft-deleted because they still own
	// book_file rows: hard-deleting them would orphan those rows
	// (database.ErrBookOwnsFiles). They are named in SkippedOwnsFilesIDs, NOT
	// in Errors: a merge loser keeps its files for good, so it is skipped on
	// every run, and repeating one error line per book per run would bury the
	// real errors. The nightly job reports the count once per run.
	SkippedOwnsFiles    int      `json:"skipped_owns_files"`
	SkippedOwnsFilesIDs []string `json:"skipped_owns_files_ids,omitempty"`
	// The next three count the books a user still has listening state on
	// (merge.BookHasCarryableUserState: a position, a status, progress,
	// listened time or the hide flag). Such a book is never purged with its
	// state on it. Each count is named in its *IDs list and reported once
	// per run like SkippedOwnsFiles.
	//
	// CarriedToSibling: the state was carried, all or nothing, to a member
	// of the book's version group that Audiobookshelf lists, and the book
	// was then purged. These are also counted in Purged.
	CarriedToSibling    int      `json:"carried_to_sibling"`
	CarriedToSiblingIDs []string `json:"carried_to_sibling_ids,omitempty"`
	// KeptHasProgress: no version-group sibling Audiobookshelf lists exists
	// (a hidden copy -- not primary, not organized, quarantined -- never
	// receives the state, or it would be stranded out of sight), so the book
	// stays in the trash, shown as "has progress", until the owner restores
	// it or discards its progress (DiscardProgressAndPurge).
	KeptHasProgress    int      `json:"kept_has_progress"`
	KeptHasProgressIDs []string `json:"kept_has_progress_ids,omitempty"`
	// CarryFailed: a sibling exists but the carry did not complete (or the
	// sibling could not be looked up). Fail closed: every user's state is
	// back on the book and it stays in the trash; the reason is in Errors.
	CarryFailed    int      `json:"carry_failed"`
	CarryFailedIDs []string `json:"carry_failed_ids,omitempty"`
	Errors         []string `json:"errors"`
}

// AudiobookUpdate represents a partial update to an audiobook
type AudiobookUpdate struct {
	*database.Book
	AuthorName      *string                    `json:"author_name,omitempty"`
	SeriesName      *string                    `json:"series_name,omitempty"`
	Overrides       map[string]OverridePayload `json:"overrides,omitempty"`
	UnlockOverrides []string                   `json:"unlock_overrides,omitempty"`

	// ClearSeries asks for the book's series link to be removed, the same
	// as a SeriesName that trims to "". The JSON form is `"series_id": null`,
	// which SeriesID (an *int) cannot carry: nil there means "not sent".
	ClearSeries bool `json:"-"`
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
	// Quoted is true when the user wrote the value in double quotes
	// (title:"a*b"). A quoted value is a literal substring: no regex, glob,
	// or comparison. See internal/querygrammar.
	Quoted bool `json:"quoted,omitempty"`
}

// ListFilters holds optional filters for listing audiobooks.
type ListFilters struct {
	IsPrimaryVersion *bool
	// ExcludeQuarantined drops quarantined books during the indexed scan so
	// pagination counts the post-quarantine set (a 500-page returns 500
	// non-quarantined books, and the total count matches). Set by the HTTP
	// layer from the inverse of ?show_quarantined.
	ExcludeQuarantined bool
	LibraryState       string
	Tag                string
	Tags               []string
	SortBy             string        // column sort key
	SortOrder          string        // "asc" or "desc"
	FieldFilters       []FieldFilter // advanced field-specific filters (book-global)
	PerUserFilters     []FieldFilter // per-user filters (read_status, progress_pct, last_played)
	UserID             string        // caller's user ID; required for PerUserFilters
	// Fingerprinting filters
	FingerprintStatus  string // "none", "partial", "complete", or "" for any
	CoveragePercentMin *int   // minimum coverage percentage (inclusive)
	CoveragePercentMax *int   // maximum coverage percentage (inclusive)
	// RestrictToIDs, when non-nil, limits the result to books whose ID is in
	// the set. It is ANDed with every other predicate, including the tag
	// intersection, so a caller can narrow an ID-shaped query (the
	// has_file_errors and quick-query fast paths) without losing the search,
	// field filters, sort or pagination the same request asked for.
	//
	// nil and empty are DIFFERENT, matching database.BookSummaryFilter: nil
	// means "no ID restriction", while non-nil-but-empty means "no book is
	// eligible" and correctly yields an empty page with a count of 0. Never
	// test this with len() alone.
	RestrictToIDs map[string]struct{}
}

// UpdateAudiobookRequest represents parameters for updating an audiobook
//
// RawPayload is the request body's top-level keys. It is what Sent reads, so
// a request built by hand must list in it every field it means to change:
// Updates holds parsed values only, and a field present there but absent from
// RawPayload (and from Overrides) is treated as not sent.
type UpdateAudiobookRequest struct {
	Updates             *AudiobookUpdate
	RawPayload          map[string]json.RawMessage
	ResolvingAuthorName string
	ResolvingSeriesName string
}

// Sent reports whether the client named field (a payload key, which is also
// its lock key): as a top-level key, or as an override that carries a value.
// An override that only clears or (un)locks the lock changes no field, so it
// does not count. UpdateAudiobook makes every "did the client send this"
// decision through it.
func (r *UpdateAudiobookRequest) Sent(field string) bool {
	if r == nil {
		return false
	}
	if _, ok := r.RawPayload[field]; ok {
		return true
	}
	if r.Updates != nil {
		if o, ok := r.Updates.Overrides[field]; ok && len(o.Value) > 0 && !o.Clear {
			return true
		}
	}
	return false
}

// DeleteAudiobookOptions contains options for deleting an audiobook
type DeleteAudiobookOptions struct {
	SoftDelete bool
	BlockHash  bool
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

// strippedCompiledFieldNames returns the field names from a compiled filter
// slice, for log diagnostics only.
func strippedCompiledFieldNames(ff []compiledFilter) []string {
	out := make([]string, 0, len(ff))
	for _, f := range ff {
		out = append(out, f.Field)
	}
	return out
}
