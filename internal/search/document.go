// file: internal/search/document.go
// version: 1.1.0
// guid: 6a2d8f1c-4b3e-4f60-a7c5-2e8d0f1b9a47
// last-edited: 2026-10-18
//
// BookDocument is the flat, Bleve-indexable projection of a Book
// plus its related author, series, and tag rows. The fields here
// drive both what's searchable and how matches are scored (field
// boosts live in the mapping, not the struct).

package search

import "time"

// BookDocument is the denormalized record indexed in Bleve.
//
// Field boost policy (applied via the index mapping, not this
// struct): title 3×, author 2×, series 1.5×, narrator 1.2×,
// description 0.5×. All other text fields default boost 1.0.
// Numeric and keyword fields are stored without analysis so
// range + exact queries land on them.
type BookDocument struct {
	// Identity
	BookID string `json:"book_id"`

	// Analyzed text (English stemmer + ASCII folding)
	Title       string `json:"title,omitempty"`
	Author      string `json:"author,omitempty"`
	Narrator    string `json:"narrator,omitempty"`
	Series      string `json:"series,omitempty"`
	Publisher   string `json:"publisher,omitempty"`
	Description string `json:"description,omitempty"`
	FilePath    string `json:"file_path,omitempty"`

	// Tag names flattened for multi-value match. Each tag is indexed
	// as a keyword (case-insensitive exact). Search `tag:favorites`
	// matches if "favorites" appears in this slice.
	Tags []string `json:"tags,omitempty"`

	// Keyword (exact, case-insensitive) — no stemming
	Format       string `json:"format,omitempty"`
	Genre        string `json:"genre,omitempty"`
	Language     string `json:"language,omitempty"`
	LibraryState string `json:"library_state,omitempty"`
	ISBN10       string `json:"isbn10,omitempty"`
	ISBN13       string `json:"isbn13,omitempty"`
	ASIN         string `json:"asin,omitempty"`

	// Numeric (for range queries: year:>2000, bitrate:<128, …)
	Year          int   `json:"year,omitempty"`
	SeriesNumber  int   `json:"series_number,omitempty"`
	DurationSec   int   `json:"duration_seconds,omitempty"`
	BitrateKbps   int   `json:"bitrate_kbps,omitempty"`
	SampleRateHz  int   `json:"sample_rate_hz,omitempty"`
	Channels      int   `json:"channels,omitempty"`
	BitDepth      int   `json:"bit_depth,omitempty"`
	FileSizeBytes int64 `json:"file_size_bytes,omitempty"`

	// Dates (for relative-date queries: date_added:>-7d, date_modified:>-1y).
	// Pointers so a book with no timestamp indexes no date at all rather than
	// year 1. DateAdded is Book.CreatedAt, DateModified is Book.UpdatedAt (every
	// database write). Both are UTC. Added with mapping version 4.
	DateAdded    *time.Time `json:"date_added,omitempty"`
	DateModified *time.Time `json:"date_modified,omitempty"`

	// Boolean flags (for `has_cover:true`-style queries)
	HasCover bool `json:"has_cover,omitempty"`

	// Type marker lets Bleve's type field disambiguate documents if
	// we later index authors/series/playlists in the same index.
	Type string `json:"_type"`
}

// BookDocType is the value placed in BookDocument.Type.
const BookDocType = "book"
