// file: internal/scanner/override_guard.go
// version: 2.0.0
// guid: c7c82b95-8fdb-4d7e-9787-b7f136df7a1e
// last-edited: 2026-09-02

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
// HISTORY (2026-08-24 .. 2026-09-02): the first version of this guard kept its
// own key list -- title, author, series, series_sequence, narrator, language,
// publisher -- and consulted the field-state rows under those names. The WRITER
// (audiobooks.UpdateAudiobook and the UI) stores author_name, series_name and
// series_position. Nothing ever wrote "author", "series" or "series_sequence",
// so for those three columns the guard was inert: a curated author, series or
// position was clobbered on every rescan while the guard, and its test (which
// iterated the guard's own list), reported success. The keys now come from
// database.UserLockableFields, the ONE vocabulary shared with the writer and
// every metafetch apply path, and the test fixture uses the writer's spelling.
//
// COVERAGE, by design:
//
//   - GUARDED: every scanner-overlaid column that has a lock key in
//     database.UserLockableFields -- Title, AuthorID, SeriesID, SeriesSequence,
//     Narrator, Language, Publisher, ASIN. The scanner never sets Genre,
//     Description, AudiobookReleaseYear, ISBN10 or ISBN13, so those keys have
//     nothing to guard here (TestApplyScannerFields_GuardsEveryLockableColumnItOverlays
//     pins that claim against the Book struct).
//
//   - NOT GUARDED, no lock key exists (4): OpenLibraryID, HardcoverID,
//     GoogleBooksID, WorkID. A user cannot lock them today. If a key is ever
//     added to the vocabulary for one, the conformance test above fails until
//     applyScannerFields guards it too.
//
//   - NOT GUARDED, deliberately (9): FilePath, Format, FileHash, FileSize,
//     OriginalFileHash, OrganizedFileHash, Duration, LibraryState, Quantity.
//     These are read off the file itself; the scanner IS authoritative for them
//     and a user override would be meaningless. Guarding them would let a stale
//     lock freeze a file's real size or hash, which breaks dedup.

// lockedFieldsForBook returns the set of lock keys (database.UserLockableFields)
// the user has spoken for on this book, so applyScannerFields leaves those
// columns alone. It is a thin adapter over database.LockedUserFields, the guard
// every other write path shares; the scanner keeps its own wrapper only because
// it cannot abort -- the file-derived columns still have to be recorded.
//
// FAIL CLOSED. If the locks cannot be read we cannot tell a locked field from an
// unlocked one, and the two error directions are not symmetric: guessing
// "unlocked" overwrites a user edit that cannot be recovered, while guessing
// "locked" merely leaves a tag value unapplied until the next successful scan.
// So a read error locks EVERY lockable field for that book.
//
// The bool reports whether the state was read successfully; callers log on false
// rather than silently treating a degraded scan as a clean one.
func lockedFieldsForBook(store database.MetadataFieldStateReader, bookID string) (map[string]bool, bool) {
	locked, err := database.LockedUserFields(store, bookID)
	if err != nil {
		return database.AllUserLockableFieldsLocked(), false
	}
	return locked, true
}
