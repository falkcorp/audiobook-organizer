// file: internal/scanner/override_guard.go
// version: 1.0.0
// guid: c7c82b95-8fdb-4d7e-9787-b7f136df7a1e
// last-edited: 2026-08-24

package scanner

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A rescan overlays freshly-read tag values onto the existing row
// (applyScannerFields). Until this guard existed the overlay was unconditional
// for every field the scanner derives, so a scan silently reverted metadata the
// user had curated: they fixed a title in the UI, the file's own tag still held
// the junk value, and the next scheduled scan wrote the junk back. That is the
// standing "a running scan CLOBBERS applied metadata" hazard, and it is a
// data-loss bug, not a cosmetic one -- the user's edit is gone with no record
// that a scan overwrote it.
//
// database.MetadataFieldState records per-field provenance, keyed by a field
// NAME string. The names are the vocabulary the metadata handler writes
// (handler.go's field list): title, author, narrator, publisher, publishDate,
// series, language, isbn10, isbn13, series_sequence.

// guardedField ties one Book field the scanner overlays to the field-state key
// that governs it. Only fields a user can actually lock appear here.
//
// COVERAGE IS DELIBERATELY PARTIAL, and the gaps are listed rather than left
// implicit -- a guard that looks total but is not is worse than no guard,
// because it invites the reader to stop checking:
//
//   - COVERED (7): Title, AuthorID, SeriesID, SeriesSequence, Narrator,
//     Language, Publisher. AuthorID/SeriesID are ids while their keys
//     ("author"/"series") name the entity; locking the author is taken to mean
//     "do not repoint this book at a different author", which is the same
//     intent expressed at the id level.
//
//   - NOT COVERED, no field-state key exists (5): ASIN, OpenLibraryID,
//     HardcoverID, GoogleBooksID, WorkID. These are provider identifiers with
//     no entry in the handler's field list, so today a user cannot lock them
//     and there is nothing to consult. If a key is ever added for one, add it
//     here in the same change -- otherwise the guard silently keeps skipping it.
//
//   - NOT COVERED, deliberately (9): FilePath, Format, FileHash, FileSize,
//     OriginalFileHash, OrganizedFileHash, Duration, LibraryState, Quantity.
//     These are read off the file itself; the scanner IS authoritative for them
//     and a user override would be meaningless. Guarding them would let a stale
//     lock freeze a file's real size or hash, which breaks dedup.
var guardedFieldKeys = map[string]string{
	"title":           "title",
	"author":          "author",
	"series":          "series",
	"series_sequence": "series_sequence",
	"narrator":        "narrator",
	"language":        "language",
	"publisher":       "publisher",
}

// scanFieldStateReader is the one store method this guard needs.
type scanFieldStateReader interface {
	GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error)
}

// lockedFieldsForBook returns the set of guarded field keys the user has spoken
// for, so applyScannerFields leaves them alone.
//
// FAIL CLOSED. If the field states cannot be read we cannot tell a locked field
// from an unlocked one, and the two error directions are not symmetric: guessing
// "unlocked" overwrites a user edit that cannot be recovered, while guessing
// "locked" merely leaves a tag value unapplied until the next successful scan.
// So a read error locks every guarded field for that book. Same reasoning as the
// provisional-merge guard in internal/merge.
//
// The bool reports whether the state was read successfully; callers log on false
// rather than silently treating a degraded scan as a clean one.
func lockedFieldsForBook(store scanFieldStateReader, bookID string) (map[string]bool, bool) {
	locked := make(map[string]bool, len(guardedFieldKeys))

	if store == nil || bookID == "" {
		for _, key := range guardedFieldKeys {
			locked[key] = true
		}
		return locked, false
	}

	states, err := store.GetMetadataFieldStates(bookID)
	if err != nil {
		for _, key := range guardedFieldKeys {
			locked[key] = true
		}
		return locked, false
	}

	for _, st := range states {
		// HasUserOverride only -- NOT HasProviderValue. A fetched value means a
		// provider supplied it, not that the user chose it; blocking on that
		// would make any book that ever had metadata fetched permanently immune
		// to re-tagging, so a user who fixes a file's tags could never get the
		// correction picked up. Only an explicit human act (a lock or an
		// override value) is protected here. This matches the narrower of the
		// two spellings already in the tree (title_repair.go, handler.go:1001);
		// the repair jobs additionally refuse on FetchedValue because their job
		// is only ever to fix file-derived junk, which is not this path's job.
		if st.HasUserOverride() {
			locked[st.Field] = true
		}
	}
	return locked, true
}
