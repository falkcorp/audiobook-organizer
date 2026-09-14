// file: internal/metafetch/fill_only.go
// version: 1.2.0
// guid: 9b4e2c71-5a3d-4f08-b6e1-2d7c8a90f513
// last-edited: 2026-09-14

package metafetch

import (
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// StripFilledFields is the fill-only apply policy (owner decision A3#3,
// 2026-09-13): batch applies and auto-fetch FILL the book's descriptive fields,
// they never overwrite one that already holds a value.
//
// After the owner ruling of 2026-09-14, the paths that may OVERWRITE are:
//   - the single-book apply (hand-picked in the apply dialog);
//   - a batch row the owner approved in the review lane (a matching row pin),
//     whether the gate passed it or the approval lifted a refusal.
//
// The paths that stay FILL-ONLY are: batch rows with no pin or a pin not from
// a review row (Apply page, Apply high confidence, Apply selected, scripts),
// /metadata/batch-apply-candidates, auto-fetch, the upgrade job, and
// maintenance.auto-match-transcribed. That last one writes only title and
// author, which this function does not strip; it drops each filled one from
// its fields allowlist instead, so it can only fill an empty title or author.
//
// It blanks every descriptive field of meta whose book column is already
// filled, so applyMetadataUnguarded (and previewFields, which mirrors it)
// cannot write it, and returns the keys it blanked. Run it after
// StripLockedFields' input is built and before the apply body, on the same
// meta both the apply and the preview use.
//
// The IDENTITY fields -- title, author (add-only already), series, series
// position, ASIN, ISBN-10/13 -- are not touched: they are what the apply gate
// judged the match on, and a batch apply exists to correct them. print_year is
// already fill-only in the apply body.
//
// Until this, applyMetadataUnguarded overwrote every descriptive field on every
// path: IsBetterValue/IsBetterStringPtr return true whenever both values are
// real, and cover, description, genre, subtitle, page count, secondary series,
// runtime and the audiobook release year were assigned unconditionally.
func StripFilledFields(book *database.Book, meta metadata.BookMetadata) (metadata.BookMetadata, []string) {
	if book == nil {
		return meta, nil
	}
	var stripped []string
	strip := func(key string) { stripped = append(stripped, key) }

	if meta.Publisher != "" && filledString(book.Publisher) {
		meta.Publisher = ""
		strip("publisher")
	}
	if meta.Language != "" && filledString(book.Language) {
		meta.Language = ""
		strip("language")
	}
	if meta.PublishYear != 0 && meta.PublishYearIsAudiobookRelease && filledInt(book.AudiobookReleaseYear) {
		meta.PublishYear = 0
		strip("audiobook_release_year")
	}
	if meta.CoverURL != "" && filledString(book.CoverURL) {
		meta.CoverURL = ""
		strip("cover_url")
	}
	if meta.Narrator != "" && filledString(book.Narrator) {
		meta.Narrator = ""
		strip("narrator")
	}
	if meta.Description != "" && filledString(book.Description) {
		meta.Description = ""
		strip("description")
	}
	if meta.Genre != "" && filledString(book.Genre) {
		meta.Genre = ""
		strip("genre")
	}
	if meta.Abridged != nil && book.Abridged != nil {
		meta.Abridged = nil
		strip("abridged")
	}
	if meta.Subtitle != "" && filledString(book.Subtitle) {
		meta.Subtitle = ""
		strip("subtitle")
	}
	if meta.PageCount > 0 && filledInt(book.PageCount) {
		meta.PageCount = 0
		strip("page_count")
	}
	// The secondary position belongs to the secondary series: keep or drop
	// them together, so a position is never pinned onto a different series.
	if meta.SeriesSecondary != "" && filledString(book.SeriesSecondary) {
		meta.SeriesSecondary = ""
		meta.SeriesSecondaryPosition = ""
		strip("series_secondary")
	}
	if meta.DurationSec > 0 && filledInt(book.AudibleRuntimeMin) {
		meta.DurationSec = 0
		strip("audible_runtime_min")
	}
	return meta, stripped
}

// filledString reports whether a book column holds a real value. A garbage
// placeholder ("Unknown", "N/A", ...) counts as empty, so a fill may replace it.
func filledString(p *string) bool {
	return p != nil && strings.TrimSpace(*p) != "" && !IsGarbageValue(*p)
}

// filledInt reports whether a book number column holds a positive value.
func filledInt(p *int) bool {
	return p != nil && *p > 0
}
