// file: internal/metadata/taglib_tagmap.go
// version: 1.3.0
// guid: 8b9c0d1e-2f3a-4b5c-6d7e-8f9a0b1c2d3e
//
// Shared tag map builder used by both WASM and CGO taglib writers.

package metadata

import (
	"fmt"
	"strconv"
	"strings"
)

// positiveIntTag renders a metadata value that is conceptually a positive
// integer, accepting whichever concrete type the caller happened to use.
//
// WHY THIS EXISTS. "year" and "series_index" were read with a bare
// `.(int)` assertion. Callers do not agree on the type — the review/apply path
// builds its tag map with string values, and metadata decoded from JSON arrives
// as float64 — so for those callers the assertion failed, the tag was silently
// dropped, and the file never received it.
//
// That silence is self-sustaining, and it is the same defect that hid in "track"
// until 2026-08-15: the unchanged-tag filter compares the value it wants against
// the value on disk, finds it absent, decides a write is needed, and this
// function then discards it. Next run, identical. The write "succeeds" every
// time and the value never lands, so the file is rewritten in full forever.
//
// series_index was measured doing exactly this in production on 2026-08-16.
// "year" is the more dangerous of the two because it IS mapped in the filter's
// currentVals, so it loops without even logging the unmapped-key warning.
//
// Returns ok=false for zero, negative, unparseable or absent values, which
// leaves the tag out entirely — the correct conservative behaviour.
func positiveIntTag(v any) (string, bool) {
	switch n := v.(type) {
	case int:
		if n > 0 {
			return strconv.Itoa(n), true
		}
	case int32:
		if n > 0 {
			return strconv.FormatInt(int64(n), 10), true
		}
	case int64:
		if n > 0 {
			return strconv.FormatInt(n, 10), true
		}
	case float64: // JSON numbers decode to float64
		if n > 0 {
			return strconv.FormatInt(int64(n), 10), true
		}
	case float32:
		if n > 0 {
			return strconv.FormatInt(int64(n), 10), true
		}
	case string:
		s := strings.TrimSpace(n)
		if s == "" {
			return "", false
		}
		// Tolerate a decimal string ("4.0") as well as a plain integer;
		// series positions in particular arrive in both shapes.
		if f, err := strconv.ParseFloat(s, 64); err == nil && f > 0 {
			return strconv.FormatInt(int64(f), 10), true
		}
	}
	return "", false
}

// buildWriteTagMap converts metadata map[string]interface{} to the
// map[string][]string format that TagLib's property API expects.
func buildWriteTagMap(metadata map[string]any) map[string][]string {
	tags := make(map[string][]string)

	if title, ok := metadata["title"].(string); ok && title != "" {
		tags["TITLE"] = []string{title}
	}
	if artist, ok := metadata["artist"].(string); ok && artist != "" {
		tags["ALBUMARTIST"] = []string{artist}
		tags["ARTIST"] = []string{artist}
		tags["COMPOSER"] = []string{""}
	}
	if album, ok := metadata["album"].(string); ok && album != "" {
		tags["ALBUM"] = []string{album}
	}
	if genre, ok := metadata["genre"].(string); ok && genre != "" {
		tags["GENRE"] = []string{genre}
	}
	if year, ok := positiveIntTag(metadata["year"]); ok {
		tags["DATE"] = []string{year}
	}
	if narrator, ok := metadata["narrator"].(string); ok && narrator != "" {
		tags["PERFORMER"] = []string{narrator}
		tags["NARRATOR"] = []string{narrator}
	}
	if lang, ok := metadata["language"].(string); ok && lang != "" {
		tags["LANGUAGE"] = []string{lang}
	}
	if pub, ok := metadata["publisher"].(string); ok && pub != "" {
		tags["LABEL"] = []string{pub}
		tags["PUBLISHER"] = []string{pub}
	}
	if isbn10, ok := metadata["isbn10"].(string); ok && isbn10 != "" {
		tags["ISBN10"] = []string{isbn10}
	}
	if isbn13, ok := metadata["isbn13"].(string); ok && isbn13 != "" {
		tags["ISBN13"] = []string{isbn13}
	}
	if desc, ok := metadata["description"].(string); ok && desc != "" {
		tags["DESCRIPTION"] = []string{desc}
		tags["COMMENT"] = []string{desc}
	}
	if series, ok := metadata["series"].(string); ok && series != "" {
		tags["SERIES"] = []string{series}
		tags["GROUPING"] = []string{series}
		if _, hasAlbum := metadata["album"]; !hasAlbum {
			tags["ALBUM"] = []string{""}
		}
	}
	if si, ok := positiveIntTag(metadata["series_index"]); ok {
		tags["SERIES_INDEX"] = []string{si}
	}
	// TRACKNUMBER is what the reader looks for first (see taglib_reader.go's
	// parseSlashPair over TRACKNUMBER/TRACK/TRCK), so an "n/total" value written
	// here round-trips into Metadata.TrackNumber / Metadata.TrackTotal.
	//
	// This case did not exist, and its absence was self-sustaining: the
	// multi-file write path always emits a "track" entry, the unchanged-tag
	// filter had no mapping for it and so always wrote, and then THIS function
	// silently dropped it — so the file never actually received a track tag, the
	// next read found none, and the next run wrote again. Every multi-file book
	// rewrote every one of its files on every write-back run, permanently, and
	// the tag it was rewriting for never landed. Mapping the tag in the filter
	// alone does not fix it; the value has to actually reach the file.
	//
	// Accept the int form too: callers that number tracks without a total pass
	// an int rather than the "n/total" string.
	switch track := metadata["track"].(type) {
	case string:
		if track != "" {
			tags["TRACKNUMBER"] = []string{track}
		}
	case int:
		if track > 0 {
			tags["TRACKNUMBER"] = []string{fmt.Sprintf("%d", track)}
		}
	}

	// Custom AUDIOBOOK_ORGANIZER_* tags
	customPairs := [][2]string{
		{TagBookID, "book_id"}, {TagISBN10, "isbn10"}, {TagISBN13, "isbn13"},
		{TagASIN, "asin"}, {TagOpenLibrary, "open_library_id"},
		{TagHardcover, "hardcover_id"}, {TagGoogleBooks, "google_books_id"},
		{TagEdition, "edition"}, {TagPrintYear, "print_year"},
	}
	for _, pair := range customPairs {
		if val, ok := metadata[pair[1]].(string); ok && val != "" {
			tags[pair[0]] = []string{val}
		}
	}
	tags[TagVersion] = []string{CustomTagVersion}

	return tags
}
