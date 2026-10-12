// file: internal/deluge/mark_in_place.go
// version: 1.0.0
// guid: 8ee900f5-372f-426f-a797-2f6d18be3aa1
// last-edited: 2026-10-11

package deluge

import (
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// BookFileModifier is the one store call MarkImportedInPlace makes.
type BookFileModifier interface {
	ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
}

// InPlaceOutcome is what MarkImportedInPlace did.
type InPlaceOutcome int

const (
	// InPlaceMarked: the row was marked imported.
	InPlaceMarked InPlaceOutcome = iota
	// InPlaceAlreadyMarked: another writer marked it first; nothing written.
	InPlaceAlreadyMarked
	// InPlaceMoved: the row no longer names path; nothing written.
	InPlaceMoved
	// InPlaceGone: the row no longer exists; nothing written.
	InPlaceGone
)

// MarkImportedInPlace records that a pending book file is already at its
// library destination: the importer resolved the destination to the file's
// own path, so there is nothing to copy. It sets ImportedFromDelugeAt and
// nothing else -- no DelugeOriginalPath (the file never left a Deluge save
// path), no path change, no move -- under the row's lock, and only while the
// row still names path and is unmarked.
//
// Without it the row stays on GetBookFilesNeedingDelugeImportCore forever.
// ImportToLibraryWith has marked such a row since 2026-09-14;
// deluge.centralize and maintenance.bulk-deluge-import skipped it and wrote
// nothing, so every run listed it again (bulk import counting it "imported"
// each time). A row deluge.link-backfill links is exactly this case: it is in
// the library and carries a DelugeHash, with ImportedFromDelugeAt unset.
//
// A write whose fsync failed (database.ErrBookFileDurabilityUnknown) was
// applied, so it is logged and reported as InPlaceMarked.
func MarkImportedInPlace(store BookFileModifier, bookID, fileID, path string, now time.Time) (InPlaceOutcome, error) {
	if store == nil {
		return InPlaceGone, fmt.Errorf("MarkImportedInPlace: store is nil")
	}
	got := InPlaceGone
	row, err := store.ModifyBookFile(bookID, fileID, func(bf *database.BookFile) error {
		switch {
		case bf.FilePath != path:
			got = InPlaceMoved
			return database.ErrSkipBookFileWrite
		case bf.ImportedFromDelugeAt != nil:
			got = InPlaceAlreadyMarked
			return database.ErrSkipBookFileWrite
		}
		t := now
		bf.ImportedFromDelugeAt = &t
		got = InPlaceMarked
		return nil
	})
	switch {
	case errors.Is(err, database.ErrBookFileDurabilityUnknown):
		importLog.Error("MarkImportedInPlace: marking %s imported was written but its fsync failed (%v): durability unknown; keeping it",
			logger.SanitizeLogValue(fileID), err)
		return got, nil
	case err != nil:
		return got, fmt.Errorf("MarkImportedInPlace: mark book file %s imported at %s: %w", fileID, path, err)
	case row == nil:
		return InPlaceGone, nil
	}
	return got, nil
}
