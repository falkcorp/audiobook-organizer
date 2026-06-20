// file: internal/metadata/taglib_reader.go
// version: 1.1.0
// guid: 9e8d7c6b-5a4f-3e2d-1c0b-9a8b7c6d5e4f

package metadata

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// BuildMetadataFromTaglibMap takes the flat key → values map returned by
// TagLib (both the CGO and WASM paths produce the same shape) and folds
// it into a Metadata struct using the same field-priority rules as the
// dhowden-based BuildMetadataFromTag. This exists so the taglib fallback
// path in ExtractMetadata can produce a result compatible with everything
// downstream — scanner, dedup, organize — without anyone needing to know
// which parser ultimately read the file.
//
// The key matching is intentionally forgiving: TagLib emits uppercase keys
// by default but individual formats can deliver lowercase or mixed-case
// variants (Vorbis comments especially), so we normalize to uppercase on
// lookup.
func BuildMetadataFromTaglibMap(tags map[string][]string, filePath string, metaLog logger.Logger) Metadata {
	if metaLog == nil {
		metaLog = logger.New("metadata")
	}
	// Normalize all keys to uppercase so lookups are case-insensitive.
	norm := make(map[string][]string, len(tags))
	for k, v := range tags {
		norm[strings.ToUpper(k)] = v
	}
	get := func(keys ...string) string {
		for _, k := range keys {
			if vs, ok := norm[strings.ToUpper(k)]; ok {
				for _, v := range vs {
					if cleaned := cleanTagValue(v); cleaned != "" {
						return cleaned
					}
				}
			}
		}
		return ""
	}

	var metadata Metadata

	metadata.Album = get("ALBUM")
	metadata.Title = get("TITLE")
	if metadata.Title == "" && metadata.Album != "" {
		metadata.Title = metadata.Album
	}
	if metadata.Title == "" {
		metadata.Title = strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	}

	// Author priority: ALBUMARTIST > ARTIST > COMPOSER (composer is
	// narrator in audiobooks, so it's a last-ditch fallback).
	albumArtist := get("ALBUMARTIST", "ALBUM_ARTIST", "ALBUM ARTIST")
	artist := get("ARTIST")
	composer := get("COMPOSER")
	authorFromArtist := false
	switch {
	case albumArtist != "":
		metadata.Artist = albumArtist
		metadata.AuthorSource = "taglib.albumartist"
	case artist != "":
		metadata.Artist = artist
		metadata.AuthorSource = "taglib.artist"
		authorFromArtist = true
	case composer != "":
		metadata.Artist = composer
		metadata.AuthorSource = "taglib.composer"
	}

	metadata.Genre = get("GENRE")

	// Narrator: dedicated fields first, then fall back to artist if
	// artist wasn't already used for the author slot.
	metadata.Narrator = get("NARRATOR", "PERFORMER", "READER", "TXXX:NARRATOR", "TXXX:PERFORMER")
	if metadata.Narrator == "" && !authorFromArtist && artist != "" && artist != metadata.Artist {
		metadata.Narrator = artist
	}

	metadata.Language = get("LANGUAGE")
	metadata.Publisher = get("PUBLISHER", "LABEL")
	metadata.Comments = get("DESCRIPTION", "COMMENT")

	// Year. TagLib gives a DATE property; the dhowden path understands
	// "YYYY", "YYYY-MM-DD", and year-in-noise formats, so mirror that.
	if dateStr := get("DATE", "YEAR", "ORIGINALDATE"); dateStr != "" {
		if y, err := strconv.Atoi(extractYearDigits(dateStr)); err == nil {
			metadata.Year = y
		}
	}

	// Track / disc position (for chapter ordering + album grouping).
	metadata.TrackNumber, metadata.TrackTotal = parseSlashPair(get("TRACKNUMBER", "TRACK", "TRCK"))
	metadata.DiscNumber, metadata.DiscTotal = parseSlashPair(get("DISCNUMBER", "DISC", "TPOS"))
	// Grouping (box-set/anthology divider).
	metadata.Grouping = get("GROUPING", "GRP1", "TIT1", "CONTENTGROUP")

	// Series and series index. Custom audiobook tags come first, then
	// the movement tags iTunes uses, then fall back to splitting the
	// album name on " - ".
	metadata.Series = get("SERIES", "SERIESNAME", "SERIES_NAME", "TXXX:SERIES", "MOVEMENTNAME", "MOVEMENT")
	if idxStr := get("SERIES_INDEX", "SERIESINDEX", "TXXX:SERIES_INDEX", "MOVEMENTNUMBER", "MOVEMENT_NUMBER"); idxStr != "" {
		if idx, err := strconv.Atoi(strings.TrimSpace(idxStr)); err == nil && idx > 0 {
			metadata.SeriesIndex = idx
		}
	}
	if metadata.Series == "" && strings.Contains(metadata.Album, " - ") {
		parts := strings.Split(metadata.Album, " - ")
		if len(parts) > 1 {
			metadata.Series = strings.TrimSpace(parts[0])
		}
	}
	if metadata.Series == "" && metadata.Comments != "" {
		if extracted := extractSeriesFromComments(metadata.Comments); extracted != "" {
			metadata.Series = extracted
		}
	}
	if metadata.Series == "" {
		if series, idx := extractSeriesFromVolumeString(metadata.Album); series != "" {
			metadata.Series = series
			if metadata.SeriesIndex == 0 && idx > 0 {
				metadata.SeriesIndex = idx
			}
		}
	}
	if metadata.Series == "" {
		if series, idx := extractSeriesFromVolumeString(metadata.Title); series != "" {
			metadata.Series = series
			if metadata.SeriesIndex == 0 && idx > 0 {
				metadata.SeriesIndex = idx
			}
		}
	}
	if metadata.SeriesIndex == 0 {
		metadata.SeriesIndex = DetectVolumeNumber(metadata.Title)
	}

	// External IDs.
	metadata.ISBN10 = normalizeISBN(get("ISBN10", "TXXX:ISBN10"))
	metadata.ISBN13 = normalizeISBN(get("ISBN13", "TXXX:ISBN13", "ISBN"))
	metadata.ASIN = get("ASIN", "TXXX:ASIN")
	metadata.OpenLibraryID = get("OPEN_LIBRARY_ID", "OPENLIBRARYID", "TXXX:OPEN_LIBRARY_ID")
	metadata.HardcoverID = get("HARDCOVER_ID", "HARDCOVERID", "TXXX:HARDCOVER_ID")
	metadata.GoogleBooksID = get("GOOGLE_BOOKS_ID", "GOOGLEBOOKSID", "TXXX:GOOGLE_BOOKS_ID")

	// Custom AUDIOBOOK_ORGANIZER_* round-trip tags.
	metadata.BookOrganizerID = get("AUDIOBOOK_ORGANIZER_BOOK_ID", "TXXX:AUDIOBOOK_ORGANIZER_BOOK_ID")
	metadata.OrganizerTagVersion = get("AUDIOBOOK_ORGANIZER_TAG_VERSION", "TXXX:AUDIOBOOK_ORGANIZER_TAG_VERSION")
	metadata.Edition = get("EDITION", "TXXX:EDITION")
	metadata.PrintYear = get("PRINT_YEAR", "PRINTYEAR", "TXXX:PRINT_YEAR")

	metaLog.Debug("taglib fallback read %d tag keys from %s", len(norm), filePath)
	return metadata
}

// normalizeISBN strips hyphens and whitespace from an ISBN-like string.
// Returns "" if the result isn't 10 or 13 digits (optionally with trailing X).
func normalizeISBN(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= '0' && r <= '9') || r == 'X' || r == 'x' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) != 10 && len(out) != 13 {
		return ""
	}
	return out
}
