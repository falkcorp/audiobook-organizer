// file: internal/audiobooks/service_types.go
// version: 1.0.0
// guid: a3f9b2c1-d4e5-6f70-8a9b-0c1d2e3f4a5b
// last-edited: 2026-06-23

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

// UpdateAudiobookRequest represents parameters for updating an audiobook
type UpdateAudiobookRequest struct {
	Updates             *AudiobookUpdate
	RawPayload          map[string]json.RawMessage
	ResolvingAuthorName string
	ResolvingSeriesName string
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

// strippedFieldNames returns the field names from a FieldFilter slice,
// for log diagnostics only.
func strippedFieldNames(ff []FieldFilter) []string {
	out := make([]string, 0, len(ff))
	for _, f := range ff {
		out = append(out, f.Field)
	}
	return out
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
